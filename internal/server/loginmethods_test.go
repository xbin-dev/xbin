package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/branding"
	"github.com/xbin-dev/xbin/internal/users"
)

// getMethods fetches GET /api/xbin/login/methods with no credential at all.
func getMethods(t *testing.T, h http.Handler) loginMethods {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/xbin/login/methods", nil))
	var m loginMethods
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &m) != nil {
		t.Fatalf("login/methods: %d %s", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("login/methods Cache-Control %q", cc)
	}
	if m.API != 1 {
		t.Fatalf("api %d", m.API)
	}
	return m
}

// The sign-in discovery route: public, what the login page shows, in every
// mode — no-auth, no accounts yet, password only, SSO configured with and
// without --external-url, SSO-only — and never throttled.
func TestLoginMethods(t *testing.T) {
	// no-auth mode: served, and says there is nothing to sign in with
	na, err := auth.Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if m := getMethods(t, (&Server{Auth: na}).Handler()); m.Auth || m.Password.Enabled || m.SSO.Enabled || m.Invites || m.Title != "xbin" {
		t.Fatalf("no-auth: %+v", m)
	}

	// auth on, no accounts yet (the bootstrap token only)
	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.SetUsers(st)
	if m := getMethods(t, (&Server{Auth: a}).Handler()); !m.Auth || m.Password.Enabled || m.SSO.Enabled || m.Invites {
		t.Fatalf("no accounts: %+v", m)
	}

	// password accounts, no SSO; the brand's title
	h, s := impServer(t)
	s.Brand = branding.New(filepath.Join(t.TempDir(), "branding.json"))
	title := "Acme Ops"
	if _, err := s.Brand.Apply(branding.Patch{Title: &title}); err != nil {
		t.Fatal(err)
	}
	m := getMethods(t, h)
	if !m.Auth || !m.Password.Enabled || m.Password.AdminOnly || m.SSO.Enabled || m.SSO.Label != "" || !m.Invites || m.Title != "Acme Ops" {
		t.Fatalf("password only: %+v", m)
	}

	// SSO configured but not startable (no --external-url): the page shows no button
	st2 := s.Auth.Users
	if err := st2.SetSSO(&users.SSOConfig{Kind: "github", ClientID: "c", ButtonLabel: "Sign in with Acme"}); err != nil {
		t.Fatal(err)
	}
	if m := getMethods(t, h); m.SSO.Enabled || m.SSO.Label != "" {
		t.Fatalf("SSO without external-url: %+v", m)
	}
	s.ExternalURL = "https://xbin.test"
	if m := getMethods(t, h); !m.SSO.Enabled || m.SSO.Label != "Sign in with Acme" || !m.Password.Enabled || m.Password.AdminOnly {
		t.Fatalf("SSO on: %+v", m)
	}

	// SSO-only mode (D53): the form stays for admins
	if err := st2.SetPasswordLoginDisabled(true); err != nil {
		t.Fatal(err)
	}
	if m := getMethods(t, h); !m.Password.Enabled || !m.Password.AdminOnly || !m.SSO.Enabled || !m.Invites {
		t.Fatalf("SSO-only: %+v", m)
	}

	// not throttled: a throttled client still reads it
	for i := 0; i < throttleMaxFails; i++ {
		postJSON(h, "/api/xbin/login", `{"username":"bob","password":"nope"}`, "", "192.0.2.9")
	}
	if w := postJSON(h, "/api/xbin/login", `{"username":"bob","password":"pw"}`, "", "192.0.2.9"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("throttle precondition: %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/api/xbin/login/methods", nil)
	r.RemoteAddr = "192.0.2.9:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("methods under throttle: %d", w.Code)
	}
}

// The app's invite redemption: check names the account without spending
// the invite; redeem sets the password once and answers a bearer session;
// a weak password is a 400 that leaves the invite; reuse, unknown and
// expired invites are one 403; failures count against the login throttle.
func TestInviteCheckRedeem(t *testing.T) {
	h, s := impServer(t)
	st := s.Auth.Users
	if _, err := st.UpsertInvited(users.User{ID: "erin", Name: "Erin Example"}); err != nil {
		t.Fatal(err)
	}
	tok, err := st.CreateInvite("erin", 0)
	if err != nil {
		t.Fatal(err)
	}

	// check: who, until when, which workspace — and nothing spent
	w := postJSON(h, "/api/xbin/invite/check", `{"invite":"`+tok+`"}`, "", "10.1.0.1")
	var chk struct {
		User    struct{ ID, Name string }
		Expires int64
		Title   string
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &chk) != nil {
		t.Fatalf("check: %d %s", w.Code, w.Body.String())
	}
	if chk.User.ID != "erin" || chk.User.Name != "Erin Example" || chk.Title != "xbin" ||
		chk.Expires < time.Now().Add(users.InviteTTL-time.Minute).Unix() || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("check body: %+v", chk)
	}
	if _, ok := st.InviteUser(tok); !ok {
		t.Fatal("check spent the invite")
	}
	for _, body := range []string{`{"invite":"nope"}`, `{}`} {
		if w := postJSON(h, "/api/xbin/invite/check", body, "", "10.1.0.2"); w.Code != http.StatusForbidden || !jsonHas(w, "error", "invalid or expired invite") {
			t.Fatalf("check %s: %d %s", body, w.Code, w.Body.String())
		}
	}

	// a weak password: 400 with the policy, the invite stays
	if w := postJSON(h, "/api/xbin/invite/redeem", `{"invite":"`+tok+`","password":"short"}`, "", "10.1.0.1"); w.Code != http.StatusBadRequest || !jsonHas(w, "error", "password too short (min 8 characters)") {
		t.Fatalf("weak password: %d %s", w.Code, w.Body.String())
	}
	if _, ok := st.InviteUser(tok); !ok {
		t.Fatal("a refused password spent the invite")
	}

	// redeem: the token response of /api/xbin/login, the password set, via "invite"
	sess := appToken(t, postJSON(h, "/api/xbin/invite/redeem", `{"invite":"`+tok+`","password":"erin-password-1"}`, "", "10.1.0.1"))
	if sess.User.ID != "erin" || sess.User.Name != "Erin Example" || sess.User.Role != users.RoleUser || sess.DeviceID != "" {
		t.Fatalf("redeem: %+v", sess)
	}
	if code, p := probeAs(h, sess.Token); code != http.StatusOK || p["user"] != "erin" {
		t.Fatalf("bearer principal: %d %v", code, p)
	}
	if u, _ := st.Get("erin"); u.LastLoginVia != "invite" {
		t.Fatalf("last login via %q", u.LastLoginVia)
	}
	if _, ok := st.Verify("erin", "erin-password-1"); !ok {
		t.Fatal("password not set")
	}
	appToken(t, postJSON(h, "/api/xbin/login", `{"username":"erin","password":"erin-password-1"}`, "", "10.1.0.1"))

	// once: check and redeem both refuse the spent invite
	if w := postJSON(h, "/api/xbin/invite/check", `{"invite":"`+tok+`"}`, "", "10.1.0.3"); w.Code != http.StatusForbidden {
		t.Fatalf("check after redeem: %d", w.Code)
	}
	if w := postJSON(h, "/api/xbin/invite/redeem", `{"invite":"`+tok+`","password":"erin-password-2"}`, "", "10.1.0.3"); w.Code != http.StatusForbidden || !jsonHas(w, "error", "invalid or expired invite") {
		t.Fatalf("reuse: %d %s", w.Code, w.Body.String())
	}
	if _, ok := st.Verify("erin", "erin-password-1"); !ok {
		t.Fatal("a refused reuse changed the password")
	}

	// expired: refused by both, even with a good password
	tok2, err := st.CreateInvite("bob", 0)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := st.Get("bob")
	nu := *u
	nu.InviteExpires = time.Now().Add(-time.Minute).Unix()
	if _, err := st.Upsert(nu, ""); err != nil {
		t.Fatal(err)
	}
	if w := postJSON(h, "/api/xbin/invite/check", `{"invite":"`+tok2+`"}`, "", "10.1.0.4"); w.Code != http.StatusForbidden {
		t.Fatalf("expired check: %d", w.Code)
	}
	if w := postJSON(h, "/api/xbin/invite/redeem", `{"invite":"`+tok2+`","password":"bob-password-9"}`, "", "10.1.0.4"); w.Code != http.StatusForbidden {
		t.Fatalf("expired redeem: %d", w.Code)
	}
	if _, ok := st.Verify("bob", "pw"); !ok {
		t.Fatal("an expired invite changed the password")
	}

	// throttled: five failures from one client → 429 for check and redeem,
	// even with a valid invite; the invite is still there after
	tok3, err := st.CreateInvite("dave", 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < throttleMaxFails; i++ {
		postJSON(h, "/api/xbin/invite/check", `{"invite":"guess-`+string(rune('a'+i))+`"}`, "", "10.1.0.5")
	}
	if w := postJSON(h, "/api/xbin/invite/check", `{"invite":"`+tok3+`"}`, "", "10.1.0.5"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("throttled check: %d", w.Code)
	}
	if w := postJSON(h, "/api/xbin/invite/redeem", `{"invite":"`+tok3+`","password":"dave-password-1"}`, "", "10.1.0.5"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("throttled redeem: %d", w.Code)
	}
	if _, ok := st.InviteUser(tok3); !ok {
		t.Fatal("a throttled redeem spent the invite")
	}
	// …and the form login shares the same throttle
	if w := postJSON(h, "/api/xbin/login", `{"username":"dave","password":"pw"}`, "", "10.1.0.5"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("shared throttle: %d", w.Code)
	}

	// a disabled account's invite is refused like an unknown one
	u, _ = st.Get("dave")
	nu = *u
	nu.Disabled = true
	if _, err := st.Upsert(nu, ""); err != nil {
		t.Fatal(err)
	}
	if w := postJSON(h, "/api/xbin/invite/redeem", `{"invite":"`+tok3+`","password":"dave-password-1"}`, "", "10.1.0.6"); w.Code != http.StatusForbidden {
		t.Fatalf("disabled redeem: %d", w.Code)
	}
}

// In no-auth mode there is nothing to redeem an invite into.
func TestInviteNoAuth(t *testing.T) {
	a, err := auth.Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	h := (&Server{Auth: a}).Handler()
	if w := postJSON(h, "/api/xbin/invite/check", `{"invite":"x"}`, "", ""); w.Code != http.StatusForbidden {
		t.Fatalf("no-auth check: %d %s", w.Code, w.Body.String())
	}
}

func jsonHas(w *httptest.ResponseRecorder, key, want string) bool {
	var m map[string]any
	return json.Unmarshal(w.Body.Bytes(), &m) == nil && m[key] == want
}
