package checkpoint

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// covers P16 T20 — rule C5 for the purge, behaviourally: a checkpoint whose
// files, manifest and a directory are symlinks to a FIFO (and its directory)
// outside the workspace is materialized, then purged; the purge removes the
// materialized tree, whose symlinks point at the FIFO, without xbind (or the
// purge's own tools) opening anything through them: the tripwire never trips,
// nothing blocks, and the FIFO and its directory are still there. The
// hostile-tree case the integrator wires into internal/confine's
// TestNoFollowingHostWalks.
func TestPurgeNoFollowingHostWalk(t *testing.T) {
	needGit(t)
	w := newTripwire(t)
	s, _ := testStore(t)
	src := tile(t, s, "apps/links", map[string]string{"a.txt": "a\n"})
	for rel, target := range map[string]string{
		"xbin.json":    w.path,
		"lib/index.js": w.path,
		"deps/other":   filepath.Dir(w.path),
	} {
		p := filepath.Join(src.WorkTree, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	tree := capture(t, s, src, true).Hash
	root, err := s.Materialize(src.Tile, tree)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(root, "xbin.json")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the materialized tree holds no symlink to the FIFO: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, err := s.Purge(ctx, src.Tile, tree, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Purge: %v", err)
		}
	case <-time.After(2 * time.Minute):
		t.Fatal("the purge blocked")
	}
	if w.tripped.Load() {
		t.Fatal("purging opened the FIFO outside the workspace")
	}
	if exists(root) {
		t.Error("the materialized tree survived the purge")
	}
	if fi, err := os.Lstat(w.path); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("the FIFO a symlink pointed at is gone or changed: %v", err)
	}
}
