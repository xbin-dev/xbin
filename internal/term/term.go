// Package term implements persistent PTY terminal sessions behind /ws/term.
//
// Sessions are owner-privileged (the editing plane): shells run as the xbind
// user with the owner token in env, cwd'd to a component's source directory.
// A session outlives its WebSocket — reattach by id replays bounded
// scrollback. Wire protocol in docs/protocol.md: binary frames are raw PTY
// bytes; text frames are JSON control messages.
package term

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"github.com/magik6k/xbin/internal/gpu"
	"github.com/magik6k/xbin/internal/sandbox"
	"github.com/magik6k/xbin/internal/sandbox/relay"
	"github.com/magik6k/xbin/internal/util"
)

const (
	maxScrollback = 256 << 10 // per session
	maxSessions   = 64
	idleTimeout   = 24 * time.Hour
)

// Network scope for a terminal session (query ?net=). The netns/relay is fixed
// at spawn, so switching scope restarts the session (the frontend opens a fresh
// WS with a new ?net=). Default is internet: own netns, no host interfaces.
const (
	NetInternet = "internet" // own netns + egress relay, net:internet only (default)
	NetHost     = "host"     // share the host network (LAN + host services visible)
	NetNone     = "none"     // isolated netns, no egress (airgapped; xbind unreachable)
)

// normalizeNet clamps an incoming ?net= value to a known scope (default internet).
func normalizeNet(s string) string {
	switch s {
	case NetHost, NetNone:
		return s
	default:
		return NetInternet
	}
}

type control struct {
	Op   string `json:"op"` // resize|ping
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

type client struct {
	conn *websocket.Conn
	send chan []byte // PTY output frames
}

type Session struct {
	ID      string
	Cwd     string // workspace-relative component path
	Net     string // network scope (NetInternet|NetHost|NetNone)
	cmd     *exec.Cmd
	pty     *os.File
	cleanup func()       // sandbox spec temp cleanup (nil for a plain shell)
	relay   *relay.Relay // egress relay (internet scope only; nil otherwise)
	envKey  string       // persistent per-component layer this session holds ("" = none/ephemeral)
	born    time.Time

	mu         sync.Mutex
	scrollback []byte
	clients    map[*client]struct{}
	lastActive time.Time
	dead       bool
}

type Manager struct {
	Root     string          // workspace root
	Listen   string          // xbind's listen addr (host:port) — for the relay host-forward
	Env      func() []string // extra env for shells (token, HOME, …)
	upgrader websocket.Upgrader

	// Isolate + Rootfs run terminals in a rootfs sandbox (plans/runtime.md RT-4):
	// the base rootfs userland (toolchains + agent CLIs), the workspace mounted
	// read-write (editing plane), a persistent $HOME (shared agent config), and a
	// per-session network scope (default: own netns + internet-only egress relay,
	// so host interfaces stay hidden). ExtraBinds add read-only mounts (e.g. the
	// SDK source so `go build` resolves). Off ⇒ a plain host shell.
	Isolate    bool
	Rootfs     string
	ExtraBinds []sandbox.Bind

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
	} else {
		s, err = m.create(cwd, netMode, gpuMode)
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
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].born.Equal(sessions[j].born) {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].born.Before(sessions[j].born)
	})
	out := []map[string]any{}
	for _, s := range sessions {
		s.mu.Lock()
		out = append(out, map[string]any{
			"id": s.ID, "cwd": s.Cwd, "net": s.Net, "clients": len(s.clients),
			"created": s.born.UTC().Format(time.RFC3339),
		})
		s.mu.Unlock()
	}
	return out
}

func (m *Manager) create(cwd, netMode, gpuMode string) (*Session, error) {
	dir := m.Root
	rel := ""
	if cwd != "" {
		var err error
		dir, rel, err = util.SafeJoin(m.Root, cwd)
		if err != nil {
			return nil, err
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("cwd %q is not a directory", cwd)
		}
	}

	m.mu.Lock()
	if len(m.sessions) >= maxSessions {
		m.mu.Unlock()
		return nil, fmt.Errorf("session limit (%d) reached", maxSessions)
	}
	m.mu.Unlock()

	cmd, cleanup, postStart, envKey := m.shellCmd(dir, rel, netMode, gpuMode)

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 32})
	if err != nil {
		cleanup()
		if envKey != "" {
			m.releaseEnv(envKey)
		}
		return nil, fmt.Errorf("spawn shell: %w", err)
	}
	// The egress relay can only start once init has created the TUN in its netns
	// (post-fork), so wire it up after StartWithSize.
	var rl *relay.Relay
	if postStart != nil {
		rl = postStart()
	}

	s := &Session{
		ID: util.RandomToken(8), Cwd: rel, Net: netMode, cmd: cmd, pty: f,
		cleanup: cleanup, relay: rl, envKey: envKey,
		born: time.Now(), clients: map[*client]struct{}{}, lastActive: time.Now(),
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()

	go s.pump(func() {
		m.remove(s.ID)
		if envKey != "" {
			m.releaseEnv(envKey)
		}
	})
	slog.Info("terminal session created", "id", s.ID, "cwd", filepath.ToSlash(rel), "net", netMode)
	return s, nil
}

// shellCmd builds the (unstarted) shell command: a rootfs sandbox when
// isolation is on, else a plain host shell. Returns a cleanup for sandbox state,
// an optional postStart hook (run after the PTY starts) that wires the egress
// relay, and the persistent env-layer key this session holds ("" = none).
func (m *Manager) shellCmd(dir, rel, netMode, gpuMode string) (*exec.Cmd, func(), func() *relay.Relay, string) {
	if m.Isolate && m.Rootfs != "" && sandbox.Available() {
		if cmd, cleanup, post, envKey, err := m.sandboxShell(dir, rel, netMode, gpuMode); err == nil {
			return cmd, cleanup, post, envKey
		} else {
			slog.Warn("terminal sandbox setup failed; falling back to host shell", "err", err)
		}
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	cmd := exec.Command(shell)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor", "XBIN_COMPONENT="+rel)
	if os.Getenv("LANG") == "" {
		cmd.Env = append(cmd.Env, "LANG=C.UTF-8")
	}
	if m.Env != nil {
		cmd.Env = append(cmd.Env, m.Env()...)
	}
	return cmd, func() {}, nil, ""
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

// scopedBinds builds a terminal's workspace binds (plans/runtime.md).
//
//   - A ROOT terminal (rel == "") is the owner plane: the whole workspace
//     read-write (edit anything, create components, workspace-level git).
//   - A COMPONENT terminal (rel != "") is isolated: the workspace is read-only
//     EXCEPT $HOME and this component's own directory (its code + its own .git
//     repo). So a rogue agent can only touch its component and $HOME — never
//     workspace state (xbin.json, AGENTS.md, go.work), runtime data (data/,
//     .xbin/), or other components. Commits work because each component is its
//     own git repo, writable inside the component dir even while the root is ro.
//
// Read-only ExtraBinds (e.g. the SDK) are appended last. rw binds go AFTER the
// ro root so they shadow it at their paths.
func scopedBinds(root, rel string, extra []sandbox.Bind) []sandbox.Bind {
	if rel == "" {
		return append([]sandbox.Bind{{Src: root, Dst: root}}, extra...) // owner plane: all rw
	}
	binds := []sandbox.Bind{{Src: root, Dst: root, RO: true}} // workspace: read-only
	if home := filepath.Join(root, "home"); pathIsDir(home) {
		binds = append(binds, sandbox.Bind{Src: home, Dst: home}) // $HOME: read-write
	}
	comp := filepath.Join(root, filepath.FromSlash(rel))
	binds = append(binds, sandbox.Bind{Src: comp, Dst: comp}) // this component: read-write
	return append(binds, extra...)
}

func pathIsDir(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }

// sandboxShell runs the shell in a rootfs sandbox (RT-4): the base rootfs, the
// workspace read-only except $HOME and this component's own dir (see scopedBinds)
// — the editing plane scoped to this component — and any
// read-only ExtraBinds (the SDK for `go build`). The overlay upper is a
// **persistent per-component layer** (`.xbin/term/<key>/`) so system-level
// changes (apt installs, /etc configs) survive across sessions — a resettable
// dev sandbox per component (plans/component-env.md). Only one live session may
// hold a component's layer; concurrent sessions on the same component fall back
// to an ephemeral upper. netMode picks the network scope (internet/host/none).
func (m *Manager) sandboxShell(dir, rel, netMode, gpuMode string) (*exec.Cmd, func(), func() *relay.Relay, string, error) {
	binds := scopedBinds(m.Root, rel, m.ExtraBinds)
	env := m.sandboxEnv(rel, netMode)
	// Owner-plane GPU access for the dev sandbox (?gpu=all|<index>).
	if gpuMode != "" && gpuMode != "none" {
		if gb, genv := gpu.Binds(gpu.Resolve([]string{"gpu:" + gpuMode})); len(gb) > 0 {
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
	}

	// Persistent per-component upper (if we can claim it), else ephemeral tmpfs.
	envKey := termKey(rel)
	if m.acquireEnv(envKey) {
		layer := filepath.Join(m.Root, ".xbin", "term", envKey)
		up, work := filepath.Join(layer, "upper"), filepath.Join(layer, "work")
		if os.MkdirAll(up, 0o755) == nil && os.MkdirAll(work, 0o755) == nil {
			spec.Upper, spec.Work = up, work
		} else {
			m.releaseEnv(envKey)
			envKey = ""
		}
	} else {
		envKey = "" // someone else holds it → ephemeral, no persistence this session
	}

	switch netMode {
	case NetHost:
		spec.HostNet = true // owner escape hatch — LAN + host services, interfaces visible
	case NetNone:
		spec.Net = "none" // isolated netns, default-deny egress
	default: // NetInternet
		spec.Net = "relay" // own netns; egress relay enforces net:internet
	}
	cmd, h, err := sandbox.Launch(spec)
	if err != nil {
		if envKey != "" {
			m.releaseEnv(envKey)
		}
		return nil, nil, nil, "", err
	}

	pol, _ := sandbox.Parse([]string{"net:internet"})
	hostFwd := m.hostForward()
	// post runs after the PTY starts: complete uid mapping (range mode) and, for
	// the internet scope, stand up the egress relay on the init's TUN.
	post := func() *relay.Relay {
		if err := h.SetupUserns(); err != nil {
			slog.Warn("terminal sandbox: userns setup", "err", err)
		}
		if netMode != NetInternet || !h.NeedsRelay() {
			return nil
		}
		fd, err := h.RecvTUN()
		if err != nil {
			slog.Warn("terminal egress relay: recv tun (egress disabled)", "err", err)
			return nil
		}
		rl, err := relay.Start(relay.Config{
			TunFD: fd, Allow: pol.Allow, Resolver: sandbox.HostResolver(),
			Gateway: netip.MustParseAddr(sandbox.GatewayIP), HostFwd: hostFwd,
		})
		if err != nil {
			slog.Warn("terminal egress relay: start (egress disabled)", "err", err)
			return nil
		}
		return rl
	}
	return cmd, h.Cleanup, post, envKey, nil
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
// toolchains (not the host's), plus XBIN_URL/TOKEN/WORKSPACE/HOME from m.Env().
// In internet scope the netns can't reach xbind's 127.0.0.1 listener, so
// XBIN_URL is rewritten to the relay gateway host-forward.
func (m *Manager) sandboxEnv(rel, netMode string) []string {
	env := []string{
		"TERM=xterm-256color", "COLORTERM=truecolor",
		"XBIN_COMPONENT=" + rel,
		"IN_SANDBOX=1", // scripts/agents can tell they're in the terminal sandbox
		"IS_SANDBOX=1", // the spelling agent CLIs (Claude Code) actually check
		"LANG=C.UTF-8",
		"PATH=/usr/local/go/bin:/usr/local/node/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	var xbinURL string
	if netMode == NetInternet {
		if _, port, err := net.SplitHostPort(m.Listen); err == nil {
			xbinURL = "http://" + net.JoinHostPort(sandbox.GatewayIP, port)
		}
	}
	var effURL, token string // the reachable xbind URL + owner token, for git/curl
	if m.Env != nil {
		for _, e := range m.Env() {
			if strings.HasPrefix(e, "PATH=") {
				continue // the rootfs PATH above wins
			}
			if v, ok := strings.CutPrefix(e, "XBIN_URL="); ok {
				if xbinURL != "" {
					continue // rewritten below to the relay gateway
				}
				effURL = v
			}
			if v, ok := strings.CutPrefix(e, "XBIN_TOKEN="); ok {
				token = v
			}
			env = append(env, e)
		}
	}
	if xbinURL != "" {
		env = append(env, "XBIN_URL="+xbinURL)
		effURL = xbinURL
	}
	// Make the SDK's gateway host `http://xbin/…` work for raw git/curl in the
	// terminal too: git's env-config rewrites it to the reachable XBIN_URL and
	// attaches the owner bearer token, scoped to that URL so the token never
	// goes anywhere else. This is what lets a template instance's `template`
	// remote fetch (plans/agent-v2.md); it also makes `curl http://xbin/…` work.
	if effURL != "" && token != "" {
		env = append(env,
			"GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=url."+effURL+"/.insteadOf", "GIT_CONFIG_VALUE_0=http://xbin/",
			"GIT_CONFIG_KEY_1=http."+effURL+"/.extraHeader", "GIT_CONFIG_VALUE_1=Authorization: Bearer "+token,
		)
	}
	return env
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
				s.kill()
				delete(m.sessions, id)
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
				select {
				case c.send <- out:
				default: // slow client: drop it, it can reattach
					delete(s.clients, c)
					close(c.send)
				}
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
	_ = s.cmd.Wait()
	if s.relay != nil {
		s.relay.Close()
	}
	if s.cleanup != nil {
		s.cleanup()
	}
	onExit()
	slog.Info("terminal session ended", "id", s.ID)
}

func (s *Session) kill() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.pty.Close()
}

func (s *Session) attach(conn *websocket.Conn) {
	c := &client{conn: conn, send: make(chan []byte, 64)}

	s.mu.Lock()
	if s.dead {
		s.mu.Unlock()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"op":"exit"}`))
		conn.Close()
		return
	}
	sb := make([]byte, len(s.scrollback))
	copy(sb, s.scrollback)
	s.clients[c] = struct{}{}
	s.lastActive = time.Now()
	s.mu.Unlock()

	// Writer: session id first (browsers can't read upgrade headers), then
	// scrollback replay (new output is queued in c.send behind it, preserving
	// order), then live stream.
	go func() {
		_ = conn.WriteMessage(websocket.TextMessage,
			[]byte(fmt.Sprintf(`{"op":"session","id":"%s","net":"%s"}`, s.ID, s.Net)))
		if len(sb) > 0 {
			if err := conn.WriteMessage(websocket.BinaryMessage, sb); err != nil {
				return
			}
		}
		for out := range c.send {
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.BinaryMessage, out); err != nil {
				return
			}
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"op":"exit"}`))
		conn.Close()
	}()

	// Reader: browser input → PTY; control frames.
	go func() {
		defer func() {
			s.mu.Lock()
			if _, ok := s.clients[c]; ok {
				delete(s.clients, c)
				close(c.send)
			}
			s.lastActive = time.Now()
			s.mu.Unlock()
			conn.Close()
		}()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			switch mt {
			case websocket.BinaryMessage:
				if _, err := s.pty.Write(data); err != nil {
					return
				}
			case websocket.TextMessage:
				var ctl control
				if json.Unmarshal(data, &ctl) != nil {
					continue
				}
				if ctl.Op == "resize" && ctl.Cols > 0 && ctl.Rows > 0 {
					_ = pty.Setsize(s.pty, &pty.Winsize{
						Cols: uint16(ctl.Cols), Rows: uint16(ctl.Rows),
					})
				}
			}
		}
	}()
}
