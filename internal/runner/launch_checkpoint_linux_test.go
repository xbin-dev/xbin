//go:build linux && integration

// Run with: go test -tags=integration ./internal/runner/ -run 'Pinned|Nested'
// Needs user namespaces, an unpacked rootfs (XBIN_TEST_ROOTFS, or the repo's
// .rootfs) and a host go for the probe; skips otherwise.
package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/internal/vm"
)

// ckProbe builds a static backend that reads each of $PROBE_READ in its
// working directory (the code's canonical path) and tries to write there,
// printing what it saw. No path goes in its env: resourceBinds would bind
// any workspace path it found there read-write.
func ckProbe(t *testing.T) string {
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
	for _, rel := range strings.Split(os.Getenv("PROBE_READ"), ",") {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		fmt.Printf("read %s=%s %v\n", rel, strings.TrimSpace(string(b)), err)
	}
	if err := os.WriteFile(filepath.Join(dir, "written-by-backend"), []byte("x"), 0o644); err != nil {
		fmt.Println("write=refused")
	} else {
		fmt.Println("write=ok")
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

// ckRun runs a launched backend sandbox to its end: its output and exit code.
func ckRun(t *testing.T, cmd *exec.Cmd, h *sandbox.Handle) (string, int) {
	t.Helper()
	defer h.Cleanup()
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := h.SetupUserns(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return out.String() + "userns: " + err.Error(), -1
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if ee, ok := err.(*exec.ExitError); ok {
			return out.String(), ee.ExitCode()
		}
		return out.String(), 0
	case <-time.After(ckTimeout):
		_ = cmd.Process.Kill()
		t.Fatalf("the backend sandbox timed out\n%s", out.String())
	}
	return "", -1
}

// ckTimeout bounds one backend run (longer under VM emulation).
var ckTimeout = time.Minute

// ckBackend is a workspace with an isolated Runner whose run dir and
// gateway socket exist, so a launch spec's binds all have a source.
func ckBackend(t *testing.T, fs string) (ws string, r *Runner, dir string) {
	t.Helper()
	ws = t.TempDir()
	run := filepath.Join(t.TempDir(), "run")
	ckWrite(t, filepath.Join(run, "gateway.sock"), "")
	r = &Runner{Root: ws, RunDir: run, Isolate: true, Rootfs: fs, states: map[string]*state{}}
	return ws, r, run
}

// covers P6 P9 P14 — the code half (15-test-plan §4; the data half is
// M2's): pinned, a backend sees its checkpoint at its canonical path and not
// the work tree, and a write there fails by the bind's read-only flag; the
// live reload target sees its work tree as ever. The VM variant runs the
// same spec in a microVM and skips where VM sandboxes can't run.
func TestPinnedBackendSeesCheckpoint(t *testing.T) {
	fs := ckRootfs(t)
	probe := ckProbe(t)
	ws, r, run := ckBackend(t, fs)
	x := filepath.Join(ws, "apps/x")
	mat := filepath.Join(ws, ".xbin/deploy", util.TileKey("apps/x"), ckTree('a'))
	ckWrite(t, filepath.Join(x, "marker"), "worktree")
	ckMaterialize(t, mat, map[string]string{"marker": "checkpoint"})
	dir := filepath.Join(run, util.CompKey("apps/x"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{"PROBE_READ=marker", "XBIN_COMPONENT=apps/x"}
	pinned := &registry.Component{Path: "apps/x", Dir: x, CodeRoot: mat, Manifest: registry.Manifest{Runtime: "go"}}
	workTree := &registry.Component{Path: "apps/x", Dir: x, Manifest: registry.Manifest{Runtime: "go"}}

	check := func(t *testing.T, out string, code int, want string) {
		t.Helper()
		if code != 0 || !strings.Contains(out, "read marker="+want+" <nil>") || !strings.Contains(out, "write=refused") {
			t.Errorf("exit %d, want the backend to read %q and have its write refused:\n%s", code, want, out)
		}
		for _, d := range []string{x, mat} {
			if _, err := os.Lstat(filepath.Join(d, "written-by-backend")); err == nil {
				t.Errorf("the backend wrote into %s", d)
			}
		}
	}
	t.Run("namespaces", func(t *testing.T) {
		cmd, h, err := r.sandboxCmd(pinned, probe, dir, filepath.Join(dir, "g1.sock"), env, sandbox.EgressPolicy{}, "")
		if err != nil {
			t.Fatal(err)
		}
		out, code := ckRun(t, cmd, h)
		check(t, out, code, "checkpoint")

		cmd, h, err = r.sandboxCmd(workTree, probe, dir, filepath.Join(dir, "g2.sock"), env, sandbox.EgressPolicy{}, "")
		if err != nil {
			t.Fatal(err)
		}
		out, code = ckRun(t, cmd, h)
		check(t, out, code, "worktree")
	})

	t.Run("vm", func(t *testing.T) {
		repo, _ := filepath.Abs("../..")
		assets := filepath.Join(repo, "bin")
		for env, name := range map[string]string{
			"XBIN_FIRECRACKER": "firecracker", "XBIN_VM_KERNEL": "vmlinux",
			"XBIN_VM_AGENT": "xbin-vmagent", "XBIN_MKFS_EROFS": "mkfs.erofs",
			"XBIN_QEMU": "qemu-system-x86_64", "XBIN_VHOST_VSOCK": "vhost-device-vsock",
		} {
			if os.Getenv(env) == "" {
				t.Setenv(env, filepath.Join(assets, name))
			}
		}
		m := &vm.Manager{Root: ws, Rootfs: fs, Bx: filepath.Join(assets, "bx")}
		st := m.Status()
		if !st.Available {
			t.Skipf("VM sandboxes unavailable here (no KVM or no VM assets): %s", st.Reason)
		}
		if st.Emulated {
			ckTimeout = 5 * time.Minute
		}
		spec := r.launchSpec(pinned, probe, dir, env, sandbox.EgressPolicy{}, "")
		if err := m.Apply(context.Background(), spec, vm.Options{MemMiB: 512, Hostname: "x", Local: []string{dir}}); err != nil {
			t.Fatal(err)
		}
		cmd, h, err := sandbox.Launch(spec)
		if err != nil {
			t.Fatal(err)
		}
		out, code := ckRun(t, cmd, h)
		check(t, out, code, "checkpoint")
	})
}

// covers T18 P16 — 06-security T18.3 as ruled: pinned apps/a with a nested
// component apps/a/b. The sandbox init makes the mount point apps/a/b inside
// the checkpoint without following a symlink and binds b's own code after
// the checkpoint bind, so the backend sees both: b's work tree, or b's own
// checkpoint while b is pinned. A checkpoint in which a path element above
// a nested mount point is a symlink out of the tree fails the launch closed,
// naming the path, and nothing is made on the host outside the tree.
func TestNestedComponentBoundAfterCheckpoint(t *testing.T) {
	fs := ckRootfs(t)
	probe := ckProbe(t)
	ws, r, run := ckBackend(t, fs)
	a, b := filepath.Join(ws, "apps/a"), filepath.Join(ws, "apps/a/b")
	h, n := filepath.Join(ws, "apps/h"), filepath.Join(ws, "apps/h/s/n")
	ckWrite(t, filepath.Join(a, "xbin.json"), `{"runtime":"go"}`)
	ckWrite(t, filepath.Join(a, "marker"), "a-worktree")
	ckWrite(t, filepath.Join(b, "xbin.json"), `{}`)
	ckWrite(t, filepath.Join(b, "marker"), "b-worktree")
	ckWrite(t, filepath.Join(h, "xbin.json"), `{"runtime":"go"}`)
	ckWrite(t, filepath.Join(n, "xbin.json"), `{}`)
	reg, err := registry.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	r.Reg = reg
	mat := func(tile, tree string) string { return filepath.Join(ws, ".xbin/deploy", util.TileKey(tile), tree) }
	treeA, treeB, treeH := ckTree('a'), ckTree('b'), ckTree('c')
	ckMaterialize(t, mat("apps/a", treeA), map[string]string{"marker": "a-checkpoint"})
	ckMaterialize(t, mat("apps/a/b", treeB), map[string]string{"marker": "b-checkpoint"})
	outside := t.TempDir()
	if err := os.MkdirAll(mat("apps/h", treeH), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(mat("apps/h", treeH), "s")); err != nil {
		t.Fatal(err)
	}
	pins := map[string]string{"apps/a": treeA, "apps/h": treeH}
	r.CodeFor = func(tile, dep string) (Code, error) {
		if tr, ok := pins[tile]; ok {
			return Code{Tree: tr}, nil
		}
		return Code{WorkTree: true}, nil
	}
	r.Materialize = func(tile, tree string) (string, error) { return mat(tile, tree), nil }
	launch := func(t *testing.T, tile, gen string) (string, int) {
		t.Helper()
		dir := filepath.Join(run, util.CompKey(tile))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		c := &registry.Component{Path: tile, Dir: filepath.Join(ws, tile), CodeRoot: mat(tile, pins[tile]), Manifest: registry.Manifest{Runtime: "go"}}
		env := []string{"PROBE_READ=marker,b/marker"}
		cmd, hd, err := r.sandboxCmd(c, probe, dir, filepath.Join(dir, gen+".sock"), env, sandbox.EgressPolicy{}, "")
		if err != nil {
			t.Fatal(err)
		}
		return ckRun(t, cmd, hd)
	}

	out, code := launch(t, "apps/a", "g1")
	if code != 0 || !strings.Contains(out, "read marker=a-checkpoint <nil>") || !strings.Contains(out, "read b/marker=b-worktree <nil>") {
		t.Fatalf("exit %d, want the checkpoint and b's work tree:\n%s", code, out)
	}
	if ents, err := os.ReadDir(filepath.Join(mat("apps/a", treeA), "b")); err != nil || len(ents) != 0 {
		t.Errorf("b's mount point in the checkpoint: %v %v, want an empty directory", ents, err)
	}

	pins["apps/a/b"] = treeB
	out, code = launch(t, "apps/a", "g2")
	if code != 0 || !strings.Contains(out, "read b/marker=b-checkpoint <nil>") {
		t.Fatalf("exit %d, want b's own checkpoint:\n%s", code, out)
	}

	out, code = launch(t, "apps/h", "g1")
	if code == 0 || !strings.Contains(out, "symlink") || !strings.Contains(out, filepath.Join(h, "s")) {
		t.Fatalf("a symlink above a nested mount point: exit %d, want a failed start naming %s:\n%s", code, filepath.Join(h, "s"), out)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("the launch made %v outside the checkpoint", ents)
	}
}
