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
//	POST   /v1/push               Authorization: Bearer <key>
//	                              {handle, envelope, collapseId?, priority?}
//	GET    /healthz
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
	"slices"
	"strconv"
	"strings"
	"time"
)

// Config configures a relay Server.
type Config struct {
	// StatePath is the JSON state file ("" = memory only, tests).
	StatePath string
	// Topics are the APNs topics (app bundle ids) handles may name. Empty
	// allows any — only for development relays.
	Topics []string
	// APNs sends the notifications; nil makes /v1/push answer 503.
	APNs *APNs
	// Rate limits. WorkspaceRate and HandleRate bound pushes per workspace
	// and per handle; NewWorkspaceRate and NewHandleRate bound
	// registrations per client IP. Zero values take the defaults below.
	WorkspaceRate, HandleRate, NewWorkspaceRate, NewHandleRate Rate
	// TrustProxy takes the client IP from the last X-Forwarded-For hop
	// (set it only behind a reverse proxy that appends one).
	TrustProxy bool
	// Expiry is how long APNs keeps an undelivered notification (default 24h).
	Expiry time.Duration
	Now    func() time.Time
	Log    *slog.Logger
}

// Default rates.
var (
	DefaultWorkspaceRate    = Rate{PerHour: 3600, Burst: 120}
	DefaultHandleRate       = Rate{PerHour: 600, Burst: 30}
	DefaultNewWorkspaceRate = Rate{PerHour: 10, Burst: 3}
	DefaultNewHandleRate    = Rate{PerHour: 120, Burst: 20}
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
	nwLim *limiter
	nhLim *limiter
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
	st, err := openStore(cfg.StatePath)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, st: st, mux: http.NewServeMux(),
		wsLim: newLimiter(def(cfg.WorkspaceRate, DefaultWorkspaceRate), cfg.Now),
		hLim:  newLimiter(def(cfg.HandleRate, DefaultHandleRate), cfg.Now),
		nwLim: newLimiter(def(cfg.NewWorkspaceRate, DefaultNewWorkspaceRate), cfg.Now),
		nhLim: newLimiter(def(cfg.NewHandleRate, DefaultNewHandleRate), cfg.Now),
	}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	s.mux.HandleFunc("POST /v1/handles", s.handleNewHandle)
	s.mux.HandleFunc("PUT /v1/handles/{handle}", s.handleRepoint)
	s.mux.HandleFunc("DELETE /v1/handles/{handle}", s.handleDeleteHandle)
	s.mux.HandleFunc("POST /v1/workspaces", s.handleNewWorkspace)
	s.mux.HandleFunc("POST /v1/push", s.handlePush)
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	s.mux.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func tooMany(w http.ResponseWriter, wait time.Duration, what string) {
	secs := int(wait/time.Second) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeErr(w, http.StatusTooManyRequests, what+" rate limit")
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
	if ok, wait := s.nhLim.allow(s.clientIP(r)); !ok {
		tooMany(w, wait, "registration")
		return
	}
	var q handleReq
	if json.NewDecoder(r.Body).Decode(&q) != nil {
		writeErr(w, http.StatusBadRequest, "need {apnsToken, topic, env}")
		return
	}
	if msg := s.validate(&q); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	h, err := s.st.newHandle(q.APNsToken, q.Topic, q.Env, s.cfg.Now())
	if err != nil {
		s.cfg.Log.Error("relay: save state", "err", err)
		writeErr(w, http.StatusInternalServerError, "could not store the handle")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"handle": h})
}

func (s *Server) handleRepoint(w http.ResponseWriter, r *http.Request) {
	if ok, wait := s.nhLim.allow(s.clientIP(r)); !ok {
		tooMany(w, wait, "registration")
		return
	}
	id := r.PathValue("handle")
	var q handleReq
	if !validHandle(id) || json.NewDecoder(r.Body).Decode(&q) != nil {
		writeErr(w, http.StatusBadRequest, "need a handle and {apnsToken, topic, env}")
		return
	}
	if msg := s.validate(&q); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	switch err := s.st.repoint(id, q.APNsToken, q.Topic, q.Env); {
	case errors.Is(err, errNoHandle):
		writeErr(w, http.StatusNotFound, "unknown handle")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not store the handle")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"handle": id})
	}
}

func (s *Server) handleDeleteHandle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("handle")
	if !validHandle(id) || !s.st.deleteHandle(id) {
		writeErr(w, http.StatusNotFound, "unknown handle")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNewWorkspace(w http.ResponseWriter, r *http.Request) {
	if ok, wait := s.nwLim.allow(s.clientIP(r)); !ok {
		tooMany(w, wait, "workspace registration")
		return
	}
	id, key, err := s.st.newWorkspace(s.cfg.Now())
	if err != nil {
		s.cfg.Log.Error("relay: save state", "err", err)
		writeErr(w, http.StatusInternalServerError, "could not store the workspace")
		return
	}
	s.cfg.Log.Info("relay: workspace registered", "workspace", id)
	writeJSON(w, http.StatusOK, map[string]string{"workspaceId": id, "key": key})
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
	key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	ws, err := s.st.workspaceFor(strings.TrimSpace(key), now)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unknown workspace key")
		return
	}
	if ok, wait := s.wsLim.allow(ws); !ok {
		tooMany(w, wait, "workspace")
		return
	}
	var q pushReq
	if json.NewDecoder(r.Body).Decode(&q) != nil || q.Envelope == nil {
		writeErr(w, http.StatusBadRequest, "need {handle, envelope, collapseId?, priority?}")
		return
	}
	if !validHandle(q.Handle) {
		writeErr(w, http.StatusNotFound, "unknown handle")
		return
	}
	if !q.Envelope.valid() {
		writeErr(w, http.StatusBadRequest, "envelope: {v:1, epk, n, ct} (base64url; ct at most 3200 characters)")
		return
	}
	if len(q.CollapseID) > maxCollapseID {
		writeErr(w, http.StatusBadRequest, "collapseId: at most 64 bytes")
		return
	}
	if q.Priority != 0 && q.Priority != 5 && q.Priority != 10 {
		writeErr(w, http.StatusBadRequest, "priority: 10 or 5")
		return
	}
	h, err := s.st.target(q.Handle, ws, now)
	switch {
	case errors.Is(err, errNoHandle):
		writeErr(w, http.StatusNotFound, "unknown handle")
		return
	case errors.Is(err, errHandleBound):
		writeErr(w, http.StatusForbidden, "handle belongs to another workspace")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "state error")
		return
	}
	if ok, wait := s.hLim.allow(q.Handle); !ok {
		tooMany(w, wait, "handle")
		return
	}
	if s.cfg.APNs == nil {
		writeErr(w, http.StatusServiceUnavailable, "this relay has no APNs key configured")
		return
	}
	body, err := apnsBody(q.Envelope)
	if err != nil || len(body) > maxAPNsBody {
		writeErr(w, http.StatusBadRequest, "envelope too large")
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
		writeErr(w, http.StatusGone, "handle gone ("+ae.Reason+")")
	case errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests:
		tooMany(w, max(ae.RetryAfter, time.Second), "APNs")
	case errors.As(err, &ae) && ae.Status == http.StatusBadRequest:
		writeErr(w, http.StatusBadRequest, "APNs refused the notification: "+ae.Reason)
	default:
		s.cfg.Log.Warn("relay: APNs send failed", "err", err)
		writeErr(w, http.StatusBadGateway, "APNs unavailable")
	}
}

// Counts reports how many handles and workspaces the relay holds.
func (s *Server) Counts() (handles, workspaces int) { return s.st.counts() }
