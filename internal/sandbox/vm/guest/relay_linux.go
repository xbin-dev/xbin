//go:build linux

package guest

import (
	"fmt"
	"os"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
)

// The FUSE relay runs in its own process, never in the agent.
//
// The agent starts every session with os.StartProcess, which on Linux is a
// vfork: the calling thread sits in a raw clone syscall — holding its
// scheduler P, with signals blocked — until the child execs. A backend's
// program lives on a FUSE mount (/run/backend, or the tile's directory), so
// that execve waits on FUSE requests the relay must carry to the host. Were
// the relay goroutines in the agent's own runtime they could not run:
//
//   - with one vCPU (GOMAXPROCS=1) the forking thread holds the only P, so a
//     relay goroutine whose read of /dev/fuse returns never gets one back;
//   - with any number, a garbage collection whose stop-the-world falls in that
//     window waits for the forking thread's P — which only the exec, i.e. the
//     relay, would release — while every other goroutine, the relay's
//     included, is already stopped.
//
// Either way the backend never execs and never listens (plans/vm-sandbox.md
// §Files). A separate process has its own runtime, and it never forks, so
// nothing the agent does can stop it.

const relayArg = "__xbin-fuse-relay"

// relay is the agent's end of the relay process's control socket: each
// mount's /dev/fuse fd and vsock stream are handed over on it.
type relay struct {
	mu  sync.Mutex
	fd  int
	pid int
}

// startRelay starts the relay process (this binary again, from the
// initramfs: nothing in its exec touches FUSE).
func (a *agent) startRelay() error {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	child := os.NewFile(uintptr(fds[1]), "relay")
	defer child.Close()
	p, done, err := a.spawn.Start("/proc/self/exe", []string{"xbin-vmagent", relayArg}, &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr, child}, // fd 3: the control socket
	})
	if err != nil {
		unix.Close(fds[0])
		return err
	}
	// The kernel's OOM killer must not pick it: every host file would go.
	_ = os.WriteFile(fmt.Sprintf("/proc/%d/oom_score_adj", p.Pid), []byte("-1000"), 0o644)
	a.relay = &relay{fd: fds[0], pid: p.Pid}
	go func() {
		ws := <-done
		logf("the FUSE relay (pid %d) exited: %s — the host's files are unreachable", p.Pid, waitString(ws))
	}()
	return nil
}

// hand passes one mount's /dev/fuse fd and vsock stream to the relay, which
// pumps them from then on; the agent keeps no copy (a relay that died must
// fail the mount's requests, not leave them waiting).
func (r *relay) hand(dev, vs int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := unix.Sendmsg(r.fd, []byte{'m'}, unix.UnixRights(dev, vs), nil, 0)
	unix.Close(dev)
	unix.Close(vs)
	return err
}

// relayMain is the relay process: pump every handed-over mount until the
// agent goes away.
func relayMain() {
	const ctl = 3
	_ = os.WriteFile("/proc/self/comm", []byte("xbin-fuse-relay"), 0) // not "exe" in a dump
	oob := make([]byte, unix.CmsgSpace(2*4))
	buf := make([]byte, 1)
	for {
		n, oobn, _, _, err := unix.Recvmsg(ctl, buf, oob, unix.MSG_CMSG_CLOEXEC)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "xbin-vmagent relay: %v\n", err)
			os.Exit(1)
		}
		if n == 0 && oobn == 0 {
			os.Exit(0) // the agent closed its end
		}
		msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
		if err != nil || len(msgs) != 1 {
			continue
		}
		fds, err := unix.ParseUnixRights(&msgs[0])
		if err != nil || len(fds) != 2 {
			for _, fd := range fds {
				unix.Close(fd)
			}
			continue
		}
		go pumpRequests(fds[0], fds[1])
		go pumpReplies(fds[0], fds[1])
	}
}

// waitString renders an exit status for the console.
func waitString(ws unix.WaitStatus) string {
	switch {
	case ws.Signaled():
		return "killed by " + ws.Signal().String()
	case ws.Exited():
		return "exit status " + strconv.Itoa(ws.ExitStatus())
	}
	return fmt.Sprintf("status %#x", uint32(ws))
}
