//go:build linux && integration

// Run with: go test -tags=integration ./internal/runner/ -run 'CheckpointData|ProtectedBuildConfined'
// Needs user namespaces, an unpacked rootfs (XBIN_TEST_ROOTFS, or the repo's
// .rootfs) and a host go; skips otherwise. TestMain is build_linux_test.go's.
package runner

import (
	"encoding/json"
	"errors"
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

// dataProbe builds a static backend that reads marker at its working
// directory (the code's canonical path) and in $XBIN_RES_STORE, then writes
// into both, printing what it saw.
func dataProbe(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go for the probe")
	}
	src := t.TempDir()
	ckWrite(t, filepath.Join(src, "go.mod"), "module probe"+ckGoMod)
	ckWrite(t, filepath.Join(src, "main.go"), `package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	dir, _ := os.Getwd()
	for name, d := range map[string]string{"code": dir, "data": os.Getenv("XBIN_RES_STORE")} {
		b, err := os.ReadFile(filepath.Join(d, "marker"))
		fmt.Printf("read %s=%s %v\n", name, strings.TrimSpace(string(b)), err)
		if err := os.WriteFile(filepath.Join(d, "written-by-backend"), []byte("x"), 0o644); err != nil {
			fmt.Printf("write %s=refused\n", name)
		} else {
			fmt.Printf("write %s=ok\n", name)
		}
	}
}
`)
	bin := filepath.Join(src, "probe")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("probe: %v %s", err, out)
	}
	return bin
}

// covers P6 P9 P14 — TestPinnedBackendSeesCheckpoint's data half (15-test-plan
// §4): a pinned non-primary deployment's backend reads its checkpoint at the
// canonical path, read-only by the bind, and its resource at the XBIN_RES_*
// path main's backend uses; the host directory behind that path is the
// deployment's own volume, which takes the backend's write, and main's is
// left untouched. main's backend, with the same env, reads and writes main's.
func TestPinnedBackendSeesCheckpointData(t *testing.T) {
	fs := ckRootfs(t)
	probe := dataProbe(t)
	ws, r, run := ckBackend(t, fs)
	x := filepath.Join(ws, "apps/x")
	mat := filepath.Join(ws, ".xbin/deploy", util.TileKey("apps/x"), ckTree('a'))
	mainStore := filepath.Join(ws, ".xbin/resenc/apps~x/store")
	devStore := filepath.Join(ws, ".xbin/resenc/.deployments/apps~x/dev/fs/store")
	ckWrite(t, filepath.Join(x, "marker"), "worktree")
	ckMaterialize(t, mat, map[string]string{"marker": "checkpoint"})
	ckWrite(t, filepath.Join(mainStore, "marker"), "main-data")
	ckWrite(t, filepath.Join(devStore, "marker"), "dev-data")
	r.Primary = func(string) string { return "main" }
	r.EnvFor = func(c *registry.Component, dep string) ([]string, map[string]ResBind) {
		if dep == "main" {
			return nil, nil
		}
		return nil, map[string]ResBind{mainStore: {Src: devStore}}
	}
	env := []string{"XBIN_COMPONENT=apps/x", "XBIN_RES_STORE=" + mainStore}
	launch := func(t *testing.T, v *registry.Component, sub string) string {
		t.Helper()
		dir := filepath.Join(run, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		cmd, h, err := r.sandboxCmd(v, probe, dir, filepath.Join(dir, "g1.sock"), env, sandbox.EgressPolicy{}, "")
		if err != nil {
			t.Fatal(err)
		}
		out, code := ckRun(t, cmd, h)
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		return out
	}

	dev := &registry.Component{Path: "apps/x", Dir: x, Deployment: "dev", CodeRoot: mat, Manifest: registry.Manifest{Runtime: "go"}}
	out := launch(t, dev, sockDir("apps/x", "dev"))
	for _, want := range []string{"read code=checkpoint <nil>", "write code=refused", "read data=dev-data <nil>", "write data=ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("dev's backend: want %q in\n%s", want, out)
		}
	}
	if _, err := os.Lstat(filepath.Join(devStore, "written-by-backend")); err != nil {
		t.Error("dev's write didn't land in its own volume")
	}
	for _, d := range []string{mainStore, mat, x} {
		if _, err := os.Lstat(filepath.Join(d, "written-by-backend")); err == nil {
			t.Errorf("dev's backend wrote into %s", d)
		}
	}

	main := &registry.Component{Path: "apps/x", Dir: x, Manifest: registry.Manifest{Runtime: "go"}}
	out = launch(t, main, util.CompKey("apps/x"))
	if !strings.Contains(out, "read data=main-data <nil>") || !strings.Contains(out, "read code=worktree <nil>") {
		t.Errorf("main's backend:\n%s", out)
	}
	if _, err := os.Lstat(filepath.Join(mainStore, "written-by-backend")); err != nil {
		t.Error("main's write didn't land in main's volume")
	}
}

// covers P21 T16 T17 NP-07-14 — TestProtectedBuildProductsSeparated, confined:
// a manager's operation onto the protected primary builds the checkpoint in
// its own namespace (the artifact at protected/build/<tree>/bin printing the
// checkpoint's output, its Go caches under protected/cache) and writes none
// of the tile's shared artifacts or caches; a second one reuses it; after
// .xbin/ loses it, a restart path rebuilds it from the saved build.json when
// every input matches, and holds the start when one moved.
func TestProtectedBuildConfined(t *testing.T) {
	fs := ckRootfs(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	ws := t.TempDir()
	x := filepath.Join(ws, "apps/x")
	ckWrite(t, filepath.Join(x, "xbin.json"), `{"runtime":"go"}`)
	ckWrite(t, filepath.Join(x, "go.mod"), "module example.com/x"+ckGoMod)
	ckWrite(t, filepath.Join(x, "backend/main.go"), ckMain(`"worktree"`))
	ckWrite(t, filepath.Join(ws, "go.work"), "go 1.22\n\nuse ./apps/x\n")
	tree := ckTree('1')
	mat := filepath.Join(ws, ".xbin/deploy", util.TileKey("apps/x"), tree)
	ckMaterialize(t, mat, map[string]string{"xbin.json": `{"runtime":"go"}`, "go.mod": "module example.com/x" + ckGoMod, "backend/main.go": ckMain(`"protected-checkpoint"`)})
	reg, err := registry.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	confine.Configure(fs)
	defer confine.Configure("")
	r := &Runner{Root: ws, Isolate: true, Rootfs: fs, Reg: reg, states: map[string]*state{}}
	r.CodeFor = func(tile, dep string) (Code, error) { return Code{Tree: tree}, nil }
	r.Materialize = func(tile, t string) (string, error) {
		return filepath.Join(ws, ".xbin/deploy", util.TileKey(tile), t), nil
	}
	v := &registry.Component{Path: "apps/x", Dir: x, CodeRoot: mat, Manifest: registry.Manifest{Runtime: "go"}}
	pr, sh := r.protectedProducts("apps/x"), r.sharedProducts("apps/x")

	bin, rec, err := r.prepareProtected(v, tree)
	if err != nil {
		if be, ok := err.(*BuildError); ok {
			t.Fatalf("build: %s", be.Output)
		}
		t.Fatal(err)
	}
	if want := filepath.Join(pr.base, pr.arts, tree, "bin"); bin != want {
		t.Fatalf("artifact %s, want %s", bin, want)
	}
	if out, err := exec.Command(bin).CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "protected-checkpoint" {
		t.Fatalf("the artifact ran %q %v", out, err)
	}
	var saved buildRecord
	if json.Unmarshal(rec, &saved) != nil || saved.Tile != "apps/x" || saved.Tree != tree || saved.Toolchain == "" {
		t.Fatalf("build.json for the plane: %s", rec)
	}
	for _, d := range []string{pr.gocache, pr.modcache} {
		if !realDir(d) {
			t.Errorf("the protected cache %s wasn't used", d)
		}
	}
	for _, d := range []string{sh.base, sh.gocache, sh.modcache} {
		if _, err := os.Lstat(d); err == nil {
			t.Errorf("the protected build wrote the shared %s", d)
		}
	}

	st, _ := os.Stat(bin)
	if again, _, err := r.prepareProtected(v, tree); err != nil || again != bin {
		t.Fatalf("a second operation: %s %v", again, err)
	}
	if st2, _ := os.Stat(bin); !st2.ModTime().Equal(st.ModTime()) {
		t.Error("a second operation rebuilt a kept artifact")
	}

	// .xbin/ loses the artifact: a restart path rebuilds only on matching inputs
	if err := os.RemoveAll(filepath.Join(pr.base, pr.arts)); err != nil {
		t.Fatal(err)
	}
	moved := saved
	moved.Toolchain = "go0.1"
	mj, _ := json.Marshal(moved)
	if _, err := r.rebuildLostProtected(v, tree, mj); !errors.Is(err, ErrProtectedLost) {
		t.Fatalf("moved inputs: %v, want the start held", err)
	}
	if _, ok := r.protectedArtifact(v, tree); ok {
		t.Fatal("a held start built anyway")
	}
	if got, err := r.rebuildLostProtected(v, tree, rec); err != nil || got != bin {
		t.Fatalf("matching inputs: %s %v, want the artifact rebuilt at %s", got, err, bin)
	}
	if out, err := exec.Command(bin).CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "protected-checkpoint" {
		t.Fatalf("the rebuilt artifact ran %q %v", out, err)
	}
}
