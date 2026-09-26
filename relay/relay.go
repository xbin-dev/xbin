// Package relay is xbin's push relay (plans/native.md §14): the one service
// that holds the app publisher's APNs key. Self-hosted xbind instances
// cannot talk to APNs themselves, so they post sealed envelopes here; the
// relay maps an opaque handle to the device token and forwards the envelope
// untouched inside a generic alert ("New activity", mutable-content) that
// the app's Notification Service Extension decrypts on the device. The relay
// never sees notification content — only handles, tokens and ciphertext.
//
// HTTP API (relay/README.md):
//
//	POST   /v1/handles            {apnsToken, topic, env} → {handle}
//	PUT    /v1/handles/{handle}   {apnsToken, topic, env} → {handle}
//	DELETE /v1/handles/{handle}   → 204
//	POST   /v1/workspaces         → {workspaceId, key}
//	GET    /v1/workspace          Authorization: Bearer <key> → {workspaceId}
//	POST   /v1/push               Authorization: Bearer <key>
//	                              {handle, envelope, collapseId?, priority?}
//	GET    /healthz
//
// Errors are {"error": "<text>", "code": "<code>"}; the codes (Err*) are
// the contract, the text is for people.
//
// Standard library only.
package relay

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Config configures a relay Server.
type Config struct {
	// StatePath is the state snapshot; its journal is StatePath + ".log"
	// ("" = memory only, tests).
	StatePath string
	// Topics are the APNs topics (app bundle ids) handles may name. Empty
	// allows any — only for development relays.
	Topics []string
	// APNs sends the notifications; nil makes /v1/push answer 503.
	APNs *APNs
	// Rate limits. WorkspaceRate and HandleRate bound pushes per workspace
	// and per handle — a workspace's budget is charged only for pushes to a
	// handle it may use, after the handle's own limit; RefusedPushRate
	// bounds, per workspace, pushes to handles that are unknown or another
	// workspace's (so they neither drain the delivery budget nor probe
	// without bound); NewWorkspaceRate and NewHandleRate bound
	// registrations per client (an IPv4 address; an IPv6 /64 for handles,
	// a /48 for workspaces); AllNewWorkspacesRate bounds workspace
	// registrations from everyone together. Zero values take the defaults
	// below.
	WorkspaceRate, HandleRate, RefusedPushRate, NewWorkspaceRate, NewHandleRate, AllNewWorkspacesRate Rate
	// TrustProxy takes the client IP from the last X-Forwarded-For hop
	// (set it only behind a reverse proxy that appends one).
	TrustProxy bool
	// VerifyTokens checks each device token with APNs before storing a
	// handle (a background notification, which the app ignores): a made-up
	// token is refused instead of filling the state. Needs APNs.
	VerifyTokens bool
	// Capacity and retention (zero = the Default* values): at most
	// MaxHandles handles and MaxWorkspaces workspaces (then 503 "full");
	// handles no workspace pushed to within UnboundHandleTTL, handles idle
	// for IdleHandleTTL and workspace keys never used within
	// UnusedWorkspaceTTL are deleted.
	MaxHandles, MaxWorkspaces                           int
	UnboundHandleTTL, IdleHandleTTL, UnusedWorkspaceTTL time.Duration
	// Expiry is how long APNs keeps an undelivered notification (default 24h).
	Expiry time.Duration
	Now    func() time.Time
	Log    *slog.Logger
}

// Default rates.
var (
	DefaultWorkspaceRate    = Rate{PerHour: 3600, Burst: 120}
	DefaultHandleRate       = Rate{PerHour: 600, Burst: 30}
	DefaultRefusedPushRate  = Rate{PerHour: 600, Burst: 60}
	DefaultNewWorkspaceRate = Rate{PerHour: 10, Burst: 3}
	DefaultNewHandleRate    = Rate{PerHour: 360, Burst: 60} // generous: carrier NAT puts many phones behind one IPv4
	// DefaultAllNewWorkspacesRate is a backstop: far more opt-ins than a
	// public relay sees, far fewer than a flood from many prefixes.
	DefaultAllNewWorkspacesRate = Rate{PerHour: 600, Burst: 60}
)

// Error codes (the "code" of an error body).
const (
	ErrBadRequest      = "bad_request"      // 400: malformed; retrying cannot help
	ErrBadKey          = "bad_key"          // 401: unknown workspace key
	ErrHandleBound     = "handle_bound"     // 403: the handle belongs to another workspace
	ErrHandleUnknown   = "handle_unknown"   // 404: no such handle (deleted, expired, or never here)
	ErrHandleGone      = "handle_gone"      // 410: APNs says the device token is dead
	ErrBadToken        = "bad_token"        // 400: APNs refused the device token at registration
	ErrAPNsRefused     = "apns_refused"     // 400: APNs refused the notification
	ErrRateLimited     = "rate_limited"     // 429, with Retry-After
	ErrAPNsUnavailable = "apns_unavailable" // 502
	ErrNoAPNs          = "no_apns"          // 503: this relay has no APNs key
	ErrFull            = "full"             // 503: the relay is at capacity
	ErrInternal        = "internal"         // 500
)

// Limits on what a push may carry. APNs takes 4 KiB per notification; the
// envelope must leave room for the aps dictionary.
const (
	maxBody       = 8 << 10
	maxEnvelopeCT = 3200 // base64url characters of ciphertext||tag
	maxAPNsBody   = 4096
	maxCollapseID = 64
)

// Server is the relay's HTTP handler.
type Server struct {
	cfg   Config
	st    *store
	mux   *http.ServeMux
	wsLim *limiter
	hLim  *limiter
	rfLim *limiter
	nwLim *limiter
	nhLim *limiter
	awLim *limiter
}

// New opens the state and builds the handler.
func New(cfg Config) (*Server, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Expiry <= 0 {
		cfg.Expiry = 24 * time.Hour
	}
	def := func(r, d Rate) Rate {
		if r == (Rate{}) {
			return d
		}
		return r
	}
	orInt := func(v, d int) int {
		if v <= 0 {
			return d
		}
		return v
	}
	orDur := func(v, d time.Duration) time.Duration {
		if v <= 0 {
			return d
		}
		return v
	}
	st, err := openStore(cfg.StatePath, storeLimits{
		maxHandles: orInt(cfg.MaxHandles, DefaultMaxHandles), maxWorkspaces: orInt(cfg.MaxWorkspaces, DefaultMaxWorkspaces),
		unboundTTL: orDur(cfg.UnboundHandleTTL, DefaultUnboundHandleTTL), idleTTL: orDur(cfg.IdleHandleTTL, DefaultIdleHandleTTL),
		unusedWorkspace: orDur(cfg.UnusedWorkspaceTTL, DefaultUnusedWorkspaceTTL)})
	if err != nil {
		return nil, err
	}
	if st.j != nil {
		st.j.onErr = func(err error) { cfg.Log.Error("relay: compacting the state", "err", err) }
	}
	s := &Server{cfg: cfg, st: st, mux: http.NewServeMux(),
		wsLim: newLimiter(def(cfg.WorkspaceRate, DefaultWorkspaceRate), cfg.Now),
		hLim:  newLimiter(def(cfg.HandleRate, DefaultHandleRate), cfg.Now),
		rfLim: newLimiter(def(cfg.RefusedPushRate, DefaultRefusedPushRate), cfg.Now),
		nwLim: newLimiter(def(cfg.NewWorkspaceRate, DefaultNewWorkspaceRate), cfg.Now),
		nhLim: newLimiter(def(cfg.NewHandleRate, DefaultNewHandleRate), cfg.Now),
		awLim: newLimiter(def(cfg.AllNewWorkspacesRate, DefaultAllNewWorkspacesRate), cfg.Now),
	}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	s.mux.HandleFunc("POST /v1/handles", s.handleNewHandle)
	s.mux.HandleFunc("PUT /v1/handles/{handle}", s.handleRepoint)
	s.mux.HandleFunc("DELETE /v1/handles/{handle}", s.handleDeleteHandle)
	s.mux.HandleFunc("POST /v1/workspaces", s.handleNewWorkspace)
	s.mux.HandleFunc("GET /v1/workspace", s.handleWorkspace)
	s.mux.HandleFunc("POST /v1/push", s.handlePush)
	return s, nil
}

// Close writes the state out (usage timestamps included) and stops
// accepting changes. Call it after the HTTP server has shut down.
func (s *Server) Close() error { return s.st.close() }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	s.mux.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

func tooMany(w http.ResponseWriter, wait time.Duration, what string) {
	secs := int(wait/time.Second) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeErr(w, http.StatusTooManyRequests, ErrRateLimited, what+" rate limit")
}

// clientKey is what the registration limits count per: the client's IPv4
// address, or its IPv6 prefix of v6bits (a /64 is one subscriber's LAN —
// per-address buckets would give a single host 2^64 of them).
func (s *Server) clientKey(r *http.Request, v6bits int) string {
	raw := ""
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			raw = strings.TrimSpace(parts[len(parts)-1])
		}
	}
	if raw == "" {
		raw = r.RemoteAddr
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			raw = host
		}
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return raw
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is4() {
		return addr.String()
	}
	p, _ := addr.Prefix(v6bits)
	return p.String()
}

type handleReq struct {
	APNsToken string `json:"apnsToken"`
	Topic     string `json:"topic"`
	Env       string `json:"env"`
}

// validate normalises the token (lowercase hex) and env.
func (s *Server) validate(q *handleReq) string {
	q.APNsToken = strings.ToLower(strings.TrimSpace(q.APNsToken))
	if len(q.APNsToken) < 64 || len(q.APNsToken) > 200 {
		return "apnsToken: hex, 64–200 characters"
	}
	if _, err := hex.DecodeString(q.APNsToken); err != nil {
		return "apnsToken: hex, 64–200 characters"
	}
	if q.Topic == "" || len(q.Topic) > 200 || (len(s.cfg.Topics) > 0 && !slices.Contains(s.cfg.Topics, q.Topic)) {
		return "topic: not an app this relay serves"
	}
	switch q.Env {
	case "production":
	case "development", "sandbox":
		q.Env = "development"
	default:
		return "env: production | development"
	}
	return ""
}

func validHandle(h string) bool {
	b, err := base64.RawURLEncoding.DecodeString(h)
	return err == nil && len(b) == 16
}

func (s *Server) handleNewHandle(w http.ResponseWriter, r *http.Request) {
	if ok, wait := s.nhLim.allow(s.clientKey(r, 64)); !ok {
		tooMany(w, wait, "registration")
		return
	}
	var q handleReq
	if json.NewDecoder(r.Body).Decode(&q) != nil {
		writeErr(w, http.StatusBadRequest, ErrBadRequest, "need {apnsToken, topic, env}")
		return
	}
	if msg := s.validate(&q); msg != "" {
		writeErr(w, http.StatusBadRequest, ErrBadRequest, msg)
		return
	}
	if !s.verifyToken(w, r, q) {
		return
	}
	h, err := s.st.newHandle(q.APNsToken, q.Topic, q.Env, s.cfg.Now())
	switch {
	case errors.Is(err, errFull):
		s.cfg.Log.Warn("relay: at capacity, handle refused")
		writeErr(w, http.StatusServiceUnavailable, ErrFull, "the relay is at capacity; try later")
	case err != nil:
		s.cfg.Log.Error("relay: save state", "err", err)
		writeErr(w, http.StatusInternalServerError, ErrInternal, "could not store the handle")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"handle": h})
	}
}

// verifyToken asks APNs whether a device token is real (Config.VerifyTokens):
// a background notification with no content. False = answered.
func (s *Server) verifyToken(w http.ResponseWriter, r *http.Request, q handleReq) bool {
	if !s.cfg.VerifyTokens || s.cfg.APNs == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	_, err := s.cfg.APNs.Send(ctx, Notification{Token: q.APNsToken, Topic: q.Topic, Env: q.Env, Background: true,
		Payload: []byte(`{"aps":{"content-available":1}}`), Priority: 5, Expiration: s.cfg.Now().Add(time.Hour)})
	var ae *APNsError
	switch {
	case err == nil:
		return true
	case errors.As(err, &ae) && (ae.Dead() || ae.Status == http.StatusBadRequest):
		writeErr(w, http.StatusBadRequest, ErrBadToken, "APNs refused the device token ("+ae.Reason+")")
	case errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests:
		tooMany(w, max(ae.RetryAfter, time.Second), "APNs")
	default:
		s.cfg.Log.Warn("relay: APNs token check failed", "err", err)
		writeErr(w, http.StatusBadGateway, ErrAPNsUnavailable, "could not check the device token with APNs; try later")
	}
	return false
}

func (s *Server) handleRepoint(w http.ResponseWriter, r *http.Request) {
	if ok, wait := s.nhLim.allow(s.clientKey(r, 64)); !ok {
		tooMany(w, wait, "registration")
		return
	}
	id := r.PathValue("handle")
	var q handleReq
	if !validHandle(id) || json.NewDecoder(r.Body).Decode(&q) != nil {
		writeErr(w, http.StatusBadRequest, ErrBadRequest, "need a handle and {apnsToken, topic, env}")
		return
	}
	if msg := s.validate(&q); msg != "" {
		writeErr(w, http.StatusBadRequest, ErrBadRequest, msg)
		return
	}
	if !s.verifyToken(w, r, q) {
		return
	}
	switch err := s.st.repoint(id, q.APNsToken, q.Topic, q.Env); {
	case errors.Is(err, errNoHandle):
		writeErr(w, http.StatusNotFound, ErrHandleUnknown, "unknown handle")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, ErrInternal, "could not store the handle")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"handle": id})
	}
}

func (s *Server) handleDeleteHandle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("handle")
	if !validHandle(id) || !s.st.deleteHandle(id) {
		writeErr(w, http.StatusNotFound, ErrHandleUnknown, "unknown handle")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNewWorkspace(w http.ResponseWriter, r *http.Request) {
	if ok, wait := s.nwLim.allow(s.clientKey(r, 48)); !ok {
		tooMany(w, wait, "workspace registration")
		return
	}
	if ok, wait := s.awLim.allow(""); !ok {
		s.cfg.Log.Warn("relay: the global workspace registration limit is reached")
		tooMany(w, wait, "workspace registration")
		return
	}
	id, key, err := s.st.newWorkspace(s.cfg.Now())
	switch {
	case errors.Is(err, errFull):
		s.cfg.Log.Warn("relay: at capacity, workspace refused")
		writeErr(w, http.StatusServiceUnavailable, ErrFull, "the relay is at capacity; try later")
		return
	case err != nil:
		s.cfg.Log.Error("relay: save state", "err", err)
		writeErr(w, http.StatusInternalServerError, ErrInternal, "could not store the workspace")
		return
	}
	s.cfg.Log.Info("relay: workspace registered", "workspace", id)
	writeJSON(w, http.StatusOK, map[string]string{"workspaceId": id, "key": key})
}

// bearer resolves the workspace key of a request.
func (s *Server) bearer(w http.ResponseWriter, r *http.Request) (string, bool) {
	key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	ws, err := s.st.workspaceFor(strings.TrimSpace(key), s.cfg.Now())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, ErrBadKey, "unknown workspace key")
		return "", false
	}
	return ws, true
}

// handleWorkspace is GET /v1/workspace: which workspace a key is — how
// xbind checks a key an admin gives it (and a use of the key).
func (s *Server) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	if ws, ok := s.bearer(w, r); ok {
		writeJSON(w, http.StatusOK, map[string]string{"workspaceId": ws})
	}
}

// Envelope is the sealed notification as xbind produces it (native/spec/
// push.md). The relay checks its shape and size only — it cannot open it.
type Envelope struct {
	V   int    `json:"v"`
	EPK string `json:"epk"`
	N   string `json:"n"`
	CT  string `json:"ct"`
}

func (e *Envelope) valid() bool {
	dec := base64.RawURLEncoding
	epk, err1 := dec.DecodeString(e.EPK)
	n, err2 := dec.DecodeString(e.N)
	ct, err3 := dec.DecodeString(e.CT)
	return e.V == 1 && err1 == nil && err2 == nil && err3 == nil &&
		len(epk) == 32 && len(n) == 12 && len(ct) > 16 && len(e.CT) <= maxEnvelopeCT
}

type pushReq struct {
	Handle     string    `json:"handle"`
	Envelope   *Envelope `json:"envelope"`
	CollapseID string    `json:"collapseId"`
	Priority   int       `json:"priority"`
}

// apnsBody is the alert the device receives: generic text, mutable-content
// so the extension runs, and the envelope under "xbin".
func apnsBody(env *Envelope) ([]byte, error) {
	return json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert":           "New activity",
			"mutable-content": 1,
			"sound":           "default",
		},
		"xbin": env,
	})
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	now := s.cfg.Now()
	ws, ok := s.bearer(w, r)
	if !ok {
		return
	}
	var q pushReq
	if json.NewDecoder(r.Body).Decode(&q) != nil || q.Envelope == nil {
		writeErr(w, http.StatusBadRequest, ErrBadRequest, "need {handle, envelope, collapseId?, priority?}")
		return
	}
	if !q.Envelope.valid() {
		writeErr(w, http.StatusBadRequest, ErrBadRequest, "envelope: {v:1, epk, n, ct} (base64url; ct at most 3200 characters)")
		return
	}
	if len(q.CollapseID) > maxCollapseID {
		writeErr(w, http.StatusBadRequest, ErrBadRequest, "collapseId: at most 64 bytes")
		return
	}
	if q.Priority != 0 && q.Priority != 5 && q.Priority != 10 {
		writeErr(w, http.StatusBadRequest, ErrBadRequest, "priority: 10 or 5")
		return
	}
	// Resolve the handle before charging the workspace: pushes to a handle
	// it can't use (made up, deleted, another workspace's) spend a budget
	// of their own, not the one its deliveries live on — a registration an
	// attacker controls on the workspace's side must not starve the rest.
	refused := func(status int, code, msg string) {
		if ok, wait := s.rfLim.allow(ws); !ok {
			tooMany(w, wait, "refused pushes")
			return
		}
		writeErr(w, status, code, msg)
	}
	if !validHandle(q.Handle) {
		refused(http.StatusNotFound, ErrHandleUnknown, "unknown handle")
		return
	}
	h, err := s.st.target(q.Handle, ws, now)
	switch {
	case errors.Is(err, errNoHandle):
		refused(http.StatusNotFound, ErrHandleUnknown, "unknown handle")
		return
	case errors.Is(err, errHandleBound):
		refused(http.StatusForbidden, ErrHandleBound, "handle belongs to another workspace")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, ErrInternal, "state error")
		return
	}
	if ok, wait := s.hLim.allow(q.Handle); !ok {
		tooMany(w, wait, "handle")
		return
	}
	if ok, wait := s.wsLim.allow(ws); !ok {
		tooMany(w, wait, "workspace")
		return
	}
	if s.cfg.APNs == nil {
		writeErr(w, http.StatusServiceUnavailable, ErrNoAPNs, "this relay has no APNs key configured")
		return
	}
	body, err := apnsBody(q.Envelope)
	if err != nil || len(body) > maxAPNsBody {
		writeErr(w, http.StatusBadRequest, ErrBadRequest, "envelope too large")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	id, err := s.cfg.APNs.Send(ctx, Notification{Token: h.Token, Topic: h.Topic, Env: h.Env, Payload: body,
		CollapseID: q.CollapseID, Priority: q.Priority, Expiration: now.Add(s.cfg.Expiry)})
	var ae *APNsError
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "apnsId": id})
	case errors.As(err, &ae) && ae.Dead():
		n := s.st.dropToken(h.Token)
		s.cfg.Log.Info("relay: device token gone", "reason", ae.Reason, "handlesDropped", n)
		writeErr(w, http.StatusGone, ErrHandleGone, "handle gone ("+ae.Reason+")")
	case errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests:
		tooMany(w, max(ae.RetryAfter, time.Second), "APNs")
	case errors.As(err, &ae) && ae.Status == http.StatusBadRequest:
		writeErr(w, http.StatusBadRequest, ErrAPNsRefused, "APNs refused the notification: "+ae.Reason)
	default:
		s.cfg.Log.Warn("relay: APNs send failed", "err", err)
		writeErr(w, http.StatusBadGateway, ErrAPNsUnavailable, "APNs unavailable")
	}
}

// Counts reports how many handles and workspaces the relay holds.
func (s *Server) Counts() (handles, workspaces int) { return s.st.counts() }
