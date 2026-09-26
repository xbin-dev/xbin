package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// --- frame tokens ---
//
// A frame token is a tile frontend's only credential (plans/auth.md §6): an
// HMAC over (component, user, expiry) minted into the tile's document and
// renewed by the tile itself at /api/xbin/frame-token.
//
// Credential binding (plans/native.md §5, §20 row 2): a token also names
// the CREDENTIAL GENERATION it was minted under, and verifies only while
// that generation lives — so logout, device revocation, sign-out-everywhere
// and disabling the user kill the frames a login opened, instead of those
// frames renewing themselves forever. Renewal copies the generation. Forms:
//
//	s.<handle>      a login session (browser cookie or app bearer): dies
//	                with the session (logout, expiry, revocation, restart)
//	u.<epoch>.<n>   the user's generation: DropUserSessions bumps n; epoch
//	                is boot-random, so these never survive a restart either
//	o.<hash>        the bootstrap owner token: dies when it is rotated
//
// Wire format: base64url(component)|base64url(user)|exp|gen|hmac. The legacy
// 4-field form (…|exp|hmac, before binding) still verifies until it expires —
// only tokens that could have been minted before this boot, see
// legacyFrameWindow — and renews into a bound token tied to the user's
// current generation, so pages open across the upgrade keep working.

// genState is the live-generation bookkeeping (guarded by Auth.mu).
type genState struct {
	sessions map[string]string // session generation handle → session id
	users    map[string]uint64 // user id → generation (bumped by DropUserSessions)
	epoch    string            // boot-random prefix of user generations
	boot     time.Time
}

func newGenState() genState {
	return genState{sessions: map[string]string{}, users: map[string]uint64{},
		epoch: util.RandomToken(4), boot: time.Now()}
}

// legacyFrameWindow bounds the pre-binding 4-field tokens: one verifies only
// if it expires within this long after boot — i.e. it was minted by the
// xbind that ran before (frame tokens live 15 minutes). A later one can't be
// genuine: this xbind never mints the legacy form.
const legacyFrameWindow = time.Hour

// ownerGenLocked is the owner-token generation (caller holds a.mu): a keyed
// hash of the current token, so rotation retires every frame it minted
// without the token itself appearing in tile-visible frame tokens.
func (a *Auth) ownerGenLocked() string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte("xbin-owner-gen\x00" + a.ownerToken))
	return "o." + hex.EncodeToString(m.Sum(nil)[:8])
}

func (a *Auth) ownerGen() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.ownerGenLocked()
}

// UserGen is a user's current credential generation (what a frame token
// minted without a session — a legacy renewal, a terminal or backend of that
// user — binds to).
func (a *Auth) UserGen(userID string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.userGenLocked(userID)
}

func (a *Auth) userGenLocked(userID string) string {
	return "u." + a.gens.epoch + "." + strconv.FormatUint(a.gens.users[userID], 10)
}

// defaultGen binds a token minted without a session behind it: the user's
// generation, or the owner token's for an owner-attributed token.
func (a *Auth) defaultGen(userID string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if userID == "" {
		return a.ownerGenLocked()
	}
	return a.userGenLocked(userID)
}

// genLive reports whether generation gen is still valid for a token naming
// userID. A session generation also reports who is viewing through that
// session (an admin's view-as, D64) — its frames stay read-only.
func (a *Auth) genLive(gen, userID string) (impersonator string, ok bool) {
	kind, rest, found := strings.Cut(gen, ".")
	if !found || rest == "" {
		return "", false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	switch kind {
	case "s":
		s := a.sessions[a.gens.sessions[rest]]
		if s == nil || s.userID != userID || a.expiredLocked(s, time.Now()) {
			return "", false
		}
		return s.impersonator, true
	case "u":
		return "", userID != "" && gen == a.userGenLocked(userID)
	case "o":
		return "", userID == "" && gen == a.ownerGenLocked()
	}
	return "", false
}

// validGen: generation strings travel inside the |-separated token, so they
// are restricted to a safe alphabet (they are always xbind-minted).
func validGen(g string) bool {
	if g == "" || len(g) > 96 {
		return false
	}
	for _, c := range g {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.') {
			return false
		}
	}
	return true
}

// MintFrameToken creates a token binding requests to (component, user) for
// ttl, tied to the user's current generation (the owner token's when userID
// is ""). Callers holding the request principal use MintFrameTokenFor, which
// binds to the principal's own login.
func (a *Auth) MintFrameToken(component, userID string, ttl time.Duration) string {
	return a.mintFrame(component, userID, a.defaultGen(userID), ttl)
}

// MintFrameTokenFor mints a frame token for component on behalf of p: the
// token names p's user and is bound to p's credential generation — the
// session p signed in with, or (a frame principal renewing) the generation
// its own token carried. A principal without one (a legacy token, a
// terminal, a backend) binds to its user's current generation.
func (a *Auth) MintFrameTokenFor(p Principal, component string, ttl time.Duration) string {
	gen := p.Gen
	if !validGen(gen) {
		gen = a.defaultGen(p.UserID)
	}
	return a.mintFrame(component, p.UserID, gen, ttl)
}

func (a *Auth) mintFrame(component, userID, gen string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	payload := fmt.Sprintf("%s|%s|%d|%s",
		base64.RawURLEncoding.EncodeToString([]byte(component)),
		base64.RawURLEncoding.EncodeToString([]byte(userID)), exp, gen)
	return payload + "|" + a.sign(payload)
}

func (a *Auth) sign(payload string) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// VerifyFrameToken returns the (component, user) a valid, unexpired token
// attributes to — for a bound token, only while its generation lives.
func (a *Auth) VerifyFrameToken(tok string) (component, userID string, ok bool) {
	ft, ok := a.verifyFrame(tok)
	return ft.component, ft.userID, ok
}

type frameClaims struct {
	component, userID, gen string
	impersonator           string // the view-as admin behind an s. generation
}

func (a *Auth) verifyFrame(tok string) (frameClaims, bool) {
	parts := strings.Split(tok, "|")
	var payload, sig, gen string
	switch len(parts) {
	case 5:
		gen = parts[3]
		if !validGen(gen) {
			return frameClaims{}, false
		}
		payload, sig = strings.Join(parts[:4], "|"), parts[4]
	case 4: // legacy, pre-binding
		payload, sig = strings.Join(parts[:3], "|"), parts[3]
	default:
		return frameClaims{}, false
	}
	if !hmac.Equal([]byte(a.sign(payload)), []byte(sig)) {
		return frameClaims{}, false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return frameClaims{}, false
	}
	if gen == "" && exp > a.gens.boot.Add(legacyFrameWindow).Unix() {
		return frameClaims{}, false
	}
	comp, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return frameClaims{}, false
	}
	uid, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return frameClaims{}, false
	}
	ft := frameClaims{component: string(comp), userID: string(uid), gen: gen}
	if gen != "" {
		imp, live := a.genLive(gen, ft.userID)
		if !live {
			return frameClaims{}, false
		}
		ft.impersonator = imp
	}
	return ft, true
}

// framePrincipal builds an element-frontend principal from a verified frame
// token. It is an *element* identity: the tile acts as itself, and its admin
// capability comes from the tile's own grants — it does NOT inherit the
// driving user's privilege (a tile an admin merely opens can't call admin
// APIs unless the tile is itself granted). The user id rides along only for
// attribution and to bind the token to its session. A frame token naming a
// user who no longer exists is rejected. A token minted under an admin's
// view-as session stays read-only with it (D64), cookie or not.
func (a *Auth) framePrincipal(tok string) (Principal, bool) {
	ft, ok := a.verifyFrame(tok)
	if !ok {
		return Principal{}, false
	}
	p := Principal{Component: ft.component, UserID: ft.userID, Via: "frame", Gen: ft.gen, Impersonator: ft.impersonator}
	if ft.userID != "" {
		if _, found := a.userSnapshot(ft.userID); !found {
			return Principal{}, false
		}
		// The driving user's Access rides along: per-tile gates for anything
		// beyond the frame's own tile follow the human, so one tile's frame
		// token can't read another tile's static files past the user's RBAC.
		p.Access = a.accessSnapshot(ft.userID)
	}
	return p, true
}
