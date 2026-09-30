package runner

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/util"
)

// ckTree names a checkpoint tree in these tests: a full tree id.
func ckTree(c byte) string { return strings.Repeat(string(c), 40) }

// ckWorkspace is a workspace of registered components (each path gets an
// xbin.json with the given manifest), with CodeFor answering pinned for the
// paths in pins (their primary's tree) and Materialize answering each tree's
// directory under .xbin/deploy. asked records every path the hooks were
// asked about.
type ckWorkspace struct {
	root  string
	reg   *registry.Registry
	pins  map[string]string
	asked map[string]int
}

func newCkWorkspace(t *testing.T, comps map[string]string, pins map[string]string) *ckWorkspace {
	t.Helper()
	w := &ckWorkspace{root: t.TempDir(), pins: pins, asked: map[string]int{}}
	for p, m := range comps {
		dir := filepath.Join(w.root, filepath.FromSlash(p))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "xbin.json"), []byte(m), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(w.root)
	if err != nil {
		t.Fatal(err)
	}
	w.reg = reg
	return w
}

func (w *ckWorkspace) dir(p string) string { return filepath.Join(w.root, filepath.FromSlash(p)) }

func (w *ckWorkspace) mat(p, tree string) string {
	return filepath.Join(w.root, ".xbin", "deploy", util.TileKey(p), tree)
}

// runner is an isolated Runner over the workspace with the hooks wired.
func (w *ckWorkspace) runner() *Runner {
	r := &Runner{Root: w.root, RunDir: lsRun, Isolate: true, Rootfs: filepath.Join(w.root, "rootfs"), Reg: w.reg, states: map[string]*state{}}
	r.CodeFor = func(tile, dep string) (Code, error) {
		w.asked[tile]++
		if tr, ok := w.pins[tile]; ok {
			return Code{Tree: tr}, nil
		}
		return Code{WorkTree: true}, nil
	}
	r.Materialize = func(tile, tree string) (string, error) { return w.mat(tile, tree), nil }
	return r
}

// view is the deployment view of p's pinned code.
func (w *ckWorkspace) view(p, runtime string) *registry.Component {
	return &registry.Component{Path: p, Dir: w.dir(p), CodeRoot: w.mat(p, w.pins[p]), Manifest: registry.Manifest{Runtime: runtime}}
}

// covers D119e D119g T18 PO-11 — a pinned or non-primary launch spec binds the
// materialized checkpoint at c.Dir, read-only by the bind flag, and each
// component nested in it after the code bind from its own primary's code; the
// live reload target keeps {Src: c.Dir, Dst: c.Dir, RO: true} and binds
// nothing more, whatever the tile's nested components run.
func TestLaunchSpecBinds(t *testing.T) {
	const a = "apps/a"
	w := newCkWorkspace(t, map[string]string{
		a:                     `{"runtime":"go"}`,
		a + "/pin":            `{}`, // pinned: its checkpoint
		a + "/pin/deep":       `{}`, // on its work tree, in a checkpoint that excludes it: bound
		a + "/sub":            `{}`, // on its work tree: bound, since a's checkpoint excludes it
		a + "/sub/inner":      `{}`, // pinned inside a work tree: its checkpoint over it
		a + "/sub/plain":      `{}`, // on its work tree inside a work tree: nothing to bind
		"apps/other":          `{}`, // not nested: never asked about
		"apps/other/x":        `{}`,
		"apps/a-sibling-name": `{}`, // a path prefix, not a child
	}, map[string]string{a: ckTree('a'), a + "/pin": ckTree('b'), a + "/sub/inner": ckTree('c'), "apps/other": ckTree('d')})
	r := w.runner()
	env := []string{"XBIN_COMPONENT=" + a}
	bin := filepath.Join(w.root, ".xbin/build", util.CompKey(a), "c", ckTree('a'), "bin")
	gw := filepath.Join(lsRun, "gateway.sock")
	runDir := []sandbox.Bind{{Src: lsDir, Dst: lsDir}, {Src: gw, Dst: gw}}

	t.Run("pinned: the checkpoint at the canonical path, nested components after it", func(t *testing.T) {
		v := w.view(a, "go")
		nested, err := r.nestedBinds(v)
		if err != nil {
			t.Fatal(err)
		}
		spec := r.launchSpec(v, bin, lsDir, env, sandbox.EgressPolicy{}, "", nested...)
		want := []sandbox.Bind{
			{Src: w.mat(a, ckTree('a')), Dst: w.dir(a), RO: true},
			{Src: w.mat(a+"/pin", ckTree('b')), Dst: w.dir(a + "/pin"), RO: true},
			{Src: w.dir(a + "/pin/deep"), Dst: w.dir(a + "/pin/deep"), RO: true},
			{Src: w.dir(a + "/sub"), Dst: w.dir(a + "/sub"), RO: true},
			{Src: w.mat(a+"/sub/inner", ckTree('c')), Dst: w.dir(a + "/sub/inner"), RO: true},
		}
		want = append(want, runDir...)
		want = append(want, sandbox.Bind{Src: bin, Dst: "/run/backend", RO: true})
		if !reflect.DeepEqual(spec.Binds, want) {
			t.Errorf("binds:\n got %+v\nwant %+v", spec.Binds, want)
		}
		if spec.Cwd != w.dir(a) || spec.Entry != "/run/backend" {
			t.Errorf("cwd %q entry %q: the code runs at its canonical path", spec.Cwd, spec.Entry)
		}
		for _, b := range spec.Binds {
			if b.Src == w.dir(a) || (strings.HasPrefix(b.Dst, w.dir(a)) && !b.RO) {
				t.Errorf("pinned code binds the work tree, or its path read-write: %+v", b)
			}
		}
		for _, p := range []string{"apps/other", "apps/other/x", "apps/a-sibling-name"} {
			if w.asked[p] != 0 {
				t.Errorf("%s isn't nested in %s but its code was looked up", p, a)
			}
		}
	})

	t.Run("node: the entry at its canonical path, the checkpoint under it", func(t *testing.T) {
		v := w.view(a, "node")
		entry := filepath.Join(w.dir(a), "backend/server.js")
		spec := r.launchSpec(v, entry, lsDir, env, sandbox.EgressPolicy{}, "")
		if spec.Binds[0] != (sandbox.Bind{Src: w.mat(a, ckTree('a')), Dst: w.dir(a), RO: true}) {
			t.Errorf("code bind %+v", spec.Binds[0])
		}
		if !reflect.DeepEqual(spec.Argv, []string{"node", entry}) {
			t.Errorf("argv %q", spec.Argv)
		}
	})

	workTree := []sandbox.Bind{{Src: w.dir(a), Dst: w.dir(a), RO: true}}
	for _, tc := range []struct {
		name string
		c    *registry.Component
	}{
		{"the live reload target keeps today's bind", &registry.Component{Path: a, Dir: w.dir(a), Manifest: registry.Manifest{Runtime: "go"}}},
		{"a non-primary deployment on the work tree too", &registry.Component{Path: a, Dir: w.dir(a), Deployment: "dev", Manifest: registry.Manifest{Runtime: "go"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			delete(w.asked, a+"/pin")
			nested, err := r.nestedBinds(tc.c)
			if err != nil || nested != nil {
				t.Fatalf("the work tree shows its nested components itself: %v %v", nested, err)
			}
			if w.asked[a+"/pin"] != 0 {
				t.Error("a work-tree launch looked up a nested component's code")
			}
			spec := r.launchSpec(tc.c, filepath.Join(w.root, ".xbin/build", util.CompKey(a), "bin"), lsDir, env, sandbox.EgressPolicy{}, "", nested...)
			if want := append(append([]sandbox.Bind{}, workTree...), runDir...); !reflect.DeepEqual(spec.Binds[:3], want) {
				t.Errorf("binds %+v, want %+v first", spec.Binds, want)
			}
		})
	}

	t.Run("a nested component whose code can't be had fails the start", func(t *testing.T) {
		r := w.runner()
		r.Materialize = func(tile, tree string) (string, error) {
			if tile == a+"/pin" {
				return "", errors.New("store unreachable")
			}
			return w.mat(tile, tree), nil
		}
		if _, err := r.nestedBinds(w.view(a, "go")); err == nil || !strings.Contains(err.Error(), a+"/pin") {
			t.Fatalf("got %v, want an error naming %s", err, a+"/pin")
		}
		if _, _, err := r.sandboxCmd(w.view(a, "go"), bin, lsDir, lsDir+"/g1.sock", env, sandbox.EgressPolicy{}, ""); err == nil {
			t.Fatal("sandboxCmd launched without the nested component's code")
		}
	})
}

// covers T17 D119e — the setup run of pinned code sees its checkpoint at the
// tile's canonical path, never the work tree, and runs the view's setup;
// the live reload target's setup run keeps today's spec.
func TestSetupLayerSeesCheckpointNotWorkTree(t *testing.T) {
	root := t.TempDir()
	dir, mat := filepath.Join(root, "apps/x"), filepath.Join(root, ".xbin/deploy/k/"+ckTree('a'))
	r := &Runner{Root: root, Isolate: true, Rootfs: filepath.Join(root, "rootfs")}
	wt := &registry.Component{Path: "apps/x", Dir: dir, Manifest: registry.Manifest{Runtime: "go", Setup: "pip install -r requirements.txt # work tree"}}
	pinned := &registry.Component{Path: "apps/x", Dir: dir, CodeRoot: mat, Manifest: registry.Manifest{Runtime: "go", Setup: "pip install -r requirements.txt"}}

	spec := r.envSetupSpec(pinned, "/u", "/w")
	if want := []sandbox.Bind{{Src: mat, Dst: dir, RO: true}}; !reflect.DeepEqual(spec.Binds, want) {
		t.Errorf("setup binds %+v, want %+v", spec.Binds, want)
	}
	if spec.Cwd != dir || spec.Argv[2] != pinned.Manifest.Setup {
		t.Errorf("cwd %q argv %q: the checkpoint's setup at the canonical path", spec.Cwd, spec.Argv)
	}
	if r.envLayerDir(pinned) == r.envLayerDir(wt) {
		t.Error("the layer is keyed by the work tree's setup, not the view's")
	}

	spec = r.envSetupSpec(wt, "/u", "/w")
	want := &sandbox.Spec{
		Lower: []string{r.Rootfs}, Upper: "/u", Work: "/w",
		Binds: []sandbox.Bind{{Src: dir, Dst: dir, RO: true}},
		Entry: "/bin/sh", Argv: []string{"sh", "-exc", wt.Manifest.Setup},
		Env:     []string{envSetupPATH, "HOME=/root", "LANG=C.UTF-8", "DEBIAN_FRONTEND=noninteractive", "XBIN_COMPONENT=apps/x"},
		Cwd:     dir,
		HostUID: os.Getuid(), HostGID: os.Getgid(), Net: "relay",
		NoFollow: true, FollowBase: true, // master's setup spec (WP-2b)
	}
	if !reflect.DeepEqual(spec, want) {
		t.Errorf("the work tree's setup spec changed:\n got %+v\nwant %+v", spec, want)
	}
}

// covers T17 SC-ROLLBACK — env layers are shared by hash (the same setup in
// the work tree and in a checkpoint is one layer, another setup another),
// and GC keeps every layer still referenced: the one just built, the running
// generation's, the pinned primary's and the work tree's; only an
// unreferenced one goes.
func TestEnvLayerGCKeepsReferenced(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "apps/x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "xbin.json"), []byte(`{"runtime":"go","setup":"apk add jq"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := &registry.Registry{Root: root, PinnedPrimary: func(rel string) (*registry.PinnedCode, bool) {
		if rel != "apps/x" {
			return nil, false
		}
		return &registry.PinnedCode{Manifest: registry.Manifest{Runtime: "go", Setup: "apk add curl"}}, true
	}}
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Root: root, Isolate: true, Rootfs: filepath.Join(root, "rootfs"), Reg: reg, states: map[string]*state{}}
	view := func(setup, codeRoot string) *registry.Component {
		return &registry.Component{Path: "apps/x", Dir: dir, CodeRoot: codeRoot, Manifest: registry.Manifest{Runtime: "go", Setup: setup}}
	}
	if a, b := r.envLayerDir(view("apk add jq", "")), r.envLayerDir(view("apk add jq", "/ckpt")); a != b {
		t.Errorf("the same setup in two deployments: %q and %q, want one layer", a, b)
	}
	if a, b := r.envLayerDir(view("apk add jq", "")), r.envLayerDir(view("apk add curl", "/ckpt")); a == b {
		t.Error("another setup shares a layer")
	}

	workTree, pinned := r.setupHash("apk add jq"), r.setupHash("apk add curl")
	running, built, stale := r.setupHash("apk add git"), r.setupHash("apk add make"), r.setupHash("apk add gcc")
	r.states["apps/x"] = &state{comp: "apps/x", cur: &instance{envHash: running}}
	base := filepath.Join(root, ".xbin/env", util.CompKey("apps/x"))
	for _, h := range []string{workTree, pinned, running, built, stale} {
		if err := os.MkdirAll(filepath.Join(base, h, "upper"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.gcEnvLayers(view("apk add make", ""), r.envKeep(view("apk add make", ""), built))
	for h, want := range map[string]bool{workTree: true, pinned: true, running: true, built: true, stale: false} {
		if _, err := os.Stat(filepath.Join(base, h)); (err == nil) != want {
			t.Errorf("layer %s kept=%v, want %v", h, err == nil, want)
		}
	}
}

// covers D119h T12 — pinned backend code never builds or starts without a
// sandbox: with isolation off buildCode refuses every backend runtime with
// the contract's reason and writes nothing; with isolation on but no sandbox
// configured, the checkpoint's Go build is refused by confine (a direct run
// can't show the checkpoint at the tile's path), never run on the work tree.
func TestCheckpointBuildNeedsIsolation(t *testing.T) {
	w := newCkWorkspace(t, map[string]string{"apps/x": `{"runtime":"go"}`}, map[string]string{"apps/x": ckTree('a')})
	r := w.runner()
	r.Isolate = false
	for _, rt := range []string{"go", "node", "python"} {
		if _, err := r.buildCode(w.view("apps/x", rt), Code{Tree: ckTree('a')}); !errors.Is(err, errPinnedNeedsIsolation) {
			t.Errorf("%s without isolation: %v", rt, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(w.root, ".xbin/build")); err == nil {
		t.Error("a refused build wrote under .xbin/build")
	}
	if _, err := r.buildCode(w.view("apps/x", "go"), Code{Tree: "../../etc"}); err == nil {
		t.Error("a tree that isn't a full id was built")
	}
	if confine.Isolated() {
		t.Fatal("confinement is on in a unit test")
	}
	if _, err := hostToolchain(); err != nil {
		t.Skip("no go toolchain:", err)
	}
	r.Isolate = true
	if err := os.MkdirAll(w.mat("apps/x", ckTree('a')), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := r.buildCode(w.view("apps/x", "go"), Code{Tree: ckTree('a')})
	if !errors.Is(err, confine.ErrNeedsIsolation) {
		t.Fatalf("a checkpoint build without a sandbox: %v, want confine.ErrNeedsIsolation", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(w.root, ".xbin/build", util.CompKey("apps/x"), "c")); len(ents) != 0 {
		t.Errorf("the refused build left %v", ents)
	}
}

// covers D119e NP-07-4 T11 D166 — what a checkpoint build of apps/x sees
// beyond its own tree: its own go.work, made from the checkpoint's go.mod
// and code, with each module it reaches and the code it builds against (a
// tile whose primary is pinned is shown from its checkpoint, over its
// directory, and read there — where that checkpoint keeps its go.mod, the
// root or backend/; one on its work tree is the workspace's own); a nested
// component bound back from its primary's code; a Go tile x doesn't reach
// isn't in its go.work (though its primary is looked up, as every Go
// tile's is); the workspace's go.work isn't read; go.sum's pins.
func TestCheckpointBuildPlan(t *testing.T) {
	w := newCkWorkspace(t, map[string]string{
		"apps/x":     `{"runtime":"go"}`,
		"apps/x/sub": `{}`,
		"apps/y":     `{}`,
		"apps/z":     `{}`,
		"apps/w":     `{}`,
	}, map[string]string{"apps/x": ckTree('a'), "apps/y": ckTree('b')})
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	// xbind's own root go.work lists every module; no build reads it
	write(filepath.Join(w.root, "go.work"), "// Code generated by xbind; DO NOT EDIT (remove this line to take ownership).\n\ngo 1.24\n\nuse (\n\t./apps/w\n\t./apps/x\n\t./apps/x/sub\n\t./apps/y/backend\n\t./apps/z\n)\n")
	write(w.dir("apps/x/sub/go.mod"), "module sub\n")
	write(w.dir("apps/y/backend/go.mod"), "module y\n") // the work tree keeps it in backend/
	write(w.dir("apps/z/go.mod"), "module z\n")
	write(w.dir("apps/w/go.mod"), "module w\n\nreplace golang.org/x/sys => ./evil\n")
	matX, matY := w.mat("apps/x", ckTree('a')), w.mat("apps/y", ckTree('b'))
	write(filepath.Join(matX, "go.mod"), "module example.com/x\n\nrequire y v0.0.0\n")
	write(filepath.Join(matX, "backend", "main.go"), "package main\n\nimport (\n\t\"sub/p\"\n\t\"z\"\n)\n\nfunc main() { p.P(); z.Z() }\n")
	write(filepath.Join(matX, "go.sum"), "example.com/m v1.2.0 h1:x=\nexample.com/m v1.2.0/go.mod h1:y=\nexample.com/n v0.1.0/go.mod h1:z=\n")
	write(filepath.Join(matY, "go.mod"), "module y\n") // at the root, where the work tree has backend/

	r := w.runner()
	plan, err := r.checkpointPlan(w.view("apps/x", "go"), ckTree('a'), matX)
	if err != nil {
		t.Fatal(err)
	}
	wantBinds := []sandbox.Bind{
		confine.At(w.dir("apps/x/sub"), w.dir("apps/x/sub"), true),
		confine.At(matY, w.dir("apps/y"), true),
	}
	if !reflect.DeepEqual(plan.binds, wantBinds) {
		t.Errorf("binds:\n got %+v\nwant %+v", plan.binds, wantBinds)
	}
	wantMods := []buildModule{
		{Use: "./apps/x", Code: ckTree('a')},
		{Use: "./apps/x/sub", Code: "worktree"},
		{Use: "./apps/y", Code: ckTree('b')},
		{Use: "./apps/z", Code: "worktree"},
	}
	if !reflect.DeepEqual(plan.modules, wantMods) {
		t.Errorf("modules:\n got %+v\nwant %+v", plan.modules, wantMods)
	}
	if plan.work == nil {
		t.Fatal("no build workspace")
	}
	gw := string(plan.work.GoWork)
	if !strings.Contains(gw, "\t"+w.dir("apps/y")+"\n") || strings.Contains(gw, "apps/w") || strings.Contains(gw, "evil") || strings.Contains(gw, "apps/y/backend") {
		t.Errorf("the build's go.work:\n%s", gw)
	}
	if want := []string{"example.com/m@v1.2.0", "example.com/n@v0.1.0"}; !reflect.DeepEqual(plan.sum, want) {
		t.Errorf("go.sum pins %q, want %q", plan.sum, want)
	}
	if w.asked["apps/z"] == 0 || w.asked["apps/y"] == 0 || w.asked["apps/w"] == 0 {
		t.Error("a Go module's primary wasn't looked up")
	}

	// y's primary moves to a checkpoint keeping its module in backend/
	w.pins["apps/y"] = ckTree('c')
	write(filepath.Join(w.mat("apps/y", ckTree('c')), "backend", "go.mod"), "module y\n")
	if plan, err = r.checkpointPlan(w.view("apps/x", "go"), ckTree('a'), matX); err != nil || len(plan.modules) != 4 || plan.modules[2] != (buildModule{Use: "./apps/y/backend", Code: ckTree('c')}) {
		t.Errorf("y from backend/: %+v %v", plan.modules, err)
	}

	// a hand-managed go.work that leaves the workspace through a symlink is
	// never followed: none of its lines reach the build
	if err := os.Remove(filepath.Join(w.root, "go.work")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "go.work")
	write(outside, "go 1.22\n\nuse ./apps/x\n\nreplace example.com/m => ./planted\n")
	if err := os.Symlink(outside, filepath.Join(w.root, "go.work")); err != nil {
		t.Fatal(err)
	}
	if plan, err = r.checkpointPlan(w.view("apps/x", "go"), ckTree('a'), matX); err != nil || plan.work == nil || strings.Contains(string(plan.work.GoWork), "planted") {
		t.Errorf("a go.work outside the workspace was read: %v\n%s", err, plan.work.GoWork)
	}

	// a checkpoint holding no Go module: no workspace (the build runs GOWORK=off)
	if err := os.Remove(filepath.Join(matX, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if plan, err = r.checkpointPlan(w.view("apps/x", "go"), ckTree('a'), matX); err != nil || plan.work != nil || plan.modules != nil {
		t.Errorf("a checkpoint without go.mod: %+v %v", plan, err)
	}
}

// covers D119e T11 — a checkpoint's artifact is reused while its build.json
// records the same tile, with no build; one recorded for another tile (a
// colliding CompKey) or without its binary is not; and whatever a work-tree
// build left at c/ (a symlink) is never followed.
func TestCheckpointArtifactReuse(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, ".xbin/build", util.CompKey("apps/x"))
	tree := ckTree('a')
	art := filepath.Join(base, "c", tree)
	if err := os.MkdirAll(art, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := func(tile string) {
		b, _ := json.Marshal(buildRecord{Tile: tile, Tree: tree, Toolchain: "go1.0"})
		if err := os.WriteFile(filepath.Join(art, "build.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rec("apps/x")
	if _, ok := artifactRecord(base, tree); ok {
		t.Error("an artifact without its binary is usable")
	}
	if err := os.WriteFile(filepath.Join(art, "bin"), []byte("#!"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Root: root, Isolate: true}
	v := &registry.Component{Path: "apps/x", Dir: filepath.Join(root, "apps/x"), CodeRoot: filepath.Join(root, "ckpt"), Manifest: registry.Manifest{Runtime: "go"}}
	got, err := r.buildCode(v, Code{Tree: tree})
	if err != nil || got != filepath.Join(art, "bin") {
		t.Fatalf("reuse: %q %v", got, err)
	}
	rec("apps/y")
	if old, _ := artifactRecord(base, tree); old == nil || old.Tile != "apps/y" {
		t.Fatalf("the other tile's record: %+v", old)
	}
	// a rebuild names the inputs that moved (07-runtime §3.1's caveat)
	was := &buildRecord{Toolchain: "go1.25.0", GoFlags: "-modcacherw", Modules: []buildModule{{"./apps/x", tree}, {"./apps/y", "worktree"}}}
	now := &buildRecord{Toolchain: "go1.26.0", GoFlags: "-modcacherw", Modules: []buildModule{{"./apps/x", tree}, {"./apps/y", ckTree('b')}}}
	if got, want := now.changedFrom(was), []string{"toolchain go1.25.0 → go1.26.0", "module ./apps/y: worktree → " + ckTree('b')}; !reflect.DeepEqual(got, want) {
		t.Errorf("rebuilt with %q, want %q", got, want)
	}

	// c/ replaced by a symlink to a directory outside: removed, never followed
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(base, "c")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "c")); err != nil {
		t.Fatal(err)
	}
	if _, ok := artifactRecord(base, tree); ok {
		t.Error("an artifact reached through a symlink is usable")
	}
	fd, err := artifactsDir(base)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if !realDir(filepath.Join(base, "c")) {
		t.Error("c/ is not a directory of its own")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("the symlink's target was written: %v", ents)
	}
}
