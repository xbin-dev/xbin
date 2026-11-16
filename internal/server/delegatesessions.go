package server

// delegatesessions.go — agent sessions (handles), the connection view and
// the workspace map for a person's workspace agent (D228 part C0;
// docs/protocol/delegation.md §Agent sessions). The store is
// internal/delegation/sessions.go; the gate's use of a handle is in
// delegate.go (delegateHandle, sessionAllows, taint).

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/agentpolicy"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/term"
)

// HeaderAgentSession carries the handle a delegate call runs under.
const HeaderAgentSession = "X-XBin-Agent-Session"

// AgentWorld is what the server asks about tiles, orgs and their agent
// rules for agent sessions (the broker answers).
type AgentWorld interface {
	// AgentOrgsOf lists the orgs whose rules apply to tile (its owner org
	// and those it is shared into); "workspace" for a tile in none.
	AgentOrgsOf(tile string) []string
	// AgentModelAllowedIn is why model may not see org's tiles (nil: it may).
	AgentModelAllowedIn(org, model string) error
	// AgentBriefAllowed is why a conversation holding org's data may not
	// send text to a tile agent session on tile, served as served
	// (agentpolicy.Served…) on model ("" unknown); nil: it may.
	AgentBriefAllowed(org, tile, served, model string) error
	// AgentConnectionFor is the connection view of person p for a scope and
	// a model, and what changing to whatIf ("" none) would do.
	AgentConnectionFor(p auth.Principal, orgs, tiles []string, model, whatIf string) (agentpolicy.View, *agentpolicy.Change)
	// AgentScreens lists the screens p sees with the tiles each holds, less
	// the tiles hide reports.
	AgentScreens(p auth.Principal, hide func(tile string) bool) []map[string]any
	// AgentPersonOrgs lists the orgs p is a member of.
	AgentPersonOrgs(p auth.Principal) []map[string]any
	// AgentOrgVisible reports an org p can see: "workspace", one they are
	// a member of, or one a tile they can read is under.
	AgentOrgVisible(p auth.Principal, org string) bool
}

// delegateBootstrap are the routes a delegate may call without a handle:
// making one, and the reads it needs before it has one.
var delegateBootstrap = map[string]bool{
	"POST /delegate/sessions": true,
	"GET /delegate/state":     true,
	"GET /whoami":             true,
	"GET /docs/search":        true,
	"GET /docs/page":          true,
}

func (s *Server) registerDelegateSessionsAPI() {
	s.RegisterAPI("POST /delegate/sessions", s.apiAgentSessionNew)
	s.RegisterAPI("GET /delegate/sessions/{id}", s.apiAgentSessionGet)
	s.RegisterAPI("PATCH /delegate/sessions/{id}", s.apiAgentSessionPatch)
	s.RegisterAPI("DELETE /delegate/sessions/{id}", s.apiAgentSessionDelete)
	s.RegisterAPI("GET /delegate/sessions/{id}/connection", s.apiAgentSessionConnection)
	s.RegisterAPI("GET /delegate/map", s.apiDelegateMap)
	s.RegisterAPI("GET /delegate/start-defaults", s.apiStartDefaults)
	s.RegisterAPI("POST /delegate/sessions/{id}/allows", s.apiAgentSessionAllows)
	s.RegisterAPI("POST /delegate/sessions/{id}/taint", s.apiAgentSessionTaint)
}

// delegateHandle is the live handle a delegate's request names: this
// person's, of this holder. ok is false without one.
func (s *Server) delegateHandle(r *http.Request, p auth.Principal) (delegation.AgentSession, bool) {
	id := strings.TrimSpace(r.Header.Get(HeaderAgentSession))
	if id == "" || s.Delegation == nil {
		return delegation.AgentSession{}, false
	}
	return s.Delegation.AgentSession(p.UserID, p.Agent, id)
}

// handleOf is the handle principal p's call runs under (p.AgentSession,
// set by the gate).
func (s *Server) handleOf(p auth.Principal) (delegation.AgentSession, bool) {
	if p.Agent == "" || p.AgentSession == "" || s.Delegation == nil {
		return delegation.AgentSession{}, false
	}
	return s.Delegation.AgentSession(p.UserID, p.Agent, p.AgentSession)
}

func (s *Server) orgsOf(tile string) []string {
	if s.AgentWorld == nil {
		return []string{agentpolicy.WorkspaceID}
	}
	return s.AgentWorld.AgentOrgsOf(tile)
}

// inSessionScope reports whether tile is inside the handle's scope: a nil
// scope is everything; else one of its tiles, or a tile of one of its orgs.
func (s *Server) inSessionScope(h delegation.AgentSession, tile string) bool {
	if h.Scope == nil {
		return true
	}
	if slices.Contains(h.Scope.Tiles, tile) {
		return true
	}
	for _, o := range s.orgsOf(tile) {
		if slices.Contains(h.Scope.Orgs, o) {
			return true
		}
	}
	return false
}

// hiddenByScope is the refusal for a tile outside the handle's scope.
func hiddenByScope(user string) string {
	return "hidden by you: this tile is outside what " + user + " let this conversation see (the session's scope)"
}

// taint records that a call under p's handle touched tile: its orgs join
// the handle's tainted set. Reads count.
func (s *Server) taint(p auth.Principal, tile string) {
	if p.Agent == "" || p.AgentSession == "" || tile == "" || s.Delegation == nil {
		return
	}
	_ = s.Delegation.TaintAgentSession(p.UserID, p.Agent, p.AgentSession, s.orgsOf(tile))
}

// sessionCaller is the delegate a handle route answers: a person's agent
// only (the routes are the agent's own; the person's page reads the
// connection through GET /agents/connection?session=).
func (s *Server) sessionCaller(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p := auth.PrincipalOf(r)
	if p.Agent == "" || p.Via != "delegate" || p.UserID == "" || s.Delegation == nil {
		WriteError(w, http.StatusForbidden, "agent sessions are a person's workspace agent's: only it makes, reads and ends them", delegationDocs)
		return p, false
	}
	return p, true
}

func (s *Server) sessionView(p auth.Principal, h delegation.AgentSession) map[string]any {
	out := map[string]any{"id": h.ID, "scope": h.Scope, "model": h.Model, "tainted": h.Tainted, "created": h.Created, "used": h.Used}
	if s.AgentWorld != nil {
		view, _ := s.connectionOf(p, h, "")
		out["connection"] = map[string]any{"counts": view.Counts}
	}
	return out
}

// connectionOf is the policy package's view for the handle, as the person.
func (s *Server) connectionOf(p auth.Principal, h delegation.AgentSession, whatIf string) (agentpolicy.View, *agentpolicy.Change) {
	var orgs, tiles []string
	if h.Scope != nil {
		orgs, tiles = h.Scope.Orgs, h.Scope.Tiles
	}
	return s.AgentWorld.AgentConnectionFor(personOf(p), orgs, tiles, h.Model, whatIf)
}

func (s *Server) writeSessionErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, delegation.ErrNoAgentSession):
		WriteError(w, http.StatusNotFound, err.Error(), delegationDocs)
	case errors.Is(err, delegation.ErrBadModel), errors.Is(err, delegation.ErrBadScope2):
		WriteError(w, http.StatusBadRequest, err.Error(), delegationDocs)
	default:
		WriteError(w, http.StatusInternalServerError, "the session couldn't be saved", delegationDocs)
	}
}

// apiAgentSessionNew — POST /delegate/sessions {scope: {orgs, tiles} | null,
// model}: the person's agent → {id, scope, model, tainted, connection:
// {counts}}. A handle for one conversation: what it may see and the model
// it is on.
func (s *Server) apiAgentSessionNew(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionCaller(w, r)
	if !ok {
		return
	}
	var body struct {
		Scope *delegation.SessionScope `json:"scope"`
		Model string                   `json:"model"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, http.StatusBadRequest, `need {scope: {orgs, tiles} | null, model: "<upstream>/<model>"}`, delegationDocs)
		return
	}
	h, err := s.Delegation.NewAgentSession(p.UserID, p.Agent, body.Scope, body.Model)
	if err != nil {
		s.writeSessionErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, s.sessionView(p, h))
}

// apiAgentSessionGet — GET /delegate/sessions/{id}.
func (s *Server) apiAgentSessionGet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionCaller(w, r)
	if !ok {
		return
	}
	h, found := s.Delegation.AgentSession(p.UserID, p.Agent, r.PathValue("id"))
	if !found {
		s.writeSessionErr(w, delegation.ErrNoAgentSession)
		return
	}
	WriteJSON(w, http.StatusOK, s.sessionView(p, h))
}

// apiAgentSessionPatch — PATCH /delegate/sessions/{id} {scope?, model?}.
// The scope may always change (the rules still filter). The model may
// change only to one every tainted org's rules allow: the conversation
// already holds those orgs' data, and it goes where the model goes.
// Otherwise 409 {code: "model_taint", orgs, reason} — the way forward is a
// new session.
func (s *Server) apiAgentSessionPatch(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionCaller(w, r)
	if !ok {
		return
	}
	var body struct {
		Scope *delegation.SessionScope `json:"scope"`
		Model *string                  `json:"model"`
		raw   map[string]any
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var raw map[string]any
	if err := DecodeJSON(r, &raw); err != nil {
		WriteError(w, http.StatusBadRequest, `need {scope?: {orgs, tiles} | null, model?}`, delegationDocs)
		return
	}
	_, setScope := raw["scope"]
	if m, ok := raw["model"].(string); ok {
		body.Model = &m
	}
	if sc, ok := raw["scope"].(map[string]any); ok {
		body.Scope = &delegation.SessionScope{Orgs: stringsOf(sc["orgs"]), Tiles: stringsOf(sc["tiles"])}
	}
	id := r.PathValue("id")
	var taint map[string]any
	h, err := s.Delegation.UpdateAgentSession(p.UserID, p.Agent, id, func(h *delegation.AgentSession) error {
		if body.Model != nil && *body.Model != h.Model && s.AgentWorld != nil {
			var orgs []string
			reason := ""
			for _, o := range h.Tainted {
				if err := s.AgentWorld.AgentModelAllowedIn(o, *body.Model); err != nil {
					orgs = append(orgs, o)
					if reason == "" {
						reason = err.Error()
					}
				}
			}
			if len(orgs) > 0 {
				taint = map[string]any{"code": "model_taint", "orgs": orgs, "reason": reason, "docs": delegationDocs,
					"error": "this conversation has seen data of " + strings.Join(orgs, ", ") + ", which " + *body.Model + " may not see: start a new session to change to it"}
				return errors.New("model_taint")
			}
		}
		if body.Model != nil {
			h.Model = *body.Model
		}
		if setScope {
			h.Scope = body.Scope
		}
		return nil
	})
	if taint != nil {
		WriteJSON(w, http.StatusConflict, taint)
		return
	}
	if err != nil {
		s.writeSessionErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, s.sessionView(p, h))
}

func stringsOf(v any) []string {
	out := []string{}
	if xs, ok := v.([]any); ok {
		for _, x := range xs {
			if str, ok := x.(string); ok {
				out = append(out, str)
			} else {
				out = append(out, "") // not a name: the store refuses the scope
			}
		}
	}
	return out
}

// apiAgentSessionDelete — DELETE /delegate/sessions/{id}: 204.
func (s *Server) apiAgentSessionDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionCaller(w, r)
	if !ok {
		return
	}
	if found, _ := s.Delegation.DeleteAgentSession(p.UserID, p.Agent, r.PathValue("id")); !found {
		s.writeSessionErr(w, delegation.ErrNoAgentSession)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// redactedConnection is the view as it may enter a model's context: the
// tiles the conversation is connected to by name, and everything else —
// blocked, no-access, hidden — as counts and org-level reasons only. A
// blocked tile's name is data of the org that blocked it.
func redactedConnection(v agentpolicy.View) map[string]any {
	tiles := []map[string]any{}
	reasons := map[string]map[string]any{} // org → {blocked, reason}
	for _, t := range v.Tiles {
		switch t.State {
		case agentpolicy.Connected:
			tiles = append(tiles, map[string]any{"tile": t.Tile, "name": t.Name, "org": t.Org, "harnesses": t.Harnesses})
		case agentpolicy.Blocked:
			org := t.By
			if org == "" {
				org = t.Org
			}
			e := reasons[org]
			if e == nil {
				e = map[string]any{"org": org, "blocked": 0, "reason": t.Reason, "rule": t.Rule}
				reasons[org] = e
			}
			e["blocked"] = e["blocked"].(int) + 1
		}
	}
	blocked := []map[string]any{}
	for _, e := range reasons {
		blocked = append(blocked, e)
	}
	slices.SortFunc(blocked, func(a, b map[string]any) int { return strings.Compare(a["org"].(string), b["org"].(string)) })
	return map[string]any{"model": v.Model, "scope": v.Scope, "orgs": v.Orgs, "tiles": tiles, "blocked": blocked, "counts": v.Counts}
}

// apiAgentSessionConnection — GET /delegate/sessions/{id}/connection
// [?whatIf=<model>]: the connection view for the handle, for the agent:
// connected tiles by name; blocked, hidden and no-access ones as counts
// and org-level reasons (redactedConnection). whatIf adds what changing
// the model would connect and disconnect, as counts, and whether the
// handle's taint allows the change.
func (s *Server) apiAgentSessionConnection(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionCaller(w, r)
	if !ok {
		return
	}
	h, found := s.Delegation.AgentSession(p.UserID, p.Agent, r.PathValue("id"))
	if !found || s.AgentWorld == nil {
		s.writeSessionErr(w, delegation.ErrNoAgentSession)
		return
	}
	whatIf := strings.TrimSpace(r.URL.Query().Get("whatIf"))
	view, change := s.connectionOf(p, h, whatIf)
	out := redactedConnection(view)
	out["id"], out["tainted"] = h.ID, h.Tainted
	if change != nil {
		var orgs []string
		for _, o := range h.Tainted {
			if s.AgentWorld.AgentModelAllowedIn(o, whatIf) != nil {
				orgs = append(orgs, o)
			}
		}
		out["whatIf"] = map[string]any{"model": whatIf, "connects": len(change.Connect), "disconnects": len(change.Disconnect), "counts": change.Counts,
			"allowed": len(orgs) == 0, "taintedOrgs": orgs}
	}
	WriteJSON(w, http.StatusOK, out)
}

// apiDelegateMap — GET /delegate/map: the workspace as the person sees it,
// compact, for a conversation: tiles (title, kind, orgs, state, agent
// level, whether a tile agent of theirs runs there), screens with the
// tiles they hold, the person's orgs — within the handle's scope and the
// orgs' rules, with what is left out as counts. Built from the registry,
// the session directory and the broker's screens; it keeps no state.
func (s *Server) apiDelegateMap(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if p.UserID == "" || p.Component != "" {
		WriteError(w, http.StatusForbidden, "the workspace map is a person's, or their agent's", delegationDocs)
		return
	}
	h, hasHandle := s.handleOf(p)
	running := map[string]int{}
	if s.Term != nil {
		for _, si := range s.Term.ListFor(term.HomeKey(personOf(p)), "", nil) {
			if si.Kind == term.KindAgent {
				running[si.Cwd]++
			}
		}
	}
	tiles := []map[string]any{}
	hiddenByYou, blocked := 0, 0
	left := map[string]bool{}
	for _, c := range s.Reg.Components() {
		if c.IsTemplate() || isChrome(c.Path) || !p.CanReadTile(c.Path) {
			continue
		}
		switch {
		case hasHandle && !s.inSessionScope(h, c.Path):
			hiddenByYou++
			left[c.Path] = true
			continue
		case p.Agent != "" && s.agentPolicy().TileAllows(delegation.Subject{Holder: p.Agent, User: p.UserID, Tile: c.Path, Model: p.AgentModel}) != nil:
			blocked++
			left[c.Path] = true
			continue
		}
		level, _ := s.agentLevelOf(c)
		kind := "page"
		if c.HasBackend() {
			kind = "app"
		}
		row := map[string]any{"tile": c.Path, "title": c.Manifest.DisplayTitle(), "kind": kind, "orgs": s.orgsOf(c.Path),
			"state": string(s.Reg.LifecycleState(c.Path)), "agent": level, "tileAgents": running[c.Path]}
		if level == registry.AgentOff {
			row["agent"] = level
		}
		tiles = append(tiles, row)
		s.taint(p, c.Path) // its name and title are its org's data
	}
	out := map[string]any{"user": p.UserID, "tiles": tiles, "leftOut": map[string]int{"hiddenByYou": hiddenByYou, "blocked": blocked}}
	if s.AgentWorld != nil {
		out["screens"] = s.AgentWorld.AgentScreens(personOf(p), func(tile string) bool { return left[tile] || !p.CanReadTile(tile) })
		out["orgs"] = s.AgentWorld.AgentPersonOrgs(personOf(p))
	}
	WriteJSON(w, http.StatusOK, out)
}

// ---- what a start resolves to (D228 C0, second round) ----

// startHarness is one harness an agent session of the person's agent may
// run on a tile, with what a start of it resolves to.
type startHarness struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Mode        string `json:"mode"`        // the mode a start that names none opens in
	Asks        bool   `json:"asks"`        // that mode asks its person before it acts
	Net         string `json:"net"`         // "" the tile's default network
	API         bool   `json:"api"`         // tile-API access
	NeedsTicket bool   `json:"needsTicket"` // the person approves each start (their preference doesn't let it start unasked)
	// NoSignIn: it runs here with no sign-in of the person's — the
	// workspace's LLM connector serves it (G8). A brief's card says so.
	NoSignIn bool `json:"noSignIn"`
}

// startDefaultsView is GET /delegate/start-defaults' answer.
type startDefaultsView struct {
	Tile      string         `json:"tile"`
	Harness   string         `json:"harness"` // the default: the first the person lets start unasked, else the first allowed that can run (startDefaults)
	Harnesses []startHarness `json:"harnesses"`
	Why       string         `json:"why,omitempty"` // no harness may run there: the rule's reason
}

func (d startDefaultsView) of(id string) (startHarness, bool) {
	for _, h := range d.Harnesses {
		if h.ID == id {
			return h, true
		}
	}
	return startHarness{}, false
}
func (d startDefaultsView) modeFor(id string) string { h, _ := d.of(id); return h.Mode }
func (d startDefaultsView) netFor(id string) string  { h, _ := d.of(id); return h.Net }

// startDefaults resolves what a start by p's agent on tile opens with when
// it names nothing: per harness the tile's orgs' rules allow (the policy's
// SessionAllows — their harness lists, and whether the harness can run
// where the tile's sessions must), the mode, network and API access of the
// person's preference for agent-started sessions (the policy's StartPrefs)
// or, where they ask to approve each start, the harness's asking mode. One
// place: the preview of a start request, the unasked start and
// GET /delegate/start-defaults all read it.
//
// The default, when no harness is the person's saved choice (one their
// preference lets start unasked): the first allowed one that can run as
// things are — on the person's own sign-in where an agent session saw
// one, or on the workspace's LLM connector — else the first allowed. So a
// start the agent asks for doesn't open on a coding agent the connector
// can't serve and nobody is signed in to (the journey's G8: Claude Code
// first in the catalog, on a workspace whose host provides the model).
func (s *Server) startDefaults(p auth.Principal, tile string) startDefaultsView {
	out := startDefaultsView{Tile: tile, Harnesses: []startHarness{}}
	pol := s.agentPolicy()
	sub := delegation.Subject{Holder: p.Agent, User: p.UserID, Tile: tile, Model: p.AgentModel}
	provs := agent.Providers()
	llm := startLLMOf(s, p, tile, provs)
	for _, prov := range provs {
		if err := pol.SessionAllows(sub, prov.ID); err != nil {
			if out.Why == "" {
				out.Why = err.Error()
			}
			continue
		}
		h := startHarness{ID: prov.ID, Name: prov.Name, API: true, NeedsTicket: true}
		if prefs, ok := pol.StartPrefs(p.UserID, tile, prov.ID); ok {
			h.Mode, h.Net, h.API, h.NeedsTicket = prefs.Mode, prefs.Net, prefs.API, false
		}
		if m, err := prov.ResolveMode(h.Mode); err == nil { // "" is the harness's own default
			h.Mode = m
		}
		if !prov.Asks(h.Mode) {
			// a mode not known to ask its person (a harness that keeps its
			// modes to itself, or a preference the catalog no longer
			// vouches for): never unasked — the person sees it on a card
			h.NeedsTicket = true
		}
		h.Asks = prov.Asks(h.Mode)
		h.NoSignIn = noSignIn(llm[prov.ID])
		out.Harnesses = append(out.Harnesses, h)
	}
	for _, h := range out.Harnesses {
		if !h.NeedsTicket {
			out.Harness = h.ID // the person's saved choice: always wins
			break
		}
	}
	if out.Harness == "" && len(out.Harnesses) > 0 && llm != nil && !runsNow(llm[out.Harnesses[0].ID]) {
		for _, h := range out.Harnesses {
			if h.NoSignIn {
				out.Harness = h.ID
				break
			}
		}
	}
	if out.Harness == "" && len(out.Harnesses) > 0 {
		out.Harness = out.Harnesses[0].ID
	}
	if len(out.Harnesses) > 0 {
		out.Why = ""
	} else if out.Why == "" {
		out.Why = "no coding agent may run on " + tile
	}
	return out
}

// startLLMOf is what a new agent session of p's on tile would run on, per
// harness (term's AgentLLM: the connector, their own sign-in, or neither);
// nil when sessions get nothing of the connector. A var for tests.
var startLLMOf = func(s *Server, p auth.Principal, tile string, provs []agent.Provider) map[string]*term.ProviderLLM {
	if s.Term == nil {
		return nil
	}
	person := personOf(p)
	return s.Term.AgentLLM(person.UserID, term.HomeKey(person), tile, provs)
}

// noSignIn: the connector serves this harness here, whatever sign-ins the
// person has — "connector", or "own-first" (their own sign-in when a
// session finds one, else the connector).
func noSignIn(row *term.ProviderLLM) bool {
	return row != nil && row.Connector && (row.Use == "connector" || row.Use == "own-first")
}

// runsNow: a session of this harness would start as things are — on the
// connector, or on the person's own sign-in where a session saw one. nil
// (sessions get nothing of the connector) says nothing against it.
func runsNow(row *term.ProviderLLM) bool {
	return row == nil || noSignIn(row) || row.Use == "own" && row.SignedIn != nil && *row.SignedIn
}

// byTileQuery is the tile a request names in ?tile=.
func byTileQuery(_ *Server, _ auth.Principal, r *http.Request, _ string) string {
	return strings.Trim(r.URL.Query().Get("tile"), "/")
}

// apiStartDefaults — GET /delegate/start-defaults?tile=: what a tile agent
// started there by the person's agent would open with — {tile, harness,
// harnesses: [{id, name, mode, asks, net, api, needsTicket}], why?} — so
// the agent can say it truthfully before it asks. The person's terminal
// level on the tile is needed to start one at all (403 otherwise).
func (s *Server) apiStartDefaults(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionCaller(w, r)
	if !ok {
		return
	}
	tile := strings.Trim(r.URL.Query().Get("tile"), "/")
	if tile == "" || !p.CanTerminalTileVia(tile) {
		WriteError(w, http.StatusForbidden, p.UserID+" doesn't have terminal access there: their agent can't start a tile agent", delegationDocs)
		return
	}
	WriteJSON(w, http.StatusOK, s.startDefaults(p, tile))
}

// sessionOrgs reads {orgs: […]} and checks each is one the person can see
// ("workspace", an org of theirs, or one a tile they read is under): an
// org they can't see is refused like one that isn't there.
func (s *Server) sessionOrgs(w http.ResponseWriter, r *http.Request, p auth.Principal) ([]string, bool) {
	var body struct {
		Orgs []string `json:"orgs"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := DecodeJSON(r, &body); err != nil || len(body.Orgs) == 0 || len(body.Orgs) > 200 {
		WriteError(w, http.StatusBadRequest, "need {orgs: [id, …]} (1 to 200)", delegationDocs)
		return nil, false
	}
	for _, o := range body.Orgs {
		if o == "" || len(o) > 200 || s.AgentWorld == nil || !s.AgentWorld.AgentOrgVisible(personOf(p), o) {
			WriteError(w, http.StatusForbidden, "no org "+strconv.Quote(o)+" that "+p.UserID+" can see", delegationDocs)
			return nil, false
		}
	}
	return body.Orgs, true
}

// apiAgentSessionAllows — POST /delegate/sessions/{id}/allows {orgs}: does
// the session's model satisfy those orgs' rules? → {allowed, reasons:
// {org: why}}. For what the agent keeps itself (its notebook): a page
// stamped with orgs is readable in a session only if this says yes.
func (s *Server) apiAgentSessionAllows(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionCaller(w, r)
	if !ok {
		return
	}
	h, found := s.Delegation.AgentSession(p.UserID, p.Agent, r.PathValue("id"))
	if !found {
		s.writeSessionErr(w, delegation.ErrNoAgentSession)
		return
	}
	orgs, ok := s.sessionOrgs(w, r, p)
	if !ok {
		return
	}
	reasons := map[string]string{}
	for _, o := range orgs {
		if err := s.AgentWorld.AgentModelAllowedIn(o, h.Model); err != nil {
			reasons[o] = err.Error()
		}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"id": h.ID, "model": h.Model, "allowed": len(reasons) == 0, "reasons": reasons})
}

// apiAgentSessionTaint — POST /delegate/sessions/{id}/taint {orgs}: the
// conversation took in data of those orgs through something xbind didn't
// see (a notebook page stamped with them): add them to the session's
// taint. Add-only — nothing takes an org out — and only orgs whose rules
// the session's model satisfies: data it may not see isn't data it may
// read (409 {code: "model_taint", orgs, reason} otherwise). → the session.
func (s *Server) apiAgentSessionTaint(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionCaller(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	h, found := s.Delegation.AgentSession(p.UserID, p.Agent, id)
	if !found {
		s.writeSessionErr(w, delegation.ErrNoAgentSession)
		return
	}
	orgs, ok := s.sessionOrgs(w, r, p)
	if !ok {
		return
	}
	var refused []string
	reason := ""
	for _, o := range orgs {
		if err := s.AgentWorld.AgentModelAllowedIn(o, h.Model); err != nil {
			refused = append(refused, o)
			if reason == "" {
				reason = err.Error()
			}
		}
	}
	if len(refused) > 0 {
		WriteJSON(w, http.StatusConflict, map[string]any{"code": "model_taint", "orgs": refused, "reason": reason, "docs": delegationDocs,
			"error": "this session's model may not see data of " + strings.Join(refused, ", ") + ": it can't take it in"})
		return
	}
	if err := s.Delegation.TaintAgentSession(p.UserID, p.Agent, id, orgs); err != nil {
		s.writeSessionErr(w, err)
		return
	}
	h, _ = s.Delegation.AgentSession(p.UserID, p.Agent, id)
	WriteJSON(w, http.StatusOK, s.sessionView(p, h))
}

// RearmDelegateNotices schedules, at boot, the expiry notice of every
// request that waits or is an unused ticket — the timers of the xbind that
// stopped are gone. One that fell due meanwhile is told at once; reading
// the request already answers "expired" either way (the store resolves
// expiry on every read).
func (s *Server) RearmDelegateNotices() {
	if s.Delegation == nil || s.HolderNotify == nil {
		return
	}
	for _, user := range s.Delegation.People() {
		reqs, _ := s.Delegation.Requests(user, false)
		for _, req := range reqs {
			switch {
			case req.Agent == "":
			case req.State == delegation.Pending || req.State == delegation.Approved:
				s.notifyAt(req.Expires, user, req.ID, req.Agent)
			case req.State == "expired":
				s.HolderNotify(req.Agent, user, "request", req.ID) // it expired while xbind was down, or just before (decided ones are kept a day)
			}
		}
	}
}
