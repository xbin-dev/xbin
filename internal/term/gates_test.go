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
	// Grants as the broker computes them (TermNetFor): no org sets (legacy),
	// an org tile with relay rules, and an org tile whose sets carry host.
	noSets := func(p auth.Principal) TermNet { return legacyTermNet(p) }
	sets := TermNet{OrgOK: true, Rules: []string{"net:10.0.0.0/8"}, OrgLabel: "org network (devs-net)"}
	setsHost := TermNet{OrgOK: true, OrgHost: true, HostOK: true, OrgLabel: "org network (infra-net)"}

	for _, tc := range []struct {
		name    string
		p       auth.Principal
		g       TermNet
		api     bool
		net     string
		wantAPI bool
		wantNet string
	}{
		// Pre-D54 rows (no org sets) are unchanged.
		{"admin keeps host", admin, noSets(admin), true, NetHost, true, NetHost},
		{"admin default is internet", admin, noSets(admin), true, "", true, NetInternet},
		{"ungranted loses api+net", plain, noSets(plain), true, NetInternet, false, NetNone},
		{"ungranted host clamps", plain, noSets(plain), false, NetHost, false, NetNone},
		{"ungranted none passes", plain, noSets(plain), false, NetNone, false, NetNone},
		{"ungranted org without sets → none", plain, noSets(plain), false, NetOrg, false, NetNone},
		{"granted keeps api+internet", granted, noSets(granted), true, NetInternet, true, NetInternet},
		{"granted host still clamps", granted, noSets(granted), true, NetHost, true, NetNone},
		{"granted org without sets → internet", granted, noSets(granted), true, NetOrg, true, NetInternet},
		// Org tile with sets: the set is the grant — no termNet needed; it
		// replaces plain internet for members; host stays admin-only.
		{"member default is org", plain, sets, false, "", false, NetOrg},
		{"member org", plain, sets, false, NetOrg, false, NetOrg},
		{"member internet → org", plain, sets, false, NetInternet, false, NetOrg},
		{"member host → org", plain, sets, false, NetHost, false, NetOrg},
		{"member none stays", plain, sets, false, NetNone, false, NetNone},
		{"termNet member internet → org too", granted, sets, true, NetInternet, true, NetOrg},
		{"admin default is org", admin, sets, true, "", true, NetOrg},
		{"admin may still pick internet", admin, sets, true, NetInternet, true, NetInternet},
		{"admin may still pick host", admin, sets, true, NetHost, true, NetHost},
		// A host rule grants host networking to members.
		{"member host via set", plain, setsHost, false, NetHost, false, NetHost},
		{"member default org(host)", plain, setsHost, false, "", false, NetOrg},
	} {
		api, net := clampTermScopes(tc.p, tc.api, tc.net, tc.g)
		if api != tc.wantAPI || net != tc.wantNet {
			t.Errorf("%s: got (api=%v net=%s), want (api=%v net=%s)", tc.name, api, net, tc.wantAPI, tc.wantNet)
		}
	}
}

// ScopesFor lists what the picker may offer, widest first, with the default.
func TestScopesFor(t *testing.T) {
	admin := auth.Principal{Owner: true}
	plain := auth.Principal{UserID: "u", User: &users.User{ID: "u", Role: "user"}}
	ids := func(s []Scope) string {
		out := ""
		for _, x := range s {
			out += x.ID + ","
		}
		return out
	}
	if s, def := ScopesFor(plain, legacyTermNet(plain)); ids(s) != "none," || def != NetNone {
		t.Fatalf("ungranted: %s %s", ids(s), def)
	}
	granted := auth.Principal{UserID: "g", User: &users.User{ID: "g", Role: "user", TermNet: true}}
	if s, def := ScopesFor(granted, legacyTermNet(granted)); ids(s) != "internet,none," || def != NetInternet {
		t.Fatalf("granted: %s %s", ids(s), def)
	}
	sets := TermNet{OrgOK: true, Rules: []string{"net:10.0.0.0/8"}, OrgLabel: "org network (devs-net)", OrgDesc: "🖧 LAN 10.0.0.0/8"}
	if s, def := ScopesFor(plain, sets); ids(s) != "org,none," || def != NetOrg || s[0].Label != "org network (devs-net)" || s[0].Desc == "" {
		t.Fatalf("member with sets: %s %s %+v", ids(s), def, s)
	}
	if s, def := ScopesFor(admin, sets); ids(s) != "org,internet,host,none," || def != NetOrg {
		t.Fatalf("admin with sets: %s %s", ids(s), def)
	}
	if s, _ := ScopesFor(plain, TermNet{OrgOK: true, OrgHost: true, HostOK: true}); ids(s) != "org,host,none," {
		t.Fatalf("member with host set: %s", ids(s))
	}
	// Clamp notes name the effective scope.
	if n := clampNote(NetHost, NetOrg, sets); n == "" || n[len(n)-len("org network (devs-net)"):] != "org network (devs-net)" {
		t.Fatalf("clamp note: %q", n)
	}
	if n := clampNote(NetInternet, NetNone, legacyTermNet(plain)); n != "internet egress needs term-net here — running as offline" {
		t.Fatalf("clamp note: %q", n)
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
	// A relay scope (internet, org) rewrites XBIN_URL to the relay gateway.
	for _, e := range m.sandboxEnv("apps/foo", true, "/w/homes/alice", "tile-token") {
		if v, ok := cutPrefix(e, "XBIN_URL="); ok && v != "http://10.0.2.2:1" {
			t.Fatalf("relay scope XBIN_URL = %q, want the gateway host-forward", v)
		}
	}
	env := m.sandboxEnv("apps/foo", false, "/w/homes/alice", "tile-token")
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
