//go:build linux

package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"
)

// covers T2 — openat2's EAGAIN is retried: under RESOLVE_BENEATH the kernel
// refuses a ".." step that races any rename on the host with EAGAIN, and a
// busy host would turn a legitimate in-tree "../x" into a plain error. With
// renames running flat out beside it, every open through ".." succeeds.
func TestOpenBeneathRetriesEAGAIN(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	storm := t.TempDir()
	one, two := filepath.Join(storm, "1"), filepath.Join(storm, "2")
	if err := os.WriteFile(one, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var stop atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !stop.Load() {
			_ = os.Rename(one, two)
			_ = os.Rename(two, one)
		}
	}()
	defer func() { stop.Store(true); <-done }()
	failed := 0
	for i := 0; i < 3000; i++ {
		f, err := OpenBeneath(root, "a/b/../x")
		if err != nil {
			if errors.Is(err, unix.ENOSYS) {
				t.Skip("no openat2 on this kernel")
			}
			failed++
			continue
		}
		f.Close()
	}
	if failed > 0 {
		t.Errorf("%d of 3000 opens through \"..\" failed beside a rename storm", failed)
	}
}
