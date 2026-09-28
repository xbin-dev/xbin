//go:build linux && integration

// Run with: go test -tags=integration ./internal/confine/
// Needs user namespaces and an unpacked rootfs (XBIN_TEST_ROOTFS, or the
// repo's .rootfs from `make rootfs`); skips otherwise.
package confine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// covers D119g D119e T18 — confine's bind destination in a real sandbox. A job
// whose DirFrom is a checkpoint's tree runs at Dir, the canonical path, and
// sees the checkpoint there, never the work tree; read-only with
// ReadOnlyDir, so nothing it writes reaches either tree. The .xbin, data and
// homes masks still apply around it (the tile rebound inside the workspace,
// as a build binds it) and beneath it (the rebound tree holding them
// itself), and a read-write dir nested in the .xbin cover still takes the
// job's output. A bind made by At lands inside the read-only checkpoint at a
// mount point the checkpoint lacks.
func TestRebindDirInSandbox(t *testing.T) {
	fs := testRootfs(t)
	tmp := t.TempDir()
	files := func(base string, m map[string]string) {
		t.Helper()
		for f, body := range m {
			p := filepath.Join(base, filepath.FromSlash(f))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if body == "/" {
				if err := os.MkdirAll(p, 0o755); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	secrets := map[string]string{".xbin/token": "SECRET", "data/vault": "VAULT", "homes/u/h": "HOME", ".xbin/build/k": "/"}
	script := `
echo "pwd=$(pwd)"
echo "marker=$(cat marker)"
echo "nested=$(cat nested/main.go 2>/dev/null)"
if (echo x > written) 2>/dev/null; then echo write=ok; else echo write=refused; fi
echo "token=$(cat "$ROOT/.xbin/token" 2>/dev/null)"
echo "vault=$(cat "$ROOT/data/vault" 2>/dev/null)"
echo "home=$(cat "$ROOT/homes/u/h" 2>/dev/null)"
if (echo built > "$ROOT/.xbin/build/k/out") 2>/dev/null; then echo out=ok; else echo out=refused; fi
`
	run := func(t *testing.T, root string, c Cmd) map[string]string {
		t.Helper()
		c.Argv, c.Env = []string{"sh", "-c", script}, []string{"ROOT=" + root}
		res, err := Run(context.Background(), c)
		if err != nil {
			t.Fatalf("confined run: %v\n%s%s", err, res.Stdout, res.Stderr)
		}
		got := map[string]string{}
		for _, l := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
			k, v, _ := strings.Cut(l, "=")
			got[k] = v
		}
		return got
	}
	check := func(t *testing.T, got, want map[string]string) {
		t.Helper()
		for k, w := range want {
			if got[k] != w {
				t.Errorf("%s = %q, want %q", k, got[k], w)
			}
		}
	}

	Configure(fs)
	defer Configure("")
	if !Isolated() {
		t.Fatal("not isolated")
	}

	t.Run("the tile rebound inside the workspace", func(t *testing.T) {
		root := filepath.Join(tmp, "ws")
		work := filepath.Join(root, "apps", "x")
		ckpt := filepath.Join(tmp, "ckpt")
		nested := filepath.Join(tmp, "nested")
		files(root, secrets)
		files(work, map[string]string{"marker": "work"})
		files(ckpt, map[string]string{"marker": "ckpt"})
		files(nested, map[string]string{"main.go": "nested"})
		got := run(t, root, Cmd{Dir: work, DirFrom: ckpt, ReadOnlyDir: true, Binds: []sandbox.Bind{
			RO(root),
			MaskOpen(filepath.Join(root, ".xbin")), Mask(filepath.Join(root, "data")), Mask(filepath.Join(root, "homes")),
			RW(filepath.Join(root, ".xbin", "build", "k")),
			At(nested, filepath.Join(work, "nested"), true),
		}})
		check(t, got, map[string]string{"pwd": work, "marker": "ckpt", "nested": "nested", "write": "refused",
			"token": "", "vault": "", "home": "", "out": "ok"})
		for _, p := range []string{filepath.Join(work, "written"), filepath.Join(ckpt, "written")} {
			if _, err := os.Lstat(p); err == nil {
				t.Errorf("a read-only rebind let a write through to %s", p)
			}
		}
		if b, _ := os.ReadFile(filepath.Join(work, "marker")); string(b) != "work" {
			t.Errorf("the work tree changed: %q", b)
		}
		if b, _ := os.ReadFile(filepath.Join(root, ".xbin", "build", "k", "out")); string(b) != "built\n" {
			t.Errorf("the output dir nested in the .xbin cover got %q", b)
		}
		if _, err := os.Lstat(filepath.Join(work, "nested")); err == nil {
			t.Error("At's mount point was made in the work tree, not in the checkpoint")
		}
	})

	t.Run("the masks beneath the rebound tree", func(t *testing.T) {
		live := filepath.Join(tmp, "live")
		snap := filepath.Join(tmp, "snap")
		files(live, map[string]string{"marker": "live", ".xbin/token": "LIVE", ".xbin/build/k": "/"})
		files(snap, secrets)
		files(snap, map[string]string{"marker": "snap"})
		got := run(t, live, Cmd{Dir: live, DirFrom: snap, ReadOnlyDir: true, Binds: []sandbox.Bind{
			MaskOpen(filepath.Join(live, ".xbin")), Mask(filepath.Join(live, "data")), Mask(filepath.Join(live, "homes")),
			RW(filepath.Join(live, ".xbin", "build", "k")),
		}})
		check(t, got, map[string]string{"pwd": live, "marker": "snap", "write": "refused",
			"token": "", "vault": "", "home": "", "out": "ok"})
		if b, _ := os.ReadFile(filepath.Join(live, ".xbin", "build", "k", "out")); string(b) != "built\n" {
			t.Errorf("the output dir nested in the .xbin cover got %q", b)
		}
	})
}
