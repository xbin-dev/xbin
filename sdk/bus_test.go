package xbin

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
)

// Subscribe / Unsubscribe register this component's bus push subscription
// through the gateway (xbind scopes it to the caller).
func TestSubscribe(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	type call struct{ method, path, body string }
	var mu sync.Mutex
	var calls []call
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, call{r.Method, r.URL.EscapedPath(), string(b)})
		mu.Unlock()
		if r.Method == "DELETE" {
			http.Error(w, `{"error":"no such subscription"}`, http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	})}
	go srv.Serve(ln)
	defer srv.Close()
	t.Setenv("XBIN_GATEWAY", sock)
	t.Setenv("XBIN_TOKEN", "tok")
	clientOnce = sync.Once{}
	defer func() { clientOnce = sync.Once{} }()

	if err := Subscribe("deploys", "res:apps/ci/bus", "deploy/", "/on-deploy"); err != nil {
		t.Fatal(err)
	}
	if err := Unsubscribe("deploys"); err == nil {
		t.Fatal("a 404 must be an error")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0].method != "PUT" || calls[0].path != "/api/xbin/bus/subscriptions" ||
		calls[1].method != "DELETE" || calls[1].path != "/api/xbin/bus/subscriptions/deploys" {
		t.Fatalf("calls: %+v", calls)
	}
	var body map[string]string
	_ = json.Unmarshal([]byte(calls[0].body), &body)
	if body["name"] != "deploys" || body["resource"] != "res:apps/ci/bus" || body["prefix"] != "deploy/" || body["path"] != "/on-deploy" {
		t.Fatalf("body: %v", body)
	}
}
