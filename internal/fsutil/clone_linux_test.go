//go:build linux

package fsutil

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// sparseFile writes a size-byte file at p holding data at each offset in at
// (a 4 KiB block of that offset's byte), holes elsewhere.
func sparseFile(t *testing.T, p string, size int64, at ...int64) {
	t.Helper()
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	for _, off := range at {
		if _, err := f.WriteAt(bytes.Repeat([]byte{byte(off>>20) | 1}, 4096), off); err != nil {
			t.Fatal(err)
		}
	}
}

func allocated(t *testing.T, p string) int64 {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		t.Fatal(err)
	}
	return st.Blocks * 512
}

func sameContent(t *testing.T, a, b string) {
	t.Helper()
	x, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(x, y) {
		t.Fatalf("%s and %s differ", a, b)
	}
}

// tmpfsDir is a dir on a tmpfs (no reflinks there), or skips.
func tmpfsDir(t *testing.T) string {
	var st unix.Statfs_t
	if err := unix.Statfs("/dev/shm", &st); err != nil || st.Type != unix.TMPFS_MAGIC {
		t.Skip("no tmpfs at /dev/shm")
	}
	d, err := os.MkdirTemp("/dev/shm", "clone-test-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

// Without reflinks (tmpfs) only the data is copied: the holes stay holes.
func TestCloneSparseKeepsHoles(t *testing.T) {
	dir := tmpfsDir(t)
	src, dst := filepath.Join(dir, "disk.img"), filepath.Join(dir, "copy.img")
	const size = 64 << 20
	sparseFile(t, src, size, 0, 32<<20, size-4096)
	reflinked, err := CloneSparse(context.Background(), src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if reflinked {
		t.Fatal("a reflink on tmpfs")
	}
	fi, err := os.Stat(dst)
	if err != nil || fi.Size() != size || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the copy: %v %v", fi, err)
	}
	if a := allocated(t, dst); a > 64<<10 {
		t.Fatalf("the copy allocates %d bytes: its holes were filled", a)
	}
	sameContent(t, src, dst)
	// an empty and a hole-only file
	sparseFile(t, filepath.Join(dir, "holes.img"), size)
	if _, err := CloneSparse(context.Background(), filepath.Join(dir, "holes.img"), filepath.Join(dir, "holes2.img")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "holes2.img")); fi.Size() != size || allocated(t, filepath.Join(dir, "holes2.img")) != 0 {
		t.Fatalf("a hole-only copy: %v", fi)
	}
}

// reflinkDir is a dir where FICLONE works — the test's temp dir, else one
// beside the package (a btrfs or XFS checkout when /tmp is a tmpfs) — or
// skips.
func reflinkDir(t *testing.T) string {
	t.Helper()
	cands := []string{t.TempDir()}
	if d, err := os.MkdirTemp(".", ".reflink-test-"); err == nil {
		t.Cleanup(func() { os.RemoveAll(d) })
		if abs, err := filepath.Abs(d); err == nil {
			cands = append(cands, abs)
		}
	}
	for _, dir := range cands {
		probe, probe2 := filepath.Join(dir, "p"), filepath.Join(dir, "p2")
		sparseFile(t, probe, 1<<20, 0)
		a, _ := os.Open(probe)
		b, _ := os.Create(probe2)
		err := unix.IoctlFileClone(int(b.Fd()), int(a.Fd()))
		a.Close()
		b.Close()
		if err == nil {
			return dir
		}
	}
	t.Skipf("no reflinks in %v", cands)
	return ""
}

// Where the filesystem shares extents, the copy is a reflink.
func TestCloneSparseReflinks(t *testing.T) {
	dir := reflinkDir(t)
	src, dst := filepath.Join(dir, "disk.img"), filepath.Join(dir, "copy.img")
	sparseFile(t, src, 16<<20, 0, 8<<20)
	reflinked, err := CloneSparse(context.Background(), src, dst)
	if err != nil || !reflinked {
		t.Fatalf("reflinked %v: %v", reflinked, err)
	}
	sameContent(t, src, dst)
	// the copy is its own file
	f, _ := os.OpenFile(dst, os.O_WRONLY, 0)
	f.WriteAt([]byte("changed"), 0)
	f.Close()
	if b, _ := os.ReadFile(src); string(b[:7]) == "changed" {
		t.Fatal("a write to the copy reached the source")
	}
}

// CloneSparse never follows a symlink, never writes over a file, copies
// only regular files, and leaves nothing behind when it fails.
func TestCloneSparseRefuses(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "disk.img")
	sparseFile(t, src, 1<<20, 0)
	os.Symlink(src, filepath.Join(dir, "link"))
	if _, err := CloneSparse(context.Background(), filepath.Join(dir, "link"), filepath.Join(dir, "a")); err == nil {
		t.Fatal("a symlinked source was followed")
	}
	os.WriteFile(filepath.Join(dir, "taken"), []byte("keep"), 0o644)
	if _, err := CloneSparse(context.Background(), src, filepath.Join(dir, "taken")); err == nil {
		t.Fatal("an existing destination was written over")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "taken")); string(b) != "keep" {
		t.Fatalf("the existing destination: %q", b)
	}
	os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(dir, "dlink"))
	if _, err := CloneSparse(context.Background(), src, filepath.Join(dir, "dlink")); err == nil {
		t.Fatal("a symlinked destination was followed")
	}
	if _, err := os.Lstat(filepath.Join(dir, "elsewhere")); err == nil {
		t.Fatal("the destination's link target was made")
	}
	if _, err := CloneSparse(context.Background(), dir, filepath.Join(dir, "b")); err == nil {
		t.Fatal("a directory was copied")
	}
	if _, err := os.Lstat(filepath.Join(dir, "b")); err == nil {
		t.Fatal("a failed copy left its destination")
	}
	// cancelled: nothing left either (tmpfs: the copy is a real one)
	shm := tmpfsDir(t)
	big := filepath.Join(shm, "big.img")
	sparseFile(t, big, 256<<20, 0, 128<<20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CloneSparse(ctx, big, filepath.Join(shm, "c")); err == nil {
		t.Fatal("a cancelled copy succeeded")
	}
	if _, err := os.Lstat(filepath.Join(shm, "c")); err == nil {
		t.Fatal("a cancelled copy left its destination")
	}
}

// Exchange swaps two dirs in one step.
func TestExchange(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.MkdirAll(filepath.Join(a, "sub"), 0o700)
	os.WriteFile(filepath.Join(a, "sub", "x"), []byte("a's"), 0o644)
	os.MkdirAll(b, 0o700)
	os.WriteFile(filepath.Join(b, "y"), []byte("b's"), 0o644)
	if err := Exchange(a, b); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(b, "sub", "x")); string(got) != "a's" {
		t.Fatalf("b after: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(a, "y")); string(got) != "b's" {
		t.Fatalf("a after: %q", got)
	}
	if err := Exchange(a, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("an exchange with nothing succeeded")
	}
	if _, err := os.Stat(filepath.Join(a, "y")); err != nil {
		t.Fatalf("a failed exchange moved something: %v", err)
	}
}
