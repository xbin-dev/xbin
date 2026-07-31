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
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/gpu"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/relay"
	"github.com/xbin-dev/xbin/internal/util"
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
	gen          int
	sock         string
	token        string
	cmd          *exec.Cmd
	relay        *relay.Relay     // userspace egress/ingress relay (nil unless plumbed)
	splicer      *relay.Splicer   // L3 splice to a net-provider tile (nil unless bound)
	linkSplicers []*relay.Splicer // lan-ingress legs into provider tiles (plans/ingress.md)
	provider     string           // net-provider this instance is a client of ("" if none)
	egress       []string         // granted net:* rules (for visibility)
	started      time.Time        // for uptime
	waitCh       chan struct{}    // closed when the process exits
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
	RunDir string // .xbin/run
	Auth   *auth.Auth
	Hub    *events.Hub
	Reg    *registry.Registry
	// EnvForComponent returns resource/identity env for a component instance
	// (installed by the broker; nil-safe).
	EnvForComponent func(c *registry.Component) []string
	// ShouldRun reports whether a component may spawn — false for a disabled/
	// offloaded component (plans/lifecycle.md). Gates Ensure authoritatively, so
	// the watcher/grant respawn paths (run.Changed) can't bring a disabled backend
	// back. nil = always allowed. Wired to the registry lifecycle by main.
	ShouldRun func(comp string) bool
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
	// GPU returns a component's granted GPUs (gpu:* grants); their device nodes +
	// driver libs are bound into the sandbox. nil = no GPU access.
	GPU func(c *registry.Component) []gpu.Device
	// NetRoster returns a net-provider tile's per-client links (empty = not a
	// provider); NetTarget returns the provider + link addrs for a component whose
	// net interface is bound to a provider tile (plans/interfaces.md).
	NetRoster func(c *registry.Component) []sandbox.NetClient
	NetTarget func(c *registry.Component) (provider, addr, gw string, ok bool)
	// NetHost reports whether a component's net interface is bound to the host
	// builtin (share the host network).
	NetHost func(c *registry.Component) bool
	// NetCaps reports whether a component holds the admin-granted cap:net-admin
	// capability — a net-PROVIDER tile keeps the network-admin caps (NET_ADMIN,
	// NET_RAW, NET_BIND_SERVICE) to build its dataplane, instead of being fully
	// unprivileged (plans/interfaces.md, DECISIONS D18a). nil = never.
	NetCaps func(c *registry.Component) bool
	// ContainerCaps reports whether a component holds the admin-granted
	// cap:containers capability — a **container-host tile** keeps its userns
	// capabilities + a minimal seccomp floor so rootless podman can build nested
	// namespaces/mounts (plans/containers.md). nil = never.
	ContainerCaps func(c *registry.Component) bool
	// IngressNet reports whether a component needs in-netns reachability even
	// without egress — it has a bound stream expose or stream interface
	// (plans/ingress.md) — which forces the TUN+relay plumbing with a deny-all
	// egress policy so DialInto can reach its ports. nil = never.
	IngressNet func(c *registry.Component) bool
	// IngressFwd returns a component's policy-exempt gateway forwards (virtual
	// gateway port → host dial target): a terminator tile's ingress-forward
	// socket, a stream interface's provider port. nil/empty = none.
	IngressFwd func(c *registry.Component) map[int]string
	// NetLinks returns a component's lan-ingress legs into provider tiles
	// (plans/ingress.md ING-6). nil = none.
	NetLinks func(c *registry.Component) []sandbox.NetLink
	// Published + HairpinDial wire split-horizon resolution for published
	// hostnames into each egress relay (plans/ingress.md ING-6): DNS answers
	// published names with the hairpin VIP, and VIP flows dial the ingress
	// path. nil = no split horizon.
	Published   func(host string) bool
	HairpinDial func(port int) (net.Conn, error)
	// Cgroup, when set, attaches each backend to a per-component cgroup v2 leaf
	// for memory/CPU/pids accounting (best-effort; nil-safe).
	Cgroup *cgroup.Manager

	mu     sync.Mutex
	states map[string]*state
	netmux *netMux
}

func New(root string, a *auth.Auth, hub *events.Hub, reg *registry.Registry) *Runner {
	runDir := runDirFor(root)
	_ = os.MkdirAll(filepath.Join(root, ".xbin", "log"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, ".xbin", "cache"), 0o755)
	r := &Runner{
		Root: root, RunDir: runDir, Auth: a, Hub: hub, Reg: reg,
		states: map[string]*state{}, netmux: newNetMux(),
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
	// Lifecycle gate (plans/lifecycle.md): a disabled/offloaded component never
	// spawns — enforced here so no path (proxy, watcher rebuild, grant change)
	// can start it. The proxy still 409s earlier for a nicer message.
	if r.ShouldRun != nil && !r.ShouldRun(c.Path) {
		return "", fmt.Errorf("component %s is not enabled", c.Path)
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
	// Don't respawn a disabled/offloaded component on a file change (Ensure would
	// refuse anyway; this just skips the pointless goroutine + build).
	if hadProcess && (r.ShouldRun == nil || r.ShouldRun(c.Path)) {
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
			s.lastErr = fmt.Errorf("backend crash-looping (%d exits); fix the code and save to retry — see .xbin/log/%s.log", recent, util.CompKey(c.Path))
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
		out := filepath.Join(r.Root, ".xbin", "build", util.CompKey(c.Path), "bin")
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return "", err
		}
		cmd := exec.Command("go", "build", "-o", out, entry)
		cmd.Dir = c.Dir
		cmd.Env = append(os.Environ(),
			"GOCACHE="+filepath.Join(r.Root, ".xbin", "cache", "go-build"),
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
			return "", &BuildError{Output: fmt.Sprintf("entry %s not found (set \"entry\" in xbin.json)", entry)}
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
		"XBIN_SOCKET="+sock,
		"XBIN_COMPONENT="+c.Path,
		"XBIN_GATEWAY="+filepath.Join(r.RunDir, "gateway.sock"),
		"XBIN_TOKEN="+token,
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
		// Build the component's env layer (setup deps) if declared, then stack it.
		envLower, err := r.ensureEnvLayer(c)
		if err != nil {
			return nil, err
		}
		cmd, sb, err = r.sandboxCmd(c, bin, dir, sock, env, pol, envLower)
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
		filepath.Join(r.Root, ".xbin", "log", util.CompKey(c.Path)+".log"),
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
	if r.Cgroup != nil {
		r.Cgroup.Add(util.CompKey(c.Path), cmd.Process.Pid)
	}
	// Range-uid sandbox: map the child's uids and release its init (which is
	// blocked waiting) before anything reads back from it (e.g. the TUN fd).
	if err := sb.SetupUserns(); err != nil {
		fmt.Fprintf(logf, "userns setup: %v\n", err)
	}

	inst := &instance{gen: gen, sock: sock, token: token, cmd: cmd, started: time.Now(), egress: pol.Strings(), waitCh: make(chan struct{})}

	// Network setup: the init handed back its TUN fd(s) — egress first, then one
	// per provider client-link, then this component's own lan-ingress legs. The
	// egress is either spliced to a provider tile (this component is a client of
	// it) or run through the userspace relay.
	if sb.NeedsRelay() {
		var netClients []sandbox.NetClient
		if r.NetRoster != nil {
			netClients = r.NetRoster(c)
		}
		provider, _, _, spliced := "", "", "", false
		if r.NetTarget != nil {
			provider, _, _, spliced = r.NetTarget(c)
		}
		if fd, err := sb.RecvTUN(); err != nil {
			fmt.Fprintf(logf, "egress tun: %v (egress disabled)\n", err)
		} else if spliced {
			r.ensureProvider(provider) // provider must be up so its links are registered
			if pfd, ok := r.netmux.get(provider, c.Path); ok {
				inst.splicer = relay.Splice(fd, pfd)
				inst.provider = provider
			} else {
				fmt.Fprintf(logf, "net provider %s link not ready — no egress\n", provider)
			}
		} else {
			cfg := relay.Config{TunFD: fd, Allow: pol.Allow, Resolver: sandbox.HostResolver()}
			if pol.Empty() {
				// Ingress-only plumbing: the relay exists so xbind can dial IN
				// (bound stream exposes); outbound stays deny-all — including
				// DNS, which would otherwise be a free exfiltration channel.
				cfg.Resolver = ""
			} else if r.Published != nil && r.HairpinDial != nil {
				// Split-horizon for published names rides only on tiles that
				// have SOME egress — a no-egress tile gets no hairpin either.
				cfg.Published = r.Published
				cfg.HairpinDial = r.HairpinDial
			}
			if r.IngressFwd != nil {
				if m := r.IngressFwd(c); len(m) > 0 {
					cfg.Gateway = netip.MustParseAddr(sandbox.GatewayIP)
					cfg.HostFwd = m
					cfg.HostDial = r.hostDial
				}
			}
			if rl, err := relay.Start(cfg); err != nil {
				fmt.Fprintf(logf, "egress relay: %v (egress disabled)\n", err)
			} else {
				inst.relay = rl
			}
		}
		// Provider tile: receive one TUN per client link and register it.
		for _, cl := range netClients {
			if fd, err := sb.RecvTUN(); err != nil {
				fmt.Fprintf(logf, "client link %s: %v\n", cl.Name, err)
			} else {
				r.netmux.register(c.Path, cl.Name, fd)
			}
		}
		// Lan-ingress legs: splice each to the provider's matching client link
		// (registered under "<client>#<slot>" in its roster).
		var netLinks []sandbox.NetLink
		if r.NetLinks != nil {
			netLinks = r.NetLinks(c)
		}
		for _, ll := range netLinks {
			fd, err := sb.RecvTUN()
			if err != nil {
				fmt.Fprintf(logf, "lan-ingress link %s: %v\n", ll.Slot, err)
				continue
			}
			r.ensureProvider(ll.Provider)
			if pfd, ok := r.netmux.get(ll.Provider, c.Path+"#"+ll.Slot); ok {
				inst.linkSplicers = append(inst.linkSplicers, relay.Splice(fd, pfd))
			} else {
				fmt.Fprintf(logf, "lan-ingress provider %s link not ready for %s\n", ll.Provider, ll.Slot)
			}
		}
		if len(netClients) > 0 {
			// This provider (re)started with fresh link fds; any client already
			// running is spliced to a now-stale fd, so nudge each to re-splice.
			// Lan-ingress roster entries are keyed "<client>#<slot>" — strip to
			// the component for the nudge.
			go func(clients []sandbox.NetClient) {
				for _, cl := range clients {
					name := cl.Name
					if i := strings.IndexByte(name, '#'); i >= 0 {
						name = name[:i]
					}
					if cc, ok := r.Reg.Component(name); ok {
						r.Changed(cc)
					}
				}
			}(netClients)
		}
	}

	go func() {
		_ = cmd.Wait()
		if inst.relay != nil {
			inst.relay.Close()
		}
		if inst.splicer != nil {
			inst.splicer.Close() // stop the L3 splice (leaves the provider link open)
		}
		for _, s := range inst.linkSplicers {
			s.Close()
		}
		if r.Cgroup != nil {
			r.Cgroup.Remove(util.CompKey(c.Path))
		}
		cleanup() // remove the sandbox spec temp file (init self-removes; this is a backstop)
		logf.Close()
		r.Auth.RevokeInstance(token)
		close(inst.waitCh)
	}()
	return inst, nil
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
func (r *Runner) sandboxCmd(c *registry.Component, bin, dir, sock string, env []string, pol sandbox.EgressPolicy, envLower string) (*exec.Cmd, *sandbox.Handle, error) {
	gw := filepath.Join(r.RunDir, "gateway.sock")
	binds := []sandbox.Bind{
		{Src: c.Dir, Dst: c.Dir, RO: true}, // component source, read-only
		{Src: dir, Dst: dir},               // run dir — the listen socket lands here
		{Src: gw, Dst: gw},                 // the gateway socket (component↔component + RBAC)
	}
	// File-backed resources (sqlite) are handed to the backend as absolute
	// XBIN_RES_* env paths; bind their dirs read-write so they persist.
	binds = append(binds, resourceBinds(env, r.Root)...)

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

	// Granted GPUs (gpu:*): bind the device nodes + driver libs and add env.
	if r.GPU != nil {
		if gb, genv := gpu.Binds(r.GPU(c)); len(gb) > 0 {
			binds = append(binds, gb...)
			env = append(env, genv...)
		}
	}

	// Overlay lowers: the component env layer (setup deps) on top of the base
	// rootfs, so the layer's files win.
	lower := []string{r.Rootfs}
	if envLower != "" {
		lower = []string{envLower, r.Rootfs}
	}
	spec := &sandbox.Spec{
		Lower:        lower,
		Binds:        binds,
		Entry:        entry,
		Argv:         argv,
		Env:          env,
		Cwd:          c.Dir,
		HostUID:      os.Getuid(),
		HostGID:      os.Getgid(),
		Unprivileged: true, // tile backends need no caps: drop them + seccomp block-list
	}
	// Interface wiring (plans/interfaces.md): a net-provider tile gets one TUN per
	// bound client; a component's `net` interface resolves to host-share, a splice
	// through a provider tile, or the relay under a builtin (internet/lan) policy.
	if r.NetRoster != nil {
		spec.NetClients = r.NetRoster(c)
	}
	if r.NetLinks != nil {
		spec.NetLinks = r.NetLinks(c) // lan-ingress legs (plans/ingress.md)
	}
	if r.NetCaps != nil && r.NetCaps(c) {
		spec.NetAdmin = true // net-provider tile (cap:net-admin) keeps net-admin caps
	}
	if r.ContainerCaps != nil && r.ContainerCaps(c) {
		spec.Containers = true // container-host tile (cap:containers): keep caps, minimal seccomp
	}
	if r.NetHost != nil && r.NetHost(c) {
		spec.HostNet = true // net → host builtin (share the host network)
	} else if r.NetTarget != nil {
		if _, addr, gw, ok := r.NetTarget(c); ok {
			spec.Net, spec.NetAddr, spec.NetGw = "splice", addr, gw
		}
	}
	if spec.Net == "" && !spec.HostNet && !pol.Empty() {
		spec.Net = "relay" // granted / bound builtin egress → TUN + userspace relay
	}
	if spec.Net == "" && !spec.HostNet {
		// Ingress plumbing without egress (plans/ingress.md): a bound stream
		// expose / stream interface / lan-ingress leg needs the TUN so xbind
		// can reach in — the relay runs with a deny-all outbound policy.
		if (r.IngressNet != nil && r.IngressNet(c)) || len(spec.NetLinks) > 0 {
			spec.Net = "relay"
		}
	}
	return sandbox.Launch(spec)
}

func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimRight(root, "/")+"/")
}

func pathExists(p string) bool { _, err := os.Stat(p); return err == nil }

// resourceBinds returns the read-write binds for a component's file-backed
// resources (sqlite). EnvFor hands each granted same-scope sqlite resource to the
// backend as an absolute XBIN_RES_* path. We bind that path's **directory** (the
// scope's resource dir), not the file, so that:
//   - a *fresh* db works — the file doesn't exist yet (sqlite creates it on first
//     open), so binding the file alone would drop it (pathExists was false) and
//     the db would land on the throwaway overlay instead of persisting; and
//   - sqlite's -wal/-shm sidecars, written next to the db, persist too.
//
// Non-path resources (kv/blob/bus, addressed by res: string over HTTP) aren't
// paths and are skipped. Dirs are deduped; only paths under root are bound.
func resourceBinds(env []string, root string) []sandbox.Bind {
	seen := map[string]bool{}
	var binds []sandbox.Bind
	for _, e := range env {
		i := strings.IndexByte(e, '=')
		if i < 0 {
			continue
		}
		v := e[i+1:]
		if !strings.HasPrefix(v, "/") || !within(v, root) {
			continue
		}
		// A `filesystem` resource hands the backend a DIRECTORY (bind it); a
		// `sqlite` resource hands a FILE path (bind its dir so a fresh db + the
		// -wal/-shm sidecars persist, not just the file).
		d := v
		if fi, err := os.Stat(v); err != nil || !fi.IsDir() {
			d = filepath.Dir(v)
		}
		if seen[d] || !pathExists(d) {
			continue
		}
		seen[d] = true
		binds = append(binds, sandbox.Bind{Src: d, Dst: d})
	}
	return binds
}

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

// Stop terminates a single component's running backend, if any (e.g. when the
// owner disables/offloads it). A subsequent request re-spawns it (unless the
// caller has since gated it). No-op if it isn't running.
func (r *Runner) Stop(comp string) {
	r.mu.Lock()
	s := r.states[comp]
	r.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	inst := s.cur
	s.cur = nil
	s.mu.Unlock()
	if inst != nil {
		r.stop(inst, 5*time.Second)
	}
}

// StopAll terminates all backends (xbind shutdown).
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

// Status reports per-component backend state for /api/xbin/status.
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

// runDirFor picks the socket directory. Preferred: <ws>/.xbin/run. Unix
// socket paths are limited to ~108 bytes, so for deeply nested workspaces we
// fall back to a short tmp dir and leave a symlink at .xbin/run so shells
// still find the sockets where the docs say they are.
// runDirFor picks the directory holding xbind's IPC sockets (the gateway socket
// + each backend's per-generation listen socket). It is bind-mounted **RW** into
// every component sandbox, so it must live on a **tmpfs**, never the workspace
// disk: a component sandbox must not get a RW mount backed by host disk
// (plans/isolation.md — only tmpfs / gocryptfs / ro). We prefer systemd's
// RuntimeDirectory (/run/xbin) or $XDG_RUNTIME_DIR (both tmpfs), then $TMPDIR;
// a symlink under `.xbin/run` points at it for discoverability.
func runDirFor(root string) string {
	link := filepath.Join(root, ".xbin", "run")
	_ = os.RemoveAll(link) // stale sockets/symlink from a previous xbind
	h := sha256.Sum256([]byte(root))
	name := "xbin-" + hex.EncodeToString(h[:4])
	for _, base := range runtimeBases() {
		dir := filepath.Join(base, name, "run")
		_ = os.RemoveAll(dir)
		if os.MkdirAll(dir, 0o700) != nil {
			continue
		}
		if !isTmpfs(dir) {
			_ = os.RemoveAll(dir)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(link), 0o755)
		_ = os.Symlink(dir, link)
		return dir
	}
	// No tmpfs available: fall back to the workspace and warn — a component
	// sandbox will then bind a RW host-disk run dir (set RuntimeDirectory=xbin).
	slog.Warn("run dir: no tmpfs runtime dir found (RuntimeDirectory/XDG_RUNTIME_DIR/TMPDIR); component sandboxes will get a RW host-disk run dir — set RuntimeDirectory=xbin in the unit")
	_ = os.MkdirAll(link, 0o755)
	return link
}

// runtimeBases lists tmpfs candidates for the run dir, most-preferred first.
func runtimeBases() []string {
	var out []string
	if d := os.Getenv("RUNTIME_DIRECTORY"); d != "" { // systemd RuntimeDirectory=
		out = append(out, strings.SplitN(d, ":", 2)[0])
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		out = append(out, d)
	}
	return append(out, os.TempDir())
}

// isTmpfs reports whether dir is on a tmpfs/ramfs, so a RW bind of it into a
// sandbox exposes no host disk.
func isTmpfs(dir string) bool {
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) != nil {
		return false
	}
	switch uint32(st.Type) {
	case 0x01021994, 0x858458f6: // TMPFS_MAGIC, RAMFS_MAGIC
		return true
	}
	return false
}
