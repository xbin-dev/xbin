// Package runner supervises component backends: rebuild-on-change (Go),
// restart-on-change (node/python). Each component's backend serves HTTP on
// a private unix socket; the proxy package routes /api/<component>/… to it.
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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/gpu"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/relay"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/internal/vm"
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
	// Per deployment (deploy.go); zero values mean today's generation: the
	// work tree bound at c.Dir, the tile's one bin, its flat CompKey leaf.
	dep      string // its deployment's name
	code     Code   // what it runs
	root     string // the host directory bound at c.Dir
	artifact string // its built artifact
	leaf     string // its cgroup leaf
	envHash  string // its env layer's hash
	// retired is set once xbind stops the generation (stopGen): a swap
	// replaced it, or a stop, a reap or a shutdown ended it. Never set by
	// the process's own exit (a crash). The proxy resends a request whose
	// generation failed it only because xbind retired it (Gen.Retired).
	retired atomic.Bool
}

type state struct {
	mu        sync.Mutex
	comp      string // the tile's path
	dep       string // the deployment's name (deployments.go)
	gone      bool   // the deployment was removed: nothing installs here again
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
	// AlwaysOnSwitched names tile's non-primary deployments whose alwaysOn
	// switch is on (alwayson.go, 07-runtime §11); nil = none.
	AlwaysOnSwitched func(tile string) []string
	// HoldReason says why ShouldRun refuses comp ("is disabled", "is held:
	// …"), for Ensure's error; nil or "" = "is not enabled".
	HoldReason func(comp string) string
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
	cgOps  cgroupOps // limits.go: a test's cgroup manager in Cgroup's place; nil = Cgroup
	// TileCgroup is the tile sandboxes' cgroup parent, kind-tile rows' Leaf (nil: none).
	TileCgroup *cgroup.Manager
	VM         *vm.Manager // "vm" backends (vm.go); nil = none
	vms        vmState
	// Sandboxes lists every running generation (sbx.go, D112; nil-safe).
	Sandboxes *sbx.Registry
	// GoVersions is told of each Go build that succeeds, of a work tree or
	// a checkpoint: a tile its upgrade alert names is re-checked when what
	// decides its versions changed (goversionscheck.go, D166). nil = none.
	GoVersions      *GoVersions
	DeploymentHooks       // installed by the deployments plane; nil-safe (deploy.go)
	inUse           inUse // inspect.go: the trees and artifacts generations use

	mu     sync.Mutex
	states map[string]*state
	engine *engine  // engine.go: nil = today's build, start, health, stop and clock
	ao     alwaysOn // alwayson.go
	netmux *netMux
	stats  statsState // live per-tile resource stats (stats.go)
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

// state is comp's primary's runner state, created dirty when missing.
func (r *Runner) state(comp string) *state { return r.stateOf(comp, r.primary(comp)) }

// Ensure returns the unix socket of a healthy backend for c's primary,
// (re)building first if needed. Blocks concurrent callers during builds
// (single-flight) so a save under load never surfaces connection-refused.
func (r *Runner) Ensure(ctx context.Context, c *registry.Component) (string, error) {
	return sockOf(r.ensurePrimary(ctx, c, r.primary(c.Path)))
}

// EnsureGen is Ensure answering the generation itself, for a caller that
// sends it a request and must tell, when the request fails, whether xbind
// retired the generation meanwhile (the proxy).
func (r *Runner) EnsureGen(ctx context.Context, c *registry.Component) (Gen, error) {
	inst, err := r.ensurePrimary(ctx, c, r.primary(c.Path))
	return Gen{inst}, err
}

// Gen is one backend generation a request is sent to: its socket, and
// whether xbind has retired it since. The zero Gen has no socket and is
// never retired.
type Gen struct{ inst *instance }

// Sock is the generation's unix socket.
func (g Gen) Sock() string {
	if g.inst == nil {
		return ""
	}
	return g.inst.sock
}

// Retired says whether xbind has stopped the generation: a swap replaced it
// (the deployment's newer generation already serves), or a stop, a reap or
// a shutdown ended it. A generation that exited by itself (a crash) is not
// retired. Once true it stays true: a retired generation is never handed
// out again.
func (g Gen) Retired() bool { return g.inst != nil && g.inst.retired.Load() }

// sockOf is a generation's socket, for the callers that only dial it.
func sockOf(inst *instance, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return inst.sock, nil
}

// ensurePrimary is Ensure for deployment dep, c's primary, whose code the
// registry's component describes (07-runtime §5.1).
func (r *Runner) ensurePrimary(ctx context.Context, c *registry.Component, dep string) (*instance, error) {
	if err := registry.ValidateRuntime(c.Manifest); err != nil {
		return nil, fmt.Errorf("component %s: %w", c.Path, err) // runtime "cgi" (D117): never runs
	}
	if c.Manifest.Runtime == "" || c.Manifest.Runtime == "static" {
		return nil, fmt.Errorf("component %s has no long-running backend", c.Path)
	}
	// Lifecycle gate (plans/lifecycle.md): a disabled/offloaded component never
	// spawns — enforced here so no path (proxy, watcher rebuild, grant change)
	// can start it. The proxy still 409s earlier for a nicer message.
	if r.ShouldRun != nil && !r.ShouldRun(c.Path) {
		why := "is not enabled"
		if r.HoldReason != nil {
			if w := r.HoldReason(c.Path); w != "" {
				why = w
			}
		}
		return nil, fmt.Errorf("component %s %s", c.Path, why)
	}
	return r.ensureState(ctx, c, r.stateOf(c.Path, dep))
}

// ensureState is the single flight on one deployment's state s: its healthy
// generation, its sticky error until a change, or the build this caller
// takes (runCurrent), re-checked after every build.
func (r *Runner) ensureState(ctx context.Context, c *registry.Component, s *state) (*instance, error) {
	for {
		s.mu.Lock()
		s.lastReq = r.now()
		if !s.dirty && s.cur != nil {
			inst := s.cur
			s.mu.Unlock()
			return inst, nil
		}
		if !s.dirty && s.lastErr != nil {
			err := s.lastErr
			s.mu.Unlock()
			return nil, err
		}
		if s.building {
			done := s.buildDone
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		// We take the build.
		s.building = true
		s.dirty = false
		s.buildDone = make(chan struct{})
		s.mu.Unlock()

		err := r.runCurrent(c, s)

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
func (r *Runner) Track(comp string) func() { return r.track(r.state(comp)) }

// track marks one in-flight connection to s's deployment until the release.
func (r *Runner) track(s *state) func() {
	s.mu.Lock()
	s.active++
	s.lastReq = r.now()
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.active--
			s.lastReq = r.now()
			s.mu.Unlock()
		})
	}
}

// Changed marks a component dirty and kicks a background rebuild so build
// errors surface on save, not on next request.
func (r *Runner) Changed(c *registry.Component) {
	if !c.HasBackend() {
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

// runCurrent runs one generation transition of s's deployment of c onto the
// code its record names now (D119e): every restart path — a lazy start, a
// crash, a reap, a grant, alwaysOn, an xbind restart — reaches a build
// through here, so a pinned deployment never runs its work tree. A record
// that can't answer fails the start (06-security C7). Without a plane there
// is no record, and the primary follows the work tree.
func (r *Runner) runCurrent(c *registry.Component, s *state) error {
	code, err := r.recordCode(c.Path, s.dep)
	if err != nil {
		r.emit(c.Path, s.dep, "build-start", "")
		r.emit(c.Path, s.dep, "build-error", err.Error())
		return err
	}
	return r.buildAndStart(c, s, code)
}

// buildAndStart runs one generation transition of s's deployment onto code.
// Called single-flight per state. The generation spawns from code's view (c
// itself for the primary's work tree) and its artifact, kept per checkpoint
// (resolveGenFor, inspect.go).
func (r *Runner) buildAndStart(c *registry.Component, s *state, code Code) error {
	dep := s.dep
	r.emit(c.Path, dep, "build-start", "")

	g, err := r.resolveGenFor(c, dep, code)
	if err != nil {
		r.emit(c.Path, dep, "build-error", err.Error())
		return err
	}
	v, bin := g.view, g.bin

	s.mu.Lock()
	s.gen++
	gen := s.gen
	old := s.cur
	first := old != nil && r.stopFirst(v) // vm.go: no two guests on one sqlite
	if first {
		s.cur = nil
	}
	s.mu.Unlock()
	if first {
		r.stopGen(old, drainDeadline)
		old = nil
	}

	inst, err := r.startFor(v, dep, bin, gen)
	if err != nil {
		g.release()
		r.emit(c.Path, dep, "build-error", err.Error())
		return err
	}
	inst.code, inst.root, inst.artifact = code, g.root, g.artifact
	if err := r.awaitHealthy(v, inst); err != nil {
		if !errors.Is(err, errExited) && r.wantsVM(v) {
			r.dumpVM(inst) // vm.go: what the guest was doing, into the log
			r.sbxFail(v, sbx.Health, fmt.Errorf("the VM backend never listened: %w — what the VM was doing is in %s", err, deploymentLog(v.Path, r.viewDeployment(v))))
		}
		r.stopGen(inst, 2*time.Second)
		g.release()
		err = fmt.Errorf("backend did not become healthy: %w", err)
		r.emit(c.Path, dep, "build-error", err.Error())
		return err
	}
	if code.WorkTree && r.workTreeLeft(c.Path, dep) {
		// Live reload left the deployment while its work tree built (a pause
		// committed): what this generation read may postdate the pause's
		// checkpoint, so it never serves (D174). The current generation
		// serves until the deploy the pause queued swaps the checkpoint in;
		// without one, the next request builds what the record names.
		r.stopGen(inst, 2*time.Second)
		g.release()
		return nil
	}

	if !r.install(s, inst, false) {
		g.release()
		return util.NoDeployment(c.Path, dep) // removed while it built
	}
	if old != nil {
		go r.stopGen(old, drainDeadline)
	}

	// Crash watch: if the healthy process dies without being replaced, mark
	// the state so the next request rebuilds (and break crash loops).
	go func() {
		<-inst.waitCh
		g.release() // it binds its tree and runs its artifact no more
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.cur != inst {
			return // replaced normally
		}
		s.cur = nil
		s.crashes = append(s.crashes, r.now())
		recent := 0
		for _, t := range s.crashes {
			if r.now().Sub(t) < crashWindow*time.Duration(crashLimit) {
				recent++
			}
		}
		if recent >= crashLimit { // a save never reaches pinned code (07-runtime §7)
			s.lastErr = crashLoopError(c.Path, dep, code, recent)
			r.emit(c.Path, dep, "build-error", s.lastErr.Error())
		} else {
			s.dirty = true           // transparent restart on next request
			go r.afterExitOf(c, dep) // alwaysOn: each deployment's own (alwayson.go)
		}
	}()

	r.emit(c.Path, dep, "build-ok", "")
	slog.Info("backend up", "component", c.Path, "gen", gen)
	return nil
}

// start starts generation gen of the deployment view c names (the engine's
// startGen falls back to it).
func (r *Runner) start(c *registry.Component, bin string, gen int) (*instance, error) {
	return r.startDeployment(c, r.viewDeployment(c), bin, gen)
}

// startDeployment starts generation gen of deployment dep from bin, its view
// c: with its own run dir, log, env and instance token (spawnSetup).
func (r *Runner) startDeployment(c *registry.Component, dep, bin string, gen int) (*instance, error) {
	sp, err := r.spawnSetup(c, dep, gen)
	if err != nil {
		return nil, err
	}
	dir, sock, token, env := sp.dir, sp.sock, sp.token, sp.env

	var cmd *exec.Cmd
	var sb *sandbox.Handle
	var pol sandbox.EgressPolicy
	var netWhy string // why the net verdict withheld egress (netmux.go)
	cleanup := func() {}
	if r.Isolate && sandboxable(c.Manifest.Runtime) {
		pol, netWhy = r.spawnEgress(c)
		// Build the component's env layer (setup deps) if declared, then stack it.
		envLower, err := r.ensureEnvLayer(c)
		if err != nil {
			return nil, err
		}
		cmd, sb, err = r.sandboxCmd(c, bin, dir, sock, env, pol, envLower)
		if err != nil {
			r.vmRelease(sock)
			r.sbxFail(c, sbx.Start, err)
			return nil, fmt.Errorf("sandbox: %w", err)
		}
		cleanup = sb.Cleanup
	} else {
		if c.CodeRoot != "" { // no mount namespace shows a checkpoint at c.Dir (D119h)
			return nil, fmt.Errorf("%s: a checkpoint runs only in a sandbox (--isolate)", c.Path)
		}
		if dep != util.MainDeployment || c.Deployment != "" { // nor binds its own data at its paths (D119h)
			return nil, fmt.Errorf("%s: deployment %s runs only in a sandbox (--isolate)", c.Path, dep)
		}
		switch c.Manifest.Runtime { // exec-ok (all three): isolation off — the workspace has no sandbox; SpawnUser may drop to a scope uid
		case "go":
			cmd = exec.Command(bin) // exec-ok: see above
		case "node":
			cmd = exec.Command("node", bin) // exec-ok: see above
		case "python":
			cmd = exec.Command("python3", bin) // exec-ok: see above
		}
		cmd.Dir = c.Dir
		cmd.Env = env
		if r.SpawnUser != nil {
			if cred := r.SpawnUser(c); cred != nil {
				cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
			}
		}
	}

	logf, err := os.OpenFile(sp.log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(logf, "--- gen %d start %s ---\n", gen, time.Now().Format(time.RFC3339))
	logVerdict(logf, netWhy)
	cmd.Stdout, cmd.Stderr = logf, logf

	if err := cmd.Start(); err != nil {
		logf.Close()
		cleanup()
		r.vmRelease(sock)
		r.sbxFail(c, sbx.Start, err)
		return nil, fmt.Errorf("start backend: %w", err)
	}
	// limits.go: flat while main runs alone; the registry lists the leaf the
	// generation is placed in
	leaf := r.chooseLeaf(c.Path, dep)
	mode, unlist := r.modeOf(c, sock), r.sbxAddLeaf(c, gen, sock, cmd.Process.Pid, r.listedLeaf(leaf))
	r.registerInstance(token, c.Path, dep)
	r.joinLeaf(c.Path, dep, leaf, sock, cmd.Process.Pid)
	// Range-uid sandbox: map the child's uids and release its init (which is
	// blocked waiting) before anything reads back from it (e.g. the TUN fd).
	if err := sb.SetupUserns(); err != nil {
		fmt.Fprintf(logf, "userns setup: %v\n", err)
	}

	inst := &instance{gen: gen, sock: sock, token: token, cmd: cmd, started: time.Now(), egress: pol.Strings(), waitCh: make(chan struct{}), leaf: leaf}

	// Network setup: the init handed back its TUN fd(s) — egress first, then one
	// per provider client-link, then this component's own lan-ingress legs. The
	// egress is either spliced to a provider tile (this component is a client of
	// it) or run through the userspace relay.
	if sb.NeedsRelay() {
		np := r.netPlanFor(c, dep, logf) // netmux.go: a non-primary view gets no primary-only wiring (D127o)
		if fd, err := sb.RecvTUN(); err != nil {
			fmt.Fprintf(logf, "egress tun: %v (egress disabled)\n", err)
		} else if np.spliced {
			r.ensureProvider(np.provider) // provider must be up so its links are registered
			if pfd, ok := r.netmux.get(np.provider, c.Path); ok {
				inst.splicer = relay.Splice(fd, pfd)
				inst.provider = np.provider
			} else {
				_ = syscall.Close(fd)
				fmt.Fprintf(logf, "net provider %s link not ready — no egress\n", np.provider)
			}
		} else {
			cfg := relay.Config{TunFD: fd, CloseTUN: true, Allow: pol.Allow, Resolver: sandbox.HostResolver()}
			if pol.HasHostRules() {
				cfg.AllowHost = pol.AllowsHost // DNS-pinned hostname egress (D35)
			}
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
			if len(np.fwd) > 0 {
				cfg.Gateway = netip.MustParseAddr(sandbox.GatewayIP)
				cfg.HostFwd = np.fwd
				cfg.HostDial = np.dial // ingress.go: checked at each dial, as the generation's deployment
			}
			if rl, err := relay.Start(cfg); err != nil {
				fmt.Fprintf(logf, "egress relay: %v (egress disabled)\n", err)
			} else {
				inst.relay = rl
			}
		}
		// Provider tile: receive one TUN per client link and register it.
		for _, cl := range np.clients {
			if fd, err := sb.RecvTUN(); err != nil {
				fmt.Fprintf(logf, "client link %s: %v\n", cl.Name, err)
			} else {
				r.netmux.register(c.Path, cl.Name, fd)
			}
		}
		// Lan-ingress legs: splice each to the provider's matching client link
		// (registered under "<client>#<slot>" in its roster).
		for _, ll := range np.links {
			fd, err := sb.RecvTUN()
			if err != nil {
				fmt.Fprintf(logf, "lan-ingress link %s: %v\n", ll.Slot, err)
				continue
			}
			r.ensureProvider(ll.Provider)
			if pfd, ok := r.netmux.get(ll.Provider, c.Path+"#"+ll.Slot); ok {
				inst.linkSplicers = append(inst.linkSplicers, relay.Splice(fd, pfd))
			} else {
				_ = syscall.Close(fd)
				fmt.Fprintf(logf, "lan-ingress provider %s link not ready for %s\n", ll.Provider, ll.Slot)
			}
		}
		if len(np.clients) > 0 {
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
						r.ChangedTile(cc)
					}
				}
			}(np.clients)
		}
	}

	go func() {
		_ = cmd.Wait()
		unlist()
		r.sbxExited(c, mode, cmd.ProcessState, inst.started)
		if inst.relay != nil {
			inst.relay.Close()
		}
		if inst.splicer != nil {
			inst.splicer.Close() // stop the L3 splice (leaves the provider link open)
		}
		for _, s := range inst.linkSplicers {
			s.Close()
		}
		r.leaveLeaf(leaf) // its own deployment's leaf alone (limits.go)
		cleanup()         // remove the sandbox spec temp file (init self-removes; this is a backstop)
		r.vmRelease(sock)
		logf.Close()
		r.Auth.RevokeInstance(token)
		close(inst.waitCh)
	}()
	return inst, nil
}
