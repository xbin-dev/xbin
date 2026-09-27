package confine_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	)
}

// covers P16 T2 — rule C5 for the checkpoint estimate and capture (the case
// of internal/checkpoint's TestCaptureNoFollowingHostWalk): a work tree whose
// manifest, a source file, .git/HEAD and a dependency link point at the FIFO
// outside it, and one whose .git/HEAD and a file are FIFOs, are estimated
// and captured into a fresh store without anything opening them.
func nofollowCapture(t *testing.T, fifo string) {
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
	root := filepath.Join(t.TempDir(), "ws")
	mk := func(tile string, dirs ...string) checkpoint.Source {
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
	links := mk("apps/links", "lib", "deps", ".git")
	for rel, target := range map[string]string{
		"xbin.json":    fifo,
		"lib/index.js": fifo,
		".git/HEAD":    fifo,
		"deps/other":   filepath.Dir(fifo),
	} {
		if err := os.Symlink(target, filepath.Join(links.WorkTree, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	fifos := mk("apps/fifo", ".git")
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
