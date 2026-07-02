// Package term implements persistent PTY terminal sessions behind /ws/term.
//
// Sessions are owner-privileged (the editing plane): shells run as the buxond
// user with the owner token in env, cwd'd to a component's source directory.
// A session outlives its WebSocket — reattach by id replays bounded
// scrollback. Wire protocol in docs/protocol.md: binary frames are raw PTY
// bytes; text frames are JSON control messages.
package term

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"github.com/magik6k/buxon/internal/util"
)

const (
	maxScrollback = 256 << 10 // per session
	maxSessions   = 64
	idleTimeout   = 24 * time.Hour
)

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
	ID   string
	Cwd  string // workspace-relative component path
	cmd  *exec.Cmd
	pty  *os.File
	born time.Time

	mu         sync.Mutex
	scrollback []byte
	clients    map[*client]struct{}
	lastActive time.Time
	dead       bool
}

type Manager struct {
	Root     string          // workspace root
	Env      func() []string // extra env for shells (token, HOME, …)
	upgrader websocket.Upgrader

	mu       sync.Mutex
	sessions map[string]*Session
}

func NewManager(root string, env func() []string) *Manager {
	m := &Manager{
		Root: root, Env: env,
		sessions: map[string]*Session{},
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
// Query: cwd=<component-path> (new session) or session=<id> (reattach).
func (m *Manager) ServeWS(w http.ResponseWriter, r *http.Request) {
	sessID := r.URL.Query().Get("session")
	cwd := r.URL.Query().Get("cwd")

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
		s, err = m.create(cwd)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	conn, err := m.upgrader.Upgrade(w, r, http.Header{
		"X-Buxon-Session": []string{s.ID},
	})
	if err != nil {
		return
	}
	s.attach(conn)
}

// List returns session metadata for the status API.
func (m *Manager) List() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []map[string]any{}
	for _, s := range m.sessions {
		s.mu.Lock()
		out = append(out, map[string]any{
			"id": s.ID, "cwd": s.Cwd, "clients": len(s.clients),
			"created": s.born.UTC().Format(time.RFC3339),
		})
		s.mu.Unlock()
	}
	return out
}

func (m *Manager) create(cwd string) (*Session, error) {
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

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	cmd := exec.Command(shell)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"BUXON_COMPONENT="+rel,
	)
	if os.Getenv("LANG") == "" {
		cmd.Env = append(cmd.Env, "LANG=C.UTF-8")
	}
	if m.Env != nil {
		cmd.Env = append(cmd.Env, m.Env()...)
	}

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 32})
	if err != nil {
		return nil, fmt.Errorf("spawn shell: %w", err)
	}

	s := &Session{
		ID: util.RandomToken(8), Cwd: rel, cmd: cmd, pty: f,
		born: time.Now(), clients: map[*client]struct{}{}, lastActive: time.Now(),
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()

	go s.pump(func() { m.remove(s.ID) })
	slog.Info("terminal session created", "id", s.ID, "cwd", filepath.ToSlash(rel))
	return s, nil
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
			[]byte(fmt.Sprintf(`{"op":"session","id":"%s"}`, s.ID)))
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
