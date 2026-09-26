package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
)

// A backend learns who is calling from headers xbind sets after stripping
// anything the caller sent — the human behind a frame (D29), and whether an
// admin is looking through that human's eyes (view-as, D64), so a backend
// holding per-user private data can refuse it to the viewer.
func TestIdentifyHeaders(t *testing.T) {
	px := &Proxy{UserLevel: func(uid, tile string) string { return "read" }}
	for _, tc := range []struct {
		name   string
		p      auth.Principal
		user   string
		viewed string
	}{
		{"frame as a user", auth.Principal{UserID: "dev1", Component: "apps/agent", Via: "frame"}, "dev1", ""},
		{"an admin viewing as dev1", auth.Principal{UserID: "dev1", Component: "apps/agent", Via: "frame", Impersonator: "admin"}, "dev1", "admin"},
		{"automation", auth.Principal{Component: "apps/other", Via: "instance"}, "", ""},
	} {
		r := httptest.NewRequest("GET", "/api/apps/agent/runs", nil)
		r.Header.Set("X-XBin-User", "spoofed")
		r.Header.Set("X-XBin-Viewed-By", "spoofed")
		px.identify(r, tc.p, "admin", "apps/agent")
		if got := r.Header.Get(HeaderUser); got != tc.user {
			t.Errorf("%s: user %q, want %q", tc.name, got, tc.user)
		}
		if got := r.Header.Get(HeaderViewedBy); got != tc.viewed {
			t.Errorf("%s: viewed-by %q, want %q", tc.name, got, tc.viewed)
		}
	}
}

// xbind's own credentials never reach a backend (review): a link followed
// to /api/<tile>/export carried the session cookie, a bx call the owner
// token, and the backend — the tile's writers' code — could replay either
// as the caller, an admin included. The tile's own cookies and a
// non-bearer Authorization pass.
func TestIdentifyStripsXbindCredentials(t *testing.T) {
	px := &Proxy{}
	p := auth.Principal{UserID: "ana", Via: "session"}
	r := httptest.NewRequest("GET", "/api/apps/t/export", nil)
	for _, c := range []string{auth.CookieName, auth.SessionCookieHostName, auth.TileCookieName, auth.HostTileCookieName, "tile_pref"} {
		r.AddCookie(&http.Cookie{Name: c, Value: "v-" + c})
	}
	r.Header.Set("Authorization", "Bearer owner-token")
	px.identify(r, p, "reader", "apps/t")
	var names []string
	for _, c := range r.Cookies() {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "tile_pref" {
		t.Errorf("the backend sees cookies %v, want only the tile's own", names)
	}
	if h := r.Header.Get("Authorization"); h != "" {
		t.Errorf("the backend sees Authorization %q", h)
	}
	for _, a := range []string{"Basic dXNlcjpwYXNz", "bearer lower"} {
		r := httptest.NewRequest("GET", "/api/apps/t/x", nil)
		r.Header.Set("Authorization", a)
		px.identify(r, p, "reader", "apps/t")
		if got := r.Header.Get("Authorization"); (got == "") != strings.HasPrefix(strings.ToLower(a), "bearer ") {
			t.Errorf("Authorization %q → %q", a, got)
		}
	}
	// No xbind cookie: the header is left exactly as sent.
	r = httptest.NewRequest("GET", "/api/apps/t/x", nil)
	r.Header.Set("Cookie", "a=1;b=2")
	px.identify(r, p, "reader", "apps/t")
	if r.Header.Get("Cookie") != "a=1;b=2" {
		t.Errorf("an unrelated Cookie header was rewritten: %q", r.Header.Get("Cookie"))
	}
}
