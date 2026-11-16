package confine

// start.go — an interactive confined run: a tool that talks to a person
// through xbind (a coding agent CLI's own sign-in, D230) rather than
// printing once and exiting. It is Run's sandbox — the base rootfs, only
// the binds asked for, no capabilities, the network asked for — on a
// terminal of its own, which the caller reads and types into while the
// tool runs. The same rule holds: with isolation on a sandbox that will not
// start is an error, never a run on the host.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/relay"
)

// Proc is an interactive confined run under way. Read is what it printed on
// its terminal (io.EOF once it ended and all was read), Write types into
// it; Kill ends it. Done is closed once it ended and its sandbox is gone;
// ExitCode is then its code (-1: killed or unknown).
type Proc struct {
	tty  *os.File
	done chan struct{}
	kill func()

	mu   sync.Mutex
	code int
}

func (p *Proc) Read(b []byte) (int, error)  { return p.tty.Read(b) }
func (p *Proc) Write(b []byte) (int, error) { return p.tty.Write(b) }

// Done is closed when the run ended.
func (p *Proc) Done() <-chan struct{} { return p.done }

// ExitCode is the run's exit code once Done is closed.
func (p *Proc) ExitCode() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code
}

// Kill ends the run and everything it started. Safe to call again.
func (p *Proc) Kill() { p.kill() }

// StartTTY starts c on a terminal cols×rows wide and returns at once. The
// run ends on its own, at c.Timeout (0 = 2 minutes), or at Kill. c.Stdin and
// c.MaxOutput are not used: the terminal is both, and the caller bounds
// what it keeps.
func StartTTY(c Cmd, cols, rows int) (*Proc, error) {
	if len(c.Argv) == 0 {
		return nil, errors.New("confine: empty argv")
	}
	if c.DirFrom != "" && c.Dir == "" {
		return nil, errors.New("confine: DirFrom without a Dir to show it at")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	mu.RLock()
	fs := rootfs
	mu.RUnlock()
	if fs == "" {
		if err := c.directOK(); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	var cmd *exec.Cmd
	var h *sandbox.Handle
	if fs == "" {
		cmd = exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...) // exec-ok: isolation off — no sandbox exists, tiles already run as xbind
		cmd.Dir = c.Dir
		cmd.Env = append(hostEnv(), c.Env...)
	} else {
		spec := &sandbox.Spec{
			Lower:        []string{fs},
			Binds:        c.binds(),
			Entry:        "/usr/bin/env",
			Argv:         append([]string{"env"}, c.Argv...),
			Env:          append([]string{"PATH=" + sandboxPATH, "HOME=/tmp", "LANG=C.UTF-8", "TERM=xterm-256color"}, c.Env...),
			Cwd:          c.Dir,
			HostUID:      os.Getuid(),
			HostGID:      os.Getgid(),
			Unprivileged: true,
			FileCaps:     c.FSCaps,
			TmpfsBytes:   c.TmpfsBytes,
		}
		switch c.Net {
		case NetHost:
			spec.HostNet = true
		case NetInternet:
			spec.Net = "relay"
		}
		var err error
		cmd, h, err = sandbox.Launch(spec)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	}
	fail := func(err error) (*Proc, error) {
		cancel()
		if h != nil {
			h.Cleanup()
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return nil, err
	}
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return fail(err)
	}
	var once sync.Once
	direct := h == nil
	kill := func() {
		once.Do(func() {
			if direct { // the tool leads its own session (the terminal's): its whole group goes
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			_ = cmd.Process.Kill()
		})
	}
	var rl *relay.Relay
	if h != nil {
		// the sandbox's init is PID 1 of its namespace: killing it takes
		// everything inside with it
		abort := func(err error) (*Proc, error) {
			kill()
			_ = cmd.Wait()
			_ = tty.Close()
			return fail(err)
		}
		if err := h.SetupUserns(); err != nil {
			return abort(fmt.Errorf("userns: %v", err))
		}
		if h.NeedsRelay() {
			fd, err := h.RecvTUN()
			if err != nil {
				return abort(fmt.Errorf("egress: %v", err))
			}
			pol, _ := sandbox.Parse([]string{"net:internet"})
			if rl, err = relay.Start(relay.Config{TunFD: fd, CloseTUN: true, Allow: pol.Allow, Resolver: sandbox.HostResolver()}); err != nil {
				return abort(fmt.Errorf("relay: %v", err))
			}
		}
	}
	p := &Proc{tty: tty, done: make(chan struct{}), kill: kill, code: -1}
	stop := context.AfterFunc(ctx, kill) // the deadline
	go func() {
		err := cmd.Wait()
		stop()
		cancel()
		code := -1
		var ee *exec.ExitError
		switch {
		case err == nil:
			code = 0
		case errors.As(err, &ee) && ee.Exited():
			code = ee.ExitCode()
		}
		p.mu.Lock()
		p.code = code
		p.mu.Unlock()
		if rl != nil {
			rl.Close()
		}
		if h != nil {
			h.Cleanup()
		}
		close(p.done)
	}()
	return p, nil
}

// Close releases the terminal once the run is over (or was killed): a
// reader blocked in Read returns.
func (p *Proc) Close() error {
	p.kill()
	return p.tty.Close()
}
