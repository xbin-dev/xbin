package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// The app tunnel's edge (D204, native/spec/tunnel.md §7–§8, docs/app-tunnel.md).
// Requests reach xbind through device D's tunnel on the same handler as the
// console listener; the tunnel listener's ConnContext marks them
// (auth.Transport). Here:
//
//   - a tunnel request is plain http on the loopback Host the app's
//     forwarder listens on: X-Forwarded-* and Forwarded are dropped (nothing
//     the device sends picks a scheme or a client address), and its client
//     key is the device (ClientIP);
//   - one device per tunnel: D's tunnel serves requests carrying D's
//     credentials (the transport gate decides, internal/auth/transport.go),
//     D's own sign-in steps, web-ticket redemption, the sign-in methods,
//     /vendor/, and the credential-less tile subresources the /c/ gate lets
//     through for a device that signed in recently; anything else is 403
//     {tunnel: "device"} — other apps on the phone reach the loopback port;
//   - the app's other sign-ins (password, invite, SSO ticket, enrollment
//     codes) are refused over the tunnel — it signs in with the device key;
//   - while the workspace requires the tunnel, the app's sign-ins and
//     credentials are refused off it: 403 {tunnel: "required"}, never 401
//     (an old app would loop re-signing) or 404 (it reads that as "device
//     removed"). Browsers are not affected; the SSO browser leg isn't either.

// tunnelDocs is the page every refusal names.
const tunnelDocs = "/docs/app-tunnel.md"

// tunnelRefusal is the body (and status) of each refusal.
func tunnelRefusal(why auth.Refusal) (int, string) {
	switch why {
	case auth.RefuseRequired:
		return http.StatusForbidden, "this workspace accepts the xbin app only through its iroh tunnel — pair the app again: on your computer, settings → add a device, and scan the QR code"
	case auth.RefuseBound:
		return http.StatusUnauthorized, "this sign-in works only through the app tunnel it was made on — sign in again"
	}
	return http.StatusForbidden, "this tunnel serves only its own device's sign-in and requests"
}

// writeTunnelRefusal answers a request the transport gate refused.
func writeTunnelRefusal(w http.ResponseWriter, why auth.Refusal) {
	code, msg := tunnelRefusal(why)
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, code, map[string]string{"error": msg, "tunnel": string(why), "docs": tunnelDocs})
}

// appSignIn are the app's sign-in routes: refused off the tunnel while the
// workspace requires it.
var appSignIn = map[string]bool{
	"POST /api/xbin/login":          true,
	"POST /api/xbin/invite/redeem":  true,
	"POST /login/ticket":            true,
	"POST /api/xbin/devices/enroll": true,
	"POST /login/device/challenge":  true,
	"POST /login/device":            true,
	"POST /login/web-ticket":        true,
}

// tunnelOpen are the routes a tunnel serves without a credential: the
// device's own sign-in (its handlers check the device), web-ticket
// redemption, the sign-in methods.
var tunnelOpen = map[string]bool{
	"POST /login/device/challenge": true,
	"POST /login/device":           true,
	"POST /login/web-ticket":       true,
	"GET /api/xbin/login/methods":  true,
}

// tunnelRefusedSignIn are refused over the tunnel: it signs in with the
// device key.
var tunnelRefusedSignIn = map[string]bool{
	"POST /api/xbin/login":          true,
	"POST /api/xbin/invite/redeem":  true,
	"POST /api/xbin/invite/check":   true,
	"POST /login/ticket":            true,
	"POST /api/xbin/devices/enroll": true,
	"POST /login":                   true,
	"POST /login/invite":            true,
}

// webTicketGet reports GET /login?ticket=… (redeeming a web ticket).
func webTicketGet(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Path == "/login" && r.URL.Query().Get("ticket") != ""
}

// hasCredential reports anything that could authenticate r.
func (s *Server) hasCredential(r *http.Request) bool {
	if r.Header.Get("Authorization") != "" || r.Header.Get(auth.FrameTokenHeader) != "" || r.URL.Query().Get("frame") != "" {
		return true
	}
	_, err := s.Auth.SessionCookie(r)
	return err == nil
}

// tunnelGate is the edge above: the outermost wrapper of Handler, for both
// listeners.
func (s *Server) tunnelGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Auth == nil {
			next.ServeHTTP(w, r)
			return
		}
		t := auth.TransportOf(r)
		route := r.Method + " " + r.URL.Path
		if !t.Tunnel {
			if (appSignIn[route] || webTicketGet(r)) && s.Auth.TunnelRequired() {
				writeTunnelRefusal(w, auth.RefuseRequired)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		for k := range r.Header {
			if strings.HasPrefix(k, "X-Forwarded-") || k == "Forwarded" {
				r.Header.Del(k)
			}
		}
		switch {
		case tunnelRefusedSignIn[route]:
			w.Header().Set("Cache-Control", "no-store")
			WriteJSON(w, http.StatusForbidden, map[string]string{"tunnel": string(auth.RefuseDevice), "docs": tunnelDocs,
				"error": "over the app tunnel, sign in with this device's key (device login)"})
			return
		case tunnelOpen[route] || webTicketGet(r),
			(r.Method == http.MethodGet || r.Method == http.MethodHead) && strings.HasPrefix(r.URL.Path, "/vendor/"):
			next.ServeHTTP(w, r)
			return
		}
		_, ok, why := s.Auth.Authenticate(r)
		switch {
		case ok:
			next.ServeHTTP(w, r)
		case why != auth.RefuseNone:
			writeTunnelRefusal(w, why)
		case s.hasCredential(r):
			// D's own credential that no longer works (expired, signed
			// out): the app signs in again
			http.Error(w, "unauthorized — sign in again", http.StatusUnauthorized)
		case strings.HasPrefix(r.URL.Path, "/c/") || isPathTicket(r):
			// a tile subresource (the /c/ gate keys on this device), an
			// asset token, a path ticket: their own handlers gate them
			next.ServeHTTP(w, r)
		default:
			writeTunnelRefusal(w, auth.RefuseDevice)
		}
	})
}

// tunnelDevice refuses (403, written) a sign-in step for another device
// over a tunnel: true when r may go on for deviceID.
func tunnelDevice(w http.ResponseWriter, r *http.Request, deviceID string) bool {
	if t := auth.TransportOf(r); t.Tunnel && t.DeviceID != deviceID {
		writeTunnelRefusal(w, auth.RefuseDevice)
		return false
	}
	return true
}

// tunnelOrigin is the origin a tunnel request stands for: the device's
// recorded one ("" off the tunnel, or for an unknown device) — never the
// app's loopback forwarder.
func (s *Server) tunnelOrigin(r *http.Request) string {
	t := auth.TransportOf(r)
	if !t.Tunnel || s.Auth == nil || s.Auth.Users == nil {
		return ""
	}
	if _, d, ok := s.Auth.Users.FindDevice(t.DeviceID); ok {
		return d.Origin
	}
	return ""
}

// AppTunnel is the app tunnel as the server sees it (internal/apptunnel,
// D204): its switches, and pairing — the enroll-code route's tunnel
// branch. Nil: an xbind without it (tests).
type AppTunnel interface {
	Enabled() bool
	Required() bool
	// MintPairing opens a pairing for p's own login: the code, the link the
	// QR shows (u, c, tv, t, k, p[, r][, rq]) and the pairing's id.
	MintPairing(p auth.Principal, origin string) (*TunnelMint, error)
}

// TunnelMint is a pairing link's parts (the enroll-code answer).
type TunnelMint struct {
	Pairing, Code, URL string
	Expires            time.Time
	Required           bool
	Ticket, ServerKey  string
}

// appTunnelState is (enabled, required); both false without a tunnel.
func (s *Server) appTunnelState() (bool, bool) {
	if s.AppTunnel == nil {
		return false, false
	}
	return s.AppTunnel.Enabled(), s.AppTunnel.Required()
}

// enrollLimits is POST /devices/enroll-code's early refusals (D204):
// while the workspace requires the tunnel, only tunnel codes (409 naming
// /xbin/devices — an older shell can't pair over the tunnel); at the
// person's device limit a classic code (409 {error, limit, count}) — a
// tunnel code still mints there, its confirmation names a device to
// replace — and any code at a limit of 0.
func (s *Server) enrollLimits(w http.ResponseWriter, r *http.Request, p auth.Principal, tunnel, required bool) bool {
	if required && !tunnel {
		WriteError(w, http.StatusConflict, "this workspace pairs the xbin app only through its iroh tunnel — add the device from "+
			s.EnrollOrigin(r)+DevicesPagePath, tunnelDocs)
		return false
	}
	lim, n := s.Auth.Users.UserDeviceLimit(p.User.ID), len(s.Auth.Users.Devices(p.User.ID))
	if n >= lim && (!tunnel || lim == 0) {
		w.Header().Set("Cache-Control", "no-store")
		WriteJSON(w, http.StatusConflict, map[string]any{"error": (&users.DeviceLimitError{Count: n, Limit: lim}).Error(),
			"limit": lim, "count": n, "docs": "/docs/auth.md"})
		return false
	}
	return true
}

// writeTunnelMint answers a tunnel enroll code: the classic fields (code,
// origin, expires) with url the tunnel link, and tunnel {pairing,
// required, ticket, serverKey} — GET /devices/pairing/{pairing} follows it.
func (s *Server) writeTunnelMint(w http.ResponseWriter, p auth.Principal, origin string) {
	m, err := s.AppTunnel.MintPairing(p, origin)
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, err.Error(), tunnelDocs)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, map[string]any{
		"code": m.Code, "origin": origin, "expires": m.Expires.Unix(), "url": m.URL,
		"tunnel": map[string]any{"pairing": m.Pairing, "required": m.Required, "ticket": m.Ticket, "serverKey": m.ServerKey},
	})
}
