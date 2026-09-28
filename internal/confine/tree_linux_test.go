//go:build linux && integration

package confine

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	fusefs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// The file-caps profile in a real sandbox: a confined cp -a copies a tree
// holding a whiteout (0:0 char device), a 0600 and a 0000 file and a sealed
// dir, keeping all of it, and a confined rm removes it — what a run without
// the caps can't do. Where the host delegates a sub-uid range, the tree also
// holds a 0700 dir and a 0600 file of another sub-uid (what apt leaves in a
// tile sandbox's upper), which xbind itself can't read; on a single-uid host
// the locked modes stand in for them.
func TestFSCapsTree(t *testing.T) {
	rootfs := testRootfs(t)
	ctx := context.Background()
	root := t.TempDir()
	t.Cleanup(func() { unlock(root) }) // whatever a failure left, TempDir can remove
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(src, "usr", "lib"), 0o755))
	must(os.MkdirAll(filepath.Join(src, "sealed"), 0o755))
	must(os.Mkdir(dst, 0o700))
	must(unix.Mknod(filepath.Join(src, "usr", "lib", "removed-pkg"), unix.S_IFCHR, 0)) // an overlay whiteout
	must(os.WriteFile(filepath.Join(src, "private"), []byte("private"), 0o600))
	must(os.WriteFile(filepath.Join(src, "locked"), []byte("locked"), 0o600))
	must(os.WriteFile(filepath.Join(src, "sealed", "inner"), []byte("inner"), 0o644))
	must(os.Symlink("/etc/passwd", filepath.Join(src, "passwd")))
	must(os.Chmod(filepath.Join(src, "locked"), 0))
	must(os.Chmod(filepath.Join(src, "sealed"), 0))
	must(os.Chmod(filepath.Join(src, "usr"), 0o555))

	Configure(rootfs)
	defer Configure("")
	t.Cleanup(func() { // a failure's sub-uid-owned leftovers: only a confined rm can clear them
		Configure(rootfs)
		defer Configure("")
		_ = RemoveAll(ctx, src)
		_ = RemoveAll(ctx, dst)
	})

	ranged, _ := sandbox.IDMapStatus(os.Getuid(), os.Getgid())
	owned := filepath.Join(src, "owned")
	if ranged {
		must(os.MkdirAll(filepath.Join(owned, "sub"), 0o755))
		must(os.WriteFile(filepath.Join(owned, "sub", "f"), []byte("sub-uid"), 0o600))
		if _, err := Run(ctx, Cmd{Argv: []string{"sh", "-c", "chown -R 1000:1000 owned && chmod 0700 owned/sub"},
			Dir: src, FSCaps: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.ReadFile(filepath.Join(owned, "sub", "f")); err == nil {
			t.Fatal("xbind can still read the sub-uid's file: the chown didn't take")
		}
	} else {
		t.Log("single-uid host: no sub-uid-owned files in the tree")
	}

	// without the caps the same tool is locked out: the test proves something
	if _, err := Run(ctx, Cmd{Argv: []string{"cp", "-a", "--", src + "/.", dst + "/"}, Dir: dst,
		Binds: []sandbox.Bind{RO(src)}}); err == nil {
		t.Fatal("a capability-less cp -a read a 0000 file and a sealed dir")
	}
	must(RemoveAll(ctx, dst)) // what it did copy
	must(os.Mkdir(dst, 0o700))

	if err := CopyTree(ctx, src, dst); err != nil {
		t.Fatal(err)
	}
	wh, err := os.Lstat(filepath.Join(dst, "usr", "lib", "removed-pkg"))
	if err != nil || wh.Mode()&os.ModeCharDevice == 0 || wh.Sys().(*syscall.Stat_t).Rdev != 0 {
		t.Fatalf("the whiteout did not survive the copy: %v %v", wh, err)
	}
	for name, want := range map[string]os.FileMode{"locked": 0, "private": 0o600, "sealed": os.ModeDir, "usr": os.ModeDir | 0o555} {
		fi, err := os.Lstat(filepath.Join(dst, name))
		if err != nil || fi.Mode() != want {
			t.Errorf("%s: mode %v, want %v (%v)", name, fi.Mode(), want, err)
		}
	}
	if ranged {
		so, err1 := os.Lstat(filepath.Join(owned, "sub"))
		co, err2 := os.Lstat(filepath.Join(dst, "owned", "sub"))
		if err1 != nil || err2 != nil {
			t.Fatal(err1, err2)
		}
		uid := so.Sys().(*syscall.Stat_t).Uid
		if uid == uint32(os.Getuid()) || co.Sys().(*syscall.Stat_t).Uid != uid || co.Mode() != os.ModeDir|0o700 {
			t.Errorf("the sub-uid's dir came back as uid %d %v (source uid %d)", co.Sys().(*syscall.Stat_t).Uid, co.Mode(), uid)
		}
		res, err := Run(ctx, Cmd{Argv: []string{"cat", "owned/sub/f"}, Dir: dst, ReadOnlyDir: true, FSCaps: true})
		if err != nil || string(res.Stdout) != "sub-uid" {
			t.Errorf("the sub-uid's file: %q %v", res.Stdout, err)
		}
	}
	unlock(dst) // the host side reads the copy as its owner
	for name, want := range map[string]string{"locked": "locked", "private": "private", "sealed/inner": "inner"} {
		if b, _ := os.ReadFile(filepath.Join(dst, name)); string(b) != want {
			t.Errorf("%s = %q, want %q", name, b, want)
		}
	}
	if l, _ := os.Readlink(filepath.Join(dst, "passwd")); l != "/etc/passwd" {
		t.Errorf("the link came back as %q", l)
	}
	if n, err := DiskUsage(ctx, src); err != nil || n <= 0 {
		t.Errorf("DiskUsage = %d, %v", n, err)
	}

	// rm: a capability-less run can't clear the sealed dir; the caps can
	if _, err := Run(ctx, Cmd{Argv: []string{"find", src, "-mindepth", "1", "-delete"}, Dir: src}); err == nil {
		t.Fatal("a capability-less find -delete emptied a sealed dir")
	}
	for _, d := range []string{src, dst} {
		if err := RemoveAll(ctx, d); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(d); !os.IsNotExist(err) {
			t.Errorf("%s survived", d)
		}
	}
	if _, err := os.Stat("/etc/passwd"); err != nil {
		t.Fatal("removing the tree followed its link")
	}

	// file caps only: no mounts, no device nodes, no nested user namespace
	for _, sh := range []string{"mount -t tmpfs none /mnt", "mknod /tmp/null c 1 3", "unshare -U true"} {
		res, err := Run(ctx, Cmd{Argv: []string{"sh", "-c", sh}, FSCaps: true})
		if err == nil {
			t.Errorf("%q succeeded under the file-caps profile: %s", sh, res.Stdout)
		} else if strings.Contains(err.Error(), "could not start") {
			t.Fatal(err)
		}
	}
}

// unlock makes a whole tree readable and removable by its owner (a dir is
// opened up before WalkDir lists it).
func unlock(root string) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if d != nil && d.Type()&fs.ModeSymlink == 0 {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
}

// Confined, as direct: a copied upper keeps every user xattr exactly —
// both overlay flavours' opaque markers and ownership overrides included.
func TestCopyTreeXattrs(t *testing.T) {
	rootfs := testRootfs(t)
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	if err := os.Mkdir(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	if !xattrTree(t, src) {
		t.Skip("the temp dir's filesystem takes no user xattrs")
	}
	Configure(rootfs)
	defer Configure("")
	if err := CopyTree(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	checkXattrs(t, dst)
}

// A destination that refuses user xattrs fails the copy, confined or
// direct: an error, never a silent drop. The same tree without them copies
// onto it fine, so the xattrs are what fails it.
func TestCopyTreeRefusedXattrs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	src, plain := filepath.Join(root, "src"), filepath.Join(root, "plain")
	if !xattrTree(t, src) {
		t.Skip("the temp dir's filesystem takes no user xattrs")
	}
	if err := os.MkdirAll(filepath.Join(plain, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(plain, "d", "f"), []byte("f"), 0o600)
	mnt := noXattrFS(t, root)

	copyOnto := func(t *testing.T, mode string) {
		for _, c := range []struct {
			src     string
			refused bool
		}{{plain, false}, {src, true}} {
			dst := filepath.Join(mnt, mode+"-"+filepath.Base(c.src))
			if err := os.Mkdir(dst, 0o700); err != nil {
				t.Fatal(err)
			}
			err := CopyTree(ctx, c.src, dst)
			switch {
			case c.refused && err == nil:
				t.Errorf("%s: the xattrs were dropped without an error", c.src)
			case c.refused:
				t.Logf("refused as it should be: %v", err)
			case err != nil:
				t.Errorf("%s (no xattrs): %v", c.src, err)
			}
		}
		if b, err := os.ReadFile(filepath.Join(mnt, mode+"-plain", "d", "f")); err != nil || string(b) != "f" {
			t.Errorf("the plain copy: %q %v", b, err)
		}
	}
	t.Run("direct", func(t *testing.T) { copyOnto(t, "direct") })
	t.Run("confined", func(t *testing.T) {
		Configure(testRootfs(t))
		defer Configure("")
		copyOnto(t, "confined")
	})
}

// noXattrFS mounts, under root, a FUSE loopback of a fresh dir that answers
// every xattr call ENOSYS — a filesystem without user xattrs, as tmpfs was
// before Linux 6.6 — and returns its mount point. Skips where FUSE isn't.
func noXattrFS(t *testing.T, root string) string {
	t.Helper()
	back, mnt := filepath.Join(root, "noxattr-back"), filepath.Join(root, "noxattr")
	for _, d := range []string{back, mnt} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	lb, err := fusefs.NewLoopbackRoot(back)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := fusefs.Mount(mnt, lb, &fusefs.Options{MountOptions: fuse.MountOptions{DisableXAttrs: true, FsName: "noxattr"}})
	if err != nil {
		t.Skip("no FUSE here:", err)
	}
	t.Cleanup(func() { _ = srv.Unmount() })
	probe := filepath.Join(mnt, "probe")
	_ = os.WriteFile(probe, nil, 0o644)
	if err := unix.Setxattr(probe, "user.test", []byte("x"), 0); err == nil {
		t.Skip("the FUSE mount took a user xattr")
	}
	_ = os.Remove(probe)
	return mnt
}
