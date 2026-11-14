package broker

// agentpolicy.go — org constraints on agents (D229; rules and evaluation:
// internal/agentpolicy, storage: users/orgagents.go). The broker knows
// which orgs a tile falls under, so it gathers the layers and asks:
//
//   - AgentHarnessGate: may an agent session of this harness run on this
//     tile (term.Manager's gate for sessions people start, and the
//     workspace agent's when it starts one);
//   - WorkspaceAgentGate: may a person's workspace agent reach this tile
//     with this model (the delegation gate's org seam);
//   - AgentConnection: the list a person reads — every org and tile as
//     connected, hidden, blocked (and why) or no-access, with totals.
//
// Routes (docs/protocol/orgs.md, docs/protocol/agents.md):
//
//	GET  /api/xbin/agents/policy        the workspace's bounds + credential default
//	PUT  /api/xbin/agents/policy        workspace admins
//	PUT  /api/xbin/orgs/{org}/agents    workspace admins, or an admin of the org — within the bounds
//	GET  /api/xbin/agents/connection    a person's own connection view
//
// With no users store (the single-user workspace) there are no orgs and no
// bounds: every gate answers yes.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/agentpolicy"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

const agentPolicyDocs = "/docs/auth.md"

func (b *Broker) registerAgentPolicy(srv *server.Server) {
	srv.RegisterAPI("GET /agents/policy", b.apiAgentsPolicyGet)
	srv.RegisterAPI("PUT /agents/policy", b.apiAgentsPolicyPut)
	srv.RegisterAPI("PUT /orgs/{org}/agents", b.apiOrgAgentsPut)
	srv.RegisterAPI("GET /agents/connection", b.apiAgentsConnection)
}

// knownHarness names the harnesses this xbind can run (the catalog, and
// the test fake where a fixture advertises it).
func knownHarness(id string) bool {
	_, ok := agent.Lookup(id)
	return ok
}

// AgentLayers is the layers of agent rules tile falls under: the
// workspace's bounds, then each org that owns it or that it is shared
// into. nil without a users store (nothing constrains anything).
func (b *Broker) AgentLayers(tile string) []agentpolicy.Layer {
	if b.Users == nil {
		return nil
	}
	return b.Users.AgentLayers(strings.Trim(tile, "/"))
}

// AgentCheck asks tile's layers one question (agentpolicy.Ask).
func (b *Broker) AgentCheck(tile string, ask agentpolicy.Ask) agentpolicy.Decision {
	return agentpolicy.Check(b.AgentLayers(tile), ask)
}

// AgentHarnessGate answers why an agent session of harness may not run on
// tile — the reason a person reads ("Finance allows these coding agents
// only: claude, codex (not gemini)") — or nil. For everyone: the person
// who opens the session themselves and the workspace agent alike.
func (b *Broker) AgentHarnessGate(tile, harness string) error {
	return b.AgentCheck(tile, agentpolicy.Ask{Harness: harness}).Err()
}

// AgentHarnesses is the harness rule that holds on tile: any, or the ids
// every layer allows.
func (b *Broker) AgentHarnesses(tile string) agentpolicy.Allow {
	return agentpolicy.Harnesses(b.AgentLayers(tile))
}

// WorkspaceAgentGate answers why userID's workspace agent may not reach
// tile with model (nil: no model chosen, which only "any" allows): the
// tile's orgs' on/off switch and model rule, and the workspace's. It does
// not ask whether the person can read the tile — the caller's own gates
// do (DelegateTile).
func (b *Broker) WorkspaceAgentGate(userID, tile string, model *agentpolicy.ModelRef) error {
	return b.AgentCheck(tile, agentpolicy.Ask{WorkspaceAgent: true, Model: model}).Err()
}

// AgentTileAllows and AgentSessionAllows are WorkspaceAgentGate in the
// shape the delegation package asks (its Policy: TileAllows,
// SessionAllows): may holder — a person's workspace agent — acting for user
// on model reach tile, and may it open an agent session of harness there.
// model is the id the agent says its conversation is on ("" for none
// said); whether that model is private is the LLM connector's answer
// (ResolveModel), never the agent's.
func (b *Broker) AgentTileAllows(holder, user, tile, model string) error {
	return b.WorkspaceAgentGate(user, tile, b.ResolveModel(model))
}

func (b *Broker) AgentSessionAllows(holder, user, tile, model, harness string) error {
	return b.AgentCheck(tile, agentpolicy.Ask{WorkspaceAgent: true, Harness: harness, Model: b.ResolveModel(model)}).Err()
}

// ResolveModel turns a model id ("upstream/model") into a ModelRef with
// what the LLM connector knows about it (AgentModels); nil for "". With no
// connector installed, or for a model it doesn't know, the model is not
// private: only the connector can say an upstream is.
func (b *Broker) ResolveModel(id string) *agentpolicy.ModelRef {
	if id = strings.TrimSpace(id); id == "" {
		return nil
	}
	if b.AgentModels != nil {
		if m, ok := b.AgentModels(id); ok {
			return &m
		}
	}
	m := agentpolicy.ParseModelID(id)
	return &m
}

// AgentCredentials resolves whose credentials serve a coding agent's model
// calls for one session (agentpolicy.ResolveCredentials): the session's
// choice, the person's saved default for the harness, the workspace's.
func (b *Broker) AgentCredentials(session, saved string) (use string, forced bool) {
	ws := agentpolicy.CredentialsOwnFirst
	if b.Users != nil {
		ws = b.Users.WorkspaceAgents().Credentials
	}
	return agentpolicy.ResolveCredentials(session, saved, ws)
}

// AgentConnection is p's connection view for scope and model: the tiles p
// can read and the orgs p has to do with, each connected, hidden, blocked
// or — for what the scope names and p can't see — no-access. p is a
// person (their own session, or their workspace agent's principal); the
// root token and --no-auth see every tile.
func (b *Broker) AgentConnection(p auth.Principal, scope agentpolicy.Scope, model *agentpolicy.ModelRef) agentpolicy.View {
	return agentpolicy.Connection(b.agentInput(p, scope, model))
}

// AgentWhatIf is what changing p's session from model to another would
// connect and disconnect.
func (b *Broker) AgentWhatIf(p auth.Principal, scope agentpolicy.Scope, model, to *agentpolicy.ModelRef) agentpolicy.Change {
	return agentpolicy.WhatIf(b.agentInput(p, scope, model), to)
}

func (b *Broker) agentInput(p auth.Principal, scope agentpolicy.Scope, model *agentpolicy.ModelRef) agentpolicy.Input {
	in := agentpolicy.Input{Scope: scope, Model: model}
	seen := map[string]bool{}
	for _, c := range b.Reg.Components() {
		if c.IsTemplate() || c.Path == "root" || c.Path == "shell" || !p.CanReadTile(c.Path) {
			continue
		}
		t := agentpolicy.Tile{Path: c.Path, Name: c.Manifest.DisplayTitle()}
		if b.Users != nil {
			t.Owner, t.Orgs = b.Users.TileOrgs(c.Path)
		}
		for _, id := range t.Orgs {
			seen[id] = true
		}
		in.Tiles = append(in.Tiles, t)
	}
	if b.Users == nil {
		return in
	}
	in.Bounds = b.Users.WorkspaceAgents().Rules
	for _, o := range b.Users.Orgs() {
		_, member := o.Member(p.UserID)
		if !seen[o.ID] && !member && !b.canManageUsers(p) {
			continue // an org p has nothing to do with stays unnamed
		}
		org := agentpolicy.Org{ID: o.ID, Name: o.Name}
		if o.Agents != nil {
			org.Rules = *o.Agents
		}
		in.Orgs = append(in.Orgs, org)
	}
	return in
}

// ownSession reports a person acting in their own session, app or device:
// not a tile's credentials, not a terminal's or an agent session's token
// (wherever it is bound), not view-as. An org admin sets their org's agent
// rules only so — a coding agent running on their token must not be able
// to widen the rules it runs under.
func ownSession(p auth.Principal) bool {
	return p.Component == "" && p.Impersonator == "" && (p.Owner || p.UserID != "") &&
		p.Via != "terminal" && p.Via != "instance" && p.Via != "frame"
}

// ---- GET /agents/policy ----

type agentsPolicyView struct {
	users.WorkspaceAgents
	// Harnesses this xbind can run: what an allowlist may name.
	Known  []string `json:"knownHarnesses"`
	CanSet bool     `json:"canSet"`
}

func (b *Broker) agentsPolicyView(p auth.Principal) agentsPolicyView {
	v := agentsPolicyView{CanSet: b.canManageUsers(p), Known: []string{}}
	v.Credentials = agentpolicy.CredentialsOwnFirst
	if b.Users != nil {
		v.WorkspaceAgents = b.Users.WorkspaceAgents()
	}
	for _, pr := range agent.Providers() {
		v.Known = append(v.Known, pr.ID)
	}
	return v
}

// apiAgentsPolicyGet — the workspace's bounds on agents and its credential
// default. People read it (the launcher and the org's admins need it); a
// tile's own credentials only with user management.
func (b *Broker) apiAgentsPolicyGet(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if p.Component != "" && !b.canManageUsers(p) {
		server.WriteError(w, http.StatusForbidden, "the workspace's agent rules are read by people, or with the xbin:users capability", agentPolicyDocs)
		return
	}
	server.WriteJSON(w, http.StatusOK, b.agentsPolicyView(p))
}

// decodeAgentRules reads a PUT body as a JSON object of rules.
func decodeAgentRules(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body map[string]json.RawMessage
	if err := server.DecodeJSON(r, &body); err != nil || body == nil {
		server.WriteError(w, http.StatusBadRequest, `need {"workspaceAgent": "on"|"off", "harnesses": "any"|["claude", …], "models": "any"|"private"|["upstream", "upstream/model", …]}: each optional, null puts a rule back to its default`, agentPolicyDocs)
		return nil, false
	}
	return body, true
}

// apiAgentsPolicyPut — PUT /agents/policy {workspaceAgent?, harnesses?,
// models?, credentials?}: the workspace's bounds (workspace admins). A
// rule not named keeps its value.
func (b *Broker) apiAgentsPolicyPut(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	body, ok := decodeAgentRules(w, r)
	if !ok {
		return
	}
	patch, err := agentpolicy.ParsePatch(body, "credentials")
	if err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error(), agentPolicyDocs)
		return
	}
	creds := ""
	if raw, ok := body["credentials"]; ok && string(raw) != "null" {
		if json.Unmarshal(raw, &creds) != nil || creds == "" {
			server.WriteError(w, http.StatusBadRequest, `credentials: "own-first" or "connector"`, agentPolicyDocs)
			return
		}
	} else if ok {
		creds = agentpolicy.CredentialsOwnFirst
	}
	out, err := st.SetWorkspaceAgents(patch, creds, knownHarness)
	if err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error(), agentPolicyDocs)
		return
	}
	p := auth.PrincipalOf(r)
	slog.Info("audit", "who", p.From(), "method", "PUT", "path", "/agents/policy", "status", http.StatusOK, "agents", rulesJSON(out))
	b.agentPolicyChanged("")
	server.WriteJSON(w, http.StatusOK, b.agentsPolicyView(p))
}

// apiOrgAgentsPut — PUT /orgs/{org}/agents {workspaceAgent?, harnesses?,
// models?}: an org's rules for agents on its tiles. Workspace admins (and
// the xbin:users capability) for any org; an admin of the org, signed in
// themselves, for their own — never a tile driven by one. The result must
// sit within the workspace's bounds. → {agents, bounds}.
func (b *Broker) apiOrgAgentsPut(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	org := r.PathValue("org")
	p := auth.PrincipalOf(r)
	if !b.canManageUsers(p) && !(ownSession(p) && p.Access.IsAdminOrg(org)) {
		server.WriteError(w, http.StatusForbidden, "an org's agent rules are set by a workspace admin, or by an admin of the org in their own session — not from a tile, a terminal or an agent session", agentPolicyDocs)
		return
	}
	body, ok := decodeAgentRules(w, r)
	if !ok {
		return
	}
	patch, err := agentpolicy.ParsePatch(body)
	if err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error(), agentPolicyDocs)
		return
	}
	out, err := st.SetOrgAgents(org, patch, knownHarness)
	if err != nil {
		code := http.StatusBadRequest
		if strings.HasPrefix(err.Error(), "no such org") {
			code = http.StatusNotFound
		}
		server.WriteError(w, code, err.Error(), agentPolicyDocs)
		return
	}
	slog.Info("audit", "who", p.From(), "method", "PUT", "path", "/orgs/"+org+"/agents", "status", http.StatusOK, "agents", rulesJSON(out))
	b.agentPolicyChanged(org)
	server.WriteJSON(w, http.StatusOK, map[string]any{"agents": out, "bounds": st.WorkspaceAgents().Rules})
}

// rulesJSON is v as the API shows it, for the audit line.
func rulesJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// agentPolicyChanged tells open panels: the users event the orgs views
// reload on, and an `agents` event ({org} — "" for the workspace's bounds)
// for a launcher or a connection panel to read its rules again. Running
// sessions are left alone: a rule decides what starts next.
func (b *Broker) agentPolicyChanged(org string) {
	if b.Hub == nil {
		return
	}
	b.Hub.Publish(events.Event{Type: "users"})
	b.Hub.Publish(events.Event{Type: "agents", Data: map[string]string{"org": org}})
}

// ---- GET /agents/connection ----

// splitList reads a repeated or comma-separated query parameter.
func splitList(vals []string) []string {
	var out []string
	for _, v := range vals {
		for _, e := range strings.Split(v, ",") {
			if e = strings.Trim(strings.TrimSpace(e), "/"); e != "" && !slices.Contains(out, e) {
				out = append(out, e)
			}
		}
	}
	return out
}

// apiAgentsConnection — GET /agents/connection[?model=&orgs=&tiles=&whatIf=]:
// the caller's own connection view. model names the session's model
// ("upstream/model", resolved by the LLM connector — a caller can't claim
// an upstream is private); orgs and tiles are the session's scope (comma-
// separated or repeated; none: everything the person can see; orgs takes
// "workspace" for the tiles in no org); whatIf names another model and
// adds what changing to it would connect and disconnect. Answers a
// person, for themselves — whatever credential names them as the caller
// (their session, the app, their workspace agent) — never a tile.
func (b *Broker) apiAgentsConnection(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if p.Component != "" {
		server.WriteError(w, http.StatusForbidden, "the connection view is a person's own: a tile's credentials can't read it", "/docs/protocol/agents.md")
		return
	}
	q := r.URL.Query()
	scope := agentpolicy.Scope{Orgs: splitList(q["orgs"]), Tiles: splitList(q["tiles"])}
	if len(scope.Orgs)+len(scope.Tiles) > 2000 {
		server.WriteError(w, http.StatusBadRequest, "a scope names at most 2000 orgs and tiles", "/docs/protocol/agents.md")
		return
	}
	model := b.ResolveModel(q.Get("model"))
	out := struct {
		agentpolicy.View
		WhatIf *agentpolicy.Change `json:"whatIf,omitempty"`
	}{View: b.AgentConnection(p, scope, model)}
	if to := b.ResolveModel(q.Get("whatIf")); to != nil {
		ch := b.AgentWhatIf(p, scope, model, to)
		out.WhatIf = &ch
	}
	server.WriteJSON(w, http.StatusOK, out)
}
