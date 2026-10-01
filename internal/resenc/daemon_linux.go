//go:build linux

package resenc

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// daemon is one gocryptfs process serving a cipher directory, held by a
// pidfd (where the kernel has them, 5.3+) so its pid names no other process
// while xbind waits on it or signals it.
type daemon struct {
	pid int
	fd  int // the pidfd; -1 without one
}

// daemonsOn finds the gocryptfs processes serving cipher (absolute, clean):
// this uid's processes whose command line is a serving gocryptfs's — "-fg",
// which the daemonized child of every mount carries, and cipher as its
// cipher directory, the second-to-last argument (the mountpoint is the
// last). It reads /proc: nothing found where it can't. Close them with
// closeDaemons.
func daemonsOn(cipher string) []daemon {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	uid := uint32(os.Getuid())
	var out []daemon
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 || pid == os.Getpid() || !servesCipher(pid, cipher) {
			continue
		}
		if fi, err := os.Stat(filepath.Join("/proc", e.Name())); err != nil || fi.Sys().(*syscall.Stat_t).Uid != uid {
			continue
		}
		fd, err := unix.PidfdOpen(pid, 0)
		switch {
		case errors.Is(err, unix.ESRCH):
			continue // exited meanwhile
		case err != nil:
			fd = -1
		}
		if !servesCipher(pid, cipher) { // the pid named another process by the time the pidfd opened
			if fd >= 0 {
				_ = unix.Close(fd)
			}
			continue
		}
		out = append(out, daemon{pid: pid, fd: fd})
	}
	return out
}

// servesCipher reports whether process pid's command line is a serving
// gocryptfs's on cipher (daemonsOn).
func servesCipher(pid int, cipher string) bool {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil || len(b) == 0 {
		return false
	}
	args := bytes.Split(bytes.TrimSuffix(b, []byte{0}), []byte{0})
	if len(args) < 4 || filepath.Clean(string(args[len(args)-2])) != cipher {
		return false
	}
	for _, a := range args[1 : len(args)-2] {
		if string(a) == "-fg" {
			return true
		}
	}
	return false
}

// wait reports whether d has exited, waiting for it at most within: on a
// pidfd, poll wakes at the exit itself.
func (d daemon) wait(within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		left := time.Until(deadline)
		if d.fd >= 0 {
			ms := int(left / time.Millisecond)
			if ms < 0 {
				ms = 0
			}
			n, err := unix.Poll([]unix.PollFd{{Fd: int32(d.fd), Events: unix.POLLIN}}, ms)
			switch {
			case n > 0:
				return true
			case errors.Is(err, unix.EINTR) && left > 0:
				continue
			default:
				return false
			}
		}
		// no pidfd (an old kernel): its pid until it's gone, or names
		// another process — then it is gone too
		if err := unix.Kill(d.pid, 0); errors.Is(err, unix.ESRCH) {
			return true
		}
		if left <= 0 {
			return false
		}
		time.Sleep(min(left, 10*time.Millisecond))
	}
}

// signal sends sig to d.
func (d daemon) signal(sig syscall.Signal) {
	if d.fd >= 0 {
		_ = unix.PidfdSendSignal(d.fd, sig, nil, 0)
		return
	}
	_ = unix.Kill(d.pid, sig)
}

// closeDaemons releases ds's pidfds.
func closeDaemons(ds []daemon) {
	for _, d := range ds {
		if d.fd >= 0 {
			_ = unix.Close(d.fd)
		}
	}
}
