package boot

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	goruntime "runtime"
	"time"

	"github.com/xbin-dev/xbin"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/builtins"
	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/deps"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/gpu"
	ingressPkg "github.com/xbin-dev/xbin/internal/ingress"
	"github.com/xbin-dev/xbin/internal/proxy"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/internal/watch"
)

// watchDebounce coalesces editor save bursts (plans/implementation.md phase 1).
const watchDebounce = 300 * time.Millisecond

// State is everything a boot builds up, step by step, and what serves once
// the steps are done. Exported fields are what the runtime API and tests
// read; the rest is plumbing between steps.
type State struct {
	Cfg     *Config
	WS      string
	Auth    *auth.Auth
	Users   *users.Store
	Reg     *registry.Registry
	Hub     *events.Hub
	Run     *runner.Runner
	Term    *term.Manager
	Broker  *broker.Broker
	Proxy   *proxy.Proxy
	Server  *server.Server
	Started time.Time

	trusted          []netip.Prefix
	externalURL      string
	overlay          string
	webFS, docsFS    fs.FS
	bxDir, baseURL   string
	streams          *ingressPkg.Streams
	forwards         *ingressPkg.Forwards
	reconcileIngress func()
	watcher          *watch.Watcher
	priv             Privileges
}

// Step is one named stage of a boot. Steps run in list order; the order is
// load-bearing (see the edges noted on each) and asserted by a test.
type Step struct {
	Name string
	Run  func(*State) error
}

// Steps is the boot sequence. Edges that matter:
//   - workspace before privileges: root seeds AGENTS.md/CLAUDE.md into a
//     workspace it may not own afterwards.
//   - auth+users before homes: the legacy home/ migration's target is the
//     sole user (or sole admin) the store knows.
//   - registry before broker: the essential-tile backfill rescans the
//     registry and then creates per-component repos.
//   - broker before vault/proxy/ingress/isolation/server: they wire into it.
//   - server last before watch/serve: every handler is registered by then.
var Steps = []Step{
	{"workspace", (*State).stepWorkspace},
	{"privileges", (*State).stepPrivileges},
	{"auth+users", (*State).stepAuthUsers},
	{"homes", (*State).stepHomes},
	{"registry", (*State).stepRegistry},
	{"terminals", (*State).stepTerminals},
	{"assets", (*State).stepAssets},
	{"broker", (*State).stepBroker},
	{"limit-alerts", (*State).stepLimitAlerts},
	{"vault", (*State).stepVault},
	{"proxy", (*State).stepProxy},
	{"ingress", (*State).stepIngress},
	{"cgroup", (*State).stepCgroup},
	{"isolation", (*State).stepIsolation},
	{"server", (*State).stepServer},
	{"watch", (*State).stepWatch},
}

// Run boots the workspace described by cfg and serves it until ctx is done
// (a signal, in main; the test's cancel). Every failure is an error — no
// os.Exit — and the console listener is cfg.Listener when given.
func Run(ctx context.Context, cfg *Config) error {
	d, err := cfg.Validate()
	if err != nil {
		return err
	}
	st := &State{Cfg: cfg, WS: d.ws, trusted: d.trusted, externalURL: d.externalURL, overlay: d.overlay,
		Started: time.Now(), priv: cfg.Privileges}
	if st.priv == nil {
		st.priv = OSPrivileges{}
	}
	if os.Getpid() == 1 {
		go reapZombies()
	}
	for _, s := range Steps {
		if err := s.Run(st); err != nil {
			return fmt.Errorf("%s: %w", s.Name, err)
		}
	}
	return st.serve(ctx)
}

// ---- the steps, in order ----

// Auto-init an empty workspace mount (deployment.md §install), then backfill
// infrastructure files into workspaces created by older xbind. Only this:
// unlike app content, its absence is never deliberate — agents depend on
// AGENTS.md. (Home dotfiles are no longer backfilled here: homes are per-user
// and seeded lazily on first terminal, tm.SeedHome below.)
func (st *State) stepWorkspace() error {
	ws := st.WS
	if _, err := os.Stat(filepath.Join(ws, "xbin.json")); err != nil {
		slog.Info("initializing workspace", "dir", ws)
		if err := InitWorkspace(ws); err != nil {
			return fmt.Errorf("auto-init: %w", err)
		}
	}
	for _, f := range []string{"AGENTS.md"} {
		if err := seedTemplateFile(ws, f); err != nil {
			slog.Warn("backfill", "file", f, "err", err)
		}
	}
	if _, err := os.Lstat(filepath.Join(ws, "CLAUDE.md")); err != nil {
		_ = os.Symlink("AGENTS.md", filepath.Join(ws, "CLAUDE.md"))
	}
	return nil
}

// D13(b): started as root on a workspace owned by someone else → become
// that user (unless tier-2 scope uids need us to stay root).
func (st *State) stepPrivileges() error {
	if st.priv.Euid() == 0 && !st.Cfg.ScopeUIDs {
		return st.priv.DropToOwner(st.WS)
	}
	return nil
}

func (st *State) stepAuthUsers() error {
	a, err := auth.Load(st.WS, st.Cfg.NoAuth)
	if err != nil {
		return err
	}
	// Human users (plans/multi-user.md). No users configured ⇒ single-user
	// mode: the root token is the only principal.
	userStore, err := users.Open(filepath.Join(st.WS, "data"))
	if err != nil {
		return err
	}
	a.SetUsers(userStore)
	if n := userStore.Count(); n > 0 {
		slog.Info("multi-user mode", "users", n)
	}
	// Dev convenience: with auth on and no users yet, seed a known admin so
	// you can sign in immediately and iterate on the multi-user UI. DEV ONLY
	// (gated on --dev, which never runs in production).
	if st.Cfg.Dev && !st.Cfg.NoAuth && userStore.Count() == 0 {
		if _, err := userStore.Upsert(users.User{
			ID: "admin", Name: "Dev Admin", Role: users.RoleAdmin,
		}, "admin"); err == nil {
			slog.Warn("dev: seeded admin user — login 'admin' / 'admin' — DEV ONLY, never expose")
		}
	}
	st.Auth, st.Users = a, userStore
	return nil
}

// Per-user terminal homes (decision D6, amended): migrate a legacy shared
// home/ to homes/<user>. The target is the workspace's one human when that's
// unambiguous (the sole user, else the sole admin), otherwise the token
// principal's "owner" — reassign later with a plain `mv homes/owner
// homes/<user>`. Bails (refusing to start) only when BOTH forms hold real
// data, so nothing is ever merged by guesswork.
func (st *State) stepHomes() error {
	homeTarget := "owner"
	if all := st.Users.List(); len(all) == 1 {
		homeTarget = all[0].ID
	} else if len(all) > 1 {
		var admins []string
		for _, u := range all {
			if u.IsAdmin() {
				admins = append(admins, u.ID)
			}
		}
		if len(admins) == 1 {
			homeTarget = admins[0]
		} else {
			slog.Warn("home migration: several admin users — a legacy home/ (if any) becomes homes/owner; reassign with `mv homes/owner homes/<user>`")
		}
	}
	if moved, err := term.MigrateHomes(st.WS, homeTarget, pristineHomeFile); err != nil {
		return fmt.Errorf("per-user home migration: %w", err)
	} else if moved != "" {
		slog.Info("migrated legacy shared home/ to a per-user home", "to", moved)
	}
	return nil
}

func (st *State) stepRegistry() error {
	reg, err := registry.Open(st.WS)
	if err != nil {
		return err
	}
	st.Reg = reg
	st.Hub = events.NewHub()
	st.Run = runner.New(st.WS, st.Auth, st.Hub, reg)
	// Materialize deps/ symlinks and the generated go.work (phase 3).
	for _, p := range deps.Reconcile(reg) {
		slog.Warn("deps", "problem", p)
	}
	if err := deps.GoWork(reg, deps.SDKPath()); err != nil {
		slog.Warn("go.work", "err", err)
	}
	return nil
}

func (st *State) stepTerminals() error {
	ws, reg := st.WS, st.Reg
	st.baseURL = "http://" + st.Cfg.Listen
	// Make `bx` runnable in terminals. In the container it's already on PATH
	// (/opt/xbin/bin, Dockerfile); in dev/host mode it usually isn't, so we
	// prepend the directory holding the bx binary.
	st.bxDir = locateBx(st.Cfg.Bin, st.Cfg.Dev)
	if st.bxDir == "" {
		slog.Warn("bx CLI not found; terminals won't have it on PATH (build it: go build -o bin/bx ./cmd/bx, or set XBIN_BIN)")
	}
	bxDir, baseURL := st.bxDir, st.baseURL
	tm := term.NewManager(ws, func() []string {
		// HOME and XBIN_TOKEN are per-session (per-user home; tile-scoped
		// terminal token, plans/terminal-tokens.md) — the term manager sets
		// them; everything here is session-independent. The owner token never
		// enters a terminal.
		env := []string{
			"XBIN_URL=" + baseURL,
			"XBIN_WORKSPACE=" + ws,
			"XBIN_DOCS=" + filepath.Join(ws, ".xbin", "docs"), // builder docs, on disk
		}
		if bxDir != "" {
			env = append(env, "PATH="+bxDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
		return env
	})
	tm.Listen = st.Cfg.Listen      // for the internet-scope relay host-forward to xbind
	tm.SeedHome = seedHomeSkeleton // .zshrc/.bashrc/… into a fresh per-user home
	tm.Tokens = st.Auth            // per-session tile-scoped terminal tokens (plans/terminal-tokens.md)
	// D17a: a non-admin's terminal masks out the source of every tile below
	// their read level — the mount-level half of the same visibility rule the
	// tile list applies (chrome isn't a registry component, so no exception
	// needed here).
	tm.HiddenTiles = func(p auth.Principal) []string {
		var hide []string
		for _, c := range reg.Components() {
			if !p.CanReadTile(c.Path) {
				hide = append(hide, c.Path)
			}
		}
		return hide
	}
	// D40: restricted terminals mount an ALLOW-LIST view — only readable
	// components are bound, and the workspace root files are replaced with
	// redacted copies (xbin.json filtered to readable rows — the full file
	// is the whole grants/bindings topology incl. public hostnames; go.work
	// covering only readable modules so builds don't chase absent dirs).
	tm.TermView = func(p auth.Principal) ([]string, map[string][]byte) {
		var readable []string
		for _, c := range reg.Components() {
			if p.CanReadTile(c.Path) {
				readable = append(readable, c.Path)
			}
		}
		canRead := func(path string) bool { return p.CanReadTile(path) }
		files := map[string][]byte{
			"xbin.json": registry.RedactedManifestJSON(reg.Workspace(), canRead),
		}
		if gw := deps.GoWorkFor(reg, deps.SDKPath(), canRead); gw != "" {
			files["go.work"] = []byte(gw)
		}
		for _, name := range []string{"AGENTS.md", ".gitignore"} {
			if b, err := os.ReadFile(filepath.Join(reg.Root, name)); err == nil {
				files[name] = b
			}
		}
		return readable, files
	}
	st.Term = tm
	return nil
}

func (st *State) stepAssets() error {
	st.webFS, st.docsFS = xbin.WebFS(), xbin.DocsFS()
	if st.Cfg.Dev {
		if src := devSourceDir(); src != "" {
			slog.Info("dev mode: serving web/ and docs/ from disk", "dir", src)
			st.webFS = os.DirFS(filepath.Join(src, "web"))
			st.docsFS = os.DirFS(filepath.Join(src, "docs"))
		}
	}
	// Materialize the builder docs on disk (XBIN_DOCS) so a terminal — sandboxed
	// or not — can read AGENTS.md's companions (elements.md, auth.md, …) as files.
	if err := extractFS(st.docsFS, filepath.Join(st.WS, ".xbin", "docs")); err != nil {
		slog.Warn("extract docs", "err", err)
	}
	return nil
}

// Broker: grants/RBAC policy, vault, resources (plans/auth.md).
func (st *State) stepBroker() error {
	ws, reg, userStore := st.WS, st.Reg, st.Users
	brk, err := broker.New(reg, st.Hub, st.Cfg.ScopeUIDs && st.priv.Euid() == 0)
	if err != nil {
		return err
	}
	// Embedded optional tile catalog (plans/tile-sharing.md).
	if set, err := builtins.Load(xbin.BuiltinTilesFS()); err != nil {
		slog.Warn("builtin tiles", "err", err)
	} else {
		brk.SetBuiltins(set)
	}
	// Embedded builtin template catalog (plans/templates.md).
	if set, err := builtins.LoadTemplates(xbin.BuiltinTemplatesFS()); err != nil {
		slog.Warn("builtin templates", "err", err)
	} else {
		brk.SetBuiltinTemplates(set)
	}
	// Materialize builtin templates as read-only git repos so instances can
	// carry a `template` remote and pull upstream fixes (plans/agent-v2.md).
	brk.MaterializeTemplateRepos(xbin.BuiltinTemplatesFS())
	// Builtin update tracking (plans/builtin-updates.md): offer newer embedded
	// scaffold/tiles to existing workspaces without trampling customizations.
	{
		tileSet, _ := builtins.Load(xbin.BuiltinTilesFS())
		updater := builtins.NewUpdater(reg.Root, tileSet, xbin.TemplateFS())
		brk.SetUpdater(updater)
		// Backfill ESSENTIAL builtin tiles into workspaces created before they
		// existed (newer chrome targets them — the shell's ⚑ opens
		// tiles/organisations). Ledgered in data/backfills.json: a deliberate
		// delete sticks across restarts. On first-seen PRESENT units the
		// ledger records them untouched, so fresh workspaces never reinstall.
		if installed, err := updater.BackfillEssentials(filepath.Join(ws, "data", "backfills.json")); err != nil {
			slog.Warn("essential-builtin backfill", "err", err)
		} else if len(installed) > 0 {
			slog.Info("backfilled essential builtin tiles", "tiles", installed)
			_ = reg.Rescan()
			// Members should be able to SEE the new chrome tile: extend the
			// workspace defaults only where the admin already runs a defaults
			// regime (fresh-store seeding is untouched; empty defaults on an
			// old workspace stay empty — access changes are the admin's call).
			if dt := userStore.DefaultTiles(); len(dt) > 0 {
				changed := false
				for _, t := range installed {
					if _, ok := dt[t]; !ok {
						dt[t] = users.LevelRead
						changed = true
					}
				}
				if changed {
					if err := userStore.SetDefaultTiles(dt); err != nil {
						slog.Warn("backfill defaults", "err", err)
					}
				}
			}
		}
	}
	// After a broker-driven structure change (tile import), reconcile deps and
	// regenerate go.work immediately so the new tile is usable at once.
	brk.OnStructureChange = func() {
		deps.Reconcile(reg)
		if err := deps.GoWork(reg, deps.SDKPath()); err != nil {
			slog.Warn("go.work", "err", err)
		}
		brk.EnsureComponentRepos() // new/imported components get their own git repo
	}
	brk.EnsureComponentRepos() // migrate existing components to per-component repos
	brk.Users = userStore
	// D54: a terminal's network on an org-owned tile is the org's network
	// sets; the broker knows ownership + sets, the term manager asks.
	st.Term.TermNet = brk.TermNetFor
	brk.ExternalURL = st.externalURL
	st.Broker = brk
	return nil
}

// Fold cgroup at-limit events into the workspace alerts: a tile that keeps
// hitting its memory or pids cap surfaces in the admin console / shell.
// Delta-tracked so a one-off blip clears once the tile settles.
func (st *State) stepLimitAlerts() error {
	run, reg, brk := st.Run, st.Reg, st.Broker
	if run.Cgroup != nil && run.Cgroup.Enabled() {
		lastMem, lastPids := map[string]int64{}, map[string]int64{}
		brk.SetLimitAlerts(func() []broker.Alert {
			var out []broker.Alert
			for _, c := range reg.Components() {
				key := util.CompKey(c.Path)
				mem, pids, ok := run.Cgroup.AtLimit(key)
				if !ok {
					continue
				}
				if mem > lastMem[key] {
					out = append(out, broker.Alert{Level: "warn", Kind: "oom", Tile: c.Path,
						Message: c.Path + " hit its memory limit (was OOM-killed) — it may be leaking or under-provisioned"})
				}
				if pids > lastPids[key] {
					out = append(out, broker.Alert{Level: "warn", Kind: "pids", Tile: c.Path,
						Message: c.Path + " hit its process (pids) limit — a runaway fork/spawn?"})
				}
				lastMem[key], lastPids[key] = mem, pids
			}
			return out
		})
	}
	return nil
}

// Vault encryption barrier (docs/auth.md §vault, plans/vault-data.md); the
// mode is ResolveVaultMode's.
func (st *State) stepVault() error {
	brk, cfg := st.Broker, st.Cfg
	brk.AllowInsecureVault = cfg.InsecureVault || cfg.NoAuth
	switch ResolveVaultMode(cfg, brk.Barrier().Initialized()) {
	case VaultFromEnv:
		if err := brk.UnsealOrInit(cfg.VaultPassphrase); err != nil {
			return fmt.Errorf("vault unseal: %w", err)
		}
		slog.Info("vault: encryption at rest active (auto-unsealed from env)")
	case VaultPlaintext:
		slog.Warn("vault: NO encryption at rest — data is plaintext on disk (--insecure-vault/--no-auth). Set XBIN_VAULT_PASSPHRASE or drop the flag to encrypt")
	case VaultDevKey:
		// Dev convenience: init on first run, unseal on later runs, with a fixed
		// in-source key (INSECURE — dev only), so encryption-at-rest is exercised
		// without extra setup.
		if err := brk.UnsealOrInit(devVaultPassphrase); err != nil {
			return fmt.Errorf("vault dev-init: %w", err)
		}
		slog.Warn("vault: encryption at rest active with a built-in DEV key (INSECURE — dev only; use XBIN_VAULT_PASSPHRASE or manual unseal in production)")
	case VaultSealed:
		slog.Warn("vault: encrypted and SEALED — an admin must unseal it after login (bx vault unseal, or the admin console); secret reads/writes fail until then")
	default:
		slog.Warn("vault: LOCKED — no encryption configured. An admin sets it up after login (bx vault unseal, or the admin console); secret storage is refused until then")
	}
	return nil
}

func (st *State) stepProxy() error {
	reg, run, hub, brk, userStore := st.Reg, st.Run, st.Hub, st.Broker, st.Users
	px := &proxy.Proxy{Reg: reg, Runner: run, Hub: hub, Policy: brk.Policy}
	// D29: backends get the driving user attributed (X-XBin-User[-Level]).
	px.UserLevel = func(uid, tile string) string {
		acc, ok := userStore.Access(uid)
		if !ok {
			return ""
		}
		return acc.TileLevel(tile)
	}
	brk.SetDispatch(broker.DispatchViaProxy(px))
	run.EnvForComponent = brk.EnvFor
	// Approving a net:*/res:*/gpu:* grant restarts the caller so the new egress
	// policy / resource env / GPU devices (all captured at spawn) take effect now.
	brk.OnGrantChange = func(comp string) {
		if c, ok := reg.Component(comp); ok {
			run.Changed(c)
		}
	}
	brk.StopBackend = run.Stop // lifecycle: disabling stops the backend now
	// A component may spawn only if enabled AND its encrypted tile state is
	// currently accessible (vault unsealed + mounts up) — see plans/vault-data.md.
	run.ShouldRun = func(comp string) bool {
		return reg.LifecycleState(comp) == registry.StateEnabled && !brk.EncryptionHold(comp)
	}
	brk.Version = st.Cfg.Version
	brk.ProxyHandler = px // internal archiver calls for backup/restore
	st.Proxy = px
	return nil
}

// Ingress (plans/ingress.md): the L4 stream relay + per-terminator forward
// doors, and their broker/runner wiring. The managers reconcile against
// broker-computed state on boot, binding changes, and rescans.
func (st *State) stepIngress() error {
	run, brk, px := st.Run, st.Broker, st.Proxy
	strm := &ingressPkg.Streams{Dial: run.DialInto}
	fwds := &ingressPkg.Forwards{
		Dir: run.RunDir,
		Handler: func(source string) http.Handler {
			return &ingressPkg.HTTPHandler{
				Source: source, Lookup: brk.IngressLookup,
				Forward: func(w http.ResponseWriter, r *http.Request, rt ingressPkg.Route) {
					px.ForwardIngress(w, r, rt, true)
				},
			}
		},
	}
	brk.IngressSocket = fwds.SocketPath
	brk.DialStream = run.DialInto
	st.reconcileIngress = func() {
		strm.Reconcile(brk.IngressStreamSpecs())
		fwds.Reconcile(brk.IngressSources())
	}
	brk.OnIngressChange = st.reconcileIngress
	run.IngressNet = brk.IngressNetFor
	run.IngressFwd = brk.IngressFwdFor
	run.NetLinks = brk.NetLinksFor
	run.Published = brk.PublishedHost
	run.HairpinDial = brk.HairpinDial
	if st.Cfg.ScopeUIDs && st.priv.Euid() == 0 {
		run.SpawnUser = brk.SpawnUser
	}
	st.streams, st.forwards = strm, fwds
	return nil
}

// Per-component cgroup v2 limits + accounting (memory/CPU/pids), best-effort
// — active only when xbind's cgroup is delegated (systemd Delegate=yes / a
// container). A runaway tile then OOMs / throttles / can't fork *alone*.
func (st *State) stepCgroup() error {
	if cg := cgroup.New(); cg.Enabled() {
		memMax := parseBytes("XBIN_LIMIT_MEM", st.Cfg.LimitMem, 2<<30) // 2 GiB
		cg.SetLimits(cgroup.Limits{
			MemMax:    memMax,
			PidsMax:   int64(max(512, goruntime.NumCPU()*8)), // fork-bomb ceiling
			CPUWeight: 100,                                   // fair share; burst when idle
		})
		st.Run.Cgroup = cg
		st.Term.Cgroup = cg // restricted (non-admin) terminals get the same caps (D17d)
		slog.Info("cgroup v2 limits enabled", "memMax", 2<<30, "pidsMax", max(512, goruntime.NumCPU()*8))
	}
	return nil
}

// Isolation is orthogonal to --dev/--no-auth (which only change asset serving
// and logging): the sandbox network/fs model is different enough that dev
// should run against it too (`make dev`).
func (st *State) stepIsolation() error {
	cfg, run, tm, brk := st.Cfg, st.Run, st.Term, st.Broker
	if !cfg.Isolate {
		return nil
	}
	if cfg.Rootfs == "" {
		return fmt.Errorf("--isolate needs --rootfs <dir> (an unpacked base OCI rootfs; `make rootfs`)")
	}
	if !sandbox.Available() {
		return fmt.Errorf("--isolate: unprivileged user namespaces unavailable on this host")
	}
	abs, err := filepath.Abs(cfg.Rootfs)
	if err != nil || !dirExists(abs) {
		return fmt.Errorf("--isolate: rootfs %q not found", cfg.Rootfs)
	}
	run.Rootfs = abs
	run.Isolate = true
	run.Egress = brk.EgressFor
	run.GPU = brk.GPUFor
	run.NetRoster = brk.NetProviderRoster
	run.NetTarget = brk.NetClientTarget
	run.NetHost = brk.NetHostShare
	run.NetCaps = brk.NetAdminFor         // cap:net-admin → keep net-admin caps
	run.ContainerCaps = brk.ContainersFor // cap:containers → keep userns caps for rootless podman
	if inv := gpu.Inventory(); len(inv) > 0 {
		slog.Info("NVIDIA GPUs available for gpu:* grants", "count", len(inv))
	}
	// Terminals share the base rootfs too (RT-4): the workspace is bound rw
	// (editing plane), plus the SDK source ro so `go build` resolves.
	tm.Isolate = true
	tm.Rootfs = abs
	// Safety gate: never stack an existing terminal upper on a base image
	// different from the one it was built on (corrupts apt/dpkg state). Abort
	// if a pinned base is missing — the base upgrade must preserve old bases.
	if err := tm.CheckBaseImages(); err != nil {
		return err
	}
	tm.GCBaseImages() // release preserved bases no terminal pins anymore
	// Same locator as go.work generation (XBIN_SDK_PATH → /opt/xbin/sdk).
	// Never fall back to "": filepath.Abs("") is the daemon's cwd (the
	// install prefix in prod), and binding that read-only over the sandbox
	// shadowed the rw $HOME/component mounts beneath it.
	if p := deps.SDKPath(); p != "" {
		if sdk, err := filepath.Abs(p); err == nil && dirExists(sdk) {
			tm.ExtraBinds = append(tm.ExtraBinds, sandbox.Bind{Src: sdk, Dst: sdk, RO: true})
		}
	}
	slog.Info("per-component isolation enabled (tier 3)", "rootfs", abs)
	// Sandboxes need a delegated sub-uid/gid RANGE for apt/dpkg to chown
	// files to the system users their post-install scripts create. Without
	// it the sandbox falls back to single-uid mode (only container-root
	// mapped), where those chowns fail with EINVAL and heavier package
	// installs break midway (systemd, dbus, …) while simple ones still work.
	// Warn loudly — the failure is otherwise a cryptic dpkg error.
	if rangeOK, reason := sandbox.IDMapStatus(os.Getuid(), os.Getgid()); rangeOK {
		slog.Info("sandbox uid mapping: full sub-id range (apt/dpkg system-user installs work)")
	} else {
		slog.Warn("sandbox uid mapping: SINGLE-UID fallback — apt/dpkg installs that create system users (systemd, dbus, …) will fail with chown \"Invalid argument\"; delegate a sub-id range to this user and install the uidmap package (deploy/install.sh does both), then restart xbind",
			"reason", reason)
	}
	return nil
}

func (st *State) stepServer() error {
	srv := &server.Server{
		Reg: st.Reg, Auth: st.Auth, Hub: st.Hub, Term: st.Term,
		WebFS: st.webFS, DocsFS: st.docsFS,
		ComponentAPI: st.Proxy, Version: st.Cfg.Version,
		TrustedProxies: st.trusted,
		ExternalURL:    st.externalURL,
		Overlay:        st.overlay,
	}
	if st.overlay != "" {
		slog.Info("dev overlay: /c/ files shadowed from disk (manifests excluded)", "dir", st.overlay)
	}
	// One client-IP resolver for everything: login throttle, session IP
	// attribution, and the /c/ warm-IP gate (all trusted-proxy aware).
	st.Auth.SetClientIP(srv.ClientIP)
	st.Broker.Register(srv)
	st.registerRuntimeAPI(srv)
	st.Server = srv
	return nil
}

// Watch → rescan, live reload, rebuilds.
func (st *State) stepWatch() error {
	w, err := watch.New(st.WS, watchDebounce)
	if err != nil {
		return err
	}
	st.watcher = w
	go watchLoop(w, st.Reg, st.Hub, st.Run, st.Broker, st.reconcileIngress)
	return nil
}
