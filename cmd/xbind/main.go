// xbind — the xbin workspace daemon. See plans/ for the design and /docs/
// (served by this binary) for the builder-facing documentation.
//
//	xbind init <dir>              scaffold a workspace
//	xbind [flags]                 serve a workspace
//	xbind version
package main

import (
	"bytes"
	"crypto/tls"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/magik6k/xbin"
	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/broker"
	"github.com/magik6k/xbin/internal/builtins"
	"github.com/magik6k/xbin/internal/cgroup"
	"github.com/magik6k/xbin/internal/deps"
	"github.com/magik6k/xbin/internal/events"
	"github.com/magik6k/xbin/internal/gpu"
	ingressPkg "github.com/magik6k/xbin/internal/ingress"
	"github.com/magik6k/xbin/internal/proxy"
	"github.com/magik6k/xbin/internal/registry"
	"github.com/magik6k/xbin/internal/runner"
	"github.com/magik6k/xbin/internal/sandbox"
	"github.com/magik6k/xbin/internal/server"
	"github.com/magik6k/xbin/internal/term"
	"github.com/magik6k/xbin/internal/users"
	"github.com/magik6k/xbin/internal/util"
	"github.com/magik6k/xbin/internal/watch"
)

var version = "dev" // set via -ldflags at release (make build → `git describe`)
var startTime = time.Now()

// buildVersion resolves the running binary's build id: the -ldflags value if
// one was baked in at build time, else the VCS revision Go stamps into the
// binary automatically (works for `go build`/`go run` from the repo), else
// "dev". This is what the UI shows as the xbind build commit.
func buildVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		var rev string
		var dirty bool
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if rev != "" {
			if len(rev) > 12 {
				rev = rev[:12]
			}
			if dirty {
				rev += "-dirty"
			}
			return rev
		}
	}
	return version
}

// devVaultPassphrase brings the vault up automatically under --dev/--no-auth so
// encryption-at-rest is ON by default while dogfooding (a bare `make dev`
// encrypts filesystem/sqlite/blob resources + kv). It is a FIXED key baked into
// the source — INSECURE by construction — and is never used outside dev/no-auth;
// real deployments supply XBIN_VAULT_PASSPHRASE or unseal manually.
const devVaultPassphrase = "xbin-dev-insecure-vault"

func kernelRelease() string {
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "__sandbox-init":
			// Hidden re-exec target: run as PID 1 inside a component's fresh
			// namespaces, assemble its rootfs, and exec the backend. Must be
			// handled before any flag parsing (plans/isolation-impl.md).
			if len(os.Args) < 3 {
				fatal("usage: xbind __sandbox-init <spec>")
			}
			sandbox.RunInit(os.Args[2]) // never returns on linux
			return
		case "init":
			if len(os.Args) < 3 {
				fatal("usage: xbind init <dir>")
			}
			if err := initWorkspace(os.Args[2]); err != nil {
				fatal("init: %v", err)
			}
			fmt.Println("workspace initialized:", os.Args[2])
			return
		case "version":
			fmt.Println("xbind", buildVersion())
			return
		}
	}

	var (
		wsFlag        = flag.String("workspace", envOr("XBIN_WORKSPACE", "/workspace"), "workspace directory")
		listen        = flag.String("listen", envOr("XBIN_LISTEN", "127.0.0.1:8642"), "listen address")
		dev           = flag.Bool("dev", false, "dev mode: web/docs served from source tree, debug logs")
		noAuth        = flag.Bool("no-auth", false, "disable auth (dev only; every request is admin)")
		scopeUIDs     = flag.Bool("scope-uids", false, "run each scope's backends under a dedicated uid (requires root; auth tier 2)")
		insecureVault = flag.Bool("insecure-vault", false, "store secrets AND resource data as PLAINTEXT at rest (not recommended; --no-auth implies it; a bare --dev instead auto-encrypts with a dev key)")
		isolate       = flag.Bool("isolate", false, "run each backend in a per-component sandbox (namespaces + overlay rootfs; auth tier 3, needs --rootfs)")
		rootfs        = flag.String("rootfs", envOr("XBIN_ROOTFS", ""), "base rootfs dir (unpacked OCI image) for --isolate sandboxes")
		ingressListen = flag.String("ingress-listen", envOr("XBIN_INGRESS_LISTEN", ""), "public ingress HTTP listener (plans/ingress.md; \"\" = off). Serves ONLY published tile routes — never the console")
		ingressCert   = flag.String("ingress-cert", envOr("XBIN_INGRESS_CERT", ""), "TLS certificate (PEM) for the ingress listener (with --ingress-key; reloaded on change)")
		ingressKey    = flag.String("ingress-key", envOr("XBIN_INGRESS_KEY", ""), "TLS key (PEM) for the ingress listener")
	)
	flag.Parse()

	ws, err := filepath.Abs(*wsFlag)
	if err != nil {
		fatal("%v", err)
	}
	// --dev serves web/docs from the source tree and turns on debug logs; it
	// no longer implies --no-auth, so `make dev` can exercise multi-user auth
	// while live-editing core elements. Use --no-auth explicitly (or
	// `make dev-noauth`) for the frictionless admin-everything mode.
	if err := serve(ws, *listen, *dev, *noAuth, *scopeUIDs, *insecureVault, *isolate, *rootfs,
		ingressOpts{Listen: *ingressListen, Cert: *ingressCert, Key: *ingressKey}); err != nil {
		fatal("%v", err)
	}
}

// ingressOpts is the builtin HTTP terminator's config (plans/ingress.md ING-3).
type ingressOpts struct{ Listen, Cert, Key string }

func serve(ws, listen string, dev, noAuth, scopeUIDs, insecureVault, isolate bool, rootfs string, ing ingressOpts) error {
	lvl := slog.LevelInfo
	if dev {
		lvl = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))

	if os.Getpid() == 1 {
		go reapZombies()
	}

	// Auto-init an empty workspace mount (deployment.md §install).
	if _, err := os.Stat(filepath.Join(ws, "xbin.json")); err != nil {
		slog.Info("initializing workspace", "dir", ws)
		if err := initWorkspace(ws); err != nil {
			return fmt.Errorf("auto-init: %w", err)
		}
	}
	// Backfill infrastructure files into workspaces created by older xbind.
	// Only this: unlike app content, its absence is never deliberate — agents
	// depend on AGENTS.md. (Home dotfiles are no longer backfilled here: homes
	// are per-user and seeded lazily on first terminal, tm.SeedHome below.)
	for _, f := range []string{"AGENTS.md"} {
		if err := seedTemplateFile(ws, f); err != nil {
			slog.Warn("backfill", "file", f, "err", err)
		}
	}
	if _, err := os.Lstat(filepath.Join(ws, "CLAUDE.md")); err != nil {
		_ = os.Symlink("AGENTS.md", filepath.Join(ws, "CLAUDE.md"))
	}

	// D13(b): started as root on a workspace owned by someone else → become
	// that user (unless tier-2 scope uids need us to stay root).
	if os.Geteuid() == 0 && !scopeUIDs {
		if err := dropToWorkspaceOwner(ws); err != nil {
			return err
		}
	}

	a, err := auth.Load(ws, noAuth)
	if err != nil {
		return err
	}
	// Human users (plans/multi-user.md). No users configured ⇒ single-user
	// mode: the root token is the only principal.
	userStore, err := users.Open(filepath.Join(ws, "data"))
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
	if dev && !noAuth && userStore.Count() == 0 {
		if _, err := userStore.Upsert(users.User{
			ID: "admin", Name: "Dev Admin", Role: users.RoleAdmin,
		}, "admin"); err == nil {
			slog.Warn("dev: seeded admin user — login 'admin' / 'admin' — DEV ONLY, never expose")
		}
	}
	// Per-user terminal homes (decision D6, amended): migrate a legacy shared
	// home/ to homes/<user>. The target is the workspace's one human when that's
	// unambiguous (the sole user, else the sole admin), otherwise the token
	// principal's "owner" — reassign later with a plain `mv homes/owner
	// homes/<user>`. Bails (refusing to start) only when BOTH forms hold real
	// data, so nothing is ever merged by guesswork.
	homeTarget := "owner"
	if all := userStore.List(); len(all) == 1 {
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
	if moved, err := term.MigrateHomes(ws, homeTarget, pristineHomeFile); err != nil {
		return fmt.Errorf("per-user home migration: %w", err)
	} else if moved != "" {
		slog.Info("migrated legacy shared home/ to a per-user home", "to", moved)
	}

	reg, err := registry.Open(ws)
	if err != nil {
		return err
	}
	hub := events.NewHub()
	run := runner.New(ws, a, hub, reg)

	// Materialize deps/ symlinks and the generated go.work (phase 3).
	for _, p := range deps.Reconcile(reg) {
		slog.Warn("deps", "problem", p)
	}
	if err := deps.GoWork(reg, deps.SDKPath()); err != nil {
		slog.Warn("go.work", "err", err)
	}

	baseURL := "http://" + listen
	// Make `bx` runnable in terminals. In the container it's already on PATH
	// (/opt/xbin/bin, Dockerfile); in dev/host mode it usually isn't, so we
	// prepend the directory holding the bx binary.
	bxDir := locateBx(dev)
	if bxDir == "" {
		slog.Warn("bx CLI not found; terminals won't have it on PATH (build it: go build -o bin/bx ./cmd/bx, or set XBIN_BIN)")
	}
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
	tm.Listen = listen             // for the internet-scope relay host-forward to xbind
	tm.SeedHome = seedHomeSkeleton // .zshrc/.bashrc/… into a fresh per-user home
	tm.Tokens = a                  // per-session tile-scoped terminal tokens (plans/terminal-tokens.md)
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

	webFS, docsFS := xbin.WebFS(), xbin.DocsFS()
	if dev {
		if src := devSourceDir(); src != "" {
			slog.Info("dev mode: serving web/ and docs/ from disk", "dir", src)
			webFS = os.DirFS(filepath.Join(src, "web"))
			docsFS = os.DirFS(filepath.Join(src, "docs"))
		}
	}
	// Materialize the builder docs on disk (XBIN_DOCS) so a terminal — sandboxed
	// or not — can read AGENTS.md's companions (elements.md, auth.md, …) as files.
	if err := extractFS(docsFS, filepath.Join(ws, ".xbin", "docs")); err != nil {
		slog.Warn("extract docs", "err", err)
	}

	// Broker: grants/RBAC policy, vault, resources (plans/auth.md).
	brk, err := broker.New(reg, hub, scopeUIDs && os.Geteuid() == 0)
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
		brk.SetUpdater(builtins.NewUpdater(reg.Root, tileSet, xbin.TemplateFS()))
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

	// Fold cgroup at-limit events into the workspace alerts: a tile that keeps
	// hitting its memory or pids cap surfaces in the admin console / shell.
	// Delta-tracked so a one-off blip clears once the tile settles.
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

	// Vault encryption barrier (docs/auth.md §vault, plans/vault-data.md). It
	// protects both secrets and resource data (kv/filesystem/sqlite/blob) at
	// rest. Secure by default in production; ON by default under a bare `make
	// dev` too. Modes (first match wins):
	//   - XBIN_VAULT_PASSPHRASE set → auto-init/unseal at boot (convenient).
	//   - --insecure-vault or --no-auth → plaintext at rest (the opt-out; the
	//     frictionless/harness path — barrier stays uninitialized).
	//   - --dev (without the above) → auto-init with a built-in DEV key so
	//     `make dev` encrypts by default (INSECURE; dev only).
	//   - production, no env, barrier set up → start SEALED; an admin unseals.
	//   - production, no env, no barrier → start LOCKED until an admin unseals
	//     (creates the barrier on first use; the passphrase never touches env —
	//     the strongest mode).
	brk.AllowInsecureVault = insecureVault || noAuth
	switch pass := os.Getenv("XBIN_VAULT_PASSPHRASE"); {
	case pass != "":
		if err := brk.UnsealOrInit(pass); err != nil {
			return fmt.Errorf("vault unseal: %w", err)
		}
		slog.Info("vault: encryption at rest active (auto-unsealed from env)")
	case insecureVault || noAuth:
		slog.Warn("vault: NO encryption at rest — data is plaintext on disk (--insecure-vault/--no-auth). Set XBIN_VAULT_PASSPHRASE or drop the flag to encrypt")
	case dev:
		// Dev convenience: init on first run, unseal on later runs, with a fixed
		// in-source key (INSECURE — dev only), so encryption-at-rest is exercised
		// without extra setup.
		if err := brk.UnsealOrInit(devVaultPassphrase); err != nil {
			return fmt.Errorf("vault dev-init: %w", err)
		}
		slog.Warn("vault: encryption at rest active with a built-in DEV key (INSECURE — dev only; use XBIN_VAULT_PASSPHRASE or manual unseal in production)")
	case brk.Barrier().Initialized():
		slog.Warn("vault: encrypted and SEALED — an admin must unseal it after login (bx vault unseal, or the admin console); secret reads/writes fail until then")
	default:
		slog.Warn("vault: LOCKED — no encryption configured. An admin sets it up after login (bx vault unseal, or the admin console); secret storage is refused until then")
	}

	px := &proxy.Proxy{Reg: reg, Runner: run, Hub: hub, Policy: brk.Policy}
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
	version = buildVersion() // resolve once for the daemon (brk + server + gateway)
	brk.Version = version
	brk.ProxyHandler = px // internal archiver calls for backup/restore

	// Ingress (plans/ingress.md): the L4 stream relay + per-terminator forward
	// doors, and their broker/runner wiring. The managers reconcile against
	// broker-computed state on boot, binding changes, and rescans.
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
	reconcileIngress := func() {
		strm.Reconcile(brk.IngressStreamSpecs())
		fwds.Reconcile(brk.IngressSources())
	}
	brk.OnIngressChange = reconcileIngress
	run.IngressNet = brk.IngressNetFor
	run.IngressFwd = brk.IngressFwdFor
	run.NetLinks = brk.NetLinksFor
	run.Published = brk.PublishedHost
	run.HairpinDial = brk.HairpinDial

	if scopeUIDs && os.Geteuid() == 0 {
		run.SpawnUser = brk.SpawnUser
	}
	// Per-component cgroup v2 limits + accounting (memory/CPU/pids), best-effort
	// — active only when xbind's cgroup is delegated (systemd Delegate=yes / a
	// container). A runaway tile then OOMs / throttles / can't fork *alone*.
	if cg := cgroup.New(); cg.Enabled() {
		cg.SetLimits(cgroup.Limits{
			MemMax:    envBytes("XBIN_LIMIT_MEM", 2<<30),     // 2 GiB
			PidsMax:   int64(max(512, goruntime.NumCPU()*8)), // fork-bomb ceiling
			CPUWeight: 100,                                   // fair share; burst when idle
		})
		run.Cgroup = cg
		tm.Cgroup = cg // restricted (non-admin) terminals get the same caps (D17d)
		slog.Info("cgroup v2 limits enabled", "memMax", 2<<30, "pidsMax", max(512, goruntime.NumCPU()*8))
	}
	// Isolation is orthogonal to --dev/--no-auth (which only change asset serving
	// and logging): the sandbox network/fs model is different enough that dev
	// should run against it too (`make dev`).
	if isolate {
		if rootfs == "" {
			fatal("--isolate needs --rootfs <dir> (an unpacked base OCI rootfs; `make rootfs`)")
		}
		if !sandbox.Available() {
			fatal("--isolate: unprivileged user namespaces unavailable on this host")
		}
		abs, err := filepath.Abs(rootfs)
		if err != nil || !dirExists(abs) {
			fatal("--isolate: rootfs %q not found", rootfs)
		}
		run.Rootfs = abs
		run.Isolate = true
		run.Egress = brk.EgressFor
		run.GPU = brk.GPUFor
		run.NetRoster = brk.NetProviderRoster
		run.NetTarget = brk.NetClientTarget
		run.NetHost = brk.NetHostShare
		run.NetCaps = brk.NetAdminFor        // cap:net-admin → keep net-admin caps
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
			fatal("%v", err)
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
	}

	srv := &server.Server{
		Reg: reg, Auth: a, Hub: hub, Term: tm,
		WebFS: webFS, DocsFS: docsFS,
		ComponentAPI: px, Version: version,
	}
	brk.Register(srv)
	srv.RegisterAPI("GET /backends", func(w http.ResponseWriter, r *http.Request) {
		if !brk.IsAdmin(auth.PrincipalOf(r)) {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		server.WriteJSON(w, http.StatusOK, run.Status())
	})
	// Full runtime visibility for the admin console: host + per-backend process,
	// namespaces, and egress/network activity (plans/isolation.md).
	srv.RegisterAPI("GET /runtime", func(w http.ResponseWriter, r *http.Request) {
		if !brk.IsAdmin(auth.PrincipalOf(r)) {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		var ms goruntime.MemStats
		goruntime.ReadMemStats(&ms)
		host := map[string]any{
			"version": version, "pid": os.Getpid(), "uid": os.Geteuid(),
			"kernel": kernelRelease(), "numCPU": goruntime.NumCPU(),
			"goroutines": goruntime.NumGoroutine(), "heapMB": float64(ms.HeapAlloc) / 1e6,
			"uptimeSec": int64(time.Since(startTime).Seconds()),
			"isolate":   run.Isolate, "rootfs": run.Rootfs, "scopeUids": scopeUIDs && os.Geteuid() == 0,
			"protections": sandbox.DetectProtections(), // terminal mount/read guard availability
		}
		server.WriteJSON(w, http.StatusOK, map[string]any{
			"host": host, "backends": run.Inspect(), "resources": brk.ResourceUsage(),
		})
	})
	// Ingress overview (plans/ingress.md): exposes + bindings + routes from
	// the broker, live listener/forward status from the managers. Admin-only —
	// the published surface and its failure modes are operator data.
	srv.RegisterAPI("GET /ingress", func(w http.ResponseWriter, r *http.Request) {
		if !brk.IsAdmin(auth.PrincipalOf(r)) {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		out := brk.IngressOverview()
		out["streams"] = strm.Status()
		out["forwards"] = fwds.Status()
		out["httpListener"] = map[string]any{
			"listen": ing.Listen, "tls": ing.Cert != "" && ing.Key != "",
		}
		server.WriteJSON(w, http.StatusOK, out)
	})
	// Per-tile runtime status — readable from a tile terminal (self) or by an
	// admin for any tile. Read-only. Runtime metrics we already collect, scoped
	// to one component: backend process/cgroup/egress + disk usage/quota + its
	// alerts. `bx status` renders it.
	srv.RegisterAPI("GET /tile-status", func(w http.ResponseWriter, r *http.Request) {
		p := auth.PrincipalOf(r)
		comp := strings.Trim(r.URL.Query().Get("component"), "/")
		if comp == "" {
			comp = p.Component // default: the caller's own tile (terminal / element principal)
		}
		if comp == "" {
			http.Error(w, "specify ?component= (or call from a tile terminal)", http.StatusBadRequest)
			return
		}
		if !brk.IsAdmin(p) && p.Component != comp {
			http.Error(w, "you can only read your own tile's status", http.StatusForbidden)
			return
		}
		var be *runner.Backend
		for _, b := range run.Inspect() {
			if b.Path == comp {
				bb := b
				be = &bb
				break
			}
		}
		usage, quota, blocked := brk.TileDiskStatus(comp)
		server.WriteJSON(w, http.StatusOK, map[string]any{
			"component": comp,
			"backend":   be, // nil when no backend is running
			"disk":      map[string]any{"usageBytes": usage, "quotaBytes": quota, "blocked": blocked},
			"alerts":    brk.TileAlerts(comp),
		})
	})

	// Watch → rescan, live reload, rebuilds.
	w, err := watch.New(ws, watchDebounce)
	if err != nil {
		return err
	}
	go watchLoop(w, reg, hub, run, brk, reconcileIngress)

	handler := srv.Handler()

	// Gateway socket: element backends call other elements / xbin APIs here
	// with their instance tokens (plans/auth.md §3). Same handler, same auth.
	gwSock := filepath.Join(run.RunDir, "gateway.sock")
	_ = os.Remove(gwSock)
	gwLn, err := net.Listen("unix", gwSock)
	if err != nil {
		return fmt.Errorf("gateway socket: %w", err)
	}
	go func() {
		gwSrv := &http.Server{
			Handler: handler,
			// Reap idle keep-alive conns from backends' pooled clients (the
			// SDK's IdleConnTimeout is 90s — under this) without touching
			// long-lived streams (WS is hijacked; SSE is never idle).
			IdleTimeout:       120 * time.Second,
			ReadHeaderTimeout: 30 * time.Second,
		}
		if err := gwSrv.Serve(gwLn); err != nil {
			slog.Error("gateway serve", "err", err)
		}
	}()

	// Boot reconcile: stand up stream listeners + forward doors for existing
	// bindings before traffic arrives.
	reconcileIngress()

	// The builtin HTTP terminator (plans/ingress.md ING-3): a SECOND listener
	// — public, unauthenticated traffic never shares the console socket. It
	// serves only published tile routes (Host-routed, path-allowlisted); TLS
	// is bring-your-own-cert or none (a Tailscale/LB/reverse-proxy front, or
	// the Traefik tile for public ACME).
	if ing.Listen != "" {
		ingressHandler := &ingressPkg.HTTPHandler{
			Source: broker.IngressSourceRuntime, Lookup: brk.IngressLookup,
			Forward: func(w http.ResponseWriter, r *http.Request, rt ingressPkg.Route) {
				px.ForwardIngress(w, r, rt, false)
			},
		}
		iln, err := net.Listen("tcp", ing.Listen)
		if err != nil {
			return fmt.Errorf("ingress listener: %w", err)
		}
		if (ing.Cert == "") != (ing.Key == "") {
			return fmt.Errorf("ingress TLS needs BOTH --ingress-cert and --ingress-key")
		}
		if ing.Cert != "" {
			tc, err := ingressTLSConfig(ing.Cert, ing.Key)
			if err != nil {
				return fmt.Errorf("ingress TLS: %w", err)
			}
			iln = tls.NewListener(iln, tc)
		}
		// Hairpin flows into the builtin terminator dial its local address.
		brk.IngressHTTPAddr = localDialAddr(ing.Listen)
		go func() {
			iSrv := &http.Server{
				Handler:           ingressHandler,
				IdleTimeout:       120 * time.Second,
				ReadHeaderTimeout: 30 * time.Second,
			}
			if err := iSrv.Serve(iln); err != nil {
				slog.Error("ingress serve", "err", err)
			}
		}()
		slog.Info("ingress listener up", "addr", ing.Listen, "tls", ing.Cert != "")
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}

	if noAuth {
		slog.Warn("auth disabled")
		fmt.Printf("\n  xbin (no auth): %s\n\n", baseURL)
	} else {
		fmt.Printf("\n  xbin login URL:\n  %s/login?token=%s\n\n", baseURL, a.OwnerTokenValue())
	}

	httpSrv := &http.Server{
		Handler:           handler,
		IdleTimeout:       120 * time.Second, // reap idle browser/CLI keep-alives
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		slog.Info("shutting down")
		_ = httpSrv.Close()
		run.StopAll()
		os.Exit(0)
	}()
	return httpSrv.Serve(ln)
}

func watchLoop(w *watch.Watcher, reg *registry.Registry, hub *events.Hub, run *runner.Runner, brk *broker.Broker, reconcileIngress func()) {
	for ev := range w.C {
		if err := reg.Rescan(); err != nil {
			slog.Warn("rescan", "err", err)
		}
		brk.Provision()
		reconcileIngress() // manifest exposes / bindings may have changed on disk
		for _, p := range deps.Reconcile(reg) {
			slog.Debug("deps", "problem", p)
		}
		if err := deps.GoWork(reg, deps.SDKPath()); err != nil {
			slog.Warn("go.work", "err", err)
		}
		affected := map[string]*registry.Component{}
		for _, p := range ev.Paths {
			if c, _, ok := reg.Resolve(p); ok {
				affected[c.Path] = c
			}
		}
		for _, c := range affected {
			slog.Debug("changed", "component", c.Path)
			hub.Publish(events.Event{Type: "reload", Component: c.Path})
			run.Changed(c)
		}
	}
}

// templateRename maps template names to their real destinations (go:embed
// skips dotfiles, so the template stores them undotted).
// homeSkel maps per-user home dotfiles to their embedded template sources
// (the on-disk names carry the dot; go:embed sources can't).
var homeSkel = map[string]string{
	".zshrc":        "home/zshrc",
	".bashrc":       "home/bashrc",
	".bash_profile": "home/bash_profile",
}

// seedHomeSkeleton writes any missing skeleton dotfiles into a per-user home
// (term.Manager.SeedHome). Idempotent: user edits are never overwritten.
func seedHomeSkeleton(dir string) error {
	for dst, src := range homeSkel {
		p := filepath.Join(dir, dst)
		if _, err := os.Lstat(p); err == nil {
			continue
		}
		b, err := fs.ReadFile(xbin.TemplateFS(), src)
		if err != nil {
			continue
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// pristineHomeFile reports whether a legacy home/ file is byte-identical to the
// template skeleton — the home migration's both-forms check (a home/ recreated
// by an older xbind's backfill is safe to drop; anything else is not).
func pristineHomeFile(rel string, data []byte) bool {
	src, ok := homeSkel[rel]
	if !ok {
		return false
	}
	b, err := fs.ReadFile(xbin.TemplateFS(), src)
	return err == nil && bytes.Equal(b, data)
}

var templateRename = map[string]string{
	"gitignore":         ".gitignore",
	"home/zshrc":        "home/.zshrc",
	"home/bashrc":       "home/.bashrc",
	"home/bash_profile": "home/.bash_profile",
}

// seedTemplateFile writes one template file into the workspace if (and only
// if) its destination doesn't exist yet.
func seedTemplateFile(dir, name string) error {
	dst := name
	if r, ok := templateRename[name]; ok {
		dst = r
	}
	out := filepath.Join(dir, filepath.FromSlash(dst))
	if _, err := os.Stat(out); err == nil {
		return nil // never overwrite
	}
	b, err := fs.ReadFile(xbin.TemplateFS(), name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(out, b, 0o644)
}

// initWorkspace scaffolds a new workspace from the embedded template and
// (decision D2) makes it a git repo. Never overwrites existing files.
func initWorkspace(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tpl := xbin.TemplateFS()
	err := fs.WalkDir(tpl, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return seedTemplateFile(dir, p)
	})
	if err != nil {
		return err
	}
	for _, sub := range []string{".xbin", "data", "home", "apps", "lib"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	// Agents discover builder guidance via either name; keep one source.
	if _, err := os.Lstat(filepath.Join(dir, "CLAUDE.md")); err != nil {
		_ = os.Symlink("AGENTS.md", filepath.Join(dir, "CLAUDE.md"))
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if git, err := exec.LookPath("git"); err == nil {
			cmd := exec.Command(git, "init", "-q")
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				slog.Warn("git init failed", "out", string(out))
			}
		}
	}
	// Record scaffold provenance so a future xbind can offer updates to these
	// components without clobbering the user's edits (plans/builtin-updates.md).
	if err := builtins.NewUpdater(dir, nil, xbin.TemplateFS()).RecordScaffoldSeed(); err != nil {
		slog.Warn("record scaffold provenance", "err", err)
	}
	return nil
}

// dropToWorkspaceOwner implements decision D13(b): xbind started as root
// becomes the workspace directory's owner so bind-mounted workspaces keep
// sane file ownership.
func dropToWorkspaceOwner(ws string) error {
	fi, err := os.Stat(ws)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid == 0 {
		return nil
	}
	slog.Info("dropping privileges to workspace owner", "uid", st.Uid, "gid", st.Gid)
	if err := syscall.Setgroups([]int{int(st.Gid)}); err != nil {
		return fmt.Errorf("setgroups: %w", err)
	}
	if err := syscall.Setgid(int(st.Gid)); err != nil {
		return fmt.Errorf("setgid: %w", err)
	}
	if err := syscall.Setuid(int(st.Uid)); err != nil {
		return fmt.Errorf("setuid: %w", err)
	}
	return nil
}

// reapZombies handles PID-1 duty: adopt and reap orphaned grandchildren.
// Our own exec.Cmd Waits race benignly (they get ECHILD and treat it as exit).
func reapZombies() {
	c := make(chan os.Signal, 16)
	signal.Notify(c, syscall.SIGCHLD)
	for range c {
		for {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
		}
	}
}

// locateBx returns the directory containing the `bx` CLI so it can be put on
// terminals' PATH. Resolution order: XBIN_BIN override; next to the xbind
// binary (container /opt/xbin/bin, `make build` bin/); dev repo bin/; then
// whatever is already on xbind's PATH.
func locateBx(dev bool) string {
	if p := os.Getenv("XBIN_BIN"); p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		if d := filepath.Dir(exe); isFile(filepath.Join(d, "bx")) {
			return d
		}
	}
	if dev {
		if src := devSourceDir(); src != "" && isFile(filepath.Join(src, "bin", "bx")) {
			return filepath.Join(src, "bin")
		}
	}
	if p, err := exec.LookPath("bx"); err == nil {
		return filepath.Dir(p)
	}
	return ""
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// extractFS materializes an embedded FS to a directory (idempotent overwrite).
func extractFS(src fs.FS, dst string) error {
	return fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out := filepath.Join(dst, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		b, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// devSourceDir finds the repo root when running via `go run ./cmd/xbind`.
func devSourceDir() string {
	if _, err := os.Stat("web/bx-frame.js"); err == nil {
		wd, _ := os.Getwd()
		return wd
	}
	return ""
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// envBytes reads a byte count from env — a plain integer or a K/M/G/T suffix
// (e.g. "2G", "512M") — falling back to def.
func envBytes(k string, def int64) int64 {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	mult := int64(1)
	switch v[len(v)-1] {
	case 'k', 'K':
		mult, v = 1<<10, v[:len(v)-1]
	case 'm', 'M':
		mult, v = 1<<20, v[:len(v)-1]
	case 'g', 'G':
		mult, v = 1<<30, v[:len(v)-1]
	case 't', 'T':
		mult, v = 1<<40, v[:len(v)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n <= 0 {
		slog.Warn("ignoring malformed byte-size env var", "key", k, "value", os.Getenv(k))
		return def
	}
	return n * mult
}

func fatal(f string, args ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", args...)
	os.Exit(1)
}

// watchDebounce coalesces editor save bursts (plans/implementation.md phase 1).
const watchDebounce = 300 * time.Millisecond

// ingressTLSConfig serves the BYO cert pair, re-loading it when the cert
// file changes on disk (a cert renewed in place picks up on the next
// handshake — no restart).
func ingressTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	var mu sync.Mutex
	var cached *tls.Certificate
	var mtime time.Time
	load := func() (*tls.Certificate, error) {
		fi, err := os.Stat(certFile)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		if cached != nil && fi.ModTime().Equal(mtime) {
			return cached, nil
		}
		c, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			if cached != nil {
				slog.Warn("ingress TLS reload failed; keeping previous cert", "err", err)
				return cached, nil
			}
			return nil, err
		}
		cached, mtime = &c, fi.ModTime()
		return cached, nil
	}
	if _, err := load(); err != nil { // validate at startup
		return nil, err
	}
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return load() },
	}, nil
}

// localDialAddr maps a listen address to the address local (hairpin) flows
// dial: wildcard hosts become loopback.
func localDialAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return ""
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
