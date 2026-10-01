package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/users"
)

// covers PD-47 06§12.1 — a signed-out browser sent to the partitions page
// (a push, a refusal's words, the docs) signs in and lands back on it: the
// redirect carries ?next=, the login form and the single sign-on link carry
// it on, and both sign-ins land there. Only a same-origin path that isn't a
// sign-in route is followed (safeNext); anything else lands on /, as a
// sign-in without next always did.
func TestLoginNext(t *testing.T) {
	f := newFakeIdP(t)
	f.email, f.name = "jane@corp.com", "Jane"
	h, _, st := ssoTestServer(t, f, []string{"corp.com"})
	if _, err := st.Upsert(users.User{ID: "ann"}, "password123"); err != nil {
		t.Fatal(err)
	}
	get := func(u string, accept string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", u, nil)
		if accept != "" {
			r.Header.Set("Accept", accept)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	// the redirect: the partitions page carries its path; every other page
	// goes to plain /login, as before
	for u, want := range map[string]string{
		PersonPagePath:          "/login?next=%2Fxbin%2Fpartitions",
		PersonPagePath + "?x=1": "/login?next=%2Fxbin%2Fpartitions%3Fx%3D1",
		"/docs/partitions.md":   "/login",
		"/":                     "/login",
	} {
		if w := get(u, "text/html"); w.Code != http.StatusFound || w.Header().Get("Location") != want {
			t.Errorf("signed out, GET %s: %d → %q, want %q", u, w.Code, w.Header().Get("Location"), want)
		}
	}
	// the login page carries a safe next on, in its form and its SSO link
	page := get("/login?next=%2Fxbin%2Fpartitions", "text/html").Body.String()
	if !strings.Contains(page, `<input type="hidden" name="next" value="/xbin/partitions">`) || !strings.Contains(page, `href="/login/sso?next=%2Fxbin%2Fpartitions"`) {
		t.Errorf("the login page doesn't carry next:\n%s", page)
	}
	evil := []string{"//evil.example/x", "https://evil.example/", "/\\evil.example", "/login/sso", "/logout", "javascript:alert(1)", "/\n/evil"}
	for _, n := range append(evil, "", "/") {
		page := get("/login?next="+url.QueryEscape(n), "text/html").Body.String()
		if strings.Contains(page, `name="next"`) || !strings.Contains(page, `href="/login/sso"`) {
			t.Errorf("next %q: the login page carries it:\n%s", n, page)
		}
	}
	// the password sign-in lands on next, a refused one on /
	login := func(next string) string {
		form := url.Values{"username": {"ann"}, "password": {"password123"}}
		if next != "" {
			form.Set("next", next)
		}
		r := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusFound {
			t.Fatalf("password sign-in (next %q): %d %s", next, w.Code, w.Body.String())
		}
		return w.Header().Get("Location")
	}
	if got := login("/xbin/partitions"); got != "/xbin/partitions" {
		t.Errorf("password sign-in with next: lands on %q", got)
	}
	for _, n := range append(evil, "") {
		if got := login(n); got != "/" {
			t.Errorf("password sign-in with next %q: lands on %q, want /", n, got)
		}
	}
	// single sign-on: next rides the signed state cookie to the callback
	sso := func(start string) string {
		w1 := get(start, "")
		loc, err := url.Parse(w1.Header().Get("Location"))
		if w1.Code != http.StatusFound || err != nil {
			t.Fatalf("sso start %s: %d %v", start, w1.Code, err)
		}
		f.nonce = loc.Query().Get("nonce")
		cb := url.Values{"code": {"authcode-1"}, "state": {loc.Query().Get("state")}}
		r := httptest.NewRequest("GET", "/login/sso/callback?"+cb.Encode(), nil)
		for _, c := range (&http.Response{Header: w1.Header()}).Cookies() {
			r.AddCookie(c)
		}
		w2 := httptest.NewRecorder()
		h.ServeHTTP(w2, r)
		if w2.Code != http.StatusFound {
			t.Fatalf("sso callback (%s): %d %s", start, w2.Code, w2.Body.String())
		}
		return w2.Header().Get("Location")
	}
	if got := sso("/login/sso?next=%2Fxbin%2Fpartitions"); got != "/xbin/partitions" {
		t.Errorf("SSO sign-in with next: lands on %q", got)
	}
	for _, n := range []string{"", "//evil.example/x", "https://evil.example/"} {
		if got := sso("/login/sso?next=" + url.QueryEscape(n)); got != "/" {
			t.Errorf("SSO sign-in with next %q: lands on %q, want /", n, got)
		}
	}
}
