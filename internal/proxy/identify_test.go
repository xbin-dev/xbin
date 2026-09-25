package proxy

import (
	"net/http/httptest"
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
