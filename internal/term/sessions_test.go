package term

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// The directory reasons over the session map alone, so stub sessions (no
// PTY) exercise it; the reattach gate runs before any upgrade, so a stub is
// enough there too.
func stub(m *Manager, id, homeKey, cwd string, born time.Time) *Session {
	s := &Session{ID: id, Cwd: cwd, homeKey: homeKey, born: born, lastActive: born,
		clients: map[*client]struct{}{}, gpu: "none", api: true}
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	return s
}

func TestListForIsPerUserOrderedAndFiltered(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	t0 := time.Unix(1000, 0)
	stub(m, "b", "alice", "apps/mine", t0.Add(time.Second))
	stub(m, "a", "alice", "apps/mine", t0)
	stub(m, "c", "alice", "apps/docs", t0.Add(2*time.Second))
	stub(m, "d", "bob", "apps/mine", t0)
	m.Rename("a", "build")

	ids := func(l []SessionInfo) []string {
		out := []string{}
		for _, s := range l {
			out = append(out, s.ID)
		}
		return out
	}
	all := m.ListFor("alice", "", nil)
	if got := ids(all); len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("alice's sessions = %v, want [a b c] oldest first", got)
	}
	if all[0].Name != "build" || all[0].GPU != "none" || !all[0].API || all[0].Cwd != "apps/mine" || all[0].Clients != 0 {
		t.Fatalf("row = %+v", all[0])
	}
	if got := ids(m.ListFor("alice", "apps/docs", nil)); len(got) != 1 || got[0] != "c" {
		t.Fatalf("?cwd= = %v", got)
	}
	// a tile alice may no longer open a terminal on is left out
	may := func(rel string) bool { return rel == "apps/mine" }
	if got := ids(m.ListFor("alice", "", may)); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("filtered = %v, want [a b]", got)
	}
	if got := ids(m.ListFor("bob", "", nil)); len(got) != 1 || got[0] != "d" {
		t.Fatalf("bob's = %v", got)
	}
	if got := m.ListFor("nobody", "", nil); got == nil || len(got) != 0 {
		t.Fatalf("a stranger gets an empty list, never nil: %v", got)
	}
	if m.Rename("zz", "x") {
		t.Fatal("renamed a session that does not exist")
	}
	if m.Owner("d") != "bob" || m.Owner("zz") != "" {
		t.Fatal("Owner")
	}
}

func TestReattachGate(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	stub(m, "s1", "alice", "apps/mine", time.Now())
	do := func(p auth.Principal) (int, string) {
		r := httptest.NewRequest("GET", "/ws/term?session=s1", nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		m.ServeWS(w, r)
		return w.Code, w.Body.String()
	}
	alice := func(level string) auth.Principal {
		return auth.Principal{UserID: "alice", Via: "session",
			User: &users.User{ID: "alice", Role: "user", Tiles: map[string]string{"apps/mine": level}}}
	}
	// the creator, still terminal-level: passes the gate (and then fails the
	// upgrade, which is not a websocket handshake here — anything but 403/404)
	if c, b := do(alice(users.LevelTerminal)); c == 403 || c == 404 {
		t.Fatalf("creator refused: %d %s", c, b)
	}
	// the creator whose level was withdrawn: refused, with the reason
	if c, b := do(alice(users.LevelWrite)); c != 403 || b == "" || b[:16] != "terminal access " {
		t.Fatalf("revoked creator: %d %q, want 403 revoked", c, b)
	}
	// another user, even terminal-level on the tile: not theirs
	bob := auth.Principal{UserID: "bob", Via: "session",
		User: &users.User{ID: "bob", Role: "user", Tiles: map[string]string{"apps/mine": users.LevelTerminal}}}
	if c, b := do(bob); c != 403 || b[:16] != "session belongs " {
		t.Fatalf("another user: %d %q", c, b)
	}
	// an admin: passes both checks
	if c, b := do(auth.Principal{Owner: true}); c == 403 || c == 404 {
		t.Fatalf("admin refused: %d %s", c, b)
	}
}

func TestOnChangeSeesRenameAndClose(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	var got []string
	m.OnChange = func(op, homeKey, id, cwd string) { got = append(got, op+":"+homeKey+":"+id+":"+cwd) }
	s := stub(m, "s1", "alice", "apps/mine", time.Now())
	m.Rename("s1", "x")
	m.changed("close", s)
	if len(got) != 2 || got[0] != "rename:alice:s1:apps/mine" || got[1] != "close:alice:s1:apps/mine" {
		t.Fatalf("OnChange calls = %v", got)
	}
	m.OnChange = nil
	m.changed("open", s) // nil-safe
}
