package boot

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/jsonc"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// lrGate answers the watcher loop's questions from maps: a tile absent from
// lr has no record (main, attached); "" is paused. A primary absent from
// primary is main.
type lrGate struct {
	lr      map[string]string
	primary map[string]string
	moved   []string
}

func (g *lrGate) LiveReload(tile string) (string, bool) {
	dep, ok := g.lr[tile]
	if !ok {
		return util.MainDeployment, true
	}
	return dep, dep != ""
}

func (g *lrGate) Primary(tile string) string {
	return cmp.Or(g.primary[tile], util.MainDeployment)
}

func (g *lrGate) WorkTreeMoved(tile string) { g.moved = append(g.moved, tile) }

// lrOut is what one batch drove: the events' wire bytes, the rebuilds
// ("tile@deployment") and the work-tree notices, each sorted.
type lrOut struct {
	events, changed, moved []string
}

// lrDrain reads every event the hub has queued for ch, as wire bytes.
func lrDrain(t *testing.T, ch <-chan events.Event) []string {
	t.Helper()
	var out []string
	for {
		select {
		case e := <-ch:
			b, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, string(b))
		default:
			sort.Strings(out)
			return out
		}
	}
}

// lrRoute runs routeBatch over one batch and records what it drove.
func lrRoute(t *testing.T, reload map[string]*registry.Component, restart map[string]bool, gate liveGate) lrOut {
	t.Helper()
	hub := events.NewHub()
	ch, cancel := hub.Subscribe(nil)
	defer cancel()
	var out lrOut
	routeBatch(reload, restart, gate, hub, func(c *registry.Component, dep string) {
		out.changed = append(out.changed, c.Path+"@"+dep)
	})
	out.events = lrDrain(t, ch)
	sort.Strings(out.changed)
	if g, ok := gate.(*lrGate); ok {
		out.moved = slices.Sorted(slices.Values(g.moved))
	}
	return out
}

// todayBatch is the loop body before live reload could be paused
// (internal/boot/serve.go before 07-runtime §6): a bare reload for every
// changed component, then run.Changed for each one that restarts.
func todayBatch(reload map[string]*registry.Component, restart map[string]bool, hub *events.Hub, changed func(*registry.Component)) {
	for _, c := range reload {
		slog.Debug("changed", "component", c.Path)
		hub.Publish(events.Event{Type: "reload", Component: c.Path})
		if restart[c.Path] {
			changed(c)
		}
	}
}

// lrToday records what todayBatch drove; a zero-state rebuild is main's.
func lrToday(t *testing.T, reload map[string]*registry.Component, restart map[string]bool) lrOut {
	t.Helper()
	hub := events.NewHub()
	ch, cancel := hub.Subscribe(nil)
	defer cancel()
	var out lrOut
	todayBatch(reload, restart, hub, func(c *registry.Component) {
		out.changed = append(out.changed, c.Path+"@"+util.MainDeployment)
	})
	out.events = lrDrain(t, ch)
	sort.Strings(out.changed)
	return out
}

func lrComps(paths ...string) map[string]*registry.Component {
	m := map[string]*registry.Component{}
	for _, p := range paths {
		m[p] = &registry.Component{Path: p}
	}
	return m
}

func lrSet(paths ...string) map[string]bool {
	m := map[string]bool{}
	for _, p := range paths {
		m[p] = true
	}
	return m
}

func lrReloadEv(tile string) string { return `{"type":"reload","component":"` + tile + `"}` }

func lrDepReloadEv(tile, dep string) string {
	return `{"type":"deployments","component":"` + tile + `","data":{"op":"reload","deployment":"` + dep + `"}}`
}

// covers D119c D119d C2 PO-5 NP-13-12 — the live reload gate (07-runtime §6): one
// batch's changed components map to the events published, the deployments
// rebuilt and the paused tiles noticed. A tile without a record gets exactly
// today's bare reload and rebuild; a paused one no event and no rebuild,
// only the plane's work-tree notice; a live reload target other than the
// primary a deployments op reload naming it and a rebuild of that deployment
// alone, never an event of today's types; a nested tile is gated on its own
// record, apart from its parent.
func TestReloadPlan(t *testing.T) {
	cases := []struct {
		name    string
		reload  map[string]*registry.Component
		restart map[string]bool
		gate    *lrGate
		want    lrOut
	}{
		{name: "zero state", reload: lrComps("apps/a", "apps/b", "notes"), restart: lrSet("apps/a", "notes"), gate: &lrGate{},
			want: lrOut{events: []string{lrReloadEv("apps/a"), lrReloadEv("apps/b"), lrReloadEv("notes")}, changed: []string{"apps/a@main", "notes@main"}}},
		{name: "paused", reload: lrComps("apps/a"), restart: lrSet("apps/a"), gate: &lrGate{lr: map[string]string{"apps/a": ""}},
			want: lrOut{moved: []string{"apps/a"}}},
		{name: "live reload on dev", reload: lrComps("apps/a"), restart: lrSet("apps/a"), gate: &lrGate{lr: map[string]string{"apps/a": "dev"}},
			want: lrOut{events: []string{lrDepReloadEv("apps/a", "dev")}, changed: []string{"apps/a@dev"}}},
		{name: "native-only edit on dev", reload: lrComps("apps/a"), restart: lrSet(), gate: &lrGate{lr: map[string]string{"apps/a": "dev"}},
			want: lrOut{events: []string{lrDepReloadEv("apps/a", "dev")}}},
		{name: "nested tile paused, parent zero state", reload: lrComps("apps/a", "apps/a/b"), restart: lrSet("apps/a", "apps/a/b"),
			gate: &lrGate{lr: map[string]string{"apps/a/b": ""}},
			want: lrOut{events: []string{lrReloadEv("apps/a")}, changed: []string{"apps/a@main"}, moved: []string{"apps/a/b"}}},
		{name: "parent paused, nested tile on dev", reload: lrComps("apps/a", "apps/a/b"), restart: lrSet("apps/a", "apps/a/b"),
			gate: &lrGate{lr: map[string]string{"apps/a": "", "apps/a/b": "dev"}},
			want: lrOut{events: []string{lrDepReloadEv("apps/a/b", "dev")}, changed: []string{"apps/a/b@dev"}, moved: []string{"apps/a"}}},
		{name: "static tile paused", reload: lrComps("notes"), restart: lrSet("notes"), gate: &lrGate{lr: map[string]string{"notes": ""}},
			want: lrOut{moved: []string{"notes"}}},
		{name: "live reload on a reassigned primary", reload: lrComps("apps/a"), restart: lrSet("apps/a"),
			gate: &lrGate{lr: map[string]string{"apps/a": "prod"}, primary: map[string]string{"apps/a": "prod"}},
			want: lrOut{events: []string{lrReloadEv("apps/a")}, changed: []string{"apps/a@prod"}}},
		{name: "live reload on main while another deployment is primary", reload: lrComps("apps/a"), restart: lrSet("apps/a"),
			gate: &lrGate{lr: map[string]string{"apps/a": "main"}, primary: map[string]string{"apps/a": "prod"}},
			want: lrOut{events: []string{lrDepReloadEv("apps/a", "main")}, changed: []string{"apps/a@main"}}},
		{name: "a mixed batch", reload: lrComps("apps/a", "apps/b", "apps/c"), restart: lrSet("apps/a", "apps/b", "apps/c"),
			gate: &lrGate{lr: map[string]string{"apps/b": "", "apps/c": "dev"}},
			want: lrOut{events: []string{lrDepReloadEv("apps/c", "dev"), lrReloadEv("apps/a")}, changed: []string{"apps/a@main", "apps/c@dev"}, moved: []string{"apps/b"}}},
		{name: "empty batch", reload: lrComps(), restart: lrSet(), gate: &lrGate{}, want: lrOut{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := lrRoute(t, c.reload, c.restart, c.gate)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("drove\n  %+v\nwant\n  %+v", got, c.want)
			}
			if len(c.gate.lr) == 0 && len(c.gate.primary) == 0 {
				if old := lrToday(t, c.reload, c.restart); !reflect.DeepEqual(got, old) {
					t.Errorf("zero state drove\n  %+v\ntoday's loop drives\n  %+v", got, old)
				}
			}
		})
	}

	// A save is judged by the work tree's manifest, not the pinned
	// primary's: the work tree names mobile/main.js its native entry while
	// the checkpoint keeps the native.js convention, so an edit of
	// mobile/main.js is native-only for a live reload target on the work tree.
	t.Run("native entry judged by the work tree", func(t *testing.T) {
		ws := t.TempDir()
		lrWriteFiles(t, ws, map[string]string{
			"apps/cal/xbin.json":      `{"runtime":"go","native":"./mobile/main.js"}`,
			"apps/cal/mobile/main.js": `export {}`,
			"apps/cal/native.js":      `export {}`,
		})
		var pinned registry.Manifest
		if err := jsonc.Unmarshal([]byte(`{"runtime":"go"}`), &pinned); err != nil {
			t.Fatal(err)
		}
		reg := &registry.Registry{Root: ws, PinnedPrimary: func(rel string) (*registry.PinnedCode, bool) {
			return &registry.PinnedCode{Manifest: pinned}, rel == "apps/cal"
		}}
		if err := reg.Rescan(); err != nil {
			t.Fatal(err)
		}
		if c, _ := reg.Component("apps/cal"); c == nil || c.WorkTree == nil || c.NativeEntryName() != registry.NativeConvention {
			t.Fatalf("apps/cal isn't composed from its pinned primary: %+v", c)
		}
		for _, row := range []struct {
			path    string
			restart bool
		}{{"apps/cal/mobile/main.js", false}, {"apps/cal/native.js", true}} {
			reload, restart := changedComponents(reg, []string{row.path})
			if reload["apps/cal"] == nil || restart["apps/cal"] != row.restart {
				t.Errorf("%s: reload %v restart %v, want apps/cal restart=%v", row.path, reload, restart, row.restart)
			}
		}
		gate := &lrGate{lr: map[string]string{"apps/cal": "dev"}, primary: map[string]string{"apps/cal": "main"}}
		reload, restart := changedComponents(reg, []string{"apps/cal/mobile/main.js"})
		want := lrOut{events: []string{lrDepReloadEv("apps/cal", "dev")}}
		if got := lrRoute(t, reload, restart, gate); !reflect.DeepEqual(got, want) {
			t.Errorf("a native-only edit on dev drove %+v, want %+v", got, want)
		}
	})
}

// lrStoreTrap is the deployments plane with its store behind a tripwire:
// LiveReload and Primary are the plane's in-memory answers, and a work-tree
// notice — the only way a save reaches a drift count, the checkpoint store
// or a confined run — fails the test.
type lrStoreTrap struct {
	*deployments.Plane
	t     *testing.T
	calls int
}

func (s *lrStoreTrap) WorkTreeMoved(tile string) {
	s.calls++
	s.t.Errorf("a save in zero-state tile %s reached the checkpoint store", tile)
}

// covers D119c D119d PO-12 SC-LATENCY-DEFAULT — a save in zero-state tiles reads
// only memory: through the real deployments plane, booted on a workspace
// without records, a batch reaches exactly today's bare reloads and rebuilds,
// never the work-tree notice behind which the checkpoint store sits, and
// needs no disk at all: the workspace is moved away before the batch, and
// nothing reappears at its path. The plane's own notice and state read
// answer nothing for those tiles either.
func TestWatchLoopZeroStateNoStoreIO(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "ws")
	lrWriteFiles(t, ws, map[string]string{
		"apps/counter/xbin.json":       `{"runtime":"go"}`,
		"apps/counter/backend/main.go": `package main`,
		"apps/counter/native.js":       `export {}`,
		"apps/a/xbin.json":             `{"runtime":"node"}`,
		"apps/a/b/xbin.json":           `{"runtime":"static"}`,
		"apps/a/b/index.html":          `<p>b</p>`,
		"notes/index.html":             `<p>notes</p>`,
	})
	reg, err := registry.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	hub := events.NewHub()
	dp := &deployments.Plane{Root: ws, Reg: reg, Hub: hub}
	if err := dp.Boot(); err != nil {
		t.Fatal(err)
	}
	reg.PinnedPrimary = dp.PinnedPrimary
	trap := &lrStoreTrap{Plane: dp, t: t}
	paths := []string{
		"apps/counter/native.js", "apps/a/x.js",
		"apps/a/b/index.html", "notes/index.html", "notes/more.html", "outside/file.txt",
	}

	away := ws + ".away"
	if err := os.Rename(ws, away); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(away, ws) })

	reload, restart := changedComponents(reg, paths)
	got := lrRoute(t, reload, restart, trap)
	want := lrToday(t, reload, restart)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("zero-state batch drove\n  %+v\ntoday's loop drives\n  %+v", got, want)
	}
	if len(want.events) != 4 || !reflect.DeepEqual(want.changed, []string{"apps/a/b@main", "apps/a@main", "notes@main"}) {
		t.Errorf("today's loop drove %+v: the fixture no longer exercises reloads and a native-only edit", want)
	}
	for tile := range reload {
		dp.WorkTreeMoved(tile)
		if d, ok := dp.WorkTreeDrift(tile); ok {
			t.Errorf("%s: zero-state tile reports work-tree drift %+v", tile, d)
		}
	}
	if trap.calls != 0 {
		t.Errorf("%d work-tree notices for zero-state tiles", trap.calls)
	}
	if _, err := os.Lstat(ws); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the batch touched the workspace's path (lstat: %v): a save in a zero-state tile must not need the disk", err)
	}
}

func lrWriteFiles(t testing.TB, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// todayChangedComponents is changedComponents before a save was judged by
// the work tree's view: the baseline of BenchmarkWatchBatch.
func todayChangedComponents(reg *registry.Registry, paths []string) (map[string]*registry.Component, map[string]bool) {
	reload, restart := map[string]*registry.Component{}, map[string]bool{}
	for _, p := range paths {
		c, rest, ok := reg.Resolve(p)
		if !ok {
			continue
		}
		reload[c.Path] = c
		if !c.NativeOnlyChange(rest) {
			restart[c.Path] = true
		}
	}
	return reload, restart
}

// BenchmarkWatchBatch is the save path of 07-runtime §13.1: a 1 000-path
// batch over 40 tiles of a workspace without deployment records, mapped to
// components and routed, with today's loop body as the baseline ("today")
// and the gated one ("gated"), which must stay within 5 % of it.
func BenchmarkWatchBatch(b *testing.B) {
	ws := b.TempDir()
	files := map[string]string{}
	var paths []string
	for i := range 40 {
		tile := fmt.Sprintf("apps/t%02d", i)
		files[tile+"/xbin.json"] = `{"runtime":"go"}`
		for j := range 25 {
			paths = append(paths, fmt.Sprintf("%s/src/f%02d.go", tile, j))
		}
	}
	lrWriteFiles(b, ws, files)
	reg, err := registry.Open(ws)
	if err != nil {
		b.Fatal(err)
	}
	hub := events.NewHub()
	dp := &deployments.Plane{Root: ws, Reg: reg, Hub: hub}
	if err := dp.Boot(); err != nil {
		b.Fatal(err)
	}
	reg.PinnedPrimary = dp.PinnedPrimary
	nop := func(*registry.Component) {}
	// run.ChangedDeployment rebuilds only the primary, which it looks up.
	nopDep := func(c *registry.Component, dep string) {
		if dep == dp.Primary(c.Path) {
			nop(c)
		}
	}

	b.Run("today", func(b *testing.B) {
		for b.Loop() {
			reload, restart := todayChangedComponents(reg, paths)
			todayBatch(reload, restart, hub, nop)
		}
	})
	b.Run("gated", func(b *testing.B) {
		for b.Loop() {
			reload, restart := changedComponents(reg, paths)
			routeBatch(reload, restart, dp, hub, nopDep)
		}
	})
}
