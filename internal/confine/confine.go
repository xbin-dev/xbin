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
	"path/filepath"
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
// same path inside, so tools print paths the caller understands — except
// where the run asks for another path to be shown there (DirFrom, At), which
// only a sandbox can do: a direct run refuses those (ErrNeedsIsolation).
type Cmd struct {
	Argv    []string       // Argv[0] is looked up in PATH (the sandbox's, or the host's when direct)
	Dir     string         // working directory; bound read-write unless ReadOnlyDir
	DirFrom string         // host path mounted at Dir (default Dir itself); needs isolation
	Binds   []sandbox.Bind // more mounts (read-only, masks, At, …); Dir's bind is added for you
	Env     []string       // on top of a minimal PATH/HOME/LANG (direct: on top of xbind's env minus GIT_*)
	Net     Net
	Stdin   io.Reader

	ReadOnlyDir bool          // bind Dir read-only
	Timeout     time.Duration // 0 = 2 minutes
	MaxOutput   int           // per stream; 0 = 64 MiB (the rest is dropped)

	// FSCaps runs the tool with the file capabilities instead of none
	// (sandbox.Spec.FileCaps): for du, rm and cp -a of a tree sandboxes
	// wrote (tree.go), which can hold files of other (sub-)uids and modes
	// their owner locked. Only for fixed tools whose argv no tree content
	// steers. Isolation off: no effect (a direct run is xbind).
	FSCaps bool
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

// ErrNeedsIsolation: a direct run (isolation off) was asked to show a host
// path at another destination — DirFrom other than Dir, or a bind made by At.
// Without a mount namespace the tool would run on what lies at the
// destination on the host (the work tree, not the checkpoint), so the run is
// refused before anything starts: D78 never degrades to the host (P18).
var ErrNeedsIsolation = errors.New("confined run: this job shows another path at its destination and needs --isolate")

const sandboxPATH = "/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// Run executes c: in a sandbox when isolation is on, else directly.
func Run(ctx context.Context, c Cmd) (Result, error) {
	if len(c.Argv) == 0 {
		return Result{}, errors.New("confine: empty argv")
	}
	if c.DirFrom != "" && c.Dir == "" {
		return Result{}, errors.New("confine: DirFrom without a Dir to show it at")
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
	if fs == "" {
		if err := c.directOK(); err != nil {
			return Result{}, err
		}
	}
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
			FileCaps:     c.FSCaps,
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
			rl, err := relay.Start(relay.Config{TunFD: fd, CloseTUN: true, Allow: pol.Allow, Resolver: sandbox.HostResolver()})
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

// binds is the sandbox's bind list: Dir first — showing DirFrom when set,
// the working directory stays Dir — then the caller's binds in order, those
// made by At unmarked.
func (c Cmd) binds() []sandbox.Bind {
	var bs []sandbox.Bind
	if c.Dir != "" {
		src := c.Dir
		if c.DirFrom != "" {
			src = c.DirFrom
		}
		bs = append(bs, sandbox.Bind{Src: src, Dst: c.Dir, RO: c.ReadOnlyDir})
	}
	for _, b := range c.Binds {
		if src, ok := strings.CutPrefix(b.Src, atMark); ok {
			b.Src = src
		}
		bs = append(bs, b)
	}
	return bs
}

// directOK refuses a direct run that asks for another path at a destination
// (P18): DirFrom naming anything but Dir, or a bind made by At. A bind the
// caller builds by hand keeps today's direct behaviour whatever its Src and
// Dst (the git import's ~/.ssh at /root/.ssh): the direct run already sees
// the host's own files where the sandbox would show them.
func (c Cmd) directOK() error {
	if c.DirFrom != "" && filepath.Clean(c.DirFrom) != filepath.Clean(c.Dir) {
		return fmt.Errorf("%w (%s shown at %s)", ErrNeedsIsolation, c.DirFrom, c.Dir)
	}
	for _, b := range c.Binds {
		if src, ok := strings.CutPrefix(b.Src, atMark); ok {
			return fmt.Errorf("%w (%s shown at %s)", ErrNeedsIsolation, src, b.Dst)
		}
	}
	return nil
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

// At binds a host path at another destination (a checkpoint's nested
// component, a per-build go.work over the workspace's); needs isolation.
// Pass it in a Cmd's Binds only: the bind carries a mark that tells Run it
// was made here — a direct run refuses it (ErrNeedsIsolation) even when src
// and dst are one path — and that no other consumer strips, so handed to
// anything but confine it fails to mount rather than binding silently.
func At(src, dst string, ro bool) sandbox.Bind {
	return sandbox.Bind{Src: atMark + src, Dst: dst, RO: ro}
}

// atMark prefixes the Src of a bind made by At. A path never holds a NUL, so
// no hand-built bind carries it.
const atMark = "\x00confine.At\x00"

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
