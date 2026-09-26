package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
)

// originHost: tile's origin host (with the harness port).
func (w *assetWS) originHost(tile string) string {
	return w.a.TileHostID(tile) + ".xbin.localhost:9260"
}

var (
	shellNav = []reqOpt{hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Dest", "iframe"), hdr("Sec-Fetch-Site", "same-origin")}
	hopNav   = []reqOpt{hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Dest", "iframe"), hdr("Sec-Fetch-Site", "same-site")}
	sameOrig = hdr("Sec-Fetch-Site", "same-origin")
)

// ticketURL is where the workspace sends the shell's frame for a tile
// document path, for the session sess (a session cookie option).
func (w *assetWS) ticketURL(p string, sess reqOpt) (*url.URL, *httptest.ResponseRecorder) {
	w.t.Helper()
	rec := w.do(p, append(shellNav, sess)...)
	if rec.Code != http.StatusFound {
		return nil, rec
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		w.t.Fatal(err)
	}
	return u, rec
}

// exchangeURL follows a ticket URL on the tile origin and returns the tile
// cookie it set.
func (w *assetWS) exchangeURL(u *url.URL, opts ...reqOpt) (*http.Cookie, *httptest.ResponseRecorder) {
	w.t.Helper()
	rec := w.do(u.RequestURI(), append(append([]reqOpt{host(u.Host)}, hopNav...), opts...)...)
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.HostTileCookieName {
			return c, rec
		}
	}
	return nil, rec
}

// exchange runs the whole shell flow for (tile, uid) on path: workspace
// 302 with a ticket → the tile origin's exchange. Returns the cookie.
func (w *assetWS) exchange(tile, uid, p string) (*http.Cookie, *httptest.ResponseRecorder) {
	w.t.Helper()
	u, rec := w.ticketURL(p+"?x=1&y=2", w.session(uid))
	if u == nil {
		w.t.Fatalf("workspace leg for %s as %s: %d %v", tile, uid, rec.Code, rec.Header())
	}
	if u.Host != w.originHost(tile) {
		w.t.Fatalf("sent to %s, want %s", u.Host, w.originHost(tile))
	}
	return w.exchangeURL(u)
}

// A: the workspace sends the shell's frame to the tile origin with a
// one-time, session-bound ticket; the exchange sets a __Host- cookie
// (host-only, HttpOnly, Secure, SameSite=Strict) and redirects to the same
// URL without the ticket; the clean document is sandboxed WITH
// allow-same-origin, framable only by the workspace and itself. A failed
// exchange never redirects and sets nothing.
func TestOriginsExchange(t *testing.T) {
	w := newAssetWS(t, TileAssetsOrigins)
	u, rec := w.ticketURL("/c/apps/a/?x=1&y=2", w.session("ana"))
	if u == nil || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("workspace leg: %d %v", rec.Code, rec.Header())
	}
	if u.Query().Get("x") != "1" || u.Query().Get(ticketParam) == "" || u.Query().Get("frame") != "" {
		t.Fatalf("ticket URL %s", u)
	}
	c, rec := w.exchangeURL(u)
	if rec.Code != http.StatusFound || c == nil {
		t.Fatalf("exchange: %d %v", rec.Code, rec.Header())
	}
	if loc := rec.Header().Get("Location"); loc != "/c/apps/a/?x=1&y=2" {
		t.Fatalf("redirect %q must drop only the ticket", loc)
	}
	if c.Domain != "" || c.Path != "/" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || !c.Secure || c.MaxAge <= 0 {
		t.Fatalf("cookie scope: %+v", c)
	}
	// The ticket is one-time.
	if c2, rec := w.exchangeURL(u); c2 != nil || rec.Code != http.StatusUnauthorized || rec.Header().Get("Location") != "" {
		t.Fatalf("replayed ticket: %d %v", rec.Code, c2)
	}
	oa := host(w.originHost("apps/a"))
	rec = w.do("/c/apps/a/", oa, cookie(c.Name, c.Value), hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Site", "same-site"))
	csp := rec.Header().Get("Content-Security-Policy")
	if rec.Code != 200 || !strings.Contains(csp, "allow-same-origin") || !strings.Contains(csp, "frame-ancestors 'self' http://xbin.localhost:9260") {
		t.Fatalf("clean URL with cookie: %d %q", rec.Code, csp)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `content="origins"`) || !strings.Contains(body, `<meta name="xbin-workspace-origin" content="http://xbin.localhost:9260">`) {
		t.Fatal("origins document lacks its metas")
	}
	// Every /c/ response of the origin keeps frame-ancestors.
	if rec := w.do("/c/apps/a/img.svg", oa, cookie(c.Name, c.Value), sameOrig); !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors") {
		t.Fatalf("asset without frame-ancestors: %v", rec.Header())
	}

	// Failed exchanges: another tile's ticket, garbage, a user without
	// read, an unbound ticket, a plain frame token.
	bobReq := httptest.NewRequest("GET", "/", nil)
	bobReq.Host = "xbin.localhost:9260"
	bobReq.AddCookie(&http.Cookie{Name: auth.SessionCookieHostName, Value: w.a.NewSession("bob", "192.0.2.1")})
	bobBind, _ := w.a.TileBinding(bobReq, "bob")
	ub, _ := w.ticketURL("/c/apps/b/", w.session("ana"))
	for name, q := range map[string]string{
		"another tile's ticket": ticketParam + "=" + url.QueryEscape(ub.Query().Get(ticketParam)),
		"garbage":               ticketParam + "=x1.a.b.c.d.e",
		"user without read":     ticketParam + "=" + url.QueryEscape(w.a.MintTileTicket("apps/a", "bob", bobBind)),
		"unbound ticket":        ticketParam + "=" + url.QueryEscape(w.a.MintTileTicket("apps/a", "ana", "")),
		"frame token":           "frame=" + url.QueryEscape(w.a.MintFrameToken("apps/a", "ana", 60e9)),
	} {
		rec := w.do("/c/apps/a/?"+q, append([]reqOpt{oa}, hopNav...)...)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("Location") != "" || len(rec.Result().Cookies()) != 0 {
			t.Errorf("%s: %d loc=%q cookies=%d", name, rec.Code, rec.Header().Get("Location"), len(rec.Result().Cookies()))
		}
	}
	// A cross-site initiator can't plant a session (login CSRF from outside).
	u, _ = w.ticketURL("/c/apps/a/", w.session("ana"))
	if c, _ := w.exchangeURL(u, hdr("Sec-Fetch-Site", "cross-site")); c != nil {
		t.Error("cross-site navigation exchanged a ticket")
	}
}

// A: the cookie lives exactly as long as the browser session that got it —
// sign-out kills it on the next request; a view-as session's tile is
// read-only on its origin too.
func TestOriginsCookieFollowsSession(t *testing.T) {
	w := newAssetWS(t, TileAssetsOrigins)
	w.s.RegisterAPI("POST /write-test", func(rw http.ResponseWriter, r *http.Request) { WriteOK(rw) })
	sid := w.a.NewSession("ana", "192.0.2.1")
	sess := cookie(auth.SessionCookieHostName, sid)
	u, _ := w.ticketURL("/c/apps/a/", sess)
	c, _ := w.exchangeURL(u)
	oa := host(w.originHost("apps/a"))
	ck := cookie(c.Name, c.Value)
	if rec := w.do("/c/apps/a/app.js", oa, sameOrig, ck); rec.Code != 200 {
		t.Fatalf("before sign-out: %d", rec.Code)
	}
	if rec := w.do("/api/xbin/write-test", oa, sameOrig, ck, method("POST")); rec.Code != 200 {
		t.Fatalf("write before sign-out: %d", rec.Code)
	}
	w.do("/logout", sess, sameOrig, method("POST"))
	if rec := w.do("/c/apps/a/app.js", oa, sameOrig, ck); rec.Code != http.StatusUnauthorized {
		t.Fatalf("tile cookie survived sign-out: %d", rec.Code)
	}
	// View-as: an admin's (here the owner's) read-only view of ana.
	owner := auth.Principal{Owner: true}
	tk, err := w.a.NewImpersonationTicket(owner, "ana")
	if err != nil {
		t.Fatal(err)
	}
	imp, err := w.a.RedeemImpersonation(tk, owner, "", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	u, _ = w.ticketURL("/c/apps/a/", cookie(auth.SessionCookieHostName, imp))
	c, _ = w.exchangeURL(u)
	ck = cookie(c.Name, c.Value)
	if rec := w.do("/api/xbin/write-test", oa, sameOrig, ck, method("POST")); rec.Code != http.StatusForbidden {
		t.Fatalf("view-as write on a tile origin: %d", rec.Code)
	}
	if rec := w.do("/c/apps/a/app.js", oa, sameOrig, ck); rec.Code != 200 {
		t.Fatalf("view-as read: %d", rec.Code)
	}
}

// A: authorization on the tile origin. Uncredentialed → 401; another
// tile's cookie, another user's cookie → refused; cross-tile loads exactly
// when the user can read the other tile, and never as a document; RBAC is
// re-read on every request; chrome is not served.
func TestOriginsAuthorization(t *testing.T) {
	w := newAssetWS(t, TileAssetsOrigins)
	ca, _ := w.exchange("apps/a", "ana", "/c/apps/a/")
	cb, _ := w.exchange("apps/b", "ana", "/c/apps/b/")
	bob, _ := w.exchange("apps/b", "bob", "/c/apps/b/")
	oa := host(w.originHost("apps/a"))

	for _, p := range []string{"/c/apps/a/app.js", "/c/apps/a/", "/api/xbin/frame-token?component=apps/a", "/ws/events"} {
		if rec := w.do(p, oa); rec.Code != http.StatusUnauthorized {
			t.Errorf("uncredentialed %s: %d", p, rec.Code)
		}
	}
	if rec := w.do("/c/apps/a/app.js", oa, sameOrig, cookie(ca.Name, ca.Value)); rec.Code != 200 {
		t.Fatalf("own asset: %d", rec.Code)
	}
	if rec := w.do("/c/apps/a/app.js", oa, sameOrig, cookie(cb.Name, cb.Value)); rec.Code != http.StatusUnauthorized {
		t.Errorf("apps/b's cookie on apps/a's origin: %d", rec.Code)
	}
	if rec := w.do("/c/apps/a/app.js", host(w.originHost("apps/b")), sameOrig, cookie(bob.Name, bob.Value)); rec.Code != http.StatusForbidden {
		t.Errorf("bob (no read on apps/a) via his apps/b origin: %d", rec.Code)
	}
	// A duplicate (tossed) cookie makes the request carry none.
	if rec := w.do("/c/apps/a/app.js", oa, sameOrig, cookie(ca.Name, ca.Value), cookie(ca.Name, "x")); rec.Code != http.StatusUnauthorized {
		t.Errorf("duplicate tile cookies: %d", rec.Code)
	}
	// Cross-tile: ana can read apps/b → served (sandboxed); can't read
	// apps/secret → 403; apps/b's DOCUMENT never runs on apps/a's origin.
	rec := w.do("/c/apps/b/lib.js", oa, sameOrig, cookie(ca.Name, ca.Value))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Security-Policy"), "sandbox; frame-ancestors") {
		t.Errorf("cross-tile asset: %d %q", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
	if rec := w.do("/c/apps/secret/s.js", oa, sameOrig, cookie(ca.Name, ca.Value)); rec.Code != http.StatusForbidden {
		t.Errorf("unreadable cross-tile asset: %d", rec.Code)
	}
	if rec := w.do("/c/apps/b/", oa, sameOrig, cookie(ca.Name, ca.Value)); rec.Code != http.StatusForbidden {
		t.Errorf("another tile's document on this origin: %d", rec.Code)
	}
	if rec := w.do("/c/shell/shell.js", oa, sameOrig, cookie(ca.Name, ca.Value)); rec.Code != http.StatusNotFound {
		t.Errorf("chrome on a tile origin: %d", rec.Code)
	}
	if rec := w.do("/c/apps/a/leak.txt", oa, sameOrig, cookie(ca.Name, ca.Value)); rec.Code != http.StatusNotFound {
		t.Errorf("symlink escape on a tile origin: %d", rec.Code)
	}
	// RBAC change → the next request.
	u, _ := w.st.Get("ana")
	delete(u.Tiles, "apps/a")
	if _, err := w.st.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	if rec := w.do("/c/apps/a/app.js", oa, sameOrig, cookie(ca.Name, ca.Value)); rec.Code != http.StatusForbidden {
		t.Errorf("revoked user's cookie: %d", rec.Code)
	}
	// Disabled user.
	b, _ := w.st.Get("bob")
	b.Disabled = true
	if _, err := w.st.Upsert(*b, ""); err != nil {
		t.Fatal(err)
	}
	if rec := w.do("/c/apps/b/lib.js", host(w.originHost("apps/b")), sameOrig, cookie(bob.Name, bob.Value)); rec.Code != http.StatusUnauthorized {
		t.Errorf("disabled user's cookie: %d", rec.Code)
	}
}

// A: /api on the tile origin acts as the tile's frame principal (cookie
// alone); the tile cookie never reaches what serves /api and nothing there
// sets cookies; a sibling tile origin — same SITE, so the browser attaches
// the cookie — can't ride it: no fetch, no POST, no WebSocket.
func TestOriginsAPIAndCSRF(t *testing.T) {
	w := newAssetWS(t, TileAssetsOrigins)
	w.s.RegisterAPI("GET /whoami-test", func(rw http.ResponseWriter, r *http.Request) {
		p := auth.PrincipalOf(r)
		http.SetCookie(rw, &http.Cookie{Name: "xbin_session", Value: "tossed", Domain: "xbin.localhost"})
		names := []string{}
		for _, c := range r.Cookies() {
			names = append(names, c.Name)
		}
		WriteJSON(rw, 200, map[string]any{"component": p.Component, "user": p.UserID, "via": p.Via, "cookies": names})
	})
	w.s.RegisterAPI("POST /whoami-test", func(rw http.ResponseWriter, r *http.Request) { WriteOK(rw) })
	ca, _ := w.exchange("apps/a", "ana", "/c/apps/a/")
	oa := host(w.originHost("apps/a"))
	ck := cookie(ca.Name, ca.Value)

	rec := w.do("/api/xbin/whoami-test", oa, ck, cookie("tile_own", "1"), sameOrig)
	var who struct {
		Component, User, Via string
		Cookies              []string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &who)
	if rec.Code != 200 || who.Component != "apps/a" || who.User != "ana" || who.Via != "frame" {
		t.Fatalf("/api on the tile origin: %d %+v", rec.Code, who)
	}
	if strings.Join(who.Cookies, ",") != "tile_own" {
		t.Errorf("the handler saw cookies %v (the tile cookie must be stripped)", who.Cookies)
	}
	if sc := rec.Header().Values("Set-Cookie"); len(sc) != 0 {
		t.Errorf("a tile-origin /api response set cookies: %v", sc)
	}
	if rec := w.do("/api/xbin/frame-token?component=apps/a", oa, ck, sameOrig); rec.Code != 200 {
		t.Fatalf("token renewal on the tile origin: %d", rec.Code)
	}
	if rec := w.do("/api/xbin/frame-token?component=apps/b", oa, ck, sameOrig); rec.Code != 403 {
		t.Fatalf("the tile origin minted another tile's token: %d", rec.Code)
	}
	sibling := "http://" + w.originHost("apps/b")
	for _, c := range []struct {
		name string
		opts []reqOpt
	}{
		{"sibling fetch", []reqOpt{hdr("Sec-Fetch-Site", "same-site"), hdr("Sec-Fetch-Mode", "cors")}},
		{"sibling POST", []reqOpt{method("POST"), hdr("Origin", sibling), hdr("Sec-Fetch-Site", "same-site")}},
		{"sibling POST, no metadata", []reqOpt{method("POST"), hdr("Origin", sibling)}},
		{"sibling websocket", []reqOpt{hdr("Upgrade", "websocket"), hdr("Origin", sibling)}},
		{"cross-site", []reqOpt{hdr("Sec-Fetch-Site", "cross-site")}},
	} {
		opts := append([]reqOpt{oa, ck}, c.opts...)
		if rec := w.do("/api/xbin/whoami-test", opts...); rec.Code != http.StatusForbidden {
			t.Errorf("%s rode the tile cookie: %d", c.name, rec.Code)
		}
	}
	// …but the shell framing the tile (a same-site navigation) is fine.
	if rec := w.do("/c/apps/a/", oa, ck, hdr("Sec-Fetch-Site", "same-site"), hdr("Sec-Fetch-Mode", "navigate")); rec.Code != 200 {
		t.Errorf("same-site document navigation: %d", rec.Code)
	}
	// An explicit frame token must be this tile's.
	if rec := w.do("/api/xbin/whoami-test", oa, w.frame("apps/b", "ana")); rec.Code != http.StatusForbidden {
		t.Errorf("apps/b's frame token on apps/a's origin: %d", rec.Code)
	}
	if rec := w.do("/api/xbin/whoami-test", oa, ck, w.frame("apps/a", "bob")); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Errorf("cookie and frame token of different users: %d", rec.Code)
	}
	// A cookie-backed navigation carrying ?frame= drops it from the URL.
	rec = w.do("/c/apps/a/?q=1&frame="+url.QueryEscape(w.a.MintFrameToken("apps/a", "ana", 60e9)), oa, ck, sameOrig, hdr("Sec-Fetch-Mode", "navigate"))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/c/apps/a/?q=1" {
		t.Errorf("?frame= on a cookie-backed navigation: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

var metaRefresh = regexp.MustCompile(`http-equiv="refresh" content="0;url=([^"]+)"`)

// A on the workspace origin: who initiated a navigation to a tile's
// document decides how it continues. The workspace itself or the user: a
// 302 with a ticket. Another site (a link in chat — PoC: the chain used to
// be cross-site, so the tile rendered with every subresource 401) or a top-
// level open from another tile: a same-origin interstitial that can't be
// framed, whose hop the exchange accepts. Refused: another tile's
// principal, a bare frame token (no session to bind); served: a header-
// credentialed client (the native app's scheme handler).
func TestOriginsWorkspaceNavigation(t *testing.T) {
	w := newAssetWS(t, TileAssetsOrigins)
	old := url.QueryEscape(w.a.MintFrameToken("apps/a", "ana", 60e9)) // a pre-origins bx-frame URL
	rec := w.do("/c/apps/a/sub/page.html?q=1&frame="+old, append(shellNav, w.session("ana"))...)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusFound || !strings.HasPrefix(loc, "http://"+w.originHost("apps/a")+"/c/apps/a/sub/page.html?q=1&"+ticketParam+"=") || strings.Contains(loc, "frame=") {
		t.Fatalf("redirect: %d %q", rec.Code, loc)
	}
	for _, site := range []string{"none", ""} { // typed/bookmark; no Fetch Metadata
		opts := []reqOpt{hdr("Accept", "text/html"), w.session("ana")}
		if site != "" {
			opts = append(opts, hdr("Sec-Fetch-Site", site), hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Dest", "document"))
		}
		if rec := w.do("/c/apps/a/", opts...); rec.Code != http.StatusFound {
			t.Errorf("initiator %q: %d", site, rec.Code)
		}
	}

	// Cross-site and tile-initiated top-level opens: the interstitial.
	for _, site := range []string{"cross-site", "same-site"} {
		rec := w.do("/c/apps/a/", w.session("ana"), hdr("Sec-Fetch-Site", site), hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Dest", "document"))
		m := metaRefresh.FindStringSubmatch(rec.Body.String())
		if rec.Code != 200 || m == nil || rec.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatalf("%s open: %d %v %s", site, rec.Code, rec.Header(), rec.Body.String())
		}
		u, _ := url.Parse(strings.ReplaceAll(m[1], "&amp;", "&"))
		c, rec := w.exchangeURL(u, hdr("Sec-Fetch-Dest", "document")) // the refresh's initiator is the workspace: same-site
		if c == nil {
			t.Fatalf("%s: the interstitial's hop didn't exchange: %d", site, rec.Code)
		}
		sub := w.do("/c/apps/a/app.js", host(u.Host), cookie(c.Name, c.Value), sameOrig, hdr("Sec-Fetch-Dest", "script"))
		if sub.Code != 200 {
			t.Fatalf("%s-opened tile: subresource %d", site, sub.Code)
		}
	}

	// Refused: another tile's frame principal (the credential would be
	// apps/a's), a bare frame token (no session to bind to).
	if rec := w.do("/c/apps/a/?frame="+url.QueryEscape(w.a.MintFrameToken("apps/b", "ana", 60e9)), append(shellNav, w.session("ana"))...); rec.Code != http.StatusForbidden {
		t.Fatalf("another tile's principal: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := w.do("/c/apps/a/?frame="+old, shellNav...); rec.Code != http.StatusForbidden {
		t.Fatalf("bare frame token navigation: %d", rec.Code)
	}
	// A client with a header credential (the native app's scheme handler)
	// gets the document itself, even asking for text/html.
	if rec := w.do("/c/apps/a/", hdr("Accept", "text/html"), w.frame("apps/a", "ana")); rec.Code != 200 {
		t.Fatalf("header-credentialed document fetch: %d", rec.Code)
	}
	chrome := w.do("/c/shell/", append(shellNav, w.session("ana"))...)
	if chrome.Code != 200 || chrome.Header().Get("Content-Security-Policy") != "frame-ancestors 'self'" {
		t.Fatalf("chrome: %d %q", chrome.Code, chrome.Header().Get("Content-Security-Policy"))
	}
	if rec := w.do("/c/apps/a/app.js", w.session("ana")); rec.Code != 200 {
		t.Fatalf("non-navigation stays: %d", rec.Code)
	}
	r := httptest.NewRequest("GET", "/api/xbin/components", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	cw := httptest.NewRecorder()
	w.s.apiComponents(cw, r)
	var list []struct{ Path, Origin string }
	_ = json.Unmarshal(cw.Body.Bytes(), &list)
	got := map[string]string{}
	for _, c := range list {
		got[c.Path] = c.Origin
	}
	if got["apps/a"] != "http://"+w.originHost("apps/a") || got["shell"] != "" {
		t.Fatalf("/components origins: %v", got)
	}
	if got["apps/a"] == got["apps/b"] {
		t.Fatal("two tiles share an origin")
	}
}

// A: one tile can't frame another tile authenticated (PoC: tile A's origin
// framed the workspace URL of apps/b; the same-site navigation carried the
// session cookie, the workspace minted apps/b's credential and apps/b
// rendered inside A — clickjacking, and A answering its dialogs as its
// parent). Now the cookie is dropped from same-site frames, a browser
// without Fetch Metadata showing a tile Referer gets the unframeable
// interstitial, and the tile's documents allow only the workspace and
// themselves as ancestors.
func TestOriginsTileCannotFrameAnotherTile(t *testing.T) {
	w := newAssetWS(t, TileAssetsOrigins)
	rec := w.do("/c/apps/b/", append(hopNav, w.session("ana"), hdr("Accept", "text/html"))...)
	if loc := rec.Header().Get("Location"); strings.Contains(loc, ticketParam) || rec.Code == 200 {
		t.Fatalf("a same-site frame got apps/b: %d %q", rec.Code, loc)
	}
	// No Fetch Metadata, Referer on apps/a's origin: the interstitial, never
	// framed.
	rec = w.do("/c/apps/b/", w.session("ana"), hdr("Accept", "text/html"), hdr("Referer", "http://"+w.originHost("apps/a")+"/"))
	if rec.Code != 200 || rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("old browser, tile Referer: %d %v", rec.Code, rec.Header())
	}
	// And apps/b's own documents refuse any other ancestor.
	cb, _ := w.exchange("apps/b", "ana", "/c/apps/b/")
	rec = w.do("/c/apps/b/", host(w.originHost("apps/b")), cookie(cb.Name, cb.Value), hopNav[0], hopNav[1], hopNav[2])
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'self' http://xbin.localhost:9260") {
		t.Fatalf("tile document without frame-ancestors: %q", rec.Header().Get("Content-Security-Policy"))
	}
}

// A on the workspace origin, cookie hygiene: tile origins are same-site, so
// their requests would carry the Lax session cookie legacy's opaque tiles
// never got. Dropped: same-site requests other than top-level navigations,
// an Origin on the tiles domain (POST without Fetch Metadata — the browsers
// TileContext fails open for), a Referer on the tiles domain unless a
// navigation.
func TestOriginsWorkspaceCookieHygiene(t *testing.T) {
	w := newAssetWS(t, TileAssetsOrigins)
	w.s.RegisterAPI("POST /hyg-test", func(rw http.ResponseWriter, r *http.Request) { WriteOK(rw) })
	w.s.RegisterAPI("GET /hyg-test", func(rw http.ResponseWriter, r *http.Request) { WriteOK(rw) })
	tileOrigin := "http://" + w.originHost("apps/a")
	for name, c := range map[string]struct {
		m    string
		opts []reqOpt
		want int
	}{
		"POST, Origin a tile, no metadata": {"POST", []reqOpt{hdr("Origin", tileOrigin)}, 401},
		"GET, Referer a tile, no metadata": {"GET", []reqOpt{hdr("Referer", tileOrigin+"/c/apps/a/")}, 401},
		"same-site fetch":                  {"GET", []reqOpt{hdr("Sec-Fetch-Site", "same-site"), hdr("Sec-Fetch-Mode", "cors")}, 401},
		"POST from the shell":              {"POST", []reqOpt{hdr("Origin", "http://xbin.localhost:9260"), sameOrig}, 200},
		"POST, no metadata, workspace":     {"POST", []reqOpt{hdr("Origin", "http://xbin.localhost:9260")}, 200},
		"GET, Referer the workspace":       {"GET", []reqOpt{hdr("Referer", "http://xbin.localhost:9260/")}, 200},
	} {
		rec := w.do("/api/xbin/hyg-test", append(c.opts, w.session("ana"), method(c.m))...)
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d", name, rec.Code, c.want)
		}
	}
	// A same-site frame of the shell, a form POST to /logout: no session.
	if rec := w.do("/c/shell/", append(hopNav, w.session("ana"), hdr("Accept", "text/html"))...); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Errorf("shell framed from a tile origin: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	sid := w.a.NewSession("ana", "192.0.2.1")
	w.do("/logout", method("POST"), hdr("Origin", tileOrigin), hdr("Sec-Fetch-Site", "same-site"), hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Dest", "document"), cookie(auth.SessionCookieHostName, sid))
	if rec := w.do("/api/xbin/hyg-test", cookie(auth.SessionCookieHostName, sid), sameOrig); rec.Code != 200 {
		t.Error("a tile origin's form POST to /logout signed the user out")
	}
	// A top-level same-site navigation keeps it (Lax lets that through
	// cross-site too).
	if rec := w.do("/c/shell/", w.session("ana"), hdr("Sec-Fetch-Site", "same-site"), hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Dest", "document")); rec.Code != 200 {
		t.Errorf("top-level same-site navigation: %d", rec.Code)
	}
}

// Hosts under the tiles domain that aren't tile labels, and the tile hosts'
// allow-list of paths: nothing but /c/, /api/, /ws/events, /docs/, /vendor/.
func TestOriginsHostRouting(t *testing.T) {
	w := newAssetWS(t, TileAssetsOrigins)
	for _, h := range []string{"evil.xbin.localhost", "t-short.xbin.localhost", "a.t-" + strings.Repeat("a", 16) + ".xbin.localhost"} {
		if rec := w.do("/c/apps/a/app.js", host(h)); rec.Code != http.StatusNotFound {
			t.Errorf("host %s: %d", h, rec.Code)
		}
	}
	ca, _ := w.exchange("apps/a", "ana", "/c/apps/a/")
	oa := host(w.originHost("apps/a"))
	for _, p := range []string{"/login", "/", "/ws/term", "/logout"} {
		if rec := w.do(p, oa, cookie(ca.Name, ca.Value)); rec.Code != http.StatusNotFound {
			t.Errorf("%s on a tile origin: %d", p, rec.Code)
		}
	}
	if rec := w.do("/healthz", oa); rec.Code != 200 {
		t.Errorf("/healthz: %d", rec.Code)
	}
	// Navigations to what isn't the tile's own business go to the workspace
	// origin: its pages (links built from location.origin — invites, view-as),
	// another tile's pages (which then land on THAT tile's origin).
	nav := []reqOpt{oa, hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Site", "same-origin")}
	for p, want := range map[string]string{
		"/login?invite=abc":                    "http://xbin.localhost:9260/login?invite=abc",
		"/docs/elements.md":                    "http://xbin.localhost:9260/docs/elements.md",
		"/c/apps/b/?q=1&frame=x&xbin_ticket=y": "http://xbin.localhost:9260/c/apps/b/?q=1",
		"/c/shell/":                            "http://xbin.localhost:9260/c/shell/",
		"/":                                    "http://xbin.localhost:9260/",
	} {
		rec := w.do(p, nav...)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
			t.Errorf("navigation to %s on a tile origin: %d %q, want → %s", p, rec.Code, rec.Header().Get("Location"), want)
		}
	}
	// The tile's own page without a credential, top-level: once through the
	// workspace for a fresh ticket (marker), then — if that didn't stick —
	// the page. Framed: the page at once (a reload from the shell fixes it).
	top := append(nav, hdr("Sec-Fetch-Dest", "document"))
	rec := w.do("/c/apps/a/sub/page.html?q=1", top...)
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "http://xbin.localhost:9260/c/apps/a/sub/page.html?q=1&xbin_retry=1" {
		t.Errorf("credential refresh: %d %q", rec.Code, loc)
	}
	if rec := w.do("/c/apps/a/sub/page.html?q=1&xbin_retry=1", top...); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Open it from the workspace") {
		t.Errorf("second miss must stop with the page: %d", rec.Code)
	}
	if rec := w.do("/c/apps/a/", append(nav, hdr("Sec-Fetch-Dest", "iframe"))...); rec.Code != http.StatusUnauthorized ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'self' http://xbin.localhost:9260") {
		t.Errorf("framed miss: %d %q", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
	if rec := w.do("/c/apps/a/", oa, hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Site", "cross-site")); rec.Code != http.StatusUnauthorized {
		t.Errorf("cross-site entry: %d, want the page", rec.Code)
	}
	// …and the workspace sends it back with a ticket, marker kept (no loop).
	rec = w.do("/c/apps/a/sub/page.html?q=1&xbin_retry=1", hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Site", "none"), w.session("ana"))
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "http://"+w.originHost("apps/a")+"/c/apps/a/sub/page.html?q=1&xbin_retry=1&"+ticketParam+"=") {
		t.Errorf("workspace leg: %d %q", rec.Code, loc)
	}

	// The workspace session cookie means nothing on a tile origin.
	if rec := w.do("/c/apps/a/app.js", oa, w.session("ana"), sameOrig); rec.Code != http.StatusUnauthorized {
		t.Errorf("workspace cookie on a tile origin: %d", rec.Code)
	}
	// And the tile cookie means nothing on the workspace origin.
	if rec := w.do("/c/apps/a/app.js", cookie(ca.Name, ca.Value)); rec.Code != http.StatusUnauthorized {
		t.Errorf("tile cookie on the workspace origin: %d", rec.Code)
	}
}
