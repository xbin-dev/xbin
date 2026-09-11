package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// impServer: an admin (alice), a user (bob), a second admin (dave), and a
// probe route that reports the principal the middleware installed.
func impServer(t *testing.T) (http.Handler, *Server) {
	t.Helper()
	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []users.User{
		{ID: "alice", Role: users.RoleAdmin},
		{ID: "bob", Role: users.RoleUser},
		{ID: "dave", Role: users.RoleAdmin},
	} {
		if _, err := st.Upsert(u, "pw"); err != nil {
			t.Fatal(err)
		}
	}
	a.SetUsers(st)
	s := &Server{Auth: a}
	probe := func(w http.ResponseWriter, r *http.Request) {
		p := auth.PrincipalOf(r)
		WriteJSON(w, http.StatusOK, map[string]any{"user": p.UserID, "by": p.Impersonator, "admin": p.IsAdmin()})
	}
	s.RegisterAPI("GET /probe", probe)
	s.RegisterAPI("POST /probe", probe)
	return s.Handler(), s
}

func withCookie(method, path, body, sid string) *http.Request {
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, rd)
	r.Header.Set("Content-Type", "application/json")
	if sid != "" {
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sid})
	}
	return r
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c.Value
		}
	}
	t.Fatalf("no session cookie set (%d %s)", w.Code, w.Body.String())
	return ""
}

func TestImpersonateFlow(t *testing.T) {
	h, s := impServer(t)
	alice := s.Auth.NewSession("alice", "10.0.0.1")
	bob := s.Auth.NewSession("bob", "10.0.0.2")

	// bob can't mint; alice can.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("POST", "/api/xbin/impersonate", `{"user":"alice"}`, bob))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-admin mint: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("POST", "/api/xbin/impersonate", `{"user":"bob"}`, alice))
	if w.Code != http.StatusOK {
		t.Fatalf("mint: %d %s", w.Code, w.Body.String())
	}
	var minted struct{ URL, User string }
	_ = json.Unmarshal(w.Body.Bytes(), &minted)
	if !strings.HasPrefix(minted.URL, "/login?impersonate=") || minted.User != "bob" {
		t.Fatalf("mint body: %s", w.Body.String())
	}

	// bob's browser can't redeem alice's ticket; the ticket is burnt by the try.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("GET", minted.URL, "", bob))
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-browser redeem: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("GET", minted.URL, "", alice))
	if w.Code != http.StatusForbidden {
		t.Fatalf("burnt ticket redeemed: %d", w.Code)
	}
	// A signed-out browser is told to sign in first.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("GET", "/login?impersonate=zzz", "", ""))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "sign in") {
		t.Fatalf("anonymous redeem: %d %s", w.Code, w.Body.String())
	}

	// Mint again, redeem as alice: 302 / with a new cookie.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("POST", "/api/xbin/impersonate", `{"user":"bob"}`, alice))
	_ = json.Unmarshal(w.Body.Bytes(), &minted)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("GET", minted.URL, "", alice))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("redeem: %d → %q %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	view := sessionCookie(t, w)
	if view == alice || view == "" {
		t.Fatal("redeem did not mint a new session")
	}

	// Reads are bob's; writes are refused with the API error shape.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("GET", "/api/xbin/probe", "", view))
	var seen struct {
		User, By string
		Admin    bool
	}
	_ = json.Unmarshal(w.Body.Bytes(), &seen)
	if w.Code != http.StatusOK || seen.User != "bob" || seen.By != "alice" || seen.Admin {
		t.Fatalf("view read: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("POST", "/api/xbin/probe", `{}`, view))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"error":"read-only`) {
		t.Fatalf("view write: %d %s", w.Code, w.Body.String())
	}
	// No nesting from inside the view.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("POST", "/api/xbin/impersonate", `{"user":"dave"}`, view))
	if w.Code != http.StatusForbidden {
		t.Fatalf("nested mint: %d", w.Code)
	}

	// Stop hands alice's own session back.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("POST", "/api/xbin/impersonate/stop", "", view))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"restored":true`) {
		t.Fatalf("stop: %d %s", w.Code, w.Body.String())
	}
	if got := sessionCookie(t, w); got != alice {
		t.Fatalf("stop restored %q, want alice's session", got)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("GET", "/api/xbin/probe", "", view))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("view session after stop: %d", w.Code)
	}
	// stop on a plain session is a 400, not a logout.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("POST", "/api/xbin/impersonate/stop", "", alice))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("stop on plain session: %d", w.Code)
	}
}

func TestImpersonateLogoutAndTerminal(t *testing.T) {
	h, s := impServer(t)
	alice := s.Auth.NewSession("alice", "")
	mint := func(user string) string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie("POST", "/api/xbin/impersonate", `{"user":"`+user+`"}`, alice))
		var m struct{ URL string }
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, withCookie("GET", m.URL, "", alice))
		return sessionCookie(t, w)
	}

	// /logout from a view returns the admin to themselves, not to /login.
	view := mint("bob")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("POST", "/logout", "", view))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" || sessionCookie(t, w) != alice {
		t.Fatalf("logout from view: %d → %q", w.Code, w.Header().Get("Location"))
	}

	// A terminal is refused inside a view even when the user could open one.
	view = mint("dave")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("GET", "/ws/term", "", view))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "read-only") {
		t.Fatalf("terminal in view: %d %s", w.Code, w.Body.String())
	}

	// The admin's session expiring while viewing → stop clears the cookie.
	s.Auth.DropSession(alice)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, withCookie("POST", "/api/xbin/impersonate/stop", "", view))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"restored":false`) {
		t.Fatalf("stop after admin expiry: %d %s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName && c.MaxAge >= 0 {
			t.Fatalf("cookie not cleared: %+v", c)
		}
	}
}

func TestReadOnlyAllowed(t *testing.T) {
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/xbin/users", true},
		{"HEAD", "/c/apps/x/", true},
		{"POST", "/api/xbin/impersonate/stop", true},
		{"POST", "/api/xbin/impersonate", false},
		{"PUT", "/api/xbin/prefs/layout", false},
		{"DELETE", "/ws/term", false},
		{"POST", "/api/apps/x/save", false},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := readOnlyAllowed(r); got != c.want {
			t.Errorf("readOnlyAllowed(%s %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}
