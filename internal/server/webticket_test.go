package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	w := redeemWeb(h, link.String(), "", "10.1.0.1", nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/c/apps/x/?tab=2" {
		t.Fatalf("redeem: %d %q %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("redeem headers: %v", w.Header())
	}
	sid := sessionCookie(t, w)
	if code, p := probeCookie(h, sid); code != http.StatusOK || p["user"] != "bob" {
		t.Fatalf("the web session: %d %v", code, p)
	}
	// single use
	if w := redeemWeb(h, link.String(), "", "10.1.0.1", nil); w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
		t.Fatalf("replay: %d", w.Code)
	}
	// An empty body lands on "/".
	code, out = mintWeb(h, dev, "", "")
	if code != http.StatusOK {
		t.Fatalf("mint without a body: %d %v", code, out)
	}
	if w := redeemWeb(h, out["url"].(string), "", "10.1.0.1", nil); w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("default next: %d %q", w.Code, w.Header().Get("Location"))
	}
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
	if w := redeemWeb(h, link.String(), "", "", nil); w.Code != http.StatusFound || w.Header().Get("Location") != "/c/apps/x/" {
		t.Fatalf("without next: %d %q", w.Code, w.Header().Get("Location"))
	}
}

// Login CSRF: a ticket only redeems as a navigation nobody started, never
// switches a browser signed in as someone else, and is spent by any attempt.
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
	// No Fetch Metadata at all (an old browser, curl): allowed.
	if w := redeemWeb(h, mint(), "", "", map[string]string{"Sec-Fetch-Site": "", "Sec-Fetch-Mode": "", "Sec-Fetch-Dest": ""}); w.Code != http.StatusFound {
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
	opened := sessionCookie(t, redeemWeb(h, mint(dev), "", "", nil))
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
	if w := redeemWeb(h, out["url"].(string), "", "", nil); w.Code != http.StatusFound {
		t.Fatalf("SSO-only redeem: %d %s", w.Code, w.Body.String())
	}
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
	fresh := sessionCookie(t, redeemWeb(h, out["url"].(string), "", "", nil))
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
	late := sessionCookie(t, redeemWeb(h, out["url"].(string), "", "", nil))
	if code, out := enrollCode("", late, ""); code != http.StatusForbidden || out["stepUp"] != "password" {
		t.Fatalf("the handoff laundered an old device login: %d %v", code, out)
	}
	// A new device login (a new Face ID signature) is fresh again.
	again := s.Auth.NewBearerSession("bob", devID, "").Token
	if code, _ := enrollCode(again, "", ""); code != http.StatusOK {
		t.Fatalf("a new device login: %d", code)
	}
}
