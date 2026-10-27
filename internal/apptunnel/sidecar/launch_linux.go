//go:build linux

package sidecar

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// launched is one started process and xbind's ends of its pipes and
// factory.
type launched struct {
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	stdout, stderr io.ReadCloser
	fac            *sandbox.Factory
	h              *sandbox.Handle // the sandbox's, until the process exits
	sandboxed      bool            // run (or tried to run) in the namespace sandbox
	execFailed     bool            // the binary itself couldn't be exec'd
}

// errNoSandbox: isolation is on, and this host can't make the sandbox.
var errNoSandbox = errors.New("app tunnel: isolation is on and this host has no user namespaces for the iroh sidecar's sandbox — it does not run unconfined (D78)")

// launch starts xbin-iroh serve: in a bare, host-network, unprivileged
// namespace sandbox (plans/app-tunnel.md §5.6) where user namespaces work
// — even with isolation off — else, with isolation off only, as a plain
// child. The binary goes in open (Spec.Exe), the factory's child end as
// --fd N.
func (s *Supervisor) launch(x *exe, unconfine bool) (launched, error) {
	l := launched{sandboxed: !unconfine && !s.o.noSandbox && sandbox.Available()}
	if !l.sandboxed && s.o.Isolated {
		return l, errNoSandbox
	}
	fac, child, err := sandbox.NewFactory()
	if err != nil {
		return l, err
	}
	// a copy of the binary's fd per run: Handle.Started closes it, and the
	// extracted binary itself is kept for the restarts
	dfd, err := unix.FcntlInt(x.f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		fac.Close()
		child.Close()
		return l, err
	}
	bin := os.NewFile(uintptr(dfd), "xbin-iroh")
	env := append([]string{"XBIN_IROH_LOG=" + os.Getenv("XBIN_IROH_LOG")}, s.o.extraEnv...)
	var h *sandbox.Handle
	if l.sandboxed {
		l.cmd, h, err = sandbox.Launch(&sandbox.Spec{
			Bare:         true,
			HostNet:      true,
			Unprivileged: true,
			Exe:          bin,
			Agent:        child,
			Argv:         []string{"xbin-iroh", "serve"}, // + --fd N
			Env:          env,
			Hostname:     "xbin-iroh",
			HostUID:      os.Getuid(),
			HostGID:      os.Getgid(),
		})
		if err != nil {
			fac.Close()
			child.Close()
			bin.Close()
			return l, err
		}
	} else {
		// fd 3 is the factory's end, fd 4 the binary
		l.cmd = exec.Command("/proc/self/fd/4", "serve", "--fd", "3") // exec-ok: xbind's own embedded binary; no sandbox exists with isolation off
		l.cmd.Args[0] = "xbin-iroh"
		l.cmd.ExtraFiles = []*os.File{child, bin}
		l.cmd.Env = env
	}
	// our own pipes, so Wait never closes a read end the readers still use
	var ends [3][2]*os.File // stdin, stdout, stderr: [child's, ours]
	closeAll := func() {
		for _, e := range ends {
			for _, f := range e {
				if f != nil {
					f.Close()
				}
			}
		}
	}
	for i := range ends {
		r, w, perr := os.Pipe()
		if perr != nil {
			closeAll()
			h.Cleanup()
			fac.Close()
			child.Close()
			bin.Close()
			return l, perr
		}
		if i == 0 {
			ends[i] = [2]*os.File{r, w}
		} else {
			ends[i] = [2]*os.File{w, r}
		}
	}
	l.cmd.Stdin, l.cmd.Stdout, l.cmd.Stderr = ends[0][0], ends[1][0], ends[2][0]
	if err := l.cmd.Start(); err != nil {
		closeAll()
		h.Cleanup()
		fac.Close()
		child.Close()
		bin.Close()
		l.execFailed = !l.sandboxed
		return l, err
	}
	for _, e := range ends {
		e[0].Close()
	}
	if h != nil {
		h.Started() // closes our copies of child and bin
		if err := h.SetupUserns(); err != nil {
			_ = l.cmd.Process.Kill()
			_ = l.cmd.Wait()
			h.Cleanup()
			closeAll()
			fac.Close()
			return l, err
		}
		l.h = h // cleaned up once the process exits: a range-mode init reads its spec twice
	} else {
		child.Close()
		bin.Close()
	}
	l.stdin, l.stdout, l.stderr, l.fac = ends[0][1], ends[1][1], ends[2][1], fac
	return l, nil
}

// cleanup releases what the sandbox's launch kept (its spec file) once the
// process has exited. Nil-safe.
func (l launched) cleanup() { l.h.Cleanup() }

func signalTerm(c *exec.Cmd) error { return c.Process.Signal(syscall.SIGTERM) }
