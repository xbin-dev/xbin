package server

// devicespage.go — the devices page, GET /xbin/devices (D204,
// docs/app-tunnel.md): a guaranteed surface shipped with xbind, like
// /xbin/limits (limitspage.go), so a workspace whose shell predates the app
// tunnel still pairs a phone over it — the confirmation-code step an old
// shell can't show — and a workspace that requires the tunnel still adds
// devices; enroll-code's refusal names it.
//
// The same contract as the limits page: web/devices.html served byte for
// byte (no HTML transform, no principal-specific bytes), reading and acting
// through /api/xbin with the signed-in person's session; top-level only,
// a CSP of 'self' alone, and no copy under /vendor/ (topLevelPage).

import (
	"io/fs"
	"net/http"
)

const (
	// DevicesPagePath is where the devices page is served.
	DevicesPagePath = "/xbin/devices"
	devicesPageFile = "devices.html" // in the web assets (xbin.WebFS)
)

// handleDevicesPage serves the devices page to any signed-in principal.
func (s *Server) handleDevicesPage(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.WebFS, devicesPageFile)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", personPageCSP)
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Referrer-Policy", "same-origin")
	_, _ = w.Write(b)
}
