package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

const testDeviceKey = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEpVrcD7rXkoAfUgG-zMQbXbm6_8DAlKHGjvB5plisVHw_cypnpN6IdyZpAD3skiCZvuksA6pE7T7Iai4J8zi6Xw"

// webDevice enrolls a device for uid and opens its device-key session.
func webDevice(t *testing.T, s *Server, uid string) (deviceID, bearer string) {
	t.Helper()
	d, err := s.Auth.Users.AddDevice(uid, users.Device{Name: "phone", Platform: "ios", PublicKey: testDeviceKey, Origin: "https://ws.example"})
	if err != nil {
		t.Fatal(err)
	}
	return d.ID, s.Auth.NewBearerSession(uid, d.ID, "10.0.0.9").Token
}

// mintWeb calls POST /api/xbin/web-ticket with a bearer (or cookie).
func mintWeb(h http.Handler, bearer, cookie, body string) (int, map[string]any) {
	r := withCookie("POST", "/api/xbin/web-ticket", body, cookie)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// redeemWeb opens a ticket URL the way the app's browser does: a top-level
// navigation nobody started (Sec-Fetch-Site: none), optionally with the
// browser's existing cookie and other Fetch Metadata.
func redeemWeb(h http.Handler, rawURL, cookie, ip string, hdr map[string]string) *httptest.ResponseRecorder {
	u, _ := url.Parse(rawURL)
	r := withCookie("GET", u.RequestURI(), "", cookie)
	r.Header.Del("Content-Type")
	r.Header.Set("Sec-Fetch-Site", "none")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.Header.Set("Sec-Fetch-Dest", "document")
	for k, v := range hdr {
		if v == "" {
			r.Header.Del(k)
		} else {
			r.Header.Set(k, v)
		}
	}
	if ip != "" {
		r.RemoteAddr = ip + ":4321"
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var reConfirmNonce = regexp.MustCompile(`name="confirm" value="([^"]+)"`)

// pageConfirm is what the "Continue as" page hands its browser: the nonce
// in its form and the cookie its response set.
func pageConfirm(t *testing.T, page *httptest.ResponseRecorder) (nonce, cookie string) {
	t.Helper()
	m := reConfirmNonce.FindStringSubmatch(page.Body.String())
	if page.Code != http.StatusOK || m == nil {
		t.Fatalf("no confirmation page: %d %s", page.Code, page.Body.String())
	}
	for _, c := range page.Result().Cookies() {
		if c.Name == webConfirmCookie(false) && c.MaxAge > 0 {
			cookie = c.Value
		}
	}
	if cookie == "" {
		t.Fatalf("the page set no confirm cookie: %v", page.Result().Cookies())
	}
	return m[1], cookie
}

// confirmWeb presses Continue: a same-origin form post (httptest's host is
// example.com) carrying nonce, the confirm cookie and optionally the
// browser's session cookie; hdr overrides headers ("" deletes).
func confirmWeb(h http.Handler, nonce, confirmCookie, cookie, ip string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/login/web-ticket", strings.NewReader(url.Values{"confirm": {nonce}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.Header.Set("Sec-Fetch-Dest", "document")
	r.Header.Set("Origin", "http://example.com")
	for k, v := range hdr {
		if v == "" {
			r.Header.Del(k)
		} else {
			r.Header.Set(k, v)
		}
	}
	if confirmCookie != "" {
		r.AddCookie(&http.Cookie{Name: webConfirmCookie(false), Value: confirmCookie})
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
	}
	if ip != "" {
		r.RemoteAddr = ip + ":4321"
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// signInWeb is a signed-out browser's whole redeem: open the link, get the
// page, press Continue → the session it opened, having landed on next.
func signInWeb(t *testing.T, h http.Handler, link, ip, next string) string {
	t.Helper()
	nonce, cc := pageConfirm(t, redeemWeb(h, link, "", ip, nil))
	w := confirmWeb(h, nonce, cc, "", ip, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != next {
		t.Fatalf("continue: %d %q %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	return sessionCookie(t, w)
}

// noSession: w set no session cookie.
func noSession(w *httptest.ResponseRecorder) bool {
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName {
			return false
		}
	}
	return true
}

func probeCookie(h http.Handler, sid string) (int, map[string]any) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("GET", "/api/xbin/probe", "", sid))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// The handoff end to end: the app's device session mints, the browser
// redeems once into a cookie session of the same user and lands on next.
func TestWebTicketFlow(t *testing.T) {
	h, s := impServer(t)
	u, _ := s.Auth.Users.Get("bob")
	nu := *u
	nu.Name, nu.Email = "Bob Builder", "bob@example.com"
	if _, err := s.Auth.Users.Upsert(nu, ""); err != nil {
		t.Fatal(err)
	}
	_, dev := webDevice(t, s, "bob")
	code, out := mintWeb(h, dev, "", `{"next":"/c/apps/x/?tab=2"}`)
	if code != http.StatusOK || out["expiresIn"] != float64(60) || out["expires"] == nil {
		t.Fatalf("mint: %d %v", code, out)
	}
	link, err := url.Parse(out["url"].(string))
	if err != nil || link.Scheme != "https" || link.Host != "ws.example" || link.Path != "/login" ||
		link.Query().Get("next") != "/c/apps/x/?tab=2" || len(link.Query().Get("ticket")) < 32 {
		t.Fatalf("url: %v (%v)", out["url"], err)
	}
	// A HEAD (a link checker, a preview fetch) doesn't spend it.
	hr := httptest.NewRequest("HEAD", link.RequestURI(), nil)
	hw := httptest.NewRecorder()
	h.ServeHTTP(hw, hr)
	if hw.Code != http.StatusMethodNotAllowed || len(hw.Result().Cookies()) != 0 {
		t.Fatalf("HEAD: %d", hw.Code)
	}
	// Opening it signs nothing in: a signed-out browser gets the page naming
	// the account, unframeable, with the landing path.
	page := redeemWeb(h, link.String(), "", "10.1.0.1", nil)
	if page.Code != http.StatusOK || !noSession(page) {
		t.Fatalf("redeem: %d %v %s", page.Code, page.Result().Cookies(), page.Body.String())
	}
	body := page.Body.String()
	for _, want := range []string{"Continue as Bob Builder", "bob · bob@example.com", "<code>/c/apps/x/?tab=2</code>", `action="/login/web-ticket"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q:\n%s", want, body)
		}
	}
	if csp := page.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") ||
		!strings.Contains(csp, "form-action 'self'") || page.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("page headers: %v", page.Header())
	}
	// strict-origin, not no-referrer: under no-referrer the browser would
	// send Continue's Origin as "null" (the harness caught it).
	if page.Header().Get("Referrer-Policy") != "strict-origin" || !strings.Contains(page.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("redeem headers: %v", page.Header())
	}
	nonce, cc := pageConfirm(t, page)
	// The link is spent by the open, the page is the only way on.
	if w := redeemWeb(h, link.String(), "", "10.1.0.1", nil); w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
		t.Fatalf("replay: %d", w.Code)
	}
	w := confirmWeb(h, nonce, cc, "", "10.1.0.1", nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/c/apps/x/?tab=2" {
		t.Fatalf("continue: %d %q %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	sid := sessionCookie(t, w)
	if code, p := probeCookie(h, sid); code != http.StatusOK || p["user"] != "bob" {
		t.Fatalf("the web session: %d %v", code, p)
	}
	// Continue is single use too.
	if w := confirmWeb(h, nonce, cc, "", "10.1.0.1", nil); w.Code != http.StatusForbidden || !noSession(w) {
		t.Fatalf("a second Continue: %d", w.Code)
	}
	// An empty body lands on "/".
	code, out = mintWeb(h, dev, "", "")
	if code != http.StatusOK {
		t.Fatalf("mint without a body: %d %v", code, out)
	}
	signInWeb(t, h, out["url"].(string), "10.1.0.1", "/")
	// Signing the app out ends the browser session it opened.
	r := httptest.NewRequest("POST", "/logout", nil)
	r.Header.Set("Authorization", "Bearer "+dev)
	lw := httptest.NewRecorder()
	h.ServeHTTP(lw, r)
	if lw.Code != http.StatusNoContent {
		t.Fatalf("app sign-out: %d", lw.Code)
	}
	if code, _ := probeCookie(h, sid); code != http.StatusUnauthorized {
		t.Fatalf("the web session outlived the app's sign-out: %d", code)
	}
}

// Nothing but the app's device session mints: not a browser cookie, the
// app's password session, a tile (its frame token), a terminal, the owner.
func TestWebTicketNonDeviceRefused(t *testing.T) {
	h, s := impServer(t)
	webDevice(t, s, "bob")
	cookie := s.Auth.NewSession("bob", "")
	appPW := s.Auth.NewBearerSession("bob", "", "").Token
	frame := s.Auth.MintFrameToken("apps/x", "bob", time.Minute)
	term := s.Auth.MintTerminal("apps/x", "bob")
	for name, c := range map[string][2]string{ // bearer, cookie
		"a browser session":          {"", cookie},
		"the app's password session": {appPW, ""},
		"a terminal token":           {term, ""},
		"the owner token":            {s.Auth.OwnerTokenValue(), ""},
	} {
		if code, out := mintWeb(h, c[0], c[1], `{}`); code != http.StatusForbidden {
			t.Errorf("%s minted a web ticket: %d %v", name, code, out)
		}
	}
	r := withCookie("POST", "/api/xbin/web-ticket", `{}`, "")
	r.Header.Set(auth.FrameTokenHeader, frame)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a tile minted a web ticket: %d %s", w.Code, w.Body.String())
	}
}

// next is a same-origin path, checked at the mint and bound to the ticket.
func TestWebTicketNext(t *testing.T) {
	h, s := impServer(t)
	_, dev := webDevice(t, s, "bob")
	for _, bad := range []string{
		"https://evil.example/", "//evil.example/", "/\\evil.example", "\\\\evil.example", "/\t/evil.example",
		"/\n/evil.example", "/%2F/evil.example", "/%5Cevil.example", "evil", "javascript:alert(1)",
		"/a b", "/café", "/" + strings.Repeat("a", 2048), "http:/evil", "/x\x00",
		"/login", "/login?ticket=x", "/login/device", "/logout",
	} {
		body, _ := json.Marshal(map[string]string{"next": bad})
		if code, out := mintWeb(h, dev, "", string(body)); code != http.StatusBadRequest {
			t.Errorf("next %q: %d %v", bad, code, out)
		}
	}
	for _, good := range []string{"/", "/c/apps/x/", "/c/apps/x/?q=%20a&b=1#frag", "/docs/auth.md"} {
		body, _ := json.Marshal(map[string]string{"next": good})
		if code, out := mintWeb(h, dev, "", string(body)); code != http.StatusOK {
			t.Errorf("next %q: %d %v", good, code, out)
		}
	}
	// A next changed in the URL is refused — and the ticket is spent.
	_, out := mintWeb(h, dev, "", `{"next":"/c/apps/x/"}`)
	link, _ := url.Parse(out["url"].(string))
	q := link.Query()
	q.Set("next", "/c/apps/other/")
	tampered := *link
	tampered.RawQuery = q.Encode()
	if w := redeemWeb(h, tampered.String(), "", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("tampered next: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := redeemWeb(h, link.String(), "", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("a refused ticket redeemed later: %d", w.Code)
	}
	// No next in the URL: the ticket's own.
	_, out = mintWeb(h, dev, "", `{"next":"/c/apps/x/"}`)
	link, _ = url.Parse(out["url"].(string))
	q = link.Query()
	q.Del("next")
	link.RawQuery = q.Encode()
	signInWeb(t, h, link.String(), "", "/c/apps/x/")
}

// Login CSRF: a ticket's GET never signs a browser in. It is refused as a
// navigation a page started, never switches a browser signed in as someone
// else, and is spent by any attempt.
func TestWebTicketCSRF(t *testing.T) {
	h, s := impServer(t)
	_, dev := webDevice(t, s, "bob")
	mint := func() string {
		t.Helper()
		code, out := mintWeb(h, dev, "", `{"next":"/"}`)
		if code != http.StatusOK {
			t.Fatalf("mint: %d %v", code, out)
		}
		return out["url"].(string)
	}
	for name, hdr := range map[string]map[string]string{
		"a cross-site link":           {"Sec-Fetch-Site": "cross-site"},
		"a tile origin (same-site)":   {"Sec-Fetch-Site": "same-site"},
		"a page of the workspace":     {"Sec-Fetch-Site": "same-origin"},
		"a subresource (img, fetch)":  {"Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Dest": "image"},
		"a cross-site form, no dest":  {"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Dest": ""},
		"a cors fetch from this page": {"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors"},
	} {
		link, ip := mint(), "10.3.0."+strconv.Itoa(len(name)) // the spent retry counts against the throttle
		if w := redeemWeb(h, link, "", ip, hdr); w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
			t.Errorf("%s redeemed: %d", name, w.Code)
		}
		if w := redeemWeb(h, link, "", ip, nil); w.Code != http.StatusForbidden {
			t.Errorf("%s: the refused ticket stayed redeemable (%d)", name, w.Code)
		}
	}
	// No Fetch Metadata at all (an old browser, curl): the page, no session.
	if w := redeemWeb(h, mint(), "", "", map[string]string{"Sec-Fetch-Site": "", "Sec-Fetch-Mode": "", "Sec-Fetch-Dest": ""}); w.Code != http.StatusOK || !noSession(w) {
		t.Fatalf("no metadata: %d %s", w.Code, w.Body.String())
	}
	// A browser signed in as alice is not switched to bob, and keeps its cookie.
	alice := s.Auth.NewSession("alice", "")
	if w := redeemWeb(h, mint(), alice, "", nil); w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
		t.Fatalf("cross-user redeem: %d", w.Code)
	}
	if code, p := probeCookie(h, alice); code != http.StatusOK || p["user"] != "alice" {
		t.Fatalf("alice's session after a refused redeem: %d %v", code, p)
	}
	// …nor the owner-token cookie.
	if w := redeemWeb(h, mint(), s.Auth.OwnerTokenValue(), "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("redeem over the owner cookie: %d", w.Code)
	}
	// The same person already signed in: let through, no second session.
	bob := s.Auth.NewSession("bob", "")
	before := len(s.Auth.Sessions())
	if w := redeemWeb(h, mint(), bob, "", nil); w.Code != http.StatusFound || len(w.Result().Cookies()) != 0 {
		t.Fatalf("same-user redeem: %d %v", w.Code, w.Result().Cookies())
	}
	if n := len(s.Auth.Sessions()); n != before {
		t.Fatalf("same-user redeem opened a session: %d → %d", before, n)
	}
}

// The review's scenario: Mallory mints a link to HER account and sends it
// to Bob (a chat message, a QR code). Bob's Safari — signed out, since he
// uses the app — opens it from another app: Sec-Fetch-Site none, exactly
// the xbin app's own open. It must not sign him in unseen: he gets a page
// naming Mallory, and only that page's own Continue, from his browser,
// opens a session.
func TestWebTicketExternalOpen(t *testing.T) {
	h, s := impServer(t)
	u, _ := s.Auth.Users.Get("dave")
	nu := *u
	nu.Name = "Mallory"
	if _, err := s.Auth.Users.Upsert(nu, ""); err != nil {
		t.Fatal(err)
	}
	_, mallory := webDevice(t, s, "dave")
	mint := func() string {
		t.Helper()
		code, out := mintWeb(h, mallory, "", `{"next":"/c/apps/mallory/"}`)
		if code != http.StatusOK {
			t.Fatalf("mint: %d %v", code, out)
		}
		return out["url"].(string)
	}
	before := len(s.Auth.Sessions())
	page := redeemWeb(h, mint(), "", "10.5.0.1", nil) // Sec-Fetch-Site: none, navigate, no cookie
	if page.Code != http.StatusOK || !noSession(page) || !strings.Contains(page.Body.String(), "Continue as Mallory") {
		t.Fatalf("an external open: %d %v %s", page.Code, page.Result().Cookies(), page.Body.String())
	}
	if n := len(s.Auth.Sessions()); n != before {
		t.Fatalf("opening the link opened a session: %d → %d", before, n)
	}
	nonce, cc := pageConfirm(t, page)

	// Nothing but the page's own form post, from this browser, continues —
	// and a refused post spends nothing (the real Continue still works).
	for name, c := range map[string]struct {
		cookie string
		hdr    map[string]string
	}{
		"a cross-site form":                   {cc, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}},
		"a tile origin (same-site)":           {cc, map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "http://t1.example.com"}},
		"a link from outside (none)":          {cc, map[string]string{"Sec-Fetch-Site": "none", "Origin": ""}},
		"a fetch from this origin":            {cc, map[string]string{"Sec-Fetch-Mode": "cors"}},
		"no Fetch Metadata, no Origin":        {cc, map[string]string{"Sec-Fetch-Site": "", "Sec-Fetch-Mode": "", "Sec-Fetch-Dest": "", "Origin": ""}},
		"no Fetch Metadata, a foreign Origin": {cc, map[string]string{"Sec-Fetch-Site": "", "Sec-Fetch-Mode": "", "Origin": "https://evil.example"}},
		"no Fetch Metadata, an opaque Origin": {cc, map[string]string{"Sec-Fetch-Site": "", "Sec-Fetch-Mode": "", "Origin": "null"}},
		"same-origin, but a foreign Origin":   {cc, map[string]string{"Origin": "https://evil.example"}},
	} {
		if w := confirmWeb(h, nonce, c.cookie, "", "10.5.0."+strconv.Itoa(2+len(name)), c.hdr); w.Code != http.StatusForbidden || !noSession(w) {
			t.Errorf("%s continued: %d %v", name, w.Code, w.Result().Cookies())
		}
	}
	// Another browser's nonce (a page Mallory opened herself), posted
	// without the cookie that page set, or with Bob's page's cookie, is
	// refused.
	nonce2, _ := pageConfirm(t, redeemWeb(h, mint(), "", "10.5.1.1", nil))
	if w := confirmWeb(h, nonce2, "", "", "10.5.1.1", nil); w.Code != http.StatusForbidden || !noSession(w) {
		t.Fatalf("a nonce without its cookie: %d", w.Code)
	}
	if w := confirmWeb(h, nonce2, cc, "", "10.5.1.2", nil); w.Code != http.StatusForbidden || !noSession(w) {
		t.Fatalf("a nonce with another page's cookie: %d", w.Code)
	}
	// The page's own Continue, from Bob's browser: a session — of Mallory's
	// account, which he saw named before he pressed it.
	w := confirmWeb(h, nonce, cc, "", "10.5.0.1", nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/c/apps/mallory/" {
		t.Fatalf("continue: %d %q %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if code, p := probeCookie(h, sessionCookie(t, w)); code != http.StatusOK || p["user"] != "dave" {
		t.Fatalf("the session: %d %v", code, p)
	}
	// A same-origin post whose Origin a forced no-referrer policy nulled
	// (the browser still vouches with Sec-Fetch-Site) continues…
	nonce, cc = pageConfirm(t, redeemWeb(h, mint(), "", "10.5.4.1", nil))
	if w := confirmWeb(h, nonce, cc, "", "10.5.4.1", map[string]string{"Origin": "null"}); w.Code != http.StatusSeeOther {
		t.Fatalf("same-origin, Origin null: %d %s", w.Code, w.Body.String())
	}
	// …and browsers without Fetch Metadata but with Origin (Safari < 16.4)
	// can too.
	nonce, cc = pageConfirm(t, redeemWeb(h, mint(), "", "10.5.2.1", nil))
	if w := confirmWeb(h, nonce, cc, "", "10.5.2.1", map[string]string{"Sec-Fetch-Site": "", "Sec-Fetch-Mode": "", "Sec-Fetch-Dest": ""}); w.Code != http.StatusSeeOther {
		t.Fatalf("Origin only: %d %s", w.Code, w.Body.String())
	}
	// A browser that signed in as someone else between the page and the
	// press is not switched.
	nonce, cc = pageConfirm(t, redeemWeb(h, mint(), "", "10.5.3.1", nil))
	bob := s.Auth.NewSession("bob", "")
	if w := confirmWeb(h, nonce, cc, bob, "10.5.3.1", nil); w.Code != http.StatusForbidden || !noSession(w) {
		t.Fatalf("continue over bob's session: %d", w.Code)
	}
	if code, p := probeCookie(h, bob); code != http.StatusOK || p["user"] != "bob" {
		t.Fatalf("bob's session after a refused continue: %d %v", code, p)
	}
	// A GET of the confirm route is not a way in.
	gw := httptest.NewRecorder()
	h.ServeHTTP(gw, httptest.NewRequest("GET", "/login/web-ticket?confirm="+nonce, nil))
	if gw.Code == http.StatusOK || gw.Code == http.StatusSeeOther || !noSession(gw) {
		t.Fatalf("GET /login/web-ticket: %d", gw.Code)
	}
}

// The confirmation is short-lived and bound to the device session like
// the ticket: expiry, the device removed, the app signed out, the account
// disabled between the page and the press all refuse it.
func TestWebTicketConfirmRefusals(t *testing.T) {
	h, s := impServer(t)
	devID, dev := webDevice(t, s, "bob")
	page := func(ip string) (string, string) {
		t.Helper()
		code, out := mintWeb(h, dev, "", `{}`)
		if code != http.StatusOK {
			t.Fatalf("mint: %d %v", code, out)
		}
		return pageConfirm(t, redeemWeb(h, out["url"].(string), "", ip, nil))
	}
	nonce, cc := page("10.6.0.1")
	s.Auth.TestExpireWebTickets()
	if w := confirmWeb(h, nonce, cc, "", "10.6.0.1", nil); w.Code != http.StatusForbidden || !noSession(w) {
		t.Fatalf("an expired confirmation: %d", w.Code)
	}
	nonce, cc = page("10.6.0.2")
	u, _ := s.Auth.Users.Get("bob")
	nu := *u
	nu.Disabled = true
	if _, err := s.Auth.Users.Upsert(nu, ""); err != nil {
		t.Fatal(err)
	}
	if w := confirmWeb(h, nonce, cc, "", "10.6.0.2", nil); w.Code != http.StatusForbidden || !noSession(w) {
		t.Fatalf("disabled between page and press: %d", w.Code)
	}
	nu.Disabled = false
	if _, err := s.Auth.Users.Upsert(nu, ""); err != nil {
		t.Fatal(err)
	}
	dev = s.Auth.NewBearerSession("bob", devID, "").Token
	nonce, cc = page("10.6.0.3")
	r := httptest.NewRequest("POST", "/logout", nil)
	r.Header.Set("Authorization", "Bearer "+dev)
	h.ServeHTTP(httptest.NewRecorder(), r)
	if w := confirmWeb(h, nonce, cc, "", "10.6.0.3", nil); w.Code != http.StatusForbidden || !noSession(w) {
		t.Fatalf("the app signed out between page and press: %d", w.Code)
	}
	dev = s.Auth.NewBearerSession("bob", devID, "").Token
	nonce, cc = page("10.6.0.4")
	if _, _, err := s.Auth.Users.RemoveDevice(devID); err != nil {
		t.Fatal(err)
	}
	if w := confirmWeb(h, nonce, cc, "", "10.6.0.4", nil); w.Code != http.StatusForbidden || !noSession(w) {
		t.Fatalf("the device removed between page and press: %d", w.Code)
	}
	// Guessing nonces counts against the login throttle.
	for i := 0; i < 5; i++ {
		g := "guess" + strconv.Itoa(i)
		if w := confirmWeb(h, g, g, "", "10.6.9.9", nil); w.Code != http.StatusForbidden {
			t.Fatalf("a guessed nonce: %d", w.Code)
		}
	}
	if w := confirmWeb(h, "x", "x", "", "10.6.9.9", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("throttle: %d", w.Code)
	}
}

// Expired, revoked, disabled, signed out everywhere: refused. Bad tickets
// count against the login throttle; mints are rate-limited per device.
func TestWebTicketRefusals(t *testing.T) {
	h, s := impServer(t)
	devID, dev := webDevice(t, s, "bob")
	mint := func(bearer string) string {
		t.Helper()
		code, out := mintWeb(h, bearer, "", `{}`)
		if code != http.StatusOK {
			t.Fatalf("mint: %d %v", code, out)
		}
		return out["url"].(string)
	}
	// expiry
	link := mint(dev)
	s.Auth.TestExpireWebTickets()
	if w := redeemWeb(h, link, "", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("expired: %d", w.Code)
	}
	// disabled between mint and redeem
	link = mint(dev)
	u, _ := s.Auth.Users.Get("bob")
	nu := *u
	nu.Disabled = true
	if _, err := s.Auth.Users.Upsert(nu, ""); err != nil {
		t.Fatal(err)
	}
	if w := redeemWeb(h, link, "", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("disabled: %d", w.Code)
	}
	nu.Disabled = false
	if _, err := s.Auth.Users.Upsert(nu, ""); err != nil {
		t.Fatal(err)
	}
	// sign-out-everywhere voids it
	link = mint(dev)
	s.Auth.DropUserSessions("bob")
	if w := redeemWeb(h, link, "", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("after sign-out-everywhere: %d", w.Code)
	}
	// the device removed between mint and redeem; and a web session already
	// open ends with it
	dev = s.Auth.NewBearerSession("bob", devID, "").Token
	opened := signInWeb(t, h, mint(dev), "", "/")
	link = mint(dev)
	if _, _, err := s.Auth.Users.RemoveDevice(devID); err != nil {
		t.Fatal(err)
	}
	s.Auth.DropDeviceSessions(devID)
	if w := redeemWeb(h, link, "", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("revoked device: %d", w.Code)
	}
	if code, _ := probeCookie(h, opened); code != http.StatusUnauthorized {
		t.Fatalf("a web session outlived its device: %d", code)
	}
	// a removed device's session (were it still alive) mints nothing
	devID2, dev2 := webDevice(t, s, "bob")
	if _, _, err := s.Auth.Users.RemoveDevice(devID2); err != nil {
		t.Fatal(err)
	}
	if code, _ := mintWeb(h, dev2, "", `{}`); code != http.StatusForbidden {
		t.Fatalf("a removed device minted: %d", code)
	}
	// guessing: five bad tickets from one IP, then 429
	for i := 0; i < 5; i++ {
		if w := redeemWeb(h, "https://ws.example/login?ticket=guess"+string(rune('a'+i)), "", "10.9.9.9", nil); w.Code != http.StatusForbidden {
			t.Fatalf("bad ticket: %d", w.Code)
		}
	}
	_, dev3 := webDevice(t, s, "bob")
	if w := redeemWeb(h, mint(dev3), "", "10.9.9.9", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("throttle: %d", w.Code)
	}
	// the mint rate
	var code int
	var out map[string]any
	for i := 0; i < 11 && code != http.StatusTooManyRequests; i++ {
		code, out = mintWeb(h, dev3, "", `{}`)
	}
	if code != http.StatusTooManyRequests {
		t.Fatalf("mint rate: %d %v", code, out)
	}
}

// SSO-bound accounts (D93): the handoff needs the IdP's window like a device
// login does.
func TestWebTicketSSOBound(t *testing.T) {
	h, s := impServer(t)
	_, dev := webDevice(t, s, "bob")
	if err := s.Auth.Users.SetSSO(&users.SSOConfig{Kind: "github", ClientID: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Auth.Users.SetPasswordLoginDisabled(true); err != nil {
		t.Fatal(err)
	}
	code, out := mintWeb(h, dev, "", `{}`)
	if code != http.StatusForbidden || out["reauth"] != "sso" {
		t.Fatalf("SSO-only, no SSO sign-in: %d %v", code, out)
	}
	if err := s.Auth.Users.TouchLogin("bob", "sso"); err != nil {
		t.Fatal(err)
	}
	code, out = mintWeb(h, dev, "", `{}`)
	if code != http.StatusOK {
		t.Fatalf("SSO-only within the window: %d %v", code, out)
	}
	signInWeb(t, h, out["url"].(string), "", "/")
}

// The enrollment step-up and the app (docs/auth.md §Device login): a device
// login is a Face ID signature — a step-up — so a device session under 10
// minutes old mints an enrollment code without the password (the app's
// "add another device"); an older one needs the password like any session.
// A browser session handed over by a web ticket keeps the device login's
// time, so the handoff can't turn an old device session into a fresh login.
func TestEnrollStepUpDeviceLogin(t *testing.T) {
	h, s := impServer(t)
	devID, dev := webDevice(t, s, "bob")
	enrollCode := func(bearer, cookie, body string) (int, map[string]any) {
		r := withCookie("POST", "/api/xbin/devices/enroll-code", body, cookie)
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, out := enrollCode(dev, "", ""); code != http.StatusOK || out["code"] == nil {
		t.Fatalf("a fresh device login is a step-up: %d %v", code, out)
	}
	// The handoff from a fresh device login: fresh too.
	_, out := mintWeb(h, dev, "", `{}`)
	fresh := signInWeb(t, h, out["url"].(string), "", "/")
	if code, out := enrollCode("", fresh, ""); code != http.StatusOK {
		t.Fatalf("the web session of a fresh device login: %d %v", code, out)
	}
	// Eleven minutes on: the device session needs the password…
	s.Auth.TestAgeSession(dev, auth.EnrollFreshLogin+time.Minute, 0)
	if code, out := enrollCode(dev, "", ""); code != http.StatusForbidden || out["stepUp"] != "password" {
		t.Fatalf("an old device session minted a code: %d %v", code, out)
	}
	if code, out := enrollCode(dev, "", `{"password":"pw"}`); code != http.StatusOK {
		t.Fatalf("old device session + password: %d %v", code, out)
	}
	// …and so does a browser session it hands over now.
	_, out = mintWeb(h, dev, "", `{}`)
	late := signInWeb(t, h, out["url"].(string), "", "/")
	if code, out := enrollCode("", late, ""); code != http.StatusForbidden || out["stepUp"] != "password" {
		t.Fatalf("the handoff laundered an old device login: %d %v", code, out)
	}
	// A new device login (a new Face ID signature) is fresh again.
	again := s.Auth.NewBearerSession("bob", devID, "").Token
	if code, _ := enrollCode(again, "", ""); code != http.StatusOK {
		t.Fatalf("a new device login: %d", code)
	}
}
