package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/deps"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// goVersionsWorld is the fixture's go command: the lists of each tile's
// builds with the shared go.work ("shared:<tile>") and with its own
// ("own:<tile>"), a pin lifting its own module only. failShared fails a
// tile's shared list, probeErr the go.work's probe, block holds every list
// until the check stops; calls records each list and probe.
type goVersionsWorld struct {
	mu         sync.Mutex
	lists      map[string][]modVer
	failShared map[string]bool
	probeErr   error
	block      bool
	calls      []string
}

func (w *goVersionsWorld) set(key string, mods []modVer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lists[key] = mods
}

// took is the calls made since n, and how many there are now.
func (w *goVersionsWorld) took(n int) ([]string, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.calls[n:]), len(w.calls)
}

func (w *goVersionsWorld) list(ctx context.Context, c *registry.Component, entry string, gowork []byte, pins []deps.Pin) ([]modVer, error) {
	key := "own:" + c.Path
	if bytes.Contains(gowork, []byte("DO NOT EDIT (remove this line")) {
		key = "shared:" + c.Path
	}
	w.mu.Lock()
	w.calls = append(w.calls, key)
	block, fail := w.block, strings.HasPrefix(key, "shared:") && w.failShared[c.Path]
	mods := slices.Clone(w.lists[key])
	w.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if fail {
		return nil, errors.New("go: example.com/dep@v1.2.0: missing go.sum entry")
	}
	for _, p := range pins {
		for i := range mods {
			if mods[i].Path == p.Path && compareSemver(p.Version, mods[i].Version) > 0 {
				mods[i].Version = p.Version
			}
		}
	}
	return mods, nil
}

func (w *goVersionsWorld) probe(ctx context.Context, c *registry.Component, gowork []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, "probe:"+c.Path)
	return w.probeErr
}

// goVersionsFixture is a registry with Go tiles apps/a and apps/b and a
// GoVersions over it whose go command is a goVersionsWorld of lists.
func goVersionsFixture(t *testing.T, lists map[string][]modVer) (*GoVersions, *goVersionsWorld, string) {
	t.Helper()
	root := t.TempDir()
	writeWS(t, root, "xbin.json", `{"schema":1}`)
	for _, tile := range []string{"apps/a", "apps/b"} {
		writeGoTile(t, root, tile)
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	w := &goVersionsWorld{lists: lists, failShared: map[string]bool{}}
	g := &GoVersions{Run: &Runner{Root: root, Reg: reg}, Path: filepath.Join(root, "data", "go-build-versions.json"), Version: "v0.3.65",
		list: w.list, probe: w.probe}
	return g, w, root
}

func writeWS(t *testing.T, root, rel, s string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeGoTile(t *testing.T, root, tile string) {
	t.Helper()
	writeWS(t, root, tile+"/xbin.json", `{"runtime":"go"}`)
	writeWS(t, root, tile+"/go.mod", "module "+filepath.Base(tile)+"\n\ngo 1.22\n")
	writeWS(t, root, tile+"/backend/main.go", "package main\n\nfunc main() {}\n")
}

// upgraded boots g on a workspace an earlier xbind built apps/b in: the
// first pass is due.
func upgraded(t *testing.T, g *GoVersions, root string) {
	t.Helper()
	writeWS(t, root, ".xbin/build/"+util.CompKey("apps/b")+"/bin", "elf")
	if err := g.Boot(); err != nil || !g.due {
		t.Fatalf("an upgraded workspace: due=%v %v", g.due, err)
	}
}

// runPass runs a pass in the foreground, as Start (first) or CheckAll do.
func runPass(g *GoVersions, first bool) {
	g.mu.Lock()
	ctx := g.ctxLocked()
	g.mu.Unlock()
	if g.begin(first) {
		g.pass(ctx, first)
	}
}

func readGoVersionsState(t *testing.T, g *GoVersions) goVersionsState {
	t.Helper()
	b, err := os.ReadFile(g.Path)
	if err != nil {
		t.Fatal(err)
	}
	var st goVersionsState
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// The check runs once on its own: due on the first boot of a workspace an
// earlier xbind built a Go tile in, never on a fresh one (which gets the
// done-marker at once, and has nothing to re-check), not again once done —
// and an interrupted first pass resumes after the tiles it checked. The
// baseline set is the Go tiles at the upgrade, and each compared tile's
// baseline is kept, affected or not.
func TestGoVersionsOnce(t *testing.T) {
	lists := func() map[string][]modVer {
		return map[string][]modVer{
			"shared:apps/a": {{Path: "a"}, {Path: "example.com/dep", Version: "v1.2.0"}},
			"own:apps/a":    {{Path: "a"}, {Path: "example.com/dep", Version: "v1.0.0"}},
			"shared:apps/b": {{Path: "b"}, {Path: "example.com/dep", Version: "v1.2.0"}},
			"own:apps/b":    {{Path: "b"}, {Path: "example.com/dep", Version: "v1.2.0"}},
		}
	}
	// fresh: nothing built
	g, _, _ := goVersionsFixture(t, lists())
	if err := g.Boot(); err != nil {
		t.Fatal(err)
	}
	if st := readGoVersionsState(t, g); !st.Done || !st.Fresh || st.Since != "v0.3.65" || g.due || len(st.Baseline) != 0 {
		t.Fatalf("fresh workspace: %+v due=%v", st, g.due)
	}
	if started, err := g.CheckAll(); started || !errors.Is(err, ErrGoVersionsNothing) {
		t.Errorf("a re-check of a fresh workspace: %v %v", started, err)
	}

	// an upgrade: an earlier xbind built apps/b
	g, w, root := goVersionsFixture(t, lists())
	upgraded(t, g, root)
	st := readGoVersionsState(t, g)
	if st.Done || st.Since != "v0.3.65" || !reflect.DeepEqual(st.Baseline, map[string]*goVersionsBase{"apps/a": nil, "apps/b": nil}) {
		t.Fatalf("upgrade: %+v", st)
	}
	// interrupted after apps/b
	g.st.Checked = []string{"apps/b"}
	if err := g.saveLocked(); err != nil {
		t.Fatal(err)
	}
	g2 := &GoVersions{Run: g.Run, Path: g.Path, Version: "v0.3.66", list: w.list, probe: w.probe}
	if err := g2.Boot(); err != nil || !g2.due || g2.st.Since != "v0.3.65" {
		t.Fatalf("resume: due=%v since=%q %v", g2.due, g2.st.Since, err)
	}
	runPass(g2, true)
	if seen, _ := w.took(0); slices.Contains(seen, "own:apps/b") || !slices.Contains(seen, "shared:apps/a") {
		t.Errorf("the resumed pass listed %q", seen)
	}
	st = readGoVersionsState(t, g2)
	if !st.Done || st.Fresh || len(st.Checked) != 0 || st.Since != "v0.3.65" {
		t.Fatalf("after the pass: %+v", st)
	}
	if e := st.Tiles["apps/a"]; e == nil || !reflect.DeepEqual(e.Require, []string{"example.com/dep v1.2.0"}) || !e.Minimal || e.Inputs == "" {
		t.Fatalf("apps/a's entry: %+v", e)
	}
	if b := st.Baseline["apps/a"]; b == nil || !reflect.DeepEqual(b.Had, []string{"a", "example.com/dep v1.2.0"}) {
		t.Errorf("apps/a's baseline: %+v", b)
	}
	// done: the next boot runs nothing
	g3 := &GoVersions{Run: g.Run, Path: g.Path, Version: "v0.3.67"}
	if err := g3.Boot(); err != nil || g3.due {
		t.Fatalf("after done: due=%v %v", g3.due, err)
	}
	if msg, ok := g3.Alert(); !ok || !strings.HasPrefix(msg, "apps/a builds with older dependency versions since v0.3.65 ") {
		t.Errorf("alert %q %v", msg, ok)
	}
	// a second pass compares an unaffected tile with its baseline too
	g4, w4, root4 := goVersionsFixture(t, lists())
	upgraded(t, g4, root4)
	runPass(g4, true)
	if st := readGoVersionsState(t, g4); st.Baseline["apps/b"] == nil || st.Tiles["apps/b"] != nil {
		t.Errorf("an unaffected tile's baseline: %+v", st)
	}
	_, n := w4.took(0)
	w4.set("own:apps/b", []modVer{{Path: "b"}, {Path: "example.com/dep", Version: "v1.1.0"}})
	runPass(g4, false)
	if calls, _ := w4.took(n); slices.ContainsFunc(calls, func(s string) bool { return strings.HasPrefix(s, "shared:") }) {
		t.Errorf("a re-check listed the shared go.work again: %q", calls)
	}
	if rep := g4.Report(); len(rep.Tiles) != 2 || rep.Tiles[1].Tile != "apps/b" || !reflect.DeepEqual(rep.Tiles[1].Require, []string{"example.com/dep v1.2.0"}) {
		t.Errorf("apps/b lowered since: %+v", rep)
	}
}

// An earlier xbind's build is a work-tree binary or a checkpoint artifact
// (shared or a protected primary's) whose build.json records the shared
// go.work; one D166 built, or one behind a symlink, isn't.
func TestGoVersionsBuiltBefore(t *testing.T) {
	tree := strings.Repeat("ab", 20)
	for _, tc := range []struct {
		name  string
		files map[string]string
		link  bool
		want  bool
	}{
		{"nothing", nil, false, false},
		{"a work-tree binary", map[string]string{".xbin/build/" + util.CompKey("apps/a") + "/bin": "elf"}, false, true},
		{"a checkpoint's", map[string]string{".xbin/build/" + util.CompKey("apps/a") + "/c/" + tree + "/build.json": `{"tile":"apps/a","tree":"` + tree + `"}`}, false, true},
		{"a D166 checkpoint's", map[string]string{".xbin/build/" + util.CompKey("apps/a") + "/c/" + tree + "/build.json": `{"tile":"apps/a","tree":"` + tree + `","workspace":"tile"}`}, false, false},
		{"another tile's", map[string]string{".xbin/build/" + util.CompKey("apps/a") + "/c/" + tree + "/build.json": `{"tile":"apps/z","tree":"` + tree + `"}`}, false, false},
		{"a protected primary's", map[string]string{".xbin/deploy/" + util.TileKey("apps/a") + "/protected/build/" + tree + "/build.json": `{"tile":"apps/a","tree":"` + tree + `"}`}, false, true},
		{"one behind a symlink", map[string]string{"elsewhere/" + tree + "/build.json": `{"tile":"apps/a","tree":"` + tree + `"}`}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeWS(t, root, "xbin.json", `{"schema":1}`)
			writeGoTile(t, root, "apps/a")
			for rel, s := range tc.files {
				writeWS(t, root, rel, s)
			}
			if tc.link {
				base := filepath.Join(root, ".xbin", "build", util.CompKey("apps/a"))
				if err := os.MkdirAll(base, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(base, "c")); err != nil {
					t.Fatal(err)
				}
			}
			reg, err := registry.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			if got := (&Runner{Root: root, Reg: reg}).goBuiltBefore(); got != tc.want {
				t.Errorf("goBuiltBefore = %v", got)
			}
		})
	}
}

// The alert: dismissed until the tile's lines change; a tile's line goes
// when its own go.mod catches up (the re-check after its next build), and
// a build that changed nothing deciding its versions lists nothing.
func TestGoVersionsAlertDismissRecheck(t *testing.T) {
	g, w, root := goVersionsFixture(t, map[string][]modVer{
		"shared:apps/a": {{Path: "a"}, {Path: "example.com/dep", Version: "v1.2.0"}, {Path: "example.com/two", Version: "v0.5.0"}},
		"own:apps/a":    {{Path: "a"}, {Path: "example.com/dep", Version: "v1.0.0"}, {Path: "example.com/two", Version: "v0.4.0"}},
		"shared:apps/b": {{Path: "b"}, {Path: "example.com/dep", Version: "v1.2.0"}, {Path: "example.com/two", Version: "v0.5.0"}},
		"own:apps/b":    {{Path: "b"}, {Path: "example.com/dep", Version: "v1.1.0"}, {Path: "example.com/two", Version: "v0.5.0"}},
	})
	upgraded(t, g, root)
	runPass(g, true)
	msg, ok := g.Alert()
	if !ok || !strings.Contains(msg, "2 Go tiles build with older dependency versions since v0.3.65") ||
		!strings.Contains(msg, "add `require example.com/dep v1.2.0` and `require example.com/two v0.5.0` to apps/a's go.mod") ||
		!strings.Contains(msg, "add `require example.com/dep v1.2.0` to apps/b's go.mod") {
		t.Fatalf("alert %q %v", msg, ok)
	}
	if err := g.Dismiss("apps/x"); err != ErrGoVersionsUnknownTile {
		t.Errorf("dismiss an unknown tile: %v", err)
	}
	if err := g.Dismiss("apps/b"); err != nil {
		t.Fatal(err)
	}
	if msg, _ := g.Alert(); strings.Contains(msg, "apps/b") || !strings.HasPrefix(msg, "apps/a builds") {
		t.Errorf("after dismissing apps/b: %q", msg)
	}
	// a pass with the same lines keeps the dismissal
	runPass(g, false)
	if msg, _ := g.Alert(); strings.Contains(msg, "apps/b") {
		t.Errorf("a pass with the same lines re-raised apps/b: %q", msg)
	}
	// apps/a's go.mod catches up on one of its two: its next build re-checks
	// it against what it had, and the line it still needs is the alert's
	writeWS(t, root, "apps/a/go.mod", "module a\n\ngo 1.22\n\nrequire example.com/dep v1.2.0\n")
	w.set("own:apps/a", []modVer{{Path: "a"}, {Path: "example.com/dep", Version: "v1.2.0"}, {Path: "example.com/two", Version: "v0.4.0"}})
	g.Built("apps/a")
	waitIdle(t, g, "apps/a")
	if e := g.st.Tiles["apps/a"]; e == nil || !reflect.DeepEqual(e.Require, []string{"example.com/two v0.5.0"}) {
		t.Fatalf("after catching up on dep: %+v", e)
	}
	// built again with nothing changed that decides its versions: nothing listed
	_, n := w.took(0)
	g.Built("apps/a")
	waitIdle(t, g, "apps/a")
	g.Built("apps/b") // dismissed, unchanged: the same
	waitIdle(t, g, "apps/b")
	if calls, _ := w.took(n); len(calls) != 0 {
		t.Errorf("builds with the same inputs listed %q", calls)
	}
	// and on the other: gone
	writeWS(t, root, "apps/a/go.mod", "module a\n\ngo 1.22\n\nrequire (\n\texample.com/dep v1.3.0\n\texample.com/two v0.5.0\n)\n")
	w.set("own:apps/a", []modVer{{Path: "a"}, {Path: "example.com/dep", Version: "v1.3.0"}, {Path: "example.com/two", Version: "v0.5.0"}})
	g.Built("apps/a")
	waitIdle(t, g, "apps/a")
	if _, ok := g.Alert(); ok {
		t.Errorf("an alert after both tiles are done or dismissed: %+v", g.Report())
	}
	if rep := g.Report(); len(rep.Tiles) != 1 || rep.Tiles[0].Tile != "apps/b" || !rep.Tiles[0].Dismissed {
		t.Errorf("report %+v", rep)
	}
	// a build of a tile the alert doesn't name lists nothing
	_, n = w.took(0)
	writeWS(t, root, "apps/a/go.mod", "module a\n\ngo 1.22\n\nrequire example.com/dep v1.4.0\n")
	g.Built("apps/a")
	waitIdle(t, g, "apps/a")
	if calls, _ := w.took(n); len(calls) != 0 {
		t.Errorf("a build of a tile with no entry listed %q", calls)
	}
	// lines that change re-raise a dismissed tile
	writeWS(t, root, "apps/b/go.mod", "module b\n\ngo 1.22\n\nrequire example.com/two v0.4.0\n")
	w.set("own:apps/b", []modVer{{Path: "b"}, {Path: "example.com/dep", Version: "v1.1.0"}, {Path: "example.com/two", Version: "v0.4.0"}})
	g.Built("apps/b")
	waitIdle(t, g, "apps/b")
	if msg, _ := g.Alert(); !strings.Contains(msg, "`require example.com/dep v1.2.0` and `require example.com/two v0.5.0` to apps/b's go.mod") {
		t.Errorf("changed lines: %q", msg)
	}
}

// covers G1 review finding 1 — a shared go.work the go command refuses is
// the workspace's error, said once: the probe runs once, no other tile
// lists the same go.work, no tile gets an error of its own, and the next
// pass lists the tiles left without a baseline. A shared list that fails
// for one tile alone (the probe loads the go.work) is that tile's error.
func TestGoVersionsWorkspaceError(t *testing.T) {
	lists := map[string][]modVer{
		"shared:apps/a": {{Path: "a"}, {Path: "example.com/dep", Version: "v1.2.0"}},
		"own:apps/a":    {{Path: "a"}, {Path: "example.com/dep", Version: "v1.0.0"}},
		"shared:apps/b": {{Path: "b"}, {Path: "example.com/dep", Version: "v1.2.0"}},
		"own:apps/b":    {{Path: "b"}, {Path: "example.com/dep", Version: "v1.0.0"}},
	}
	g, w, root := goVersionsFixture(t, lists)
	g.Parallel = 1
	upgraded(t, g, root)
	w.failShared = map[string]bool{"apps/a": true, "apps/b": true}
	w.probeErr = errors.New("go: module ../c listed in go.work file requires go >= 1.27, but go.work lists go 1.24")
	runPass(g, true)
	rep := g.Report()
	if !rep.Done || len(rep.Errors) != 0 || len(rep.Tiles) != 0 ||
		rep.WorkspaceError != "the workspace's shared go.work doesn't load: "+w.probeErr.Error() {
		t.Fatalf("report %+v", rep)
	}
	calls, n := w.took(0)
	slices.Sort(calls)
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "probe:") || !strings.HasPrefix(calls[1], "shared:") {
		t.Errorf("the pass ran %q: one shared list, one probe", calls)
	}
	// fixed: the next pass lists both baselines
	w.mu.Lock()
	w.failShared, w.probeErr = map[string]bool{}, nil
	w.mu.Unlock()
	runPass(g, false)
	if rep := g.Report(); rep.WorkspaceError != "" || len(rep.Tiles) != 2 || len(rep.Errors) != 0 {
		t.Fatalf("after the fix: %+v", rep)
	}
	if calls, _ := w.took(n); !slices.Contains(calls, "shared:apps/a") || !slices.Contains(calls, "shared:apps/b") {
		t.Errorf("the next pass ran %q", calls)
	}

	// one tile's shared list fails, the go.work loads: that tile's error
	g, w, root = goVersionsFixture(t, lists)
	upgraded(t, g, root)
	w.failShared = map[string]bool{"apps/b": true}
	runPass(g, true)
	rep = g.Report()
	if rep.WorkspaceError != "" || len(rep.Tiles) != 1 || rep.Tiles[0].Tile != "apps/a" || len(rep.Errors) != 1 ||
		rep.Errors[0].Tile != "apps/b" || !strings.HasPrefix(rep.Errors[0].Error, "built with the workspace's go.work: go: example.com/dep") {
		t.Fatalf("one tile's failure: %+v", rep)
	}
}

// covers G1 review findings 2 and 3 — a re-check compares only the tiles
// the workspace had at the upgrade (a tile added since never linked the
// shared go.work's versions), and a tile deleted (or no longer Go) leaves
// the alert and the report at once, and the state with the next pass.
func TestGoVersionsBaselineSet(t *testing.T) {
	g, w, root := goVersionsFixture(t, map[string][]modVer{
		"shared:apps/a": {{Path: "a"}, {Path: "example.com/dep", Version: "v1.2.0"}},
		"own:apps/a":    {{Path: "a"}, {Path: "example.com/dep", Version: "v1.0.0"}},
		"shared:apps/b": {{Path: "b"}, {Path: "example.com/dep", Version: "v1.2.0"}},
		"own:apps/b":    {{Path: "b"}, {Path: "example.com/dep", Version: "v1.0.0"}},
		"shared:apps/n": {{Path: "n"}, {Path: "example.com/dep", Version: "v1.2.0"}},
		"own:apps/n":    {{Path: "n"}, {Path: "example.com/dep", Version: "v1.0.0"}},
	})
	upgraded(t, g, root)
	runPass(g, true)
	writeGoTile(t, root, "apps/n")
	if err := os.RemoveAll(filepath.Join(root, "apps", "a")); err != nil {
		t.Fatal(err)
	}
	if err := g.Run.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if msg, ok := g.Alert(); !ok || strings.Contains(msg, "apps/a") || !strings.HasPrefix(msg, "apps/b builds") {
		t.Errorf("the alert after apps/a was deleted: %q", msg)
	}
	if rep := g.Report(); len(rep.Tiles) != 1 || rep.Tiles[0].Tile != "apps/b" {
		t.Errorf("the report after apps/a was deleted: %+v", rep)
	}
	_, n := w.took(0)
	runPass(g, false)
	if calls, _ := w.took(n); slices.ContainsFunc(calls, func(s string) bool { return strings.HasSuffix(s, ":apps/n") || strings.HasSuffix(s, ":apps/a") }) {
		t.Errorf("a re-check listed %q", calls)
	}
	st := readGoVersionsState(t, g)
	if _, ok := st.Baseline["apps/a"]; ok || st.Tiles["apps/a"] != nil || st.Tiles["apps/n"] != nil || st.Tiles["apps/b"] == nil {
		t.Errorf("the state after a re-check: %+v", st)
	}
}

// covers G1 review finding 4 — the first pass doesn't run again when an
// admin's pass completed it during the delay, and Stop ends a running pass
// (its lists' context), records nothing of it, leaves the first pass due
// for the next boot, and starts nothing more.
func TestGoVersionsStartStop(t *testing.T) {
	lists := map[string][]modVer{
		"shared:apps/a": {{Path: "a"}, {Path: "example.com/dep", Version: "v1.2.0"}},
		"own:apps/a":    {{Path: "a"}, {Path: "example.com/dep", Version: "v1.0.0"}},
		"shared:apps/b": {{Path: "b"}, {Path: "example.com/dep", Version: "v1.2.0"}},
		"own:apps/b":    {{Path: "b"}, {Path: "example.com/dep", Version: "v1.2.0"}},
	}
	g, w, root := goVersionsFixture(t, lists)
	upgraded(t, g, root)
	g.Delay = 100 * time.Millisecond
	g.Start()
	if started, err := g.CheckAll(); !started || err != nil {
		t.Fatalf("CheckAll: %v %v", started, err)
	}
	waitPass(t, g)
	_, n := w.took(0)
	time.Sleep(300 * time.Millisecond)
	if calls, _ := w.took(n); len(calls) != 0 {
		t.Errorf("the first pass ran again after an admin's completed it: %q", calls)
	}
	g.Stop()

	g, w, root = goVersionsFixture(t, lists)
	upgraded(t, g, root)
	w.block = true
	if started, err := g.CheckAll(); !started || err != nil {
		t.Fatalf("CheckAll: %v %v", started, err)
	}
	for i := 0; ; i++ {
		if _, n := w.took(0); n > 0 {
			break
		}
		if i > 2000 {
			t.Fatal("no list started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	stopped := make(chan struct{})
	go func() { g.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(30 * time.Second):
		t.Fatal("Stop didn't end the pass")
	}
	if st := readGoVersionsState(t, g); st.Done || len(st.Checked) != 0 || len(st.Tiles) != 0 || len(st.Errors) != 0 || st.Baseline["apps/a"] != nil {
		t.Errorf("a stopped pass recorded %+v", st)
	}
	if started, err := g.CheckAll(); started || err == nil {
		t.Errorf("CheckAll after Stop: %v %v", started, err)
	}
}

// waitPass waits for g's pass to end.
func waitPass(t *testing.T, g *GoVersions) {
	t.Helper()
	for i := 0; i < 2000; i++ {
		g.mu.Lock()
		done := !g.running && g.st.Done
		g.mu.Unlock()
		if done {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the pass never ended")
}

// waitIdle waits for tile's re-check to end.
func waitIdle(t *testing.T, g *GoVersions, tile string) {
	t.Helper()
	for i := 0; i < 2000; i++ {
		g.mu.Lock()
		busy := g.busy[tile]
		g.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s's re-check never ended", tile)
}
