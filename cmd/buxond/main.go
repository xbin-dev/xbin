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
	"syscall"
	"time"

	"github.com/magik6k/buxon"
	"github.com/magik6k/buxon/internal/auth"
	"github.com/magik6k/buxon/internal/broker"
	"github.com/magik6k/buxon/internal/deps"
	"github.com/magik6k/buxon/internal/events"
	"github.com/magik6k/buxon/internal/proxy"
	"github.com/magik6k/buxon/internal/registry"
	"github.com/magik6k/buxon/internal/runner"
	"github.com/magik6k/buxon/internal/server"
	"github.com/magik6k/buxon/internal/term"
	"github.com/magik6k/buxon/internal/watch"
)

var version = "dev" // set via -ldflags at release

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
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
		wsFlag    = flag.String("workspace", envOr("BUXON_WORKSPACE", "/workspace"), "workspace directory")
		listen    = flag.String("listen", envOr("BUXON_LISTEN", "127.0.0.1:8642"), "listen address")
		dev       = flag.Bool("dev", false, "dev mode: no auth, web/docs served from source tree")
		noAuth    = flag.Bool("no-auth", false, "disable auth (dev only; implied by -dev)")
		scopeUIDs = flag.Bool("scope-uids", false, "run each scope's backends under a dedicated uid (requires root; auth tier 2)")
	)
	flag.Parse()

	ws, err := filepath.Abs(*wsFlag)
	if err != nil {
		fatal("%v", err)
	}
	if err := serve(ws, *listen, *dev, *noAuth || *dev, *scopeUIDs); err != nil {
		fatal("%v", err)
	}
}

func serve(ws, listen string, dev, noAuth, scopeUIDs bool) error {
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
			"HOME=" + home, // D6: dotfiles live inside the workspace
		}
		if bxDir != "" {
			env = append(env, "PATH="+bxDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
		return env
	})

	webFS, docsFS := buxon.WebFS(), buxon.DocsFS()
	if dev {
		if src := devSourceDir(); src != "" {
			slog.Info("dev mode: serving web/ and docs/ from disk", "dir", src)
			webFS = os.DirFS(filepath.Join(src, "web"))
			docsFS = os.DirFS(filepath.Join(src, "docs"))
		}
	}

	// Broker: grants/RBAC policy, vault, resources (plans/auth.md).
	brk, err := broker.New(reg, hub, scopeUIDs && os.Geteuid() == 0)
	if err != nil {
		return err
	}

	// Vault encryption barrier (docs/auth.md §vault). Auto-unseal (or
	// first-time init) from BUXON_VAULT_PASSPHRASE; otherwise the vault runs
	// in plaintext-at-rest mode with a warning, or stays sealed until an
	// admin unseals it via the API.
	if pass := os.Getenv("BUXON_VAULT_PASSPHRASE"); pass != "" {
		if err := brk.UnsealOrInit(pass); err != nil {
			return fmt.Errorf("vault unseal: %w", err)
		}
		slog.Info("vault barrier unsealed (encryption at rest active)")
	} else if brk.Barrier().Initialized() {
		slog.Warn("vault is encrypted but SEALED — set BUXON_VAULT_PASSPHRASE or POST /api/buxon/vault-unseal; secret reads/writes fail until unsealed")
	} else if brk.VaultInsecure() {
		slog.Warn("vault has NO encryption at rest — secrets are plaintext on disk; set BUXON_VAULT_PASSPHRASE to enable the barrier (docs/auth.md)")
	}

	px := &proxy.Proxy{Reg: reg, Runner: run, Hub: hub, Policy: brk.Policy}
	brk.SetDispatch(broker.DispatchViaProxy(px))
	run.EnvForComponent = brk.EnvFor
	if scopeUIDs && os.Geteuid() == 0 {
		run.SpawnUser = brk.SpawnUser
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
