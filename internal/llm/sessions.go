package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	sdkllm "github.com/xbin-dev/xbin/sdk/llm"
)

// sessions.go — the connector as coding tools get it (D226,
// docs/llm.md "Coding agents and terminals"): the workspace's switches for
// agent sessions and terminals, what a new session is offered (the models
// and the default one), the session keys, the
// key a bound component asks for a harness it runs in a sandbox (POST
// /llm/keys), and the handler of the LLM-only listener such a sandbox
// reaches.

// Sessions are the workspace's switches for coding tools: one source's say
// (the admin's store or the host's file). A member left out says nothing.
type Sessions struct {
	// Agents: agent sessions are offered the connector (default on). Who
	// then serves a session — the connector or the person's own sign-in —
	// is the agents policy's and the person's to say (the workspace's
	// credentials default, D229), not this switch's.
	Agents *bool `json:"agents,omitempty"`
	// Terminals: tile terminals get XBIN_LLM_URL and XBIN_LLM_KEY (default
	// on).
	Terminals *bool `json:"terminals,omitempty"`
	// OpenAIEnv: terminals also get OPENAI_BASE_URL and OPENAI_API_KEY, the
	// pair most tools read (default off: a tool that reads the key and not
	// the base URL sends the key to OpenAI).
	OpenAIEnv *bool `json:"openaiEnv,omitempty"`
}

// sessionsInForce are the switches as merged.
type sessionsInForce struct {
	Agents    bool `json:"agents"`
	Terminals bool `json:"terminals"`
	OpenAIEnv bool `json:"openaiEnv"`
}

// lay puts one source's say over f; lock marks each member it sets (the
// host's).
func (f *sessionsInForce) lay(s Sessions, lock func(path string)) {
	if s.Agents != nil {
		f.Agents = *s.Agents
		lock("sessions.agents")
	}
	if s.Terminals != nil {
		f.Terminals = *s.Terminals
		lock("sessions.terminals")
	}
	if s.OpenAIEnv != nil {
		f.OpenAIEnv = *s.OpenAIEnv
		lock("sessions.openaiEnv")
	}
}

// sessionsBody is PUT /llm/config's sessions member.
type sessionsBody struct {
	Agents    *bool `json:"agents"`
	Terminals *bool `json:"terminals"`
	OpenAIEnv *bool `json:"openaiEnv"`
}

// apply writes b into next, each member unless hold says the host has it.
func (b *sessionsBody) apply(next *Settings, snap *snapshot, hold func(path string, same bool) bool) {
	if b == nil {
		return
	}
	if b.Agents != nil && !hold("sessions.agents", *b.Agents == snap.sessions.Agents) {
		v := *b.Agents
		next.Sessions.Agents = &v
	}
	if b.Terminals != nil && !hold("sessions.terminals", *b.Terminals == snap.sessions.Terminals) {
		v := *b.Terminals
		next.Sessions.Terminals = &v
	}
	if b.OpenAIEnv != nil && !hold("sessions.openaiEnv", *b.OpenAIEnv == snap.sessions.OpenAIEnv) {
		v := *b.OpenAIEnv
		next.Sessions.OpenAIEnv = &v
	}
}

// RoleHarness is the role whose model coding harnesses default to
// (xbin/harness; the default model when the role has none).
const RoleHarness = "harness"

// OfferModel is one model a session is offered.
type OfferModel struct {
	ID      string
	Context int // context window in tokens, when the upstream's list says (0 = unknown)
	Output  int // most output tokens, likewise
}

// SessionOffer is the connector as a new terminal or agent session of one
// person on one tile would get it.
type SessionOffer struct {
	// Agents: agent sessions are offered the connector; Terminals and
	// OpenAIEnv the terminal switches.
	Agents    bool
	Terminals bool
	OpenAIEnv bool
	// Model is the default model for a coding harness, as
	// "<upstream>/<model>" ("" when the workspace set none and no upstream
	// lists one); Models every model the person may use.
	Model  string
	Models []OfferModel
}

// offerModelsWait bounds how long opening a session waits for the
// upstreams' model lists (cached a minute): a session never hangs on a
// provider's /models.
const offerModelsWait = 4 * time.Second

// Offer answers what a new session of user (OwnerUser for the root token,
// "" for nobody) on component is offered; false while the connector serves
// no call at all (nothing configured, or switched off) — then sessions are
// exactly as without a connector. models false leaves Models out and asks
// no upstream for its list — a terminal's open, a launcher's listing —
// unless the workspace set no default model: then the first listed is it.
func (s *Service) Offer(ctx context.Context, user, component string, models bool) (SessionOffer, bool) {
	snap := s.current()
	if ready(snap) != nil {
		return SessionOffer{}, false
	}
	o := SessionOffer{Agents: snap.sessions.Agents, Terminals: snap.sessions.Terminals, OpenAIEnv: snap.sessions.OpenAIEnv}
	c := Caller{User: user, Component: component, Via: "key"}
	if u, m, err := snap.resolve(pseudoUpstream + "/" + RoleHarness); err == nil && !s.refusedByPolicy(c, u, m) {
		o.Model = publicName(u, m)
	}
	if !models && o.Model != "" {
		return o, true
	}
	ctx, cancel := context.WithTimeout(ctx, offerModelsWait)
	defer cancel()
	list, _ := s.listModels(ctx, snap, c)
	for _, m := range list.Data {
		if strings.HasPrefix(m.ID, pseudoUpstream+"/") {
			continue // xbin/default, xbin/<role>: the defaults under another name
		}
		o.Models = append(o.Models, OfferModel{ID: m.ID,
			Context: extraInt(m, "context_length", "context_window", "max_context_length", "max_model_len"),
			Output:  extraInt(m, "max_output_tokens", "max_completion_tokens")})
	}
	if o.Model == "" && len(o.Models) > 0 {
		o.Model = o.Models[0].ID
	}
	if !models {
		o.Models = nil
	}
	return o, true
}

// ResolveModel says what a model id ("<upstream>/<model>", an alias, a
// role's name) is under the configuration in force: its upstream, the
// upstream's own name for it, and whether that upstream is marked private.
// false for an id nothing resolves.
func (s *Service) ResolveModel(id string) (upstream, model string, private, ok bool) {
	u, m, err := s.current().resolve(id)
	if err != nil {
		return "", "", false, false
	}
	return u.Name, m, u.Private, true
}

// extraInt is the first of names an upstream's model object carries as a
// positive whole number.
func extraInt(m sdkllm.Model, names ...string) int {
	for _, n := range names {
		var v float64
		if raw, ok := m.Extra[n]; ok && json.Unmarshal(raw, &v) == nil && v >= 1 && v < 1<<40 {
			return int(v)
		}
	}
	return 0
}

// MintKey makes an LLM key for a session or a harness run: metered to user
// (OwnerUser for the root token; "" when nobody acts and component alone is
// metered) and component, dead after ttl (0: MaxKeyTTL) or revoke — which
// the minter calls when the session ends. An error when this Service keeps
// no key store of its own.
func (s *Service) MintKey(g KeyGrant, ttl time.Duration) (token string, grant KeyGrant, revoke func(), err error) {
	if s.llmKeys == nil {
		return "", KeyGrant{}, nil, fmt.Errorf("this LLM connector mints no keys")
	}
	token, grant, err = s.llmKeys.Mint(g, ttl)
	if err != nil {
		return "", KeyGrant{}, nil, err
	}
	id := grant.ID
	return token, grant, func() { s.llmKeys.Revoke(id) }, nil
}

// ---- keys for components (POST /llm/keys) -------------------------------------

// DefaultComponentKeyTTL is the life of a key a component asks for without
// naming one.
const DefaultComponentKeyTTL = 24 * time.Hour

// maxKeyLabel bounds the label and sandbox a component names for its key.
const maxKeyLabel = 200

// apiMintKey — POST /llm/keys {user?, ttlSec?, label?, sandbox?}: a
// component bound to the connector asks for a narrow key for a harness it
// runs for the person it acts for (the SDK's LLMKeyFor). The key is metered
// exactly as the component's own calls are: to the person the connector
// attributes this credential's calls to — its partition's person, the
// person driving its frame or terminal, nobody for a backend that serves
// everyone — and to the component. user, when named, must be that person:
// a component never names someone else. The key is minted under the
// binding and ends with it; a non-primary deployment gets one only where
// its tile's edge policy leaves it the binding in full.
func (s *Service) apiMintKey(w http.ResponseWriter, r *http.Request) {
	c, ok := s.keyCaller(w, r)
	if !ok {
		return
	}
	var body struct {
		User    string `json:"user"`
		TTLSec  int64  `json:"ttlSec"`
		Label   string `json:"label"`
		Sandbox string `json:"sandbox"`
	}
	if err := decode(r, &body); err != nil || body.TTLSec < 0 || len(body.Label) > maxKeyLabel || len(body.Sandbox) > maxKeyLabel {
		server.WriteError(w, http.StatusBadRequest, "need {user?, ttlSec?, label?, sandbox?} (label and sandbox at most 200 characters)", docs)
		return
	}
	if c.ReadOnly != "" { // a non-primary deployment under the read clamp: a key would let it call
		server.WriteJSON(w, http.StatusForbidden, map[string]any{"error": c.ReadOnly, "code": "xbin_llm_edge", "docs": docs})
		return
	}
	if body.User != "" && body.User != c.User {
		who := "nobody (it serves everyone: its calls are metered to the tile)"
		if c.User != "" {
			who = c.User
		}
		server.WriteError(w, http.StatusForbidden, "an LLM key is for the person this credential acts for — "+who+" — not "+body.User, docs)
		return
	}
	ttl := time.Duration(body.TTLSec) * time.Second
	if ttl <= 0 {
		ttl = DefaultComponentKeyTTL
	}
	// Bound: the key stands only while the tile's binding does (keyHolds)
	token, g, _, err := s.MintKey(KeyGrant{User: c.User, Component: c.Component, Label: body.Label, Sandbox: body.Sandbox, Bound: true}, ttl)
	if err != nil {
		server.WriteError(w, http.StatusServiceUnavailable, err.Error(), docs)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	server.WriteJSON(w, http.StatusOK, map[string]any{"id": g.ID, "token": token, "user": g.User, "component": g.Component,
		"expires": g.Expires.UTC().Format(time.RFC3339), "url": BaseURL})
}

// apiRevokeKey — DELETE /llm/keys/{id}: the component that asked for a key
// ends it. 204 whether or not it still lived; a key of another component's
// is not found.
func (s *Service) apiRevokeKey(w http.ResponseWriter, r *http.Request) {
	c, ok := s.keyCaller(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if s.llmKeys != nil {
		s.llmKeys.RevokeWhere(func(g KeyGrant) bool { return g.ID == id && g.Component == c.Component })
	}
	w.WriteHeader(http.StatusNoContent)
}

// keyCaller admits the caller of the key routes: a component whose llm slot
// is bound to the connector, by its own credential — never a person's
// sign-in (their sessions get keys from xbind) and never an LLM key (a key
// mints no key).
func (s *Service) keyCaller(w http.ResponseWriter, r *http.Request) (Caller, bool) {
	p := auth.PrincipalOf(r)
	if _, isKey := bearerKey(r); isKey || p.Via == "llm" || p.Component == "" {
		server.WriteError(w, http.StatusForbidden, "LLM keys are asked for by a tile bound to the LLM connector, with its own credential", docs)
		return Caller{}, false
	}
	if p.ReadOnly() {
		server.WriteError(w, http.StatusForbidden, "viewing as someone changes nothing", docs)
		return Caller{}, false
	}
	snap := s.current()
	c, ref := s.admit(r, snap)
	if ref != nil {
		code, _ := ref.err.Code.(string)
		server.WriteJSON(w, ref.status, map[string]any{"error": ref.err.Message, "code": code, "docs": docs})
		return Caller{}, false
	}
	return c, true
}

// ---- the LLM-only listener ------------------------------------------------------

// KeyOnlyHandler serves the connector's OpenAI-compatible routes at
// /v1/chat/completions, /v1/responses and /v1/models to the holder of an
// LLM key, and nothing else to anyone: the handler of the listener a
// sandbox with no route to xbind is given (a tile sandbox created with
// net.llm, docs/sandbox-manager.md). A request without a key is 401, any
// other path 404, both in OpenAI's error shape. It runs outside xbind's
// own middleware, so no other credential means anything here.
func (s *Service) KeyOnlyHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.watched(s.apiCall(sdkllm.PathChat)))
	mux.HandleFunc("POST /v1/responses", s.watched(s.apiCall(sdkllm.PathResponses)))
	mux.HandleFunc("GET /v1/models", s.apiModels)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := bearerKey(r); !ok {
			refuse(http.StatusUnauthorized, "invalid_api_key", "this address takes an LLM key (Authorization: Bearer xbl_…) and nothing else").write(w)
			return
		}
		if _, pattern := mux.Handler(r); pattern == "" {
			refuse(http.StatusNotFound, "xbin_llm_route", "only /v1/chat/completions, /v1/responses and /v1/models are served here").write(w)
			return
		}
		// no principal of xbind's rides this request: identify reads the key
		mux.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Via: "llm"})))
	})
}
