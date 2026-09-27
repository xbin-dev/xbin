//go:build linux && integration

// Run with: go test -tags=integration ./internal/runner/ -run Checkpoint
// Needs user namespaces, an unpacked rootfs (XBIN_TEST_ROOTFS, or the repo's
// .rootfs) and a host go; skips otherwise. TestMain is build_linux_test.go's.
package runner

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/util"
)

// ckRootfs is the rootfs the confined checkpoint tests run over, or a skip.
func ckRootfs(t *testing.T) string {
	t.Helper()
	fs := os.Getenv("XBIN_TEST_ROOTFS")
	if fs == "" {
		fs, _ = filepath.Abs("../../.rootfs")
	}
	if _, err := os.Stat(filepath.Join(fs, "etc", "os-release")); err != nil || !sandbox.Available() {
		t.Skip("no rootfs or no user namespaces")
	}
	return fs
}

// ckWrite writes a work-tree file (0644, parents made).
func ckWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ckMaterialize lays files out as a materialized checkpoint does:
// directories 0755, files 0444.
func ckMaterialize(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, s := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o444); err != nil {
			t.Fatal(err)
		}
	}
}

const ckGoMod = "\n\ngo 1.22\n"

func ckMain(body string) string {
	return "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(" + body + ") }\n"
}

// covers P9 P16 T1 NP-07-4 L8 — a checkpoint's Go build, confined (15-test-plan
// §4; with TestConfinedGoBuild's checkpoint case, 07-runtime §10.5): the
// work tree's main.go prints `worktree` and the checkpoint's `checkpoint`;
// the build binds the materialized checkpoint at the tile's canonical path
// (confine's DirFrom), so the binary prints `checkpoint` and the root
// go.work's `use ./apps/x` resolves to it; a nested component excluded from
// the checkpoint is bound back from its work tree; the go.work module of a
// tile whose primary is pinned builds from that checkpoint, and from its
// work tree while that primary follows it; a checkpoint keeping its module
// in backend/ gets go.work's line corrected; the hostile fsmonitor marker
// is never written; caches stay the tile's own; the artifact lands at the
// per-checkpoint path with its build.json, is reused, and a failed build
// leaves nothing.
func TestConfinedBuildOfCheckpointAtCanonicalPath(t *testing.T) {
	fs := ckRootfs(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	ws := t.TempDir()
	x, y, sub := filepath.Join(ws, "apps/x"), filepath.Join(ws, "apps/y"), filepath.Join(ws, "apps/x/sub")
	ckWrite(t, filepath.Join(x, "xbin.json"), `{"runtime":"go"}`)
	ckWrite(t, filepath.Join(x, "go.mod"), "module example.com/x"+ckGoMod)
	ckWrite(t, filepath.Join(x, "backend/main.go"), ckMain(`"worktree"`))
	ckWrite(t, filepath.Join(sub, "xbin.json"), `{}`)
	ckWrite(t, filepath.Join(sub, "go.mod"), "module example.com/sub"+ckGoMod)
	ckWrite(t, filepath.Join(sub, "sub.go"), "package sub\n\nfunc Who() string { return \"sub-worktree\" }\n")
	ckWrite(t, filepath.Join(y, "xbin.json"), `{}`)
	ckWrite(t, filepath.Join(y, "go.mod"), "module example.com/y"+ckGoMod)
	ckWrite(t, filepath.Join(y, "y.go"), "package y\n\nfunc Who() string { return \"y-worktree\" }\n")
	ckWrite(t, filepath.Join(ws, "go.work"), "go 1.22\n\nuse (\n\t./apps/x\n\t./apps/x/sub\n\t./apps/y\n)\n")
	ckWrite(t, filepath.Join(ws, "data", "vault"), "secret")
	marker := filepath.Join(t.TempDir(), "PWNED")
	if out, err := exec.Command("git", "-C", x, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	cfg, _ := os.OpenFile(filepath.Join(x, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = cfg.WriteString("[core]\n\tfsmonitor = \"touch " + marker + "; echo\"\n")
	cfg.Close()

	mat := func(tile, tree string) string { return filepath.Join(ws, ".xbin/deploy", util.TileKey(tile), tree) }
	treeX, treeX2, treeX3, treeBad, treeY := ckTree('1'), ckTree('2'), ckTree('3'), ckTree('4'), ckTree('9')
	main := "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/sub\"\n\t\"example.com/y\"\n)\n\nfunc main() { fmt.Println(\"checkpoint\", sub.Who(), y.Who()) }\n"
	for _, tr := range []string{treeX, treeX2} {
		ckMaterialize(t, mat("apps/x", tr), map[string]string{"xbin.json": `{"runtime":"go"}`, "go.mod": "module example.com/x" + ckGoMod, "backend/main.go": main})
	}
	ckMaterialize(t, mat("apps/x", treeX3), map[string]string{"backend/go.mod": "module example.com/x" + ckGoMod, "backend/main.go": ckMain(`"checkpoint-backend-layout"`)})
	ckMaterialize(t, mat("apps/x", treeBad), map[string]string{"go.mod": "module example.com/x" + ckGoMod, "backend/main.go": "package main\n\nfunc main() { undefined() }\n"})
	ckMaterialize(t, mat("apps/y", treeY), map[string]string{"go.mod": "module example.com/y" + ckGoMod, "y.go": "package y\n\nfunc Who() string { return \"y-checkpoint\" }\n"})

	reg, err := registry.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	pins := map[string]string{"apps/y": treeY}
	confine.Configure(fs)
	defer confine.Configure("")
	r := &Runner{Root: ws, Isolate: true, Rootfs: fs, Reg: reg, states: map[string]*state{}}
	r.CodeFor = func(tile, dep string) (Code, error) {
		if tr, ok := pins[tile]; ok {
			return Code{Tree: tr}, nil
		}
		return Code{WorkTree: true}, nil
	}
	r.Materialize = func(tile, tree string) (string, error) { return mat(tile, tree), nil }
	arts := filepath.Join(ws, ".xbin/build", util.CompKey("apps/x"), "c")
	build := func(tree string) (string, error) {
		v := &registry.Component{Path: "apps/x", Dir: x, CodeRoot: mat("apps/x", tree), Manifest: registry.Manifest{Runtime: "go"}}
		return r.buildCode(v, Code{Tree: tree})
	}
	mustBuild := func(tree, want string) string {
		t.Helper()
		bin, err := build(tree)
		if err != nil {
			if be, ok := err.(*BuildError); ok {
				t.Fatalf("build %s: %s", tree[:7], be.Output)
			}
			t.Fatalf("build %s: %v", tree[:7], err)
		}
		if bin != filepath.Join(arts, tree, "bin") {
			t.Fatalf("artifact at %s, want the per-checkpoint path", bin)
		}
		out, err := exec.Command(bin).Output()
		if got := strings.TrimSpace(string(out)); err != nil || got != want {
			t.Fatalf("the backend of %s: %q %v, want %q", tree[:7], got, err, want)
		}
		return bin
	}

	bin := mustBuild(treeX, "checkpoint sub-worktree y-checkpoint")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the tile's .git/config ran on the host")
	}
	for _, d := range []string{"go-build", "mod"} {
		if _, err := os.Stat(filepath.Join(ws, ".xbin/cache/tile", util.CompKey("apps/x"), d)); err != nil {
			t.Errorf("the tile's own %s cache: %v", d, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(ws, ".xbin/build", util.CompKey("apps/x"), "bin")); err == nil {
		t.Error("the checkpoint build wrote the work tree's artifact")
	}
	var rec buildRecord
	if b, err := os.ReadFile(filepath.Join(arts, treeX, "build.json")); err != nil || json.Unmarshal(b, &rec) != nil {
		t.Fatalf("build.json: %v", err)
	}
	wantMods := []buildModule{{Use: "./apps/x", Code: treeX}, {Use: "./apps/x/sub", Code: "worktree"}, {Use: "./apps/y", Code: treeY}}
	if rec.Tile != "apps/x" || rec.Tree != treeX || !strings.HasPrefix(rec.Toolchain, "go") || !equalModules(rec.Modules, wantMods) {
		t.Errorf("build.json %+v", rec)
	}
	// the nested component's mount point: an empty directory made inside the tree
	if ents, err := os.ReadDir(filepath.Join(mat("apps/x", treeX), "sub")); err != nil || len(ents) != 0 {
		t.Errorf("the nested mount point in the checkpoint: %v %v", ents, err)
	}

	// reused, not rebuilt (P9)
	before, _ := os.Lstat(bin)
	if again := mustBuild(treeX, "checkpoint sub-worktree y-checkpoint"); again != bin {
		t.Fatalf("reuse gave %s", again)
	}
	if after, _ := os.Lstat(bin); !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
		t.Error("an existing artifact was rebuilt")
	}

	// the other tile's primary follows its work tree now: so does the build
	delete(pins, "apps/y")
	mustBuild(treeX2, "checkpoint sub-worktree y-worktree")

	// a checkpoint with its module in backend/: go.work's line corrected per build
	mustBuild(treeX3, "checkpoint-backend-layout")
	if b, _ := os.ReadFile(filepath.Join(ws, "go.work")); !strings.Contains(string(b), "\t./apps/x\n") {
		t.Errorf("the workspace's go.work changed: %s", b)
	}

	// a failed build leaves nothing that looks like an artifact
	if _, err := build(treeBad); err == nil || !strings.Contains(err.Error(), "undefined") {
		t.Fatalf("a broken checkpoint: %v", err)
	}
	ents, _ := os.ReadDir(arts)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if want := strings.Join([]string{treeX, treeX2, treeX3}, " "); strings.Join(names, " ") != want {
		t.Errorf("checkpoint artifacts %q, want %q (no failed build, no leftovers)", names, want)
	}
}

func equalModules(a, b []buildModule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
