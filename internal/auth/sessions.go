package auth

import (
	"sort"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// --- sessions ---
//
// A session is a human login held server-side. Two transports carry its id:
//
//   - the browser's HttpOnly cookie (NewSession — password, invite, SSO,
//     view-as), and
//   - the native app's Authorization: Bearer (NewBearerSession — device-key
//     login, and the app's password / SSO-ticket login before a device is
//     enrolled; plans/native.md §5).
//
// Both resolve to the same human principal with the same lifetimes; a
// session id works only on the transport it was minted for, so a browser
// cookie can't be replayed as a bearer or the reverse. Every session carries
// a generation handle (gen): a random, non-secret name frame tokens embed so
// they die with the session that minted them (frametoken.go).

// session is a live login (server-side; the client holds only the id).
type session struct {
	userID     string
	created    time.Time // login time — the absolute-TTL anchor
	lastActive time.Time // last authenticated request — the idle-TTL anchor
	ip         string    // client IP at login
	lastIP     string    // client IP of the most recent authenticated request
	gen        string    // generation handle frame tokens bind to (not a credential)
	bearer     bool      // an app session (Authorization: Bearer), not a cookie
	deviceID   string    // the enrolled device behind a device-key session
	// notAfter, when set, caps the session below sessionAbsTTL: a device
	// login in SSO-only mode lives no longer than the IdP's last word
	// (the user's last SSO sign-in + sessionAbsTTL; devicelogin.go).
	notAfter time.Time
	// Impersonation (impersonate.go): who is looking, and how to hand the
	// browser back to them when they stop — their own session id, or the
	// owner token when they came in on the bootstrap cookie.
	impersonator   string
	restoreSession string
	restoreOwner   bool
}

// via names the principal channel of a session.
func (s *session) via() string {
	switch {
	case !s.bearer:
		return "session"
	case s.deviceID != "":
		return "device"
	}
	return "app"
}

// addSessionLocked registers s under a fresh id and generation handle
// (caller holds a.mu). Every session-creating path goes through here.
func (a *Auth) addSessionLocked(s *session) string {
	id := util.RandomToken(32)
	s.gen = util.RandomToken(12)
	a.sessions[id] = s
	a.gens.sessions[s.gen] = id
	a.indexSessionLocked(id) // tile-origin credentials bind to it (tilebinding.go)
	return id
}

// dropSessionLocked removes a session and its generation handle (caller
// holds a.mu) — the one delete path, so no frame token outlives its session.
func (a *Auth) dropSessionLocked(id string) {
	if s, ok := a.sessions[id]; ok {
		delete(a.gens.sessions, s.gen)
		delete(a.sessions, id)
	}
}

// expiredLocked reports whether a session is past its idle or absolute TTL
// (or its own cap).
func (a *Auth) expiredLocked(s *session, now time.Time) bool {
	return now.Sub(s.lastActive) > a.sessionIdleTTL || now.Sub(s.created) > a.sessionAbsTTL ||
		!s.notAfter.IsZero() && now.After(s.notAfter)
}

// SessionMaxTTL is the absolute session lifetime (XBIN_SESSION_MAX_TTL).
func (a *Auth) SessionMaxTTL() time.Duration { return a.sessionAbsTTL }

// LoginTime reports when the login session behind a human principal was
// opened (its password / SSO / device sign-in) — the freshness step-up
// checks read (enrolling a device, devicelogin.go). false: p did not
// authenticate with a live login session of its own user (owner token,
// tiles, terminals, a session a restart ended).
func (a *Auth) LoginTime(p Principal) (time.Time, bool) {
	h, ok := strings.CutPrefix(p.Gen, "s.")
	if !ok || p.UserID == "" || p.Component != "" {
		return time.Time{}, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	s := a.sessions[a.gens.sessions[h]]
	if s == nil || s.userID != p.UserID || a.expiredLocked(s, time.Now()) {
		return time.Time{}, false
	}
	return s.created, true
}

// NewSession creates a server-side browser session for a user, returning its
// id (the cookie value). ip is the client IP the login came from
// (attribution for the sessions API; also warms the IP for the /c/
// subresource gate).
func (a *Auth) NewSession(userID, ip string) string {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sweepSessionsLocked(now) // login is rare — opportunistic reap, no goroutine
	id := a.addSessionLocked(&session{userID: userID, created: now, lastActive: now, ip: ip, lastIP: ip})
	a.warmLocked(ip, now)
	return id
}

// BearerSession is what an app login hands back: the token and the two
// deadlines the app plans re-authentication around.
type BearerSession struct {
	Token       string
	ExpiresIdle time.Time // dies at this time unless used (slides with use)
	ExpiresMax  time.Time // hard cap since login
}

// NewBearerSession creates an app session (Authorization: Bearer) for a user.
// deviceID names the enrolled device for a device-key login ("" for the
// app's password / SSO-ticket login). Same lifetimes as a browser session.
func (a *Auth) NewBearerSession(userID, deviceID, ip string) BearerSession {
	return a.NewBearerSessionUntil(userID, deviceID, ip, time.Time{})
}

// NewBearerSessionUntil is NewBearerSession capped at notAfter (zero: no cap
// below the usual lifetimes) — a device login under SSO-only mode.
func (a *Auth) NewBearerSessionUntil(userID, deviceID, ip string, notAfter time.Time) BearerSession {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sweepSessionsLocked(now)
	id := a.addSessionLocked(&session{userID: userID, created: now, lastActive: now, ip: ip, lastIP: ip,
		bearer: true, deviceID: deviceID, notAfter: notAfter})
	a.warmLocked(ip, now)
	bs := BearerSession{Token: id, ExpiresIdle: now.Add(a.sessionIdleTTL), ExpiresMax: now.Add(a.sessionAbsTTL)}
	if !notAfter.IsZero() && notAfter.Before(bs.ExpiresMax) {
		bs.ExpiresMax = notAfter
	}
	if bs.ExpiresMax.Before(bs.ExpiresIdle) {
		bs.ExpiresIdle = bs.ExpiresMax
	}
	return bs
}

// DropSession invalidates a browser session (logout).
func (a *Auth) DropSession(id string) {
	a.mu.Lock()
	a.dropSessionLocked(id)
	a.mu.Unlock()
	a.saveGens() // an ended login must not come back as an orphan after a crash
}

// DropBearerSession ends an app session by its token (the app's sign-out),
// reporting whose it was — the user and, for a device-key login, the
// enrolled device. ok=false when tok is not a live bearer session.
func (a *Auth) DropBearerSession(tok string) (userID, deviceID string, ok bool) {
	a.mu.Lock()
	s, ok := a.sessions[tok]
	if ok && s.bearer {
		a.dropSessionLocked(tok)
	}
	a.mu.Unlock()
	if !ok || !s.bearer {
		return "", "", false
	}
	a.saveGens()
	return s.userID, s.deviceID, true
}

// DropUserSessions ends every session of one user — browser and app alike
// ("sign out everywhere", D53; also disable and delete) — revokes the
// terminal tokens minted for them, so shells they had open lose their API
// credential too (the socket itself stays attached until it reconnects),
// bumps the user's credential generation, so every frame token minted for
// them dies with it (frametoken.go) — the tiles of logins a restart ended
// included — and voids the enrollment codes and app sign-in tickets still
// pending for them, so a code minted just before can't enroll a device
// after. Enrolled devices stay (the callers decide: broker devicesapi.go).
// Returns the number of sessions dropped.
func (a *Auth) DropUserSessions(userID string) int {
	a.mu.Lock()
	n := 0
	for id, s := range a.sessions {
		if s.userID == userID {
			a.dropSessionLocked(id)
			n++
		}
	}
	for h, s := range a.gens.orphans {
		if s.userID == userID {
			delete(a.gens.orphans, h)
		}
	}
	for tok, t := range a.terminals {
		if t.userID == userID {
			delete(a.terminals, tok)
		}
	}
	a.gens.users[userID]++
	a.dev.dropUserLocked(userID)
	a.mu.Unlock()
	a.saveGens() // the bump must outlive a crash
	return n
}

// DropDeviceSessions ends every session opened with one device's key (the
// device was revoked). Their frame tokens die with them. Returns the count.
func (a *Auth) DropDeviceSessions(deviceID string) int {
	if deviceID == "" {
		return 0
	}
	a.mu.Lock()
	n := 0
	for id, s := range a.sessions {
		if s.deviceID == deviceID {
			a.dropSessionLocked(id)
			n++
		}
	}
	for h, s := range a.gens.orphans {
		if s.deviceID == deviceID {
			delete(a.gens.orphans, h)
		}
	}
	a.mu.Unlock()
	a.saveGens()
	return n
}

// sessionView is what a live lookup reports about a session.
type sessionView struct {
	userID, impersonator, gen, deviceID, via string
}

// sessionUser resolves a session id presented on one transport (bearer or
// cookie) to its user, enforcing expiry: a session dies after sessionIdleTTL
// of inactivity (sliding) or sessionAbsTTL since login (hard cap), whichever
// first — so a stolen credential can't authenticate forever. A live lookup
// slides the idle window and records the caller's IP (sessions-API
// attribution). A session presented on the other transport doesn't exist.
func (a *Auth) sessionUser(id, ip string, bearer bool) (sessionView, bool) {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[id]
	if !ok || s.bearer != bearer {
		return sessionView{}, false
	}
	if a.expiredLocked(s, now) {
		a.dropSessionLocked(id)
		return sessionView{}, false
	}
	s.lastActive = now
	if ip != "" {
		s.lastIP = ip
	}
	return sessionView{userID: s.userID, impersonator: s.impersonator, gen: "s." + s.gen, deviceID: s.deviceID, via: s.via()}, true
}

// bearerSessionPrincipal resolves an app session token to the human
// principal — the same one a browser cookie of that user resolves to
// (disabled or deleted users refuse).
func (a *Auth) bearerSessionPrincipal(tok, ip string) (Principal, bool) {
	sv, ok := a.sessionUser(tok, ip, true)
	if !ok {
		return Principal{}, false
	}
	u, found := a.userSnapshot(sv.userID)
	if !found {
		return Principal{}, false
	}
	return Principal{UserID: sv.userID, User: u, Access: a.accessSnapshot(sv.userID), Via: sv.via,
		DeviceID: sv.deviceID, Gen: sv.gen}, true
}

// sweepSessionsLocked drops expired sessions so abandoned logins don't grow the
// map unbounded (caller holds a.mu).
func (a *Auth) sweepSessionsLocked(now time.Time) {
	for id, s := range a.sessions {
		if a.expiredLocked(s, now) {
			a.dropSessionLocked(id)
		}
	}
	for h, s := range a.gens.orphans {
		if a.expiredLocked(s, now) {
			delete(a.gens.orphans, h)
		}
	}
	a.sweepSessionRefsLocked(now)
	a.sweepWarmLocked(now)
	a.dev.sweepLocked(now)
}

// SessionInfo is the admin view of one live session (GET /api/xbin/sessions).
// The session id is a credential: it NEVER leaves this package in serialized
// form — ID is exported only so the handler can mark the caller's own row;
// never put it on the wire.
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
	// Via is session (browser cookie), device (the app, device key) or app
	// (the app, password / SSO ticket); DeviceID names the device.
	Via      string
	DeviceID string
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
			IP: s.ip, LastIP: s.lastIP, Impersonator: s.impersonator, Via: s.via(), DeviceID: s.deviceID,
		})
	}
	a.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].LastActive.After(out[j].LastActive) })
	return out
}
