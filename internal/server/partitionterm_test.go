package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-09 PD-29 — the agent-session routes are converted (06 §1): a
// person's terminal on a partitioned tile (its credential acts in their
// partition) passes the partition gate on every POST /term/sessions*
// route, carrying its partition — no longer refused as unconverted.
func TestPartitionTermRoutesConverted(t *testing.T) {
	w := partServer(t)
	w.s.Term = term.NewManager(t.TempDir(), nil)
	w.s.Handler() // mounts the core API, the term routes included
	anaTerm := auth.Principal{Component: "apps/a", UserID: "ana", Via: "terminal"}
	for _, path := range []string{"/term/sessions", "/term/sessions/x/restart", "/term/sessions/x/prompt", "/term/sessions/x/cancel",
		"/term/sessions/x/permissions/p1", "/term/sessions/x/elicitations/e1", "/term/sessions/x/options"} {
		r := httptest.NewRequest("POST", path, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), anaTerm))
		r2, deny := w.s.partitionGate(r)
		if deny != nil {
			rec := httptest.NewRecorder()
			deny(rec, r2)
			t.Errorf("POST %s: refused %d %s", path, rec.Code, rec.Body.String())
			continue
		}
		if got := auth.PrincipalOf(r2).Partition; got != "user:ana" {
			t.Errorf("POST %s: the handler's principal carries %q", path, got)
		}
	}
	for pat, why := range PartitionUnconverted() {
		if strings.Contains(pat, "/term/") {
			t.Errorf("%s is still unconverted (%s)", pat, why)
		}
	}
}

// covers PD-09 S2 — admins and other people's sessions over HTTP (06 §3,
// §Tests term/server): on a partitioned tile an admin gets 403 on reattach
// and on every drive route of bob's session (get, events, log, diff,
// prompt, cancel, restart, permission, question and option answers,
// rename), may still end it, and lists it without its name; bob drives his
// own. On a tile that isn't partitioned an admin keeps today's pass.
func TestPartitionTermAdmin(t *testing.T) {
	h, s := impServer(t)
	root := t.TempDir()
	for _, d := range []string{"apps/p", "apps/u"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s.Term = term.NewManager(root, nil)
	s.Hub = events.NewHub()
	s.Term.SessionPartition = func(p auth.Principal, tile, dep string) (term.Partition, error) {
		if tile != "apps/p" {
			return term.Partition{}, nil
		}
		return term.Partition{Partitioned: true, Tile: tile, Part: "user:" + p.UserID, Key: "k-" + p.UserID}, nil
	}
	s.Term.TilePartitioned = func(path string) (string, bool) { return path, path == "apps/p" }
	tiles := map[string]string{"apps/p": users.LevelTerminal, "apps/u": users.LevelTerminal}
	if _, err := s.Auth.Users.Upsert(users.User{ID: "bob", Role: users.RoleUser, Tiles: tiles}, "pw"); err != nil {
		t.Fatal(err)
	}
	bobP := auth.Principal{UserID: "bob", Via: "session", User: &users.User{ID: "bob", Role: users.RoleUser, Tiles: tiles}}
	alicePrincipal := auth.Principal{UserID: "alice", Via: "session", User: &users.User{ID: "alice", Role: users.RoleAdmin}}
	serveWS := func(p auth.Principal, q string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/ws/term?"+q, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		rec := httptest.NewRecorder()
		s.Term.ServeWS(rec, r) // a new session opens, then the upgrade fails: it runs with no client
		return rec
	}
	serveWS(bobP, "cwd=apps/p")
	serveWS(bobP, "cwd=apps/u")
	t.Cleanup(func() {
		for _, row := range s.Term.ListFor("bob", "", nil) {
			s.Term.Kill(row.ID)
		}
		for deadline := time.Now().Add(10 * time.Second); len(s.Term.ListFor("bob", "", nil)) > 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		}
	})
	ids := map[string]string{}
	for _, row := range s.Term.ListFor("bob", "", nil) {
		ids[row.Cwd] = row.ID
	}
	if ids["apps/p"] == "" || ids["apps/u"] == "" {
		t.Fatalf("bob's sessions: %v", ids)
	}
	alice := s.Auth.NewSession("alice", "")
	bob := s.Auth.NewSession("bob", "")
	do := func(sid, method, path, body string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, withCookie(method, "/api/xbin"+path, body, sid))
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}
	for tile, name := range map[string]string{"apps/p": "bob's secret plan", "apps/u": "a plain tab"} {
		if c, b := do(bob, "PATCH", "/term/sessions/"+ids[tile], `{"name":"`+name+`"}`); c != 200 {
			t.Fatalf("bob renames his %s session: %d %s", tile, c, b)
		}
	}

	p := ids["apps/p"]
	drives := [][3]string{
		{"GET", "/term/sessions/" + p, ""}, {"GET", "/term/sessions/" + p + "/events", ""},
		{"GET", "/term/sessions/" + p + "/events?limit=10", ""}, {"GET", "/term/sessions/" + p + "/log", ""},
		{"GET", "/term/sessions/" + p + "/diff?turn=1", ""}, {"POST", "/term/sessions/" + p + "/prompt", `{"text":"dump her data"}`},
		{"POST", "/term/sessions/" + p + "/cancel", ""}, {"POST", "/term/sessions/" + p + "/restart", `{}`},
		{"POST", "/term/sessions/" + p + "/permissions/p1", `{"decision":"allow_always"}`},
		{"POST", "/term/sessions/" + p + "/elicitations/e1", `{"action":"accept","content":{}}`},
		{"POST", "/term/sessions/" + p + "/options", `{"id":"mode","value":"bypassPermissions"}`},
	}
	for _, d := range drives {
		if c, b := do(alice, d[0], d[1], d[2]); c != 403 || !strings.Contains(b, "keeps each person's data apart") {
			t.Errorf("admin %s %s: %d %s", d[0], d[1], c, b)
		}
	}
	if c, b := do(alice, "PATCH", "/term/sessions/"+p, `{"name":"x"}`); c != 403 {
		t.Errorf("admin renames bob's partition session: %d %s", c, b)
	}
	if rec := serveWS(alicePrincipal, "session="+p); rec.Code != 403 || !strings.Contains(rec.Body.String(), "keeps each person's data apart") {
		t.Errorf("admin reattaches to bob's partition session: %d %s", rec.Code, rec.Body.String())
	}
	// bob drives his own; an admin keeps today's pass on apps/u
	if c, b := do(bob, "GET", "/term/sessions/"+p, ""); c != 200 {
		t.Errorf("bob reads his own session: %d %s", c, b)
	}
	if c, b := do(alice, "GET", "/term/sessions/"+ids["apps/u"], ""); c != 200 {
		t.Errorf("admin on an unpartitioned tile's session: %d %s", c, b)
	}
	if rec := serveWS(alicePrincipal, "session="+ids["apps/u"]); rec.Code == 403 || rec.Code == 404 {
		t.Errorf("admin reattaching on an unpartitioned tile: %d %s", rec.Code, rec.Body.String())
	}

	// ?user= listing: the partition session without its name
	c, b := do(alice, "GET", "/term/sessions?user=bob", "")
	var rows []term.SessionInfo
	if c != 200 || json.Unmarshal([]byte(b), &rows) != nil || len(rows) != 2 || strings.Contains(b, "secret plan") {
		t.Fatalf("?user=bob: %d %s", c, b)
	}
	for _, r := range rows {
		switch r.Cwd {
		case "apps/p":
			if r.Name != "" || r.Partition != "user:bob" {
				t.Errorf("the partition row in an admin's listing: %+v", r)
			}
		case "apps/u":
			if r.Name != "a plain tab" || r.Partition != "" {
				t.Errorf("an unpartitioned row changed: %+v", r)
			}
		}
	}
	// bob's own listing keeps his names
	if c, b := do(bob, "GET", "/term/sessions?cwd=apps/p", ""); c != 200 || !strings.Contains(b, `"name":"bob's secret plan"`) || !strings.Contains(b, `"partition":"user:bob"`) {
		t.Errorf("bob's own listing: %d %s", c, b)
	}

	// view-as: an admin looking as bob (read-only) gets no silent path in —
	// every read of his session there is refused (PD-08: view-as opens no
	// partition), his listing leaves its name out, and his partition
	// history isn't merged in; his session on apps/u reads as today
	s.Term.PersonPartitionKey = func(id string) string { return "k-" + id }
	hist := func(dir, id, cwd string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{"meta":{"id":"` + id + `","cwd":"` + cwd + `","provider":"fake","created":"x","ended":"x","turns":1,"preview":"` + id + ` plan"},"events":[]}`
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hist(filepath.Join(root, "data", "agent-history", ".partitions", "k-bob", util.TileKey("apps/p")), "aa01", "apps/p")
	hist(filepath.Join(root, "data", "agent-history", "bob", util.CompKey("apps/u")), "bb02", "apps/u")
	view := viewAs(t, h, alice, "bob")
	for _, d := range drives[:5] { // the GETs: session, events (both), log, diff
		if c, b := do(view, d[0], d[1], d[2]); c != 403 || !strings.Contains(b, "viewing as someone") {
			t.Errorf("view-as %s %s: %d %s", d[0], d[1], c, b)
		}
	}
	if c, b := do(view, "GET", "/term/sessions/"+ids["apps/u"], ""); c != 200 {
		t.Errorf("view-as on bob's unpartitioned session (today's): %d %s", c, b)
	}
	if c, b := do(view, "GET", "/term/sessions", ""); c != 200 || strings.Contains(b, "secret plan") || !strings.Contains(b, "a plain tab") {
		t.Errorf("view-as listing: %d %s", c, b)
	}
	if c, b := do(view, "GET", "/agent/history", ""); c != 200 || strings.Contains(b, "aa01") || !strings.Contains(b, "bb02") {
		t.Errorf("view-as history: %d %s", c, b)
	}
	if c, b := do(view, "GET", "/agent/history/aa01/events", ""); c != 404 {
		t.Errorf("view-as reads bob's partition transcript: %d %s", c, b)
	}
	if c, b := do(bob, "GET", "/agent/history", ""); c != 200 || !strings.Contains(b, "aa01") || !strings.Contains(b, "bb02") {
		t.Errorf("bob's own history: %d %s", c, b)
	}
	if c, b := do(bob, "GET", "/agent/history/aa01/events", ""); c != 200 || !strings.Contains(b, "aa01 plan") {
		t.Errorf("bob reads his partition transcript: %d %s", c, b)
	}

	// kill stays: the admin ends bob's partition session
	if c, b := do(alice, "DELETE", "/term/sessions/"+p, ""); c != http.StatusNoContent {
		t.Errorf("admin ends bob's partition session: %d %s", c, b)
	}
}

// viewAs mints admin sid's view-as session of user (POST /impersonate,
// then the ticket's URL) → its cookie.
func viewAs(t *testing.T, h http.Handler, sid, user string) string {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("POST", "/api/xbin/impersonate", `{"user":"`+user+`"}`, sid))
	var m struct{ URL string }
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || m.URL == "" {
		t.Fatalf("impersonate %s: %d %s", user, w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("GET", m.URL, "", sid))
	return sessionCookie(t, w)
}
