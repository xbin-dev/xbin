package tilesbx

// launch.go — how a tile sandbox's first process comes to be
// (plans/tile-sandbox-runtime.md §7 step 5, §5): the Spec a definition
// becomes, the binds its mounts become, and the Launcher that starts it —
// sandbox.Launch and exec.Cmd.Start in production, a fake in the unit tests.
// Namespace mode is built here; VM mode (vm.Apply, Resident) is vm.go's,
// through modeOps.

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/layers"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// Inside a namespace sandbox.
const (
	agentPath   = "/opt/xbin/bin/bx" // the static bx, bound read-only: the agent (`bx __sbx-agent`)
	defaultPATH = sandbox.RootfsPATH // the rootfs toolchains first, as terminals and backends have it (D134)
)

// Launcher starts a sandbox's first process from its Spec.
type Launcher interface {
	// Start launches spec and starts it. On an error nothing was started,
	// and spec.Agent and spec.Lock are still the caller's to close (WP-2).
	Start(spec *sandbox.Spec, o LaunchOpts) (Proc, error)
}

// LaunchOpts are the parts of a start that aren't the Spec's.
type LaunchOpts struct {
	Log      io.Writer // the process's stdout and stderr: the sandbox's log ring
	CgroupFD *os.File  // start straight into this cgroup leaf (nil: none)
}

// ExitStatus is how a sandbox's first process ended.
type ExitStatus struct {
	Code   int            // its exit code; -1 when a signal ended it
	Signal syscall.Signal // > 0 when a signal ended it
	Err    error          // Wait failed for another reason
}

// Proc is a started sandbox's first process (the init, which becomes the
// agent — or, in VM mode, the shim).
type Proc interface {
	Pid() int
	// InCgroup reports whether the start placed it in LaunchOpts.CgroupFD's
	// leaf; false after the ENOSYS/EINVAL fallback, when the caller joins
	// it with AddWith.
	InCgroup() bool
	// SetupUserns writes a range-mode sandbox's uid maps and releases its
	// init (sandbox.Handle.SetupUserns).
	SetupUserns() error
	// RecvTUN is the egress TUN the init hands back (-1 when the spec asked
	// for none). The fd is the caller's.
	RecvTUN() (int, error)
	// Started closes xbind's copies of the files the sandbox inherited —
	// the factory's child end and the lock among them (Handle.Started).
	Started()
	// Wait waits for the process to end (called once, by the watcher).
	Wait() ExitStatus
	// Kill SIGKILLs it and every process under it; Signal sends another
	// signal (a VM's shim: SIGHUP).
	Kill() error
	Signal(os.Signal) error
	// Cleanup releases what the launch left on the host (the spec file).
	Cleanup()
}

// nsLauncher is the production Launcher: sandbox.Launch, started into its
// leaf where the kernel can (clone3), else started, then joined.
type nsLauncher struct{}

func (nsLauncher) Start(spec *sandbox.Spec, o LaunchOpts) (Proc, error) {
	start := func(fd *os.File) (*exec.Cmd, *sandbox.Handle, error) {
		cmd, h, err := sandbox.Launch(spec)
		if err != nil {
			return nil, nil, err
		}
		cmd.Stdout, cmd.Stderr = o.Log, o.Log
		cmd.WaitDelay = 2 * time.Second // a log pipe some process still holds never wedges Wait
		if fd != nil {
			useCgroupFD(cmd, fd)
		}
		if err := cmd.Start(); err != nil {
			h.Cleanup()
			return nil, nil, err
		}
		return cmd, h, nil
	}
	cmd, h, err := start(o.CgroupFD)
	inCg := o.CgroupFD != nil
	if err != nil && inCg && cgroupFDUnsupported(err) {
		// A kernel before 5.7: start it, then the caller joins it (AddWith)
		// before its uid maps release the init (§6.2).
		cmd, h, err = start(nil)
		inCg = false
	}
	if err != nil {
		return nil, err
	}
	return &nsProc{cmd: cmd, h: h, inCg: inCg}, nil
}

type nsProc struct {
	cmd  *exec.Cmd
	h    *sandbox.Handle
	inCg bool

	// hmu serializes SetupUserns and Cleanup: an init that dies at once is
	// torn down (Cleanup, by the watcher) while its start may still be in
	// SetupUserns, and both touch the handle's sync pipe.
	hmu sync.Mutex

	mu     sync.Mutex
	waited bool // Wait returned: the init is reaped, its pid may be anyone's
}

func (p *nsProc) Pid() int       { return p.cmd.Process.Pid }
func (p *nsProc) InCgroup() bool { return p.inCg }
func (p *nsProc) Started()       { p.h.Started() }

func (p *nsProc) SetupUserns() error {
	p.hmu.Lock()
	defer p.hmu.Unlock()
	return p.h.SetupUserns()
}

func (p *nsProc) Cleanup() {
	p.hmu.Lock()
	defer p.hmu.Unlock()
	p.h.Cleanup()
}

// Kill SIGKILLs the init and every process under it: its PID 1 can't die
// while one of its threads waits on a wedged FUSE root (killtree.go). The
// tree is walked only while the init is still unreaped — Signal(0) goes
// through its pidfd, and Wait marks it — so its pid names no other process.
func (p *nsProc) Kill() error {
	err := p.cmd.Process.Kill()
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.waited && p.cmd.Process.Signal(syscall.Signal(0)) == nil {
		killTree(p.cmd.Process.Pid)
	}
	return err
}

func (p *nsProc) Signal(s os.Signal) error { return p.cmd.Process.Signal(s) }

func (p *nsProc) RecvTUN() (int, error) {
	if !p.h.NeedsRelay() {
		return -1, nil
	}
	return p.h.RecvTUN()
}

func (p *nsProc) Wait() ExitStatus {
	err := p.cmd.Wait()
	p.mu.Lock()
	p.waited = true
	p.mu.Unlock()
	ps := p.cmd.ProcessState
	if ps == nil {
		return ExitStatus{Code: -1, Err: err}
	}
	st := ExitStatus{Code: ps.ExitCode()}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		st.Code, st.Signal = -1, ws.Signal()
	}
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		st.Err = err // WaitDelay, a copy error: the status still stands
	}
	return st
}

// overlayFlavour is the overlay a namespace sandbox's root is mounted with
// here — what its upper's flavour stamp must say (§1.3).
func overlayFlavour() string {
	if sandbox.FuseOverlayfs() != "" {
		return layers.OverlayFuse
	}
	return layers.OverlayKernel
}

// nsSpec is a namespace sandbox's Spec (§7 step 5): the pinned base under
// cur/'s upper, the lockdown of a restricted terminal (Restricted,
// MountGuard, NO_NEW_PRIVS) with mount points never followed (NoFollow)
// and fuse-overlayfs watched (FuseWatch), its own hostname, the relay, the
// factory and the lock, the agent bound read-only as the entry. Its
// environment is the agent's own; a session never inherits it — each exec
// carries its own environment (sessionEnv, exec.go).
func (m *Manager) nsSpec(d *Def, lower, cur string, binds []sandbox.Bind, agent, lock *os.File) *sandbox.Spec {
	return &sandbox.Spec{
		Lower: []string{lower},
		Upper: filepath.Join(cur, "upper"),
		Work:  filepath.Join(cur, "work"),
		Binds: append([]sandbox.Bind{{Src: m.bxPath, Dst: agentPath, RO: true}}, binds...),
		Entry: agentPath, Argv: []string{"bx", "__sbx-agent"},
		Env:      []string{"PATH=" + defaultPATH, "IN_SANDBOX=1"},
		Hostname: d.Name,
		HostUID:  os.Getuid(), HostGID: os.Getgid(),
		Net:        "relay",
		Restricted: true, MountGuard: true, NoFollow: true, FuseWatch: true,
		Agent: agent, Lock: lock,
	}
}

// binds resolves a definition's mounts against the tile's reach now (§5):
// a res mount must still be a filesystem resource the tile holds, its view
// up (the vault unsealed); a reader's mount is read-only whatever the
// definition says; {source:true} is the tile's code, read-only. The
// sub-path is resolved by the init beneath the resource's root without
// following a symlink (Bind.Sub), and every mount point is made without
// following one (Spec.NoFollow).
func (m *Manager) binds(k Key, d *Def) ([]sandbox.Bind, error) {
	var out []sandbox.Bind
	for _, mt := range d.Mounts {
		if mt.Source {
			root, err := m.CodeRoot(k)
			if err != nil {
				return nil, err
			}
			out = append(out, sandbox.Bind{Src: root, Dst: mt.At, RO: true})
			continue
		}
		ms, err := m.ResourceMount(k, mt.Res)
		switch {
		case err != nil:
			return nil, refuse(RefInvalid, "the mount at %s: %v", mt.At, err)
		case ms.Kind != "filesystem":
			return nil, refuse(RefInvalid, "the mount at %s: %s is a %s resource: only filesystem resources mount", mt.At, mt.Res, ms.Kind)
		case ms.Encrypted && !ms.Ready:
			if m.deps.Vault != nil && m.deps.Vault.Sealed() {
				return nil, &Error{Refusal: RefUnavailable, Msg: "the vault is sealed: " + mt.Res + " can't be mounted until it is unsealed", RetryAfter: 30 * time.Second}
			}
			return nil, &Error{Refusal: RefUnavailable, Msg: mt.Res + " isn't mounted yet", RetryAfter: 5 * time.Second}
		}
		ro := mt.RO || (ms.Role != "writer" && ms.Role != "admin")
		out = append(out, sandbox.Bind{Src: ms.Src, Sub: mt.Path, Dst: mt.At, RO: ro})
	}
	return out, nil
}

// specInput is what a mode's spec builder gets from the start.
type specInput struct {
	Lower, Cur  string         // the pinned base's rootfs dir; the sandbox's cur/
	Binds       []sandbox.Bind // its mounts, resolved (binds)
	Agent, Lock *os.File       // the factory's child end; the flocked state lock
}

// modeOps is what differs between the modes at a start and a stop.
// Namespace mode's are nsOps; VM mode's vmOps (vm.go).
type modeOps struct {
	// check refuses a start the mode can't run now (nil: none): before the
	// book, and again once the run is up — a switch turned off meanwhile
	// never leaves one running.
	check func(m *Manager) error
	// spec builds the sandbox's Spec. What it takes that must be given back
	// (a VM reservation) its undo releases — on a failed start before the
	// process runs, else at the run's teardown. undo may be nil.
	spec func(m *Manager, k Key, d *Def, in specInput) (spec *sandbox.Spec, undo func(), err error)
	// leaf is the sandbox's cgroup leaf limits (§6.2's table); accel is
	// the run's (specAccel: "" in namespace mode).
	leaf func(d *Def, lim Limits, accel string) cgroup.Limits
	// readyWait bounds the wait for the agent's "ready".
	readyWait func(r *run) time.Duration
	// stop ends a run a stop asked to end, and asks it to end
	// (m.end) — nil: syncThenEnd, a sync then SIGKILL.
	stop func(m *Manager, r *run, why string)
	// exitReason is why a run of this mode ended on its own, given its
	// leaf's OOM kills ("": the common reasons, exitReason); quoted: it
	// quotes the run's log already, so a start it failed adds no line of it.
	exitReason func(r *run, oom int64) (why string, quoted bool)
}

// nsOps is namespace mode.
var nsOps = &modeOps{
	spec: func(m *Manager, _ Key, d *Def, in specInput) (*sandbox.Spec, func(), error) {
		if m.bxPath == "" {
			return nil, nil, refuse(RefUnavailable, "tile sandboxes need the bx binary xbind couldn't find at startup (XBIN_BIN)")
		}
		return m.nsSpec(d, in.Lower, in.Cur, in.Binds, in.Agent, in.Lock), nil, nil
	},
	leaf:      func(d *Def, lim Limits, _ string) cgroup.Limits { return leafLimits(d, lim) },
	readyWait: func(*run) time.Duration { return nsReadyWait },
}

// pinLayer is layers.Pin (a test may stand in for it).
var pinLayer = layers.Pin
