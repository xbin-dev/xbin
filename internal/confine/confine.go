// Package confine runs the tools xbind needs on workspace data — git, the Go
// toolchain — inside a throwaway sandbox, never with xbind's own privileges
// (D78, docs/isolation.md §Confined tool runs).
//
// Why: a tile directory, and a user's home, are written from inside sandboxes
// (terminals, coding agents, backends' setup). A tool xbind runs there reads
// that content as configuration — git runs whatever a repo's .git/config
// names as core.fsmonitor or a filter, `go build` runs git for VCS stamping —
// so a host-side run turns "write access to my tile" into "code execution as
// xbind": every tile's vault, every user's data. Here the tool runs in a
// fresh sandbox over the base rootfs with only the directories it needs
// bound in, no capabilities, and no network unless asked; whatever the
// content makes it do stays inside.
//
// Configure (at boot, when isolation is on) turns it on. A workspace running
// without isolation has no sandbox at all — its terminals and backends
// already run as the xbind user — so there Run executes the tool directly,
// with the same hardened environment. With isolation on, a sandbox that will
// not start is an error: Run never falls back to the host.
//
// The rule this package exists for is enforced by TestNoDirectExec: daemon
// code may not exec a program except through here, or at a call site that
// says why it is safe (`// exec-ok: <reason>`).
package confine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/relay"
)

var (
	mu     sync.RWMutex
	rootfs string // "" = isolation off: tools run directly
)

// Configure turns confinement on: tools run in sandboxes over this rootfs
// (an unpacked base image with git; the same one terminals use).
func Configure(rootfsDir string) {
	mu.Lock()
	rootfs = rootfsDir
	mu.Unlock()
}

// Isolated reports whether tools run sandboxed (isolation is on).
func Isolated() bool {
	mu.RLock()
	defer mu.RUnlock()
	return rootfs != ""
}

// Net is a confined run's network.
type Net int

const (
	NetNone     Net = iota // an empty network namespace (the default)
	NetInternet            // egress to public addresses through the relay (module downloads)
	NetHost                // the host's network (a fetch an operator asked for: import from a URL)
)

// Cmd is one confined run. Paths are host paths; every bind lands at the
// same path inside, so tools print paths the caller understands.
type Cmd struct {
	Argv  []string       // Argv[0] is looked up in PATH (the sandbox's, or the host's when direct)
	Dir   string         // working directory; bound read-write unless ReadOnlyDir
	Binds []sandbox.Bind // more mounts (read-only, masks, …); Dir's bind is added for you
	Env   []string       // on top of a minimal PATH/HOME/LANG (direct: on top of xbind's env minus GIT_*)
	Net   Net
	Stdin io.Reader

	ReadOnlyDir bool          // bind Dir read-only
	Timeout     time.Duration // 0 = 2 minutes
	MaxOutput   int           // per stream; 0 = 64 MiB (the rest is dropped)
}

// Result is what a run printed.
type Result struct {
	Stdout, Stderr []byte
}

// ExitError is a run that exited non-zero (Code) — most tools' "no".
type ExitError struct {
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	if e.Stderr != "" {
		return e.Stderr
	}
	return fmt.Sprintf("exit status %d", e.Code)
}

// ExitCode reports a run's non-zero exit code.
func ExitCode(err error) (int, bool) {
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code, true
	}
	return 0, false
}

// ErrUnavailable: isolation is on but the sandbox could not be set up.
var ErrUnavailable = errors.New("confined run: the sandbox could not start")

const sandboxPATH = "/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// Run executes c: in a sandbox when isolation is on, else directly.
func Run(ctx context.Context, c Cmd) (Result, error) {
	if len(c.Argv) == 0 {
		return Result{}, errors.New("confine: empty argv")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	limit := c.MaxOutput
	if limit <= 0 {
		limit = 64 << 20
	}
	out, errb := &capped{max: limit}, &capped{max: limit}

	mu.RLock()
	fs := rootfs
	mu.RUnlock()
	var cmd *exec.Cmd
	var h *sandbox.Handle
	if fs == "" {
		cmd = exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...) // exec-ok: isolation off — no sandbox exists, tiles already run as xbind
		cmd.Dir = c.Dir
		cmd.Env = append(hostEnv(), c.Env...)
	} else {
		spec := &sandbox.Spec{
			Lower: []string{fs},
			Binds: c.binds(),
			// env resolves Argv[0] in the sandbox's PATH, so callers name
			// tools ("git") rather than guess where the rootfs keeps them
			Entry:        "/usr/bin/env",
			Argv:         append([]string{"env"}, c.Argv...),
			Env:          append([]string{"PATH=" + sandboxPATH, "HOME=/tmp", "LANG=C.UTF-8"}, c.Env...),
			Cwd:          c.Dir,
			HostUID:      os.Getuid(),
			HostGID:      os.Getgid(),
			Unprivileged: true, // no capabilities, the syscall block-list: nothing to un-mask with
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
			return Result{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		defer h.Cleanup()
	}
	cmd.Stdin = c.Stdin
	cmd.Stdout, cmd.Stderr = out, errb
	if err := cmd.Start(); err != nil {
		if h != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return Result{}, err
	}
	if h != nil {
		// the sandbox's init is PID 1 of its namespace: killing it on the
		// deadline takes everything inside with it
		stop := context.AfterFunc(ctx, func() { _ = cmd.Process.Kill() })
		defer stop()
		if err := h.SetupUserns(); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return Result{}, fmt.Errorf("%w: userns: %v", ErrUnavailable, err)
		}
		if h.NeedsRelay() {
			fd, err := h.RecvTUN()
			if err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return Result{}, fmt.Errorf("%w: egress: %v", ErrUnavailable, err)
			}
			pol, _ := sandbox.Parse([]string{"net:internet"})
			rl, err := relay.Start(relay.Config{TunFD: fd, Allow: pol.Allow, Resolver: sandbox.HostResolver()})
			if err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return Result{}, fmt.Errorf("%w: relay: %v", ErrUnavailable, err)
			}
			defer rl.Close()
		}
	}
	err := cmd.Wait()
	res := Result{Stdout: out.Bytes(), Stderr: errb.Bytes()}
	if ctx.Err() != nil {
		return res, fmt.Errorf("%s: timed out after %s", c.Argv[0], timeout)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return res, &ExitError{Code: ee.ExitCode(), Stderr: strings.TrimSpace(string(res.Stderr))}
	}
	return res, err
}

func (c Cmd) binds() []sandbox.Bind {
	var bs []sandbox.Bind
	if c.Dir != "" {
		bs = append(bs, sandbox.Bind{Src: c.Dir, Dst: c.Dir, RO: c.ReadOnlyDir})
	}
	return append(bs, c.Binds...)
}

// hostEnv is xbind's environment for a direct run, minus the GIT_* variables
// (a GIT_DIR or GIT_CONFIG_* of the daemon's must not steer a tool pointed
// at a tile).
func hostEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			env = append(env, e)
		}
	}
	return env
}

// RO is a read-only bind of a host path at the same path.
func RO(path string) sandbox.Bind { return sandbox.Bind{Src: path, Dst: path, RO: true} }

// RW is a read-write bind of a host path at the same path.
func RW(path string) sandbox.Bind { return sandbox.Bind{Src: path, Dst: path} }

// Mask hides a path under an empty, sealed tmpfs; MaskOpen lets deeper binds
// nest on top of the cover.
func Mask(path string) sandbox.Bind     { return sandbox.Bind{Dst: path, Mask: true, RO: true} }
func MaskOpen(path string) sandbox.Bind { return sandbox.Bind{Dst: path, Mask: true} }

// capped is an io.Writer that keeps the first max bytes.
type capped struct {
	bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.Len(); room > 0 {
		if len(p) > room {
			c.Buffer.Write(p[:room])
		} else {
			c.Buffer.Write(p)
		}
	}
	return len(p), nil
}
