// Package term implements persistent PTY terminal sessions behind /ws/term.
//
// Sessions are the editing plane, scoped to the tile they're opened on
// (plans/terminal-tokens.md): shells run as the xbind unix user, cwd'd to the
// component's source directory, with a per-session XBIN_TOKEN that resolves to
// that TILE's element principal — self-admin plus the tile's approved grants,
// never the driving user's privilege. The root terminal (no cwd) is disabled.
// A session outlives its WebSocket — reattach by id replays bounded
// scrollback. Wire protocol in docs/protocol.md: binary frames are raw PTY
// bytes; text frames are JSON control messages (the attach itself, the echo
// acks and pings the predictive echo needs: attach.go).
package term

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/gpu"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/relay"
	"github.com/xbin-dev/xbin/internal/util"
)

const (
	maxScrollback      = 256 << 10 // per session
	maxSessions        = 64
	maxSessionsPerUser = 32 // so one user can't starve the global pool
	idleTimeout        = 24 * time.Hour
)

type Session struct {
	ID      string
	Cwd     string // workspace-relative component path
	Net     string // network scope (NetInternet|NetHost|NetNone|NetOrg|set:<name>)
	NetNote string // why the requested scope was clamped ("" = as asked)
	Label   string // human label of the effective scope (org network name)
	Scopes  []Scope
	cmd     *exec.Cmd
	pty     *os.File
	cleanup func()       // sandbox spec temp cleanup (nil for a plain shell)
	relay   *relay.Relay // egress relay (internet scope only; nil otherwise)
	envKey  string       // persistent per-component layer this session holds ("" = none/ephemeral)
	homeKey string       // whose $HOME this session mounts (user id, or "owner" for the token)
	token   string       // per-session terminal token (revoked when the session dies)
	baseOld bool         // the held layer's base is older than the current rootfs (offer upgrade)
	born    time.Time
	gpu     string // the pickers this session was opened with (the directory restores a tab from them, D73)
	api     bool
	name    string // the tab's name, per session (sessions.go); guarded by mu
	kind    string // KindShell (a PTY) or KindAgent (agent.go: no PTY, an ACP driver over pipes)
	agent   *agentState
	pgid    bool // the process leads its own group (the non-isolated agent host): kill the group

	mu         sync.Mutex
	scrollback []byte
	clients    map[*client]struct{}
	lastActive time.Time
	dead       bool
}

type Manager struct {
	// OnChange is told when a session is opened ("open"), ends for any reason
	// ("close") or is renamed ("rename") — the server relays it to the owner's
	// browsers as a `term` event (D73). Optional.
	OnChange func(op, homeKey, id, cwd string)
	Root     string          // workspace root
	Listen   string          // xbind's listen addr (host:port) — for the relay host-forward
	Env      func() []string // extra env for shells (token, HOME, …)
	upgrader websocket.Upgrader

	// Isolate + Rootfs run terminals in a rootfs sandbox (plans/runtime.md RT-4):
	// the base rootfs userland (toolchains + agent CLIs), the workspace mounted
	// read-write (editing plane), a persistent per-user $HOME (homes/<user> —
	// agent config and dotfiles scoped to the signed-in human), and a
	// per-session network scope (default: own netns + internet-only egress relay,
	// so host interfaces stay hidden). ExtraBinds add read-only mounts (e.g. the
	// SDK source so `go build` resolves). Off ⇒ a plain host shell.
	Isolate    bool
	Rootfs     string
	ExtraBinds []sandbox.Bind

	// SeedHome populates a freshly created per-user home with the template
	// skeleton dotfiles (.zshrc/.bashrc/…); wired by main (which holds the
	// embedded template FS). Idempotent — only missing files are written.
	SeedHome func(dir string) error

	// Tokens mints/revokes the per-session terminal tokens that scope a
	// shell's XBIN_TOKEN to its tile (wired to *auth.Auth by main). nil ⇒
	// sessions get no XBIN_TOKEN at all — never the owner token.
	Tokens interface {
		MintTerminal(component, userID string) string
		RevokeTerminal(token string)
	}

	// TermNet answers "what network may this principal's terminal on this
	// tile have" — the tile's owning org's network sets (D54), wired to the
	// broker by main. nil ⇒ the pre-D54 rules (termNet → internet, host
	// admin-only).
	TermNet func(p auth.Principal, component string) TermNet

	// HiddenTiles lists the component dirs to mask out of this principal's
	// terminals (D17a — source visibility scoped to the allow-list): every
	// tile below read level. Wired by main from the registry; nil ⇒ terminals
	// see all source (fine for the single-admin workspace). Superseded by
	// TermView when set — kept as the fallback plan.
	HiddenTiles func(p auth.Principal) []string

	// TermView builds a RESTRICTED session's ALLOW-LIST workspace view (D40):
	// the component paths the principal may read, plus the redacted root-file
	// contents the staged view serves in place of the real ones (xbin.json
	// filtered to readable rows, go.work covering only readable modules,
	// AGENTS.md/.gitignore copies). When set, restricted terminals mount a
	// staged view dir at the workspace root and bind ONLY those components —
	// unreadable tiles vanish entirely, names included, and the workspace's
	// real root files (the full grants/bindings topology) never enter the
	// sandbox. nil ⇒ the old deny-list masking via HiddenTiles.
	TermView func(p auth.Principal) (readable []string, rootFiles map[string][]byte)

	// BxPath is the daemon's own bx binary (located at boot): an agent
	// session binds it read-only into its sandbox as the entry (`bx
	// __agent-host`, D74) — host and daemon are one build. OnEvent receives
	// every agent session event as it is logged (the server publishes it as a
	// `session` event). agent.go.
	BxPath  string
	OnEvent func(cwd string, ev SessionEvent)

	// Cgroup, when set (main wires it under cgroup delegation), puts each
	// RESTRICTED session's sandbox into a resource-limited leaf (D17d) so a
	// runaway non-admin terminal OOMs/throttles alone instead of taking the
	// workspace down. Admin terminals stay unlimited (dev builds are hungry).
	Cgroup interface {
		Add(name string, pid int)
		Remove(name string)
	}

	mu       sync.Mutex
	sessions map[string]*Session
	envHeld  map[string]bool // component key → a live session holds its persistent layer
}

func NewManager(root string, env func() []string) *Manager {
	m := &Manager{
		Root: root, Env: env,
		sessions: map[string]*Session{},
		envHeld:  map[string]bool{},
		upgrader: websocket.Upgrader{
			ReadBufferSize: 4096, WriteBufferSize: 4096,
			// Same-origin app; auth middleware has already run.
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
	go m.reaper()
	return m
}

// ServeWS handles an authenticated /ws/term request.
// Query: cwd=<component-path> + net=<scope> (new session) or session=<id> (reattach).
func (m *Manager) ServeWS(w http.ResponseWriter, r *http.Request) {
	sessID := r.URL.Query().Get("session")
	cwd := r.URL.Query().Get("cwd")
	netMode := normalizeNet(r.URL.Query().Get("net"))
	gpuMode := r.URL.Query().Get("gpu") // ""/none | all | <index> (owner plane)
	// api=0 opens a code-only terminal: no terminal token is minted, so it can
	// read/edit source but can't call the live tile (or xbin) API. Default on.
	apiAccess := r.URL.Query().Get("api") != "0"
	p := auth.PrincipalOf(r)

	var (
		s   *Session
		err error
	)
	if sessID != "" {
		m.mu.Lock()
		s = m.sessions[sessID]
		m.mu.Unlock()
		if s == nil {
			http.Error(w, "no such session", http.StatusNotFound)
			return
		}
		// A session mounts its creator's $HOME — another user may not attach
		// to it (admins may, for debugging; they own the workspace anyway),
		// and a creator whose terminal level on the tile was revoked since may
		// not either (sessions.go).
		if why := s.mayReattach(p); why != "" {
			http.Error(w, why, http.StatusForbidden)
			return
		}
		if s.kind == KindAgent {
			http.Error(w, "an agent session has no terminal socket — use the agent API (docs/protocol.md)", http.StatusConflict)
			return
		}
	} else {
		// Session-open gates — the "user" half of min(user, tile)
		// (plans/terminal-tokens.md). The root terminal (no cwd) is disabled
		// outright: it was the whole-workspace owner plane, is not reachable
		// from any UI, and admin work belongs to the browser UI or host-side
		// bx. A tile terminal needs TERMINAL level on that tile (D16).
		if cwd == "" {
			http.Error(w, "the root terminal is disabled — open a terminal on a tile (admin ops: the admin tile, or bx from the host)", http.StatusForbidden)
			return
		}
		_, rel, err := util.SafeJoin(m.Root, cwd)
		if err != nil || rel == "" || !p.CanTerminalTile(rel) {
			http.Error(w, "your account doesn't have terminal access to this tile", http.StatusForbidden)
			return
		}
		s, err = m.create(m.openOptsFor(p, rel, cwd, netMode, gpuMode, apiAccess))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	conn, err := m.upgrader.Upgrade(w, r, http.Header{
		"X-XBin-Session": []string{s.ID},
	})
	if err != nil {
		return
	}
	s.attach(conn)
}

// List returns session metadata for the status API, ordered by creation time
// (m.sessions is a map, so without sorting the admin view would reshuffle).
func (m *Manager) List() []map[string]any {
	out := []map[string]any{}
	for _, s := range m.sorted() {
		s.mu.Lock()
		out = append(out, map[string]any{
			"id": s.ID, "cwd": s.Cwd, "net": s.Net, "clients": len(s.clients),
			"user":    s.homeKey,
			"created": s.born.UTC().Format(time.RFC3339),
			"label":   s.Label, "scopes": s.Scopes, "name": s.name,
		})
		s.mu.Unlock()
	}
	return out
}

// openOpts carries a new session's parameters from the WS gate to the spawn.
type openOpts struct {
	cwd        string            // workspace-relative component path
	net        string            // network scope (already clamped)
	netGrant   TermNet           // what the org scope means here + what's allowed (D54)
	netHost    bool              // the scope is host networking (resolveNet)
	netRules   []string          // the relay's grant targets otherwise (nil = offline)
	scopes     []Scope           // the scopes the client may pick (session frame)
	label      string            // human label of the effective scope
	netNote    string            // why the asked scope was clamped
	gpu        string            // GPU request (owner plane)
	homeKey    string            // whose $HOME the session mounts
	userID     string            // creating user, for token attribution ("" = token principal)
	api        bool              // mint a live tile-API token (already clamped)
	restricted bool              // non-admin: D18 kernel lockdown + D17 masks/limits
	hide       []string          // component dirs masked out of the mount (D17a fallback)
	readable   []string          // allow-list view: components bound into the mount (D40)
	rootFiles  map[string][]byte // allow-list view: staged root-file contents (D40)
	kind       string            // KindShell (default) or KindAgent: the sandbox entry (agent.go)
}

// prepare is the part of opening a session that both kinds share: the cwd,
// the limits, the user's home and the terminal token. Returns the token's
// revoke.
func (m *Manager) prepare(o openOpts) (dir, rel, homeDir, token string, revokeTok func(), err error) {
	dir = m.Root
	if o.cwd != "" {
		dir, rel, err = util.SafeJoin(m.Root, o.cwd)
		if err != nil {
			return
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return "", "", "", "", nil, fmt.Errorf("cwd %q is not a directory", o.cwd)
		}
	}

	m.mu.Lock()
	if len(m.sessions) >= maxSessions {
		m.mu.Unlock()
		return "", "", "", "", nil, fmt.Errorf("session limit (%d) reached%w", maxSessions, errLimit)
	}
	perUser := 0 // one user can't exhaust the global pool
	for _, s := range m.sessions {
		if s.homeKey == o.homeKey {
			perUser++
		}
	}
	m.mu.Unlock()
	if perUser >= maxSessionsPerUser {
		return "", "", "", "", nil, fmt.Errorf("per-user terminal limit (%d) reached — close some terminals%w", maxSessionsPerUser, errLimit)
	}

	// This user's $HOME, created + skeleton-seeded on first use (lazy: the user
	// set is dynamic, so homes materialize per user, not at scaffold time).
	homeDir = HomeDir(m.Root, o.homeKey)
	if err = os.MkdirAll(homeDir, 0o700); err != nil {
		return "", "", "", "", nil, fmt.Errorf("create home %s: %w", homeDir, err)
	}
	if m.SeedHome != nil {
		if err := m.SeedHome(homeDir); err != nil {
			slog.Warn("seeding terminal home", "dir", homeDir, "err", err)
		}
	}

	// Per-session terminal token: the shell's XBIN_TOKEN resolves to THIS
	// tile's element principal (plans/terminal-tokens.md), not the owner.
	// Withheld entirely for a code-only terminal (api=0) — no token, no API.
	if m.Tokens != nil && o.api {
		token = m.Tokens.MintTerminal(rel, o.userID)
	}
	revokeTok = func() {
		if token != "" {
			m.Tokens.RevokeTerminal(token)
		}
	}
	return
}

func (m *Manager) create(o openOpts) (*Session, error) {
	dir, rel, homeDir, token, revokeTok, err := m.prepare(o)
	if err != nil {
		return nil, err
	}
	cmd, cleanup, postStart, envKey, _, err := m.shellCmd(dir, rel, homeDir, token, o)
	if err != nil {
		revokeTok()
		return nil, err
	}

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 32})
	if err != nil {
		cleanup()
		revokeTok()
		if envKey != "" {
			m.releaseEnv(envKey)
		}
		// Also log it — the error otherwise only reaches the browser (HTTP 400),
		// which makes "my terminal won't open" undiagnosable from the server side.
		slog.Error("terminal spawn failed", "cwd", filepath.ToSlash(rel), "err", err)
		return nil, fmt.Errorf("spawn shell: %w", err)
	}
	// The egress relay can only start once init has created the TUN in its netns
	// (post-fork), so wire it up after StartWithSize.
	var rl *relay.Relay
	if postStart != nil {
		rl = postStart()
	}

	s := &Session{
		ID: util.RandomToken(8), Cwd: rel, Net: o.net, cmd: cmd, pty: f, kind: KindShell,
		NetNote: o.netNote, Label: o.label, Scopes: o.scopes,
		cleanup: cleanup, relay: rl, envKey: envKey, homeKey: o.homeKey, token: token,
		baseOld: m.layerOutdated(envKey), gpu: o.gpu, api: o.api,
		born: time.Now(), clients: map[*client]struct{}{}, lastActive: time.Now(),
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	m.changed("open", s)

	// A restricted session's sandbox goes into its own resource-limited cgroup
	// leaf (D17d) — children (the shell, builds) follow the leader in.
	limited := o.restricted && m.Cgroup != nil && cmd.Process != nil
	if limited {
		m.Cgroup.Add("term-"+s.ID, cmd.Process.Pid)
	}

	go s.pump(func() {
		m.remove(s.ID)
		m.changed("close", s)
		revokeTok() // the session's API credential dies with it
		if envKey != "" {
			m.releaseEnv(envKey)
		}
		if limited {
			m.Cgroup.Remove("term-" + s.ID)
		}
	})
	slog.Info("terminal session created", "id", s.ID, "cwd", filepath.ToSlash(rel), "net", o.net, "restricted", o.restricted)
	return s, nil
}

// shellCmd builds the (unstarted) shell command: a rootfs sandbox when
// isolation is on, else a plain host shell. With isolation on, a sandbox that
// cannot be set up is an error — never a host shell, which would hand the
// session (a non-admin's, a coding agent's) xbind's own privileges (D78).
// Returns a cleanup for sandbox state, an optional postStart hook (run after
// the PTY starts) that wires the egress relay, and the persistent env-layer
// key this session holds ("" = none).
// homeDir is the session user's $HOME (homes/<user>); token the per-session
// terminal token (the shell's tile-scoped XBIN_TOKEN — "" = none). The last
// result is the entry's env (an agent session builds the agent's from it).
func (m *Manager) shellCmd(dir, rel, homeDir, token string, o openOpts) (*exec.Cmd, func(), func() *relay.Relay, string, []string, error) {
	if m.Isolate {
		if m.Rootfs == "" || !sandbox.Available() {
			return nil, nil, nil, "", nil, errors.New("terminal sandbox unavailable (isolation is on but there is no rootfs or user namespaces)")
		}
		cmd, cleanup, post, envKey, env, err := m.sandboxShell(dir, rel, homeDir, token, o)
		if err != nil {
			slog.Error("terminal sandbox setup failed", "err", err)
			return nil, nil, nil, "", nil, fmt.Errorf("the terminal sandbox could not be set up: %w", err)
		}
		return cmd, cleanup, post, envKey, env, nil
	}
	// isolation off: the workspace has no sandbox at all (tiles run as xbind)
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	cmd := exec.Command(shell) // exec-ok: isolation off — no sandbox exists; the terminal is the owner's shell as xbind
	if o.kind == KindAgent {   // the agent host as a plain child, leading its own group (agent.go)
		cmd = exec.Command(m.BxPath, "__agent-host") // exec-ok: isolation off, as the shell above
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	cmd.Dir = dir
	// Host HOME is dropped: even the fallback shell keeps dotfiles/agent config
	// in the per-user workspace home. XBIN_TOKEN likewise: only the session's
	// tile-scoped terminal token goes in, never an ambient owner token (getenv
	// is first-match, so filter, don't just append).
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "XBIN_TOKEN=") {
			continue
		}
		cmd.Env = append(cmd.Env, e)
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color", "COLORTERM=truecolor", "XBIN_COMPONENT="+rel)
	if os.Getenv("LANG") == "" {
		cmd.Env = append(cmd.Env, "LANG=C.UTF-8")
	}
	if m.Env != nil {
		for _, e := range m.Env() {
			if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "XBIN_TOKEN=") {
				continue
			}
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+homeDir)
	if token != "" {
		cmd.Env = append(cmd.Env, "XBIN_TOKEN="+token)
	}
	return cmd, func() {}, nil, "", cmd.Env, nil
}

// termKey is the per-component key for a terminal's persistent layer.
func termKey(rel string) string {
	if rel == "" {
		return "_root"
	}
	return util.CompKey(rel)
}

// acquireEnv reserves the persistent layer for key if free (only one live
// session may mount a given component's layer at a time — concurrent overlay
// mounts of the same upperdir would corrupt it). Returns false if already held.
func (m *Manager) acquireEnv(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.envHeld[key] {
		return false
	}
	m.envHeld[key] = true
	return true
}

func (m *Manager) releaseEnv(key string) {
	m.mu.Lock()
	delete(m.envHeld, key)
	m.mu.Unlock()
}

// ResetEnv wipes a component's persistent terminal layer back to the base rootfs.
// Any live session holding it is killed first (its overlay must be unmounted
// before the upperdir can be removed).
func (m *Manager) ResetEnv(rel string) error {
	key := termKey(rel)
	m.mu.Lock()
	var victims []*Session
	for _, s := range m.sessions {
		if s.envKey == key {
			victims = append(victims, s)
		}
	}
	m.mu.Unlock()
	for _, s := range victims {
		s.kill()
	}
	// Wait for the killed session(s) to fully tear down (pump → cleanup unmounts
	// the sandbox) so the upperdir is free before we remove it.
	for i := 0; i < 50; i++ {
		m.mu.Lock()
		held := m.envHeld[key]
		m.mu.Unlock()
		if !held {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return os.RemoveAll(filepath.Join(m.Root, ".xbin", "term", key))
}

// sandboxShell runs the shell in a rootfs sandbox (RT-4): the base rootfs, the
// workspace read-only except the session user's $HOME (homes/<user>) and this
// component's own dir (see scopedBinds) — the editing plane scoped to this
// component — and any read-only ExtraBinds (the SDK for `go build`). The
// overlay upper is a **persistent per-component layer** (`.xbin/term/<key>/`)
// so system-level changes (apt installs, /etc configs) survive across sessions
// — a resettable dev sandbox per component (plans/component-env.md). Only one
// live session may hold a component's layer; concurrent sessions on the same
// component fall back to an ephemeral upper. netMode picks the network scope.
func (m *Manager) sandboxShell(dir, rel, homeDir, token string, o openOpts) (*exec.Cmd, func(), func() *relay.Relay, string, []string, error) {
	binds := scopedBinds(m.Root, rel, homeDir, m.ExtraBinds, o.hide)
	viewDir := ""
	if o.restricted && rel != "" && o.rootFiles != nil { // D40 allow-list view
		vd, err := m.stageView(rel, o.homeKey, o.readable, o.rootFiles)
		if err != nil {
			return nil, nil, nil, "", nil, fmt.Errorf("stage terminal view: %w", err)
		}
		viewDir = vd
		binds = scopedBindsView(m.Root, rel, homeDir, viewDir, o.readable, m.ExtraBinds)
	}
	if o.kind == KindAgent { // the daemon's own bx, read-only, is the entry (agent.go)
		binds = append(binds, sandbox.Bind{Src: m.BxPath, Dst: agentHostPath, RO: true})
	}
	dropView := func() {
		if viewDir != "" {
			_ = os.RemoveAll(viewDir)
		}
	}
	env := m.sandboxEnv(rel, !o.netHost && o.net != NetNone, homeDir, token)
	// Owner-plane GPU access for the dev sandbox (?gpu=all|<index>).
	if o.gpu != "" && o.gpu != "none" {
		if gb, genv := gpu.Binds(gpu.Resolve([]string{"gpu:" + o.gpu})); len(gb) > 0 {
			binds = append(binds, gb...)
			env = append(env, genv...)
		}
	}
	spec := &sandbox.Spec{
		Lower:   []string{m.Rootfs},
		Binds:   binds,
		Entry:   "/bin/bash",
		Argv:    []string{"bash"},
		Env:     env,
		Cwd:     dir,
		HostUID: os.Getuid(),
		HostGID: os.Getgid(),
		// A component terminal carries the secret masks (scopedBinds); guard
		// them against umount by the root-in-userns shell, and (Landlock) deny
		// reading the secret files even if a mask is peeled. The (disabled) root
		// plane has no masks, so no guards.
		MountGuard: rel != "",
		// Non-admin user terminals additionally get the ns/cap lockdown (D18).
		Restricted: o.restricted && rel != "",
	}
	if o.kind == KindAgent {
		spec.Entry, spec.Argv = agentHostPath, []string{"bx", "__agent-host"}
	}
	if rel != "" {
		// AllowUnder: the own $HOME (under the otherwise-masked homes/), plus
		// every explicit read-only extra mount (the SDK for `go build`) — a
		// bind the sandbox itself makes must never be read-blocked, wherever
		// it lands (the /opt/xbin/sdk regression, 2026-07-12).
		allow := []string{homeDir, agentHostPath}
		for _, b := range m.ExtraBinds {
			allow = append(allow, b.Dst)
		}
		spec.ReadGuard = &sandbox.ReadGuardSpec{
			Root:       m.Root,
			SecretDirs: []string{".xbin", "data", "homes"},
			AllowUnder: allow,
		}
	}

	// Persistent per-component upper (if we can claim it), else ephemeral tmpfs.
	envKey := termKey(rel)
	if m.acquireEnv(envKey) {
		layer := filepath.Join(m.Root, ".xbin", "term", envKey)
		ver := m.ensureLayerBase(layer)        // stamp on first use (new→current, legacy→v0)
		base, ok := resolveBase(m.Rootfs, ver) // pin the upper to the base it was built on
		up, work := filepath.Join(layer, "upper"), filepath.Join(layer, "work")
		if !ok {
			// The base this layer was built on isn't installed — refuse rather
			// than corrupt its apt/dpkg state on a different base (the startup
			// gate normally prevents reaching here). Reset the terminal to upgrade.
			m.releaseEnv(envKey)
			dropView()
			return nil, nil, nil, "", nil, fmt.Errorf("this terminal's base image %q is not installed — reset the terminal to rebuild on the current base", ver)
		}
		if os.MkdirAll(up, 0o755) == nil && os.MkdirAll(work, 0o755) == nil {
			spec.Lower = []string{base}
			spec.Upper, spec.Work = up, work
		} else {
			m.releaseEnv(envKey)
			envKey = ""
		}
	} else {
		envKey = "" // someone else holds it → ephemeral, no persistence this session
	}

	// resolveNet already turned the scope into host / offline / a relay rule
	// list (net:internet, the org's union, or one named set — D54/D65).
	relayNet := false
	var pol sandbox.EgressPolicy
	switch {
	case o.netHost:
		spec.HostNet = true // owner escape hatch — LAN + host services, interfaces visible
	case o.net == NetNone:
		spec.Net = "none" // isolated netns, default-deny egress
	default:
		spec.Net = "relay" // own netns; the egress relay enforces the rules
		relayNet = true
		pol, _ = sandbox.Parse(o.netRules)
	}
	cmd, h, err := sandbox.Launch(spec)
	if err != nil {
		dropView()
		if envKey != "" {
			m.releaseEnv(envKey)
		}
		return nil, nil, nil, "", nil, err
	}

	hostFwd := m.hostForward()
	// post runs after the PTY starts: complete uid mapping (range mode) and, for
	// the relay scopes, stand up the egress relay on the init's TUN.
	post := func() *relay.Relay {
		if err := h.SetupUserns(); err != nil {
			slog.Warn("terminal sandbox: userns setup", "err", err)
		}
		if !relayNet || !h.NeedsRelay() {
			return nil
		}
		fd, err := h.RecvTUN()
		if err != nil {
			slog.Warn("terminal egress relay: recv tun (egress disabled)", "err", err)
			return nil
		}
		cfg := relay.Config{
			TunFD: fd, Allow: pol.Allow, Resolver: sandbox.HostResolver(),
			Gateway: netip.MustParseAddr(sandbox.GatewayIP), HostFwd: hostFwd,
		}
		if pol.HasHostRules() { // hostname rules need DNS pinning (D35)
			cfg.AllowHost = pol.AllowsHost
		}
		if pol.Empty() { // nothing reachable: no DNS either (no exfiltration channel)
			cfg.Resolver = ""
		}
		rl, err := relay.Start(cfg)
		if err != nil {
			slog.Warn("terminal egress relay: start (egress disabled)", "err", err)
			return nil
		}
		return rl
	}
	cleanup := func() {
		h.Cleanup()
		dropView()
	}
	return cmd, cleanup, post, envKey, env, nil
}

// hostForward maps the xbind listen port on the relay gateway IP to xbind on
// host loopback, so an internet-scope terminal (in its own netns) can still
// reach the workspace controller via XBIN_URL (bx/curl) without any host
// interface being exposed. Nil if the listen addr can't be parsed.
func (m *Manager) hostForward() map[int]string {
	_, portStr, err := net.SplitHostPort(m.Listen)
	if err != nil {
		return nil
	}
	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		return nil
	}
	return map[int]string{port: "127.0.0.1:" + portStr}
}

// sandboxEnv is the terminal env inside the rootfs: PATH points at the rootfs
// toolchains (not the host's), the session user's $HOME (homes/<user>), the
// session's tile-scoped XBIN_TOKEN (plans/terminal-tokens.md), plus
// XBIN_URL/WORKSPACE from m.Env(). In internet scope the netns can't reach
// xbind's 127.0.0.1 listener, so XBIN_URL is rewritten to the relay gateway
// host-forward.
func (m *Manager) sandboxEnv(rel string, relayNet bool, homeDir, termTok string) []string {
	env := []string{
		"TERM=xterm-256color", "COLORTERM=truecolor",
		"XBIN_COMPONENT=" + rel,
		"HOME=" + homeDir,
		"IN_SANDBOX=1", // scripts/agents can tell they're in the terminal sandbox
		"IS_SANDBOX=1", // the spelling agent CLIs (Claude Code) actually check
		"LANG=C.UTF-8",
		"PATH=/usr/local/go/bin:/usr/local/node/bin:/usr/local/bun/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	var xbinURL string
	if relayNet {
		if _, port, err := net.SplitHostPort(m.Listen); err == nil {
			xbinURL = "http://" + net.JoinHostPort(sandbox.GatewayIP, port)
		}
	}
	token := termTok // the session's tile-scoped credential, for env + git
	var effURL string
	if m.Env != nil {
		for _, e := range m.Env() {
			if strings.HasPrefix(e, "PATH=") {
				continue // the rootfs PATH above wins
			}
			if strings.HasPrefix(e, "HOME=") {
				continue // the per-user HOME above wins (getenv is first-match)
			}
			if strings.HasPrefix(e, "XBIN_TOKEN=") {
				continue // only the per-session terminal token goes in
			}
			if v, ok := strings.CutPrefix(e, "XBIN_URL="); ok {
				if xbinURL != "" {
					continue // rewritten below to the relay gateway
				}
				effURL = v
			}
			env = append(env, e)
		}
	}
	if token != "" {
		env = append(env, "XBIN_TOKEN="+token)
	}
	if xbinURL != "" {
		env = append(env, "XBIN_URL="+xbinURL)
		effURL = xbinURL
	}
	// Make the SDK's gateway host `http://xbin/…` work for raw git/curl in the
	// terminal too: git's env-config rewrites it to the reachable XBIN_URL and
	// attaches the session's tile-scoped bearer, pinned to that URL so the
	// token never goes anywhere else. This is what lets a template instance's
	// `template` remote fetch (plans/agent-v2.md); `curl http://xbin/…` works too.
	if effURL != "" && token != "" {
		env = append(env,
			"GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=url."+effURL+"/.insteadOf", "GIT_CONFIG_VALUE_0=http://xbin/",
			"GIT_CONFIG_KEY_1=http."+effURL+"/.extraHeader", "GIT_CONFIG_VALUE_1=Authorization: Bearer "+token,
		)
	}
	return env
}

// CanTouch reports whether p may operate on session id (kill/…): the session's
// creator, or an admin. Unknown ids are "touchable" so handlers return 404.
func (m *Manager) CanTouch(id string, p auth.Principal) bool {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	return s == nil || s.homeKey == HomeKey(p) || p.IsAdmin()
}

// Kill terminates a session by id: the shell is signalled, its PTY closes, and
// pump tears down the relay/sandbox. Used when the UI switches network scope
// (which must restart the session). Returns false if there is no such session.
func (m *Manager) Kill(id string) bool {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return false
	}
	s.kill()
	return true
}

func (m *Manager) remove(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

func (m *Manager) reaper() {
	for range time.Tick(time.Minute) {
		m.mu.Lock()
		for id, s := range m.sessions {
			s.mu.Lock()
			idle := len(s.clients) == 0 && time.Since(s.lastActive) > idleTimeout
			s.mu.Unlock()
			if idle {
				slog.Info("reaping idle terminal session", "id", id)
				s.kill() // pump's exit path removes it and tells the directory
			}
		}
		m.mu.Unlock()
	}
}

// pump reads PTY output, appends scrollback, and fans out to clients. It owns
// the session lifecycle: when the PTY closes (shell exit), the session dies.
func (s *Session) pump(onExit func()) {
	buf := make([]byte, 8192)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			out := make([]byte, n)
			copy(out, buf[:n])
			s.mu.Lock()
			s.scrollback = append(s.scrollback, out...)
			if len(s.scrollback) > maxScrollback {
				s.scrollback = s.scrollback[len(s.scrollback)-maxScrollback:]
			}
			s.lastActive = time.Now()
			for c := range s.clients {
				s.enqueueLocked(c, frame{b: out})
			}
			s.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	s.mu.Lock()
	s.dead = true
	for c := range s.clients {
		close(c.send)
	}
	s.clients = map[*client]struct{}{}
	s.mu.Unlock()
	waitErr := s.cmd.Wait()
	if s.relay != nil {
		s.relay.Close()
	}
	if s.cleanup != nil {
		s.cleanup()
	}
	onExit()
	// Exit status + uptime make an instantly-dying shell (sandbox init failure —
	// its stderr goes to the PTY, not the log) visible server-side: exit 127 +
	// sub-second uptime = the sandbox never reached the shell.
	slog.Info("terminal session ended", "id", s.ID, "uptime", time.Since(s.born).Round(time.Second), "exit", exitString(waitErr))
	// A session that dies this fast never showed anyone its output — surface
	// the PTY's last bytes (that's where "sandbox-init: <the actual error>"
	// went) in the log, or the failure is undiagnosable server-side.
	if time.Since(s.born) < 10*time.Second {
		s.mu.Lock()
		tail := scrollTail(s.scrollback, 2048) // room for the [sbx] debug trace + the error line
		s.mu.Unlock()
		if tail != "" {
			slog.Warn("terminal died at start; last output", "id", s.ID, "tail", tail)
		}
	}
}

// exitString renders a Wait error compactly ("ok", "exit status 127", …).
func exitString(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

// scrollTail renders the last n bytes of PTY scrollback as plain text for the
// log: ANSI escape sequences (CSI/OSC) and control bytes are stripped so the
// sandbox init's error line comes out readable.
func scrollTail(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == 0x1b: // ESC — skip the sequence it introduces
			if i+1 < len(b) && b[i+1] == '[' { // CSI: until a final byte @…~
				for i += 2; i < len(b) && (b[i] < 0x40 || b[i] > 0x7e); i++ {
				}
			} else if i+1 < len(b) && b[i+1] == ']' { // OSC: until BEL or ESC
				for i += 2; i < len(b) && b[i] != 0x07 && b[i] != 0x1b; i++ {
				}
			} else {
				i++
			}
		case c == '\n':
			out = append(out, ' ')
		case c >= 32 && c < 127:
			out = append(out, c)
		}
	}
	return strings.TrimSpace(string(out))
}

func (s *Session) kill() {
	if s.cmd.Process != nil {
		if s.pgid { // the non-isolated agent host and its agent: the whole group
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = s.cmd.Process.Kill()
	}
	if s.pty != nil {
		_ = s.pty.Close()
	}
}
