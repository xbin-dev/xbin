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
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
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
	ev := events.Event{Type: "term", Component: "apps/x", Data: termChange{Op: "open", ID: "s1", User: "alice"}}
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
	b, _ := json.Marshal(got.Data)
	if got.Type != "term" || got.Component != "apps/x" || string(b) != `{"op":"close","id":"s1","user":"alice"}` {
		t.Fatalf("published %+v (%s)", got, b)
	}
	// an agent session's status change: the same event kind, op "status",
	// the summary inline — and the same owner filter
	s.TermStatus("apps/x", term.StatusChange{User: "alice", ID: "s1", Status: "waiting_permission", Pending: 1, Turn: 2})
	got = <-ch
	b, _ = json.Marshal(got.Data)
	if got.Type != "term" || got.Component != "apps/x" || string(b) != `{"op":"status","user":"alice","id":"s1","status":"waiting_permission","pending":1,"questions":0,"turn":2}` {
		t.Fatalf("published %+v (%s)", got, b)
	}
	if !termEventFor(alice, got) || termEventFor(bob, got) || !termEventFor(admin, got) {
		t.Fatal("status events go to their owner and admins only")
	}
}

// GET /ws/term/env reports a tile's terminal layer — whether it exists and
// whether its base image is older than the current rootfs — under the same
// gate as the reset: terminal level on the tile, the root layer admin-only.
func TestTermEnvStatus(t *testing.T) {
	h, s := termServer(t)
	alice := s.Auth.NewSession("alice", "")
	bob := s.Auth.NewSession("bob", "")
	// the layer fields only; the vm block (VM sandboxes) has its own test
	get := func(sid, cwd string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie("GET", "/ws/term/env?cwd="+cwd, "", sid))
		var body map[string]any
		if json.Unmarshal(w.Body.Bytes(), &body) != nil {
			return w.Code, strings.TrimSpace(w.Body.String())
		}
		delete(body, "vm")
		b, _ := json.Marshal(body)
		return w.Code, string(b)
	}
	if c, b := get(alice, "apps/x"); c != 200 || b != `{"baseOutdated":false,"exists":false}` {
		t.Fatalf("no layer yet: %d %s", c, b)
	}
	if c, _ := get(bob, "apps/x"); c != 403 {
		t.Fatalf("a user without terminal level: %d", c)
	}
	// a layer stamped with an older base than the rootfs's
	rootfs := t.TempDir()
	_ = os.MkdirAll(filepath.Join(rootfs, "etc"), 0o755)
	_ = os.WriteFile(filepath.Join(rootfs, "etc", "xbin-base-version"), []byte("v2\n"), 0o644)
	s.Term.Rootfs = rootfs
	layer := filepath.Join(s.Term.Root, ".xbin", "term", util.CompKey("apps/x"))
	_ = os.MkdirAll(layer, 0o755)
	_ = os.WriteFile(filepath.Join(layer, "base"), []byte("v1\n"), 0o644)
	if c, b := get(alice, "apps/x"); c != 200 || b != `{"baseOutdated":true,"exists":true}` {
		t.Fatalf("an old-base layer: %d %s", c, b)
	}
	_ = os.WriteFile(filepath.Join(layer, "base"), []byte("v2\n"), 0o644)
	if c, b := get(alice, "apps/x"); c != 200 || b != `{"baseOutdated":false,"exists":true}` {
		t.Fatalf("a current layer: %d %s", c, b)
	}
}
