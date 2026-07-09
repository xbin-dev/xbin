package term

import (
	"net/http/httptest"
	"testing"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/users"
)

// The session-open gates fire before any PTY is spawned, so the denial paths
// are unit-testable (plans/terminal-tokens.md).
func TestServeWSGates(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	do := func(q string, p auth.Principal) int {
		r := httptest.NewRequest("GET", "/ws/term?"+q, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		m.ServeWS(w, r)
		return w.Code
	}
	owner := auth.Principal{Owner: true}

	// The root terminal is disabled outright — even for the owner.
	if c := do("", owner); c != 403 {
		t.Fatalf("root terminal (no cwd): %d, want 403", c)
	}
	if c := do("cwd=", owner); c != 403 {
		t.Fatalf("root terminal (empty cwd): %d, want 403", c)
	}
	// Path shenanigans can't resolve back to the root either.
	if c := do("cwd=apps%2F..", owner); c != 403 {
		t.Fatalf("cwd=apps/..: %d, want 403", c)
	}

	// A terminal-flagged non-admin can only open terminals on their tiles.
	alice := auth.Principal{UserID: "alice", Via: "session",
		User: &users.User{ID: "alice", Role: "user", Terminal: true, Tiles: []string{"apps/mine"}}}
	if c := do("cwd=apps/other", alice); c != 403 {
		t.Fatalf("tile outside allow-list: %d, want 403", c)
	}

	// Unknown reattach id stays a 404.
	if c := do("session=nope", owner); c != 404 {
		t.Fatalf("unknown session: %d, want 404", c)
	}
}

// A session token carries the tile-scoped credential into the sandbox env, and
// the git rewrite uses it — never anything from the shared Env closure.
func TestSandboxEnvToken(t *testing.T) {
	m := &Manager{
		Root:   t.TempDir(),
		Listen: "127.0.0.1:1",
		Env: func() []string {
			return []string{"XBIN_URL=http://127.0.0.1:1", "XBIN_TOKEN=OWNER-LEAK", "XBIN_WORKSPACE=/w"}
		},
	}
	env := m.sandboxEnv("apps/foo", NetNone, "/w/homes/alice", "tile-token")
	var tok, gitHdr string
	for _, e := range env {
		if v, ok := cutPrefix(e, "XBIN_TOKEN="); ok {
			tok = v
		}
		if v, ok := cutPrefix(e, "GIT_CONFIG_VALUE_1="); ok {
			gitHdr = v
		}
	}
	if tok != "tile-token" {
		t.Fatalf("XBIN_TOKEN in env = %q, want the session token", tok)
	}
	for _, e := range env {
		if e == "XBIN_TOKEN=OWNER-LEAK" {
			t.Fatal("shared Env token leaked into the terminal")
		}
	}
	if gitHdr != "Authorization: Bearer tile-token" {
		t.Fatalf("git extraHeader = %q, want the session token", gitHdr)
	}
}

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return "", false
}
