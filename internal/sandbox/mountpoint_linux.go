//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

// Mount points in a NoFollow root (Spec.NoFollow): each one the init makes
// in the new root is found from the root one component at a time without
// following a symlink, and made where missing, so a symlink the sandbox left
// in its root — a terminal's persistent upper, a backend's environment
// layer, a tile sandbox's upper — can neither redirect a mount nor make a
// directory or a file on the host (before pivot_root an absolute symlink
// resolves against the host's root). Two kinds of symlink are followed, and
// always inside the new root, never against the host's:
//   - with FollowBase, one the base rootfs (the last Lower) ships as it is:
//     the same link to the same target at the same path (shipped);
//   - on a mask's path, any: a mask only covers, and what it covers is what
//     that path reaches inside the sandbox.
//
// Following restarts the walk from the root on the path the link names, so
// every component of it is checked the same way.

// maxHops bounds the symlinks one walk follows (the kernel's MAXSYMLINKS).
const maxHops = 40

// walk finds the mount points of a NoFollow root.
type walk struct {
	root int    // the new root, O_PATH
	base int    // the base rootfs, O_PATH (FollowBase), or -1
	hint string // Spec.RootHint: ends a refusal
}

// openWalk opens the new root (after its root is mounted) and, with
// FollowBase, the base rootfs. A VM's root is a bare tmpfs: no base.
func openWalk(s *Spec, newroot string) (*walk, error) {
	root, err := unix.Open(newroot, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, must(err, "open new root")
	}
	w := &walk{root: root, base: -1, hint: s.RootHint}
	if s.FollowBase && s.VM == nil && len(s.Lower) > 0 {
		if w.base, err = unix.Open(s.Lower[len(s.Lower)-1], unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0); err != nil {
			unix.Close(root)
			return nil, must(err, "open the base rootfs")
		}
	}
	return w, nil
}

func (w *walk) close() {
	unix.Close(w.root)
	if w.base >= 0 {
		unix.Close(w.base)
	}
}

// blocked is a refusal of a mount point met in dir, with the spec's hint
// when dir is on the root's own filesystem — the layers the hint names. One
// in a mount the walk crossed (a bind of a host dir: the workspace's own
// symlinks) isn't the layer's, and the hint's reset wouldn't clear it.
func (w *walk) blocked(dir int, format string, a ...any) error {
	msg := fmt.Sprintf(format, a...)
	var r, d unix.Stat_t
	if w.hint != "" && (dir == w.root || unix.Fstat(w.root, &r) == nil && unix.Fstat(dir, &d) == nil && r.Dev == d.Dev) {
		msg += " (" + w.hint + ")"
	}
	return errors.New(msg)
}

// pointAt is a walk that follows nothing (writeInRoot's, the tests').
func pointAt(rootfd int, dst string, isDir, create bool) (int, error) {
	return (&walk{root: rootfd, base: -1}).point(dst, isDir, create, false)
}

// point opens dst's mount point, one component at a time without following
// a symlink (openNoFollow), making what is missing when create: directories,
// and the last an empty file when !isDir. Without create a missing
// component is unix.ENOENT. Walking crosses the mounts already made, so a
// later bind nests in an earlier one. A symlink on the way fails it unless
// it is shipped or anyLink (a mask's walk) says to follow it.
func (w *walk) point(dst string, isDir, create, anyLink bool) (int, error) {
	comps := splitPath(dst)
	if comps == nil {
		return -1, fmt.Errorf("mount point %s: the root itself", dst)
	}
	for hops := 0; ; hops++ {
		fd, next, err := w.try(comps, isDir, create, anyLink)
		switch {
		case err != nil || next == nil:
			return fd, err
		case hops == maxHops:
			return -1, w.blocked(w.root, "mount point %s: too many symlinks", dst)
		case len(next) == 0:
			return -1, w.blocked(w.root, "mount point %s: a symlink on its path leads to the root", dst)
		}
		comps = next
	}
}

// try is one pass of point from the root: the mount point's fd, or the path
// to restart on (non-nil; empty for the root) after a symlink it follows.
func (w *walk) try(comps []string, isDir, create, anyLink bool) (int, []string, error) {
	dir, at := w.root, ""
	defer func() {
		if dir != w.root {
			unix.Close(dir)
		}
	}()
	for i, c := range comps {
		at += "/" + c
		wantDir := isDir || i < len(comps)-1
		flags := unix.O_PATH
		if wantDir {
			flags |= unix.O_DIRECTORY
		}
		next, err := openNoFollow(dir, c, flags)
		if errors.Is(err, unix.ENOENT) && create {
			// nestedPoint makes it; neither mkdirat nor O_EXCL follows a
			// symlink, and its errors are already worded
			if next, err = nestedPoint(dir, c, at, wantDir); err != nil {
				return -1, nil, err
			}
		}
		switch {
		case err == nil:
		case errors.Is(err, unix.ENOENT):
			return -1, nil, err
		default:
			if target, ok := readLink(dir, c); ok {
				if anyLink || w.shipped(at, target) {
					return -1, follow(at, target, comps[i+1:]), nil
				}
				return -1, nil, w.blocked(dir, "nested mount point %s: a symlink is in the way", at)
			}
			switch {
			case errors.Is(err, unix.ENOTDIR):
				return -1, nil, w.blocked(dir, "nested mount point %s: not a directory", at)
			case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
				return -1, nil, w.blocked(dir, "nested mount point %s: a symlink is in the way", at)
			}
			return -1, nil, must(err, "open "+at)
		}
		if dir != w.root {
			unix.Close(dir)
		}
		dir = next
	}
	fd := dir
	dir = w.root // handed to the caller, not closed by the defer
	return fd, nil, nil
}

// shipped reports whether the base rootfs has the symlink at (a path in the
// root) as the root has it: to target, reached through directories only.
func (w *walk) shipped(at, target string) bool {
	if w.base < 0 {
		return false
	}
	comps := splitPath(at)
	dir := w.base
	defer func() {
		if dir != w.base {
			unix.Close(dir)
		}
	}()
	for _, c := range comps[:len(comps)-1] {
		next, err := openNoFollow(dir, c, unix.O_PATH|unix.O_DIRECTORY)
		if err != nil {
			return false
		}
		if dir != w.base {
			unix.Close(dir)
		}
		dir = next
	}
	t, ok := readLink(dir, comps[len(comps)-1])
	return ok && t == target
}

// readLink is name's target when name (beneath dir) is a symlink.
func readLink(dir int, name string) (string, bool) {
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(dir, name, buf)
	if err != nil || n >= len(buf) {
		return "", false
	}
	return string(buf[:n]), true
}

// follow is the path a walk restarts on after following the symlink at to
// target, then rest: an absolute target from the new root, a relative one
// from at's directory. It is resolved lexically, which is the kernel's
// answer here — at's parents are directories the walk opened — and ".."
// never climbs above the root.
func follow(at, target string, rest []string) []string {
	p := target
	if !path.IsAbs(p) {
		p = path.Join(path.Dir(at), p)
	}
	comps := splitPath(path.Join(append([]string{p}, rest...)...))
	if comps == nil {
		return []string{}
	}
	return comps
}

// splitPath is p's components below the root (nil for the root itself).
func splitPath(p string) []string {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// mountAt is init_linux.go's mountAt for a NoFollow root (newroot is the
// walk's root).
func (w *walk) mountAt(_, rel, source, fstype string, flags uintptr, data string) error {
	fd, err := w.point(rel, true, true, false)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return must(unix.Mount(source, fdPath(fd), fstype, flags, data), "mount "+fstype+" at "+rel)
}

// mountBindsNoFollow is the bind loop for a NoFollow root: ancestors first
// (sortBinds), every mount point found with the walk, the bind made on the
// opened mount point and its read-only remount on the mount found again, so
// no path string is ever resolved as a path. A read-only bind that a later
// bind nests in is remounted read-only only after everything is mounted,
// since the nested mount point may have to be made in it.
func mountBindsNoFollow(w *walk, binds []Bind, debug bool) error {
	binds = sortBinds(binds)
	var seal []int
	defer func() {
		for _, fd := range seal {
			unix.Close(fd)
		}
	}()
	for i, b := range binds {
		dbg(debug, "bind %q sub %q -> %q (ro=%v mask=%v, no-follow)", b.Src, b.Sub, b.Dst, b.RO, b.Mask)
		dst := path.Clean("/" + b.Dst)
		if b.Mask {
			fd, err := w.point(dst, true, false, true)
			if errors.Is(err, unix.ENOENT) {
				continue // nothing beneath to hide
			}
			if err != nil {
				return err
			}
			err = mountMask(fdPath(fd), b.RO)
			unix.Close(fd)
			if err != nil {
				return err
			}
			continue
		}
		src, isDir, release, err := bindSource(b)
		if err != nil {
			return err
		}
		mp, err := w.point(dst, isDir, true, false)
		if err == nil {
			// Always recursive, as mountBind (see Bind's doc).
			err = must(unix.Mount(src, fdPath(mp), "", unix.MS_BIND|unix.MS_REC, ""), "bind "+b.Src+" -> "+b.Dst)
			unix.Close(mp)
		}
		release()
		if err != nil {
			return err
		}
		if !b.RO {
			continue
		}
		top, err := w.point(dst, isDir, false, false) // lands on top of the new mount
		if err != nil {
			return must(err, "open bind root "+b.Dst)
		}
		if slices.ContainsFunc(binds[i+1:], func(n Bind) bool { return beneath(path.Clean("/"+n.Dst), dst) }) {
			seal = append(seal, top)
			continue
		}
		err = remountRO(fdPath(top))
		unix.Close(top)
		if err != nil {
			return must(err, "remount ro "+b.Dst)
		}
	}
	for _, fd := range seal {
		if err := remountRO(fdPath(fd)); err != nil {
			return must(err, "remount ro (sealed)")
		}
	}
	return nil
}

// oldroot makes the pivot's .oldroot in a NoFollow root as a real
// directory, never through a symlink.
func (w *walk) oldroot() error {
	fd, err := w.point("/.oldroot", true, true, false)
	if err == nil {
		unix.Close(fd)
	}
	return err
}

// writeInRoot writes data to p (absolute, in the new root) before the pivot,
// never through a symlink: p's directory is walked with pointAt (made where
// missing), and a symlink or other non-file at p is replaced by a regular
// file. Before pivot_root an absolute symlink resolves against the host's
// root, so a path-based write would land wherever the sandbox pointed it.
func writeInRoot(newroot, p string, data []byte) error {
	return inRoot(newroot, func(root int) error {
		dir, err := pointAt(root, path.Dir(p), true, true)
		if err != nil {
			return err
		}
		defer unix.Close(dir)
		name := path.Base(p)
		flags := unix.O_WRONLY | unix.O_CREAT | unix.O_TRUNC | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
		fd, err := unix.Openat(dir, name, flags, 0o644)
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENXIO) || errors.Is(err, unix.EISDIR) {
			if uerr := unix.Unlinkat(dir, name, 0); uerr != nil {
				return must(uerr, "replace "+p)
			}
			fd, err = unix.Openat(dir, name, flags|unix.O_EXCL, 0o644)
		}
		if err != nil {
			return must(err, "write "+p)
		}
		var st unix.Stat_t
		if err = unix.Fstat(fd, &st); err == nil && st.Mode&unix.S_IFMT != unix.S_IFREG {
			err = fmt.Errorf("not a regular file")
		}
		if err == nil {
			_, err = unix.Write(fd, data)
		}
		unix.Close(fd)
		return must(err, "write "+p)
	})
}

// inRoot runs fn with an O_PATH fd on the new root.
func inRoot(newroot string, fn func(root int) error) error {
	root, err := unix.Open(newroot, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return must(err, "open new root")
	}
	defer unix.Close(root)
	return fn(root)
}
