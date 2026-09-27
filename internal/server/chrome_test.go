package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// chromeWorkspace: a tile whose xbin.json asks for chrome (nobody approved
// it), one an admin approved, a plain tile, and the shipped
// tiles/organisations — with a user store holding admin alice and user bob.
func chromeWorkspace(t *testing.T) (*Server, *users.Store) {
	t.Helper()
	root := t.TempDir()
	page := `<!doctype html><html><head><title>t</title></head><body>t</body></html>`
	for rel, content := range map[string]string{
		"apps/asks/xbin.json":            `{"chrome": true}`,
		"apps/asks/index.html":           page,
		"apps/approved/xbin.json":        `{"chrome": true}`,
		"apps/approved/index.html":       page,
		"apps/plain/xbin.json":           `{}`,
		"apps/plain/index.html":          page,
		"tiles/organisations/xbin.json":  `{"chrome": true}`,
		"tiles/organisations/index.html": page,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.Load(root, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []users.User{{ID: "alice", Role: users.RoleAdmin}, {ID: "bob", Tiles: map[string]string{"apps/asks": users.LevelTerminal}}} {
		if _, err := st.Upsert(u, "pw"); err != nil {
			t.Fatal(err)
		}
	}
	a.SetUsers(st)
	if err := st.SetChromeApproved("apps/approved", true); err != nil {
		t.Fatal(err)
	}
	return &Server{Reg: reg, Auth: a, Hub: events.NewHub()}, st
}

// componentsView: /components as the owner, keyed by path.
func componentsView(t *testing.T, s *Server) map[string]componentInfo {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/xbin/components", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	w := httptest.NewRecorder()
	s.apiComponents(w, r)
	var list []componentInfo
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]componentInfo{}
	for _, c := range list {
		out[c.Path] = c
	}
	return out
}

// D118: a tile's own `chrome: true` — writable by anyone with a terminal
// on it — never unsandboxes it. Unapproved, its document carries the CSP
// sandbox and /components reports it sandboxed with chromeRequested; an
// admin-approved one and the shipped tiles/organisations run unsandboxed.
func TestChromeNeedsApproval(t *testing.T) {
	s, st := chromeWorkspace(t)
	doc := func(url string) *httptest.ResponseRecorder {
		return serveAs(s, "GET", url, auth.Principal{Owner: true}, nil)
	}
	sandboxed := func(url string) bool {
		w := doc(url)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", url, w.Code)
		}
		csp := w.Header().Get("Content-Security-Policy")
		meta := strings.Contains(w.Body.String(), `name="xbin-sandbox"`)
		if (csp == sandboxCSP) != meta || (csp == "") != (w.Header().Get("Cross-Origin-Opener-Policy") == "same-origin") {
			t.Fatalf("%s: inconsistent document headers: CSP %q, meta %v, COOP %q", url, csp, meta, w.Header().Get("Cross-Origin-Opener-Policy"))
		}
		return csp == sandboxCSP
	}
	if !sandboxed("/c/apps/asks/") {
		t.Fatal("an unapproved chrome request runs unsandboxed")
	}
	if sandboxed("/c/apps/approved/") || sandboxed("/c/tiles/organisations/") {
		t.Fatal("approved / shipped chrome got the sandbox")
	}
	if !sandboxed("/c/apps/plain/") {
		t.Fatal("plain tile unsandboxed")
	}

	comps := componentsView(t, s)
	if c := comps["apps/asks"]; c.Chrome || !c.ChromeRequested {
		t.Fatalf("/components apps/asks: chrome %v, chromeRequested %v", c.Chrome, c.ChromeRequested)
	}
	for _, p := range []string{"apps/approved", "tiles/organisations"} {
		if c := comps[p]; !c.Chrome || c.ChromeRequested {
			t.Fatalf("/components %s: chrome %v, chromeRequested %v", p, c.Chrome, c.ChromeRequested)
		}
	}
	if c := comps["apps/plain"]; c.Chrome || c.ChromeRequested {
		t.Fatalf("/components apps/plain: %+v", c)
	}

	// Approval takes effect at once; withdrawing it re-sandboxes the tile.
	if err := st.SetChromeApproved("apps/asks", true); err != nil {
		t.Fatal(err)
	}
	if sandboxed("/c/apps/asks/") || !componentsView(t, s)["apps/asks"].Chrome {
		t.Fatal("approved request still sandboxed")
	}
	if err := st.SetChromeApproved("apps/asks", false); err != nil {
		t.Fatal(err)
	}
	if !sandboxed("/c/apps/asks/") {
		t.Fatal("withdrawn approval left the tile unsandboxed")
	}
	// A new tile at an approved path must not inherit the approval.
	if left := st.PathLeftovers("apps/approved", "user:bob"); !strings.Contains(strings.Join(left, ";"), "trusted chrome") {
		t.Fatalf("chrome approval not a leftover: %v", left)
	}
	// Persisted with the workspace policy.
	if got := st.ChromeTiles(); len(got) != 1 || got[0] != "apps/approved" {
		t.Fatalf("approvals: %v", got)
	}
}

// GET/PUT /chrome: workspace admins only — never a user, and never the
// tile's own terminal token (a coding agent approving its own tile).
func TestChromeAPI(t *testing.T) {
	s, st := chromeWorkspace(t)
	h := s.Handler()
	ch, cancel := s.Hub.Subscribe(func(e events.Event) bool { return e.Type == "grants" })
	defer cancel()
	call := func(method, body string, set func(*http.Request)) (int, map[string]any) {
		r := httptest.NewRequest(method, "/api/xbin/chrome", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		set(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	cookie := func(sid string) func(*http.Request) {
		return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sid}) }
	}
	alice, bob := cookie(s.Auth.NewSession("alice", "")), cookie(s.Auth.NewSession("bob", ""))
	term := s.Auth.MintTerminal("apps/asks", "bob")
	agent := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+term) }

	if code, _ := call("GET", "", bob); code != http.StatusForbidden {
		t.Fatalf("user GET: %d", code)
	}
	for name, who := range map[string]func(*http.Request){"user": bob, "terminal token": agent} {
		if code, _ := call("PUT", `{"path":"apps/asks","approved":true}`, who); code != http.StatusForbidden {
			t.Fatalf("%s approved chrome: %d", name, code)
		}
	}
	if st.ChromeApproved("apps/asks") {
		t.Fatal("a refused PUT approved the tile")
	}
	code, out := call("GET", "", alice)
	if code != http.StatusOK {
		t.Fatalf("admin GET: %d", code)
	}
	rows := map[string]map[string]any{}
	for _, r := range out["tiles"].([]any) {
		m := r.(map[string]any)
		rows[m["path"].(string)] = m
	}
	if r := rows["apps/asks"]; r["requested"] != true || r["approved"] != false || r["chrome"] != false {
		t.Fatalf("apps/asks row: %v", r)
	}
	if r := rows["tiles/organisations"]; r["shipped"] != true || r["chrome"] != true {
		t.Fatalf("shipped row: %v", r)
	}
	if _, ok := rows["apps/plain"]; ok {
		t.Fatal("a tile that doesn't ask is listed")
	}
	for body, want := range map[string]int{
		`{"path":"shell","approved":true}`:     http.StatusBadRequest,
		`{"path":"apps/nope","approved":true}`: http.StatusNotFound,
		`{"path":"../x","approved":true}`:      http.StatusBadRequest,
		`{"path":"apps/asks"}`:                 http.StatusBadRequest,
	} {
		if code, _ := call("PUT", body, alice); code != want {
			t.Fatalf("PUT %s: %d, want %d", body, code, want)
		}
	}
	if code, out := call("PUT", `{"path":"apps/asks","approved":true}`, alice); code != http.StatusOK || out["chrome"] != true {
		t.Fatalf("admin approve: %d %v", code, out)
	}
	select {
	case e := <-ch:
		if e.Component != "apps/asks" {
			t.Fatalf("event: %+v", e)
		}
	default:
		t.Fatal("no grants event for the tile")
	}
	if code, out := call("PUT", `{"path":"apps/gone","approved":false}`, alice); code != http.StatusOK || out["approved"] != false {
		t.Fatalf("withdrawing an unknown path: %d %v", code, out)
	}
	if code, out := call("PUT", `{"path":"apps/asks","approved":false}`, alice); code != http.StatusOK || out["chrome"] != false || out["requested"] != true {
		t.Fatalf("admin revoke: %d %v", code, out)
	}
}
