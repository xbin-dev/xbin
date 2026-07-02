// Package auth implements buxon's principals (plans/auth.md):
//
//   - Owner: the human. Authenticated by the owner token — via login cookie
//     (browser) or Authorization: Bearer (bx, curl, terminals).
//   - Element instance: a running backend generation, authenticated by a
//     per-generation instance token minted by the runner.
//   - Element frontend: owner cookie + frame token (minted into served HTML
//     at the D4 injection point) attributing the request to a component.
//
// The gateway/permission decisions live in internal/broker/policy; this
// package only answers "who is calling".
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/magik6k/buxon/internal/users"
	"github.com/magik6k/buxon/internal/util"
)

const (
	CookieName       = "buxon_session"
	FrameTokenHeader = "X-Buxon-Frame-Token"
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
	OwnerToken string
	secret     []byte       // HMAC key for frame tokens
	Users      *users.Store // human users (nil-safe: no store ⇒ root-only)

	mu        sync.RWMutex
	instances map[string]string // instance token → component path
	sessions  map[string]string // session id → user id
	noAuth    bool
}

// Load reads (or creates) the owner token and frame-token secret under
// <workspace>/.buxon/.
func Load(workspaceRoot string, noAuth bool) (*Auth, error) {
	dir := filepath.Join(workspaceRoot, ".buxon")
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
		OwnerToken: tok,
		secret:     []byte(sec),
		instances:  map[string]string{},
		sessions:   map[string]string{},
		noAuth:     noAuth,
	}, nil
}

// SetUsers installs the user store (from main, after Load).
func (a *Auth) SetUsers(s *users.Store) { a.Users = s }

// --- sessions ---

// NewSession creates a server-side session for a user, returning its id.
func (a *Auth) NewSession(userID string) string {
	id := util.RandomToken(32)
	a.mu.Lock()
	a.sessions[id] = userID
	a.mu.Unlock()
	return id
}

// DropSession invalidates a session (logout).
func (a *Auth) DropSession(id string) {
	a.mu.Lock()
	delete(a.sessions, id)
	a.mu.Unlock()
}

func (a *Auth) sessionUser(id string) (string, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	uid, ok := a.sessions[id]
	return uid, ok
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
//  4. Owner cookie alone → owner (non-element pages: buxond UI, direct nav).
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
		}
		if p, ok, present := frame(); present {
			return p, ok
		}
		return Principal{Owner: true, Via: "dev"}, true
	}

	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		tok := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		if subtleEqual(tok, a.OwnerToken) {
			return Principal{Owner: true, Via: "bearer"}, true
		}
		if comp, ok := a.lookupInstance(tok); ok {
			return Principal{Component: comp, Via: "instance"}, true
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
	case subtleEqual(cookie.Value, a.OwnerToken):
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
