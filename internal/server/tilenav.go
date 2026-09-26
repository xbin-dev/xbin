package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
)

// The workspace side of per-tile origins (origins mode; tileorigin.go).
// Tile origins are same-SITE with the workspace — the point: their cookie is
// first-party in the shell — so, unlike legacy's opaque tile frames (cross-
// site: the Lax session cookie never reached the workspace from them), a
// tile origin's requests to the workspace would carry the session cookie.
// This file takes that back and decides how a navigation to a tile's
// document reaches the tile's origin.

// withoutTileInitiatedCookies drops every cookie from a workspace-origin
// request a tile origin (or any other same-site page) initiated — what
// SameSite=Lax did for legacy's cross-site tile frames — except a
// top-level GET navigation, which Lax lets through cross-site too:
//
//   - Sec-Fetch-Site: same-site, unless a top-level GET navigation (a tile
//     framing the shell, another tile or /logout gets no session);
//   - an Origin on the tiles domain — every browser sends Origin on POST
//     and CORS requests, Fetch Metadata or not;
//   - a Referer on the tiles domain, unless a navigation (the browsers
//     without Fetch Metadata; frame-ancestors backs up their frames).
//
// Bearer tooling (Authorization) is unaffected.
func (s *Server) withoutTileInitiatedCookies(r *http.Request) *http.Request {
	if r.Header.Get("Cookie") == "" || r.Header.Get("Authorization") != "" {
		return r
	}
	drop := false
	switch {
	case r.Header.Get("Sec-Fetch-Site") == "same-site":
		drop = !(isNavigation(r) && (topLevel(r) || s.navWithinTree(r) || s.exchangeReturn(r)))
	case s.onTilesDomain(r.Header.Get("Origin")):
		drop = true
	case s.onTilesDomain(r.Header.Get("Referer")):
		drop = !isNavigation(r)
	}
	if !drop {
		return r
	}
	return auth.WithoutCookie(r)
}

// exchangeReturn: the second workspace leg of a tile-origin exchange — a
// navigation to a sandboxed tile's document carrying the tile origin's
// exchange state, which the tile origin just sent here (the chain passed
// through it, so the browser calls it same-site). It needs the session to
// mint the ticket; all it can answer is a redirect to that tile's origin
// with a ticket for this browser's own session, bound to a state only this
// browser's tile origin cookie holds — nothing another page can read or
// plant.
func (s *Server) exchangeReturn(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/c/") || !hasQueryKey(r.URL.RawQuery, stateParam) {
		return false
	}
	owner := s.owningComponent(path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/c/"))[1:])
	c, ok := s.Reg.Component(owner)
	return ok && sandboxedFrame(owner, c)
}

// onTilesDomain: an Origin or Referer value naming a host under the tiles
// domain.
func (s *Server) onTilesDomain(v string) bool {
	if v == "" || v == "null" {
		return false
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return false
	}
	_, ok := s.tileHostOf(u.Host)
	return ok
}

// workspaceOrigin is --external-url's origin (scheme://host[:port]).
func (s *Server) workspaceOrigin() string {
	u, err := url.Parse(s.ExternalURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// tileFrameAncestors is the CSP every tile-origin /c/ response and page
// carries: only the workspace (the shell, chrome) and the tile itself may
// frame it — never another tile's origin, which is same-site and would
// otherwise frame it authenticated (clickjacking; answering its
// xbin:dialog requests as its parent).
func (s *Server) tileFrameAncestors() string {
	return "frame-ancestors 'self' " + s.workspaceOrigin()
}

// setDocCSP sets a document's CSP, keeping the tile origin's
// frame-ancestors on a tile origin.
func (s *Server) setDocCSP(w http.ResponseWriter, r *http.Request, policy string) {
	if tileOriginOf(r) != "" {
		policy += "; " + s.tileFrameAncestors()
	}
	w.Header().Set("Content-Security-Policy", policy)
}

// tileDocOnWorkspace answers a browser navigating to a sandboxed tile's
// document on the WORKSPACE origin (origins mode): tile documents run on
// their own origin only. Reports whether it answered; false means "serve it
// here" — a client carrying its credential in a header (the native app's
// scheme handler, bx, backends) is not a browser navigating.
//
// The principal must be a human or the tile itself (the credential is the
// tile's; another tile never obtains it this way) with a live browser
// session to bind the ticket to. The exchange takes two legs here
// (tileorigin.go): the first sends the browser to the tile origin with
// ?xbin_begin=<the session's binding hint>; the tile origin comes back with
// ?xbin_state=<its exchange state> (exchangeReturn), answered with a 302
// to the tile origin and a ticket bound to the session and that state. How
// the first leg continues depends on who initiated it:
//
//   - the workspace itself (the shell framing the tile, a chrome page),
//     the user (typed, a bookmark), or a browser without Fetch Metadata
//     that shows no tile Referer: 302 to the tile origin;
//   - anyone else — another tile's origin (same-site), a link in chat or
//     mail (cross-site): a same-origin interstitial that continues there,
//     so the chain the tile origin sees starts at the workspace, and which
//     refuses to render in a frame.
//
// Anything else is refused: no tile document runs on the workspace origin
// in origins mode.
func (s *Server) tileDocOnWorkspace(w http.ResponseWriter, r *http.Request, owner string) bool {
	if !isNavigation(r) || r.Header.Get(auth.FrameTokenHeader) != "" || r.Header.Get("Authorization") != "" {
		return false
	}
	p := auth.PrincipalOf(r)
	origin := s.tileOriginURL(owner)
	binding, bound := s.Auth.TileBinding(r, p.UserID)
	if origin == "" || !p.CanReadTile(owner) || (p.Component != "" && s.owningComponent(p.Component) != owner) || !bound {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		http.Error(w, "this tile runs on its own origin — open it from the workspace (docs/auth.md §Tile asset gating)", http.StatusForbidden)
		return true
	}
	q := dropQueryKeys(r.URL.RawQuery, exchangeParams...)
	if q != "" {
		q += "&"
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	if st := r.URL.Query().Get(stateParam); auth.ValidTileState(st) && r.Header.Get("Sec-Fetch-Site") != "cross-site" {
		// the return leg: a ticket for this session and the tile origin's
		// state (a state from anywhere else only yields a ticket its tile
		// origin refuses)
		http.Redirect(w, r, origin+r.URL.EscapedPath()+"?"+q+ticketParam+"="+url.QueryEscape(s.Auth.MintTileTicket(owner, p.UserID, binding, st)), http.StatusFound)
		return true
	}
	target := origin + r.URL.EscapedPath() + "?" + q + beginParam + "=" + url.QueryEscape(s.Auth.TileBindingHint(binding))
	if s.trustedInitiator(r) {
		http.Redirect(w, r, target, http.StatusFound)
		return true
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	h.Set("X-Frame-Options", "DENY")
	t := htmlEscape(target)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="0;url=%s"><title>xbin</title>
<body style="font:14px system-ui,sans-serif;max-width:36rem;margin:3rem auto;padding:0 1rem">
<p>Opening the tile… <a href="%s">continue</a></p></body>`, t, t)
	return true
}

// trustedInitiator: the navigation came from the workspace itself or from
// the user, or moves a tile's frame between the pages of its own tree
// (navWithinTree) — or comes from a browser without Fetch Metadata whose
// Referer is not a tile origin (frame-ancestors on the tile's documents
// backs this up).
func (s *Server) trustedInitiator(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "same-site":
		return s.navWithinTree(r)
	case "":
		return !s.onTilesDomain(r.Header.Get("Referer")) || s.navWithinTree(r)
	}
	return false
}

// navWithinTree: a navigation to a /c/ document whose Referer is the tile
// origin of a component in the same tile tree (sameTileTree) — a
// multi-page tile moving its frame to a page registered as a nested
// component of its own, which lives on that component's origin and arrives
// here through the tile origin's hand-off (toWorkspace). The browser sets
// Referer; another tile can suppress its own but never claim this one's.
func (s *Server) navWithinTree(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/c/") {
		return false
	}
	ref, err := url.Parse(r.Header.Get("Referer"))
	if err != nil || ref.Host == "" {
		return false
	}
	label, ok := s.tileHostOf(ref.Host)
	if !ok || label == "" {
		return false
	}
	target := s.owningComponent(path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/c/"))[1:])
	for _, c := range s.Reg.Components() {
		if s.Auth.TileHostID(c.Path) == label {
			return s.sameTileTree(c.Path, target)
		}
	}
	return false
}

// withoutTileCookie strips the tile cookie from a request proxied to a
// component backend: it is the frame principal's credential, and a backend
// replaying it would act as its own frontend driven by that user.
func withoutTileCookie(r *http.Request) *http.Request {
	cs := r.Cookies()
	keep := cs[:0]
	for _, c := range cs {
		if c.Name != auth.TileCookieName && c.Name != auth.HostTileCookieName {
			keep = append(keep, c)
		}
	}
	if len(keep) == len(r.Cookies()) {
		return r
	}
	r2 := r.Clone(r.Context())
	r2.Header = r.Header.Clone()
	r2.Header.Del("Cookie")
	for _, c := range keep {
		r2.AddCookie(c)
	}
	return r2
}

// noSetCookie wraps a tile-origin /api response so nothing behind it — a
// component backend, a handler — sets cookies on the tile origin: a
// Set-Cookie with Domain=<parent> would toss cookies into the workspace and
// every sibling tile. The tile cookie xbind itself set (a slide) before the
// wrapper is kept.
func noSetCookie(w http.ResponseWriter) http.ResponseWriter {
	return &cookieGuard{ResponseWriter: w, keep: w.Header().Values("Set-Cookie")}
}

type cookieGuard struct {
	http.ResponseWriter
	keep []string
	done bool
}

func (g *cookieGuard) scrub() {
	if g.done {
		return
	}
	g.done = true
	h := g.ResponseWriter.Header()
	h.Del("Set-Cookie")
	for _, v := range g.keep {
		h.Add("Set-Cookie", v)
	}
}

func (g *cookieGuard) WriteHeader(code int) { g.scrub(); g.ResponseWriter.WriteHeader(code) }
func (g *cookieGuard) Write(b []byte) (int, error) {
	g.scrub()
	return g.ResponseWriter.Write(b)
}
func (g *cookieGuard) Flush() {
	g.scrub()
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (g *cookieGuard) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	g.scrub()
	if h, ok := g.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}
func (g *cookieGuard) Unwrap() http.ResponseWriter { return g.ResponseWriter }
