package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// Tile origins (--tile-assets=origins, docs/auth.md §Tile asset gating) are
// same-SITE with the workspace. Two consequences live here:
//
//   - cookie names: a tile origin may set cookies for a parent domain, so
//     the workspace's session cookie is __Host-xbin_session on secure
//     requests (browsers refuse a __Host- cookie that has a Domain
//     attribute, so a tile can't toss one in front of the real one), and
//     the tile cookie is __Host-xbin_tile;
//   - binding: a tile-origin credential lives exactly as long as the
//     browser session that obtained it. The workspace mints a one-time
//     exchange TICKET bound to that session ("s:" + a keyed reference; the
//     session id itself never leaves this package), the tile origin trades
//     it for its cookie, and every later request re-checks the session:
//     signing out (or out everywhere), a password change, session expiry,
//     or the user disabled — the next tile request fails.
const (
	hostSessionCookie = "__Host-" + CookieName
	// HostTileCookieName is the tile cookie's name on secure origins.
	HostTileCookieName = "__Host-" + TileCookieName

	sessionRefPurpose = "xbin-session-ref-v1"
	tileTicketPurpose = "xbin-tile-ticket-v1"
	tileTicketPrefix  = "x1"
	sessionGenPrefix  = "s:"

	// TileTicketTTL bounds the one-time exchange ticket.
	TileTicketTTL = 2 * time.Minute
)

// errDuplicateCookie: a request carrying two cookies of one credential name
// (one tossed in by a same-site page with a Domain or Path attribute) is
// treated as carrying none.
var errDuplicateCookie = errors.New("duplicate credential cookie")

// SecureRequest: r arrived over TLS (directly or through a TLS-terminating
// proxy), or addresses localhost / *.localhost — a secure context in
// browsers, where Secure and __Host- cookies are accepted (dev).
func SecureRequest(r *http.Request) bool {
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		return true
	}
	h := r.Host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	return h == "localhost" || strings.HasSuffix(h, ".localhost")
}

// SetHostCookies switches the session cookie to __Host-xbin_session on
// secure requests (origins mode). The plain name is then not read on secure
// requests at all — a tossed xbin_session could otherwise log a signed-out
// browser into someone else's account — so switching the mode signs every
// browser out once.
func (a *Auth) SetHostCookies(on bool) { a.hostCookies = on }

// SessionCookieName is the session cookie's name on r.
func (a *Auth) SessionCookieName(r *http.Request) string {
	if a.hostCookies && SecureRequest(r) {
		return hostSessionCookie
	}
	return CookieName
}

// SessionCookieSecure reports whether the session cookie set on r carries
// Secure: as always behind TLS; in origins mode on every secure request
// (localhost included — the __Host- prefix requires it).
func (a *Auth) SessionCookieSecure(r *http.Request) bool {
	if a.hostCookies {
		return SecureRequest(r)
	}
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// SessionCookie returns r's session cookie. In origins mode two cookies of
// the name mean one was tossed in by a same-site page (possible only on
// insecure requests, where the prefix can't be used): neither is used.
func (a *Auth) SessionCookie(r *http.Request) (*http.Cookie, error) {
	if !a.hostCookies {
		return r.Cookie(CookieName)
	}
	return OnlyCookie(r, a.SessionCookieName(r))
}

// OnlyCookie returns the one cookie named name on r — none, or two or more
// (a duplicate tossed in by a same-site page), is an error.
func OnlyCookie(r *http.Request, name string) (*http.Cookie, error) {
	var found *http.Cookie
	for _, c := range r.Cookies() {
		if c.Name != name {
			continue
		}
		if found != nil {
			return nil, errDuplicateCookie
		}
		found = c
	}
	if found == nil {
		return nil, http.ErrNoCookie
	}
	return found, nil
}

// --- session references ---

func (a *Auth) sessionRef(sid string) string { return a.mac(sessionRefPurpose, sid) }

// indexSessionLocked records a new session's reference (caller holds a.mu).
func (a *Auth) indexSessionLocked(sid string) {
	if a.sessionRefs == nil {
		a.sessionRefs = map[string]string{}
	}
	a.sessionRefs[a.sessionRef(sid)] = sid
}

// sweepSessionRefsLocked drops references to sessions that are gone and
// spent tickets that expired (caller holds a.mu).
func (a *Auth) sweepSessionRefsLocked(now time.Time) {
	for ref, sid := range a.sessionRefs {
		if _, ok := a.sessions[sid]; !ok {
			delete(a.sessionRefs, ref)
		}
	}
	for k, exp := range a.usedTickets {
		if now.After(exp) {
			delete(a.usedTickets, k)
		}
	}
}

// sessionByRef resolves a live session of uid by reference WITHOUT sliding
// it (a tile's own traffic does not keep a browser session alive). end is
// the session's absolute deadline.
func (a *Auth) sessionByRef(ref, uid string) (impersonator string, end time.Time, ok bool) {
	now := time.Now()
	a.mu.RLock()
	defer a.mu.RUnlock()
	sid, found := a.sessionRefs[ref]
	if !found {
		return "", time.Time{}, false
	}
	s, found := a.sessions[sid]
	if !found || s.userID != uid || now.Sub(s.lastActive) > a.sessionIdleTTL || now.Sub(s.created) > a.sessionAbsTTL {
		return "", time.Time{}, false
	}
	return s.impersonator, s.created.Add(a.sessionAbsTTL), true
}

// TileBinding is the binding a tile-origin credential minted for uid on r
// carries: r's own live browser session of uid (a user), or the owner
// token's generation (the owner principal). ok=false: a user principal
// without a live session of their own on r — a bare frame token — gets no
// tile credential at all.
func (a *Auth) TileBinding(r *http.Request, uid string) (string, bool) {
	if a.noAuth {
		return "", true
	}
	if uid == "" {
		return a.credGeneration(""), true
	}
	c, err := a.SessionCookie(r)
	if err != nil {
		return "", false
	}
	ref := a.sessionRef(c.Value)
	if _, _, live := a.sessionByRef(ref, uid); !live {
		return "", false
	}
	return sessionGenPrefix + ref, true
}

// --- tickets and the tile cookie ---

// MintTileTicket mints the one-time exchange ticket a workspace redirect
// carries to tile's origin, under binding (TileBinding). A nonce rides in
// the binding field: two tickets for the same (tile, user, session) within
// one second must not be the same string, or the second would count as
// spent (the shell framing a tile and a direct open of it, side by side).
func (a *Auth) MintTileTicket(tile, uid, binding string) string {
	return a.mintGrant(tileTicketPrefix, tileTicketPurpose, tile, uid, binding+ticketNonceSep+util.RandomToken(8), TileTicketTTL)
}

// ticketNonceSep separates a ticket's binding from its nonce ('#' is in no
// binding: they are "s:" + base64url, a base64url owner generation, or "").
const ticketNonceSep = "#"

// RedeemTileTicket verifies a ticket and spends it — a second redemption
// fails — and checks that what it is bound to is still live.
func (a *Auth) RedeemTileTicket(tok string) (AssetGrant, bool) {
	g, ok := a.verifyGrant(tileTicketPrefix, tileTicketPurpose, tok)
	if !ok {
		return AssetGrant{}, false
	}
	key := tok[strings.LastIndexByte(tok, '.')+1:]
	a.mu.Lock()
	if a.usedTickets == nil {
		a.usedTickets = map[string]time.Time{}
	}
	_, spent := a.usedTickets[key]
	if !spent {
		if len(a.usedTickets) >= 4096 {
			a.sweepSessionRefsLocked(time.Now())
		}
		a.usedTickets[key] = g.Exp
	}
	a.mu.Unlock()
	if spent {
		return AssetGrant{}, false
	}
	g.Gen, _, _ = strings.Cut(g.Gen, ticketNonceSep)
	return a.liveTileGrant(g)
}

// MintTileCookie mints a tile origin's cookie value for (tile, user) under
// binding, valid for ttl (the caller caps it at the session's end).
func (a *Auth) MintTileCookie(tile, uid, binding string, ttl time.Duration) string {
	return a.mintGrant(tileCookiePrefix, tileCookiePurpose, tile, uid, binding, ttl)
}

// VerifyTileCookie returns the grant of a valid, unexpired tile-origin
// cookie whose binding is live. The caller checks that its tile is the
// origin's tile and that the user may still read it.
func (a *Auth) VerifyTileCookie(v string) (AssetGrant, bool) {
	g, ok := a.verifyGrant(tileCookiePrefix, tileCookiePurpose, v)
	if !ok {
		return AssetGrant{}, false
	}
	return a.liveTileGrant(g)
}

// liveTileGrant: a user's tile credential must be bound to a live browser
// session of that user (and the user must exist and be enabled); the
// owner's to the current owner token. It reports the session's
// impersonator (the tile then acts read-only, as the session does) and
// absolute end.
func (a *Auth) liveTileGrant(g AssetGrant) (AssetGrant, bool) {
	if a.noAuth {
		return g, true
	}
	if g.UserID == "" {
		return g, subtleEqual(g.Gen, a.credGeneration(""))
	}
	if _, ok := a.userSnapshot(g.UserID); !ok {
		return AssetGrant{}, false
	}
	ref, ok := strings.CutPrefix(g.Gen, sessionGenPrefix)
	if !ok {
		return AssetGrant{}, false
	}
	imp, end, live := a.sessionByRef(ref, g.UserID)
	if !live {
		return AssetGrant{}, false
	}
	g.Impersonator, g.SessionEnd = imp, end
	return g, true
}

// SessionCookieHostName is the __Host- session cookie's name (origins mode
// on secure requests), for code that strips credentials by name.
const SessionCookieHostName = hostSessionCookie

type noSetCookieKey struct{}

// WithNoSetCookie marks a request whose response must set no cookies — a
// tile origin's /api: a component backend's Set-Cookie with
// Domain=<parent> would toss cookies into the workspace and every sibling
// tile. The component proxy drops Set-Cookie from such responses (the
// WebSocket 101 included).
func WithNoSetCookie(ctx context.Context) context.Context {
	return context.WithValue(ctx, noSetCookieKey{}, true)
}

// NoSetCookie reports WithNoSetCookie.
func NoSetCookie(ctx context.Context) bool { v, _ := ctx.Value(noSetCookieKey{}).(bool); return v }
