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

	"github.com/magik6k/buxon/internal/util"
)

const (
	CookieName       = "buxon_session"
	FrameTokenHeader = "X-Buxon-Frame-Token"
)

// Principal identifies a verified caller.
type Principal struct {
	Owner     bool
	Component string // element path when the caller is an element (backend or frontend)
	Via       string // "cookie" | "bearer" | "instance" | "frame" | "cron"
	// Role is set only for synthetic principals whose role is bound at
	// creation (cron ticks carry the role chosen at job registration).
	Role string
}

func (p Principal) From() string {
	if p.Owner {
		return "owner"
	}
	return p.Component
}

type Auth struct {
	OwnerToken string
	secret     []byte // HMAC key for frame tokens

	mu        sync.RWMutex
	instances map[string]string // instance token → component path
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
		noAuth:     noAuth,
	}, nil
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

// MintFrameToken creates a token binding requests to component for ttl.
// Format: base64(component)|exp|hex(hmac).
func (a *Auth) MintFrameToken(component string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	payload := fmt.Sprintf("%s|%d", base64.RawURLEncoding.EncodeToString([]byte(component)), exp)
	return payload + "|" + a.sign(payload)
}

func (a *Auth) sign(payload string) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// VerifyFrameToken returns the component a valid, unexpired token attributes to.
func (a *Auth) VerifyFrameToken(tok string) (string, bool) {
	parts := strings.Split(tok, "|")
	if len(parts) != 3 {
		return "", false
	}
	payload := parts[0] + "|" + parts[1]
	if !hmac.Equal([]byte(a.sign(payload)), []byte(parts[2])) {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	comp, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	return string(comp), true
}

// --- request authentication ---

// FromRequest identifies the caller. Order:
//  1. Bearer owner token → owner.
//  2. Bearer instance token → element backend.
//  3. Owner cookie + frame token header → element frontend (attributed).
//  4. Owner cookie alone → owner (non-element pages: buxond UI, direct nav).
func (a *Auth) FromRequest(r *http.Request) (Principal, bool) {
	if a.noAuth {
		// Owner auth is off, but element identity still applies: dev mode
		// must exercise the same RBAC the deployed workspace enforces.
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			tok := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
			if comp, ok := a.lookupInstance(tok); ok {
				return Principal{Component: comp, Via: "instance"}, true
			}
		}
		if fc := r.Header.Get(FrameTokenHeader); fc != "" {
			if comp, ok := a.VerifyFrameToken(fc); ok {
				return Principal{Component: comp, Via: "frame"}, true
			}
		}
		if fc := r.URL.Query().Get("frame"); fc != "" {
			if comp, ok := a.VerifyFrameToken(fc); ok {
				return Principal{Component: comp, Via: "frame"}, true
			}
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

	cookie, err := r.Cookie(CookieName)
	if err != nil || !subtleEqual(cookie.Value, a.OwnerToken) {
		return Principal{}, false
	}
	if ft := r.Header.Get(FrameTokenHeader); ft != "" {
		if comp, ok := a.VerifyFrameToken(ft); ok {
			return Principal{Component: comp, Via: "frame"}, true
		}
		return Principal{}, false // present-but-invalid token is rejected, not downgraded
	}
	// WS endpoints can't set headers from browsers; allow ?frame= there.
	if ft := r.URL.Query().Get("frame"); ft != "" {
		if comp, ok := a.VerifyFrameToken(ft); ok {
			return Principal{Component: comp, Via: "frame"}, true
		}
		return Principal{}, false
	}
	return Principal{Owner: true, Via: "cookie"}, true
}

func subtleEqual(a, b string) bool {
	return len(a) == len(b) && hmac.Equal([]byte(a), []byte(b))
}
