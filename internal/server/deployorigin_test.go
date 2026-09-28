package server

// deployorigin_test.go — tile deployments under --tile-assets=origins
// (11-contract §2.6, §7.5): an origin per deployment.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// namesPolicy is urlPolicy naming every deployment of a tile, main first,
// as the deployments plane's DeploymentsOf does.
type namesPolicy struct{ *urlPolicy }

var _ DeploymentNamesPolicy = namesPolicy{}

func (p namesPolicy) DeploymentsOf(tile string) (string, []string) {
	r := p.recs[tile]
	if r == nil {
		return util.MainDeployment, []string{util.MainDeployment}
	}
	names := []string{util.MainDeployment}
	for n := range r.roots {
		if n != util.MainDeployment {
			names = append(names, n)
		}
	}
	slices.Sort(names[1:])
	return r.primary, names
}

// newOriginWS is depWS in origins mode with every deployment named, and
// GET /api/xbin/whoami echoing the request's principal (a neutral route,
// so a non-primary deployment's credentials reach it; D127r).
func newOriginWS(t *testing.T) *depWS {
	t.Helper()
	w := newDepWS(t, TileAssetsOrigins)
	w.s.Pol = namesPolicy{w.pol}
	w.s.RegisterAPI("GET /whoami", func(rw http.ResponseWriter, r *http.Request) {
		p := auth.PrincipalOf(r)
		WriteJSON(rw, http.StatusOK, map[string]string{"component": p.Component, "deployment": p.Deployment, "via": p.Via})
	})
	return w
}

// depHost is deployment dep of tile's origin host (with the harness port).
func (w *depWS) depHost(tile, dep string) string {
	return w.a.TileHostIDDeployment(tile, dep) + ".xbin.localhost:9260"
}

// who asks the whoami echo on host with opts: the status and the
// principal's (component, deployment).
func (w *depWS) who(h string, opts ...reqOpt) (int, string, string) {
	w.t.Helper()
	rec := w.do("/api/xbin/whoami", append([]reqOpt{host(h), sameOrig}, opts...)...)
	var p map[string]string
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			w.t.Fatal(err)
		}
	}
	return rec.Code, p["component"], p["deployment"]
}

// binding is uid's binding for a fresh browser session of theirs.
func (w *depWS) binding(uid string) string {
	w.t.Helper()
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "xbin.localhost:9260"
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieHostName, Value: w.a.NewSession(uid, "192.0.2.1")})
	b, ok := w.a.TileBinding(r, uid)
	if !ok {
		w.t.Fatalf("no binding for %s", uid)
	}
	return b
}

// ticketFor is the URL on h the workspace's second leg sends uid's browser
// to with a ticket for deployment dep of apps/a, path p, this browser's
// exchange state for h kept in w.states.
func (w *depWS) ticketFor(h, dep, uid, p string) *url.URL {
	if w.states == nil {
		w.states = map[string]string{}
	}
	if w.states[h] == "" {
		w.states[h] = auth.NewTileState()
	}
	tk := w.a.MintTileTicketDeployment("apps/a", dep, uid, w.binding(uid), w.states[h])
	return &url.URL{Host: h, Path: p, RawQuery: ticketParam + "=" + url.QueryEscape(tk)}
}

var docNav = []reqOpt{hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Dest", "document"), hdr("Sec-Fetch-Site", "none")}

// covers D127j T3 — each deployment of a tile has an origin of its own in
// origins mode, and a credential of one deployment's origin never acts on
// another's. main keeps today's label, x1 ticket and c1 cookie; dev's origin
// is labelled by name and trades an x2 for a c2, only for a user who writes
// the tile, checked again on every request. On dev's origin /api acts as
// dev's tile principal; main's cookie and frame token are refused there, and
// dev's on main's origin; a path naming another deployment of the tile is
// sent to the workspace (a document fetched there is refused), and a browser
// sent back from dev's origin for a fresh exchange keeps dev in the path. A
// referer on any deployment's origin counts as its tile's for a navigation
// within the tile's tree. When dev becomes primary, the bare URL and
// /components' origin move to dev's origin, which mints the bare document's
// frame token for dev, readers included, and main's origin needs write.
// Served documents and files on a deployment's origin (the "served"
// subtest), and tokens mode's asset base per deployment ("tokens").
func TestOriginLabelPerDeployment(t *testing.T) {
	w := newOriginWS(t)
	mainHost, devHost := w.originHost("apps/a"), w.depHost("apps/a", "dev")
	devID, _ := w.s.tileHostOf(devHost)

	// Labels and origins.
	if devID == "" || devHost == mainHost || w.depHost("apps/a", util.MainDeployment) != mainHost {
		t.Fatalf("labels: main %q, dev %q", mainHost, devHost)
	}
	if w.s.DeploymentOrigin("apps/a", "dev") != "http://"+devHost || w.s.tileOriginURL("apps/a") != "http://"+mainHost ||
		w.s.DeploymentOrigin("apps/a", util.MainDeployment) != "http://"+mainHost {
		t.Fatalf("origins: %q %q", w.s.DeploymentOrigin("apps/a", "dev"), w.s.tileOriginURL("apps/a"))
	}
	if tile, dep, ok := w.s.originOf(devID); !ok || tile != "apps/a" || dep != "dev" {
		t.Fatalf("originOf(dev's label) = %q %q %v", tile, dep, ok)
	}
	if (&Server{Auth: w.a}).DeploymentOrigin("apps/a", "dev") != "" {
		t.Fatal("a deployment origin outside origins mode")
	}

	// main: today's flow, x1 and c1, its principal bound to main.
	c1, rec := w.exchange("apps/a", "wes", "/c/apps/a/")
	if c1 == nil || !strings.HasPrefix(c1.Value, "c1.") {
		t.Fatalf("main's exchange: %d %v", rec.Code, c1)
	}
	ck1 := cookie(c1.Name, c1.Value)
	if code, comp, dep := w.who(mainHost, ck1); code != 200 || comp != "apps/a" || dep != "" {
		t.Fatalf("main's origin acts as %q %q (%d)", comp, dep, code)
	}

	// dev: an x2 redeems only on dev's origin, only for a writer.
	if c, rec := w.exchangeURL(w.ticketFor(mainHost, "dev", "wes", "/c/apps/a/")); c != nil || rec.Code != http.StatusUnauthorized {
		t.Fatalf("dev's ticket on main's origin: %d %v", rec.Code, c)
	}
	if c, rec := w.exchangeURL(w.ticketFor(devHost, util.MainDeployment, "wes", "/c/apps/a+dev/")); c != nil || rec.Code != http.StatusUnauthorized {
		t.Fatalf("main's ticket on dev's origin: %d %v", rec.Code, c)
	}
	if c, rec := w.exchangeURL(w.ticketFor(devHost, "dev", "ana", "/c/apps/a+dev/")); c != nil || rec.Code != http.StatusUnauthorized {
		t.Fatalf("a reader's dev ticket: %d %v", rec.Code, c)
	}
	c2, rec := w.exchangeURL(w.ticketFor(devHost, "dev", "wes", "/c/apps/a+dev/"))
	if c2 == nil || !strings.HasPrefix(c2.Value, "c2.") || rec.Header().Get("Location") != "/c/apps/a+dev/" {
		t.Fatalf("dev's exchange: %d %q %v", rec.Code, rec.Header().Get("Location"), c2)
	}
	if g, ok := w.a.VerifyTileCookie(c2.Value); !ok || g.Deployment != "dev" {
		t.Fatalf("dev's cookie: %+v", g)
	}
	ck2 := cookie(c2.Name, c2.Value)
	if code, comp, dep := w.who(devHost, ck2); code != 200 || comp != "apps/a" || dep != "dev" {
		t.Fatalf("dev's origin acts as %q %q (%d)", comp, dep, code)
	}

	// One deployment's credentials never act on the other's origin.
	devFrame := w.devFrame("wes")
	mainFrame := w.frame("apps/a", "wes")
	for name, c := range map[string]struct {
		h    string
		opt  reqOpt
		want int
	}{
		"main's cookie on dev's origin":      {devHost, ck1, http.StatusUnauthorized},
		"dev's cookie on main's origin":      {mainHost, ck2, http.StatusUnauthorized},
		"main's frame token on dev's origin": {devHost, mainFrame, http.StatusForbidden},
		"dev's frame token on main's origin": {mainHost, devFrame, http.StatusForbidden},
	} {
		if code, _, _ := w.who(c.h, c.opt); code != c.want {
			t.Errorf("%s: %d, want %d", name, code, c.want)
		}
	}
	if code, _, dep := w.who(devHost, devFrame); code != 200 || dep != "dev" {
		t.Errorf("dev's frame token on dev's origin: %d %q", code, dep)
	}

	// A reader's dev cookie, and a writer demoted to read: refused on every
	// request.
	anaDev := w.a.MintTileCookieDeployment("apps/a", "dev", "ana", w.binding("ana"), time.Hour)
	if code, _, _ := w.who(devHost, cookie(c2.Name, anaDev)); code != http.StatusForbidden {
		t.Errorf("a reader's dev cookie: %d", code)
	}
	if _, err := w.st.Upsert(users.User{ID: "wes", Role: users.RoleUser, Tiles: map[string]string{"apps/a": users.LevelRead}}, ""); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := w.who(devHost, ck2); code != http.StatusForbidden {
		t.Errorf("dev's cookie of a demoted writer: %d", code)
	}
	if code, _, _ := w.who(mainHost, ck1); code != 200 {
		t.Errorf("main's cookie of a demoted writer (main is primary): %d", code)
	}
	if _, err := w.st.Upsert(users.User{ID: "wes", Role: users.RoleUser, Tiles: map[string]string{"apps/a": users.LevelWrite, "apps/b": users.LevelRead}}, ""); err != nil {
		t.Fatal(err)
	}

	// Another deployment's (or tile's) page on dev's origin goes to the
	// workspace; fetched there, another deployment's document is refused.
	for _, p := range []string{"/c/apps/a+main/", "/c/apps/a+main/sub/page.html", "/c/apps/b/"} {
		rec := w.do(p, host(devHost), ck2, hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Site", "same-origin"))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "http://xbin.localhost:9260"+p {
			t.Errorf("%s navigated on dev's origin: %d %q", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := w.do("/c/apps/a+main/sub/page.html", host(devHost), ck2, sameOrig); rec.Code != http.StatusForbidden {
		t.Errorf("main's document fetched on dev's origin: %d", rec.Code)
	}

	// Sent back to the workspace from dev's origin (no cookie; a begin), a
	// bare path names dev, so the workspace returns it here.
	rec = w.do("/c/apps/a/sub/page.html?q=1", append([]reqOpt{host(devHost)}, docNav...)...)
	if want := "http://xbin.localhost:9260/c/apps/a+dev/sub/page.html?q=1&" + retryMarker + "=1"; rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Errorf("dev's origin without a cookie: %d %q, want %q", rec.Code, rec.Header().Get("Location"), want)
	}
	rec = w.do("/c/apps/a/?"+beginParam+"=x", append([]reqOpt{host(devHost)}, hopNav...)...)
	if u := w.location(rec); rec.Code != http.StatusFound || u.Path != "/c/apps/a+dev/" || u.Query().Get(stateParam) == "" {
		t.Errorf("a begin on dev's origin: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = w.do("/c/apps/a/", append([]reqOpt{host(mainHost)}, docNav...)...)
	if want := "http://xbin.localhost:9260/c/apps/a/?" + retryMarker + "=1"; rec.Header().Get("Location") != want {
		t.Errorf("main's origin without a cookie: %q, want %q", rec.Header().Get("Location"), want)
	}

	// A navigation from dev's origin into the tile's own tree (a nested
	// component's page) is the tile's, as from main's.
	writeWS(t, w.root, map[string]string{"apps/a/nested/index.html": assetPage})
	w.rescan()
	for _, from := range []string{devHost, mainHost} {
		rec := w.do("/c/apps/a/nested/", append(append([]reqOpt{w.session("ada")}, hopNav...), hdr("Referer", "http://"+from+"/c/apps/a+dev/"))...)
		if u := w.location(rec); rec.Code != http.StatusFound || u.Host != w.originHost("apps/a/nested") {
			t.Errorf("into the tree from %s: %d %q", from, rec.Code, rec.Header().Get("Location"))
		}
	}

	// dev becomes the primary.
	w.pol.recs["apps/a"].primary = "dev"
	var one struct {
		Component componentInfo `json:"component"`
	}
	if err := json.Unmarshal(w.do("/api/xbin/components/apps/a", w.zsOwner()).Body.Bytes(), &one); err != nil || one.Component.Origin != "http://"+devHost {
		t.Fatalf("/components' origin with dev the primary: %q %v", one.Component.Origin, err)
	}
	for _, uid := range []string{"ana", "wes"} {
		u, rec := w.ticketURL("/c/apps/a/", w.session(uid))
		if u == nil || u.Host != devHost || !strings.HasPrefix(u.Query().Get(ticketParam), "x2.") {
			t.Fatalf("%s's bare URL with dev the primary: %d %v", uid, rec.Code, u)
		}
		c, rec := w.exchangeURL(u)
		if c == nil || !strings.HasPrefix(c.Value, "c2.") {
			t.Fatalf("%s's exchange on the primary's origin: %d", uid, rec.Code)
		}
		doc := w.do("/c/apps/a/", host(devHost), cookie(c.Name, c.Value), hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Site", "same-site"))
		fp, ok := w.a.FramePrincipal(frameTokenIn(t, doc.Body.String()))
		if doc.Code != 200 || !strings.Contains(doc.Body.String(), "dev page") || !ok || fp.Deployment != "dev" ||
			!strings.Contains(doc.Header().Get("Content-Security-Policy"), "allow-same-origin") {
			t.Fatalf("%s's bare document on the primary's origin: %d %+v %s", uid, doc.Code, fp, doc.Body.String())
		}
	}
	anaMain := w.a.MintTileCookie("apps/a", "ana", w.binding("ana"), time.Hour)
	if code, _, _ := w.who(mainHost, cookie(c1.Name, anaMain)); code != http.StatusForbidden {
		t.Errorf("a reader on main's origin, main no longer primary: %d", code)
	}
	if code, _, dep := w.who(mainHost, ck1); code != 200 || dep != "" {
		t.Errorf("a writer on main's origin, main no longer primary: %d %q", code, dep)
	}
	w.pol.recs["apps/a"].primary = util.MainDeployment

	t.Run("served", func(t *testing.T) {
		w := newOriginWS(t)
		wes := w.session("wes")
		devHost := w.depHost("apps/a", "dev")
		u, rec := w.ticketURL("/c/apps/a+dev/?x=1", wes)
		if u == nil || u.Host != devHost || !strings.HasPrefix(u.Query().Get(ticketParam), "x2.") {
			t.Fatalf("a deployment URL from the workspace: %d %v", rec.Code, u)
		}
		c, rec := w.exchangeURL(u)
		if c == nil || rec.Header().Get("Location") != "/c/apps/a+dev/?x=1" {
			t.Fatalf("dev's exchange: %d %q", rec.Code, rec.Header().Get("Location"))
		}
		ck := cookie(c.Name, c.Value)
		nav := []reqOpt{host(devHost), ck, hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Site", "same-site")}
		for _, p := range []string{"/c/apps/a+dev/", "/c/apps/a/"} {
			doc := w.do(p, nav...)
			fp, ok := w.a.FramePrincipal(frameTokenIn(t, doc.Body.String()))
			if doc.Code != 200 || !strings.Contains(doc.Body.String(), "dev page") || !ok || fp.Deployment != "dev" ||
				!strings.Contains(doc.Header().Get("Content-Security-Policy"), "allow-same-origin") {
				t.Errorf("%s on dev's origin: %d %+v %s", p, doc.Code, fp, doc.Body.String())
			}
		}
		for _, p := range []string{"/c/apps/a+dev/app.js", "/c/apps/a/app.js"} {
			if rec := w.do(p, host(devHost), ck, sameOrig); rec.Code != 200 || rec.Body.String() != devFiles["app.js"] {
				t.Errorf("%s on dev's origin: %d %q", p, rec.Code, rec.Body.String())
			}
		}
		if rec := w.do("/c/apps/a+dev/", append(shellNav, w.session("ana"))...); rec.Code != http.StatusForbidden {
			t.Errorf("a reader opening dev: %d", rec.Code)
		}
		// main's origin never serves dev's files, even to a writer's cookie.
		c1, _ := w.exchange("apps/a", "wes", "/c/apps/a/")
		ck1 := cookie(c1.Name, c1.Value)
		mainHost := w.originHost("apps/a")
		if rec := w.do("/c/apps/a/app.js", host(mainHost), ck1, sameOrig); rec.Code != 200 || rec.Body.String() != `import './dep.js';` {
			t.Errorf("main's file on main's origin: %d %q", rec.Code, rec.Body.String())
		}
		if rec := w.do("/c/apps/a+dev/app.js", host(mainHost), ck1, sameOrig); rec.Code != http.StatusForbidden {
			t.Errorf("dev's file on main's origin: %d %q", rec.Code, rec.Body.String())
		}
		// With dev the primary, main's origin serves main, bare or not.
		w.pol.recs["apps/a"].primary = "dev"
		for _, p := range []string{"/c/apps/a/app.js", "/c/apps/a+main/app.js"} {
			if rec := w.do(p, host(mainHost), ck1, sameOrig); rec.Code != 200 || rec.Body.String() != `import './dep.js';` {
				t.Errorf("%s on main's origin, dev the primary: %d %q", p, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("tokens", func(t *testing.T) {
		w := newDepWS(t, TileAssetsTokens)
		wes := w.session("wes")
		devTok, devBase := assetTokenFrom(t, w.do("/c/apps/a+dev/", wes).Body.String())
		mainTok, mainBase := assetTokenFrom(t, w.do("/c/apps/a/", wes).Body.String())
		if devBase != "apps/a+dev/" || mainBase != "apps/a/" {
			t.Fatalf("asset bases: dev %q, main %q", devBase, mainBase)
		}
		for url, want := range map[string]string{
			"/c/~" + devTok + "/" + devBase + "app.js":   devFiles["app.js"],
			"/c/~" + mainTok + "/" + mainBase + "app.js": `import './dep.js';`,
		} {
			if rec := w.do(url, subresource...); rec.Code != 200 || rec.Body.String() != want {
				t.Errorf("%s: %d %q", url, rec.Code, rec.Body.String())
			}
		}
	})
}
