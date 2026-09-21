package term

// The session DIRECTORY (D73): which live sessions are a user's, per tile,
// with the tab name and the pickers each was opened with. Sessions were
// always owned per user server-side (homeKey); what the browser remembered —
// which ids belong on which tile — lived in its localStorage, so a session
// was reachable only from the browser that opened it and a user switch on
// one browser inherited the previous user's tab list. Now the server answers
// "what are mine here", tells the owner's other browsers when that changes
// (OnChange → a `term` event), and a tab's name lives on the session.

import (
	"sort"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

// SessionInfo is one row of the directory (GET /api/xbin/term/sessions).
type SessionInfo struct {
	ID         string  `json:"id"`
	Cwd        string  `json:"cwd"`
	Net        string  `json:"net"`
	Label      string  `json:"label"`
	Scopes     []Scope `json:"scopes"`
	GPU        string  `json:"gpu"`
	API        bool    `json:"api"`
	Name       string  `json:"name"`
	Created    string  `json:"created"`
	LastActive string  `json:"lastActive"`
	Clients    int     `json:"clients"` // sockets attached right now (another browser, a second tab)
	EnvHeld    bool    `json:"envHeld"` // this session holds the tile's persistent layer
}

func (s *Session) info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	scopes := s.Scopes
	if scopes == nil {
		scopes = []Scope{}
	}
	return SessionInfo{
		ID: s.ID, Cwd: s.Cwd, Net: s.Net, Label: s.Label, Scopes: scopes,
		GPU: s.gpu, API: s.api, Name: s.name,
		Created:    s.born.UTC().Format(time.RFC3339),
		LastActive: s.lastActive.UTC().Format(time.RFC3339),
		Clients:    len(s.clients), EnvHeld: s.envKey != "",
	}
}

// sorted returns the live sessions ordered by creation (m.sessions is a map).
func (m *Manager) sorted() []*Session {
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
	return sessions
}

// ListFor is the directory for one user: their live sessions (all tiles, or
// one when cwd != ""), oldest first — minus the tiles they may no longer open
// a terminal on (`may` is the caller's CanTerminalTile; nil = no filter, for
// an admin listing another user's sessions). Never nil.
func (m *Manager) ListFor(homeKey, cwd string, may func(rel string) bool) []SessionInfo {
	out := []SessionInfo{}
	for _, s := range m.sorted() {
		if s.homeKey != homeKey || (cwd != "" && s.Cwd != cwd) {
			continue
		}
		if may != nil && !may(s.Cwd) {
			continue
		}
		out = append(out, s.info())
	}
	return out
}

// Rename sets a session's tab name. false = no such session. Empty clears.
func (m *Manager) Rename(id, name string) bool {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return false
	}
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
	m.changed("rename", s)
	return true
}

// Owner reports whose session id is ("" = none) — for the rename gate.
func (m *Manager) Owner(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[id]; s != nil {
		return s.homeKey
	}
	return ""
}

// mayReattach is the reattach gate: the creator or an admin, and — for a
// non-admin — still terminal-level on the tile (a revoked level closes the
// door to sessions already open there; the session itself lives on until
// killed or reaped). "" = allowed, else the refusal.
func (s *Session) mayReattach(p auth.Principal) string {
	if p.IsAdmin() {
		return ""
	}
	if s.homeKey != HomeKey(p) {
		return "session belongs to another user"
	}
	if !p.CanTerminalTile(s.Cwd) {
		return "terminal access to this tile was revoked"
	}
	return ""
}

// changed tells the directory's listeners (the server publishes a `term`
// event to the owner's browsers) that a session was opened, closed or
// renamed. Nil-safe; never called under m.mu.
func (m *Manager) changed(op string, s *Session) {
	if m.OnChange != nil {
		m.OnChange(op, s.homeKey, s.ID, s.Cwd)
	}
}
