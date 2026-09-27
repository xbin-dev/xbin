//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

func TestEntryArgv(t *testing.T) {
	for _, c := range []struct {
		s    Spec
		fuse int
		want []string
	}{
		{Spec{Entry: "/e"}, 0, []string{"/e"}},
		{Spec{Entry: "/e", Argv: []string{"bx", "__sbx-agent"}}, 0, []string{"bx", "__sbx-agent"}},
		{Spec{Argv: []string{"bx", "__sbx-agent"}, AgentFD: 5, LockFD: 6}, 0, []string{"bx", "__sbx-agent", "--fd", "5", "--lock", "6"}},
		{Spec{Argv: []string{"bx"}, AgentFD: 3}, 0, []string{"bx", "--fd", "3"}},
		{Spec{Argv: []string{"bx"}, LockFD: 4}, 0, []string{"bx"}},                                                             // a lock alone: nothing to tell
		{Spec{Argv: []string{"bx", "__vm-host", "s"}, AgentFD: 3, VM: &proto.HostSpec{}}, 0, []string{"bx", "__vm-host", "s"}}, // the shim reads its HostSpec
		{Spec{Argv: []string{"bx"}, AgentFD: 4, LockFD: 5}, 2, []string{"bx", "--fd", "4", "--lock", "5", "--fuse-pid", "2"}},  // FuseWatch
		{Spec{Argv: []string{"bx"}, AgentFD: 4}, 7, []string{"bx", "--fd", "4", "--fuse-pid", "7"}},
		{Spec{Argv: []string{"sh"}}, 2, []string{"sh"}}, // no agent: nobody to tell
	} {
		argv := slices.Clone(c.s.Argv)
		if got := entryArgv(&c.s, c.fuse); !slices.Equal(got, c.want) {
			t.Errorf("entryArgv(%+v) = %q, want %q", c.s, got, c.want)
		}
		if !slices.Equal(c.s.Argv, argv) {
			t.Errorf("entryArgv changed the spec's argv: %q", c.s.Argv)
		}
	}
}

// Bind.Sub resolves beneath its root and refuses a symlink anywhere in the
// sub-path, a ".." and an absolute path, without touching what they point at.
func TestOpenSubRefusesSymlinks(t *testing.T) {
	dir := t.TempDir()
	root, outside := filepath.Join(dir, "res"), filepath.Join(dir, "outside")
	for _, d := range []string{filepath.Join(root, "a", "b"), outside} {
		tMkdir(t, d)
	}
	tWrite(t, filepath.Join(root, "a", "b", "f"), "inside")
	tWrite(t, filepath.Join(outside, "f"), "outside")
	for link, target := range map[string]string{"abs": outside, "rel": "../outside", "a/up": "../../outside", "a/b/lf": "f"} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	fd, err := openSub(root, "a/b/f")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(fdPath(fd)); err != nil || string(b) != "inside" {
		t.Errorf("a/b/f through the fd: %q %v", b, err)
	}
	unix.Close(fd)
	for _, sub := range []string{"abs", "abs/f", "rel/f", "a/up/f", "a/b/lf"} {
		if fd, err := openSub(root, sub); err == nil {
			unix.Close(fd)
			t.Errorf("sub %q through a symlink was opened", sub)
		} else if !strings.Contains(err.Error(), "a symlink is in the way") {
			t.Errorf("sub %q: %v (want a symlink refusal)", sub, err)
		}
	}
	for _, sub := range []string{"../outside", "a/../../outside", "/etc", "a//b", "a/b/"} {
		if fd, err := openSub(root, sub); err == nil {
			unix.Close(fd)
			t.Errorf("sub %q was opened", sub)
		}
	}
}

// Mount points in a NoFollow root: made where missing, walked without
// following; a symlink (absolute, relative, at the point or above it) or a
// file in the way fails naming the path, and nothing is made where the
// symlink points.
func TestPointAtNoFollow(t *testing.T) {
	dir := t.TempDir()
	root, outside := filepath.Join(dir, "root"), filepath.Join(dir, "outside")
	tMkdir(t, filepath.Join(root, "d"))
	tMkdir(t, outside)
	tWrite(t, filepath.Join(root, "file"), "x")
	for link, target := range map[string]string{"abs": outside, "rel": "../outside", "d/up": "../../outside"} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	rfd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(rfd)

	for dst, isDir := range map[string]bool{"/new/deep/dir": true, "/d/cfg.json": false, "/d": true} {
		fd, err := pointAt(rfd, dst, isDir, true)
		if err != nil {
			t.Fatalf("pointAt %s: %v", dst, err)
		}
		unix.Close(fd)
		fi, err := os.Lstat(filepath.Join(root, dst))
		if err != nil || fi.IsDir() != isDir || fi.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s: %v %v (want isDir=%v)", dst, fi, err, isDir)
		}
	}
	for dst, want := range map[string]string{
		"/abs": "/abs: a symlink is in the way", "/abs/sub": "/abs: a symlink is in the way",
		"/rel/sub": "/rel: a symlink is in the way", "/d/up/sub": "/d/up: a symlink is in the way",
		"/file/sub": "/file: not a directory",
	} {
		for _, create := range []bool{true, false} {
			fd, err := pointAt(rfd, dst, true, create)
			if err == nil {
				unix.Close(fd)
				t.Errorf("pointAt %s (create=%v) went through", dst, create)
			} else if !strings.Contains(err.Error(), want) {
				t.Errorf("pointAt %s (create=%v): %v, want %q", dst, create, err, want)
			}
		}
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("made %v where a symlink pointed", ents)
	}
	if _, err := pointAt(rfd, "/", true, true); err == nil {
		t.Error("the root itself is a mount point")
	}
}

// writeInRoot replaces a symlink planted at the file (absolute or relative)
// with a regular file and never writes through it.
func TestWriteInRootNeverFollows(t *testing.T) {
	dir := t.TempDir()
	root, victim := filepath.Join(dir, "root"), filepath.Join(dir, "victim")
	tMkdir(t, filepath.Join(root, "etc"))
	tMkdir(t, filepath.Join(root, "run"))
	tWrite(t, victim, "precious")
	tWrite(t, filepath.Join(root, "run", "stub"), "stub")
	for _, target := range []string{victim, "../run/stub", "missing"} {
		p := filepath.Join(root, "etc", "resolv.conf")
		_ = os.Remove(p)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		if err := writeInRoot(root, "/etc/resolv.conf", []byte("nameserver 10.0.2.3\n")); err != nil {
			t.Fatalf("over a symlink to %s: %v", target, err)
		}
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			t.Fatalf("resolv.conf after replacing a symlink to %s: %v %v", target, fi, err)
		}
		if b, _ := os.ReadFile(p); string(b) != "nameserver 10.0.2.3\n" {
			t.Errorf("content %q", b)
		}
	}
	if b, _ := os.ReadFile(victim); string(b) != "precious" {
		t.Errorf("the symlink's target was written: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "run", "stub")); string(b) != "stub" {
		t.Errorf("a relative symlink's target was written: %q", b)
	}
	// A regular file is overwritten in place; a missing directory is made.
	if err := writeInRoot(root, "/etc/resolv.conf", []byte("again\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeInRoot(root, "/etc/new/hosts", []byte("h\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "etc", "new", "hosts")); string(b) != "h\n" {
		t.Errorf("hosts: %q", b)
	}
	// A symlinked directory above the file is refused, the target untouched.
	if err := os.Symlink(dir, filepath.Join(root, "etc2")); err != nil {
		t.Fatal(err)
	}
	if err := writeInRoot(root, "/etc2/victim", []byte("x")); err == nil {
		t.Error("wrote through a symlinked directory")
	}
	if b, _ := os.ReadFile(victim); string(b) != "precious" {
		t.Errorf("the victim was written: %q", b)
	}
}

func tWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}
