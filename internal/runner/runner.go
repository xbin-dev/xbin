// Package runner supervises component backends: rebuild-on-change (Go),
// restart-on-change (node/python), per-request exec (cgi). Each component's
// backend serves HTTP on a private unix socket; the proxy package routes
// /api/<component>/… to it.
//
// Lifecycle per component (plans/implementation.md phase 2):
//
//	idle → building → starting → healthy → draining → stopped | failed
//
// Blue/green: a new generation is built and health-checked while the old one
// keeps serving; the swap is atomic; the old generation gets SIGTERM and a
// 30 s deadline (decision D8). Instance identity tokens are minted per
// generation and revoked at swap (plans/auth.md §2).
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/magik6k/buxon/internal/auth"
	"github.com/magik6k/buxon/internal/events"
	"github.com/magik6k/buxon/internal/registry"
	"github.com/magik6k/buxon/internal/sandbox"
	"github.com/magik6k/buxon/internal/sandbox/relay"
	"github.com/magik6k/buxon/internal/util"
)

const (
	healthTimeout = 5 * time.Second
	drainDeadline = 30 * time.Second // D8
	idleReap      = 30 * time.Minute
	crashWindow   = 10 * time.Second
	crashLimit    = 3
)

// BuildError carries compiler output to the error overlay.
type BuildError struct{ Output string }

func (e *BuildError) Error() string { return "build failed:\n" + e.Output }

type instance struct {
	gen     int
	sock    string
	token   string
	cmd     *exec.Cmd
	relay   *relay.Relay  // userspace egress relay (nil unless net:* granted)
	egress  []string      // granted net:* rules (for visibility)
	started time.Time     // for uptime
	waitCh  chan struct{} // closed when the process exits
}

type state struct {
	mu        sync.Mutex
	comp      string
	gen       int
	cur       *instance
	building  bool
	buildDone chan struct{} // closed when in-flight build settles
	lastErr   error         // sticky build/crash error until next change
	dirty     bool          // changed since last successful build
	lastReq   time.Time
	active    int // in-flight proxied connections (incl. SSE/WS streams)
	crashes   []time.Time
}

type Runner struct {
	Root   string // workspace root
	RunDir string // .buxon/run
	Auth   *auth.Auth
	Hub    *events.Hub
	Reg    *registry.Registry
	// EnvForComponent returns resource/identity env for a component instance
	// (installed by the broker; nil-safe).
	EnvForComponent func(c *registry.Component) []string
	// SpawnUser, when non-nil, returns uid/gid to run a component's backend
	// as (auth tier 2, per-scope uids). nil = same-user (tier 1).
	SpawnUser func(c *registry.Component) *syscall.Credential

	// Isolate + Rootfs enable per-component sandboxing (auth tier 3): each
	// backend runs in its own user/mount/pid/net namespaces over an overlay of
	// Rootfs, with default-deny egress (plans/isolation.md). Off by default.
	Isolate bool
	Rootfs  string
	// Egress returns a component's granted egress policy (net:* grants). A
	// non-empty policy enables the TUN + userspace relay; empty = default-deny.
	Egress func(c *registry.Component) sandbox.EgressPolicy

	mu     sync.Mutex
	states map[string]*state
}

func New(root string, a *auth.Auth, hub *events.Hub, reg *registry.Registry) *Runner {
	runDir := runDirFor(root)
	_ = os.MkdirAll(filepath.Join(root, ".buxon", "log"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, ".buxon", "cache"), 0o755)
	r := &Runner{
		Root: root, RunDir: runDir, Auth: a, Hub: hub, Reg: reg,
		states: map[string]*state{},
	}
	go r.reaper()
	return r
}

func (r *Runner) state(comp string) *state {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.states[comp]
	if !ok {
		s = &state{comp: comp, dirty: true}
		r.states[comp] = s
	}
	return s
}

// Ensure returns the unix socket of a healthy backend for c, (re)building
// first if needed. Blocks concurrent callers during builds (single-flight)
// so a save under load never surfaces connection-refused.
func (r *Runner) Ensure(ctx context.Context, c *registry.Component) (string, error) {
	if c.Manifest.Runtime == "cgi" || c.Manifest.Runtime == "" || c.Manifest.Runtime == "static" {
		return "", fmt.Errorf("component %s has no long-running backend", c.Path)
	}
	s := r.state(c.Path)

	for {
		s.mu.Lock()
		s.lastReq = time.Now()
		if !s.dirty && s.cur != nil {
			sock := s.cur.sock
			s.mu.Unlock()
			return sock, nil
		}
		if !s.dirty && s.lastErr != nil {
			err := s.lastErr
			s.mu.Unlock()
			return "", err
		}
		if s.building {
			done := s.buildDone
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		// We take the build.
		s.building = true
		s.dirty = false
		s.buildDone = make(chan struct{})
		s.mu.Unlock()

		err := r.buildAndStart(c, s)

		s.mu.Lock()
		s.building = false
		s.lastErr = err
		close(s.buildDone)
		s.mu.Unlock()
		// Loop: re-check (another change may have arrived mid-build).
	}
}

// Track marks one in-flight proxied connection to comp's backend; the
// returned release must be called when it ends. Long-lived streams (SSE,
// WebSocket) hold this for their whole lifetime, which keeps the idle
// reaper away from backends that are quietly serving them.
func (r *Runner) Track(comp string) func() {
	s := r.state(comp)
	s.mu.Lock()
	s.active++
	s.lastReq = time.Now()
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.active--
			s.lastReq = time.Now()
			s.mu.Unlock()
		})
	}
}

// Changed marks a component dirty and kicks a background rebuild so build
// errors surface on save, not on next request.
func (r *Runner) Changed(c *registry.Component) {
	if !c.HasBackend() || c.Manifest.Runtime == "cgi" {
		return
	}
	s := r.state(c.Path)
	s.mu.Lock()
	s.dirty = true
	s.crashes = nil
	hadProcess := s.cur != nil || s.building || s.lastErr != nil
	s.mu.Unlock()
	if hadProcess {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, _ = r.Ensure(ctx, c)
		}()
	}
}

// buildAndStart runs one generation transition. Called single-flight per state.
func (r *Runner) buildAndStart(c *registry.Component, s *state) error {
	r.Hub.Publish(events.Event{Type: "build-start", Component: c.Path})

	bin, err := r.build(c)
	if err != nil {
		r.Hub.Publish(events.Event{Type: "build-error", Component: c.Path, Text: err.Error()})
		return err
	}

	s.mu.Lock()
	s.gen++
	gen := s.gen
	old := s.cur
	s.mu.Unlock()

	inst, err := r.start(c, bin, gen)
	if err != nil {
		r.Hub.Publish(events.Event{Type: "build-error", Component: c.Path, Text: err.Error()})
		return err
	}
	if err := waitHealthy(inst.sock, healthTimeout); err != nil {
		r.stop(inst, 2*time.Second)
		err = fmt.Errorf("backend did not become healthy: %w", err)
		r.Hub.Publish(events.Event{Type: "build-error", Component: c.Path, Text: err.Error()})
		return err
	}

	s.mu.Lock()
	s.cur = inst
	s.mu.Unlock()
	if old != nil {
		go r.stop(old, drainDeadline)
	}

	// Crash watch: if the healthy process dies without being replaced, mark
	// the state so the next request rebuilds (and break crash loops).
	go func() {
		<-inst.waitCh
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.cur != inst {
			return // replaced normally
		}
		s.cur = nil
		s.crashes = append(s.crashes, time.Now())
		recent := 0
		for _, t := range s.crashes {
			if time.Since(t) < crashWindow*time.Duration(crashLimit) {
				recent++
			}
		}
		if recent >= crashLimit {
			s.lastErr = fmt.Errorf("backend crash-looping (%d exits); fix the code and save to retry — see .buxon/log/%s.log", recent, util.CompKey(c.Path))
			r.Hub.Publish(events.Event{Type: "build-error", Component: c.Path, Text: s.lastErr.Error()})
		} else {
			s.dirty = true // transparent restart on next request
		}
	}()

	r.Hub.Publish(events.Event{Type: "build-ok", Component: c.Path})
	slog.Info("backend up", "component", c.Path, "gen", gen)
	return nil
}

// build produces a runnable entry. For go it compiles; for node/python it
// just validates the entry file exists (the interpreter is the "binary").
func (r *Runner) build(c *registry.Component) (string, error) {
	switch c.Manifest.Runtime {
	case "go":
		entry := c.Manifest.Entry
		if entry == "" {
			entry = "./backend"
		}
		out := filepath.Join(r.Root, ".buxon", "build", util.CompKey(c.Path), "bin")
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return "", err
		}
		cmd := exec.Command("go", "build", "-o", out, entry)
		cmd.Dir = c.Dir
		cmd.Env = append(os.Environ(),
			"GOCACHE="+filepath.Join(r.Root, ".buxon", "cache", "go-build"),
		)
		if r.Isolate {
			// Build fully static so the backend runs on any sandbox rootfs,
			// independent of the base image's glibc (plans/isolation-impl.md).
			cmd.Env = append(cmd.Env, "CGO_ENABLED=0")
		}
		if outp, err := cmd.CombinedOutput(); err != nil {
			return "", &BuildError{Output: string(outp)}
		}
		return out, nil
	case "node", "python":
		entry := c.Manifest.Entry
		if entry == "" {
			if c.Manifest.Runtime == "node" {
				entry = "backend/server.js"
			} else {
				entry = "backend/server.py"
			}
		}
		p := filepath.Join(c.Dir, filepath.FromSlash(entry))
		if _, err := os.Stat(p); err != nil {
			return "", &BuildError{Output: fmt.Sprintf("entry %s not found (set \"entry\" in buxon.json)", entry)}
		}
		return p, nil
	default:
		return "", fmt.Errorf("unknown runtime %q", c.Manifest.Runtime)
	}
}

func (r *Runner) start(c *registry.Component, bin string, gen int) (*instance, error) {
	dir := filepath.Join(r.RunDir, util.CompKey(c.Path))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	sock := filepath.Join(dir, fmt.Sprintf("g%d.sock", gen))
	_ = os.Remove(sock)

	token := util.RandomToken(24)

	env := append(os.Environ(),
		"BUXON_SOCKET="+sock,
		"BUXON_COMPONENT="+c.Path,
		"BUXON_GATEWAY="+filepath.Join(r.RunDir, "gateway.sock"),
		"BUXON_TOKEN="+token,
	)
	if r.EnvForComponent != nil {
		env = append(env, r.EnvForComponent(c)...)
	}

	var cmd *exec.Cmd
	var sb *sandbox.Handle
	var pol sandbox.EgressPolicy
	cleanup := func() {}
	if r.Isolate && sandboxable(c.Manifest.Runtime) {
		if r.Egress != nil {
			pol = r.Egress(c)
		}
		var err error
		cmd, sb, err = r.sandboxCmd(c, bin, dir, sock, env, pol)
		if err != nil {
			return nil, fmt.Errorf("sandbox: %w", err)
		}
		cleanup = sb.Cleanup
	} else {
		switch c.Manifest.Runtime {
		case "go":
			cmd = exec.Command(bin)
		case "node":
			cmd = exec.Command("node", bin)
		case "python":
			cmd = exec.Command("python3", bin)
		}
		cmd.Dir = c.Dir
		cmd.Env = env
		if r.SpawnUser != nil {
			if cred := r.SpawnUser(c); cred != nil {
				cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
			}
		}
	}

	logf, err := os.OpenFile(
		filepath.Join(r.Root, ".buxon", "log", util.CompKey(c.Path)+".log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(logf, "--- gen %d start %s ---\n", gen, time.Now().Format(time.RFC3339))
	cmd.Stdout, cmd.Stderr = logf, logf

	if err := cmd.Start(); err != nil {
		logf.Close()
		cleanup()
		return nil, fmt.Errorf("start backend: %w", err)
	}
	r.Auth.RegisterInstance(token, c.Path)

	inst := &instance{gen: gen, sock: sock, token: token, cmd: cmd, started: time.Now(), egress: pol.Strings(), waitCh: make(chan struct{})}

	// Granted egress: the init created a TUN in the netns and handed us its fd;
	// run the userspace relay on it, enforcing the policy.
	if sb.NeedsRelay() {
		if fd, err := sb.RecvTUN(); err != nil {
			fmt.Fprintf(logf, "egress relay: %v (egress disabled)\n", err)
		} else if rl, err := relay.Start(fd, pol.Allow, r.resolver()); err != nil {
			fmt.Fprintf(logf, "egress relay: %v (egress disabled)\n", err)
		} else {
			inst.relay = rl
		}
	}

	go func() {
		_ = cmd.Wait()
		if inst.relay != nil {
			inst.relay.Close()
		}
		cleanup() // remove the sandbox spec temp file (init self-removes; this is a backstop)
		logf.Close()
		r.Auth.RevokeInstance(token)
		close(inst.waitCh)
	}()
	return inst, nil
}

// resolver is the upstream DNS the relay forwards component :53 queries to:
// the host's first nameserver, else a public default.
func (r *Runner) resolver() string {
	if b, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if ns, ok := strings.CutPrefix(line, "nameserver "); ok {
				ns = strings.TrimSpace(ns)
				if !strings.Contains(ns, ":") {
					return ns + ":53"
				}
				return "[" + ns + "]:53"
			}
		}
	}
	return "1.1.1.1:53"
}

func sandboxable(runtime string) bool {
	switch runtime {
	case "go", "node", "python":
		return true
	}
	return false
}

// sandboxCmd builds the isolated backend command (plans/isolation.md): a
// per-component namespace set over an overlay of r.Rootfs. The component's
// source is read-only, its run dir and same-scope resource files are read-write,
// the gateway socket is the one door out, and the netns is empty (default-deny
// egress; the egress relay is a follow-on).
func (r *Runner) sandboxCmd(c *registry.Component, bin, dir, sock string, env []string, pol sandbox.EgressPolicy) (*exec.Cmd, *sandbox.Handle, error) {
	gw := filepath.Join(r.RunDir, "gateway.sock")
	binds := []sandbox.Bind{
		{Src: c.Dir, Dst: c.Dir, RO: true}, // component source, read-only
		{Src: dir, Dst: dir},               // run dir — the listen socket lands here
		{Src: gw, Dst: gw},                 // the gateway socket (component↔component + RBAC)
	}
	// Same-scope resource files (sqlite/blob) are handed to the backend as env
	// paths; bind just those, read-write.
	for _, e := range env {
		i := strings.IndexByte(e, '=')
		if i < 0 {
			continue
		}
		v := e[i+1:]
		if strings.HasPrefix(v, "/") && within(v, r.Root) && pathExists(v) {
			binds = append(binds, sandbox.Bind{Src: v, Dst: v})
		}
	}

	var entry string
	var argv []string
	switch c.Manifest.Runtime {
	case "go":
		entry = "/run/backend" // the built static binary, bound in
		argv = []string{entry}
		binds = append(binds, sandbox.Bind{Src: bin, Dst: entry, RO: true})
	case "node":
		entry, argv = "/usr/bin/node", []string{"node", bin} // bin is a script under c.Dir (bound)
	case "python":
		entry, argv = "/usr/bin/python3", []string{"python3", bin}
	}

	spec := &sandbox.Spec{
		Lower:   []string{r.Rootfs},
		Binds:   binds,
		Entry:   entry,
		Argv:    argv,
		Env:     env,
		Cwd:     c.Dir,
		HostUID: os.Getuid(),
		HostGID: os.Getgid(),
	}
	if !pol.Empty() {
		spec.Net = "relay" // granted egress → TUN + userspace relay
	}
	return sandbox.Launch(spec)
}

func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimRight(root, "/")+"/")
}

func pathExists(p string) bool { _, err := os.Stat(p); return err == nil }

func (r *Runner) stop(inst *instance, deadline time.Duration) {
	if inst.cmd.Process == nil {
		return
	}
	_ = inst.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-inst.waitCh:
	case <-time.After(deadline):
		_ = inst.cmd.Process.Kill()
		<-inst.waitCh
	}
	_ = os.Remove(inst.sock)
}

// StopAll terminates all backends (buxond shutdown).
func (r *Runner) StopAll() {
	r.mu.Lock()
	states := make([]*state, 0, len(r.states))
	for _, s := range r.states {
		states = append(states, s)
	}
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range states {
		s.mu.Lock()
		inst := s.cur
		s.cur = nil
		s.mu.Unlock()
		if inst != nil {
			wg.Add(1)
			go func() { defer wg.Done(); r.stop(inst, 5*time.Second) }()
		}
	}
	wg.Wait()
}

// Status reports per-component backend state for /api/buxon/status.
func (r *Runner) Status() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]any{}
	for path, s := range r.states {
		s.mu.Lock()
		st := "idle"
		switch {
		case s.building:
			st = "building"
		case s.cur != nil:
			st = "healthy"
		case s.lastErr != nil:
			st = "failed"
		}
		e := map[string]any{"state": st, "gen": s.gen}
		if s.lastErr != nil {
			e["error"] = s.lastErr.Error()
		}
		out[path] = e
		s.mu.Unlock()
	}
	return out
}

func (r *Runner) reaper() {
	for range time.Tick(time.Minute) {
		r.mu.Lock()
		states := make([]*state, 0, len(r.states))
		for _, s := range r.states {
			states = append(states, s)
		}
		r.mu.Unlock()
		for _, s := range states {
			s.mu.Lock()
			if s.cur != nil && s.active == 0 && time.Since(s.lastReq) > idleReap {
				inst := s.cur
				s.cur = nil
				s.dirty = true // next request restarts lazily
				s.mu.Unlock()
				slog.Info("reaping idle backend", "component", s.comp)
				go r.stop(inst, 5*time.Second)
				continue
			}
			s.mu.Unlock()
		}
	}
}

func waitHealthy(sock string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("timeout dialing backend socket")
}

// runDirFor picks the socket directory. Preferred: <ws>/.buxon/run. Unix
// socket paths are limited to ~108 bytes, so for deeply nested workspaces we
// fall back to a short tmp dir and leave a symlink at .buxon/run so shells
// still find the sockets where the docs say they are.
func runDirFor(root string) string {
	runDir := filepath.Join(root, ".buxon", "run")
	_ = os.RemoveAll(runDir) // stale sockets/symlink from a previous buxond
	// Worst-case socket path: <runDir>/<name ≤33>/g<gen>.sock
	if len(runDir) <= 60 {
		_ = os.MkdirAll(runDir, 0o755)
		return runDir
	}
	h := sha256.Sum256([]byte(root))
	short := filepath.Join(os.TempDir(), "buxon-"+hex.EncodeToString(h[:4]))
	_ = os.RemoveAll(short)
	if err := os.MkdirAll(short, 0o755); err != nil {
		_ = os.MkdirAll(runDir, 0o755) // last resort: long path, may fail at bind
		return runDir
	}
	_ = os.MkdirAll(filepath.Dir(runDir), 0o755)
	_ = os.Symlink(short, runDir)
	slog.Info("workspace path too long for unix sockets; using short run dir",
		"dir", short)
	return short
}
