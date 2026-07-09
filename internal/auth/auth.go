// Package auth implements xbin's principals (plans/auth.md):
//
//   - Owner: the human. Authenticated by the owner token — via login cookie
//     (browser) or Authorization: Bearer (bx, curl, terminals).
//   - Element instance: a running backend generation, authenticated by a
//     per-generation instance token minted by the runner.
//   - Element frontend: owner cookie + frame token (minted into served HTML
//     at the D4 injection point) attributing the request to a component.
//   - Terminal: a per-session bearer scoping a tile terminal's shell to that
//     tile's element principal (plans/terminal-tokens.md) — the shell acts as
//     the tile, not as the human who opened it.
//
// The gateway/permission decisions live in internal/broker/policy; this
// package only answers "who is calling".
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/magik6k/xbin/internal/users"
	"github.com/magik6k/xbin/internal/util"
)

const (
	CookieName       = "xbin_session"
	FrameTokenHeader = "X-XBin-Frame-Token"
)

// Principal identifies a verified caller (plans/multi-user.md).
type Principal struct {
	Owner     bool        // the root token (bootstrap/admin service credential)
	UserID    string      // human user id when authenticated via a session
	User      *users.User // that user's snapshot (nil for token/element callers)
	Component string      // element path when the caller is an element
	Via       string      // "cookie" | "bearer" | "instance" | "frame" | "cron" | "session"
	// Role is set only for synthetic principals whose role is bound at
	// creation (cron ticks carry the role chosen at job registration).
	Role string
}

// IsAdmin reports admin privilege: the root token, or a user whose role is
// admin. This unifies the old "owner only" gate with admin users.
func (p Principal) IsAdmin() bool {
	return p.Owner || (p.User != nil && p.User.IsAdmin())
}

// CanUseTile reports whether this principal may open/drive a tile. The root
// token and admins: all. Users: their allow-list. Elements: not applicable
// here (governed by grants) — return true so element self-calls aren't blocked
// by the tile gate.
func (p Principal) CanUseTile(path string) bool {
	if p.IsAdmin() {
		return true
	}
	if p.User != nil {
		return p.User.CanUseTile(path)
	}
	// Element principals (frame/instance) are gated by grants, not this.
	return p.Component != ""
}

// CanTerminal reports terminal (root-shell) permission.
func (p Principal) CanTerminal() bool {
	if p.Owner {
		return true
	}
	return p.User != nil && p.User.CanTerminal()
}

func (p Principal) From() string {
	if p.Component != "" {
		return p.Component
	}
	if p.Owner {
		return "owner"
	}
	if p.UserID != "" {
		return "user:" + p.UserID
	}
	return ""
}

type Auth struct {
	ownerToken string       // guarded by mu — rotatable at runtime (RotateOwnerToken)
	tokenPath  string       // .xbin/token, rewritten on rotation
	secret     []byte       // HMAC key for frame tokens
	Users      *users.Store // human users (nil-safe: no store ⇒ root-only)

	sessionIdleTTL time.Duration // sliding inactivity window before a session dies
	sessionAbsTTL  time.Duration // hard cap since login regardless of activity

	mu        sync.RWMutex
	instances map[string]string   // instance token → component path
	terminals map[string]termID   // terminal token → (component, user)
	sessions  map[string]*session // session id → session
	noAuth    bool
}

// termID scopes a terminal-session token (plans/terminal-tokens.md): the tile
// the terminal is opened on, plus the human who opened it (attribution).
type termID struct {
	component string
	userID    string // "" for the bootstrap-token principal
}

// session is a live browser login (server-side; the cookie holds only the id).
type session struct {
	userID     string
	created    time.Time // login time — the absolute-TTL anchor
	lastActive time.Time // last authenticated request — the idle-TTL anchor
}

// Session lifetime defaults (override with XBIN_SESSION_IDLE_TTL /
// XBIN_SESSION_MAX_TTL as Go durations, e.g. "8h"). The server is
// authoritative; the 30-day cookie MaxAge is only a browser-side hint.
const (
	defaultSessionIdleTTL = 12 * time.Hour
	defaultSessionAbsTTL  = 30 * 24 * time.Hour
)

// Load reads (or creates) the owner token and frame-token secret under
// <workspace>/.xbin/.
func Load(workspaceRoot string, noAuth bool) (*Auth, error) {
	dir := filepath.Join(workspaceRoot, ".xbin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	tok, err := loadOrCreate(filepath.Join(dir, "token"))
	if err != nil {
		return nil, err
	}
	sec, err := loadOrCreate(filepath.Join(dir, "secret"))
	if err != nil {
		return nil, err
	}
	return &Auth{
		ownerToken:     tok,
		tokenPath:      filepath.Join(dir, "token"),
		secret:         []byte(sec),
		sessionIdleTTL: envDuration("XBIN_SESSION_IDLE_TTL", defaultSessionIdleTTL),
		sessionAbsTTL:  envDuration("XBIN_SESSION_MAX_TTL", defaultSessionAbsTTL),
		instances:      map[string]string{},
		terminals:      map[string]termID{},
		sessions:       map[string]*session{},
		noAuth:         noAuth,
	}, nil
}

// envDuration reads a Go duration from env, falling back to def (and warning on
// a malformed value rather than silently ignoring it).
func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		slog.Warn("ignoring malformed duration env var", "key", key, "value", v)
		return def
	}
	return d
}

// SetUsers installs the user store (from main, after Load).
func (a *Auth) SetUsers(s *users.Store) { a.Users = s }

// OwnerTokenValue returns the current owner token (startup login URL,
// host-side automation). Guarded — the token can rotate at runtime.
func (a *Auth) OwnerTokenValue() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.ownerToken
}

// IsOwnerToken reports whether tok is the current owner token (constant-time).
func (a *Auth) IsOwnerToken(tok string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return subtleEqual(tok, a.ownerToken)
}

// RotateOwnerToken replaces the owner token: a fresh random token is written
// to .xbin/token (0600, atomic) and swapped in memory. Everything the old
// token authenticated — bearer calls, owner-token cookies, and any copy that
// leaked historically (e.g. agent session transcripts recorded before
// terminal tokens, 2026-07-09) — stops working the moment this returns.
// Host-side bx/automation must re-read the file.
func (a *Auth) RotateOwnerToken() (string, error) {
	tok := util.RandomToken(32)
	tmp := a.tokenPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, a.tokenPath); err != nil {
		return "", err
	}
	a.mu.Lock()
	a.ownerToken = tok
	a.mu.Unlock()
	return tok, nil
}

// TokenLoginDisabled reports whether the bootstrap owner-token *browser* login
// has been turned off in the user store (the /login?token= URL and the
// owner-token cookie). The Bearer owner token still authenticates — tooling
// (bx, via XBIN_TOKEN) depends on it.
func (a *Auth) TokenLoginDisabled() bool {
	return a.Users != nil && a.Users.TokenLoginDisabled()
}

// --- sessions ---

// NewSession creates a server-side session for a user, returning its id.
func (a *Auth) NewSession(userID string) string {
	id := util.RandomToken(32)
	now := time.Now()
	a.mu.Lock()
	a.sweepSessionsLocked(now) // login is rare — opportunistic reap, no goroutine
	a.sessions[id] = &session{userID: userID, created: now, lastActive: now}
	a.mu.Unlock()
	return id
}

// DropSession invalidates a session (logout).
func (a *Auth) DropSession(id string) {
	a.mu.Lock()
	delete(a.sessions, id)
	a.mu.Unlock()
}

// sessionUser resolves a session id to its user, enforcing expiry: a session
// dies after sessionIdleTTL of inactivity (sliding) or sessionAbsTTL since
// login (hard cap), whichever first — so a stolen cookie can't authenticate
// forever. A live lookup slides the idle window.
func (a *Auth) sessionUser(id string) (string, bool) {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[id]
	if !ok {
		return "", false
	}
	if now.Sub(s.lastActive) > a.sessionIdleTTL || now.Sub(s.created) > a.sessionAbsTTL {
		delete(a.sessions, id)
		return "", false
	}
	s.lastActive = now
	return s.userID, true
}

// sweepSessionsLocked drops expired sessions so abandoned logins don't grow the
// map unbounded (caller holds a.mu).
func (a *Auth) sweepSessionsLocked(now time.Time) {
	for id, s := range a.sessions {
		if now.Sub(s.lastActive) > a.sessionIdleTTL || now.Sub(s.created) > a.sessionAbsTTL {
			delete(a.sessions, id)
		}
	}
}

func loadOrCreate(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b)), nil
	}
	tok := util.RandomToken(32)
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

func (a *Auth) NoAuth() bool { return a.noAuth }

// --- element instance tokens (minted by the runner per generation) ---

func (a *Auth) RegisterInstance(token, component string) {
	a.mu.Lock()
	a.instances[token] = component
	a.mu.Unlock()
}

func (a *Auth) RevokeInstance(token string) {
	a.mu.Lock()
	delete(a.instances, token)
	a.mu.Unlock()
}

func (a *Auth) lookupInstance(token string) (string, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	c, ok := a.instances[token]
	return c, ok
}

// --- terminal tokens (minted per terminal session; plans/terminal-tokens.md) ---

// MintTerminal registers a token scoping a terminal session to the tile it is
// opened on. The returned bearer resolves to that tile's ELEMENT principal —
// the tile acting as itself (self-admin + its approved grants/bindings), never
// inheriting the driving user's privilege — exactly the frame-token model, for
// shells. userID rides along for attribution and session binding.
func (a *Auth) MintTerminal(component, userID string) string {
	tok := util.RandomToken(24)
	a.mu.Lock()
	a.terminals[tok] = termID{component: component, userID: userID}
	a.mu.Unlock()
	return tok
}

// RevokeTerminal drops a terminal token (the session ended).
func (a *Auth) RevokeTerminal(token string) {
	a.mu.Lock()
	delete(a.terminals, token)
	a.mu.Unlock()
}

func (a *Auth) lookupTerminal(token string) (termID, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	id, ok := a.terminals[token]
	return id, ok
}

// terminalPrincipal builds the element principal for a terminal token. Like a
// frame token, one naming a user who no longer exists is rejected — deleting a
// user kills their live shells' API access.
func (a *Auth) terminalPrincipal(id termID) (Principal, bool) {
	if id.userID != "" {
		if _, found := a.userSnapshot(id.userID); !found {
			return Principal{}, false
		}
	}
	return Principal{Component: id.component, UserID: id.userID, Via: "terminal"}, true
}

// --- frame tokens ---

// MintFrameToken creates a token binding requests to (component, user) for
// ttl. userID is "" for the root principal. Format:
// base64(component)|base64(user)|exp|hmac.
func (a *Auth) MintFrameToken(component, userID string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	payload := fmt.Sprintf("%s|%s|%d",
		base64.RawURLEncoding.EncodeToString([]byte(component)),
		base64.RawURLEncoding.EncodeToString([]byte(userID)), exp)
	return payload + "|" + a.sign(payload)
}

func (a *Auth) sign(payload string) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// VerifyFrameToken returns the (component, user) a valid, unexpired token
// attributes to.
func (a *Auth) VerifyFrameToken(tok string) (component, userID string, ok bool) {
	parts := strings.Split(tok, "|")
	if len(parts) != 4 {
		return "", "", false
	}
	payload := parts[0] + "|" + parts[1] + "|" + parts[2]
	if !hmac.Equal([]byte(a.sign(payload)), []byte(parts[3])) {
		return "", "", false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", "", false
	}
	comp, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", false
	}
	uid, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", false
	}
	return string(comp), string(uid), true
}

// framePrincipal builds an element-frontend principal from a verified frame
// token. It is an *element* identity: the tile acts as itself, and its admin
// capability comes from the tile's own grants — it does NOT inherit the
// driving user's privilege (a tile an admin merely opens can't call admin
// APIs unless the tile is itself granted). The user id rides along only for
// attribution and to bind the token to its session. A frame token naming a
// user who no longer exists is rejected.
func (a *Auth) framePrincipal(tok string) (Principal, bool) {
	comp, uid, ok := a.VerifyFrameToken(tok)
	if !ok {
		return Principal{}, false
	}
	if uid != "" {
		if _, found := a.userSnapshot(uid); !found {
			return Principal{}, false
		}
	}
	return Principal{Component: comp, UserID: uid, Via: "frame"}, true
}

func (a *Auth) userSnapshot(uid string) (*users.User, bool) {
	if a.Users == nil {
		return nil, false
	}
	return a.Users.Get(uid)
}

// --- request authentication ---

// FromRequest identifies the caller. Order:
//  1. Bearer owner token → owner.
//  2. Bearer instance token → element backend.
//  3. Owner cookie + frame token header → element frontend (attributed).
//  4. Owner cookie alone → owner (non-element pages: xbind UI, direct nav).
func (a *Auth) FromRequest(r *http.Request) (Principal, bool) {
	// A frame token attributes the request to (component, user); it's honored
	// in every mode. Present-but-invalid is rejected, never downgraded.
	frame := func() (Principal, bool, bool) { // principal, ok, present
		if ft := r.Header.Get(FrameTokenHeader); ft != "" {
			p, ok := a.framePrincipal(ft)
			return p, ok, true
		}
		if ft := r.URL.Query().Get("frame"); ft != "" { // WS can't set headers
			p, ok := a.framePrincipal(ft)
			return p, ok, true
		}
		return Principal{}, false, false
	}

	if a.noAuth {
		// Owner auth is off, but element identity still applies so dev mode
		// exercises the same RBAC the deployed workspace enforces.
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			tok := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
			if comp, ok := a.lookupInstance(tok); ok {
				return Principal{Component: comp, Via: "instance"}, true
			}
			if id, ok := a.lookupTerminal(tok); ok {
				return a.terminalPrincipal(id)
			}
		}
		if p, ok, present := frame(); present {
			return p, ok
		}
		return Principal{Owner: true, Via: "dev"}, true
	}

	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		tok := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		if a.IsOwnerToken(tok) {
			return Principal{Owner: true, Via: "bearer"}, true
		}
		if comp, ok := a.lookupInstance(tok); ok {
			return Principal{Component: comp, Via: "instance"}, true
		}
		if id, ok := a.lookupTerminal(tok); ok {
			return a.terminalPrincipal(id)
		}
		return Principal{}, false
	}

	// Cookie: either the root token (bootstrap/admin) or a session id.
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return Principal{}, false
	}
	var base Principal
	switch {
	case a.IsOwnerToken(cookie.Value):
		// Owner-token browser login can be turned off once real accounts exist;
		// when disabled, an owner-token cookie no longer authenticates (so a
		// leaked token can't be pasted into a cookie either). Bearer is separate.
		if a.TokenLoginDisabled() {
			return Principal{}, false
		}
		base = Principal{Owner: true, Via: "cookie"}
	default:
		uid, ok := a.sessionUser(cookie.Value)
		if !ok {
			return Principal{}, false
		}
		u, found := a.userSnapshot(uid)
		if !found { // user deleted → session invalid
			return Principal{}, false
		}
		base = Principal{UserID: uid, User: u, Via: "session"}
	}

	// A frame token on top narrows to that tile frontend (carrying the same
	// human identity). The frame token's own user must match the session.
	if p, ok, present := frame(); present {
		if !ok {
			return Principal{}, false
		}
		if p.UserID != base.UserID { // cross-user frame token replay
			return Principal{}, false
		}
		return p, true
	}
	return base, true
}

func subtleEqual(a, b string) bool {
	return len(a) == len(b) && hmac.Equal([]byte(a), []byte(b))
}
