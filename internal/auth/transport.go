package auth

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// The app tunnel's transport rules (D204, plans/app-tunnel.md §4,
// native/spec/tunnel.md §7–§8): one gate, at the end of FromRequest, keyed
// on the credential generation the principal authenticated under — so the
// same rule covers a session and everything derived from it (frame tokens,
// asset tokens, tile-origin cookies, path tickets carry its generation).
//
//   - On device D's tunnel only D's credentials work: a session D opened
//     (bearer, or a web-ticket cookie it handed over), a credential derived
//     from one, or a "u." generation of D's user. Browser cookies, the owner
//     token, instance and terminal tokens, other devices and other users
//     are refused (RefuseDevice): another app on the phone can reach the
//     tunnel's loopback port.
//   - A session opened over a tunnel is bound to it: off the tunnel it and
//     everything derived from it are refused (RefuseBound).
//   - While the workspace requires the tunnel (SetTunnelPolicy), app
//     credentials are refused off it (RefuseRequired): app sessions with or
//     without a device, and the browser sessions a device opened. Browsers'
//     own sessions are not affected.

// Transport is how a request reached xbind: directly, or through device
// DeviceID's app tunnel (the tunnel listener's ConnContext puts it on every
// request of the connection; nothing a client sends can set it).
type Transport struct {
	Tunnel   bool
	DeviceID string
	UserID   string
	Endpoint string // the device's iroh EndpointId (hex)
}

// ClientKey is the tunnel's stand-in for a client IP: one per device, so
// the login throttle, session records and warm-IP gate keep devices apart.
func (t Transport) ClientKey() string { return "tunnel/" + t.DeviceID }

type transportKey struct{}

// WithTransport records the transport on a connection's context.
func WithTransport(ctx context.Context, t Transport) context.Context {
	return context.WithValue(ctx, transportKey{}, t)
}

// TransportOf is r's transport (zero: direct).
func TransportOf(r *http.Request) Transport {
	t, _ := r.Context().Value(transportKey{}).(Transport)
	return t
}

// Refusal is why the transport gate refused a credential.
type Refusal string

const (
	RefuseNone     Refusal = ""
	RefuseRequired Refusal = "required" // an app credential off the tunnel while the workspace requires it
	RefuseBound    Refusal = "bound"    // a tunnel-bound credential off its tunnel
	RefuseDevice   Refusal = "device"   // on a device's tunnel, a credential that isn't that device's
)

// SetTunnelPolicy installs what says whether the workspace requires the
// tunnel for app traffic (the app-tunnel service; nil: never).
func (a *Auth) SetTunnelPolicy(required func() bool) {
	a.mu.Lock()
	a.tunnelRequired = required
	a.mu.Unlock()
}

// TunnelRequired reports the workspace refusing app traffic off the tunnel.
func (a *Auth) TunnelRequired() bool {
	a.mu.RLock()
	f := a.tunnelRequired
	a.mu.RUnlock()
	return f != nil && f()
}

// Authenticate is FromRequest with the transport gate's reason when it
// refused an otherwise valid credential.
func (a *Auth) Authenticate(r *http.Request) (Principal, bool, Refusal) {
	p, ok := a.fromRequest(r)
	if !ok {
		return Principal{}, false, RefuseNone
	}
	if why := a.principalRefusal(TransportOf(r), p); why != RefuseNone {
		return Principal{}, false, why
	}
	a.noteAuthed(r) // any real auth warms the source (IP, or device) for the /c/ gate
	return p, true, RefuseNone
}

// principalRefusal applies the gate to a principal: one without a
// generation (an instance or terminal token) is never a device's.
func (a *Auth) principalRefusal(t Transport, p Principal) Refusal {
	if p.Gen == "" {
		if t.Tunnel {
			return RefuseDevice
		}
		return RefuseNone
	}
	return a.GenRefusal(t, p.Gen, p.UserID)
}

// GenRefusal applies the gate to a credential generation gen of user uid
// ("" = the owner) arriving on transport t — what every credential derived
// from a login (a path ticket, an asset token, a tile-origin cookie)
// checks.
func (a *Auth) GenRefusal(t Transport, gen, uid string) Refusal {
	var s session
	found := false
	if h, ok := strings.CutPrefix(gen, "s."); ok {
		a.mu.RLock()
		if ps := a.genSessionLocked(h); ps != nil {
			s, found = *ps, true
		}
		a.mu.RUnlock()
	}
	if t.Tunnel {
		switch {
		case found && t.DeviceID != "" && s.deviceID == t.DeviceID && s.userID == t.UserID:
			return RefuseNone
		case strings.HasPrefix(gen, "u.") && uid != "" && uid == t.UserID:
			return RefuseNone
		}
		return RefuseDevice
	}
	if !found {
		return RefuseNone // the owner's, a user generation's: never bound
	}
	if s.tunnel {
		return RefuseBound
	}
	if (s.bearer || s.deviceID != "") && a.TunnelRequired() {
		return RefuseRequired
	}
	return RefuseNone
}

// RequestRefusal is GenRefusal on r's transport.
func (a *Auth) RequestRefusal(r *http.Request, gen, uid string) Refusal {
	return a.GenRefusal(TransportOf(r), gen, uid)
}

// BearerOpts are NewBearerSessionOpts' options.
type BearerOpts struct {
	DeviceID string    // the enrolled device of a device-key login ("": the app's password / SSO login)
	IP       string    // the client (or ClientKey) the login came from
	NotAfter time.Time // a cap below the usual lifetimes (zero: none)
	Tunnel   bool      // opened over the device's app tunnel: it works only there
}
