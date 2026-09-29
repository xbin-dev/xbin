//go:build linux && integration

package tilesbx

// The ports capability (D135) against live tile sandboxes: a server the
// probe runs in the sandbox (sbxprobe serve), reached through
// ANY /sandboxes/{name}/ports/{port}/{path...} over a real HTTP server —
// plain requests, a body, an escaped path, stripped credentials both ways,
// and a WebSocket echo — in a namespace sandbox (the kernel overlay) and a
// VM (KVM, or XBIN_VM_ACCEL=emulate).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/sdk/ws"
)

func TestLivePorts(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	bin := liveBinaries(t)
	t.Run(ModeNamespace, func(t *testing.T) {
		t.Setenv("XBIN_FUSE_OVERLAYFS", "none") // the kernel overlay
		testLivePorts(t, newLiveEnv(t, bin, ""), ModeNamespace)
	})
	t.Run(ModeVM, func(t *testing.T) { testLivePorts(t, newLiveVMEnv(t).liveEnv, ModeVM) })
}

func testLivePorts(t *testing.T, le *liveEnv, mode string) {
	le.t = t
	le.create(map[string]any{"name": "web", "mode": mode, "memMiB": 512, "vcpus": 1, "mounts": []any{probeMount}})
	// A real server: the tunnel of an upgrade needs a connection to hijack.
	as := func(p auth.Principal) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			le.mux.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	}
	srv := httptest.NewServer(as(mgr))
	defer srv.Close()
	other := httptest.NewServer(as(noCap))
	defer other.Close()
	get := func(base, path string, hdr map[string]string) (int, string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest("GET", base+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header
	}
	refusal := func(body string) string {
		var e struct{ Refusal string }
		_ = json.Unmarshal([]byte(body), &e)
		return e.Refusal
	}

	// stopped: nothing to reach, and the route doesn't start it
	if code, body, _ := get(srv.URL, "/sandboxes/web/ports/8123/", nil); code != http.StatusConflict || refusal(body) != RefState {
		t.Fatalf("a stopped sandbox: %d %s", code, body)
	}
	le.start("web")
	if code, body, _ := get(srv.URL, "/sandboxes/web/ports/8123/", nil); code != http.StatusBadGateway || refusal(body) != RefNotListening {
		t.Fatalf("nothing listening: %d %s", code, body)
	}
	w := le.do(mgr, "POST", "/sandboxes/web/execs", map[string]any{"argv": []string{"/opt/probe/probe", "serve", "127.0.0.1:8123"}})
	le.want(w, http.StatusCreated, "")
	deadline := time.Now().Add(30 * time.Second)
	for {
		code, body, _ := get(srv.URL, "/sandboxes/web/ports/8123/", nil)
		if code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server never answered: %d %s", code, body)
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Run("a GET: the path as sent, the query, no credentials either way", func(t *testing.T) {
		code, body, h := get(srv.URL, "/sandboxes/web/ports/8123/a%2Fb/c.js?x=1&y=%20", map[string]string{
			"Authorization": "Bearer instance-token", "Cookie": "xbin_session=s", "X-XBin-From": "apps/evil",
			"X-Forwarded-For": "10.0.0.1", "Sbx-User": "alice"})
		want := "method=GET path=/a%2Fb/c.js query=x=1&y=%20 host=localhost:8123 leaked= body=0\n"
		if code != http.StatusOK || body != want {
			t.Fatalf("got %d %q, want %q", code, body, want)
		}
		if h.Get("Set-Cookie") != "" || h.Get("X-Xbin-User") != "" {
			t.Fatalf("headers from the sandbox passed: %v", h)
		}
	})
	t.Run("a POST with a body", func(t *testing.T) {
		res, err := http.Post(srv.URL+"/sandboxes/web/ports/8123/form", "text/plain", strings.NewReader("hello"))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if !strings.Contains(string(b), "method=POST path=/form") || !strings.Contains(string(b), "body=5") {
			t.Fatalf("%d %s", res.StatusCode, b)
		}
	})
	t.Run("a WebSocket echo", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c, _, err := ws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/sandboxes/web/ports/8123/ws", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		for _, msg := range []string{"hi", strings.Repeat("x", 100000)} {
			if err := c.WriteMessage(ws.TextMessage, []byte(msg)); err != nil {
				t.Fatal(err)
			}
			_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
			_, p, err := c.ReadMessage()
			if err != nil || string(p) != "echo:"+msg {
				t.Fatalf("echo: %v %.40q", err, p)
			}
		}
	})
	t.Run("refusals", func(t *testing.T) {
		for path, want := range map[string]int{
			"/sandboxes/web/ports/0/":             http.StatusBadRequest,
			"/sandboxes/web/ports/65536/":         http.StatusBadRequest,
			"/sandboxes/web/ports/08123/":         http.StatusBadRequest,
			"/sandboxes/web/ports/8123/%2E%2E/x":  http.StatusBadRequest,
			"/sandboxes/nope/ports/8123/":         http.StatusNotFound,
			"/sandboxes/web/ports/8124/":          http.StatusBadGateway,
			"/sandboxes/web%2Fx/ports/8123/":      http.StatusBadRequest,
			"/sandboxes/web/ports/8123/ok/%2e/ok": http.StatusBadRequest,
		} {
			if code, body, _ := get(srv.URL, path, nil); code != want {
				t.Errorf("%s: %d %s, want %d", path, code, body, want)
			}
		}
		if code, body, _ := get(other.URL, "/sandboxes/web/ports/8123/", nil); code != http.StatusForbidden {
			t.Errorf("a tile without cap:sandboxes: %d %s", code, body)
		}
	})
}
