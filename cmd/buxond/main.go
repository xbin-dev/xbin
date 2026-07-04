// buxond — the buxon workspace daemon. See plans/ for the design and /docs/
// (served by this binary) for the builder-facing documentation.
//
//	buxond init <dir>              scaffold a workspace
//	buxond [flags]                 serve a workspace
//	buxond version
package main

import (
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
	"strings"
	"syscall"
	"time"

	"github.com/magik6k/buxon"
	"github.com/magik6k/buxon/internal/auth"
	"github.com/magik6k/buxon/internal/broker"
	"github.com/magik6k/buxon/internal/builtins"
	"github.com/magik6k/buxon/internal/cgroup"
	"github.com/magik6k/buxon/internal/deps"
	"github.com/magik6k/buxon/internal/events"
	"github.com/magik6k/buxon/internal/gpu"
	"github.com/magik6k/buxon/internal/proxy"
	"github.com/magik6k/buxon/internal/registry"
	"github.com/magik6k/buxon/internal/runner"
	"github.com/magik6k/buxon/internal/sandbox"
	"github.com/magik6k/buxon/internal/server"
	"github.com/magik6k/buxon/internal/term"
	"github.com/magik6k/buxon/internal/users"
	"github.com/magik6k/buxon/internal/watch"
)

var version = "dev" // set via -ldflags at release
var startTime = time.Now()

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
				fatal("usage: buxond __sandbox-init <spec>")
			}
			sandbox.RunInit(os.Args[2]) // never returns on linux
			return
		case "init":
			if len(os.Args) < 3 {
				fatal("usage: buxond init <dir>")
			}
			if err := initWorkspace(os.Args[2]); err != nil {
				fatal("init: %v", err)
			}
			fmt.Println("workspace initialized:", os.Args[2])
			return
		case "version":
			fmt.Println("buxond", version)
			return
		}
	}

	var (
		wsFlag        = flag.String("workspace", envOr("BUXON_WORKSPACE", "/workspace"), "workspace directory")
		listen        = flag.String("listen", envOr("BUXON_LISTEN", "127.0.0.1:8642"), "listen address")
		dev           = flag.Bool("dev", false, "dev mode: web/docs served from source tree, debug logs")
		noAuth        = flag.Bool("no-auth", false, "disable auth (dev only; every request is admin)")
		scopeUIDs     = flag.Bool("scope-uids", false, "run each scope's backends under a dedicated uid (requires root; auth tier 2)")
		insecureVault = flag.Bool("insecure-vault", false, "allow the vault to store secrets as PLAINTEXT at rest (not recommended; --dev implies it)")
		isolate       = flag.Bool("isolate", false, "run each backend in a per-component sandbox (namespaces + overlay rootfs; auth tier 3, needs --rootfs)")
		rootfs        = flag.String("rootfs", envOr("BUXON_ROOTFS", ""), "base rootfs dir (unpacked OCI image) for --isolate sandboxes")
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
	if err := serve(ws, *listen, *dev, *noAuth, *scopeUIDs, *insecureVault, *isolate, *rootfs); err != nil {
		fatal("%v", err)
	}
}

func serve(ws, listen string, dev, noAuth, scopeUIDs, insecureVault, isolate bool, rootfs string) error {
	lvl := slog.LevelInfo
	if dev {
		lvl = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))

	if os.Getpid() == 1 {
		go reapZombies()
	}

	// Auto-init an empty workspace mount (deployment.md §install).
	if _, err := os.Stat(filepath.Join(ws, "buxon.json")); err != nil {
		slog.Info("initializing workspace", "dir", ws)
		if err := initWorkspace(ws); err != nil {
			return fmt.Errorf("auto-init: %w", err)
		}
	}
	// Backfill infrastructure files into workspaces created by older buxond.
	// Only these: unlike app content, their absence is never deliberate —
	// agents depend on AGENTS.md, and an empty $HOME drops zsh into the
	// zsh-newuser-install wizard.
	for _, f := range []string{"AGENTS.md", "home/zshrc", "home/bashrc", "home/bash_profile"} {
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
			ID: "admin", Name: "Dev Admin", Role: users.RoleAdmin, Terminal: true,
		}, "admin"); err == nil {
			slog.Warn("dev: seeded admin user — login 'admin' / 'admin' — DEV ONLY, never expose")
		}
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

	home := filepath.Join(ws, "home")
	_ = os.MkdirAll(home, 0o755)

	baseURL := "http://" + listen
	// Make `bx` runnable in terminals. In the container it's already on PATH
	// (/opt/buxon/bin, Dockerfile); in dev/host mode it usually isn't, so we
	// prepend the directory holding the bx binary.
	bxDir := locateBx(dev)
	if bxDir == "" {
		slog.Warn("bx CLI not found; terminals won't have it on PATH (build it: go build -o bin/bx ./cmd/bx, or set BUXON_BIN)")
	}
	tm := term.NewManager(ws, func() []string {
		env := []string{
			"BUXON_URL=" + baseURL,
			"BUXON_TOKEN=" + a.OwnerToken,
			"BUXON_WORKSPACE=" + ws,
			"HOME=" + home,                                      // D6: dotfiles live inside the workspace
			"BUXON_DOCS=" + filepath.Join(ws, ".buxon", "docs"), // builder docs, on disk
		}
		if bxDir != "" {
			env = append(env, "PATH="+bxDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
		return env
	})
	tm.Listen = listen // for the internet-scope relay host-forward to buxond

	webFS, docsFS := buxon.WebFS(), buxon.DocsFS()
	if dev {
		if src := devSourceDir(); src != "" {
			slog.Info("dev mode: serving web/ and docs/ from disk", "dir", src)
			webFS = os.DirFS(filepath.Join(src, "web"))
			docsFS = os.DirFS(filepath.Join(src, "docs"))
		}
	}
	// Materialize the builder docs on disk (BUXON_DOCS) so a terminal — sandboxed
	// or not — can read AGENTS.md's companions (elements.md, auth.md, …) as files.
	if err := extractFS(docsFS, filepath.Join(ws, ".buxon", "docs")); err != nil {
		slog.Warn("extract docs", "err", err)
	}

	// Broker: grants/RBAC policy, vault, resources (plans/auth.md).
	brk, err := broker.New(reg, hub, scopeUIDs && os.Geteuid() == 0)
	if err != nil {
		return err
	}
	// Embedded optional tile catalog (plans/tile-sharing.md).
	if set, err := builtins.Load(buxon.BuiltinTilesFS()); err != nil {
		slog.Warn("builtin tiles", "err", err)
	} else {
		brk.SetBuiltins(set)
	}
	// Embedded builtin template catalog (plans/templates.md).
	if set, err := builtins.LoadTemplates(buxon.BuiltinTemplatesFS()); err != nil {
		slog.Warn("builtin templates", "err", err)
	} else {
		brk.SetBuiltinTemplates(set)
	}
	// Builtin update tracking (plans/builtin-updates.md): offer newer embedded
	// scaffold/tiles to existing workspaces without trampling customizations.
	{
		tileSet, _ := builtins.Load(buxon.BuiltinTilesFS())
		brk.SetUpdater(builtins.NewUpdater(reg.Root, tileSet, buxon.TemplateFS()))
	}
	// After a broker-driven structure change (tile import), reconcile deps and
	// regenerate go.work immediately so the new tile is usable at once.
	brk.OnStructureChange = func() {
		deps.Reconcile(reg)
		if err := deps.GoWork(reg, deps.SDKPath()); err != nil {
			slog.Warn("go.work", "err", err)
		}
	}
	brk.Users = userStore

	// Vault encryption barrier (docs/auth.md §vault). Secure by default: in
	// production (no --dev/--no-auth/--insecure-vault) secrets are never
	// written in the clear. Three ways in:
	//   - BUXON_VAULT_PASSPHRASE set → auto-init/unseal at boot (convenient).
	//   - no env, barrier already set up → start SEALED; an admin unseals
	//     after login (bx vault unseal / admin console).
	//   - no env, no barrier → start LOCKED (writes refused) until an admin
	//     unseals, which creates the barrier on first use. The passphrase
	//     never touches the container env — the strongest mode.
	// --dev/--no-auth/--insecure-vault instead permit plaintext at rest.
	allowPlaintextVault := dev || noAuth || insecureVault
	brk.AllowInsecureVault = allowPlaintextVault
	switch pass := os.Getenv("BUXON_VAULT_PASSPHRASE"); {
	case pass != "":
		if err := brk.UnsealOrInit(pass); err != nil {
			return fmt.Errorf("vault unseal: %w", err)
		}
		slog.Info("vault: encryption at rest active (auto-unsealed from env)")
	case brk.Barrier().Initialized():
		slog.Warn("vault: encrypted and SEALED — an admin must unseal it after login (bx vault unseal, or the admin console); secret reads/writes fail until then")
	case allowPlaintextVault:
		slog.Warn("vault: NO encryption at rest — secrets are plaintext on disk (dev/--insecure-vault). Set BUXON_VAULT_PASSPHRASE or unseal to encrypt")
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
	brk.Version = version
	brk.ProxyHandler = px // internal archiver calls for backup/restore

	if scopeUIDs && os.Geteuid() == 0 {
		run.SpawnUser = brk.SpawnUser
	}
	// Per-component cgroup v2 accounting (memory/CPU/pids), best-effort — active
	// only when buxond's cgroup is delegated (systemd Delegate=yes / a container).
	if cg := cgroup.New(); cg.Enabled() {
		run.Cgroup = cg
		slog.Info("cgroup v2 accounting enabled")
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
		if inv := gpu.Inventory(); len(inv) > 0 {
			slog.Info("NVIDIA GPUs available for gpu:* grants", "count", len(inv))
		}
		// Terminals share the base rootfs too (RT-4): the workspace is bound rw
		// (editing plane), plus the SDK source ro so `go build` resolves.
		tm.Isolate = true
		tm.Rootfs = abs
		// A component-scoped terminal gets its own component rw and every other
		// component's source ro (plans/runtime.md): let the manager enumerate them.
		tm.Components = func() []string {
			cs := reg.Components()
			paths := make([]string, 0, len(cs))
			for _, c := range cs {
				paths = append(paths, c.Path)
			}
			return paths
		}
		if sdk, err := filepath.Abs(envOr("BUXON_SDK_PATH", "")); err == nil && dirExists(sdk) {
			tm.ExtraBinds = append(tm.ExtraBinds, sandbox.Bind{Src: sdk, Dst: sdk, RO: true})
		}
		slog.Info("per-component isolation enabled (tier 3)", "rootfs", abs)
	}

	srv := &server.Server{
		Reg: reg, Auth: a, Hub: hub, Term: tm,
		WebFS: webFS, DocsFS: docsFS,
		ComponentAPI: px,
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
		}
		server.WriteJSON(w, http.StatusOK, map[string]any{
			"host": host, "backends": run.Inspect(), "resources": brk.ResourceUsage(),
		})
	})

	// Watch → rescan, live reload, rebuilds.
	w, err := watch.New(ws, watchDebounce)
	if err != nil {
		return err
	}
	go watchLoop(w, reg, hub, run, brk)

	handler := srv.Handler()

	// Gateway socket: element backends call other elements / buxon APIs here
	// with their instance tokens (plans/auth.md §3). Same handler, same auth.
	gwSock := filepath.Join(run.RunDir, "gateway.sock")
	_ = os.Remove(gwSock)
	gwLn, err := net.Listen("unix", gwSock)
	if err != nil {
		return fmt.Errorf("gateway socket: %w", err)
	}
	go func() {
		if err := http.Serve(gwLn, handler); err != nil {
			slog.Error("gateway serve", "err", err)
		}
	}()

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}

	if noAuth {
		slog.Warn("auth disabled")
		fmt.Printf("\n  buxon (no auth): %s\n\n", baseURL)
	} else {
		fmt.Printf("\n  buxon login URL:\n  %s/login?token=%s\n\n", baseURL, a.OwnerToken)
	}

	httpSrv := &http.Server{Handler: handler}
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

func watchLoop(w *watch.Watcher, reg *registry.Registry, hub *events.Hub, run *runner.Runner, brk *broker.Broker) {
	for ev := range w.C {
		if err := reg.Rescan(); err != nil {
			slog.Warn("rescan", "err", err)
		}
		brk.Provision()
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
	b, err := fs.ReadFile(buxon.TemplateFS(), name)
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
	tpl := buxon.TemplateFS()
	err := fs.WalkDir(tpl, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return seedTemplateFile(dir, p)
	})
	if err != nil {
		return err
	}
	for _, sub := range []string{".buxon", "data", "home", "apps", "lib"} {
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
	// Record scaffold provenance so a future buxond can offer updates to these
	// components without clobbering the user's edits (plans/builtin-updates.md).
	if err := builtins.NewUpdater(dir, nil, buxon.TemplateFS()).RecordScaffoldSeed(); err != nil {
		slog.Warn("record scaffold provenance", "err", err)
	}
	return nil
}

// dropToWorkspaceOwner implements decision D13(b): buxond started as root
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
// terminals' PATH. Resolution order: BUXON_BIN override; next to the buxond
// binary (container /opt/buxon/bin, `make build` bin/); dev repo bin/; then
// whatever is already on buxond's PATH.
func locateBx(dev bool) string {
	if p := os.Getenv("BUXON_BIN"); p != "" {
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

// devSourceDir finds the repo root when running via `go run ./cmd/buxond`.
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

func fatal(f string, args ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", args...)
	os.Exit(1)
}

// watchDebounce coalesces editor save bursts (plans/implementation.md phase 1).
const watchDebounce = 300 * time.Millisecond
