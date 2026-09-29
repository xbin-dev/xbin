package main

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/sdk/sandboxcontract"
)

// The ports capability (D135): offered while the runtime serves it; the
// consumer's escaped path and query reach the runtime's ports route of the
// right sandbox unchanged, with no consumer credential and no cookie back;
// partitions and person rules as for exec; a stopped sandbox is 409 (never
// started); refusals never reach the runtime.
func TestXbinBackendPorts(t *testing.T) {
	rt := newRuntime("vm")
	rt.rt.Caps = append(rt.rt.Caps, "ports")
	m, srv, tg := xbinManager(t, rt, nil)
	a := tg.As(t, "apps/agent")
	alice := a.Verified("alice")
	var h sandboxcontract.Hello
	a.Call("GET", "/hello?protocol=1", nil, 200, &h)
	if !slices.Contains(h.Caps, "ports") {
		t.Fatalf("hello caps %v lack ports", h.Caps)
	}
	sb := alice.Create(map[string]any{"name": "web"})
	rec := m.recCopy(sb.ID)
	do := func(method, path, user string, body io.Reader) (int, string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+"/sbx/sandboxes/"+sb.ID+path, body)
		req.Header.Set("X-XBin-From", "apps/agent")
		req.Header.Set("X-XBin-Role", "consumer")
		if user != "" {
			req.Header.Set("X-XBin-User", user)
		}
		req.Header.Set("Cookie", "xbin_session=viewer")
		req.Header.Set("Sbx-User", "mallory")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	code, body, hdr := do("GET", "/ports/8000/a%2Fb/x.js?q=1&r=%20", "alice", nil)
	want := "GET /api/xbin/sandboxes/" + rec.Runtime + "/ports/8000/a%2Fb/x.js?q=1&r=%20 "
	if code != 200 || body != want || hdr.Get("Set-Cookie") != "" {
		t.Fatalf("%d %q %v, want %q", code, body, hdr, want)
	}
	c := rt.last(t, "GET", "/ports/8000/a/b/x.js")
	if c.Header.Get("Cookie") != "" || c.Header.Get("Sbx-User") != "" || c.Header.Get("X-XBin-User") != "" || c.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("the runtime saw %v", c.Header)
	}
	if code, body, _ := do("POST", "/ports/8000/form", "alice", strings.NewReader("k=v")); code != 200 || !strings.HasSuffix(body, " k=v") {
		t.Fatalf("a POST: %d %q", code, body)
	}
	rt.mu.Lock()
	before := len(rt.seen)
	rt.mu.Unlock()
	for path, want := range map[string]int{"/ports/0/": 400, "/ports/99999/": 400, "/ports/80a/": 400, "/ports/8000/%2e%2e/x": 400} {
		if code, body, _ := do("GET", path, "alice", nil); code != want {
			t.Errorf("%s: %d %s, want %d", path, code, body, want)
		}
	}
	if code, _, _ := do("GET", "/ports/8000/", "bob", nil); code != 403 { // alice's private sandbox
		t.Errorf("bob: %d, want 403", code)
	}
	tg.As(t, "apps/other").Refused("GET", "/sandboxes/"+sb.ID+"/ports/8000/", nil, 404, "not-found")
	rt.mu.Lock()
	for _, c := range rt.seen[before:] {
		if strings.Contains(c.Path, "/ports/") {
			t.Errorf("a refused port request reached the runtime: %s %s", c.Method, c.Path)
		}
	}
	rt.mu.Unlock()
	// stopped: 409, and nothing starts it
	alice.Call("POST", "/sandboxes/"+sb.ID+"/stop", nil, 200, nil)
	m.mu.Lock()
	delete(m.live, sb.ID)
	m.mu.Unlock()
	starts := len(rt.calls("POST", "/start"))
	if code, body, _ := do("GET", "/ports/8000/", "alice", nil); code != 409 || !strings.Contains(body, `"refusal":"state"`) || strings.Contains(body, rec.Runtime) {
		t.Fatalf("stopped: %d %s", code, body)
	}
	if len(rt.calls("POST", "/start")) != starts {
		t.Fatal("a port request started the sandbox")
	}

	// a runtime without ports: not offered, and the route is unsupported
	rt2 := newRuntime("vm")
	_, _, tg2 := xbinManager(t, rt2, nil)
	b := tg2.As(t, "apps/agent")
	b.Call("GET", "/hello?protocol=1", nil, 200, &h)
	if slices.Contains(h.Caps, "ports") {
		t.Fatalf("hello offers ports the runtime lacks: %v", h.Caps)
	}
	sb2 := b.Create(map[string]any{"name": "old"})
	b.Refused("GET", "/sandboxes/"+sb2.ID+"/ports/8000/", nil, 501, "unsupported")
}
