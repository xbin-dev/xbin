package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

func postJSON(h http.Handler, path, body, bearer, ip string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if ip != "" {
		r.RemoteAddr = ip + ":1234"
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func appToken(t *testing.T, w *httptest.ResponseRecorder) appSession {
	t.Helper()
	var out appSession
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Token == "" || out.TokenType != "Bearer" {
		t.Fatalf("want a token response: %d %s", w.Code, w.Body.String())
	}
	if out.ExpiresIdle == 0 || out.ExpiresMax < out.ExpiresIdle {
		t.Fatalf("deadlines: %+v", out)
	}
	return out
}

// probeAs reports the principal a bearer resolves to on /api/xbin/probe.
func probeAs(h http.Handler, bearer string) (int, map[string]any) {
	r := httptest.NewRequest("GET", "/api/xbin/probe", nil)
	r.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// The app's password sign-in: the form login's rules, a bearer session out,
// SSO-only mode for non-admins, the throttle, and bearer sign-out.
func TestAppPasswordLogin(t *testing.T) {
	h, s := impServer(t) // alice admin, bob user, dave admin — password "pw"
	w := postJSON(h, "/api/xbin/login", `{"username":"bob","password":"pw"}`, "", "10.0.0.1")
	tok := appToken(t, w)
	if tok.User.ID != "bob" || tok.User.Role != users.RoleUser || tok.DeviceID != "" {
		t.Fatalf("user: %+v", tok)
	}
	if code, p := probeAs(h, tok.Token); code != http.StatusOK || p["user"] != "bob" || p["admin"] != false {
		t.Fatalf("bearer principal: %d %v", code, p)
	}
	if u, _ := s.Auth.Users.Get("bob"); u.LastLoginVia != "password" {
		t.Fatalf("last login via %q", u.LastLoginVia)
	}
	// Wrong password: generic 401 and throttled after five.
	for i := 0; i < 5; i++ {
		if w := postJSON(h, "/api/xbin/login", `{"username":"bob","password":"nope"}`, "", "10.0.0.2"); w.Code != http.StatusUnauthorized {
			t.Fatalf("bad password: %d", w.Code)
		}
	}
	if w := postJSON(h, "/api/xbin/login", `{"username":"bob","password":"pw"}`, "", "10.0.0.2"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("throttle: %d", w.Code)
	}
	// SSO-only mode refuses non-admins (after a correct password), not admins.
	if err := s.Auth.Users.SetSSO(&users.SSOConfig{Kind: "github", ClientID: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Auth.Users.SetPasswordLoginDisabled(true); err != nil {
		t.Fatal(err)
	}
	if w := postJSON(h, "/api/xbin/login", `{"username":"bob","password":"pw"}`, "", "10.0.0.3"); w.Code != http.StatusForbidden {
		t.Fatalf("SSO-only non-admin: %d %s", w.Code, w.Body.String())
	}
	appToken(t, postJSON(h, "/api/xbin/login", `{"username":"alice","password":"pw"}`, "", "10.0.0.3"))
	// Sign-out: POST /logout with the bearer → 204, and the token is dead.
	if w := postJSON(h, "/logout", ``, tok.Token, ""); w.Code != http.StatusNoContent {
		t.Fatalf("bearer logout: %d", w.Code)
	}
	if code, _ := probeAs(h, tok.Token); code != http.StatusUnauthorized {
		t.Fatalf("token after logout: %d", code)
	}
	// Other public-looking paths stay gated.
	r := httptest.NewRequest("GET", "/api/xbin/login", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/xbin/login must need a principal: %d", w.Code)
	}
}

// The app's SSO sign-in: /login/sso?app=1&challenge=… ends in a one-shot
// ticket at xbin://sso (no cookie), redeemable only with the verifier.
func TestAppSSOTicket(t *testing.T) {
	f := newFakeIdP(t)
	f.email = "jane@corp.com"
	h, _, st := ssoTestServer(t, f, []string{"corp.com"})
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	// A bad challenge is refused before the IdP.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/login/sso?app=1&challenge=short", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad challenge: %d", w.Code)
	}

	roundTrip := func() *httptest.ResponseRecorder {
		w1 := httptest.NewRecorder()
		h.ServeHTTP(w1, httptest.NewRequest("GET", "/login/sso?app=1&challenge="+challenge, nil))
		loc, err := url.Parse(w1.Header().Get("Location"))
		if w1.Code != http.StatusFound || err != nil {
			t.Fatalf("start: %d %v", w1.Code, err)
		}
		f.nonce = loc.Query().Get("nonce")
		r2 := httptest.NewRequest("GET", "/login/sso/callback?"+url.Values{"code": {"c"}, "state": {loc.Query().Get("state")}}.Encode(), nil)
		for _, c := range (&http.Response{Header: w1.Header()}).Cookies() {
			r2.AddCookie(c)
		}
		w2 := httptest.NewRecorder()
		h.ServeHTTP(w2, r2)
		return w2
	}
	ticketOf := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		loc, err := url.Parse(w.Header().Get("Location"))
		if w.Code != http.StatusFound || err != nil || loc.Scheme != "xbin" || loc.Host != "sso" || loc.Query().Get("ticket") == "" {
			t.Fatalf("callback: %d → %q", w.Code, w.Header().Get("Location"))
		}
		for _, c := range (&http.Response{Header: w.Header()}).Cookies() {
			if c.Name == auth.CookieName && c.Value != "" {
				t.Fatal("an app sign-in set a browser session cookie")
			}
		}
		return loc.Query().Get("ticket")
	}
	tk := ticketOf(roundTrip())
	if w := postJSON(h, "/login/ticket", `{"ticket":"`+tk+`","verifier":"not-the-verifier"}`, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong verifier: %d", w.Code)
	}
	if w := postJSON(h, "/login/ticket", `{"ticket":"`+tk+`","verifier":"`+verifier+`"}`, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("a ticket must not survive a failed redemption: %d", w.Code)
	}
	tk = ticketOf(roundTrip())
	tok := appToken(t, postJSON(h, "/login/ticket", `{"ticket":"`+tk+`","verifier":"`+verifier+`"}`, "", ""))
	if tok.User.ID != "jane" {
		t.Fatalf("ticket user: %+v", tok.User)
	}
	if w := postJSON(h, "/login/ticket", `{"ticket":"`+tk+`","verifier":"`+verifier+`"}`, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("ticket replayed: %d", w.Code)
	}
	if u, _ := st.Get("jane"); u.LastLoginVia != "sso" {
		t.Fatalf("last login via %q", u.LastLoginVia)
	}
	// Failures after the state verifies go back to the app, not /login.
	u, _ := st.Get("jane")
	u.Disabled = true
	if _, err := st.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	if w := roundTrip(); w.Header().Get("Location") != "xbin://sso?error=disabled" {
		t.Fatalf("disabled in app flow: %q", w.Header().Get("Location"))
	}
}
