package auth

import (
	"sort"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// --- sessions ---

// NewSession creates a server-side session for a user, returning its id.
// ip is the client IP the login came from (attribution for the sessions
// API; also warms the IP for the /c/ subresource gate).
func (a *Auth) NewSession(userID, ip string) string {
	id := util.RandomToken(32)
	now := time.Now()
	a.mu.Lock()
	a.sweepSessionsLocked(now) // login is rare — opportunistic reap, no goroutine
	a.sessions[id] = &session{userID: userID, created: now, lastActive: now, ip: ip, lastIP: ip}
	a.warmLocked(ip, now)
	a.mu.Unlock()
	return id
}

// DropSession invalidates a session (logout).
func (a *Auth) DropSession(id string) {
	a.mu.Lock()
	delete(a.sessions, id)
	a.mu.Unlock()
}

// DropUserSessions ends every browser session of one user ("sign out
// everywhere", D53) and revokes the terminal tokens minted for them, so
// shells they had open lose their API credential too (the socket itself
// stays attached until it reconnects). Frame tokens are stateless HMACs and
// simply expire (frameTokenTTL). Returns the number of sessions dropped.
func (a *Auth) DropUserSessions(userID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for id, s := range a.sessions {
		if s.userID == userID {
			delete(a.sessions, id)
			n++
		}
	}
	for tok, t := range a.terminals {
		if t.userID == userID {
			delete(a.terminals, tok)
		}
	}
	return n
}

// sessionUser resolves a session id to its user, enforcing expiry: a session
// dies after sessionIdleTTL of inactivity (sliding) or sessionAbsTTL since
// login (hard cap), whichever first — so a stolen cookie can't authenticate
// forever. A live lookup slides the idle window and records the caller's IP
// (sessions-API attribution). impersonator names the admin looking through
// the session when it is a view of the user ("" otherwise; impersonate.go).
func (a *Auth) sessionUser(id, ip string) (userID, impersonator string, ok bool) {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[id]
	if !ok {
		return "", "", false
	}
	if now.Sub(s.lastActive) > a.sessionIdleTTL || now.Sub(s.created) > a.sessionAbsTTL {
		delete(a.sessions, id)
		return "", "", false
	}
	s.lastActive = now
	if ip != "" {
		s.lastIP = ip
	}
	return s.userID, s.impersonator, true
}

// sweepSessionsLocked drops expired sessions so abandoned logins don't grow the
// map unbounded (caller holds a.mu).
func (a *Auth) sweepSessionsLocked(now time.Time) {
	for id, s := range a.sessions {
		if now.Sub(s.lastActive) > a.sessionIdleTTL || now.Sub(s.created) > a.sessionAbsTTL {
			delete(a.sessions, id)
		}
	}
	a.sweepWarmLocked(now)
}

// SessionInfo is the admin view of one live browser session (GET
// /api/xbin/sessions). The session id is a credential: it NEVER leaves this
// package in serialized form — ID is exported only so the handler can mark
// the caller's own row; never put it on the wire.
type SessionInfo struct {
	ID         string
	UserID     string
	Created    time.Time
	LastActive time.Time
	IP         string // client IP at login
	LastIP     string // client IP of the most recent request
	// Impersonator names the admin looking through this session ("owner"
	// or a user id) — the session is a read-only view of UserID's workspace.
	Impersonator string
}

// Sessions lists live sessions for the admin sessions view (newest activity
// first). Expired ones are reaped on the way.
func (a *Auth) Sessions() []SessionInfo {
	now := time.Now()
	a.mu.Lock()
	a.sweepSessionsLocked(now)
	out := make([]SessionInfo, 0, len(a.sessions))
	for id, s := range a.sessions {
		out = append(out, SessionInfo{
			ID: id, UserID: s.userID, Created: s.created, LastActive: s.lastActive,
			IP: s.ip, LastIP: s.lastIP, Impersonator: s.impersonator,
		})
	}
	a.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].LastActive.After(out[j].LastActive) })
	return out
}
