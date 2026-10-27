package server

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// covers D204 — the devices page is xbind's own, guaranteed surface:
// GET /xbin/devices serves web/devices.html byte for byte to a signed-in
// person, top-level only under a CSP of 'self' alone; a signed-out browser
// signs in and comes back; /vendor/ never serves the page; without the
// file the route is a 404.
func TestDevicesPage(t *testing.T) {
	page := []byte("<!doctype html><title>Devices</title><script type=\"module\" src=\"/vendor/devices-page.js\"></script>\n")
	w := newAssetWS(t, TileAssetsTokens)
	w.s.WebFS = fstest.MapFS{
		devicesPageFile:   {Data: page},
		"devices-page.js": {Data: []byte("export {};\n")},
	}
	w.do("/healthz") // builds the handler
	if !slices.Contains(w.s.CoreRoutes(), "GET "+DevicesPagePath) {
		t.Fatalf("GET %s isn't a core route", DevicesPagePath)
	}
	rec := w.do(DevicesPagePath, w.session("ana"), hdr("Accept", "text/html"), hdr("Sec-Fetch-Dest", "document"))
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), page) {
		t.Fatalf("page: %d %q", rec.Code, rec.Body.String())
	}
	h := rec.Header()
	if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "script-src 'self'") ||
		h.Get("X-Frame-Options") != "DENY" || h.Get("Cross-Origin-Opener-Policy") != "same-origin" {
		t.Errorf("headers %v", h)
	}
	if rec := w.do(DevicesPagePath, hdr("Accept", "text/html")); rec.Code != 302 || rec.Header().Get("Location") != "/login?next=%2Fxbin%2Fdevices" {
		t.Errorf("signed out: %d %v", rec.Code, rec.Header())
	}
	if rec := w.do("/vendor/devices.html"); rec.Code != 404 {
		t.Errorf("/vendor/devices.html: %d", rec.Code)
	}
	w.s.WebFS = fstest.MapFS{"partitions.html": {Data: []byte("<p>x</p>")}}
	if rec := w.do(DevicesPagePath, w.session("ana"), hdr("Accept", "text/html")); rec.Code != 404 {
		t.Errorf("no page file: %d", rec.Code)
	}
}
