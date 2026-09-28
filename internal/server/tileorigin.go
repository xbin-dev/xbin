package server

import (
	"cmp"
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// Per-tile origins (--tile-assets=origins; plans/tile-asset-auth.md
// mechanism A). Each tile's frontend is served from its own origin
// t-<id>.<tiles-domain> (id: auth.TileHostID, a keyed hash of the tile
// path), a subdomain of the workspace's own site so its cookie is
// first-party for the embedding shell. The flow:
//
//  1. a browser navigates to a sandboxed tile's document on the WORKSPACE
//     origin — bx-frame's iframe, a direct open, a link — and the workspace
//     sends it to https://t-<id>.<tiles-domain>/<same path>?xbin_begin=…
//     (tilenav.go decides how, by who initiated the navigation), naming
//     the browser session's binding (a keyed hint, not a credential);
//  2. the tile origin — unless its cookie is already bound to that session
//     (then it just drops the parameter) — keeps an exchange STATE in a
//     cookie of its own (__Host-xbin_tstate) and sends the browser back to
//     the workspace with ?xbin_state=<it>; the workspace answers with the
//     same path on the tile origin and ?xbin_ticket=…, a one-time ticket
//     bound to the browser session and to that state;
//  3. the tile origin redeems the ticket — its state must be this browser's
//     (a ticket minted from another session can't be planted here: login
//     CSRF from a sibling tile origin, which is same-site), its tile the
//     origin's, its session still live, its user still able to read the
//     tile — sets the tile cookie (__Host-xbin_tile: HttpOnly, Secure,
//     SameSite=Strict, host-only, Path=/) and redirects to the clean URL;
//  4. every later request on that origin carries the cookie: /c/ is
//     authorized live against the user's access to the tile being loaded,
//     /api and /ws act as the tile's frame principal (read-only in a view-as
//     session). The tile's documents can be framed only by the workspace and
//     the tile itself (frame-ancestors). Nothing else is served there: a
//     navigation to a workspace page, or to another tile's page, is sent to
//     the workspace origin.
//
// The cookie dies with the browser session it is bound to (sign-out, sign-
// out everywhere, expiry) and never outlives it; so do the frame tokens
// minted on the tile origin (TilePrincipal carries that login's
// generation), and a view-as session's stay read-only without the cookie.
// Chrome (root, shell, chrome:true tiles) stays on the workspace origin.
//
// Tile deployments (D127j; 11-contract §2.6, §7.5): each deployment of a tile
// has an origin of its own, labelled by the tile and the deployment's name
// (auth.TileHostIDDeployment), so each keeps its own storage whichever
// deployment is primary. main's label is the tile's, and its tickets and
// cookies keep today's bytes (x1, c1); any other deployment's are x2 and c2,
// which name it. A navigation to the bare /c/<tile>/ goes to the primary's
// origin, one to /c/<tile>+<name>/ to that deployment's. On the origin of
// (tile, N) both /c/<tile>/… and /c/<tile>+<N>/… serve N, /api/<tile>/…
// acts as N's tile principal, and a path naming another deployment of the
// tile is sent to the workspace like another tile's page. Every check that
// compared a label with TileHostID(tile) asks which (tile, deployment) the
// label is (originDeployment, originOf).

type tileOriginKey struct{}

// tileOriginOf: the tile whose origin served r ("" on the workspace origin).
func tileOriginOf(r *http.Request) string {
	v, _ := r.Context().Value(tileOriginKey{}).(string)
	return v
}

// strictAssetRefusal is the 401 body for an uncredentialed tile asset load
// under a strict mode — what a developer sees in the network panel.
const strictAssetRefusal = "unauthorized — strict tile asset gating: tile files load only with a credential; " +
	"use relative URLs (bx fix assets <tile>) — docs/elements.md#asset-urls"

// ticketParam carries the exchange ticket; beginParam starts an exchange on
// the tile origin (the workspace session's binding hint); stateParam brings
// the tile origin's exchange state to the workspace; retryMarker marks a
// tile-origin → workspace → tile-origin credential refresh, so it happens
// once.
const (
	ticketParam = "xbin_ticket"
	beginParam  = "xbin_begin"
	stateParam  = "xbin_state"
	retryMarker = "xbin_retry"
)

// exchangeParams are the query parameters of the exchange (and a frame
// token): never left in a URL a page ends up at.
var exchangeParams = []string{"frame", ticketParam, beginParam, stateParam}

// tileOrigins routes requests addressed to a tile origin (origins mode) and
// guards the workspace origin against tile-initiated requests riding its
// session cookie (tilenav.go); in every other mode it is main unchanged.
func (s *Server) tileOrigins(main http.Handler) http.Handler {
	if s.assetMode() != TileAssetsOrigins || s.TilesDomain == "" {
		return main
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := s.tileHostOf(r.Host); ok {
			s.serveTileOrigin(w, r, id)
			return
		}
		main.ServeHTTP(w, s.withoutTileInitiatedCookies(r))
	})
}

// tileHostOf reports whether host (a Host header) is under the tiles domain
// and, if so, its tile label ("" when malformed — refused by the caller).
// Host is client-chosen: it only selects which credential is expected; the
// credential alone authorizes.
func (s *Server) tileHostOf(host string) (string, bool) {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	td := s.TilesDomain
	if hh, _, err := net.SplitHostPort(td); err == nil {
		td = hh
	}
	td = strings.ToLower(td)
	label, ok := strings.CutSuffix(h, "."+td)
	if !ok {
		return "", false
	}
	if len(label) != 18 || !strings.HasPrefix(label, "t-") || strings.Trim(label[2:], "abcdefghijklmnopqrstuvwxyz234567") != "" {
		return "", true
	}
	return label, true
}

// tileOriginURL is a tile's origin (scheme://t-<id>.<tiles-domain>[:port]),
// or "" outside origins mode: its primary's (D127d), which is the tile's own
// label while main is primary, as on every tile without a deployment record.
// Scheme and port follow --external-url unless --tiles-domain carries its
// own port.
func (s *Server) tileOriginURL(tile string) string {
	if s.assetMode() != TileAssetsOrigins {
		return ""
	}
	return s.deploymentOriginURL(tile, s.primaryOf(tile))
}

// DeploymentOrigin is the origin of deployment dep of tile under
// --tile-assets=origins (11-contract §2.6), "" in every other mode: what
// GET /deployments names as a deployment's origin to the callers who see
// that deployment.
func (s *Server) DeploymentOrigin(tile, dep string) string { return s.deploymentOriginURL(tile, dep) }

// deploymentOriginURL is tileOriginURL for deployment dep of tile.
func (s *Server) deploymentOriginURL(tile, dep string) string {
	if s.assetMode() != TileAssetsOrigins || s.TilesDomain == "" || s.ExternalURL == "" {
		return ""
	}
	u, err := url.Parse(s.ExternalURL)
	label := s.Auth.TileHostIDDeployment(tile, dep)
	if err != nil || u.Scheme == "" || label == "" {
		return ""
	}
	host := label + "." + s.TilesDomain
	if !strings.Contains(s.TilesDomain, ":") && u.Port() != "" {
		host += ":" + u.Port()
	}
	return u.Scheme + "://" + host
}

// serveTileOrigin serves one request on tile origin id. Everything that is
// not the tile's own business — the workspace's pages (/login, /docs/, /),
// another tile's or chrome's documents — is sent to the workspace origin
// when a browser navigates there (tile code building links from
// location.origin keeps working); anything else is 404.
func (s *Server) serveTileOrigin(w http.ResponseWriter, r *http.Request, id string) {
	p := r.URL.Path
	switch {
	case id == "":
		http.NotFound(w, r)
		return
	case p == "/healthz":
		_, _ = w.Write([]byte("ok\n"))
		return
	case strings.HasPrefix(p, "/vendor/") && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		s.handleVendor(w, r) // xbind's own public code, as on the workspace origin
		return
	case strings.HasPrefix(p, "/c/"):
		if rel := strings.TrimPrefix(p, "/c/"); strings.HasPrefix(rel, "~") {
			http.NotFound(w, r)
			return
		} else if ref := s.docTargetOf(path.Clean("/" + rel)[1:]); isChrome(ref.tile) || !s.originServes(id, ref) {
			switch {
			case s.toWorkspace(w, r): // another tile's (or deployment's, or chrome's) page: its own origin, via the workspace
			case isChrome(ref.tile):
				http.NotFound(w, r) // chrome lives on the workspace origin only
			case ref.dep != "" && s.originIsTile(id, ref.tile) && documentRequest(r, ref.rest):
				http.Error(w, "a tile origin serves only its own deployment's documents", http.StatusForbidden)
			default:
				s.serveTileOriginAuthed(w, r, id) // another tile's assets: authorized for the user; documents refused
			}
			return
		}
	case strings.HasPrefix(p, "/api/"), p == "/ws/events":
	default:
		if !s.toWorkspace(w, r) {
			http.NotFound(w, r)
		}
		return
	}
	if strings.HasPrefix(p, "/c/") && isNavigation(r) {
		switch {
		case hasQueryKey(r.URL.RawQuery, ticketParam) && r.Header.Get("Sec-Fetch-Site") != "cross-site":
			s.tileOriginExchange(w, r, id)
			return
		case hasQueryKey(r.URL.RawQuery, beginParam):
			s.tileOriginBegin(w, r, id)
			return
		}
	}
	s.serveTileOriginAuthed(w, r, id)
}

// serveTileOriginAuthed resolves the tile credential and serves /c/, /api/
// or /ws/events as the tile. A document is served only on the cookie — a
// bare frame token in a navigation's URL renders nothing (its subresources
// could not load). A top-level navigation to the tile's own page without a
// (valid) cookie — expired, a bookmark, a shared link — is sent once
// through the workspace origin for a fresh ticket (the marker stops a loop
// when the cookie never sticks); a framed one gets the page asking for a
// reload from the workspace.
func (s *Server) serveTileOriginAuthed(w http.ResponseWriter, r *http.Request, id string) {
	pr, tile, viaCookie, code := s.tileOriginPrincipal(w, r, id)
	doc := strings.HasPrefix(r.URL.Path, "/c/") && isNavigation(r)
	if code == 0 && doc && !viaCookie {
		code = http.StatusUnauthorized
	}
	if code != 0 {
		if code == http.StatusUnauthorized && doc && topLevel(r) &&
			r.Header.Get("Sec-Fetch-Site") != "cross-site" && !hasQueryKey(r.URL.RawQuery, retryMarker) {
			q := dropQueryKey(dropQueryKeys(r.URL.RawQuery, exchangeParams...), retryMarker)
			if q != "" {
				q += "&"
			}
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, strings.TrimRight(s.ExternalURL, "/")+s.workspacePath(r)+"?"+q+retryMarker+"=1", http.StatusFound)
			return
		}
		s.tileOriginDenied(w, r, code)
		return
	}
	if pr.ReadOnly() && !readOnlyAllowed(r) { // a view-as session, as authed() enforces on the workspace
		refuseReadOnly(w, r)
		return
	}
	if doc && hasQueryKey(r.URL.RawQuery, "frame") { // the cookie suffices: keep tokens out of location
		s.cleanRedirect(w, r)
		return
	}
	r = r.WithContext(context.WithValue(auth.WithPrincipal(r.Context(), pr), tileOriginKey{}, tile))
	switch p := r.URL.Path; {
	case strings.HasPrefix(p, "/c/"):
		w.Header().Set("Content-Security-Policy", s.tileFrameAncestors())
		s.handleComponentStatic(w, s.onDeploymentOrigin(r, tile, pr.Deployment))
	case strings.HasPrefix(p, "/api/"):
		r = withoutTileCookie(r)
		s.handleAPI(noSetCookie(w), r.WithContext(auth.WithNoSetCookie(r.Context())))
	default:
		s.handleEventsWS(w, r)
	}
}

// topLevel: a top-level navigation (or a browser sending no Fetch Metadata).
func topLevel(r *http.Request) bool {
	d := r.Header.Get("Sec-Fetch-Dest")
	return d == "" || d == "document"
}

// toWorkspace sends a browser navigation to the same path and query on the
// workspace origin (--external-url) and reports whether it did.
func (s *Server) toWorkspace(w http.ResponseWriter, r *http.Request) bool {
	if !isNavigation(r) || s.ExternalURL == "" {
		return false
	}
	loc := strings.TrimRight(s.ExternalURL, "/") + r.URL.EscapedPath()
	if q := dropQueryKeys(r.URL.RawQuery, exchangeParams...); q != "" {
		loc += "?" + q
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, loc, http.StatusFound)
	return true
}

// cleanRedirect redirects to r's own URL without its credentials.
func (s *Server) cleanRedirect(w http.ResponseWriter, r *http.Request) {
	loc := r.URL.EscapedPath()
	if q := dropQueryKeys(r.URL.RawQuery, exchangeParams...); q != "" {
		loc += "?" + q
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, loc, http.StatusFound)
}

// tileOriginPrincipal resolves the credential on a tile-origin request: an
// explicit frame token (xbin.fetch's header, ?frame= on WebSockets and
// xbin.url) — which must be THIS tile's — and/or the tile cookie. A cookie
// is ambient, so a cookie-only request must come from the tile origin
// itself (cookieRequestAllowed: sibling tile origins are same-SITE, and
// SameSite=Strict alone would let them ride it). Returns the principal, the
// tile, whether the cookie vouched for it, or an HTTP status to refuse with.
func (s *Server) tileOriginPrincipal(w http.ResponseWriter, r *http.Request, id string) (auth.Principal, string, bool, int) {
	var (
		p    auth.Principal
		tile string
		have bool
	)
	ft := r.Header.Get(auth.FrameTokenHeader)
	if ft == "" {
		ft = r.URL.Query().Get("frame")
	}
	if ft != "" {
		fp, ok := s.Auth.FramePrincipal(ft)
		if !ok {
			return p, "", false, http.StatusUnauthorized
		}
		tile = s.owningComponent(fp.Component)
		if s.Auth.TileHostIDDeployment(tile, fp.Deployment) != id {
			return p, "", false, http.StatusForbidden // another tile's (or deployment's) token on this origin
		}
		p, have = fp, true
	}
	viaCookie := false
	c, err := auth.OnlyCookie(r, tileCookieName(r))
	if err != nil && err != http.ErrNoCookie && !have {
		return p, "", false, http.StatusUnauthorized // duplicates: one was tossed in
	}
	if c != nil {
		g, ok := s.Auth.VerifyTileCookie(c.Value)
		switch {
		case !ok || s.Auth.TileHostIDDeployment(g.Tile, g.Deployment) != id:
			// a dead cookie next to a token: the login behind this browser's
			// tile session ended — the token must not outlive it
			return p, "", false, http.StatusUnauthorized
		case have:
			if g.UserID != p.UserID { // cross-user replay
				return p, "", false, http.StatusUnauthorized
			}
			p.Impersonator, viaCookie = g.Impersonator, true
			s.slideTileCookie(w, r, g)
		default:
			if !cookieRequestAllowed(r) {
				return p, "", false, http.StatusForbidden
			}
			tp, ok := s.Auth.TilePrincipal(g)
			if !ok {
				return p, "", false, http.StatusUnauthorized
			}
			p, tile, have, viaCookie = tp, g.Tile, true, true
			s.slideTileCookie(w, r, g)
		}
	}
	if !have {
		return p, "", false, http.StatusUnauthorized
	}
	if !s.Auth.UserCanReadTile(p.UserID, tile) || !s.originLevelHolds(p.UserID, tile, p.Deployment) {
		return p, "", false, http.StatusForbidden // RBAC changed since the credential was minted
	}
	return p, tile, viaCookie, 0
}

// cookieRequestAllowed: a request authenticated only by the ambient tile
// cookie must come from the tile's own origin (or be a top-level visit
// with no initiator). Sibling tile origins are same-site, so they can make
// the browser attach this origin's SameSite=Strict cookie — they are
// allowed only to NAVIGATE to its /c/ documents (the shell framing or
// opening the tile does exactly that; frame-ancestors then refuses any
// framing but the workspace's), never to fetch, post or open a WebSocket
// with it.
func cookieRequestAllowed(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		foreign := err != nil || !strings.EqualFold(u.Host, r.Host) // "null" included
		write := r.Method != http.MethodGet && r.Method != http.MethodHead
		if foreign && (write || strings.EqualFold(r.Header.Get("Upgrade"), "websocket")) {
			return false
		}
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	case "same-site":
		return isNavigation(r) && strings.HasPrefix(r.URL.Path, "/c/")
	}
	return false
}

// tileOriginBegin starts an exchange (?xbin_begin=<hint>, from the
// workspace). A tile cookie already bound to the login the hint names
// needs none: the parameter is dropped. Otherwise the browser gets this
// origin's exchange state — kept, when it has one, so two loads of the
// tile racing each other share it — and goes back to the workspace, which
// mints a ticket for its session and that state (tilenav.go).
func (s *Server) tileOriginBegin(w http.ResponseWriter, r *http.Request, id string) {
	if hint := r.URL.Query().Get(beginParam); hint != "" {
		if c, err := auth.OnlyCookie(r, tileCookieName(r)); err == nil {
			if g, ok := s.Auth.VerifyTileCookie(c.Value); ok && s.Auth.TileHostIDDeployment(g.Tile, g.Deployment) == id &&
				subtle.ConstantTimeCompare([]byte(s.Auth.TileBindingHint(g.Gen)), []byte(hint)) == 1 {
				s.cleanRedirect(w, r)
				return
			}
		}
	}
	state := ""
	if c, err := auth.OnlyCookie(r, tileStateCookieName(r)); err == nil && auth.ValidTileState(c.Value) {
		state = c.Value
	} else {
		state = auth.NewTileState()
	}
	http.SetCookie(w, &http.Cookie{
		Name: tileStateCookieName(r), Value: state, Path: "/", HttpOnly: true, Secure: auth.SecureRequest(r),
		SameSite: http.SameSiteLaxMode, MaxAge: int(auth.TileTicketTTL.Seconds()),
	})
	q := dropQueryKeys(r.URL.RawQuery, exchangeParams...)
	if q != "" {
		q += "&"
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, strings.TrimRight(s.ExternalURL, "/")+s.workspacePath(r)+"?"+q+stateParam+"="+state, http.StatusFound)
}

// tileOriginExchange trades a navigation's one-time ticket for the tile
// cookie and redirects to the same URL without it (so it never stays in
// location or history). The ticket must be for this browser's exchange
// state. A failed exchange never redirects — no loops.
func (s *Server) tileOriginExchange(w http.ResponseWriter, r *http.Request, id string) {
	state := ""
	if c, err := auth.OnlyCookie(r, tileStateCookieName(r)); err == nil {
		state = c.Value
	}
	g, ok := s.Auth.RedeemTileTicket(r.URL.Query().Get(ticketParam), state)
	if !ok || isChrome(g.Tile) || s.Auth.TileHostIDDeployment(g.Tile, g.Deployment) != id ||
		!s.Auth.UserCanReadTile(g.UserID, g.Tile) || !s.originLevelHolds(g.UserID, g.Tile, g.Deployment) {
		s.tileOriginDenied(w, r, http.StatusUnauthorized)
		return
	}
	s.setTileCookie(w, r, g)
	s.cleanRedirect(w, r)
}

// tileStateCookieName: the exchange state's cookie (secure: __Host-).
func tileStateCookieName(r *http.Request) string {
	if auth.SecureRequest(r) {
		return auth.HostTileStateCookieName
	}
	return auth.TileStateCookieName
}

// tileCookieName: __Host-xbin_tile on a secure origin — browsers then
// refuse the name with a Domain attribute or without Secure, so a sibling
// tile origin can't toss one in — else xbin_tile (plain-http deployments,
// warned about at boot).
func tileCookieName(r *http.Request) string {
	if auth.SecureRequest(r) {
		return auth.HostTileCookieName
	}
	return auth.TileCookieName
}

// setTileCookie issues the tile-origin cookie for grant g (a redeemed
// ticket, or the cookie being slid): TileCookieTTL, never past the end of
// the session it is bound to.
func (s *Server) setTileCookie(w http.ResponseWriter, r *http.Request, g auth.AssetGrant) {
	ttl := auth.TileCookieTTL
	if !g.SessionEnd.IsZero() {
		ttl = min(ttl, time.Until(g.SessionEnd))
	}
	if ttl < time.Second {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: tileCookieName(r), Value: s.Auth.MintTileCookieDeployment(g.Tile, g.Deployment, g.UserID, g.Gen, ttl), Path: "/",
		HttpOnly: true, Secure: auth.SecureRequest(r), SameSite: http.SameSiteStrictMode,
		MaxAge: int(ttl.Seconds()),
	})
}

// slideTileCookie re-issues a cookie in use once half its TTL has elapsed.
func (s *Server) slideTileCookie(w http.ResponseWriter, r *http.Request, g auth.AssetGrant) {
	if time.Until(g.Exp) < auth.TileCookieTTL/2 {
		s.setTileCookie(w, r, g)
	}
}

// tileOriginDenied answers an uncredentialed or refused tile-origin request.
// A browser navigation gets a page pointing back to the workspace (whose
// /c/ URL re-enters through a fresh exchange); nothing redirects on its own.
func (s *Server) tileOriginDenied(w http.ResponseWriter, r *http.Request, code int) {
	w.Header().Set("Cache-Control", "no-store")
	if !isNavigation(r) || s.ExternalURL == "" {
		http.Error(w, http.StatusText(code)+" — this tile origin needs the tile's credential; open the tile from the workspace", code)
		return
	}
	back := strings.TrimRight(s.ExternalURL, "/") + s.workspacePath(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; "+s.tileFrameAncestors())
	w.WriteHeader(code)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>xbin</title>
<body style="font:14px system-ui,sans-serif;max-width:36rem;margin:3rem auto;padding:0 1rem">
<p>This tile's session on its own origin has ended or was never started — reload the tile from the workspace.</p>
<p><a href="%s" target="_top">Open it from the workspace</a></p></body>`, htmlEscape(back))
}

// hasQueryKey / dropQueryKey work on the raw query so the other parameters
// keep their exact spelling and order.
func hasQueryKey(raw, key string) bool {
	for _, kv := range strings.Split(raw, "&") {
		k, _, _ := strings.Cut(kv, "=")
		if uk, err := url.QueryUnescape(k); err == nil && uk == key {
			return true
		}
	}
	return false
}

// dropQueryKeys drops every one of keys.
func dropQueryKeys(raw string, keys ...string) string {
	for _, k := range keys {
		raw = dropQueryKey(raw, k)
	}
	return raw
}

func dropQueryKey(raw, key string) string {
	var keep []string
	for _, kv := range strings.Split(raw, "&") {
		if kv == "" {
			continue
		}
		k, _, _ := strings.Cut(kv, "=")
		if uk, err := url.QueryUnescape(k); err == nil && uk == key {
			continue
		}
		keep = append(keep, kv)
	}
	return strings.Join(keep, "&")
}

// ---- deployment origins (11-contract §2.6) (D127j) ----

// DeploymentNamesPolicy is a Policy that names every deployment of a tile:
// its primary and each name, main first (the deployments plane's
// DeploymentsOf, in-memory). Origins mode maps an origin label back to
// (tile, deployment) with it. Without it the labels known for a tile are
// main's and its primary's, which is every label of a tile without a
// deployment record.
type DeploymentNamesPolicy interface {
	DeploymentsOf(tile string) (primary string, names []string)
}

// deploymentNames lists the deployments of tile whose origin labels
// originDeployment knows.
func (s *Server) deploymentNames(tile string) []string {
	if dp, ok := s.policy().(DeploymentNamesPolicy); ok {
		_, names := dp.DeploymentsOf(tile)
		return names
	}
	names := []string{util.MainDeployment}
	if p := s.primaryOf(tile); p != util.MainDeployment {
		names = append(names, p)
	}
	return names
}

// originDeployment answers which deployment of tile the origin labelled id
// serves: the one whose label it is (main's is TileHostID(tile), so a tile
// without a record answers main exactly where today's comparison matched).
// ok is false for any other label: another tile's origin, chrome, or a
// deployment that no longer exists.
func (s *Server) originDeployment(tile, id string) (string, bool) {
	if id == "" || isChrome(tile) {
		return "", false
	}
	for _, n := range s.deploymentNames(tile) {
		if s.Auth.TileHostIDDeployment(tile, n) == id {
			return n, true
		}
	}
	return "", false
}

// originOf is the label → (tile, deployment) lookup over the registered
// tiles and their deployments.
func (s *Server) originOf(id string) (tile, dep string, ok bool) {
	for _, c := range s.Reg.Components() {
		if dep, ok := s.originDeployment(c.Path, id); ok {
			return c.Path, dep, true
		}
	}
	return "", "", false
}

// docTarget is what a cleaned /c/ path names: the tile whose code it is,
// the deployment a deployment URL names ("" for a bare path: the primary on
// the workspace, the origin's deployment on a tile origin) and the rest
// beneath the tile.
type docTarget struct{ tile, dep, rest string }

func (s *Server) docTargetOf(cleaned string) docTarget {
	if q, ok := s.resolveQualified(cleaned); ok {
		return docTarget{tile: q.c.Path, dep: q.dep, rest: q.rest}
	}
	owner := s.owningComponent(cleaned)
	return docTarget{tile: owner, rest: tileRel(owner, cleaned)}
}

// originServes reports whether the origin labelled id serves what ref
// names: a bare path of its tile, or a deployment URL naming its own
// deployment of its tile (the alias of the primary, on the primary's own
// origin, included).
func (s *Server) originServes(id string, ref docTarget) bool {
	if ref.dep != "" {
		return !isChrome(ref.tile) && s.Auth.TileHostIDDeployment(ref.tile, ref.dep) == id
	}
	_, ok := s.originDeployment(ref.tile, id)
	return ok
}

// originIsTile reports whether the origin labelled id is one of tile's.
func (s *Server) originIsTile(id, tile string) bool {
	_, ok := s.originDeployment(tile, id)
	return ok
}

// originLevelHolds is a tile origin's level check on every request, beside
// the read check: the origin of a deployment other than the primary needs
// the user to write the tile, at their current level, as that deployment's
// URL does on the workspace (11-contract §2.3) (D127l). dep is the
// credential's ("" is main).
func (s *Server) originLevelHolds(uid, tile, dep string) bool {
	return cmp.Or(dep, util.MainDeployment) == s.primaryOf(tile) || s.userWritesTile(uid, tile)
}

// onDeploymentOrigin serves a bare /c/<tile>/… request on the origin of a
// deployment of tile other than its primary as /c/<tile>+<dep>/…: on a
// deployment's origin both name it (11-contract §2.6), so its documents'
// absolute self-URLs stay in it, and the deployment URL's gate and code
// answer. Anything else is r as it is: the primary's origin, a deployment
// URL, another tile's file, a deps/ link re-dispatched to the tile's bare
// URL, which serves its primary (07-runtime §4.1), and a deployment URL
// that something on disk shadows (11-contract §2.1).
func (s *Server) onDeploymentOrigin(r *http.Request, tile, dep string) *http.Request {
	dep = cmp.Or(dep, util.MainDeployment)
	if dep == s.primaryOf(tile) || r.Context().Value(depsHopsKey{}) != nil {
		return r
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/c/"))[1:]
	if ref := s.docTargetOf(cleaned); ref.dep != "" || ref.tile != tile {
		return r
	}
	qp := qualifiedDocPath(r, tile, dep, cleaned)
	if ref := s.docTargetOf(path.Clean(qp)[len("/c/"):]); ref.tile != tile || ref.dep != dep {
		return r
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path, r2.URL.RawPath = qp, ""
	r2.RequestURI = r2.URL.RequestURI()
	return r2
}

// qualifiedDocPath is r's cleaned /c/ path of tile at deployment dep's URL,
// /c/<tile>+<dep>/<rest>, keeping r's trailing slash.
func qualifiedDocPath(r *http.Request, tile, dep, cleaned string) string {
	p := "/c/" + tile + "+" + dep + strings.TrimPrefix(cleaned, tile)
	if strings.HasSuffix(r.URL.Path, "/") && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// workspacePath is r's escaped path as a tile origin sends its browser to
// the workspace (a fresh exchange, the page asking for one): a bare path of
// the origin's own tile names the origin's deployment when that isn't the
// primary, so the workspace sends it back to this origin, never to the
// primary's. Every other path is r's own.
func (s *Server) workspacePath(r *http.Request) string {
	id, _ := s.tileHostOf(r.Host)
	if id == "" || !strings.HasPrefix(r.URL.Path, "/c/") {
		return r.URL.EscapedPath()
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/c/"))[1:]
	ref := s.docTargetOf(cleaned)
	if ref.dep != "" {
		return r.URL.EscapedPath()
	}
	if dep, ok := s.originDeployment(ref.tile, id); ok && dep != s.primaryOf(ref.tile) {
		return (&url.URL{Path: qualifiedDocPath(r, ref.tile, dep, cleaned)}).EscapedPath()
	}
	return r.URL.EscapedPath()
}
