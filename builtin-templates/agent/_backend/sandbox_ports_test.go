package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// preview_port (D135): it refuses a port nothing listens on, records a
// live step for one that answers, and /runs/{id}/live/… serves the page to
// the run's participants only — with this tile's headers, never the
// sandbox's, and none of the viewer's credentials passed on.
func TestPreviewPort(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	// The fake manager's sandboxes are host processes: a server on the host
	// loopback is one in the sandbox.
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		h := w.Header()
		h.Set("Content-Type", "text/html")
		h.Set("Set-Cookie", "sbx=1; Path=/")
		h.Set("Clear-Site-Data", `"cookies", "storage"`)
		h.Set("NEL", `{"report_to":"x","max_age":86400}`)
		h.Set("Report-To", `{"group":"x","endpoints":[{"url":"https://evil.example/r"}]}`)
		h.Set("Content-Security-Policy", "default-src *")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-XBin-User", "forged")
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("WWW-Authenticate", `Basic realm="xbin"`)
		fmt.Fprintf(w, "<p>live %s?%s</p>", r.URL.EscapedPath(), r.URL.RawQuery)
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port

	box := mkSandbox(t, "apps/cs", "", sbxCreate{Name: "web"})
	cfg := defaultConfig()
	cfg.Class = "coding"
	cfg.Features = map[string]bool{"streaming": false}
	b := sbxBindingOf(box)
	cfg.Sandbox, cfg.Attached = &b, []SandboxBinding{b}
	r, err := ag.startRunOpts(runOpts{Title: "t", Cfg: cfg, Hold: true,
		Stamp: runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}})
	if err != nil {
		t.Fatal(err)
	}
	for u, role := range map[string]string{"carol": roleParticipant, "dave": roleViewer} {
		if _, err := ag.db.q.Exec(`INSERT INTO run_members (run_id, user, role, created) VALUES (?, ?, ?, 1)`, r.ID, u, role); err != nil {
			t.Fatal(err)
		}
	}
	ag.acl.flush(r.ID)

	// nothing listens: the model hears how to start the server
	free, _ := net.Listen("tcp", "127.0.0.1:0")
	freePort := free.Addr().(*net.TCPAddr).Port
	free.Close()
	if _, err := tool(t, ag, r, cfg, "c0", "preview_port", map[string]any{"port": freePort}); err == nil || !strings.Contains(err.Error(), "background") {
		t.Fatalf("nothing listening: %v", err)
	}
	for _, bad := range []map[string]any{{"port": 0}, {"port": 70000}, {"port": port, "path": "https://evil.example/"}, {"port": port, "path": "/a/../b"}} {
		if _, err := tool(t, ag, r, cfg, "cx", "preview_port", bad); err == nil {
			t.Fatalf("%v: no error", bad)
		}
	}
	out := mustTool(t, ag, r, cfg, "c1", "preview_port", map[string]any{"port": port, "path": "/index.html?x=1"})
	if !strings.Contains(out, "live to the human") || !strings.Contains(out, "200") {
		t.Fatalf("preview_port: %s", out)
	}
	steps, err := ag.db.steps(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var live *Step
	for _, s := range steps {
		if s.Kind == "live" {
			live = s
		}
	}
	if live == nil || !strings.Contains(live.Detail, `"port":`+strconv.Itoa(port)) || !strings.Contains(live.Detail, `"path":"/index.html?x=1"`) ||
		!strings.Contains(live.Detail, `"sandbox":"`+box.ID+`"`) {
		t.Fatalf("the live step: %+v", live)
	}

	base := fmt.Sprintf("/runs/%d/live/%s/%d/", r.ID, box.ID, port)
	get := func(c caller, target string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", target, nil)
		req.Header.Set("X-XBin-From", c.from)
		req.Header.Set("X-XBin-Role", "admin")
		req.Header.Set("X-XBin-User", c.user)
		req.Header.Set("X-XBin-User-Level", c.level)
		req.Header.Set("Cookie", "xbin_session=viewer")
		req.Header.Set("Authorization", "Bearer viewer")
		req.Header.Set("Referer", "http://xbin/api/~ticket/")
		req.Header.Set("X-Forwarded-For", "10.0.0.1")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	asCarol := caller{from: "apps/agent", user: "carol", level: "read"}
	w := get(asCarol, base+"a%20b/app.js?q=1&r=%20")
	if w.Code != 200 || w.Body.String() != "<p>live /a%20b/app.js?q=1&r=%20</p>" {
		t.Fatalf("a participant: %d %q", w.Code, w.Body)
	}
	h := w.Header()
	if h.Get("Content-Security-Policy") != liveCSP || h.Get("Referrer-Policy") != "no-referrer" || h.Get("Cache-Control") != "no-store" ||
		h.Get("Content-Type") != "text/html" {
		t.Fatalf("this tile's headers: %v", h)
	}
	for _, k := range []string{"Set-Cookie", "Clear-Site-Data", "Nel", "Report-To", "X-Frame-Options", "X-Xbin-User", "Access-Control-Allow-Origin", "Www-Authenticate"} {
		if h.Get(k) != "" {
			t.Errorf("the sandbox's %s passed: %q", k, h.Get(k))
		}
	}
	for _, k := range []string{"Cookie", "Authorization", "Referer", "X-Forwarded-For", "X-Xbin-User", "X-Xbin-From", "Sbx-User"} {
		if v := seen.Get(k); v != "" {
			t.Errorf("the viewer's %s reached the sandbox: %q", k, v)
		}
	}
	if w := get(asCarol, base+"%2E%2E/x"); w.Code != 400 {
		t.Errorf("a dot segment: %d %s", w.Code, w.Body)
	}
	if w := get(asCarol, fmt.Sprintf("/runs/%d/live/%s/%d/", r.ID, "sb-nope", port)); w.Code != 404 {
		t.Errorf("a sandbox the run hasn't: %d %s", w.Code, w.Body)
	}
	// a viewer may read the conversation, not use its sandbox; a stranger
	// doesn't see the run at all
	if w := get(caller{from: "apps/agent", user: "dave", level: "read"}, base); w.Code != 403 {
		t.Errorf("a viewer: %d %s", w.Code, w.Body)
	}
	if w := get(caller{from: "apps/agent", user: "bob", level: "read"}, base); w.Code != 404 {
		t.Errorf("a stranger: %d %s", w.Code, w.Body)
	}
	// the owner, and a refusal still carries this tile's policy
	w = get(caller{from: "apps/agent", user: "alice", level: "read"}, fmt.Sprintf("/runs/%d/live/%s/%d/", r.ID, box.ID, freePort))
	if w.Code != 502 || w.Header().Get("Content-Security-Policy") != liveCSP {
		t.Errorf("nothing listening, for the owner: %d %v %s", w.Code, w.Header(), w.Body)
	}
}
