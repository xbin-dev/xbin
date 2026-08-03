package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
)

// Credential-less tile subresource authorization (plans/auth.md §6): a
// sandboxed frame's asset loads arrive with no cookie and no Referer, so
// they're authorized by the opaque-origin Fetch-Metadata fingerprint — a
// signal unsandboxed same-origin JS cannot produce — plus a genuine
// subresource destination. Documents, fetches, HTML, and non-GETs must fail.
func TestTileSubresource(t *testing.T) {
	req := func(method, site, dest string) *http.Request {
		r := httptest.NewRequest(method, "/c/apps/x/app.js", nil)
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		if dest != "" {
			r.Header.Set("Sec-Fetch-Dest", dest)
		}
		return r
	}
	cases := []struct {
		name string
		r    *http.Request
		want bool
	}{
		{"module script, opaque origin", req("GET", "cross-site", "script"), true},
		{"stylesheet", req("GET", "cross-site", "style"), true},
		{"image", req("GET", "cross-site", "image"), true},
		{"same-site engine variant", req("GET", "same-site", "script"), true},
		{"HEAD", req("HEAD", "cross-site", "script"), true},
		{"same-origin (unsandboxed JS)", req("GET", "same-origin", "script"), false},
		{"missing site", req("GET", "", "script"), false},
		{"missing dest", req("GET", "cross-site", ""), false},
		{"document dest (HTML nav)", req("GET", "cross-site", "document"), false},
		{"iframe dest (nested doc)", req("GET", "cross-site", "iframe"), false},
		{"empty dest (fetch/XHR)", req("GET", "cross-site", "empty"), false},
		{"embed dest", req("GET", "cross-site", "embed"), false},
		{"non-GET", req("POST", "cross-site", "script"), false},
	}
	for _, c := range cases {
		if got := tileSubresource(c.r); got != c.want {
			t.Errorf("%s: tileSubresource=%v, want %v", c.name, got, c.want)
		}
	}
	// HTML documents never pass the rule even with otherwise-valid signals.
	if tileSubresource(req("GET", "cross-site", "script")) != true {
		t.Fatal("sanity")
	}
	r := httptest.NewRequest("GET", "/c/apps/x/index.html", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Sec-Fetch-Dest", "script")
	if tileSubresource(r) {
		t.Error("index.html passed the subresource rule")
	}
}

// The sandbox decision: everything runs in an opaque origin EXCEPT implicit
// chrome (root, shell — the workspace UI itself) and manifest-flagged trusted
// chrome (e.g. tiles/organisations, which acts as the human by design).
func TestSandboxedFrame(t *testing.T) {
	plain := &registry.Component{Path: "apps/x"}
	chrome := &registry.Component{Path: "tiles/organisations", Manifest: registry.Manifest{Chrome: true}}
	cases := []struct {
		path string
		comp *registry.Component
		want bool
	}{
		{"apps/x", plain, true},
		{"apps/x", nil, true}, // unregistered dir before rescan: sandboxed
		{"root", nil, false},
		{"shell", nil, false},
		{"tiles/organisations", chrome, false},
	}
	for _, c := range cases {
		if got := sandboxedFrame(c.path, c.comp); got != c.want {
			t.Errorf("%s: sandboxedFrame=%v, want %v", c.path, got, c.want)
		}
	}
}
