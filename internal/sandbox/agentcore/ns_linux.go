//go:build linux

package agentcore

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// RunNamespace is `bx __sbx-agent --fd N [--lock M] [--fuse-pid P]`: PID 1
// of a namespace-mode tile sandbox (plans/tile-sandbox-runtime.md §2.1,
// §2.4). The sandbox's init execs it last, under every guard it applies (the
// entry's Restricted and MountGuard profile, no_new_privs), with agentFD the
// sandbox's end of xbind's connection factory, lockFD (0: none) the state
// lock xbind flocked, and fusePID (0: none) the root's fuse-overlayfs, which
// the init started as its child (sandbox.Spec.FuseWatch). It serves every
// connection xbind dials over the factory until the factory's EOF — xbind is
// gone, or has stopped this sandbox — then flushes the sandbox's filesystem
// and returns 0; PID 1 exiting tears the pid namespace down, sessions and
// fuse-overlayfs with it. When fuse-overlayfs exits first, the root is gone:
// it returns ExitRootGone at once. Sessions run with oom_score_adj 500, so an
// OOM kill takes them before the agent. It returns the exit code (1: a setup
// or transport failure).
//
// Its stdout and stderr are xbind's log for the sandbox; the sessions it
// runs never inherit them, nor either fd, nor its environment.
func RunNamespace(agentFD, lockFD, fusePID int) int {
	if os.Getpid() != 1 {
		nsLogf("must run as PID 1 of a tile sandbox (xbind starts it)")
		return 2
	}
	factory, err := takeFDs(agentFD, lockFD)
	if err != nil {
		nsLogf("%v", err)
		return 1
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		nsLogf("set non-dumpable: %v", err)
		return 1
	}
	if err := dropGroups("/proc/self/setgroups"); err != nil {
		nsLogf("drop the supplementary groups: %v", err)
		return 1
	}
	shrugSignals()

	// the reaper of the whole pid namespace, and of the root's fuse-overlayfs
	spawn := PID1Spawner()
	var rootGone <-chan unix.WaitStatus // nil: nothing to watch
	if fusePID > 1 {
		// registered before any session starts: an exit already reaped is
		// among the statuses the spawner remembers
		rootGone = spawn.Register(fusePID)
	}
	core := New(Options{
		Spawn:              spawn,
		Configure:          nil, // nothing to configure: every ctl gets "ready"
		Sync:               syncRoot,
		Root:               "/",
		SessionOOMScoreAdj: sessionOOMScoreAdj,
		Logf:               nsLogf,
	})
	served := make(chan error, 1)
	go func() {
		served <- core.Serve(func() (io.ReadWriteCloser, error) { return sandbox.AcceptFrom(factory) })
	}()
	select {
	case err = <-served:
	case ws := <-rootGone:
		// Every file access in the sandbox fails with ENOTCONN from here
		// on, and nothing can be flushed: end it, and say why.
		nsLogf("the root filesystem is gone: fuse-overlayfs (pid %d) %s", fusePID, describeExit(ws))
		return ExitRootGone
	}
	exitSync()
	if errors.Is(err, io.EOF) {
		return 0 // xbind closed the factory
	}
	nsLogf("accept: %v", err)
	return 1
}

// setgroups is syscall.Setgroups (all of the process's threads; tests swap
// it).
var setgroups = syscall.Setgroups

// dropGroups clears the agent's supplementary groups — and so every
// session's that runs as the agent (no uid or gid of its own). The agent
// inherits xbind's user's groups; the sandbox's user namespace doesn't map
// them (they read as nogroup inside), yet they still count on the host: a
// host file bound in and owned by one of those groups (docker's, kvm's, a
// shared group's) would open for the sandbox. Where the namespace denies
// setgroups (a single-uid map) they can't be dropped, nor used to widen
// anything the sandbox's one mapped id couldn't reach already: nothing to
// do. A missing file (a kernel before 3.19) allows setgroups.
func dropGroups(setgroupsFile string) error {
	b, err := os.ReadFile(setgroupsFile)
	if err == nil && strings.TrimSpace(string(b)) == "deny" {
		return nil
	}
	return setgroups([]int{})
}

// sessionOOMScoreAdj is every exec's oom_score_adj (§2.4). The agent keeps
// the score it inherited; the sandbox's cgroup leaf keeps memory.oom.group
// 0, so an OOM kill ends a process, not the sandbox.
const sessionOOMScoreAdj = 500

// describeExit says how a process ended.
func describeExit(ws unix.WaitStatus) string {
	if ws.Signaled() {
		return "was killed by " + unix.SignalName(ws.Signal())
	}
	return "exited with code " + strconv.Itoa(ws.ExitStatus())
}

func nsLogf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "sbx-agent: "+format+"\n", args...)
}

// takeFDs moves the factory and the lock to close-on-exec duplicates and
// closes the numbers the init handed over, so no session ever inherits
// either; every other descriptor the agent may have been given is marked
// close-on-exec too. The lock's duplicate is never closed: a flock belongs
// to the open file description, which it keeps for the agent's lifetime.
func takeFDs(agentFD, lockFD int) (*os.File, error) {
	if agentFD < 3 || lockFD == agentFD || lockFD != 0 && lockFD < 3 {
		return nil, fmt.Errorf("bad descriptors: --fd %d --lock %d", agentFD, lockFD)
	}
	fd, err := moveCloexec(agentFD)
	if err != nil {
		return nil, fmt.Errorf("the factory (fd %d): %w", agentFD, err)
	}
	if t, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE); err != nil || t != unix.SOCK_SEQPACKET {
		unix.Close(fd)
		return nil, fmt.Errorf("fd %d is not a connection factory (a SOCK_SEQPACKET socket)", agentFD)
	}
	if lockFD > 0 {
		if _, err := moveCloexec(lockFD); err != nil {
			unix.Close(fd)
			return nil, fmt.Errorf("the lock (fd %d): %w", lockFD, err)
		}
	}
	cloexecRest()
	// non-blocking, so the runtime's poller waits on it, not a thread
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("the factory: %w", err)
	}
	return os.NewFile(uintptr(fd), "sbx-factory"), nil
}

// moveCloexec duplicates fd close-on-exec and closes fd.
func moveCloexec(fd int) (int, error) {
	nfd, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return -1, err
	}
	unix.Close(fd)
	return nfd, nil
}

// cloexecRest marks every descriptor past stdio close-on-exec: a session
// gets its own stdio and nothing else of the agent's.
func cloexecRest() {
	if unix.CloseRange(3, math.MaxUint32, unix.CLOSE_RANGE_CLOEXEC) == nil {
		return
	}
	ents, err := os.ReadDir("/proc/self/fd") // a kernel before 5.11
	if err != nil {
		return
	}
	for _, e := range ents {
		if fd, err := strconv.Atoi(e.Name()); err == nil && fd > 2 {
			unix.CloseOnExec(fd)
		}
	}
}

// shrugSignals keeps the agent alive through the signals its sessions can
// send it: Restricted keeps CAP_KILL (§2.1), and the kernel delivers to a
// pid namespace's init only the signals it has a handler for — which Go
// installs for these, and then dies of (TERM, INT, HUP), dumps and exits on
// (QUIT, ABRT, and SEGV/BUS/FPE/ILL/TRAP/SYS/STKFLT when they come from
// kill rather than a fault). Notify keeps Go's handler and the drain drops
// them. signal.Ignore would be wrong: SIG_IGN survives exec, and every
// session would start with these ignored; a handled signal resets to the
// default in the child. SIGKILL and SIGSTOP, and every signal Go leaves at
// its default, the kernel already drops for PID 1. (A fault signal forged
// with rt_sigqueueinfo still ends the agent, and its sandbox with it: code in
// the sandbox can always stop its own sandbox, and xbind trusts nothing the
// agent does, §2.6.)
func shrugSignals() {
	sigs := make(chan os.Signal, 16)
	signal.Notify(sigs, unix.SIGTERM, unix.SIGINT, unix.SIGHUP, unix.SIGQUIT, unix.SIGUSR1, unix.SIGUSR2,
		unix.SIGABRT, unix.SIGSEGV, unix.SIGBUS, unix.SIGFPE, unix.SIGILL, unix.SIGTRAP, unix.SIGSYS, unix.SIGSTKFLT)
	go func() {
		for range sigs {
		}
	}()
}

// exitSyncWait bounds the flush on the way out.
const exitSyncWait = 5 * time.Second

// exitSync is the flush on the way out, bounded. The root's FUSE server
// (fuse-overlayfs) is a process of the sandbox, which its sessions can stop,
// and a sync waiting on it would keep the agent — and so the whole sandbox
// and its lock — alive after xbind is gone. So every process is continued
// first; and if the flush still hangs past exitSyncWait, the namespace's
// processes are killed, which aborts the FUSE connection and ends the wait.
func exitSync() {
	_ = unix.Kill(-1, unix.SIGCONT) // every process of the pid namespace but this one
	done := make(chan struct{})
	go func() { syncRoot(); close(done) }()
	select {
	case <-done:
		return
	case <-time.After(exitSyncWait):
	}
	nsLogf("the final sync still waits after %s: ending the sandbox's processes", exitSyncWait)
	_ = unix.Kill(-1, unix.SIGKILL)
	select {
	case <-done:
	case <-time.After(time.Second):
	}
}

// syncRoot flushes the filesystem the sandbox's root is on — syncfs, never
// sync(2), which would flush every filesystem of the host.
func syncRoot() {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	_ = unix.Syncfs(fd)
	unix.Close(fd)
}
