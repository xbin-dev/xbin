package confine_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/confine"
)

// The operations TestNoFollowingHostWalks' behavioural half drives through a
// hostile tree, registered here because their packages import confine. Each
// mirrors the hostile-tree case its own package tests in full (the WP's
// report names it); this is the one list that says C5 holds for all of them.
func init() {
	confine.NofollowOperations = append(confine.NofollowOperations,
		confine.NofollowOperation{Name: "checkpoint capture", Run: nofollowCapture},
		confine.NofollowOperation{Name: "checkpoint materialize and GC", Run: nofollowMaterializeGC},
		confine.NofollowOperation{Name: "checkpoint drift count", Run: nofollowDrift},
	)
}

// covers P16 T2 — rule C5 for the checkpoint estimate and capture (the case
// of internal/checkpoint's TestCaptureNoFollowingHostWalk): a work tree whose
// manifest, a source file, .git/HEAD and a dependency link point at the FIFO
// outside it, and one whose .git/HEAD and a file are FIFOs, are estimated
// and captured into a fresh store without anything opening them.
func nofollowCapture(t *testing.T, fifo string) {
	needStoreTools(t)
	root := filepath.Join(t.TempDir(), "ws")
	links := hostileTile(t, root, "apps/links", map[string]string{
		"xbin.json":    fifo,
		"lib/index.js": fifo,
		".git/HEAD":    fifo,
		"deps/other":   filepath.Dir(fifo),
	})
	fifos := mkTile(t, root, "apps/fifo", ".git")
	for _, rel := range []string{".git/HEAD", "pipe.js"} {
		if err := syscall.Mkfifo(filepath.Join(fifos.WorkTree, filepath.FromSlash(rel)), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s := checkpoint.New(root)
	for _, src := range []checkpoint.Source{links, fifos} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if _, err := s.Estimate(ctx, src); err != nil {
			cancel()
			t.Fatalf("estimate of %s: %v", src.Tile, err)
		}
		_, err := s.Capture(ctx, checkpoint.CaptureRequest{Source: src, By: "user:ana", Create: true})
		cancel()
		if err != nil {
			t.Fatalf("capture of %s: %v", src.Tile, err)
		}
	}
}

// covers P9 P16 T2 — rule C5 for materializing a checkpoint and its GC (the
// case of internal/checkpoint's TestMaterializeNoFollowingHostWalk): a
// checkpoint holding links to the FIFO (xbin.json, a source file, a
// dependency, a chain c1 → c2) is materialized twice, and GC then runs over
// the tree dir, where a stale tree it must remove holds the same links and
// a FIFO. The handed-out grace keeps the real tree from outside the package,
// so the stale tree is what GC's removal walks.
func nofollowMaterializeGC(t *testing.T, fifo string) {
	needStoreTools(t)
	root := filepath.Join(t.TempDir(), "ws")
	src := hostileTile(t, root, "apps/links", map[string]string{
		"xbin.json":    fifo,
		"lib/index.js": fifo,
		"deps/other":   filepath.Dir(fifo),
		"c1":           "c2",
		"c2":           fifo,
	})
	s := checkpoint.New(root)
	s.Caps.Every = 0
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := s.Capture(ctx, checkpoint.CaptureRequest{Source: src, By: "user:ana", Create: true})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.Materialize(src.Tile, res.Hash); err != nil {
			t.Fatalf("materialize %d: %v", i, err)
		}
	}
	stale := filepath.Join(s.TreesDir(src.Tile), strings.Repeat("e", 40))
	if err := os.MkdirAll(filepath.Join(stale, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, target := range map[string]string{"xbin.json": fifo, "lib/index.js": fifo, "up": filepath.Dir(fifo)} {
		if err := os.Symlink(target, filepath.Join(stale, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(stale, "pipe.js"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.GC(ctx, src.Tile, nil); err != nil {
		t.Fatalf("GC: %v", err)
	}
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Fatalf("GC kept a stale tree nothing keeps (%v)", err)
	}
}

// covers P16 T2 — rule C5 for the drift count (the hostile half of
// internal/checkpoint's TestDriftCountChangesNothingDurable): after a
// capture, the work tree's a.txt, xbin.json and .git/HEAD become links to
// the FIFO and pipe.js a FIFO, and the count runs without opening them.
func nofollowDrift(t *testing.T, fifo string) {
	needStoreTools(t)
	root := filepath.Join(t.TempDir(), "ws")
	src := mkTile(t, root, "apps/drift", ".git")
	s := checkpoint.New(root)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := s.Capture(ctx, checkpoint.CaptureRequest{Source: src, By: "user:ana", Create: true})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	for _, rel := range []string{"a.txt", "xbin.json", ".git/HEAD"} {
		p := filepath.Join(src.WorkTree, filepath.FromSlash(rel))
		_ = os.Remove(p)
		if err := os.Symlink(fifo, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(src.WorkTree, "pipe.js"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Drift(ctx, src, res.Hash); err != nil {
		t.Fatalf("drift: %v", err)
	}
}

// needStoreTools skips unless the store's tools run directly here.
func needStoreTools(t *testing.T) {
	t.Helper()
	if confine.Isolated() {
		t.Fatal("confinement is on: this case runs the store's tools directly")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this host")
	}
	// the store's direct-mode scripts use GNU find's -printf
	if out, err := exec.Command("find", "--version").CombinedOutput(); err != nil || !bytes.Contains(out, []byte("GNU")) {
		t.Skip("no GNU find on this host")
	}
}

// mkTile makes tile under the workspace root with a.txt and dirs.
func mkTile(t *testing.T, root, tile string, dirs ...string) checkpoint.Source {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(tile))
	for _, d := range append([]string{"."}, dirs...) {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return checkpoint.Source{Tile: tile, WorkTree: dir}
}

// hostileTile makes tile holding links (tile-relative path → target).
func hostileTile(t *testing.T, root, tile string, links map[string]string) checkpoint.Source {
	t.Helper()
	src := mkTile(t, root, tile, "lib", "deps", ".git")
	for rel, target := range links {
		if err := os.Symlink(target, filepath.Join(src.WorkTree, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	return src
}
