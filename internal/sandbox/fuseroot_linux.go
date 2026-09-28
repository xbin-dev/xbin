//go:build linux

package sandbox

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

// fuse-overlayfs must not be rooted in the mount it serves.
//
// It reads and lists extended attributes by path, through
// "/proc/self/fd/<layer fd>/<name>" (there is no *xattrat call it could
// use), so it walks its own root on almost every file. If a plain
// pivot_root made the sandbox's root fuse-overlayfs's root too, that walk
// went through its own FUSE mount. A change to `/` (a create, mkdir,
// unlink or rename there) makes the kernel invalidate the root's attributes,
// and with default_permissions the next walk through `/` must first ask the
// server for them again. When that walk is fuse-overlayfs's own, for
// example lgetxattr(security.capability) while it serves the write that
// follows `echo > /x`, it asks itself. It holds one big lock, so
// `-o threaded=1` doesn't help: the root is wedged for good. Its path-based
// fallbacks through its layers' host paths walked the same mount.
//
// fuseServerRoot gives it a root of its own, before the init pivots into the
// FUSE mount. The root is a read-only tmpfs holding only a proc mount of the
// sandbox's pid namespace, and it never contains the host's tree. It works
// with two pivot_roots. pivot_root(srv, srv) stacks the host's root on srv,
// and every process rooted at the host's root moves to srv: the init, and
// fuse-overlayfs, which started from "/" (daemonized, or with Dir "/" when
// watched). The init alone then returns to the host's root through a
// descriptor opened before the pivot, and the caller's pivot_root into the
// FUSE mount moves only the processes rooted there. It attaches the FUSE
// mount where the host's root was, stacked on srv's root. The sandbox's root
// therefore stays the top of its mount namespace, not a chroot of it: a
// nested user namespace (rootless podman, `unshare -U`) needs exactly that
// (create_user_ns refuses a chrooted caller). fuse-overlayfs, rooted
// beneath the stack, resolves /proc in srv and never through FUSE.
func fuseServerRoot(base string) error {
	srv := filepath.Join(base, "fuse-root")
	if err := unix.Mkdir(srv, 0o755); err != nil {
		return must(err, "mkdir fuse-overlayfs's root")
	}
	const flags = unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC
	if err := unix.Mount("tmpfs", srv, "tmpfs", flags, "mode=0555,size=4k"); err != nil {
		return must(err, "mount fuse-overlayfs's root")
	}
	if err := unix.Mkdir(filepath.Join(srv, "proc"), 0o555); err != nil {
		return must(err, "mkdir fuse-overlayfs's /proc")
	}
	if err := unix.Mount("proc", filepath.Join(srv, "proc"), "proc", flags, ""); err != nil {
		return must(err, "mount fuse-overlayfs's /proc")
	}
	if err := unix.Mount("", srv, "", unix.MS_REMOUNT|unix.MS_RDONLY|flags, ""); err != nil {
		return must(err, "remount fuse-overlayfs's root read-only")
	}
	host, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return must(err, "open the host's root")
	}
	defer unix.Close(host)
	if err := unix.PivotRoot(srv, srv); err != nil {
		return must(err, "pivot_root fuse-overlayfs's root")
	}
	if err := unix.Fchdir(host); err != nil {
		return must(err, "back to the host's root")
	}
	return must(unix.Chroot("."), "back to the host's root")
}
