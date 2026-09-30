package main

import (
	"encoding/json"
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

// The pages' Ports rows (GET /ports/{id}, GET /ports/{id}/{port}): whether
// the manager offers ports and why not, and a probe of one port — for its
// operators (write access to the tile) and the people the port proxy
// itself admits on the page, never a reader; the page's body never comes
// back. A runtime answering 501 unsupported (the sandbox's agent predates
// ports) flags the sandbox restartNeeded until it runs again.
func TestPagePortProbe(t *testing.T) {
	rt := newRuntime("vm")
	rt.rt.Caps = append(rt.rt.Caps, "ports")
	m, srv, tg := xbinManager(t, rt, nil)
	m.mu.Lock()
	m.self = "apps/cs"
	m.mu.Unlock()
	pt := tg // the page: a verified person with write access to the tile
	pt.Verified = func(r *http.Request, user string) {
		r.Header.Set("X-XBin-User", user)
		r.Header.Set("X-XBin-User-Level", "write")
	}
	page := pt.As(t, "apps/cs").Verified("alice")
	mine := page.Create(map[string]any{"name": "web"})
	theirs := tg.As(t, "apps/agent").Verified("bob").Create(map[string]any{"name": "bots"})
	get := func(who as, path string, want int, out any) string {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("X-XBin-From", who.from)
		req.Header.Set("X-XBin-Role", who.role)
		if who.user != "" {
			req.Header.Set("X-XBin-User", who.user)
			req.Header.Set("X-XBin-User-Level", who.level)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("GET %s as %+v: %d %s, want %d", path, who, resp.StatusCode, b, want)
		}
		if out != nil {
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatal(err)
			}
		}
		return string(b)
	}
	olga := as{from: "apps/cs", role: "admin", user: "olga", level: "write"} // an operator
	rita := as{from: "apps/cs", role: "admin", user: "rita", level: "read"}
	alice := as{from: "apps/cs", role: "admin", user: "alice", level: "write"}
	agentTile := as{from: "apps/agent", role: "consumer"}

	var info struct {
		Offered       bool   `json:"offered"`
		Why           string `json:"why"`
		RestartNeeded bool   `json:"restartNeeded"`
	}
	get(olga, "/ports/"+mine.ID, 200, &info)
	if !info.Offered || info.Why != "" || info.RestartNeeded {
		t.Fatalf("offered: %+v", info)
	}
	var p portProbe
	body := get(alice, "/ports/"+mine.ID+"/8000?path=/a%20b/x.html?q=1", 200, &p)
	if !p.OK || p.Status != 200 || p.Path != "/a%20b/x.html?q=1" || strings.Contains(body, "GET /api/xbin") {
		t.Fatalf("a probe: %s", body)
	}
	if c := rt.last(t, "GET", "/ports/8000/a b/x.html"); c.Query.Get("q") != "1" || c.Header.Get("Cookie") != "" {
		t.Fatalf("the runtime saw %+v", c)
	}
	get(olga, "/ports/"+theirs.ID+"/8000", 200, &p) // an operator: any consumer's sandbox
	if !p.OK {
		t.Fatalf("an operator's probe: %+v", p)
	}
	get(rita, "/ports/"+mine.ID+"/8000", 403, nil)
	get(rita, "/ports/"+mine.ID, 403, nil)
	get(agentTile, "/ports/"+mine.ID+"/8000", 404, nil) // not the page: the contract's route is the consumers'
	get(olga, "/ports/nope/8000", 404, nil)
	get(olga, "/ports/"+mine.ID+"/0", 400, nil)

	// the sandbox's agent predates ports: said, and the sandbox wants a restart
	rt.mu.Lock()
	rt.refuse["GET /ports/9000/"] = 501
	rt.mu.Unlock()
	get(alice, "/ports/"+mine.ID+"/9000", 200, &p)
	if p.OK || p.Status != 501 || p.Refusal != "unsupported" {
		t.Fatalf("an old agent: %+v", p)
	}
	get(alice, "/ports/"+mine.ID, 200, &info)
	if !info.RestartNeeded || !strings.Contains(info.Why, "restart the sandbox") {
		t.Fatalf("after an old agent: %+v", info)
	}
	var v sandboxView
	page.Call("GET", "/sandboxes/"+mine.ID, nil, 200, &v)
	if !v.RestartNeeded || !strings.Contains(v.StateDetail, "predates ports") {
		t.Fatalf("its view: %+v", v)
	}
	// stopped: the probe says so (never a start), and the flag is gone with that run
	page.Call("POST", "/sandboxes/"+mine.ID+"/stop", nil, 200, nil)
	m.mu.Lock()
	delete(m.live, mine.ID)
	m.mu.Unlock()
	get(alice, "/ports/"+mine.ID+"/8000", 200, &p)
	if p.OK || p.Refusal != "state" {
		t.Fatalf("stopped: %+v", p)
	}
	v = sandboxView{} // (omitempty: a false one isn't sent)
	page.Call("GET", "/sandboxes/"+mine.ID, nil, 200, &v)
	if v.RestartNeeded {
		t.Fatalf("stopped, still flagged: %+v", v)
	}
	// the consumers' own ports route notices it too
	page.Call("POST", "/sandboxes/"+mine.ID+"/start", nil, 200, nil)
	page.Refused("GET", "/sandboxes/"+mine.ID+"/ports/9000/", nil, 501, "unsupported")
	v = sandboxView{}
	page.Call("GET", "/sandboxes/"+mine.ID, nil, 200, &v)
	if !v.RestartNeeded {
		t.Fatalf("after the ports route met an old agent: %+v", v)
	}

	// a runtime without ports: not offered, and why
	rt2 := newRuntime("vm")
	m2, srv2, tg2 := xbinManager(t, rt2, nil)
	_ = m2
	sb2 := tg2.As(t, "apps/agent").Verified("bob").Create(map[string]any{"name": "old"})
	srv = srv2
	get(olga, "/ports/"+sb2.ID, 200, &info)
	if info.Offered || !strings.Contains(info.Why, "doesn't serve ports") {
		t.Fatalf("no ports: %+v", info)
	}
	get(olga, "/ports/"+sb2.ID+"/8000", 200, &p)
	if p.OK || p.Refusal != "unsupported" {
		t.Fatalf("no ports, a probe: %+v", p)
	}
}
