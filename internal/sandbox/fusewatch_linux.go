//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// A watched fuse-overlayfs (Spec.FuseWatch, plans/tile-sandbox-runtime.md
// §2.4): the root's FUSE server is a child of the init — and so of the agent
// the init execs, which keeps its pid — rather than a daemon that forked
// away, so the agent can end the sandbox when it dies.

// fuseMagic is FUSE_SUPER_MAGIC, statfs's f_type on a FUSE mount.
const fuseMagic = 0x65735546

// fuseMountWait bounds the wait for fuse-overlayfs to mount the root.
const fuseMountWait = 10 * time.Second

// fuseOutputMax bounds how much of fuse-overlayfs's output an error quotes.
const fuseOutputMax = 4 << 10

// mountFuseWatched starts fuse-overlayfs in the foreground on newroot and
// polls until newroot is a FUSE mount, returning its pid. Its stdout and
// stderr go to a memfd: quoted when it exits before the mount is up, or is
// still without one after fuseMountWait (it is killed then), and traced
// under Debug on success. The init never reaps it: its exit belongs to the
// agent, whose reaper collects it even when it happens between the last
// look here and the exec (waitid WNOWAIT only peeks).
func mountFuseWatched(s *Spec, opt, newroot string) (int, error) {
	mfd, err := unix.MemfdCreate("fuse-overlayfs", unix.MFD_CLOEXEC)
	if err != nil {
		return 0, must(err, "fuse-overlayfs output")
	}
	out := os.NewFile(uintptr(mfd), "fuse-overlayfs-output")
	defer out.Close() // fuse-overlayfs holds its own copies
	fo := exec.Command(s.FuseOverlay, "-f", "-o", opt, newroot)
	fo.Stdout, fo.Stderr = out, out
	// Where a daemonizing fuse-overlayfs puts itself: in / — pivot_root then
	// moves it into the new root, leaving it no host directory as its cwd —
	// and in a session of its own, out of reach of a signal to xbind's
	// process group (a ^C in the terminal xbind runs in).
	fo.Dir = "/"
	fo.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := fo.Start(); err != nil {
		return 0, must(err, "start fuse-overlayfs")
	}
	pid := fo.Process.Pid
	// A server that mounted but never answers would hang statfs: killing it
	// aborts the FUSE connection, which ends that wait too.
	late := make(chan struct{})
	watchdog := time.AfterFunc(fuseMountWait, func() { close(late); _ = fo.Process.Kill() })
	timedOut := func() error {
		return fmt.Errorf("fuse-overlayfs mount: no FUSE mount after %s: %s", fuseMountWait, fuseOutput(out))
	}
	for {
		if childExited(pid) {
			select {
			case <-late:
				return 0, timedOut()
			default:
			}
			watchdog.Stop()
			werr := fo.Wait()
			return 0, fmt.Errorf("fuse-overlayfs mount: it exited before mounting the root (%v): %s", werr, fuseOutput(out))
		}
		var st unix.Statfs_t
		if unix.Statfs(newroot, &st) == nil && st.Type == fuseMagic {
			if !watchdog.Stop() {
				return 0, timedOut() // killed as it came up
			}
			dbg(s.Debug, "fuse-overlayfs pid %d serves the root (said %q)", pid, fuseOutput(out))
			return pid, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// childExited reports whether the child pid has exited, without reaping it.
func childExited(pid int) bool {
	var info unix.Siginfo // si_signo stays 0 while there is nothing to wait for
	err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
	return err == nil && info.Signo != 0 || errors.Is(err, unix.ECHILD)
}

// fuseOutput is the tail of what fuse-overlayfs wrote to f.
func fuseOutput(f *os.File) string {
	fi, err := f.Stat()
	if err != nil {
		return "(its output is unreadable: " + err.Error() + ")"
	}
	off := max(fi.Size()-fuseOutputMax, 0)
	b := make([]byte, fi.Size()-off)
	n, _ := f.ReadAt(b, off)
	if s := strings.TrimSpace(string(b[:n])); s != "" {
		return s
	}
	return "(no output)"
}
