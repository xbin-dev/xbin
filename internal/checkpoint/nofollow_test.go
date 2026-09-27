package checkpoint

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// tripwire is a FIFO outside every tree under test, polled with
// open(O_WRONLY|O_NONBLOCK), which succeeds only while something holds it
// open for reading: a following open of a symlink to it trips it (and the
// probe's open lets that reader through, so nothing hangs). The detector of
// internal/confine's TestNoFollowingHostWalks, for this package's case.
type tripwire struct {
	path    string
	tripped atomic.Bool
}

func newTripwire(t *testing.T) *tripwire {
	t.Helper()
	w := &tripwire{path: filepath.Join(t.TempDir(), "tripwire")}
	if err := syscall.Mkfifo(w.path, 0o600); err != nil {
		t.Skipf("no FIFOs here: %v", err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			if fd, err := syscall.Open(w.path, syscall.O_WRONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0); err == nil {
				w.tripped.Store(true)
				syscall.Close(fd)
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done })
	return w
}

// covers P16 T2 — rule C5 for capture and the estimate, behaviourally: a
// work tree whose files, .git/HEAD, the ref HEAD names and manifest are
// symlinks to a FIFO outside it, and one whose .git/HEAD is itself a FIFO,
// are checkpointed without xbind (or the capture's own tools) opening any of
// them: the tripwire never trips and nothing blocks. The hostile-tree case
// the integrator wires into internal/confine's TestNoFollowingHostWalks.
func TestCaptureNoFollowingHostWalk(t *testing.T) {
	needGit(t)
	w := newTripwire(t)
	s, _ := testStore(t)

	links := tile(t, s, "apps/links", map[string]string{"a.txt": "a\n", ".git/refs/heads/": ""})
	for rel, target := range map[string]string{
		"xbin.json":    w.path,
		"scope.json":   w.path,
		"lib/index.js": w.path,
		".git/HEAD":    w.path,
		"deps/other":   filepath.Dir(w.path),
		"up":           "../../../" + filepath.Base(filepath.Dir(w.path)),
	} {
		p := filepath.Join(links.WorkTree, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	ref := tile(t, s, "apps/ref", map[string]string{"a.txt": "a\n", ".git/HEAD": "ref: refs/heads/main\n", ".git/refs/heads/": ""})
	if err := os.Symlink(w.path, filepath.Join(ref.WorkTree, ".git", "refs", "heads", "main")); err != nil {
		t.Fatal(err)
	}
	packed := tile(t, s, "apps/packed", map[string]string{"a.txt": "a\n", ".git/HEAD": "ref: refs/heads/gone\n"})
	if err := os.Symlink(w.path, filepath.Join(packed.WorkTree, ".git", "packed-refs")); err != nil {
		t.Fatal(err)
	}
	fifo := tile(t, s, "apps/fifo", map[string]string{"a.txt": "a\n", ".git/": ""})
	if err := syscall.Mkfifo(filepath.Join(fifo.WorkTree, ".git", "HEAD"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(fifo.WorkTree, "pipe.js"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, src := range []Source{links, ref, packed, fifo} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if _, err := s.Estimate(ctx, src); err != nil {
			t.Fatalf("estimate of %s: %v", src.Tile, err)
		}
		res, err := s.Capture(ctx, CaptureRequest{Source: src, By: "user:ana", Create: true})
		cancel()
		if err != nil {
			t.Fatalf("capture of %s: %v", src.Tile, err)
		}
		if res.WorkTreeHead != "" {
			t.Errorf("%s: a work-tree head %q read through a FIFO", src.Tile, res.WorkTreeHead)
		}
		if w.tripped.Load() {
			t.Fatalf("capturing %s opened the FIFO outside it", src.Tile)
		}
	}
	got := treeOf(t, s, "apps/links", capture(t, s, links, false).Hash)
	for rel, target := range map[string]string{"xbin.json": w.path, "lib/index.js": w.path, "deps/other": filepath.Dir(w.path)} {
		if got[rel] != (entry{"120000", target}) {
			t.Errorf("%s: %+v, want a symlink to %s", rel, got[rel], target)
		}
	}
	if w.tripped.Load() {
		t.Fatal("reading the store tripped the wire")
	}

	// the detector works: a following read of the same link trips it
	read := make(chan error, 1)
	go func() { _, err := os.ReadFile(filepath.Join(links.WorkTree, "xbin.json")); read <- err }()
	select {
	case <-read:
	case <-time.After(10 * time.Second):
		t.Fatal("a following read of the FIFO never returned")
	}
	if !w.tripped.Load() {
		t.Fatal("a following read didn't trip the wire: the test proves nothing")
	}
}
