package proxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/ingress"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/users"
)

// zsForge sets everything a caller could send to claim an identity or
// smuggle a credential: every X-XBin-* header xbind sets or might set
// (X-XBin-Deployment included), in two spellings, xbind's cookies beside
// the tile's own, and a bearer.
func zsForge(r *http.Request) {
	for _, h := range []string{"X-XBin-From", "X-XBin-Role", "X-XBin-User", "X-XBin-User-Level", "X-XBin-Viewed-By",
		"X-XBin-Ingress-Host", "X-XBin-Deployment", "X-XBin-Lifecycle"} {
		r.Header.Set(h, "forged")
	}
	r.Header.Set("x-xbin-lowercase", "forged") // the server canonicalizes what a client sends lowercase
	for _, c := range []string{auth.CookieName, auth.SessionCookieHostName, auth.TileCookieName, auth.HostTileCookieName, "tile_pref"} {
		r.AddCookie(&http.Cookie{Name: c, Value: "v-" + c})
	}
	r.Header.Set("Authorization", "Bearer forged-token")
	r.Header.Set("Accept", "application/json")
}

// zsHeaders renders a header set sorted by name, one "Key: value" per
// line (a key's values in their order).
func zsHeaders(h http.Header) string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	for _, k := range keys {
		for _, v := range h[k] {
			lines = append(lines, k+": "+v)
		}
	}
	return strings.Join(lines, "\n")
}

// covers PO-4 Z5 SC-ZERO — the headers a primary's backend receives: for
// every principal kind (human, owner, element frame, instance, terminal,
// view-as, cron) identify strips every inbound X-XBin-* — a forged
// X-XBin-Deployment included — and xbind's credentials, and sets exactly
// today's set; the proxy's own path (policy → identify) and the ingress
// path (ForwardIngress) leave the same headers on the request they
// forward. Hand-maintained goldens: changing one is a compat change
// (12-compat.md).
func TestIdentifyZeroState(t *testing.T) {
	ana := &users.User{ID: "ana", Role: users.RoleUser}
	px := &Proxy{UserLevel: func(uid, tile string) string {
		if uid == "ana" {
			return "write"
		}
		return ""
	}}
	const rest = "Accept: application/json\nCookie: tile_pref=v-tile_pref"
	cases := []struct {
		name, role, tile string
		p                auth.Principal
		want             string
	}{
		{"a human (session)", "reader", "apps/t",
			auth.Principal{UserID: "ana", User: ana, Via: "session"},
			rest + "\nX-Xbin-From: user:ana\nX-Xbin-Role: reader\nX-Xbin-User: ana\nX-Xbin-User-Level: write"},
		{"a human known only by snapshot", "reader", "apps/t",
			auth.Principal{User: ana, Via: "device"},
			rest + "\nX-Xbin-From: \nX-Xbin-Role: reader\nX-Xbin-User: ana\nX-Xbin-User-Level: write"},
		{"the owner (bearer)", "admin", "apps/t",
			auth.Principal{Owner: true, Via: "bearer"},
			rest + "\nX-Xbin-From: owner\nX-Xbin-Role: admin"},
		{"an element frame driven by a user", "writer", "apps/t",
			auth.Principal{Component: "apps/caller", UserID: "ana", Via: "frame"},
			rest + "\nX-Xbin-From: apps/caller\nX-Xbin-Role: writer\nX-Xbin-User: ana\nX-Xbin-User-Level: write"},
		{"an owner-driven element frame", "writer", "apps/t",
			auth.Principal{Component: "apps/caller", Via: "frame"},
			rest + "\nX-Xbin-From: apps/caller\nX-Xbin-Role: writer"},
		{"the tile's own frame", "admin", "apps/t",
			auth.Principal{Component: "apps/t", UserID: "ana", Via: "frame"},
			rest + "\nX-Xbin-From: apps/t\nX-Xbin-Role: admin\nX-Xbin-User: ana\nX-Xbin-User-Level: write"},
		{"a backend (instance token)", "reader", "apps/t",
			auth.Principal{Component: "apps/caller", Via: "instance"},
			rest + "\nX-Xbin-From: apps/caller\nX-Xbin-Role: reader"},
		{"a tile terminal", "admin", "apps/t",
			auth.Principal{Component: "apps/t", UserID: "ana", Via: "terminal"},
			rest + "\nX-Xbin-From: apps/t\nX-Xbin-Role: admin\nX-Xbin-User: ana\nX-Xbin-User-Level: write"},
		{"an admin viewing as ana", "reader", "apps/t",
			auth.Principal{UserID: "ana", User: ana, Via: "session", Impersonator: "root"},
			rest + "\nX-Xbin-From: user:ana\nX-Xbin-Role: reader\nX-Xbin-User: ana\nX-Xbin-User-Level: write\nX-Xbin-Viewed-By: root"},
		{"view-as through a tile frame", "writer", "apps/t",
			auth.Principal{Component: "apps/caller", UserID: "ana", Via: "frame", Impersonator: "owner"},
			rest + "\nX-Xbin-From: apps/caller\nX-Xbin-Role: writer\nX-Xbin-User: ana\nX-Xbin-User-Level: write\nX-Xbin-Viewed-By: owner"},
		{"a cron tick", "admin", "apps/t",
			auth.Principal{Component: "_cron", Via: "cron", Role: "admin"},
			rest + "\nX-Xbin-From: _cron\nX-Xbin-Role: admin"},
		{"a user without a level on the target", "reader", "apps/t",
			auth.Principal{UserID: "bo", Via: "session"},
			rest + "\nX-Xbin-From: user:bo\nX-Xbin-Role: reader\nX-Xbin-User: bo"},
		{"a tile whose own path holds +", "admin", "notes+ideas",
			auth.Principal{Component: "notes+ideas", UserID: "ana", Via: "frame"},
			rest + "\nX-Xbin-From: notes+ideas\nX-Xbin-Role: admin\nX-Xbin-User: ana\nX-Xbin-User-Level: write"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/"+c.tile+"/x", nil)
		zsForge(r)
		px.identify(r, c.p, c.role, c.tile)
		if got := zsHeaders(r.Header); got != c.want {
			t.Errorf("%s: the backend receives\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
	// No UserLevel resolver installed: no level header, the rest as above.
	r := httptest.NewRequest("GET", "/api/apps/t/x", nil)
	zsForge(r)
	(&Proxy{}).identify(r, auth.Principal{UserID: "ana", User: ana, Via: "session"}, "reader", "apps/t")
	if got, want := zsHeaders(r.Header), rest+"\nX-Xbin-From: user:ana\nX-Xbin-Role: reader\nX-Xbin-User: ana"; got != want {
		t.Errorf("without a level resolver:\n%s\nwant\n%s", got, want)
	}

	// The proxy's own path and the ingress path, up to the backend: a
	// runner that may not start anything stops both right after the
	// headers are set, on the very request they would forward.
	root := t.TempDir()
	for rel, body := range map[string]string{
		"xbin.json":                     `{}`,
		"apps/t/xbin.json":              `{"runtime":"node"}`,
		"apps/t/backend/server.js":      `// never runs`,
		"notes+ideas/xbin.json":         `{"runtime":"node"}`,
		"notes+ideas/backend/server.js": `// never runs`,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	run := runner.New(root, nil, events.NewHub(), reg)
	run.ShouldRun = func(string) bool { return false }
	full := &Proxy{Reg: reg, Runner: run, UserLevel: px.UserLevel}
	for _, c := range []struct {
		name, path string
		p          auth.Principal
		want       string
	}{
		{"the owner", "/api/apps/t/x?frame=tok&q=1", auth.Principal{Owner: true, Via: "cookie"},
			rest + "\nX-Xbin-From: owner\nX-Xbin-Role: admin"},
		{"the tile's own frame", "/api/apps/t/x", auth.Principal{Component: "apps/t", UserID: "ana", Via: "frame"},
			rest + "\nX-Xbin-From: apps/t\nX-Xbin-Role: admin\nX-Xbin-User: ana\nX-Xbin-User-Level: write"},
		{"a + tile's own frame", "/api/notes+ideas/x", auth.Principal{Component: "notes+ideas", UserID: "ana", Via: "frame"},
			rest + "\nX-Xbin-From: notes+ideas\nX-Xbin-Role: admin\nX-Xbin-User: ana\nX-Xbin-User-Level: write"},
	} {
		r := httptest.NewRequest("GET", c.path, nil)
		zsForge(r)
		r = r.WithContext(auth.WithPrincipal(r.Context(), c.p))
		rec := httptest.NewRecorder()
		full.ServeHTTP(rec, r)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("%s: %d %s (the runner was to refuse the start)", c.name, rec.Code, rec.Body.String())
		}
		if got := zsHeaders(r.Header); got != c.want {
			t.Errorf("proxy path, %s:\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
	// A caller the default policy refuses gets 403 before any header is set.
	r = httptest.NewRequest("GET", "/api/apps/t/x", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Component: "apps/other", Via: "frame"}))
	rec := httptest.NewRecorder()
	full.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden || r.Header.Get(HeaderFrom) != "" {
		t.Errorf("refused caller: %d, X-XBin-From %q", rec.Code, r.Header.Get(HeaderFrom))
	}

	// Ingress: the anonymous ingress principal and the public host; the
	// tile's own cookies and the Authorization header pass, the workspace
	// session cookies never do.
	r = httptest.NewRequest("GET", "http://shop.example.com/cart?frame=x", nil)
	zsForge(r)
	rec = httptest.NewRecorder()
	full.ForwardIngress(rec, r, ingress.Route{Component: "apps/t", Slot: "web", Paths: []string{"/*"}, Source: "runtime", Host: "shop.example.com"}, false)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("ingress: %d %s", rec.Code, rec.Body.String())
	}
	wantIngress := "Accept: application/json\nAuthorization: Bearer forged-token\n" +
		"Cookie: xbin_tile=v-xbin_tile; __Host-xbin_tile=v-__Host-xbin_tile; tile_pref=v-tile_pref\n" +
		"X-Xbin-From: ingress\nX-Xbin-Ingress-Host: shop.example.com"
	if got := zsHeaders(r.Header); got != wantIngress {
		t.Errorf("ingress path:\n%s\nwant\n%s", got, wantIngress)
	}
}
