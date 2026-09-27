//go:build linux

package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// covers WP-2b — FollowBase: a symlink the base rootfs ships as it is (the
// rootfs's own /lib → usr/lib, /var/run → /run) is followed inside the new
// root, never against the host's; one the sandbox made, or changed, is
// refused with the spec's hint; a mask's walk follows any symlink, inside
// the root too; nothing is ever made where a symlink points on the host.
func TestWalkFollowsBase(t *testing.T) {
	dir := t.TempDir()
	base, root, outside := filepath.Join(dir, "base"), filepath.Join(dir, "root"), filepath.Join(dir, "outside")
	for _, d := range []string{"usr/lib", "usr/bin", "run", "etc"} {
		tMkdir(t, filepath.Join(base, d))
		tMkdir(t, filepath.Join(root, d))
	}
	tMkdir(t, filepath.Join(base, "var"))
	tMkdir(t, filepath.Join(root, "var"))
	tMkdir(t, outside)
	tMkdir(t, filepath.Join(root, "hidden"))
	shipped := map[string]string{
		"lib": "usr/lib", "var/run": "/run", "var/lock": "/run/lock", "usr/bin/X11": ".", "up": "../..",
	}
	for link, target := range shipped {
		for _, tree := range []string{base, root} {
			if err := os.Symlink(target, filepath.Join(tree, link)); err != nil {
				t.Fatal(err)
			}
		}
	}
	// what the sandbox left in its root: a new link, a changed one, and the
	// workspace's own links a mask meets
	if err := os.Remove(filepath.Join(root, "var/lock")); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"opt": outside, "var/lock": outside, "etc/rel": "../../outside",
		"data": "/elsewhere", "data2": "hidden", "loop": "loop", "etc/abs": outside,
		"dangling": filepath.Join(outside, "missing"),
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	rfd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(rfd)
	bfd, err := unix.Open(base, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(bfd)
	w := &walk{root: rfd, base: bfd, hint: "reset it"}
	strict := &walk{root: rfd, base: -1}

	// shipped links are followed, inside the root
	for dst, landed := range map[string]string{
		"/lib/xbin/sdk":         "usr/lib/xbin/sdk",
		"/var/run/xbin-x/run":   "run/xbin-x/run",
		"/usr/bin/X11/X11/tool": "usr/bin/tool",
		"/up/etc/in-root":       "etc/in-root", // ".." never climbs above the root
	} {
		fd, err := w.point(dst, true, true, false)
		if err != nil {
			t.Fatalf("point %s: %v", dst, err)
		}
		unix.Close(fd)
		if fi, err := os.Lstat(filepath.Join(root, landed)); err != nil || !fi.IsDir() {
			t.Errorf("%s: %s not made: %v", dst, landed, err)
		}
		// the same walk without FollowBase refuses the image's own link
		if fd, err := strict.point(dst, true, true, false); err == nil {
			unix.Close(fd)
			t.Errorf("%s went through a symlink without FollowBase", dst)
		}
	}
	fd, err := w.point("/lib/libcuda.so.1", false, true, false) // a file mount point
	if err != nil {
		t.Fatal(err)
	}
	unix.Close(fd)
	if fi, err := os.Lstat(filepath.Join(root, "usr/lib/libcuda.so.1")); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("file mount point: %v %v", fi, err)
	}

	// what the sandbox made or changed is refused, with the hint
	for dst, want := range map[string]string{
		"/opt/xbin/sdk":    "nested mount point /opt: a symlink is in the way (reset it)",
		"/var/lock/x":      "nested mount point /var/lock: a symlink is in the way (reset it)", // the image's link, retargeted
		"/etc/rel/x":       "nested mount point /etc/rel: a symlink is in the way",
		"/etc/abs":         "nested mount point /etc/abs: a symlink is in the way",
		"/up":              "leads to the root",
		"/lib/../opt/xbin": "nested mount point /opt: a symlink is in the way",
		"/dangling/x":      "nested mount point /dangling: a symlink is in the way", // never made through
	} {
		for _, create := range []bool{true, false} {
			fd, err := w.point(dst, true, create, false)
			if err == nil {
				unix.Close(fd)
				t.Errorf("point %s (create=%v) went through", dst, create)
			} else if !strings.Contains(err.Error(), want) {
				t.Errorf("point %s (create=%v): %v, want %q", dst, create, err, want)
			}
		}
	}

	// a mask's walk follows any symlink, inside the root
	fd, err = w.point("/data2", true, false, true)
	if err != nil {
		t.Fatalf("mask through data2: %v", err)
	}
	var st, hidden unix.Stat_t
	_ = unix.Fstat(fd, &st)
	unix.Close(fd)
	if err := unix.Stat(filepath.Join(root, "hidden"), &hidden); err != nil || st.Ino != hidden.Ino {
		t.Errorf("the mask's walk landed elsewhere than /hidden")
	}
	for dst, want := range map[string]error{"/data": unix.ENOENT, "/opt": unix.ENOENT, "/etc/abs": unix.ENOENT} {
		if fd, err := w.point(dst, true, false, true); !errors.Is(err, want) {
			if err == nil {
				unix.Close(fd)
			}
			t.Errorf("mask %s: %v, want %v (its target isn't in the root)", dst, err, want)
		}
	}
	if fd, err := w.point("/loop", true, false, true); err == nil || !strings.Contains(err.Error(), "too many symlinks") {
		if err == nil {
			unix.Close(fd)
		}
		t.Errorf("a symlink loop: %v", err)
	}

	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("made %v where a symlink pointed", ents)
	}
}
