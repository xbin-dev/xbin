package term

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// tileGateRig is a users store and a manager whose registry knows tiles
// (TileAt resolves a path to the tile it is or lies in, as boot's Resolve
// does): the levels a session's directory is decided on are the tile's.
type tileGateRig struct {
	s     *users.Store
	tiles []string
}

func newTileGateRig(t *testing.T, tiles ...string) *tileGateRig {
	t.Helper()
	s, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &tileGateRig{s: s, tiles: tiles}
}

func (g *tileGateRig) tileAt(rel string) (string, bool) {
	best := ""
	for _, tile := range g.tiles {
		if (rel == tile || strings.HasPrefix(rel, tile+"/")) && len(tile) > len(best) {
			best = tile
		}
	}
	return best, best != ""
}

// user upserts id with these entries (and the tile API in their
// terminals) and returns them as a signed-in person.
func (g *tileGateRig) user(t *testing.T, id string, tiles map[string]string) auth.Principal {
	t.Helper()
	if _, err := g.s.Upsert(users.User{ID: id, Role: users.RoleUser, Tiles: tiles, TermAPI: true}, "pw"); err != nil {
		t.Fatal(err)
	}
	return g.person(t, id)
}

// person is id as the store resolves them now.
func (g *tileGateRig) person(t *testing.T, id string) auth.Principal {
	t.Helper()
	u, ok := g.s.Get(id)
	a, ok2 := g.s.Access(id)
	if !ok || !ok2 {
		t.Fatalf("no user %q", id)
	}
	return auth.Principal{UserID: id, Via: "session", User: u, Access: a}
}

// A session's directory may lie inside a tile; the levels it is opened on
// are that TILE's — its exact entries, its owner, its org (D31) — never
// the literal path's, which only patterns match. Every way a session
// opens asks the same: a person's terminal, an agent session, a resume, a
// person's workspace agent's start.
func TestSessionGateDecidesOnTheTile(t *testing.T) {
	g := newTileGateRig(t, "apps/crm", "apps/ledger", "apps/own", "apps/open", "apps/crm/inner")
	pat := g.user(t, "pat", map[string]string{"apps/*": users.LevelTerminal, "apps/crm": users.LevelNone})
	rhea := g.user(t, "rhea", map[string]string{"apps/*": users.LevelTerminal, "apps/crm": users.LevelRead})
	ann := g.user(t, "ann", map[string]string{"apps/crm": users.LevelTerminal})
	uma := g.user(t, "uma", nil)
	mo := g.user(t, "mo", nil)
	if _, err := g.s.UpsertOrg(users.Org{ID: "sales", Members: []users.Member{{ID: "mo", Level: users.LevelTerminal}}}); err != nil {
		t.Fatal(err)
	}
	if err := g.s.SetOwner("apps/ledger", "org:sales"); err != nil {
		t.Fatal(err)
	}
	if err := g.s.SetOwner("apps/own", "user:uma"); err != nil {
		t.Fatal(err)
	}
	pat, mo, uma = g.person(t, "pat"), g.person(t, "mo"), g.person(t, "uma")

	m := NewManager(t.TempDir(), nil) // no directories: an open the gate lets through fails later, not with 403
	m.TileAt = g.tileAt
	shell := func(p auth.Principal, cwd string) int {
		r := httptest.NewRequest("GET", "/ws/term?cwd="+cwd, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		m.ServeWS(w, r)
		return w.Code
	}
	agentOpen := func(p auth.Principal, cwd, resume string) int {
		_, code, _ := m.OpenAgentWith(p, AgentOpen{Cwd: cwd, Provider: "claude", Resume: resume})
		return code
	}
	delegate := func(p auth.Principal, cwd string) int {
		p.Via, p.Agent = "delegate", "xbin~agent"
		_, code, _ := m.DelegateEffective(p, AgentOpen{Cwd: cwd, Provider: "claude"})
		return code
	}
	for _, c := range []struct {
		who  string
		p    auth.Principal
		cwd  string
		open bool
	}{
		// (a) a terminal pattern and an exact `none`: nothing inside the tile
		{"pat", pat, "apps/crm", false},
		{"pat", pat, "apps/crm/src", false},
		{"pat", pat, "apps/crm/src/deep/er", false},
		{"pat", pat, "apps/crm/src/", false},
		{"pat", pat, "/apps/crm/src", false},
		// read on the tile: no shell in any of it
		{"rhea", rhea, "apps/crm/src", false},
		// (b) a personal pattern stops at an org's tile (D31), inside it too
		{"pat", pat, "apps/ledger", false},
		{"pat", pat, "apps/ledger/src", false},
		// a nested tile is a tile of its own: the pattern reaches it
		{"pat", pat, "apps/crm/inner/src", true},
		// never break users: a subdirectory of a tile the person has
		// terminal on opens, as it did
		{"pat", pat, "apps/open/src", true},
		{"rhea", rhea, "apps/open/a/b", true},
		// and the tile's own entries now reach inside it: an exact entry,
		// the tile's owner, its org's member
		{"ann", ann, "apps/crm/src", true},
		{"uma", uma, "apps/own/lib", true},
		{"mo", mo, "apps/ledger/src", true},
		// a deployment-qualified path is no session directory: the target
		// is ?deployment=, on the tile itself
		{"pat", pat, "apps/open+dev", false},
		{"pat", pat, "apps/open+dev/src", false},
		{"ann", ann, "apps/crm+dev/src", false},
		// .. never climbs out of the tile it names
		{"pat", pat, "apps/open/../crm/src", false},
		{"ann", ann, "apps/crm/../../.xbin", false},
	} {
		if got := shell(c.p, c.cwd); (got != 403) != c.open {
			t.Errorf("%s's terminal at %s: %d, want open=%v", c.who, c.cwd, got, c.open)
		}
		if got := agentOpen(c.p, c.cwd, ""); (got != 403) != c.open {
			t.Errorf("%s's agent session at %s: %d, want open=%v", c.who, c.cwd, got, c.open)
		}
		if got := agentOpen(c.p, c.cwd, "0123abcd"); (got != 403) != c.open { // a resume (session/load) is an open
			t.Errorf("%s's resume at %s: %d, want open=%v", c.who, c.cwd, got, c.open)
		}
		if got := delegate(c.p, c.cwd); (got != 403) != c.open {
			t.Errorf("%s's workspace agent's start at %s: %d, want open=%v", c.who, c.cwd, got, c.open)
		}
	}
	// an admin opens anywhere inside a tile, as before
	admin := auth.Principal{Owner: true}
	if got := shell(admin, "apps/crm/src"); got == 403 {
		t.Errorf("an admin's terminal inside a tile: %d", got)
	}
}

// A session opened inside a tile is that tile's session: what it is given
// (its token, network, harness rules, target), who may come back to it
// and what it is listed and announced as are the tile's, while its
// directory stays where it was opened.
func TestSubdirSessionIsItsTiles(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	if err := os.MkdirAll(filepath.Join(r.root, "apps", "x", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	g := newTileGateRig(t, "apps/x")
	m.TileAt = g.tileAt
	var mu sync.Mutex
	asked := map[string][]string{}
	ask := func(what, path string) {
		mu.Lock()
		asked[what] = append(asked[what], path)
		mu.Unlock()
	}
	m.TermNet = func(p auth.Principal, comp string) TermNet { ask("net", comp); return legacyTermNet(p) }
	m.AgentGate = func(_ auth.Principal, tile, _ string) error { ask("harness", tile); return nil }
	m.TileDeployments = func(tile string) TileDeployments { ask("deployments", tile); return zeroDeployments() }
	m.SessionPartition = func(_ auth.Principal, path, _ string) (Partition, error) {
		ask("partition", path)
		return Partition{Tile: "apps/x"}, nil
	}
	toks := &targetTokens{}
	m.Tokens = toks
	var evMu sync.Mutex
	evCwd := map[string]bool{}
	m.OnEvent = func(cwd string, ev SessionEvent) {
		evMu.Lock()
		evCwd[cwd] = true
		evMu.Unlock()
		r.events <- ev
	}

	pat := g.user(t, "pat", map[string]string{"apps/*": users.LevelTerminal})
	info, code, err := m.OpenAgentWith(pat, AgentOpen{Cwd: "apps/x/sub", Provider: "fake"})
	if err != nil || code != 200 {
		t.Fatalf("a session inside a tile pat has terminal on: %d %v", code, err)
	}
	defer m.Kill(info.ID)
	if info.Cwd != "apps/x/sub" {
		t.Errorf("the session's directory: %q, want apps/x/sub as opened", info.Cwd)
	}
	r.until(t, func(e SessionEvent) bool { return e.ID == info.ID && e.Type == agent.EvStatus })
	mu.Lock()
	for _, what := range []string{"net", "harness", "deployments", "partition"} {
		for _, p := range asked[what] {
			if p != "apps/x" {
				t.Errorf("the %s rules were asked of %q, not the tile apps/x", what, p)
			}
		}
		if len(asked[what]) == 0 {
			t.Errorf("the %s rules were never asked", what)
		}
	}
	mu.Unlock()
	if got := toks.take(); got != "apps/x|pat" {
		t.Errorf("the terminal tokens minted: %q, want the tile's (apps/x|pat)", got)
	}
	evMu.Lock()
	if !evCwd["apps/x"] || len(evCwd) != 1 {
		t.Errorf("the session's events were announced on %v, want the tile apps/x", evCwd)
	}
	evMu.Unlock()

	// the tile is closed to pat (an exact none): their pattern still matches
	// apps/x/sub, but the door to the session they opened there shuts
	pat = g.user(t, "pat", map[string]string{"apps/*": users.LevelTerminal, "apps/x": users.LevelNone})
	if err := m.MayDrive(info.ID, pat); !errors.Is(err, ErrForbidden) {
		t.Errorf("pat drives their session inside a tile closed to them: %v", err)
	}
	if rows := m.ListFor(HomeKey(pat), "", pat.CanTerminalTileVia); len(rows) != 0 {
		t.Errorf("pat's directory lists a session inside a tile closed to them: %+v", rows)
	}
	rq := httptest.NewRequest("GET", "/ws/term?session="+info.ID, nil)
	rq = rq.WithContext(auth.WithPrincipal(rq.Context(), pat))
	w := httptest.NewRecorder()
	m.ServeWS(w, rq)
	if w.Code != 403 {
		t.Errorf("pat reattaches to a session inside a tile closed to them: %d", w.Code)
	}
	if _, _, c, err := m.RestartAgent(pat, info.ID, "", "", true, nil); c != 403 {
		t.Errorf("pat restarts a session inside a tile closed to them: %d %v", c, err)
	}
}

// The binds are computed on the session's tile; only the directory the
// session opened in is bound read-write — inside its tile, the same binds
// as before. A tile its person may not read is never bound read-write, in
// any part (the restricted D40 view), and a hidden tile beside the
// directory stays masked.
func TestSubdirBindsAreTheTiles(t *testing.T) {
	root := t.TempDir()
	home := HomeDir(root, "pat")
	for _, d := range []string{"apps/crm/src", "apps/crm/other", "apps/open", "homes/pat"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// the deny-list plan: a hidden tile nested in the tile, beside the
	// directory, stays masked; the tile and the directory don't
	sb := scopedBinds(root, "apps/crm", "apps/crm/src", home, nil, []string{"apps/crm/other", "apps/secret"})
	rw, _ := rwRO(sb)
	mk := masked(sb)
	if !rw[filepath.Join(root, "apps/crm/src")] || rw[filepath.Join(root, "apps/crm")] {
		t.Errorf("read-write binds %v: want the directory apps/crm/src alone", rw)
	}
	if !mk[filepath.Join(root, "apps/crm/other")] || !mk[filepath.Join(root, "apps/secret")] || mk[filepath.Join(root, "apps/crm")] {
		t.Errorf("masks %v: want apps/crm/other and apps/secret", mk)
	}
	// the allow-list view: the tile read-only, its directory read-write …
	view := t.TempDir()
	vb := scopedBindsView(root, "apps/crm", "apps/crm/src", home, view, []string{"apps/crm", "apps/open"}, nil)
	rw, ro := rwRO(vb)
	if !rw[filepath.Join(root, "apps/crm/src")] || !ro[filepath.Join(root, "apps/crm")] || rw[filepath.Join(root, "apps/crm")] {
		t.Errorf("view binds rw=%v ro=%v: want apps/crm read-only, apps/crm/src read-write", rw, ro)
	}
	// … and a tile not in the view (unreadable) is never bound, its
	// directories neither
	vb = scopedBindsView(root, "apps/crm", "apps/crm/src", home, view, []string{"apps/open"}, nil)
	rw, ro = rwRO(vb)
	for _, d := range []string{"apps/crm", "apps/crm/src"} {
		if rw[filepath.Join(root, d)] || ro[filepath.Join(root, d)] {
			t.Errorf("a tile out of the view: %s is bound (rw=%v ro=%v)", d, rw, ro)
		}
	}
	// a session on the tile itself: as before
	vb = scopedBindsView(root, "apps/crm", "apps/crm", home, view, []string{"apps/crm", "apps/open"}, nil)
	if rw, _ = rwRO(vb); !rw[filepath.Join(root, "apps/crm")] {
		t.Errorf("a session on its tile: rw=%v, want apps/crm", rw)
	}
}

// A session's directory is reached without a link: one inside a tile the
// person has terminal on, pointing at another tile, would open that tile's
// directory read-write — whatever the levels. A link that stays in the
// tile is refused the same way (the bind would follow it). .. and a
// trailing slash are SafeJoin's (pinned in TestSessionGateDecidesOnTheTile).
func TestSessionDirThroughALink(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	g := newTileGateRig(t, "apps/crm", "apps/open")
	m.TileAt = g.tileAt
	for _, d := range []string{"apps/crm/src", "apps/open/real"} {
		if err := os.MkdirAll(filepath.Join(m.Root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../crm", filepath.Join(m.Root, "apps/open/crm")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(m.Root, "apps/open/alias")); err != nil {
		t.Fatal(err)
	}
	pat := g.user(t, "pat", map[string]string{"apps/open": users.LevelTerminal})
	for _, cwd := range []string{"apps/open/crm", "apps/open/crm/src", "apps/open/alias"} {
		if _, _, _, _, _, err := m.prepare(openOpts{cwd: cwd, tile: "apps/open", homeKey: "pat"}); err == nil || !strings.Contains(err.Error(), "without a link") {
			t.Errorf("a session at %s (a link): %v, want refused", cwd, err)
		}
		r := httptest.NewRequest("GET", "/ws/term?cwd="+cwd, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), pat))
		w := httptest.NewRecorder()
		m.ServeWS(w, r)
		if w.Code != 400 && w.Code != 403 {
			t.Errorf("pat's terminal at %s (a link): %d, want refused", cwd, w.Code)
		}
	}
	if _, _, _, _, _, err := m.prepare(openOpts{cwd: "apps/open/real", tile: "apps/open", homeKey: "pat"}); err != nil {
		t.Errorf("a session at a real directory inside its tile: %v", err)
	}
}

// A past session is listed and read on the tile it ran on: a tile closed to
// its person since takes its sessions inside it out of their history too.
func TestSubdirHistoryIsTheTiles(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	g := newTileGateRig(t, "apps/crm")
	m.TileAt = g.tileAt
	dir := m.historyDir("pat", "apps/crm/src")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0123abcd.json"), []byte(`{"meta":{"id":"0123abcd","cwd":"apps/crm/src","provider":"fake","ended":"2026-11-16T00:00:00Z"},"events":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	pat := g.user(t, "pat", map[string]string{"apps/*": users.LevelTerminal})
	if rows := m.ListHistory(HistoryOf(pat), "", pat.CanTerminalTileVia); len(rows) != 1 {
		t.Fatalf("pat's history: %+v, want their one past session", rows)
	}
	pat = g.user(t, "pat", map[string]string{"apps/*": users.LevelTerminal, "apps/crm": users.LevelNone})
	if rows := m.ListHistory(HistoryOf(pat), "", pat.CanTerminalTileVia); len(rows) != 0 {
		t.Errorf("pat's history lists a session of a tile closed to them: %+v", rows)
	}
}
