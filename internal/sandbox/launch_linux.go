//go:build linux

package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// InitArg is the hidden subcommand buxond (and the test binary) must dispatch
// to RunInit *before* flag parsing — it is the re-exec target that runs inside
// the new namespaces.
const InitArg = "__sandbox-init"

// Available reports whether unprivileged user namespaces are usable here.
func Available() bool {
	if b, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		return len(b) > 0 && b[0] == '1'
	}
	// The sysctl is absent on kernels that always allow it; assume yes on Linux.
	return true
}

// RecvTUN blocks until the init sends the TUN fd (via SCM_RIGHTS) and returns it.
func (h *Handle) RecvTUN() (int, error) { return recvFD(h.ctrl) }

// Launch builds (but does not start) an *exec.Cmd that, when started, re-execs
// this binary as PID 1 of a fresh user+mount+pid+net+ipc+uts namespace set,
// where the init reads the spec and execs the backend. The caller keeps its own
// Stdout/Stderr/Start/Wait wiring; after Start, call h.RecvTUN if h.NeedsRelay.
func Launch(s *Spec) (*exec.Cmd, *Handle, error) {
	f, err := os.CreateTemp("", "bx-spec-*.json")
	if err != nil {
		return nil, nil, err
	}
	if err := json.NewEncoder(f).Encode(s); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, nil, err
	}
	f.Close()

	clone := uintptr(unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWPID |
		unix.CLONE_NEWIPC | unix.CLONE_NEWUTS)
	if !s.HostNet {
		clone |= unix.CLONE_NEWNET // components get their own (default-deny) netns
	}
	cmd := exec.Command("/proc/self/exe", InitArg, f.Name())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 clone,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: s.HostUID, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: s.HostGID, Size: 1}},
		GidMappingsEnableSetgroups: false, // rootless: setgroups=deny
	}
	h := &Handle{cleanup: func() { os.Remove(f.Name()) }}
	if s.Net == "relay" {
		parent, child, err := socketpair()
		if err != nil {
			os.Remove(f.Name())
			return nil, nil, err
		}
		cmd.ExtraFiles = []*os.File{child} // becomes fd 3 in the init
		h.ctrl = parent
		// child is kept open in the parent until Start dups it; harmless to
		// leave — Cleanup closes parent, and child GC-closes.
	}
	return cmd, h, nil
}

func socketpair() (parent, child *os.File, err error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(fds[0]), "ctrl-parent"), os.NewFile(uintptr(fds[1]), "ctrl-child"), nil
}

// recvFD receives a single fd sent with SCM_RIGHTS over a unix socket.
func recvFD(f *os.File) (int, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return -1, err
	}
	var fd int
	var opErr error
	err = rc.Read(func(sfd uintptr) bool {
		buf := make([]byte, 16)
		oob := make([]byte, unix.CmsgSpace(4))
		_, oobn, _, _, e := unix.Recvmsg(int(sfd), buf, oob, 0)
		if e == unix.EAGAIN {
			return false // wait for readability
		}
		if e != nil {
			opErr = e
			return true
		}
		scms, e := unix.ParseSocketControlMessage(oob[:oobn])
		if e != nil || len(scms) == 0 {
			opErr = fmt.Errorf("no control message")
			return true
		}
		fds, e := unix.ParseUnixRights(&scms[0])
		if e != nil || len(fds) == 0 {
			opErr = fmt.Errorf("no fd in control message")
			return true
		}
		fd = fds[0]
		return true
	})
	if err != nil {
		return -1, err
	}
	return fd, opErr
}

func must(err error, what string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}
