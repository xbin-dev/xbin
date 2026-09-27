//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// The init's side of a tile sandbox's launch (plans/tile-sandbox-runtime.md
// §2.1, §5): fd hygiene for the agent's factory and the state lock, the
// hostname, Bind.Sub, and mount points that are never followed (NoFollow).

// initFDs runs first in the init's final stage — after a range-mode re-exec,
// which must keep every fd — and before it starts anything: the agent's
// factory and the relay's control socket become close-on-exec, so
// fuse-overlayfs never inherits them (the entry gets the factory back in
// handAgentFD); the lock stays inheritable, since fuse-overlayfs holding it
// is the point. A number the spec names that isn't open fails the start.
// Then the hostname.
func initFDs(s *Spec) error {
	for _, fd := range []int{s.CtrlFD, s.AgentFD, s.LockFD} {
		if fd <= 0 {
			continue
		}
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
			return must(err, "inherited fd "+strconv.Itoa(fd))
		}
	}
	for _, fd := range []int{s.CtrlFD, s.AgentFD} {
		if fd > 0 {
			unix.CloseOnExec(fd)
		}
	}
	if s.Hostname != "" {
		if err := unix.Sethostname([]byte(s.Hostname)); err != nil {
			return must(err, "sethostname "+strconv.Quote(s.Hostname))
		}
	}
	return nil
}

// entryArgv is the argv the init execs the entry with. A namespace-mode
// agent learns its factory's and lock's numbers here, never from the
// environment (a VM's shim reads them from its HostSpec).
func entryArgv(s *Spec) []string {
	argv := s.Argv
	if len(argv) == 0 {
		argv = []string{s.Entry}
	}
	if s.AgentFD > 0 && s.VM == nil {
		argv = append(slices.Clip(argv), "--fd", strconv.Itoa(s.AgentFD))
		if s.LockFD > 0 {
			argv = append(argv, "--lock", strconv.Itoa(s.LockFD))
		}
	}
	return argv
}

// handAgentFD clears the factory's close-on-exec immediately before the
// init execs the entry: the entry alone inherits it.
func handAgentFD(s *Spec) error {
	if s.AgentFD <= 0 {
		return nil
	}
	_, err := unix.FcntlInt(uintptr(s.AgentFD), unix.F_SETFD, 0)
	return must(err, "hand the agent its factory")
}

// bindSource is where a bind's source is mounted from, whether it is a
// directory, and what to release once mounted. Without Sub it is Src as
// given (mountBind's path). With Sub, Src is a trusted root opened as given
// and Sub is walked beneath it one component at a time without following a
// symlink (openNoFollow); the bind is made from the opened file through
// /proc/self/fd, never from a path resolved again.
func bindSource(b Bind) (src string, isDir bool, release func(), err error) {
	if b.Sub == "" {
		fi, err := os.Lstat(b.Src)
		if err != nil {
			return "", false, nil, must(err, "bind src "+b.Src)
		}
		return b.Src, fi.IsDir(), func() {}, nil
	}
	fd, err := openSub(b.Src, b.Sub)
	if err != nil {
		return "", false, nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return "", false, nil, must(err, "bind src "+b.Src+" sub "+b.Sub)
	}
	return fdPath(fd), st.Mode&unix.S_IFMT == unix.S_IFDIR, func() { unix.Close(fd) }, nil
}

// openSub opens sub beneath src without following a symlink anywhere in sub.
func openSub(src, sub string) (int, error) {
	if path.IsAbs(sub) || path.Clean(sub) != sub || slices.Contains(strings.Split(sub, "/"), "..") {
		return -1, fmt.Errorf("bind %s sub %q: not a clean relative path", src, sub)
	}
	dir, err := unix.Open(src, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, must(err, "bind src "+src)
	}
	at := src
	for _, c := range strings.Split(sub, "/") {
		at = path.Join(at, c)
		next, err := openNoFollow(dir, c, unix.O_PATH)
		unix.Close(dir)
		switch {
		case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
			return -1, fmt.Errorf("bind sub %s: a symlink is in the way", at)
		case err != nil:
			return -1, must(err, "bind sub "+at)
		}
		dir = next
	}
	return dir, nil
}

// pointAt opens dst's mount point in the new root (rootfd), one component at
// a time without following a symlink (nestedPoint), making what is missing
// when create: directories, and the last an empty file when !isDir.
// Without create a missing component is unix.ENOENT. Walking crosses the
// mounts already made, so a later bind nests in an earlier one.
func pointAt(rootfd int, dst string, isDir, create bool) (int, error) {
	comps := strings.Split(strings.TrimPrefix(path.Clean("/"+dst), "/"), "/")
	if comps[0] == "" {
		return -1, fmt.Errorf("mount point %s: the root itself", dst)
	}
	dir, at := rootfd, ""
	defer func() {
		if dir != rootfd {
			unix.Close(dir)
		}
	}()
	for i, c := range comps {
		at += "/" + c
		last := i == len(comps)-1
		var next int
		var err error
		switch {
		case create:
			next, err = nestedPoint(dir, c, at, isDir || !last)
		default:
			flags := unix.O_PATH
			if isDir || !last {
				flags |= unix.O_DIRECTORY
			}
			next, err = openNoFollow(dir, c, flags)
			switch {
			case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
				err = fmt.Errorf("nested mount point %s: a symlink is in the way", at)
			case errors.Is(err, unix.ENOTDIR):
				err = fmt.Errorf("nested mount point %s: not a directory", at)
			}
		}
		if err != nil {
			return -1, err
		}
		if dir != rootfd {
			unix.Close(dir)
		}
		dir = next
	}
	fd := dir
	dir = rootfd // handed to the caller, not closed by the defer
	return fd, nil
}

// mountAtNoFollow is mountAt for a NoFollow root.
func mountAtNoFollow(newroot, rel, source, fstype string, flags uintptr, data string) error {
	return inRoot(newroot, func(root int) error {
		fd, err := pointAt(root, rel, true, true)
		if err != nil {
			return err
		}
		defer unix.Close(fd)
		return must(unix.Mount(source, fdPath(fd), fstype, flags, data), "mount "+fstype+" at "+rel)
	})
}

// mountBindsNoFollow is the bind loop for a NoFollow root: ancestors first
// (sortBinds), every mount point walked with pointAt, the bind made on the
// opened mount point and its read-only remount on the mount reopened from
// its parent, so no path string is ever resolved again. A read-only bind
// that a later bind nests in is remounted read-only only after everything
// is mounted, since the nested mount point may have to be made in it.
func mountBindsNoFollow(newroot string, binds []Bind, debug bool) error {
	return inRoot(newroot, func(root int) error {
		binds := sortBinds(binds)
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
				fd, err := pointAt(root, dst, true, false)
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
			mp, err := pointAt(root, dst, isDir, true)
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
			top, err := pointAt(root, dst, isDir, false) // lands on top of the new mount
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
	})
}

// oldrootNoFollow makes the pivot's .oldroot in a NoFollow root as a real
// directory, never through a symlink.
func oldrootNoFollow(newroot string) error {
	return inRoot(newroot, func(root int) error {
		fd, err := pointAt(root, "/.oldroot", true, true)
		if err == nil {
			unix.Close(fd)
		}
		return err
	})
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
