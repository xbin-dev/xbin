package boot

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/push"
)

// The push plane through a real boot: the routes are mounted behind the
// auth middleware, the environment configures the relay, a tile's frame
// notifies the owner, and the sealed envelope that reaches the (fake) relay
// opens with the device's key.
func TestPushInProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	quiet(t)
	var mu sync.Mutex
	var got []push.Envelope
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Handle   string        `json:"handle"`
			Envelope push.Envelope `json:"envelope"`
		}
		if r.URL.Path != "/v1/push" || r.Header.Get("Authorization") != "Bearer k-test" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Handle != "handle-owner-1" {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		got = append(got, body.Envelope)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer relay.Close()

	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(ws, ln)
	cfg.PushRelay, cfg.PushRelayKey = relay.URL, "k-test"
	ready := make(chan string, 1)
	cfg.Ready = func(addr string) { ready <- addr }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("boot did not stop")
		}
	}()
	var base string
	select {
	case addr := <-ready:
		base = "http://" + addr + "/api/xbin"
	case err := <-done:
		t.Fatalf("boot ended: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("boot never served")
	}
	call := func(method, path string, hdr map[string]string, body any) (int, map[string]any) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(b))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// --no-auth: an unauthenticated request is the owner
	if code, out := call("GET", "/push/config", nil, nil); code != 200 || out["enabled"] != true || out["source"] != "env" {
		t.Fatalf("config: %d %v", code, out)
	}
	dev, _ := ecdh.X25519().GenerateKey(nil)
	code, out := call("POST", "/devices/push", nil, map[string]any{"deviceId": "iphone", "handle": "handle-owner-1",
		"publicKey": base64.RawURLEncoding.EncodeToString(dev.PublicKey().Bytes())})
	if code != 200 {
		t.Fatalf("register: %d %v", code, out)
	}
	wsID := out["workspace"].(string)

	// a tile's frame (the root shell tile) notifies the owner
	code, out = call("GET", "/frame-token?component=root", nil, nil)
	if code != 200 || out["token"] == "" {
		t.Fatalf("frame token: %d %v", code, out)
	}
	frame := map[string]string{"X-XBin-Frame-Token": out["token"].(string)}
	if code, out := call("POST", "/devices/push", frame, map[string]any{"deviceId": "x", "handle": "handle-x-12345", "publicKey": "AAAA"}); code != 403 {
		t.Fatalf("a tile registered a device: %d %v", code, out)
	}
	if code, out := call("POST", "/notify", nil, map[string]any{"user": "owner", "title": "x"}); code != 403 {
		t.Fatalf("a person called notify: %d %v", code, out)
	}
	if code, out := call("POST", "/notify", frame, map[string]any{"user": "owner", "title": "Build green", "body": "all 12 passed", "link": "#runs/7"}); code != 202 {
		t.Fatalf("notify: %d %v", code, out)
	}
	var env push.Envelope
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		n := len(got)
		if n > 0 {
			env = got[0]
		}
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("nothing reached the relay")
		}
	}
	pt, err := push.Open(dev, env)
	if err != nil {
		t.Fatal(err)
	}
	var p push.Payload
	if err := json.Unmarshal(pt, &p); err != nil {
		t.Fatal(err)
	}
	if p != (push.Payload{V: 1, WS: wsID, Kind: push.KindTile, Title: "Build green", Body: "all 12 passed", Link: "c/root/#runs/7"}) {
		t.Fatalf("payload %+v", p)
	}
	if code, _ := call("PUT", "/push/config", nil, map[string]any{"relay": "https://elsewhere.example"}); code != 409 {
		t.Fatalf("env-configured relay changed through the API: %d", code)
	}
}
