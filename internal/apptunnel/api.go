package apptunnel

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/xbin-dev/xbin/internal/apptunnel/sidecar"
	"github.com/xbin-dev/xbin/internal/apptunnel/wire"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// docs is the page every answer names.
const docs = "/docs/app-tunnel.md"

// Register mounts the routes (docs/protocol.md §App tunnel) and makes the
// server's enroll-code and sign-in methods tunnel-aware.
func (s *Service) Register(srv *server.Server) {
	srv.RegisterAPI("GET /app-tunnel", s.apiGet)
	srv.RegisterAPI("PUT /app-tunnel", s.apiPut)
	srv.RegisterAPI("POST /app-tunnel/relay-tokens", s.apiTokens)
	srv.RegisterAPI("POST /app-tunnel/test", s.apiTest)
	srv.RegisterAPI("GET /app-tunnel/client", s.apiClient)
	srv.RegisterAPI("GET /devices/pairing/{id}", s.apiPairing)
	srv.RegisterAPI("POST /devices/pairing/{id}/confirm", s.apiConfirm)
	srv.RegisterAPI("POST /devices/{id}/tunnel", s.apiUpgrade)
	srv.AppTunnel = s
}

func (s *Service) admin(w http.ResponseWriter, r *http.Request) bool {
	p := auth.PrincipalOf(r)
	if s.opt.IsAdmin == nil || !s.opt.IsAdmin(p) || p.Component != "" {
		server.WriteError(w, http.StatusForbidden, "the app tunnel's settings are a workspace admin's", docs)
		return false
	}
	if p.ReadOnly() && r.Method != http.MethodGet {
		server.WriteError(w, http.StatusForbidden, "viewing as someone changes nothing", docs)
		return false
	}
	return true
}

// View is GET /app-tunnel.
type View struct {
	Enabled        bool            `json:"enabled"`
	TunnelOnly     bool            `json:"tunnelOnly"`
	Relay          RelayView       `json:"relay"`
	Env            string          `json:"env,omitempty"` // XBIN_IROH_RELAY, when it sets the relay
	State          sidecar.State   `json:"state"`
	Confined       bool            `json:"confined"`
	Exec           string          `json:"exec,omitempty"`
	Restarts       int             `json:"restarts"`
	Error          string          `json:"error,omitempty"`
	EndpointID     string          `json:"endpointId,omitempty"`
	HomeRelay      string          `json:"homeRelay,omitempty"`
	RelayConnected bool            `json:"relayConnected"`
	Version        string          `json:"version,omitempty"`
	Devices        DeviceCounts    `json:"devices"`
	Conns          []wire.ConnInfo `json:"conns"`
	LockedOut      *int            `json:"lockedOut,omitempty"` // PUT turning tunnelOnly on: the classic devices it locks out
	Token          string          `json:"token,omitempty"`     // generate-next only: the new token, shown once
}

// RelayView shows a relay with its tokens as hints.
type RelayView struct {
	Mode      string    `json:"mode"`
	URL       string    `json:"url,omitempty"`
	TokenHint string    `json:"tokenHint,omitempty"`
	Next      *SlotView `json:"next,omitempty"`
}

// SlotView is the staged next relay change.
type SlotView struct {
	URL       string `json:"url,omitempty"`
	TokenHint string `json:"tokenHint,omitempty"`
}

// DeviceCounts: every enrolled device, those paired with the tunnel, the
// others, those with a live tunnel connection, and the paired ones that
// fetched the current client settings.
type DeviceCounts struct {
	Total        int `json:"total"`
	TunnelPaired int `json:"tunnelPaired"`
	Classic      int `json:"classic"`
	Connected    int `json:"connected"`
	Synced       int `json:"synced"`
}

// view builds GET /app-tunnel.
func (s *Service) view() View {
	st := s.runner.Status()
	s.mu.Lock()
	defer s.mu.Unlock()
	v := View{Enabled: s.cfg.Enabled, TunnelOnly: s.cfg.TunnelOnly, State: st.State, Confined: st.Confined, Exec: st.Exec,
		Restarts: st.Restarts, Error: st.Error, HomeRelay: st.HomeRelay, RelayConnected: st.RelayConnected, Version: st.Version,
		Conns: st.Conns, Relay: RelayView{Mode: s.cfg.Relay.Mode, URL: s.cfg.Relay.URL, TokenHint: hint(s.cfg.Relay.Token)}}
	if v.Conns == nil {
		v.Conns = []wire.ConnInfo{}
	}
	if n := s.cfg.Next; n != nil {
		v.Relay.Next = &SlotView{URL: n.URL, TokenHint: hint(n.Token)}
	}
	if s.envSet {
		v.Env = s.envRelay.Mode
		if s.envRelay.URL != "" {
			v.Env = s.envRelay.URL
		}
	}
	if !s.cfg.Enabled && st.State != sidecar.StateUnavailable {
		v.State = sidecar.StateDisabled
	}
	if s.keys != nil {
		v.EndpointID = s.keys.endpointHex()
	}
	v.Devices = s.countsLocked()
	return v
}

func (s *Service) countsLocked() DeviceCounts {
	var c DeviceCounts
	if s.opt.Users == nil {
		return c
	}
	for _, u := range s.opt.Users.List() {
		for _, d := range s.opt.Users.Devices(u.ID) {
			c.Total++
			if d.TunnelKey == "" {
				continue
			}
			c.TunnelPaired++
			if len(s.conns[d.ID]) > 0 {
				c.Connected++
			}
			if g, ok := s.synced[d.ID]; ok && g == s.cfg.Gen {
				c.Synced++
			}
		}
	}
	c.Classic = c.Total - c.TunnelPaired
	return c
}

func (s *Service) apiGet(w http.ResponseWriter, r *http.Request) {
	if s.admin(w, r) {
		server.WriteJSON(w, http.StatusOK, s.view())
	}
}

// apiPut — PUT /app-tunnel {enabled?, relay?: {mode, url?}, tunnelOnly?}.
func (s *Service) apiPut(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
		Relay   *struct {
			Mode string `json:"mode"`
			URL  string `json:"url"`
		} `json:"relay"`
		TunnelOnly *bool `json:"tunnelOnly"`
	}
	if err := server.DecodeJSON(r, &body); err != nil {
		server.WriteError(w, http.StatusBadRequest, "need {enabled?, relay?: {mode: n0|custom, url?}, tunnelOnly?}", docs)
		return
	}
	s.mu.Lock()
	next := s.cfg
	restart := false
	if body.Relay != nil {
		if s.envSet {
			s.mu.Unlock()
			server.WriteError(w, http.StatusConflict, "XBIN_IROH_RELAY sets this xbind's relay", docs)
			return
		}
		nr := Relay{Mode: body.Relay.Mode, URL: body.Relay.URL}
		if nr.Mode == RelayCustom && nr.URL == s.cfg.Relay.URL {
			nr.Token = s.cfg.Relay.Token
		}
		if nr != next.Relay {
			next.Relay, next.Gen, restart = nr, next.Gen+1, true
		}
	}
	if body.Enabled != nil {
		restart = restart || *body.Enabled != next.Enabled
		next.Enabled = *body.Enabled
		if !next.Enabled {
			next.TunnelOnly = false // like SSO-only: the switch goes with what it needs
		}
	}
	turningOn := false
	if body.TunnelOnly != nil {
		turningOn = *body.TunnelOnly && !next.TunnelOnly
		next.TunnelOnly = *body.TunnelOnly
	}
	if err := next.validate(); err != nil {
		s.mu.Unlock()
		server.WriteError(w, http.StatusBadRequest, err.Error(), docs)
		return
	}
	if err := saveConfig(s.opt.Dir, next); err != nil {
		s.mu.Unlock()
		server.WriteError(w, http.StatusInternalServerError, err.Error(), docs)
		return
	}
	s.cfg = next
	var startErr error
	if restart {
		if next.Enabled {
			startErr = s.startLocked() // running: a second Start restarts it with the new settings
		} else {
			s.stopLocked()
		}
	}
	s.mu.Unlock()
	s.changed()
	v := s.view()
	if turningOn {
		n := v.Devices.Classic
		v.LockedOut = &n
	}
	if startErr != nil && v.Error == "" {
		v.Error = startErr.Error()
	}
	server.WriteJSON(w, http.StatusOK, v)
}

// apiTokens — POST /app-tunnel/relay-tokens {action, token?, url?}.
func (s *Service) apiTokens(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	var body struct {
		Action string `json:"action"`
		Token  string `json:"token"`
		URL    string `json:"url"`
	}
	if err := server.DecodeJSON(r, &body); err != nil {
		server.WriteError(w, http.StatusBadRequest, "need {action: generate-next|set-next|promote|drop-next|set-current|clear, token?}", docs)
		return
	}
	s.mu.Lock()
	if s.cfg.Relay.Mode != RelayCustom {
		s.mu.Unlock()
		server.WriteError(w, http.StatusConflict, "n0's relays take no token — set a custom relay first", docs)
		return
	}
	next, restart, shown := s.cfg, false, ""
	slot := Slot{}
	if next.Next != nil {
		slot = *next.Next
	}
	switch body.Action {
	case "generate-next":
		shown = newToken()
		slot.Token = shown
		next.Next = &slot
	case "set-next":
		if body.Token != "" {
			slot.Token = body.Token
		}
		if body.URL != "" {
			slot.URL = body.URL
		}
		next.Next = &slot
	case "promote":
		if next.Next == nil {
			s.mu.Unlock()
			server.WriteError(w, http.StatusConflict, "nothing is staged to promote", docs)
			return
		}
		if slot.URL != "" {
			next.Relay.URL = slot.URL
		}
		if slot.Token != "" {
			next.Relay.Token = slot.Token
		}
		next.Next, restart = nil, true
	case "drop-next":
		next.Next = nil
	case "set-current":
		next.Relay.Token, restart = body.Token, true
	case "clear":
		next.Relay.Token, restart = "", true
	default:
		s.mu.Unlock()
		server.WriteError(w, http.StatusBadRequest, "action: generate-next | set-next | promote | drop-next | set-current | clear", docs)
		return
	}
	next.Gen++
	if err := next.validate(); err != nil {
		s.mu.Unlock()
		server.WriteError(w, http.StatusBadRequest, err.Error(), docs)
		return
	}
	if err := saveConfig(s.opt.Dir, next); err != nil {
		s.mu.Unlock()
		server.WriteError(w, http.StatusInternalServerError, err.Error(), docs)
		return
	}
	s.cfg = next
	if restart && next.Enabled {
		_ = s.startLocked() // a second Start restarts the sidecar with the token
	}
	s.mu.Unlock()
	s.changed()
	v := s.view()
	v.Token = shown
	w.Header().Set("Cache-Control", "no-store")
	server.WriteJSON(w, http.StatusOK, v)
}

// apiTest — POST /app-tunnel/test: the sidecar dials itself through its
// relay from a throwaway endpoint.
func (s *Service) apiTest(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ev, err := s.runner.SelfTest(ctx)
	out := map[string]any{"reachable": err == nil && ev.Reachable}
	if ev.Path != "" {
		out["path"] = ev.Path
	}
	if ev.RTTMs != 0 {
		out["rttMs"] = ev.RTTMs
	}
	switch {
	case err != nil:
		out["error"] = err.Error()
	case ev.Error != "":
		out["error"] = ev.Error
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// ClientConfig is GET /app-tunnel/client (spec §7).
type ClientConfig struct {
	EndpointID string      `json:"endpointId"`
	Ticket     string      `json:"ticket"`
	ServerKey  string      `json:"serverKey"`
	Relay      ClientRelay `json:"relay"`
	Required   bool        `json:"required"`
}

// ClientRelay is the relay a device uses, and the staged next one.
type ClientRelay struct {
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
	Next  *Slot  `json:"next,omitempty"`
}

// clientConfig answers a device what it needs, recording that it synced.
func (s *Service) clientConfig(deviceID string) (ClientConfig, bool) {
	st := s.runner.Status()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.Enabled || s.keys == nil {
		return ClientConfig{}, false
	}
	r := s.relayLocked()
	c := ClientConfig{EndpointID: s.keys.endpointHex(), Ticket: st.Ticket, ServerKey: s.keys.serverKey(), Required: s.cfg.TunnelOnly}
	if r.Mode == RelayCustom {
		c.Relay = ClientRelay{URL: r.URL, Token: r.Token}
	}
	if n := s.cfg.Next; n != nil && !s.envSet {
		c.Relay.Next = &Slot{URL: n.URL, Token: n.Token}
	}
	s.synced[deviceID] = s.cfg.Gen
	return c, true
}

// apiClient — GET /app-tunnel/client: the device's own session, over its
// tunnel.
func (s *Service) apiClient(w http.ResponseWriter, r *http.Request) {
	p, t := auth.PrincipalOf(r), auth.TransportOf(r)
	if p.Via != "device" || !t.Tunnel || p.DeviceID != t.DeviceID {
		server.WriteError(w, http.StatusForbidden, "a device's own sign-in, over its app tunnel", docs)
		return
	}
	c, ok := s.clientConfig(p.DeviceID)
	if !ok {
		server.WriteError(w, http.StatusServiceUnavailable, ErrTunnelOff.Error(), docs)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	server.WriteJSON(w, http.StatusOK, c)
}

// apiPairing — GET /devices/pairing/{id}: the minting session only.
func (s *Service) apiPairing(w http.ResponseWriter, r *http.Request) {
	pr := s.pairingFor(r.PathValue("id"), auth.PrincipalOf(r))
	if pr == nil {
		server.WriteError(w, http.StatusNotFound, "no such pairing (it expired, or another sign-in started it)", docs)
		return
	}
	s.mu.Lock()
	v := s.viewLocked(pr)
	s.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	server.WriteJSON(w, http.StatusOK, v)
}

// apiConfirm — POST /devices/pairing/{id}/confirm {match, replace?}.
func (s *Service) apiConfirm(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if p.ReadOnly() {
		server.WriteError(w, http.StatusForbidden, "viewing as someone pairs nothing", docs)
		return
	}
	pr := s.pairingFor(r.PathValue("id"), p)
	if pr == nil {
		server.WriteError(w, http.StatusNotFound, "no such pairing (it expired, or another sign-in started it)", docs)
		return
	}
	var body struct {
		Match   *bool  `json:"match"`
		Replace string `json:"replace"`
	}
	if err := server.DecodeJSON(r, &body); err != nil || body.Match == nil {
		server.WriteError(w, http.StatusBadRequest, "need {match: true|false, replace?: <device id>}", docs)
		return
	}
	v, err := s.Confirm(r.Context(), pr, *body.Match, body.Replace)
	var le *users.DeviceLimitError
	switch {
	case errors.As(err, &le):
		server.WriteJSON(w, http.StatusConflict, map[string]any{"error": le.Error(), "limit": le.Limit, "count": le.Count, "docs": docs})
	case errors.Is(err, ErrPairingGone):
		server.WriteJSON(w, http.StatusConflict, map[string]any{"error": ErrPairingGone.Error(), "state": v.State, "docs": docs})
	case err != nil:
		server.WriteError(w, http.StatusInternalServerError, err.Error(), docs)
	default:
		out := map[string]any{"state": v.State}
		if v.DeviceID != "" {
			out["deviceId"] = v.DeviceID
		}
		if v.Replaced != "" {
			out["replaced"] = v.Replaced
		}
		if v.Reason != "" {
			out["reason"] = v.Reason
		}
		server.WriteJSON(w, http.StatusOK, out)
	}
}

// apiUpgrade — POST /devices/{id}/tunnel {noiseKey, endpoint} (spec §9):
// an enrolled device registers its tunnel keys — its own session, signed in
// within the last 10 minutes, over a direct connection.
func (s *Service) apiUpgrade(w http.ResponseWriter, r *http.Request) {
	p, id := auth.PrincipalOf(r), r.PathValue("id")
	if p.Via != "device" || p.DeviceID != id || p.ReadOnly() {
		server.WriteError(w, http.StatusForbidden, "only the device itself registers its tunnel keys", docs)
		return
	}
	if auth.TransportOf(r).Tunnel {
		server.WriteError(w, http.StatusForbidden, "this device is already on the tunnel", docs)
		return
	}
	if s.Required() {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "this workspace pairs devices only through the tunnel's QR code — pair the device again", "tunnel": "required", "docs": docs})
		return
	}
	if at, ok := s.opt.Auth.LoginTime(p); !ok || time.Since(at) > auth.EnrollFreshLogin {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "sign in with the device key again, then register within 10 minutes", "stepUp": "signin", "docs": docs})
		return
	}
	var body struct {
		NoiseKey string `json:"noiseKey"`
		Endpoint string `json:"endpoint"`
	}
	if err := server.DecodeJSON(r, &body); err != nil {
		server.WriteError(w, http.StatusBadRequest, "need {noiseKey, endpoint}", docs)
		return
	}
	nk, err1 := base64.RawURLEncoding.DecodeString(body.NoiseKey)
	ep, err2 := hex.DecodeString(body.Endpoint)
	if err1 != nil || len(nk) != 32 || err2 != nil || len(ep) != 32 || hex.EncodeToString(ep) != body.Endpoint {
		server.WriteError(w, http.StatusBadRequest, "noiseKey: 32 bytes, base64url without padding; endpoint: 64 lowercase hex", docs)
		return
	}
	if !s.Enabled() {
		server.WriteError(w, http.StatusConflict, ErrTunnelOff.Error(), docs)
		return
	}
	if err := s.opt.Users.SetDeviceTunnel(id, body.NoiseKey, body.Endpoint); err != nil {
		server.WriteError(w, http.StatusConflict, err.Error(), docs)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), ackTimeout)
	defer cancel()
	if err := s.Resync(ctx); err != nil {
		server.WriteError(w, http.StatusServiceUnavailable, "the tunnel didn't admit the device: "+err.Error(), docs)
		return
	}
	c, ok := s.clientConfig(id)
	if !ok {
		server.WriteError(w, http.StatusServiceUnavailable, ErrTunnelOff.Error(), docs)
		return
	}
	s.usersChanged()
	s.changed()
	w.Header().Set("Cache-Control", "no-store")
	server.WriteJSON(w, http.StatusOK, c)
}
