package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// originHost: tile's origin host (with the harness port).
func (w *assetWS) originHost(tile string) string {
	return w.a.TileHostID(tile) + ".xbin.localhost:9260"
}

// exchange performs the navigation-time ?frame= exchange on tile's origin
// and returns the cookie it set.
func (w *assetWS) exchange(tile, uid, path string) (*http.Cookie, *httptest.ResponseRecorder) {
	w.t.Helper()
	tok := w.a.MintFrameToken(tile, uid, 60e9)
	rec := w.do(path+"?x=1&frame="+url.QueryEscape(tok)+"&y=2", host(w.originHost(tile)),
		hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Dest", "iframe"), hdr("Sec-Fetch-Site", "same-site"))
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.TileCookieName {
			return c, rec
		}
	}
	return nil, rec
}

// A: the exchange sets a host-only, HttpOnly, SameSite=Strict cookie and
// redirects to the same URL without ?frame= (other parameters intact); a
// failed exchange never redirects.
func TestOriginsExchange(t *testing.T) {
	w := newAssetWS(t, TileAssetsOrigins)
	c, rec := w.exchange("apps/a", "ana", "/c/apps/a/")
	if rec.Code != http.StatusFound || c == nil {
		t.Fatalf("exchange: %d %v", rec.Code, rec.Header())
	}
	if loc := rec.Header().Get("Location"); loc != "/c/apps/a/?x=1&y=2" {
		t.Fatalf("redirect %q must drop only ?frame=", loc)
	}
	if c.Domain != "" || c.Path != "/" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || !c.Secure {
		t.Fatalf("cookie scope: %+v", c)
	}
	// The cookie then loads the clean document — sandboxed WITH
	// allow-same-origin (its own origin), injected, never on the workspace
	// origin with that token.
	rec = w.do("/c/apps/a/", host(w.originHost("apps/a")), cookie(c.Name, c.Value),
		hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Site", "same-site"))
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "allow-same-origin") {
		t.Fatalf("clean URL with cookie: %d %q", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
	if !strings.Contains(rec.Body.String(), `content="origins"`) || !strings.Contains(rec.Body.String(), `allow-same-origin`) {
		t.Fatal("origins document lacks its meta / sandbox meta")
	}
	// Failed exchanges: another tile's token on this origin, a bad token, a
	// user who can't read the tile — 401, no redirect, no cookie.
	for _, tc := range []struct{ tok, name string }{
		{w.a.MintFrameToken("apps/b", "ana", 60e9), "another tile's token"},
		{"garbage", "garbage"},
		{w.a.MintFrameToken("apps/a", "bob", 60e9), "user without read"},
	} {
		rec := w.do("/c/apps/a/?frame="+url.QueryEscape(tc.tok), host(w.originHost("apps/a")),
			hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Site", "same-site"))
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("Location") != "" || len(rec.Result().Cookies()) != 0 {
			t.Errorf("%s: %d loc=%q", tc.name, rec.Code, rec.Header().Get("Location"))
		}
	}
	// A cross-site initiator can't plant a session (login CSRF from outside).
	tok := w.a.MintFrameToken("apps/a", "ana", 60e9)
	rec = w.do("/c/apps/a/?frame="+url.QueryEscape(tok), host(w.originHost("apps/a")),
		hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Site", "cross-site"))
	if len(rec.Result().Cookies()) != 0 {
		t.Error("cross-site navigation exchanged a token")
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
	same := hdr("Sec-Fetch-Site", "same-origin")

	for _, p := range []string{"/c/apps/a/app.js", "/c/apps/a/", "/api/xbin/frame-token?component=apps/a", "/ws/events"} {
		if rec := w.do(p, oa); rec.Code != http.StatusUnauthorized {
			t.Errorf("uncredentialed %s: %d", p, rec.Code)
		}
	}
	if rec := w.do("/c/apps/a/app.js", oa, same, cookie(ca.Name, ca.Value)); rec.Code != 200 {
		t.Fatalf("own asset: %d", rec.Code)
	}
	if rec := w.do("/c/apps/a/app.js", oa, same, cookie(cb.Name, cb.Value)); rec.Code != http.StatusUnauthorized {
		t.Errorf("apps/b's cookie on apps/a's origin: %d", rec.Code)
	}
	if rec := w.do("/c/apps/a/app.js", host(w.originHost("apps/b")), same, cookie(bob.Name, bob.Value)); rec.Code != http.StatusForbidden {
		t.Errorf("bob (no read on apps/a) via his apps/b origin: %d", rec.Code)
	}
	// Cross-tile: ana can read apps/b → served (sandboxed); can't read
	// apps/secret → 403; apps/b's DOCUMENT never runs on apps/a's origin.
	rec := w.do("/c/apps/b/lib.js", oa, same, cookie(ca.Name, ca.Value))
	if rec.Code != 200 || rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Errorf("cross-tile asset: %d %q", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
	if rec := w.do("/c/apps/secret/s.js", oa, same, cookie(ca.Name, ca.Value)); rec.Code != http.StatusForbidden {
		t.Errorf("unreadable cross-tile asset: %d", rec.Code)
	}
	if rec := w.do("/c/apps/b/", oa, same, cookie(ca.Name, ca.Value)); rec.Code != http.StatusForbidden {
		t.Errorf("another tile's document on this origin: %d", rec.Code)
	}
	if rec := w.do("/c/shell/shell.js", oa, same, cookie(ca.Name, ca.Value)); rec.Code != http.StatusNotFound {
		t.Errorf("chrome on a tile origin: %d", rec.Code)
	}
	if rec := w.do("/c/apps/a/leak.txt", oa, same, cookie(ca.Name, ca.Value)); rec.Code != http.StatusNotFound {
		t.Errorf("symlink escape on a tile origin: %d", rec.Code)
	}
	// RBAC change → the next request.
	u, _ := w.st.Get("ana")
	delete(u.Tiles, "apps/a")
	if _, err := w.st.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	if rec := w.do("/c/apps/a/app.js", oa, same, cookie(ca.Name, ca.Value)); rec.Code != http.StatusForbidden {
		t.Errorf("revoked user's cookie: %d", rec.Code)
	}
	// Disabled user.
	b, _ := w.st.Get("bob")
	b.Disabled = true
	if _, err := w.st.Upsert(*b, ""); err != nil {
		t.Fatal(err)
	}
	if rec := w.do("/c/apps/b/lib.js", host(w.originHost("apps/b")), same, cookie(bob.Name, bob.Value)); rec.Code != http.StatusUnauthorized {
		t.Errorf("disabled user's cookie: %d", rec.Code)
	}
}

// A: /api on the tile origin acts as the tile's frame principal (cookie
// alone), and a sibling tile origin — same SITE, so the browser attaches the
// cookie — can't ride it: no fetch, no POST, no WebSocket, only navigation to
// the tile's documents.
func TestOriginsAPIAndCSRF(t *testing.T) {
	w := newAssetWS(t, TileAssetsOrigins)
	w.s.RegisterAPI("GET /whoami-test", func(rw http.ResponseWriter, r *http.Request) {
		p := auth.PrincipalOf(r)
		WriteJSON(rw, 200, map[string]string{"component": p.Component, "user": p.UserID, "via": p.Via})
	})
	w.s.RegisterAPI("POST /whoami-test", func(rw http.ResponseWriter, r *http.Request) { WriteOK(rw) })
	ca, _ := w.exchange("apps/a", "ana", "/c/apps/a/")
	oa := host(w.originHost("apps/a"))
	ck := cookie(ca.Name, ca.Value)

	rec := w.do("/api/xbin/whoami-test", oa, ck, hdr("Sec-Fetch-Site", "same-origin"))
	var who map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &who)
	if rec.Code != 200 || who["component"] != "apps/a" || who["user"] != "ana" || who["via"] != "frame" {
		t.Fatalf("/api on the tile origin: %d %v", rec.Code, who)
	}
	if rec := w.do("/api/xbin/frame-token?component=apps/a", oa, ck, hdr("Sec-Fetch-Site", "same-origin")); rec.Code != 200 {
		t.Fatalf("token renewal on the tile origin: %d", rec.Code)
	}
	if rec := w.do("/api/xbin/frame-token?component=apps/b", oa, ck, hdr("Sec-Fetch-Site", "same-origin")); rec.Code != 403 {
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
}

// A on the workspace origin: a browser navigating to a sandboxed tile's
// document goes to the tile origin with a fresh token; another tile's frame
// principal is not sent (the token would be the other tile's); chrome stays;
// /components reports each tile's origin.
func TestOriginsWorkspaceRedirect(t *testing.T) {
	w := newAssetWS(t, TileAssetsOrigins)
	nav := []reqOpt{hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Site", "same-origin")}
	old := url.QueryEscape(w.a.MintFrameToken("apps/a", "ana", 60e9)) // a pre-origins bx-frame URL
	rec := w.do("/c/apps/a/sub/page.html?q=1&frame="+old, append(nav, w.session("ana"))...)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusFound || !strings.HasPrefix(loc, "http://"+w.originHost("apps/a")+"/c/apps/a/sub/page.html?q=1&frame=") {
		t.Fatalf("redirect: %d %q", rec.Code, loc)
	}
	u, _ := url.Parse(loc)
	if strings.Count(loc, "frame=") != 1 {
		t.Fatalf("the old token must be dropped: %q", loc)
	}
	if c, uid, ok := w.a.VerifyFrameToken(u.Query().Get("frame")); !ok || c != "apps/a" || uid != "ana" {
		t.Fatalf("redirect token: %s %s %v", c, uid, ok)
	}
	if rec := w.do("/c/apps/a/", append(nav, w.frame("apps/b", "ana"))...); rec.Code == http.StatusFound {
		t.Fatal("another tile's frame principal was sent to apps/a's origin with a fresh token")
	}
	// A client with a header credential (the native app's scheme handler)
	// gets the document itself, even asking for text/html.
	if rec := w.do("/c/apps/a/", hdr("Accept", "text/html"), w.frame("apps/a", "ana")); rec.Code != 200 {
		t.Fatalf("header-credentialed document fetch: %d", rec.Code)
	}
	if rec := w.do("/c/shell/", append(nav, w.session("ana"))...); rec.Code != 200 {
		t.Fatalf("chrome: %d", rec.Code)
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
		"/login?invite=abc":      "http://xbin.localhost:9260/login?invite=abc",
		"/docs/elements.md":      "http://xbin.localhost:9260/docs/elements.md",
		"/c/apps/b/?q=1&frame=x": "http://xbin.localhost:9260/c/apps/b/?q=1",
		"/c/shell/":              "http://xbin.localhost:9260/c/shell/",
		"/":                      "http://xbin.localhost:9260/",
	} {
		rec := w.do(p, nav...)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
			t.Errorf("navigation to %s on a tile origin: %d %q, want → %s", p, rec.Code, rec.Header().Get("Location"), want)
		}
	}
	// The tile's own page without a credential: once through the workspace
	// for a fresh one (marker), then — if that didn't stick — the page.
	rec := w.do("/c/apps/a/sub/page.html?q=1", nav...)
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "http://xbin.localhost:9260/c/apps/a/sub/page.html?q=1&xbin_retry=1" {
		t.Errorf("credential refresh: %d %q", rec.Code, loc)
	}
	if rec := w.do("/c/apps/a/sub/page.html?q=1&xbin_retry=1", nav...); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Open it from the workspace") {
		t.Errorf("second miss must stop with the page: %d", rec.Code)
	}
	if rec := w.do("/c/apps/a/", oa, hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Site", "cross-site")); rec.Code != http.StatusUnauthorized {
		t.Errorf("cross-site entry: %d, want the page", rec.Code)
	}
	// …and the workspace sends it back with a token, marker kept (no loop).
	rec = w.do("/c/apps/a/sub/page.html?q=1&xbin_retry=1", hdr("Sec-Fetch-Mode", "navigate"), w.session("ana"))
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "http://"+w.originHost("apps/a")+"/c/apps/a/sub/page.html?q=1&xbin_retry=1&frame=") {
		t.Errorf("workspace leg: %d %q", rec.Code, loc)
	}

	// The workspace session cookie means nothing on a tile origin.
	if rec := w.do("/c/apps/a/app.js", oa, w.session("ana"), hdr("Sec-Fetch-Site", "same-origin")); rec.Code != http.StatusUnauthorized {
		t.Errorf("workspace cookie on a tile origin: %d", rec.Code)
	}
	// And the tile cookie means nothing on the workspace origin.
	if rec := w.do("/c/apps/a/app.js", cookie(ca.Name, ca.Value)); rec.Code != http.StatusUnauthorized {
		t.Errorf("tile cookie on the workspace origin: %d", rec.Code)
	}
	_ = users.LevelRead
}
