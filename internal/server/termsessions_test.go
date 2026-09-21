package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/users"
)

// The directory routes over the impersonation test server (alice/dave
// admins, bob a user) plus a term manager with no live PTYs: the listing of
// nothing is still a contract (shape, gates, the read-only view).
func termServer(t *testing.T) (http.Handler, *Server) {
	t.Helper()
	h, s := impServer(t) // Handler() mounts the core API, these routes included
	s.Term = term.NewManager(t.TempDir(), nil)
	s.Hub = events.NewHub()
	return h, s
}

func TestTermSessionsRoutes(t *testing.T) {
	h, s := termServer(t)
	alice := s.Auth.NewSession("alice", "")
	bob := s.Auth.NewSession("bob", "")
	get := func(sid, q string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie("GET", "/api/xbin/term/sessions"+q, "", sid))
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	// an admin with no sessions: an empty list, not null
	if c, b := get(alice, ""); c != 200 || b != "[]" {
		t.Fatalf("admin list: %d %s", c, b)
	}
	// a user without terminal rights: the same
	if c, b := get(bob, "?cwd=apps/x"); c != 200 || b != "[]" {
		t.Fatalf("user list: %d %s", c, b)
	}
	// ?user= is admin-only
	if c, _ := get(bob, "?user=alice"); c != 403 {
		t.Fatalf("?user= as a user: %d", c)
	}
	if c, b := get(alice, "?user=bob"); c != 200 || b != "[]" {
		t.Fatalf("?user= as admin: %d %s", c, b)
	}
	// rename: unknown id is 404 for an admin (CanTouch passes unknown ids)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("PATCH", "/api/xbin/term/sessions/nope", `{"name":"x"}`, alice))
	if w.Code != 404 {
		t.Fatalf("rename unknown: %d %s", w.Code, w.Body.String())
	}
	// the list is readable in a view-as session, the rename is not
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("POST", "/api/xbin/impersonate", `{"user":"bob"}`, alice))
	var m struct{ URL string }
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("GET", m.URL, "", alice))
	view := sessionCookie(t, w)
	if c, b := get(view, ""); c != 200 || b != "[]" {
		t.Fatalf("list in a view: %d %s", c, b)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("PATCH", "/api/xbin/term/sessions/nope", `{"name":"x"}`, view))
	if w.Code != 403 || !strings.Contains(w.Body.String(), "read-only") {
		t.Fatalf("rename in a view: %d %s", w.Code, w.Body.String())
	}
}

func TestTermEventFilter(t *testing.T) {
	ev := events.Event{Type: "term", Component: "apps/x", Data: map[string]any{"op": "open", "id": "s1", "user": "alice"}}
	alice := auth.Principal{UserID: "alice", Via: "session", User: &users.User{ID: "alice", Role: "user"}}
	bob := auth.Principal{UserID: "bob", Via: "session", User: &users.User{ID: "bob", Role: "user"}}
	admin := auth.Principal{Owner: true}
	if !termEventFor(alice, ev) || termEventFor(bob, ev) || !termEventFor(admin, ev) {
		t.Fatal("term events go to their owner and admins only")
	}
	if termEventFor(bob, events.Event{Type: "term"}) {
		t.Fatal("a term event without data reaches nobody but admins")
	}
	// the hook publishes exactly that shape
	s := &Server{Hub: events.NewHub()}
	ch, cancel := s.Hub.Subscribe(func(events.Event) bool { return true })
	defer cancel()
	s.TermChanged("close", "alice", "s1", "apps/x")
	got := <-ch
	d, _ := got.Data.(map[string]any)
	if got.Type != "term" || got.Component != "apps/x" || d["op"] != "close" || d["id"] != "s1" || d["user"] != "alice" {
		t.Fatalf("published %+v", got)
	}
}
