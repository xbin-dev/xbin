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
// hostname and Bind.Sub. Mount points that are never followed (NoFollow)
// are mountpoint_linux.go.

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
// environment (a VM's shim reads them from its HostSpec), and the pid of
// the fuse-overlayfs it watches (fusePID > 0: FuseWatch).
func entryArgv(s *Spec, fusePID int) []string {
	argv := s.Argv
	if len(argv) == 0 {
		argv = []string{s.Entry}
	}
	if s.AgentFD > 0 && s.VM == nil {
		argv = append(slices.Clip(argv), "--fd", strconv.Itoa(s.AgentFD))
		if s.LockFD > 0 {
			argv = append(argv, "--lock", strconv.Itoa(s.LockFD))
		}
		if fusePID > 0 {
			argv = append(argv, "--fuse-pid", strconv.Itoa(fusePID))
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

// SubMode is the type and permission bits of what b's source names —
// Src, or Sub beneath it resolved as the init binds it (openSub: no
// symlink followed anywhere in Sub) — so a VM exports a file bind as a
// file and a directory bind as a directory.
func SubMode(b Bind) (os.FileMode, error) {
	if b.Sub == "" {
		fi, err := os.Stat(b.Src)
		if err != nil {
			return 0, err
		}
		return fi.Mode(), nil
	}
	fd, err := openSub(b.Src, b.Sub)
	if err != nil {
		return 0, err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return 0, must(err, "bind src "+b.Src+" sub "+b.Sub)
	}
	mode := os.FileMode(st.Mode & 0o777)
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		mode |= os.ModeDir
	case unix.S_IFSOCK:
		mode |= os.ModeSocket
	case unix.S_IFCHR:
		mode |= os.ModeDevice | os.ModeCharDevice
	case unix.S_IFBLK:
		mode |= os.ModeDevice
	case unix.S_IFIFO:
		mode |= os.ModeNamedPipe
	}
	return mode, nil
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
