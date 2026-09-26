package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

// Per-tile origins (--tile-assets=origins; plans/tile-asset-auth.md
// mechanism A). Each tile's frontend is served from its own origin
// t-<id>.<tiles-domain> (id: auth.TileHostID, a keyed hash of the tile
// path), a subdomain of the workspace's own site so its cookie is
// first-party for the embedding shell. The flow:
//
//  1. a browser navigates to a sandboxed tile's document on the WORKSPACE
//     origin — bx-frame's iframe, a direct open, a link — and the workspace
//     sends it to https://t-<id>.<tiles-domain>/<same path>?xbin_ticket=…,
//     a one-time exchange ticket bound to the browser session
//     (tilenav.go decides how, by who initiated the navigation);
//  2. the tile origin redeems the ticket — its tile must be the origin's,
//     its session still live, its user still able to read the tile — sets
//     the tile cookie (__Host-xbin_tile: HttpOnly, Secure, SameSite=Strict,
//     host-only, Path=/) and redirects to the clean URL;
//  3. every later request on that origin carries the cookie: /c/ is
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

// ticketParam carries the exchange ticket; retryMarker marks a tile-origin →
// workspace → tile-origin credential refresh, so it happens once.
const (
	ticketParam = "xbin_ticket"
	retryMarker = "xbin_retry"
)

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
// or "" outside origins mode. Scheme and port follow --external-url unless
// --tiles-domain carries its own port.
func (s *Server) tileOriginURL(tile string) string {
	if s.assetMode() != TileAssetsOrigins || s.TilesDomain == "" || s.ExternalURL == "" {
		return ""
	}
	u, err := url.Parse(s.ExternalURL)
	if err != nil || u.Scheme == "" {
		return ""
	}
	host := s.Auth.TileHostID(tile) + "." + s.TilesDomain
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
		} else if owner := s.owningComponent(path.Clean("/" + rel)[1:]); isChrome(owner) || s.Auth.TileHostID(owner) != id {
			switch {
			case s.toWorkspace(w, r): // another tile's (or chrome's) page: its own origin, via the workspace
			case isChrome(owner):
				http.NotFound(w, r) // chrome lives on the workspace origin only
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
	if strings.HasPrefix(p, "/c/") && hasQueryKey(r.URL.RawQuery, ticketParam) && isNavigation(r) &&
		r.Header.Get("Sec-Fetch-Site") != "cross-site" {
		s.tileOriginExchange(w, r, id)
		return
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
			q := dropQueryKey(dropQueryKey(dropQueryKey(r.URL.RawQuery, "frame"), ticketParam), retryMarker)
			if q != "" {
				q += "&"
			}
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, strings.TrimRight(s.ExternalURL, "/")+r.URL.EscapedPath()+"?"+q+retryMarker+"=1", http.StatusFound)
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
		s.handleComponentStatic(w, r)
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
	if q := dropQueryKey(dropQueryKey(r.URL.RawQuery, "frame"), ticketParam); q != "" {
		loc += "?" + q
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, loc, http.StatusFound)
	return true
}

// cleanRedirect redirects to r's own URL without its credentials.
func (s *Server) cleanRedirect(w http.ResponseWriter, r *http.Request) {
	loc := r.URL.EscapedPath()
	if q := dropQueryKey(dropQueryKey(r.URL.RawQuery, "frame"), ticketParam); q != "" {
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
		if s.Auth.TileHostID(tile) != id {
			return p, "", false, http.StatusForbidden // another tile's token on this origin
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
		case !ok || s.Auth.TileHostID(g.Tile) != id:
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
	if !s.Auth.UserCanReadTile(p.UserID, tile) {
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

// tileOriginExchange trades a navigation's one-time ticket for the tile
// cookie and redirects to the same URL without it (so it never stays in
// location or history). A failed exchange never redirects — no loops.
func (s *Server) tileOriginExchange(w http.ResponseWriter, r *http.Request, id string) {
	g, ok := s.Auth.RedeemTileTicket(r.URL.Query().Get(ticketParam))
	if !ok || isChrome(g.Tile) || s.Auth.TileHostID(g.Tile) != id || !s.Auth.UserCanReadTile(g.UserID, g.Tile) {
		s.tileOriginDenied(w, r, http.StatusUnauthorized)
		return
	}
	s.setTileCookie(w, r, g)
	s.cleanRedirect(w, r)
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
		Name: tileCookieName(r), Value: s.Auth.MintTileCookie(g.Tile, g.UserID, g.Gen, ttl), Path: "/",
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
	back := strings.TrimRight(s.ExternalURL, "/") + r.URL.EscapedPath()
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
