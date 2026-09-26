package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
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
//  1. bx-frame (or a main-origin redirect for direct opens) navigates to
//     https://t-<id>.<tiles-domain>/c/<tile>/?frame=<frame token>;
//  2. the exchange verifies the token (its tile must be the origin's tile,
//     its user must still read it), sets the tile cookie — HttpOnly, Secure,
//     SameSite=Strict, host-only, Path=/ — and redirects to the clean URL;
//  3. every later request on that origin — relative or absolute /c/ loads,
//     workers, fetch — carries the cookie: /c/ is authorized live against
//     the user's access to the tile being loaded, /api and /ws act as the
//     tile's frame principal. Nothing else is served there.
//
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

// tileOrigins routes requests addressed to a tile origin (origins mode);
// in every other mode it is main unchanged.
func (s *Server) tileOrigins(main http.Handler) http.Handler {
	if s.assetMode() != TileAssetsOrigins || s.TilesDomain == "" {
		return main
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := s.tileHostOf(r.Host); ok {
			s.serveTileOrigin(w, r, id)
			return
		}
		main.ServeHTTP(w, r)
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

// serveTileOrigin serves one request on tile origin id.
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
	case strings.HasPrefix(p, "/c/"), strings.HasPrefix(p, "/api/"), p == "/ws/events", strings.HasPrefix(p, "/docs/"):
	default:
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(p, "/c/") && hasQueryKey(r.URL.RawQuery, "frame") && isNavigation(r) &&
		r.Header.Get("Sec-Fetch-Site") != "cross-site" {
		s.tileOriginExchange(w, r, id)
		return
	}
	pr, tile, code := s.tileOriginPrincipal(w, r, id)
	if code != 0 {
		s.tileOriginDenied(w, r, code)
		return
	}
	ctx := context.WithValue(auth.WithPrincipal(r.Context(), pr), tileOriginKey{}, tile)
	r = r.WithContext(ctx)
	switch {
	case strings.HasPrefix(p, "/c/"):
		rel := strings.TrimPrefix(p, "/c/")
		if isChrome(firstSeg(rel)) || strings.HasPrefix(rel, "~") {
			http.NotFound(w, r) // chrome lives on the workspace origin only
			return
		}
		s.handleComponentStatic(w, r)
	case strings.HasPrefix(p, "/api/"):
		s.handleAPI(w, r)
	case p == "/ws/events":
		s.handleEventsWS(w, r)
	default:
		s.handleDocs(w, r)
	}
}

// tileOriginPrincipal resolves the credential on a tile-origin request: an
// explicit frame token (xbin.fetch's header, ?frame= on WebSockets and
// xbin.url) — which must be THIS tile's — and/or the tile cookie. A cookie
// is ambient, so a cookie-only request must come from the tile origin
// itself (cookieRequestAllowed: sibling tile origins are same-SITE, and
// SameSite=Strict alone would let them ride it). Returns the principal, the
// tile, or an HTTP status to refuse with.
func (s *Server) tileOriginPrincipal(w http.ResponseWriter, r *http.Request, id string) (auth.Principal, string, int) {
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
			return p, "", http.StatusUnauthorized
		}
		tile = s.owningComponent(fp.Component)
		if s.Auth.TileHostID(tile) != id {
			return p, "", http.StatusForbidden // another tile's token on this origin
		}
		p, have = fp, true
	}
	if c, err := r.Cookie(auth.TileCookieName); err == nil {
		g, ok := s.Auth.VerifyTileCookie(c.Value)
		switch {
		case !ok || s.Auth.TileHostID(g.Tile) != id:
			if !have {
				return p, "", http.StatusUnauthorized
			}
		case have:
			if g.UserID != p.UserID { // cross-user replay
				return p, "", http.StatusUnauthorized
			}
		default:
			if !cookieRequestAllowed(r) {
				return p, "", http.StatusForbidden
			}
			tp, ok := s.Auth.TilePrincipal(g.Tile, g.UserID)
			if !ok {
				return p, "", http.StatusUnauthorized
			}
			p, tile, have = tp, g.Tile, true
			if time.Until(g.Exp) < auth.TileCookieTTL/2 {
				s.setTileCookie(w, r, g.Tile, g.UserID) // slide while in use
			}
		}
	}
	if !have {
		return p, "", http.StatusUnauthorized
	}
	if !s.Auth.UserCanReadTile(p.UserID, tile) {
		return p, "", http.StatusForbidden // RBAC changed since the credential was minted
	}
	return p, tile, 0
}

// cookieRequestAllowed: a request authenticated only by the ambient tile
// cookie must come from the tile's own origin (or be a top-level visit
// with no initiator). Sibling tile origins are same-site, so they can make
// the browser attach this origin's SameSite=Strict cookie — they are
// allowed only to NAVIGATE to its /c/ documents (the shell framing or
// opening the tile does exactly that), never to fetch, post or open a
// WebSocket with it.
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

// tileOriginExchange trades a navigation's ?frame= token for the tile
// cookie and redirects to the same URL without it (so the token never stays
// in location or history). A failed exchange never redirects — no loops.
func (s *Server) tileOriginExchange(w http.ResponseWriter, r *http.Request, id string) {
	fp, ok := s.Auth.FramePrincipal(r.URL.Query().Get("frame"))
	tile := s.owningComponent(fp.Component)
	if !ok || isChrome(tile) || s.Auth.TileHostID(tile) != id || !s.Auth.UserCanReadTile(fp.UserID, tile) {
		s.tileOriginDenied(w, r, http.StatusUnauthorized)
		return
	}
	s.setTileCookie(w, r, tile, fp.UserID)
	loc := r.URL.EscapedPath()
	if q := dropQueryKey(r.URL.RawQuery, "frame"); q != "" {
		loc += "?" + q
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, loc, http.StatusFound)
}

// setTileCookie issues the tile-origin cookie. Secure whenever the request
// arrived over TLS (or a TLS-terminating proxy) and on *.localhost, which
// browsers treat as a secure context (dev).
func (s *Server) setTileCookie(w http.ResponseWriter, r *http.Request, tile, uid string) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" || strings.HasSuffix(host, ".localhost")
	http.SetCookie(w, &http.Cookie{
		Name: auth.TileCookieName, Value: s.Auth.MintTileCookie(tile, uid), Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
		MaxAge: int(auth.TileCookieTTL.Seconds()),
	})
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
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(code)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>xbin</title>
<body style="font:14px system-ui,sans-serif;max-width:36rem;margin:3rem auto;padding:0 1rem">
<p>This tile's session on its own origin has ended or was never started.</p>
<p><a href="%s" target="_top">Open it from the workspace</a></p></body>`, htmlEscape(back))
}

// redirectToTileOrigin sends a browser navigating to a sandboxed tile's
// document on the WORKSPACE origin to the tile's origin, with a fresh frame
// token to exchange. Only a human (cookie/bearer) or the tile itself may be
// sent — the token is the tile's, and another tile must not obtain it this
// way. Reports whether it redirected.
func (s *Server) redirectToTileOrigin(w http.ResponseWriter, r *http.Request, owner string) bool {
	if !isNavigation(r) {
		return false
	}
	p := auth.PrincipalOf(r)
	if p.Component != "" && s.owningComponent(p.Component) != owner {
		return false
	}
	origin := s.tileOriginURL(owner)
	if origin == "" || !p.CanReadTile(owner) {
		return false
	}
	q := dropQueryKey(r.URL.RawQuery, "frame")
	if q != "" {
		q += "&"
	}
	tok := s.Auth.MintFrameToken(owner, p.UserID, frameTokenTTL)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, origin+r.URL.EscapedPath()+"?"+q+"frame="+url.QueryEscape(tok), http.StatusFound)
	return true
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
