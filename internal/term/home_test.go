package term

// home_test.go — the home rule (home.go) and the copies' store
// (homecopy.go), without sandboxes: what a session is given, by whom it is
// opened and what stands on the tile; the layers' life. The mounts
// themselves are home_linux_test.go's, under isolation.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/layers"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/signins"
	"github.com/xbin-dev/xbin/internal/termwire"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// homeTiles is the broker's SigninSource over a test's tiles: who owns
// each, its identity, who else (not an admin) can change it, and which
// accounts are workspace admins — the broker's own two conditions
// (internal/broker/signinothers.go), re-stated here.
type homeTiles struct {
	mu     sync.Mutex
	root   string // the workspace: a tile set here has its directory there
	tiles  map[string]*homeTile
	admins map[string]bool
	past   *signins.Past // who could change each tile before (nil: not remembered)
}

type homeTile struct {
	owner   string // "" the workspace, user:<id>, org:<id>
	ident   string
	editors []string
}

func (h *homeTiles) set(tile string, fn func(*homeTile)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tiles == nil {
		h.tiles = map[string]*homeTile{}
	}
	if h.tiles[tile] == nil {
		h.tiles[tile] = &homeTile{ident: "i-" + tile}
		if h.root != "" {
			_ = os.MkdirAll(filepath.Join(h.root, filepath.FromSlash(tile)), 0o755)
		}
	}
	fn(h.tiles[tile])
}

// drop deletes the tile: out of the registry, and its directory.
func (h *homeTiles) drop(tile string) {
	h.unregister(tile)
	if h.root != "" {
		_ = os.RemoveAll(filepath.Join(h.root, filepath.FromSlash(tile)))
	}
}

// unregister takes the tile out of the registry; its directory stays.
func (h *homeTiles) unregister(tile string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.tiles, tile)
}

func (h *homeTiles) source(user, tile string) (signins.Source, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.tiles[tile]
	if t == nil {
		return signins.Source{}, false
	}
	src := signins.Source{Tile: tile, ID: t.ident, Owner: t.owner, OwnerKind: "workspace", Admin: h.admins[user]}
	switch {
	case t.owner == "user:"+user:
		src.OwnerKind = "you"
	case strings.HasPrefix(t.owner, "user:"):
		src.OwnerKind, src.OwnerID = "person", strings.TrimPrefix(t.owner, "user:")
	case strings.HasPrefix(t.owner, "org:"):
		src.OwnerKind, src.OwnerID = "org", strings.TrimPrefix(t.owner, "org:")
	}
	if t.owner == "" {
		src.NotOwn = !src.Admin
	} else {
		src.NotOwn = t.owner != "user:"+user
	}
	for _, e := range t.editors {
		if e != user {
			src.Editors = append(src.Editors, e)
		}
	}
	sort.Strings(src.Editors)
	if h.past != nil { // as the broker does: what stands now is noted, who no longer can is part of the question
		h.past.Observe(src.ID, tile, t.owner, t.editors)
		src.Past, src.PastUntil = h.past.Others(src.ID, user)
	}
	return src, true
}

func (h *homeTiles) identity(tile string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.tiles[tile]; t != nil {
		return t.ident
	}
	return ""
}

// homeRig is a manager with the home rule wired over tiles: sandboxes "on"
// (nothing here starts one), the real sign-in store's trust answers.
func homeRig(t *testing.T) (*Manager, *homeTiles, *signins.Store) {
	t.Helper()
	root := t.TempDir()
	tiles := &homeTiles{root: root, admins: map[string]bool{"root": true}}
	store := signins.New(signins.Options{Dir: filepath.Join(root, "data", "signins"), AllowPlaintext: true})
	m := NewManager(root, nil)
	m.Isolate = true
	m.Signins = &fakeSignins{real: store}
	m.SigninSource = tiles.source
	m.TileIdentity = tiles.identity
	m.rmTree = os.RemoveAll
	return m, tiles, store
}

func person(id string) auth.Principal {
	return auth.Principal{UserID: id, Via: "session", User: &users.User{ID: id, Role: users.RoleUser}}
}

func adminPerson(id string) auth.Principal {
	return auth.Principal{UserID: id, Via: "session", User: &users.User{ID: id, Role: users.RoleAdmin}}
}

func trustNow(t *testing.T, store *signins.Store, tiles *homeTiles, user, tile string) {
	t.Helper()
	src, _ := tiles.source(user, tile)
	if _, err := store.TrustTile(user, src, "card", src.Basis()); err != nil {
		t.Fatal(err)
	}
}

// The rule, case by case: whose session, on what tile.
func TestHomeRule(t *testing.T) {
	m, tiles, store := homeRig(t)
	tiles.set("apps/alice", func(x *homeTile) { x.owner = "user:alice" })
	tiles.set("apps/bob", func(x *homeTile) { x.owner = "user:bob" })
	tiles.set("apps/org", func(x *homeTile) { x.owner = "org:acme" })
	tiles.set("apps/ws", func(x *homeTile) {})
	tiles.set("apps/shared", func(x *homeTile) { x.owner = "user:alice"; x.editors = []string{"bob"} })
	tiles.set("apps/noid", func(x *homeTile) { x.owner = "user:bob"; x.ident = "" })
	alice, root := person("alice"), adminPerson("root")
	token := auth.Principal{Owner: true, Via: "bearer"}
	delegate := auth.Principal{UserID: "alice", User: alice.User, Via: "delegate", Agent: "system/agent"}

	for _, c := range []struct {
		name string
		p    auth.Principal
		tile string
		want string
		why  string // in the plan's why
	}{
		{"her own tile", alice, "apps/alice", HomeOwn, ""},
		{"another person's tile", alice, "apps/bob", HomeCopy, "it belongs to bob"},
		{"an org's tile", alice, "apps/org", HomeCopy, "it belongs to the org acme"},
		{"the workspace's tile, not an admin", alice, "apps/ws", HomeCopy, "it belongs to the workspace"},
		{"the workspace's tile, an admin", root, "apps/ws", HomeOwn, ""},
		{"her own tile a non-admin can change", alice, "apps/shared", HomeCopy, "bob can change it"},
		{"another person's tile, an admin", root, "apps/bob", HomeCopy, "it belongs to bob"},
		{"the root token, the workspace's tile", token, "apps/ws", HomeOwn, ""},
		{"the root token, a person's tile", token, "apps/bob", HomeCopy, "the root token keeps no trust answers"},
		{"her workspace agent, her own tile", delegate, "apps/alice", HomeOwn, ""},
		{"her workspace agent, another's tile", delegate, "apps/bob", HomeCopy, "it belongs to bob"},
		{"view-as, her own tile", auth.Principal{UserID: "alice", User: alice.User, Via: "session", Impersonator: "root"}, "apps/alice", HomeCopy, "viewing as someone"},
		{"a frame's token, her own tile", auth.Principal{UserID: "alice", User: alice.User, Via: "frame", Component: "apps/alice"}, "apps/alice", HomeCopy, "a tile's credentials"},
		{"a backend's token", auth.Principal{Via: "instance", Component: "apps/alice"}, "apps/alice", HomeCopy, "a tile's credentials"},
		{"a terminal's token with no session there", auth.Principal{UserID: "alice", User: alice.User, Via: "terminal", Component: "apps/alice"}, "apps/alice", HomeCopy, "a terminal's token"},
	} {
		pl := m.homePlanFor(c.p, c.tile, false)
		if pl.mode != c.want || !pl.guarded || pl.refuse != "" || !strings.Contains(pl.why, c.why) {
			t.Errorf("%s: mode %q guarded %v refuse %q why %q — want %q with %q", c.name, pl.mode, pl.guarded, pl.refuse, pl.why, c.want, c.why)
		}
		if v := pl.view(m, false); v == nil || v.Mode != c.want || v.Docs != HomeDocs || (c.want == HomeOwn && v.Trust != nil) {
			t.Errorf("%s: view %+v", c.name, v)
		}
	}

	// the question is the sign-in's: asked of a person who can answer, with
	// the tile's basis; never of the root token or of the person's agent
	if pl := m.homePlanFor(alice, "apps/bob", false); pl.trust == nil || !pl.trust.Allowed || pl.trust.Basis == "" || pl.trust.Tile != "apps/bob" {
		t.Fatalf("alice on bob's tile: trust question %+v", pl.trust)
	}
	for name, p := range map[string]auth.Principal{"the root token": token, "her agent": delegate} {
		if pl := m.homePlanFor(p, "apps/bob", false); pl.trust != nil {
			t.Errorf("%s is asked the trust question: %+v", name, pl.trust)
		}
	}
	// a row the workspace agent reads names no owner and no editor
	if v := m.homePlanFor(delegate, "apps/bob", false).view(m, true); v.Mode != HomeCopy || v.Why != "" || v.Trust != nil {
		t.Errorf("an agent-started session's row: %+v", v)
	}

	// trusted: the real home; and what voids the trust gives the copy back
	trustNow(t, store, tiles, "alice", "apps/bob")
	if pl := m.homePlanFor(alice, "apps/bob", false); pl.mode != HomeOwn {
		t.Fatalf("trusted: %+v", pl)
	}
	if pl := m.homePlanFor(delegate, "apps/bob", false); pl.mode != HomeOwn {
		t.Fatalf("her agent on a tile she trusted: %+v", pl)
	}
	tiles.set("apps/bob", func(x *homeTile) { x.editors = []string{"carol"} }) // someone new can change it
	if pl := m.homePlanFor(alice, "apps/bob", false); pl.mode != HomeCopy || !strings.Contains(pl.why, "carol can change it") {
		t.Fatalf("a new editor after the trust: %+v", pl)
	}
	tiles.set("apps/bob", func(x *homeTile) { x.editors = nil })
	if pl := m.homePlanFor(alice, "apps/bob", false); pl.mode != HomeCopy {
		t.Fatalf("a void trust stays void: %+v", pl)
	}

	// trusting switched off: the copy, and the question says who can change that
	trustNow(t, store, tiles, "alice", "apps/org")
	if _, err := store.SetTrustPolicy(false); err != nil {
		t.Fatal(err)
	}
	if pl := m.homePlanFor(alice, "apps/org", false); pl.mode != HomeCopy || pl.trust == nil || pl.trust.Allowed || !strings.Contains(pl.why, "switched off") {
		t.Fatalf("trusting off: %+v trust %+v", pl, pl.trust)
	}
	if _, err := store.SetTrustPolicy(true); err != nil {
		t.Fatal(err)
	}
	if pl := m.homePlanFor(alice, "apps/org", false); pl.mode != HomeOwn {
		t.Fatalf("trusting back on, the answer still there: %+v", pl)
	}

	// a restart or a resume of a session that ran on the copy stays on it —
	// on her own tile too; never the other way
	if pl := m.homePlanFor(alice, "apps/alice", true); pl.mode != HomeCopy || !pl.kept {
		t.Fatalf("kept on her own tile: %+v", pl)
	}
	if pl := m.homePlanFor(alice, "apps/org", true); pl.mode != HomeCopy || !pl.kept {
		t.Fatalf("kept on a trusted tile: %+v", pl)
	}

	// no identity, or no tile: the open is refused — never the real home
	if pl := m.homePlanFor(alice, "apps/noid", false); pl.mode != HomeCopy || !strings.Contains(pl.refuse, "your real home is not opened there") {
		t.Fatalf("a tile without an identity: %+v", pl)
	}
	if pl := m.homePlanFor(alice, "apps/nowhere", false); pl.mode != HomeCopy || pl.refuse == "" {
		t.Fatalf("no tile: %+v", pl)
	}
	if _, _, _, _, _, err := m.prepare(openOpts{cwd: "apps/x", homeKey: "alice", home: m.homePlanFor(alice, "apps/noid", false)}); err == nil || !strings.Contains(err.Error(), "can't keep a copy") {
		t.Fatalf("prepare with a refused home: %v", err)
	}
	if fi, err := os.Stat(HomeDir(m.Root, "alice")); err == nil {
		t.Fatalf("a refused open made the home: %v", fi)
	}
}

// Without sandboxes nothing is protected and the row says so; a manager
// with no rule wired decides nothing and binds the home as it always did;
// one WITH a rule never binds a home nobody decided.
func TestHomeRuleWithoutSandboxesOrRule(t *testing.T) {
	m, tiles, _ := homeRig(t)
	tiles.set("apps/bob", func(x *homeTile) { x.owner = "user:bob" })
	alice := person("alice")
	home := HomeDir(m.Root, "alice")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}

	m.Isolate = false
	pl := m.homePlanFor(alice, "apps/bob", false)
	if pl.mode != HomeOwn || pl.guarded || !strings.Contains(pl.why, "without sandboxes") {
		t.Fatalf("no sandboxes: %+v", pl)
	}
	if v := pl.view(m, false); !v.Unsandboxed || v.Mode != HomeOwn {
		t.Fatalf("no sandboxes, the row: %+v", v)
	}
	if n := m.HomeSweep(); n != 0 {
		t.Fatalf("a sweep without sandboxes ended %d", n)
	}

	m.Isolate, m.SigninSource = true, nil
	pl = m.homePlanFor(alice, "apps/bob", false)
	if pl.mode != "" || pl.guarded || pl.view(m, false) != nil {
		t.Fatalf("no rule wired: %+v", pl)
	}
	if b, _, _, _, err := m.homeBind(home, openOpts{homeKey: "alice", home: pl}); err != nil || b == nil || b.Mount != nil || b.Src != home {
		t.Fatalf("no rule wired: bind %+v err %v", b, err)
	}

	// the rule wired, and an open nobody decided a home for: refused
	m.SigninSource = tiles.source
	if b, _, _, _, err := m.homeBind(home, openOpts{homeKey: "alice"}); err == nil || b != nil {
		t.Fatalf("an undecided home was bound: %+v %v", b, err)
	}
	// the copy that can't be mounted is an error — never the real home
	m.copies.start = func(sandbox.HoldSpec) (overlayHold, error) { return nil, errors.New("no user namespaces") }
	b, _, _, _, err := m.homeBind(home, openOpts{homeKey: "alice", home: m.homePlanFor(alice, "apps/bob", false)})
	if err == nil || b != nil || !strings.Contains(err.Error(), "your real home is not opened on apps/bob") || !strings.Contains(err.Error(), "no user namespaces") {
		t.Fatalf("a copy that can't be mounted: bind %+v err %v", b, err)
	}
}

// fakeHold is a hold that mounts nothing: Mount hands out an open file.
type fakeHold struct {
	spec   sandbox.HoldSpec
	done   chan struct{}
	once   sync.Once
	mounts atomic.Int32
	closed atomic.Bool
}

func (h *fakeHold) Mount() (*os.File, error) {
	select {
	case <-h.done:
		return nil, errors.New("the overlay's hold is gone")
	default:
	}
	h.mounts.Add(1)
	return os.Open(os.DevNull)
}
func (h *fakeHold) Done() <-chan struct{} { return h.done }
func (h *fakeHold) Close() {
	h.closed.Store(true)
	h.once.Do(func() { close(h.done) })
}

// fakeHolds makes m's copies start fake holds, and lists them.
func fakeHolds(m *Manager) *[]*fakeHold {
	var mu sync.Mutex
	all := &[]*fakeHold{}
	m.copies.start = func(hs sandbox.HoldSpec) (overlayHold, error) {
		h := &fakeHold{spec: hs, done: make(chan struct{})}
		mu.Lock()
		*all = append(*all, h)
		mu.Unlock()
		return h, nil
	}
	m.copies.usage = func(dir string) (int64, error) { return 4096, nil }
	return all
}

// The layers: one per person and tile identity, one hold shared by the
// sessions on it and ended with the last, a new layer for a tile made
// again, the note said once, forget, the sweeps.
func TestHomeLayerStore(t *testing.T) {
	m, tiles, _ := homeRig(t)
	holds := fakeHolds(m)
	tiles.set("apps/bob", func(x *homeTile) { x.owner = "user:bob" })
	tiles.set("apps/org", func(x *homeTile) { x.owner = "org:acme" })
	alice, carol := person("alice"), person("carol")
	for _, k := range []string{"alice", "carol"} {
		if err := os.MkdirAll(HomeDir(m.Root, k), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	open := func(p auth.Principal, tile string) (dir string, release func()) {
		t.Helper()
		o := openOpts{homeKey: HomeKey(p), home: m.homePlanFor(p, tile, false), launch: &sbxLaunch{}}
		b, dir, started, release, err := m.homeBind(HomeDir(m.Root, HomeKey(p)), o)
		if err != nil || b == nil || b.Mount == nil || b.Dst != HomeDir(m.Root, HomeKey(p)) {
			t.Fatalf("%s on %s: bind %+v err %v", p.UserID, tile, b, err)
		}
		started()
		return dir, release
	}

	// two sessions of alice's on bob's tile: one layer, one hold, two clones
	d1, rel1 := open(alice, "apps/bob")
	d2, rel2 := open(alice, "apps/bob")
	if d1 != d2 || len(*holds) != 1 || (*holds)[0].mounts.Load() != 2 {
		t.Fatalf("two sessions: layers %q %q, %d holds", d1, d2, len(*holds))
	}
	h := (*holds)[0]
	if h.spec.Lower != HomeDir(m.Root, "alice") || h.spec.Upper != filepath.Join(d1, "upper") || h.spec.Work != filepath.Join(d1, "work") {
		t.Fatalf("the hold's spec: %+v", h.spec)
	}
	if !strings.HasPrefix(d1, filepath.Join(m.Root, "data", "home-copies", "alice")+string(filepath.Separator)) {
		t.Fatalf("the layer is at %s", d1)
	}
	if fi, err := os.Stat(filepath.Join(d1, "upper")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the upper: %v %v", fi, err)
	}
	// another tile, and another person on the same tile: other layers
	d3, rel3 := open(alice, "apps/org")
	d4, rel4 := open(carol, "apps/bob")
	if d3 == d1 || d4 == d1 || d4 == d3 || len(*holds) != 3 {
		t.Fatalf("layers %q %q %q, %d holds", d1, d3, d4, len(*holds))
	}
	if st, err := layers.Read(d1); err != nil || st.Overlay == "" {
		t.Fatalf("the flavour stamp: %+v %v", st, err)
	}

	// the first of the two ends: the hold stays; the last: it is closed
	rel1()
	rel1() // (a second release of one session is nothing)
	if h.closed.Load() || m.copies.live(d1) != 1 {
		t.Fatalf("after the first session ended: closed %v, live %d", h.closed.Load(), m.copies.live(d1))
	}
	rel2()
	if !h.closed.Load() || m.copies.live(d1) != 0 {
		t.Fatal("the last session ended and the hold stays")
	}
	// the next session starts a new hold on the same layer
	d5, rel5 := open(alice, "apps/bob")
	if d5 != d1 || len(*holds) != 4 {
		t.Fatalf("the next session: layer %q, %d holds", d5, len(*holds))
	}

	// said once per layer
	if !m.copies.tell(d1) || m.copies.tell(d1) {
		t.Fatal("the copy's note is not said exactly once")
	}

	// the list: her layers, never carol's
	list := m.HomeCopies("alice")
	if len(list) != 2 || list[0].Bytes != 4096 {
		t.Fatalf("alice's copies: %+v", list)
	}
	for _, hc := range list {
		if hc.Tile == "apps/bob" && hc.Sessions != 1 || hc.Tile == "apps/org" && hc.Sessions != 1 || hc.Gone {
			t.Errorf("row %+v", hc)
		}
	}
	if id, ok := m.HomeCopyOn("alice", mustSource(t, tiles, "alice", "apps/bob")); !ok || id != filepath.Base(d1) {
		t.Fatalf("HomeCopyOn: %q %v", id, ok)
	}

	// a new layer is mounted with the kernel's overlay (a home is used by
	// every uid of a session: homecopy.go startHold) and stamped; a layer
	// is only ever mounted by the flavour that wrote it — one fuse-overlayfs
	// wrote by fuse-overlayfs, and not at all where there is none any more
	rel3()
	if st, _ := layers.Read(d3); st.Overlay != layers.OverlayKernel || (*holds)[1].spec.Fuse != "" {
		t.Fatalf("a new layer's flavour: %+v, hold %+v", st, (*holds)[1].spec)
	}
	if err := layers.Stamp(d3, layers.Stamps{Overlay: layers.OverlayFuse}); err != nil {
		t.Fatal(err)
	}
	o := openOpts{homeKey: "alice", home: m.homePlanFor(alice, "apps/org", false)}
	t.Setenv("XBIN_FUSE_OVERLAYFS", filepath.Join(m.Root, "no-fuse-overlayfs"))
	if b, _, _, _, err := m.homeBind(HomeDir(m.Root, "alice"), o); err == nil || b != nil || !strings.Contains(err.Error(), "forget what this tile wrote") {
		t.Fatalf("a layer fuse-overlayfs wrote, and no fuse-overlayfs: %+v %v", b, err)
	}
	fo := filepath.Join(m.Root, "fuse-overlayfs")
	if err := os.WriteFile(fo, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XBIN_FUSE_OVERLAYFS", fo)
	n := len(*holds)
	_, _, _, relF, err := m.homeBind(HomeDir(m.Root, "alice"), o)
	if err != nil || len(*holds) != n+1 || (*holds)[n].spec.Fuse != fo {
		t.Fatalf("a layer fuse-overlayfs wrote, with one here: %v, hold %+v", err, (*holds)[len(*holds)-1].spec)
	}
	relF()

	// forget: nobody else's, no path tricks; hers goes with its hold
	// (a layer's id is its tile's: carol's for bob's tile has the same one, under her own directory)
	for _, bad := range []string{"", "..", ".", "../carol/" + filepath.Base(d4), "hc_nope"} {
		if _, _, err := m.ForgetHomeCopy("alice", bad, "x"); !errors.Is(err, ErrNoHomeCopy) {
			t.Errorf("forget %q: %v", bad, err)
		}
	}
	go func() { time.Sleep(50 * time.Millisecond); rel5() }() // (its session ends as it is told to)
	tile, _, err := m.ForgetHomeCopy("alice", filepath.Base(d1), "forgotten")
	if err != nil || tile != "apps/bob" {
		t.Fatalf("forget: %q %v", tile, err)
	}
	if _, err := os.Stat(d1); !os.IsNotExist(err) {
		t.Fatalf("the forgotten layer is still there: %v", err)
	}
	if !(*holds)[3].closed.Load() {
		t.Fatal("the forgotten layer's hold still runs")
	}
	if _, err := os.Stat(d4); err != nil {
		t.Fatalf("carol's layer went with alice's: %v", err)
	}

	// the tile is made again at its path: another identity, another layer
	tiles.set("apps/bob", func(x *homeTile) { x.ident = "i-remade" })
	d6, rel6 := open(carol, "apps/bob")
	if d6 == d4 {
		t.Fatal("a tile made again got the old layer")
	}
	rel6()
	rel4()
	// the sweep removes a layer only when its tile's path is positively not
	// there. The old layer of the tile that was made again stays — an
	// orphan: never mounted again, listed as not used, forgotten by its
	// person (a workspace restored from a backup gives every tile another
	// identity: its people's copies must not go without their click) — and
	// so does an unregistered tile's, and one whose identity can't be read
	tiles.set("apps/kept", func(x *homeTile) { x.owner = "user:bob" })
	d7, rel7 := open(carol, "apps/kept")
	rel7()
	tiles.unregister("apps/kept")
	if n := m.SweepHomeCopies(); n != 0 {
		t.Fatalf("the sweep removed %d layers of tiles that are still there", n)
	}
	ident := m.TileIdentity
	m.TileIdentity = func(string) string { return "" } // EMFILE, EACCES: unknown, not gone
	if n := m.SweepHomeCopies(); n != 0 {
		t.Fatalf("the sweep removed %d layers on an identity it could not read", n)
	}
	for _, hc := range m.HomeCopies("carol") {
		if hc.Gone {
			t.Errorf("an unreadable identity lists %s as gone", hc.Tile)
		}
	}
	m.TileIdentity = ident
	for dir := range map[string]bool{d4: true, d6: true, d7: true} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("after the sweep, %s is gone: %v", dir, err)
		}
	}
	gone := 0
	for _, hc := range m.HomeCopies("carol") {
		if hc.Gone {
			gone++
			if hc.ID != filepath.Base(d4) {
				t.Errorf("listed as gone: %+v", hc)
			}
		}
	}
	if gone != 1 {
		t.Fatalf("the remade tile's old layer is not listed as an orphan (%d gone): %+v", gone, m.HomeCopies("carol"))
	}
	// a tile reached through a link is never "absent" (its disk may be unmounted)
	if err := os.Symlink(filepath.Join(m.Root, "nowhere"), filepath.Join(m.Root, "linked")); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{"apps/bob": false, "apps/nope": true, "nope/deeper/tile": true, "linked/tile": false, "../x": false, "": false} {
		if got := tileAbsent(m.Root, path); got != want {
			t.Errorf("tileAbsent(%q) = %v, want %v", path, got, want)
		}
	}
	tiles.drop("apps/bob") // deleted: its layers go, the old one and the new
	if n := m.SweepHomeCopies(); n != 2 {
		t.Fatalf("the sweep after the tile was deleted removed %d, want carol's two", n)
	}
	if _, err := os.Stat(d7); err != nil {
		t.Fatalf("the unregistered tile's layer went too: %v", err)
	}
	// the account is deleted: every layer of theirs
	m.ForgetHomeCopies("carol", "gone")
	if ents, _ := os.ReadDir(m.homeCopiesRoot("carol")); len(ents) != 0 {
		t.Fatalf("carol's layers after her account went: %v", ents)
	}
	if _, err := os.Stat(d3); err != nil {
		t.Fatalf("alice's other layer went too: %v", err)
	}
}

func mustSource(t *testing.T, tiles *homeTiles, user, tile string) signins.Source {
	t.Helper()
	src, ok := tiles.source(user, tile)
	if !ok {
		t.Fatalf("no tile %s", tile)
	}
	return src
}

// A hold that dies by itself is not reused: the next session starts another.
func TestHomeLayerDeadHold(t *testing.T) {
	m, tiles, _ := homeRig(t)
	holds := fakeHolds(m)
	tiles.set("apps/bob", func(x *homeTile) { x.owner = "user:bob" })
	alice := person("alice")
	home := HomeDir(m.Root, "alice")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	o := openOpts{homeKey: "alice", home: m.homePlanFor(alice, "apps/bob", false)}
	_, _, _, rel1, err := m.homeBind(home, o)
	if err != nil {
		t.Fatal(err)
	}
	(*holds)[0].once.Do(func() { close((*holds)[0].done) }) // fuse-overlayfs died
	_, _, _, rel2, err := m.homeBind(home, o)
	if err != nil || len(*holds) != 2 {
		t.Fatalf("after the hold died: %v, %d holds", err, len(*holds))
	}
	rel1() // the dead hold's session ends late: the new hold is not its to end
	if (*holds)[1].closed.Load() {
		t.Fatal("the old session's end closed the new hold")
	}
	rel2()
	if !(*holds)[1].closed.Load() {
		t.Fatal("the new hold outlived its last session")
	}
}

// liveSession puts a session in m as the rule would have started it for p
// on tile (nothing runs: a process that sleeps).
func liveSession(t *testing.T, m *Manager, p auth.Principal, tile string) *Session {
	t.Helper()
	s := fakeSession(t, m, HomeKey(p), tile)
	s.home = sessionHome{plan: m.homePlanFor(p, tile, false)}
	return s
}

// A trust that stops holding ends the sessions that have the real home on
// that tile; sessions on a copy, and on tiles that need no trust, stay.
func TestHomeSweep(t *testing.T) {
	m, tiles, store := homeRig(t)
	tiles.set("apps/alice", func(x *homeTile) { x.owner = "user:alice" })
	tiles.set("apps/bob", func(x *homeTile) { x.owner = "user:bob" })
	tiles.set("apps/org", func(x *homeTile) { x.owner = "org:acme" })
	alice := person("alice")
	trustNow(t, store, tiles, "alice", "apps/bob")

	own := liveSession(t, m, alice, "apps/alice")
	trusted := liveSession(t, m, alice, "apps/bob")
	onCopy := liveSession(t, m, alice, "apps/org")
	if !own.realHome() || !trusted.realHome() || onCopy.realHome() {
		t.Fatalf("modes: own %v trusted %v copy %v", own.realHome(), trusted.realHome(), onCopy.realHome())
	}
	if n := m.HomeSweep(); n != 0 {
		t.Fatalf("a sweep with nothing changed ended %d", n)
	}

	// she stops trusting bob's tile
	for _, tr := range store.Trusted("alice") {
		if _, err := store.Untrust("alice", tr.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n := m.HomeSweep(); n != 1 {
		t.Fatalf("after the untrust the sweep ended %d, want 1", n)
	}
	waitGone(t, m, trusted.ID)
	if got := string(trusted.hub.Tail(4096)); !strings.Contains(got, "had your real home") {
		t.Fatalf("the ended shell was not told why: %q", got)
	}
	if n := m.HomeSweep(); n != 0 {
		t.Fatalf("a second sweep ended %d more", n)
	}
	if !alive(m, own.ID) || !alive(m, onCopy.ID) {
		t.Fatal("the sweep ended a session on her own tile, or one on a copy")
	}

	// her own tile gains a co-editor: it needs trust now — the session ends
	tiles.set("apps/alice", func(x *homeTile) { x.editors = []string{"bob"} })
	if n := m.SigninSweep(); n != 1 { // the sweep every change already runs
		t.Fatalf("after a new editor the sign-in sweep ended %d, want 1", n)
	}
	waitGone(t, m, own.ID)
	// its tile leaves the registry
	gone := liveSession(t, m, adminPerson("root"), "apps/org") // (an admin: the org's tile needs trust — a copy)
	if gone.realHome() {
		t.Fatal("an admin on an org's tile has the real home")
	}
	tiles.set("apps/ws", func(x *homeTile) {})
	wsS := liveSession(t, m, adminPerson("root"), "apps/ws")
	tiles.drop("apps/ws")
	if n := m.HomeSweep(); n != 1 {
		t.Fatalf("after the tile left the registry the sweep ended %d", n)
	}
	waitGone(t, m, wsS.ID)
	if !alive(m, onCopy.ID) || !alive(m, gone.ID) {
		t.Fatal("a session on a copy was ended by a sweep")
	}
}

// A session with its person's real home is its person's alone: no admin
// pass to attach or drive it; one on the tile's copy keeps today's pass.
func TestHomeAdminPass(t *testing.T) {
	m, tiles, _ := homeRig(t)
	tiles.set("apps/alice", func(x *homeTile) { x.owner = "user:alice" })
	tiles.set("apps/bob", func(x *homeTile) { x.owner = "user:bob" })
	alice, root := person("alice"), adminPerson("root")
	alice.User.Tiles = map[string]string{"apps/*": users.LevelTerminal}
	real := liveSession(t, m, alice, "apps/alice")
	onCopy := liveSession(t, m, alice, "apps/bob")

	if why := real.mayReattach(root, false); why != realHomeRefusal {
		t.Fatalf("an admin attaching to a real-home session: %q", why)
	}
	if m.adminPass(real, root) {
		t.Fatal("an admin has a pass to drive a real-home session")
	}
	if err := m.MayDrive(real.ID, root); !errors.Is(err, ErrForbidden) {
		t.Fatalf("MayDrive by an admin: %v", err)
	}
	if why := real.mayReattach(alice, false); why != "" {
		t.Fatalf("her own session: %q", why)
	}
	if why := real.mayReattach(person("carol"), false); why != "session belongs to another user" {
		t.Fatalf("a stranger: %q", why)
	}
	// the copy: an admin's pass as before
	if why := onCopy.mayReattach(root, false); why != "" || !m.adminPass(onCopy, root) {
		t.Fatalf("an admin on a copy session: %q", why)
	}
	// ending is always allowed
	if !m.CanTouch(real.ID, root) {
		t.Fatal("an admin can't end a real-home session")
	}
	// without sandboxes nothing changes
	m.Isolate = false
	plain := liveSession(t, m, alice, "apps/alice")
	if why := plain.mayReattach(root, false); why != "" {
		t.Fatalf("no sandboxes, an admin attaching: %q", why)
	}
}

// A terminal's token gets the real home only where a session of that
// person's on that tile already has it.
func TestHomeTerminalToken(t *testing.T) {
	m, tiles, store := homeRig(t)
	tiles.set("apps/alice", func(x *homeTile) { x.owner = "user:alice" })
	tiles.set("apps/bob", func(x *homeTile) { x.owner = "user:bob" })
	tiles.set("apps/ws", func(x *homeTile) {})
	alice := person("alice")
	// tokenOf is the principal of the terminal token s was opened with
	tokenOf := func(s *Session, user string) auth.Principal {
		p := auth.Principal{UserID: user, Via: "terminal", Component: s.tile, TermRef: auth.TerminalRef(s.token)}
		if user != "" {
			p.User = alice.User
		}
		return p
	}
	shell := func(p auth.Principal, tile string) *Session {
		s := liveSession(t, m, p, tile)
		s.token = util.RandomToken(24)
		return s
	}
	// a token no live session was opened with (or none at all): the copy
	stray := auth.Principal{UserID: "alice", User: alice.User, Via: "terminal", Component: "apps/alice", TermRef: auth.TerminalRef("no-such-token")}
	if pl := m.homePlanFor(stray, "apps/alice", false); pl.mode != HomeCopy {
		t.Fatalf("a token of no session: %+v", pl)
	}
	stray.TermRef = ""
	if pl := m.homePlanFor(stray, "apps/alice", false); pl.mode != HomeCopy {
		t.Fatalf("a terminal principal without a reference: %+v", pl)
	}
	// bx agent in her own tile's shell: her real home, as the shell has
	own := shell(alice, "apps/alice")
	if pl := m.homePlanFor(tokenOf(own, "alice"), "apps/alice", false); pl.mode != HomeOwn {
		t.Fatalf("from her own tile's shell: %+v", pl)
	}
	// on a tile she has not trusted the shell is on the copy, and so is what its token opens
	onCopy := shell(alice, "apps/bob")
	if pl := m.homePlanFor(tokenOf(onCopy, "alice"), "apps/bob", false); pl.mode != HomeCopy {
		t.Fatalf("from an untrusted tile's shell: %+v", pl)
	}
	// she trusts the tile; a NEW shell there has the real home — the old one
	// stays on the copy, and ITS token still opens nothing more than it has:
	// an admin may be attached to it (copy sessions keep the admin pass), and
	// a session it opened with the real home would be one they drive
	trustNow(t, store, tiles, "alice", "apps/bob")
	fresh := shell(alice, "apps/bob")
	if !fresh.realHome() || onCopy.realHome() {
		t.Fatalf("after the trust: new shell real %v, old shell real %v", fresh.realHome(), onCopy.realHome())
	}
	if pl := m.homePlanFor(tokenOf(onCopy, "alice"), "apps/bob", false); pl.mode != HomeCopy || !strings.Contains(pl.why, "only when that terminal has it") {
		t.Fatalf("the copy shell's token, with a real-home shell of hers beside it: %+v", pl)
	}
	if pl := m.homePlanFor(tokenOf(fresh, "alice"), "apps/bob", false); pl.mode != HomeOwn {
		t.Fatalf("the real-home shell's token: %+v", pl)
	}
	// a token is its tile's and its person's: not another tile, not another person's home
	if pl := m.homePlanFor(tokenOf(own, "alice"), "apps/bob", false); pl.mode != HomeCopy {
		t.Fatalf("her own tile's token on another tile: %+v", pl)
	}
	bobs := tokenOf(own, "bob")
	if pl := m.homePlanFor(bobs, "apps/alice", false); pl.mode != HomeCopy {
		t.Fatalf("her shell's token claiming to be bob's: %+v", pl)
	}

	// the root token's shell: bx agent there is the root token's — on the
	// workspace's own tile its real home, as a one-person install has always
	// had (the token's principal has no account and is not "the owner")
	root := shell(auth.Principal{Owner: true, Via: "bearer"}, "apps/ws")
	if !root.realHome() || root.homeKey != ownerHomeKey {
		t.Fatalf("the root token's shell on the workspace's tile: real %v home %q", root.realHome(), root.homeKey)
	}
	rp := tokenOf(root, "")
	if !rootHome(rp) || rp.IsAdmin() {
		t.Fatalf("the root token's terminal principal: rootHome %v admin %v", rootHome(rp), rp.IsAdmin())
	}
	if pl := m.homePlanFor(rp, "apps/ws", false); pl.mode != HomeOwn || !pl.admin {
		t.Fatalf("bx agent in the root token's shell, on the workspace's tile: %+v", pl)
	}
	// …and on a person's tile the root token's shell is on the copy, and so is its bx agent
	rootOnBob := shell(auth.Principal{Owner: true, Via: "bearer"}, "apps/bob")
	if rootOnBob.realHome() {
		t.Fatal("the root token has its real home on bob's tile")
	}
	if pl := m.homePlanFor(tokenOf(rootOnBob, ""), "apps/bob", false); pl.mode != HomeCopy {
		t.Fatalf("bx agent in the root token's shell on bob's tile: %+v", pl)
	}
}

// On a tile a person has not trusted, an admin's session — and the root
// token's — is confined like a member's: the restricted tier and its view
// of the workspace. Trusting the tile, and their own tiles, keep the
// admin's sandbox. A restart or resume kept on the copy is confined too.
func TestHomeCopyConfinesAdmins(t *testing.T) {
	m, tiles, store := homeRig(t)
	tiles.set("apps/ws", func(x *homeTile) {})
	tiles.set("apps/bob", func(x *homeTile) { x.owner = "user:bob" })
	views := 0
	m.TermView = func(p auth.Principal) ([]string, map[string][]byte) {
		views++
		return []string{"apps/ws", "apps/bob"}, map[string][]byte{"xbin.json": []byte("{}")}
	}
	root, token, alice := adminPerson("root"), auth.Principal{Owner: true, Via: "bearer"}, person("alice")
	alice.User.Tiles = map[string]string{"apps/*": users.LevelTerminal}
	open := func(p auth.Principal, tile string) openOpts { return m.openOptsFor(p, tile, tile, "none", "", true) }

	for name, p := range map[string]auth.Principal{"an admin": root, "the root token": token} {
		// their own: the admin's sandbox, the real home
		if o := open(p, "apps/ws"); o.restricted || o.rootFiles != nil || o.home.mode != HomeOwn || o.home.confines() {
			t.Errorf("%s on the workspace's tile: restricted %v view %v home %+v", name, o.restricted, o.rootFiles != nil, o.home)
		}
		// a tile they have not trusted: a member's
		o := open(p, "apps/bob")
		if !o.restricted || o.rootFiles == nil || len(o.readable) != 2 || o.home.mode != HomeCopy || !o.home.confines() || !o.home.admin {
			t.Errorf("%s on bob's tile: restricted %v view %v readable %v home %+v", name, o.restricted, o.rootFiles != nil, o.readable, o.home)
		}
		if v := o.home.view(m, false); v == nil || !v.Confined {
			t.Errorf("%s on bob's tile: the row does not say it is confined: %+v", name, v)
		}
	}
	// a member is restricted everywhere, and "confined" is not said to them
	if o := open(alice, "apps/bob"); !o.restricted || o.home.view(m, false).Confined {
		t.Errorf("a member on bob's tile: restricted %v view %+v", o.restricted, o.home.view(m, false))
	}
	// the admin trusts the tile: new sessions there are the admin's again
	trustNow(t, store, tiles, "root", "apps/bob")
	if o := open(root, "apps/bob"); o.restricted || o.rootFiles != nil || o.home.mode != HomeOwn {
		t.Fatalf("an admin on a tile they trusted: restricted %v home %+v", o.restricted, o.home)
	}
	// …but one kept on the copy (a restart, a resume) stays a member's
	o := open(root, "apps/bob")
	m.setHome(root, &o, m.homePlanFor(root, "apps/bob", true))
	if !o.restricted || o.rootFiles == nil || o.home.mode != HomeCopy || !o.home.kept {
		t.Fatalf("an admin's session kept on the copy: restricted %v view %v home %+v", o.restricted, o.rootFiles != nil, o.home)
	}
	if views == 0 {
		t.Fatal("the view was never asked for")
	}
	// without sandboxes nothing is confined (nothing is mounted)
	m.Isolate = false
	if o := open(root, "apps/bob"); o.restricted || o.home.confines() {
		t.Fatalf("no sandboxes: restricted %v", o.restricted)
	}
}

// A session that is being opened is no session yet for a sweep: its home
// was decided before, and it is listed only once its sandbox has started
// (a VM takes a while). A trust that stops holding in between is seen when
// the session registers — its home, and the sign-in it carries — and one
// opened on a copy that is being forgotten ends as it registers.
func TestHomeJudgedAgainAtRegistration(t *testing.T) {
	m, tiles, store := homeRig(t)
	tiles.set("apps/bob", func(x *homeTile) { x.owner = "user:bob" })
	tiles.set("apps/alice", func(x *homeTile) { x.owner = "user:alice" })
	alice := person("alice")
	trustNow(t, store, tiles, "alice", "apps/bob")

	// nothing happened between the plan and the listing: it stays
	calm := liveSession(t, m, alice, "apps/bob")
	m.registered(calm)
	if !alive(m, calm.ID) || !calm.realHome() {
		t.Fatal("a session nothing happened to was ended at its registration")
	}
	// the plan is made (her real home: she trusts the tile) …
	plan := m.homePlanFor(alice, "apps/bob", false)
	if plan.mode != HomeOwn {
		t.Fatalf("the plan: %+v", plan)
	}
	// … then, while the sandbox starts, she takes the trust back; the sweep
	// that follows sees no such session (it ends the one that is listed)
	for _, tr := range store.Trusted("alice") {
		if _, err := store.Untrust("alice", tr.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n := m.EndSigninSessionsOn("alice", "apps/bob", "untrusted"); n != 1 {
		t.Fatalf("the sweep ended %d sessions, want the listed one", n)
	}
	waitGone(t, m, calm.ID)
	// … and the session registers: judged now, ended, told why
	late := fakeSession(t, m, "alice", "apps/bob")
	late.home = sessionHome{plan: plan}
	m.registered(late)
	waitGone(t, m, late.ID)
	if got := string(late.hub.Tail(4096)); !strings.Contains(got, "had your real home") {
		t.Fatalf("the session that registered late was not told why: %q", got)
	}
	// a plan made after the sweep is not judged twice, and one on her own tile never ends
	own := liveSession(t, m, alice, "apps/alice")
	m.HomeSweep()
	m.registered(own)
	if !alive(m, own.ID) {
		t.Fatal("her own tile's session was ended by a later sweep's generation")
	}
	// a session on the copy is never ended by a trust change, late or not
	onCopy := liveSession(t, m, alice, "apps/bob")
	m.HomeSweep()
	m.registered(onCopy)
	if !alive(m, onCopy.ID) {
		t.Fatal("a session on a copy was ended at its registration")
	}
}

// Forget ends the sessions on a copy; a start that is under way is waited
// for and ends itself as it registers — never a session left running on a
// layer that was removed under it. One that doesn't come up in time makes
// the forget fail with nothing removed.
func TestHomeForgetDuringAStart(t *testing.T) {
	m, tiles, _ := homeRig(t)
	tiles.set("apps/bob", func(x *homeTile) { x.owner = "user:bob" })
	alice := person("alice")
	home := HomeDir(m.Root, "alice")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	holds := fakeHolds(m)
	o := openOpts{homeKey: "alice", home: m.homePlanFor(alice, "apps/bob", false)}
	// a first session makes the layer, and ends
	_, dir, _, rel, err := m.homeBind(home, o)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	id := filepath.Base(dir)

	// a start is under way: its hold is starting (the layer's lock is held)
	start := m.copies.start
	starting, goOn := make(chan struct{}), make(chan struct{})
	m.copies.start = func(hs sandbox.HoldSpec) (overlayHold, error) {
		close(starting)
		<-goOn
		return start(hs)
	}
	type bound struct {
		dir string
		rel func()
		err error
	}
	got := make(chan bound, 1)
	go func() {
		_, dir, _, rel, err := m.homeBind(home, o)
		got <- bound{dir, rel, err}
	}()
	<-starting
	old := forgetWait
	forgetWait = 300 * time.Millisecond
	defer func() { forgetWait = old }()
	forgot := make(chan error, 1)
	go func() {
		_, _, err := m.ForgetHomeCopy("alice", id, "forgotten")
		forgot <- err
	}()
	select {
	case err := <-forgot:
		t.Fatalf("forget did not wait for the start that holds the layer: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(goOn) // the hold is up: the start has its clone, and is not a listed session yet
	b := <-got
	if b.err != nil {
		t.Fatalf("the start: %v", b.err)
	}
	// it doesn't register in time: forget fails, and NOTHING was removed — its hold runs, its layer is there
	if err := <-forgot; err == nil || !strings.Contains(err.Error(), "nothing was removed") {
		t.Fatalf("forget with a start in flight: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
		t.Fatalf("the layer was removed under a starting session: %v", err)
	}
	if h := (*holds)[len(*holds)-1]; h.closed.Load() {
		t.Fatal("the starting session's hold was ended under it")
	}

	// again, and this time the session registers while the forget waits: it ends itself, and the copy goes
	forgetWait = 20 * time.Second
	go func() {
		_, _, err := m.ForgetHomeCopy("alice", id, "forgotten")
		forgot <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); !m.copies.forgetting(dir); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("forget never started")
		}
	}
	// a new start on a copy being forgotten is refused
	if _, _, _, _, err := m.homeBind(home, o); err == nil || !strings.Contains(err.Error(), "being forgotten") {
		t.Fatalf("a start on a copy being forgotten: %v", err)
	}
	s := fakeSession(t, m, "alice", "apps/bob")
	s.home = sessionHome{plan: o.home, dir: b.dir}
	go func() { // (its sandbox goes when it ends: the hold is released)
		for alive(m, s.ID) {
			time.Sleep(time.Millisecond)
		}
		b.rel()
	}()
	m.registered(s)
	waitGone(t, m, s.ID)
	if got := string(s.hub.Tail(4096)); !strings.Contains(got, "being forgotten while this session started") {
		t.Fatalf("the session that started during a forget was not told: %q", got)
	}
	if err := <-forgot; err != nil {
		t.Fatalf("forget once the starting session ended: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the forgotten layer is still there: %v", err)
	}
}

// The host's file can forbid trusting with no request to xbind: the home
// rule looks at every judgement, an unusable trust gives the copy, and the
// first time it is seen the sessions that had the real home on a trust end.
func TestHomeTrustSwitchedOffByTheHost(t *testing.T) {
	m, tiles, store := homeRig(t)
	tiles.set("apps/bob", func(x *homeTile) { x.owner = "user:bob" })
	tiles.set("apps/alice", func(x *homeTile) { x.owner = "user:alice" })
	alice := person("alice")
	trustNow(t, store, tiles, "alice", "apps/bob")
	var off atomic.Bool
	m.SigninTrustOff = func() bool { return off.Load() }
	onTrust := liveSession(t, m, alice, "apps/bob")
	if !onTrust.realHome() {
		t.Fatal("a trusted tile's session is not on the real home")
	}
	// the host's file changes; nobody calls a route. The next thing anyone
	// does is open a terminal on her own tile (no sign-in is judged)
	off.Store(true)
	if _, err := store.SetTrustPolicy(false); err != nil {
		t.Fatal(err)
	}
	if pl := m.homePlanFor(alice, "apps/alice", false); pl.mode != HomeOwn {
		t.Fatalf("her own tile: %+v", pl)
	}
	waitGone(t, m, onTrust.ID)
	if pl := m.homePlanFor(alice, "apps/bob", false); pl.mode != HomeCopy || pl.trust == nil || pl.trust.Allowed {
		t.Fatalf("the tile she trusted, with trusting off: %+v", pl)
	}
}

// The sticky condition: a tile that is only hers now, which someone else
// could change before, is not "hers alone" — the copy, until she answers.
func TestHomeRuleRemembersPastEditors(t *testing.T) {
	m, tiles, store := homeRig(t)
	tiles.past = signins.NewPast(filepath.Join(m.Root, "data", "signins", "tiles.json"), nil)
	tiles.set("apps/alice", func(x *homeTile) { x.owner = "user:alice" })
	tiles.set("apps/ws", func(x *homeTile) {})
	alice, root := person("alice"), adminPerson("root")
	if pl := m.homePlanFor(alice, "apps/alice", false); pl.mode != HomeOwn {
		t.Fatalf("her own tile, never shared: %+v", pl)
	}
	if pl := m.homePlanFor(root, "apps/ws", false); pl.mode != HomeOwn {
		t.Fatalf("an admin on the workspace's tile: %+v", pl)
	}
	if tiles.past.Recorded() != 0 {
		t.Fatalf("a workspace nobody shared anything in recorded %d tiles", tiles.past.Recorded())
	}
	own := liveSession(t, m, alice, "apps/alice")
	// mallory gets write, leaves a hook, loses write — seen at the events
	tiles.set("apps/alice", func(x *homeTile) { x.editors = []string{"mallory"} })
	if n := m.HomeSweep(); n != 1 {
		t.Fatalf("mallory gained write: the sweep ended %d real-home sessions", n)
	}
	waitGone(t, m, own.ID)
	tiles.set("apps/alice", func(x *homeTile) { x.editors = nil })
	pl := m.homePlanFor(alice, "apps/alice", false)
	if pl.mode != HomeCopy || !strings.Contains(pl.why, "apps/alice wasn't always only yours — mallory could change it until ") || pl.trust == nil || len(pl.trust.Past) != 1 {
		t.Fatalf("her tile after mallory lost write: %+v trust %+v", pl, pl.trust)
	}
	// her answer — for the card that names mallory — gives it back
	trustNow(t, store, tiles, "alice", "apps/alice")
	if pl := m.homePlanFor(alice, "apps/alice", false); pl.mode != HomeOwn {
		t.Fatalf("after her answer: %+v", pl)
	}
	// a workspace tile a non-admin could edit: the admin's exemption is gone until they answer
	tiles.set("apps/ws", func(x *homeTile) { x.editors = []string{"frank"} })
	m.homePlanFor(root, "apps/ws", false)
	tiles.set("apps/ws", func(x *homeTile) { x.editors = nil })
	if pl := m.homePlanFor(root, "apps/ws", false); pl.mode != HomeCopy || !strings.Contains(pl.why, "frank could change it until") {
		t.Fatalf("a workspace tile frank could edit, for an admin: %+v", pl)
	}
}

// The words every client says.
func TestHomeWords(t *testing.T) {
	if HomeWords(HomeOwn) != "home: yours" || HomeWords(HomeCopy) != "home: this tile's copy (changes stay on this tile)" {
		t.Fatalf("%q / %q", HomeWords(HomeOwn), HomeWords(HomeCopy))
	}
}

// fakeSession puts a live shell session of homeKey's on tile into m: a
// process on a terminal that sleeps, pumped and removed like a real one.
func fakeSession(t *testing.T, m *Manager, homeKey, tile string) *Session {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	f, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{ID: util.RandomToken(8), Cwd: tile, tile: tile, cmd: cmd, pty: f, kind: KindShell, homeKey: homeKey,
		born: time.Now(), hub: termwire.NewHub(maxScrollback)}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	done := make(chan struct{})
	go s.pump(func() { m.remove(s.ID); close(done) })
	t.Cleanup(func() {
		s.kill()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("session %s did not end", s.ID)
		}
	})
	return s
}

func alive(m *Manager, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id] != nil
}

func waitGone(t *testing.T, m *Manager, id string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); alive(m, id); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("session %s is still there", id)
		}
	}
}
