package term

import (
	"net/http/httptest"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
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

	// A non-admin can only open terminals on tiles where their level is
	// TERMINAL (D16) — not elsewhere, and not on tiles they can merely
	// read or write.
	alice := auth.Principal{UserID: "alice", Via: "session",
		User: &users.User{ID: "alice", Role: "user", Tiles: map[string]string{
			"apps/mine": users.LevelTerminal,
			"apps/docs": users.LevelWrite,
			"lib/*":     users.LevelRead,
		}}}
	if c := do("cwd=apps/other", alice); c != 403 {
		t.Fatalf("tile outside allow-list: %d, want 403", c)
	}
	if c := do("cwd=apps/docs", alice); c != 403 {
		t.Fatalf("write-level tile must not grant a terminal: %d, want 403", c)
	}
	if c := do("cwd=lib/ui", alice); c != 403 {
		t.Fatalf("read-level tile must not grant a terminal: %d, want 403", c)
	}

	// Unknown reattach id stays a 404.
	if c := do("session=nope", owner); c != 404 {
		t.Fatalf("unknown session: %d, want 404", c)
	}
}

// The D17 b+c clamps: a non-admin without the grants gets a code-only,
// airgapped shell no matter what the query asked for; the grants restore the
// normal defaults; host networking never leaves the admin plane.
func TestClampTermScopes(t *testing.T) {
	admin := auth.Principal{Owner: true}
	plain := auth.Principal{UserID: "u", User: &users.User{ID: "u", Role: "user"}}
	granted := auth.Principal{UserID: "g", User: &users.User{ID: "g", Role: "user", TermAPI: true, TermNet: true}}

	for _, tc := range []struct {
		name    string
		p       auth.Principal
		api     bool
		net     string
		wantAPI bool
		wantNet string
	}{
		{"admin keeps host", admin, true, NetHost, true, NetHost},
		{"ungranted loses api+net", plain, true, NetInternet, false, NetNone},
		{"ungranted host clamps", plain, false, NetHost, false, NetNone},
		{"ungranted none passes", plain, false, NetNone, false, NetNone},
		{"granted keeps api+internet", granted, true, NetInternet, true, NetInternet},
		{"granted host still clamps", granted, true, NetHost, true, NetNone},
	} {
		api, net := clampTermScopes(tc.p, tc.api, tc.net)
		if api != tc.wantAPI || net != tc.wantNet {
			t.Errorf("%s: got (api=%v net=%s), want (api=%v net=%s)", tc.name, api, net, tc.wantAPI, tc.wantNet)
		}
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
