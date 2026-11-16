package server

// delegateapi.go — the delegation API (docs/protocol/delegation.md): what a
// person's own clients, and their workspace agent, call about the agent.
//
//	the switches   GET/PUT /delegate/state, GET/PUT /delegate/workspace
//	the log        GET /delegate/audit
//	approvals      /delegate/requests…  (the agent asks; the person answers)
//	               /delegate/approvals… (standing approvals: list, give, revoke)
//	change sets    /delegate/changesets… (changeset.go)
//
// Three kinds of caller reach these routes, and each handler says which it
// takes (delegateCaller):
//
//   - the PERSON, through their own top-level credential: a browser
//     session, the app's session or a device's. Everything that GRANTS is
//     theirs alone: approving a request (a ticket), a standing approval,
//     the admin opt-in, resuming, reading the log, deciding a change set;
//   - the agent's own PAGE: the sealed holder's frame, when its token was
//     minted under the person's own login (auth.Principal.LoginFrame). It
//     shows the agent's cards, and it renders model output — so a script
//     injected there must not be able to approve what the agent asked for.
//     It reads (the state, the requests, the approvals, the change sets)
//     and it may pause the agent. Nothing else;
//   - the AGENT itself, a delegate principal, through the delegate gate,
//     for the rows the delegation cap lists: asking, withdrawing, reading
//     its own requests.
//
// Never a tile's other credentials, view-as, or the root token. One
// exception, for development only (Server.DelegationDev with --no-auth,
// where nobody signs in): the implicit owner answers as the person, for
// the person ?user= names (default "owner").

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/util"
)

const delegationDocs = "/docs/protocol/delegation.md"

func (s *Server) registerDelegationAPI() {
	s.registerDelegateSessionsAPI() // agent sessions (handles), the connection view, the map (delegatesessions.go)
	s.registerDocsAPI()             // the docs, searchable on /api/ (docsapi.go)
	s.registerDelegateWebAPI()      // the web (delegateweb.go)
	s.registerDelegateMCPAPI()      // MCP tiles (delegatemcp.go)
	s.RegisterAPI("GET /delegate/state", s.apiDelegateState)
	s.RegisterAPI("PUT /delegate/state", s.apiDelegateStatePut)
	s.RegisterAPI("GET /delegate/workspace", s.apiDelegateWorkspace)
	s.RegisterAPI("PUT /delegate/workspace", s.apiDelegateWorkspacePut)
	s.RegisterAPI("GET /delegate/audit", s.apiDelegateAudit)
	s.RegisterAPI("POST /delegate/requests", s.apiDelegateRequestNew)
	s.RegisterAPI("GET /delegate/requests", s.apiDelegateRequests)
	s.RegisterAPI("GET /delegate/requests/{id}", s.apiDelegateRequest)
	s.RegisterAPI("POST /delegate/requests/{id}/approve", s.apiDelegateRequestDecide)
	s.RegisterAPI("POST /delegate/requests/{id}/deny", s.apiDelegateRequestDecide)
	s.RegisterAPI("DELETE /delegate/requests/{id}", s.apiDelegateRequestWithdraw)
	s.RegisterAPI("GET /delegate/approvals", s.apiDelegateApprovals)
	s.RegisterAPI("POST /delegate/approvals", s.apiDelegateApprovalAdd)
	s.RegisterAPI("DELETE /delegate/approvals/{id}", s.apiDelegateApprovalRevoke)
	s.registerChangeSetAPI()
}

// ---- who is calling ----

// delegateActor is the caller of a delegation route: whose agent it is
// about, and which of the three kinds of caller it is.
type delegateActor struct {
	user  string
	via   string // "session" | "device" | "app" | "cookie", "frame", "delegate"
	own   bool   // the person's own credential
	frame bool   // the agent's page, under the person's login
	agent bool   // the agent itself
	p     auth.Principal
}

// delegateActorOf judges p; ok false for everyone the file comment
// excludes.
func delegateActorOf(p auth.Principal) (delegateActor, bool) {
	a := delegateActor{user: p.UserID, via: p.Via, p: p}
	switch {
	case p.UserID == "" || p.Impersonator != "" || p.Owner:
		return a, false
	case p.Agent != "" && p.Via == "delegate" && p.Component == "":
		a.agent = true
	case p.Component == "" && p.Agent == "" && p.Via != "delegate":
		a.own = true
	case p.Via == "frame" && p.Deployment == "" && p.Partition != "global" && auth.HoldsDelegation(p.Component) && p.LoginFrame() && strings.HasPrefix(p.Gen, "s."):
		a.frame, a.via = true, "frame"
	default:
		return a, false
	}
	return a, true
}

// delegateCaller answers the actor of r when it is one of the kinds the
// route takes; otherwise it writes the 403.
func (s *Server) delegateCaller(w http.ResponseWriter, r *http.Request, own, frame, agent bool) (delegateActor, bool) {
	p := auth.PrincipalOf(r)
	a, ok := delegateActorOf(p)
	if !ok && s.devOwner(p) {
		// --no-auth: nobody signs in, so the implicit owner is the person
		a, ok = delegateActor{user: cmp.Or(r.URL.Query().Get("user"), "owner"), via: "dev", own: true, p: p}, true
	}
	if ok && (a.own && own || a.frame && frame || a.agent && agent) {
		if s.Delegation == nil {
			WriteError(w, http.StatusNotImplemented, "this xbind keeps no delegation state", delegationDocs)
			return a, false
		}
		return a, true
	}
	msg := "this is the person's own act: sign in and do it yourself — not from the agent's page, a tile or the agent"
	if agent && !own {
		msg = "only a person's workspace agent asks this"
	}
	WriteError(w, http.StatusForbidden, msg, delegationDocs)
	return a, false
}

// devOwner reports the implicit owner of an xbind run with --no-auth (and
// DelegationDev set): the principal every request without a credential
// gets there. Never the root token, a cookie or any credential of a real
// deployment.
func (s *Server) devOwner(p auth.Principal) bool {
	return s.DelegationDev && s.Auth != nil && s.Auth.NoAuth() && p.Owner && p.Via == "dev" && p.Component == "" && p.Agent == "" && p.Impersonator == ""
}

// mayOptInAdmin: a workspace admin or an org's admin.
func mayOptInAdmin(p auth.Principal) bool {
	p.Agent = "" // the person behind a delegate
	return p.IsAdmin() || p.Access != nil && len(p.Access.AdminOrgs()) > 0
}

// ---- events and pushes ----

// delegateChange is a `delegate` event's data: something about user's
// agent changed — op "request" (an approval request appeared or was
// decided), "approval" (a standing approval), "changeset", "state". It
// goes to the person's own sockets and their agent's page, nobody else's.
type delegateChange struct {
	Op   string `json:"op"`
	ID   string `json:"id,omitempty"`
	User string `json:"user"`
}

func (c delegateChange) VisibleTo(p auth.Principal) bool {
	if p.Owner && p.Via == "dev" { // --no-auth: every request is the owner
		return true
	}
	a, ok := delegateActorOf(p)
	return ok && !a.agent && a.user == c.User
}

func (s *Server) delegateEvent(user, op, id string) {
	if s.Hub != nil {
		s.Hub.Publish(events.Event{Type: "delegate", Data: delegateChange{Op: op, ID: id, User: user}})
	}
	s.notifyHolders(user, op, id)
}

// notifyHolders tells the backend of each holder that acts for user — each
// that has an agent session of theirs — that kind (request, approval,
// changeset, state) changed: a kind and an id, nothing more (the broker's
// holdernotify.go). The backend re-reads what changed.
func (s *Server) notifyHolders(user, kind, id string) {
	if s.HolderNotify == nil || s.Delegation == nil {
		return
	}
	seen := map[string]bool{}
	for _, h := range s.Delegation.AgentSessions(user, "") {
		if !seen[h.Agent] {
			seen[h.Agent] = true
			s.HolderNotify(h.Agent, user, kind, id)
		}
	}
}

// notifyAt tells holder — the agent that asked — at t that user's request
// id changed: a pending request or a ticket expires without anyone acting,
// and the backend should hear of that too. The request names its holder,
// so this doesn't depend on an agent session being alive by then.
func (s *Server) notifyAt(t time.Time, user, id, holder string) {
	if s.HolderNotify == nil || holder == "" {
		return
	}
	if d := time.Until(t) + time.Second; d > 0 && d < 48*time.Hour {
		time.AfterFunc(d, func() { s.HolderNotify(holder, user, "request", id) })
	}
}

// delegatePush tells user on their devices that their agent waits for
// them. The link is the holder's own path — the sealed component that is
// their agent — never a name written here.
func (s *Server) delegatePush(user, holder, kind, title, body, collapse string) {
	if s.DelegatePush != nil {
		s.DelegatePush(user, kind, title, body, holder, collapse)
	}
}

// ---- the switches ----

func (s *Server) delegateStateView(a delegateActor) map[string]any {
	st := s.Delegation.State(a.user)
	pend, _ := s.Delegation.Requests(a.user, true)
	aps, _ := s.Delegation.Approvals(a.user)
	sets, _ := s.Delegation.ChangeSets(a.user)
	open := 0
	for _, cs := range sets {
		if cs.Open() {
			open++
		}
	}
	ws := s.Delegation.Workspace()
	out := map[string]any{
		"user": a.user, "workspace": ws.Enabled,
		"paused": st.Paused, "adminChanges": st.AdminChanges, "canAdminChanges": mayOptInAdmin(a.p),
		"pendingRequests": len(pend), "standingApprovals": len(aps), "openChangeSets": open,
		// the web and MCP (D239): the person's own settings, and what the
		// workspace policy lets them turn on
		"web": st.Web, "mcp": st.MCP, "mcpReadOnly": st.MCPReadOnly, "net": s.netForPerson(a.user),
	}
	// what xbin's own chrome draws its states from (agentpage.go; D232)
	if !ws.At.IsZero() {
		out["workspaceAt"] = ws.At // the switch was set once: off now is "turned off", not "never set up"
	}
	if l := s.agentLocks(); l != nil {
		out["locks"] = l // what the host's file sets (agenthost.go): the chrome shows it read-only
	}
	if st.Paused && !st.PausedAt.IsZero() {
		out["pausedAt"] = st.PausedAt
	}
	if h := s.agentHolder(); h != "" {
		// the component whose page is the agent's conversation — for a
		// person who was given it; nobody has one until then (agentwho.go)
		access := s.agentAccess(a.p, h)
		out["access"] = access
		if access {
			out["agent"] = h
		}
		who, admin := s.agentWho(a.p, h)
		if admin {
			out["who"] = who // an admin: who was given one (the third setup step)
		}
		if (access || admin) && s.AgentHeld != nil { // an admin without one of their own still reads why nobody's runs
			if kind, why := s.AgentHeld(); kind != "" {
				out["held"] = kind // the chrome says why there is no conversation
				if admin || s.devOwner(a.p) {
					out["heldWhy"] = why // xbind's own reason: an admin's to act on
				}
			}
		}
	}
	return out
}

// apiDelegateState — GET /delegate/state: the person, their agent's page or
// their agent → the switches and what waits for the person.
func (s *Server) apiDelegateState(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, true, true)
	if !ok {
		return
	}
	WriteJSON(w, http.StatusOK, s.delegateStateView(a))
}

// apiDelegateStatePut — PUT /delegate/state {paused?, adminChanges?}: the
// person; their agent's page may pause ({paused: true}) and nothing else —
// resuming and the admin opt-in widen what the agent does, and are the
// person's own. Pausing stops the agent's calls at once, the ones running
// now included. adminChanges is an admin's (a workspace admin, or an
// org's): 403 for anyone else turning it on.
func (s *Server) apiDelegateStatePut(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, true, false)
	if !ok {
		return
	}
	var body struct {
		Paused       *bool `json:"paused"`
		AdminChanges *bool `json:"adminChanges"`
		// the web and MCP (D239): what widens is the person's own
		Web         *bool `json:"web"`
		MCP         *bool `json:"mcp"`
		MCPReadOnly *bool `json:"mcpReadOnly"`
	}
	net := func() bool { return body.Web != nil || body.MCP != nil || body.MCPReadOnly != nil }
	if err := DecodeJSON(r, &body); err != nil || body.Paused == nil && body.AdminChanges == nil && !net() {
		WriteError(w, http.StatusBadRequest, "need {paused?, adminChanges?, web?, mcp?, mcpReadOnly?: bool} with at least one key", delegationDocs)
		return
	}
	if !a.own && (body.AdminChanges != nil || net() || body.Paused == nil || !*body.Paused) {
		WriteError(w, http.StatusForbidden, "the agent's page may pause it; resuming it, letting it propose admin changes, read the web or use MCP tools are decided in your own session", delegationDocs)
		return
	}
	if refusal, msg := s.netSettingRefusal(body.Web, body.MCP, body.MCPReadOnly); refusal != "" {
		WriteJSON(w, http.StatusConflict, map[string]any{"error": msg, "refusal": refusal, "docs": delegationDocs})
		return
	}
	if body.AdminChanges != nil {
		switch {
		case *body.AdminChanges && !mayOptInAdmin(a.p):
			WriteError(w, http.StatusForbidden, "only a workspace admin or an org's admin has admin changes for an agent to propose", delegationDocs)
			return
		}
	}
	st, err := s.Delegation.SetState(a.user, func(st *delegation.State) {
		if body.Paused != nil {
			st.Paused = *body.Paused
		}
		if body.AdminChanges != nil {
			st.AdminChanges = *body.AdminChanges
		}
		if body.Web != nil {
			st.Web = *body.Web
		}
		if body.MCP != nil {
			st.MCP = *body.MCP
		}
		if body.MCPReadOnly != nil {
			st.MCPReadOnly = *body.MCPReadOnly
		}
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "the agent's settings can't be written", delegationDocs)
		return
	}
	if st.Paused {
		s.delegationStopped(a.user, true) // calls in flight, unused tickets, held messages; standing approvals stay
	}
	s.delegateEvent(a.user, "state", "")
	WriteJSON(w, http.StatusOK, s.delegateStateView(a))
}

// apiDelegateWorkspace — GET /delegate/workspace: admin → {enabled, by, at}.
func (s *Server) apiDelegateWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.settingsAdmin(r) || auth.PrincipalOf(r).Agent != "" {
		WriteError(w, http.StatusForbidden, "admin only", delegationDocs)
		return
	}
	WriteJSON(w, http.StatusOK, s.delegateWorkspaceView(auth.PrincipalOf(r)))
}

// apiDelegateWorkspacePut — PUT /delegate/workspace {enabled}: admin. Off
// stops every person's agent at once.
func (s *Server) apiDelegateWorkspacePut(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if !s.settingsAdmin(r) || p.Agent != "" {
		WriteError(w, http.StatusForbidden, "admin only", delegationDocs)
		return
	}
	var body struct {
		Enabled *bool   `json:"enabled"`
		Who     *string `json:"who"`
		// the web and MCP policy (D239): what a person's agent may do there
		Web *struct {
			Mode   *string   `json:"mode"`
			Allow  *[]string `json:"allow"`
			Deny   *[]string `json:"deny"`
			Search *bool     `json:"search"`
		} `json:"web"`
		MCP *mcpWorkspaceIn `json:"mcp"` // mode, and the outside servers (mcpextpolicy.go, D240)
	}
	if err := DecodeJSON(r, &body); err != nil || body.Enabled == nil && body.Who == nil && body.Web == nil && body.MCP == nil {
		WriteError(w, http.StatusBadRequest, `need {enabled?: bool, who?: "everyone" | "me" | "chosen", web?: {mode?, allow?, deny?, search?}, mcp?: {mode?, servers?}} with at least one key`, delegationDocs)
		return
	}
	defer s.hostAgentHold()()                        // the host's check and this write under one lock (agenthost.go)
	if s.hostAgentWrite(w, body.Enabled, body.Who) { // the host's file sets it
		return
	}
	if body.Web != nil || body.MCP != nil { // before the switch, so a refusal changes nothing
		var np delegation.NetPatch
		if body.Web != nil {
			np.WebMode, np.WebAllow, np.WebDeny, np.WebSearch = body.Web.Mode, body.Web.Allow, body.Web.Deny, body.Web.Search
		}
		var err error
		if body.MCP != nil {
			np.MCPMode = body.MCP.Mode
			if np.MCPServers, err = s.mcpServersPatch(body.MCP.Servers); err != nil {
				netWriteError(w, err)
				return
			}
		}
		if _, err := s.Delegation.SetNet(np, p.From()); err != nil {
			netWriteError(w, err)
			return
		}
		s.netPolicyChanged()
		if np.MCPServers != nil {
			s.SweepDelegation() // approvals of a server that left the list, or moved, go now
		}
		if body.Enabled == nil && body.Who == nil {
			WriteJSON(w, http.StatusOK, s.delegateWorkspaceView(p))
			return
		}
	}
	if body.Who != nil { // who gets an agent (agentwho.go): before the switch, so a refusal changes nothing
		if code, msg := s.setAgentWho(p, *body.Who); code != 0 {
			WriteError(w, code, msg, delegationDocs)
			return
		}
		if body.Enabled == nil {
			s.delegateEvent(p.UserID, "state", "")
			WriteJSON(w, http.StatusOK, s.delegateWorkspaceView(p))
			return
		}
	}
	ws, err := s.Delegation.SetWorkspace(*body.Enabled, p.From())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "the workspace's switch can't be written", delegationDocs)
		return
	}
	if !ws.Enabled {
		s.CutDelegateStreams("")
	}
	// Every person's agent: delegation went on or off. Their backend is told,
	// and so are their own sockets (a `delegate` state event): the chrome and
	// the app show the switch at once, not at their next read.
	told := map[string]bool{}
	for _, u := range s.Delegation.People() {
		told[u] = true
		s.delegateEvent(u, "state", "")
		if !ws.Enabled { // off: nothing approved before the switch is waiting when it is on again
			s.delegationStopped(u, false)
		}
	}
	if !told[p.UserID] { // the admin who turned it, with no record of their own yet
		s.delegateEvent(p.UserID, "state", "")
	}
	WriteJSON(w, http.StatusOK, s.delegateWorkspaceView(p))
}

// apiDelegateAudit — GET /delegate/audit[?before=<RFC 3339>&limit=<n>]:
// "what my agent did" — the caller's own rows, newest first. The person's
// own session, app or device only: never their agent (it would read what
// it was refused), its page, a tile, view-as or the root token. An admin
// reads another person's with ?user=.
func (s *Server) apiDelegateAudit(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, false, false)
	if !ok {
		return
	}
	user := a.user
	if u := r.URL.Query().Get("user"); u != "" && u != user {
		if !a.p.IsAdmin() {
			WriteError(w, http.StatusForbidden, "another person's agent log is an admin's to read", delegationDocs)
			return
		}
		user = u
	}
	var before time.Time
	if b := r.URL.Query().Get("before"); b != "" {
		t, err := time.Parse(time.RFC3339, b)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "before is an RFC 3339 time", delegationDocs)
			return
		}
		before = t
	}
	limit := 0
	for _, c := range r.URL.Query().Get("limit") {
		if c < '0' || c > '9' || limit > 1000 {
			limit = 0
			break
		}
		limit = limit*10 + int(c-'0')
	}
	rows, skipped, err := s.delegateAuditLog().ReadCounted(user, before, limit)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "the log can't be read", delegationDocs)
		return
	}
	out := map[string]any{"user": user, "calls": rows}
	if skipped > 0 {
		out["skipped"] = skipped // lines of the files read that were no row: say so, don't hide them
	}
	WriteJSON(w, http.StatusOK, out)
}

// ---- approval requests ----

// apiDelegateRequestNew — POST /delegate/requests {method, path, body? |
// bodyBase64?, summary?}: the agent asks its person to approve one side
// effect — a non-GET call to a tile's API, a POST /term/sessions or a
// POST /templates/new — exactly as it will send it. xbind judges the call
// as the gate will (the cap, the tile's rules, the person's level), stores
// it pending with its own description, and tells the person. →
// the request. 400 for a call that needs no approval or isn't delegated;
// 403 for one the tile or its org refuses anyway; 409 past the pending
// limit.
func (s *Server) apiDelegateRequestNew(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, false, false, true)
	if !ok {
		return
	}
	p := a.p
	r.Body = http.MaxBytesReader(w, r.Body, delegation.MaxBody*2)
	var in struct {
		Method     string `json:"method"`
		Path       string `json:"path"`
		Body       string `json:"body"`
		BodyBase64 string `json:"bodyBase64"`
		// ContentType is the media type the call's body will be sent as:
		// bound like the body. Absent with a body: application/json.
		ContentType *string `json:"contentType"`
		Summary     string  `json:"summary"`
	}
	if err := DecodeJSON(r, &in); err != nil || in.Method == "" || in.Path == "" {
		WriteError(w, http.StatusBadRequest, "need {method, path, body? | bodyBase64?, contentType?, summary?}", delegationDocs)
		return
	}
	body := []byte(in.Body)
	if in.BodyBase64 != "" {
		b, err := base64.StdEncoding.DecodeString(in.BodyBase64)
		if err != nil || in.Body != "" {
			WriteError(w, http.StatusBadRequest, "bodyBase64 is standard base64, given instead of body", delegationDocs)
			return
		}
		body = b
	}
	method := strings.ToUpper(in.Method)
	u, err := url.ParseRequestURI(in.Path)
	if err != nil || u.Host != "" || u.Scheme != "" || !strings.HasPrefix(u.Path, "/api/") {
		WriteError(w, http.StatusBadRequest, "path is the call's path under /api/, with its query", delegationDocs)
		return
	}
	sr, err := http.NewRequestWithContext(r.Context(), method, u.RequestURI(), nil)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "not a method and a path", delegationDocs)
		return
	}
	v := s.delegateVerdict(sr, p)
	switch {
	case v.deny != "":
		WriteError(w, http.StatusBadRequest, "this call isn't delegated: "+v.deny, delegationDocs)
		return
	case !v.effect || !(v.tileAPI || v.row.ticket || v.pattern == webFetchPattern || v.pattern == mcpCallPattern):
		WriteError(w, http.StatusBadRequest, "this call needs no approval: make it", delegationDocs)
		return
	case v.tileAPI && s.mcpEndpointOf(u.Path) != "":
		// an MCP server's tools are called through xbind, each approved as itself
		WriteError(w, http.StatusBadRequest, "a call to an MCP server's endpoint isn't approved as a tile call: call the tool with POST /api/xbin/delegate/mcp/call {server, tool, arguments} and ask for that", delegationDocs)
		return
	}
	if u.Query().Has("_method") {
		WriteError(w, http.StatusBadRequest, "this call isn't delegated: it names another method in its query", delegationDocs)
		return
	}
	ctype := ""
	switch {
	case in.ContentType != nil:
		ctype = contentTypeOf(*in.ContentType)
	case len(body) > 0:
		ctype = "application/json"
	}
	req := delegation.Request{User: a.user, Agent: p.Agent, Method: method, Target: requestTarget(sr), Tile: v.tile, ContentType: ctype, Summary: in.Summary, Kind: delegation.KindCall}
	preview := map[string]any{"class": v.class, "reversible": false}
	switch {
	case v.tileAPI:
		dp, ok := s.policy().(DelegatePolicy)
		if !ok {
			WriteError(w, http.StatusForbidden, "this xbind has no tile routing for a person's agent", delegationDocs)
			return
		}
		if s.tileHidden(p, v.tile) {
			WriteError(w, http.StatusForbidden, noTileFor(p.UserID), delegationDocs)
			return
		}
		level, role, _, err := dp.DelegateTile(p, v.tile, true)
		if err != nil {
			WriteError(w, http.StatusForbidden, err.Error(), delegationDocs)
			return
		}
		preview["tile"], preview["owner"], preview["level"], preview["role"] = v.tile, s.policy().OwnerOf(v.tile), level, role
		// what each standing choice would let the agent do from now on, in
		// xbind's words: the wider one says that it is
		preview["standing"] = map[string]string{
			delegation.ScopeCall: method + " " + u.EscapedPath() + " — this call again, with any body or query, without asking",
			delegation.ScopeTile: "EVERY write to " + v.tile + "'s API — any method, any path, any body — without asking",
		}
	case v.pattern == "POST /term/sessions":
		req.Kind = delegation.KindSession
		ask, why := s.sessionPreview(p, body)
		if why != "" {
			WriteError(w, http.StatusForbidden, why, delegationDocs)
			return
		}
		req.Tile = ask["tile"].(string)
		preview["tile"], preview["owner"], preview["session"] = req.Tile, s.policy().OwnerOf(req.Tile), ask
	case v.pattern == mcpCallPattern: // one tool of an MCP tile (delegatemcp.go)
		req.Kind = delegation.KindMCP
		mp, tile, status, why := s.mcpRequestPreview(r.Context(), p, body)
		if why != "" {
			WriteError(w, status, why, delegationDocs)
			return
		}
		req.Tile = tile
		for k, v := range mp {
			preview[k] = v
		}
	case v.pattern == webFetchPattern: // a URL the conversation may read (delegateweb.go)
		req.Kind = delegation.KindWeb
		wp, status, why := s.webRequestPreview(p, body)
		if why != "" {
			WriteError(w, status, why, delegationDocs)
			return
		}
		for k, v := range wp {
			preview[k] = v
		}
	default:
		req.Kind = delegation.KindTemplate
		// the handler's own check, before anything is put in front of the
		// person: what would be created, where and whose (delegatedry.go)
		create, why := s.createPreview(p, body)
		if why != "" {
			WriteError(w, http.StatusForbidden, why, delegationDocs)
			return
		}
		preview["create"] = create
	}
	if h, ok := s.handleOf(p); ok && h.Web {
		preview["readWeb"] = true // what it asks may come from a page it read
	}
	if len(body) > 0 && !delegation.TextBody(body) {
		// xbind's words for what can't be shown: the person approves this
		// many bytes with this hash, unread, or doesn't
		preview["body"] = map[string]any{"binary": true, "size": len(body), "sha256": delegation.BodyHash(body)}
	}
	req.Preview = preview
	out, err := s.Delegation.NewRequest(req, body)
	switch {
	case errors.Is(err, delegation.ErrTooMany):
		WriteError(w, http.StatusConflict, err.Error(), delegationDocs)
		return
	case err != nil:
		WriteError(w, http.StatusBadRequest, err.Error(), delegationDocs)
		return
	}
	s.delegateEvent(a.user, "request", out.ID)
	s.notifyAt(out.Expires, a.user, out.ID, out.Agent)
	what := method + " " + strings.TrimPrefix(u.Path, "/api/")
	if out.Kind == delegation.KindSession {
		what = "start a tile agent on " + out.Tile
	}
	// one notification for what waits, not one a request: the same
	// collapse key, a count, and at most one a minute
	if s.delegateRT.pushDue(a.user, "agent.approval") {
		text := "It wants to " + what + ". Open your agent to see exactly what, and approve or deny it."
		if pend, _ := s.Delegation.Requests(a.user, true); len(pend) > 1 {
			text = fmt.Sprintf("%d requests wait for your approval. Open your agent to see exactly what each is, and approve or deny it.", len(pend))
		}
		s.delegatePush(a.user, p.Agent, "agent.approval", "Your agent asks for your approval", text, "agent-approval")
	}
	WriteJSON(w, http.StatusOK, out)
}

// sessionPreview is what a person is shown of a POST /term/sessions body
// their agent wants approved — or why the start would be refused anyway.
// It shows what the start AMOUNTS TO (term.DelegateEffective): the harness
// and mode a resumed session takes from its history entry, not only what
// the body says; every option the body carries; whether that mode asks
// before acting; the sandbox pickers. The ticket is bound to the harness
// and mode shown here (delegateApproval hands them to the open), and to
// the body — so to every option — by its hash.
func (s *Server) sessionPreview(p auth.Principal, body []byte) (map[string]any, string) {
	var b struct {
		Cwd, Kind, Provider, Mode, Net, GPU, Name, Model, Resume string
		API                                                      *bool
		VM                                                       bool
		Options                                                  map[string]string
		Prompt                                                   string
	}
	if json.Unmarshal(body, &b) != nil || b.Cwd == "" {
		return nil, "a session start names its tile: {cwd, kind:\"agent\", provider?, …}"
	}
	if len(b.Prompt) > maxStartPrompt {
		return nil, fmt.Sprintf("a start's first prompt is at most %d KiB", maxStartPrompt>>10)
	}
	if _, why := s.startRefusal(p, sessionAskOf(body)); why != "" {
		return nil, why // no approval makes it startable: don't put it in front of the person
	}
	// what the request leaves out, xbind fills from the person's
	// preferences for sessions their agent starts and the tile's orgs'
	// rules (startDefaults) — and shows: the card says the EFFECTIVE values
	if def := s.startDefaults(p, strings.Trim(b.Cwd, "/")); b.Resume == "" {
		if b.Provider == "" {
			if def.Harness == "" {
				return nil, def.Why
			}
			b.Provider = def.Harness
		}
		if b.Net == "" {
			b.Net = def.netFor(b.Provider)
		}
		if b.Mode == "" {
			b.Mode = def.modeFor(b.Provider)
		}
	}
	if s.Term == nil {
		return nil, "terminals are not enabled"
	}
	eff, _, err := s.Term.DelegateEffective(p, term.AgentOpen{Cwd: b.Cwd, Provider: b.Provider, Mode: b.Mode, Resume: b.Resume})
	if err != nil {
		return nil, err.Error()
	}
	if err := s.agentPolicy().SessionAllows(delegation.Subject{Holder: p.Agent, User: p.UserID, Tile: eff.Tile, Model: p.AgentModel}, eff.Provider.ID); err != nil {
		return nil, err.Error()
	}
	options := map[string]string{}
	for k, v := range b.Options {
		options[k] = v
	}
	if b.Model != "" { // the body's shorthand for options.model
		options["model"] = b.Model
	}
	return map[string]any{
		"tile": eff.Tile, "provider": eff.Provider.ID, "providerName": eff.Provider.Name, "mode": eff.Mode,
		// false: this mode lets the tile agent act without asking. The mode
		// alone decides it: a delegate's start is strict (acp.Config.Strict)
		// — the mode is in force or the session ends, and no option but a
		// model or a thinking level is set
		"asks":    eff.Provider.Asks(eff.Mode),
		"options": options, // every setting the start carries; none of them can change the mode
		// what the session will be told first: sent by xbind itself once the
		// session is up, and bound by the ticket with the rest of the body.
		// Nothing the agent sends the session later is approved one by one.
		"prompt": b.Prompt,
		"net":    b.Net, "api": b.API == nil || *b.API, "vm": b.VM, "gpu": b.GPU, "model": options["model"], "name": b.Name, "resume": eff.Resumes,
	}, ""
}

// apiDelegateRequests — GET /delegate/requests[?all=1]: the person or
// their agent's page → their pending requests (all: the decided ones of the
// last day too), oldest first.
func (s *Server) apiDelegateRequests(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, true, false)
	if !ok {
		return
	}
	rows, err := s.Delegation.Requests(a.user, r.URL.Query().Get("all") == "")
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "the requests can't be read", delegationDocs)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"requests": rows})
}

// apiDelegateRequest — GET /delegate/requests/{id}: the person, their
// agent's page or their agent → one request (its state says whether it is
// a ticket yet).
func (s *Server) apiDelegateRequest(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, true, true)
	if !ok {
		return
	}
	req, found := s.Delegation.Request(a.user, r.PathValue("id"))
	if !found {
		WriteError(w, http.StatusNotFound, "no such request", delegationDocs)
		return
	}
	WriteJSON(w, http.StatusOK, req)
}

// apiDelegateRequestDecide — POST /delegate/requests/{id}/approve
// [{grant: "once" | "hour" | "always", scope?: "call" | "tile"}] and …/deny: the person, in their
// own session, app or device — never the agent's page, which shows the
// card and renders what the agent wrote. once (the default) makes the request a ticket for exactly
// that call, good for 15 minutes; hour and always — a tile API call only —
// also record a standing approval of that tile's API. → {request,
// approval?}.
func (s *Server) apiDelegateRequestDecide(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, false, false)
	if !ok {
		return
	}
	grant, scope, unseen := "", "", false
	if strings.HasSuffix(r.URL.Path, "/approve") {
		var body struct {
			Grant string `json:"grant"`
			Scope string `json:"scope"`
			// Unseen: "I approve a body I couldn't be shown" — needed, and
			// only meaningful, for a request whose body is binary
			Unseen bool `json:"unseen"`
		}
		if r.ContentLength != 0 {
			if err := DecodeJSON(r, &body); err != nil {
				WriteError(w, http.StatusBadRequest, `need {grant?: "once" | "hour" | "always", scope?: "call" | "tile", unseen?: true}`, delegationDocs)
				return
			}
		}
		grant, scope, unseen = body.Grant, body.Scope, body.Unseen
		if grant == "" {
			grant = delegation.GrantOnce
		}
		// allowing is refused while the agent is stopped (denying never is):
		// a ticket or a standing approval given now would be live the moment
		// it runs again
		if cur, found := s.Delegation.Request(a.user, r.PathValue("id")); found && cur.State == delegation.Pending {
			if refusal, msg := s.decideRefusal(a.user, cur.Agent); refusal != "" {
				refuseDecision(w, refusal, msg)
				return
			}
		}
	}
	req, ap, err := s.Delegation.DecideWith(a.user, r.PathValue("id"), grant, scope, a.via, unseen)
	switch {
	case errors.Is(err, delegation.ErrUnseenBody):
		// say what would be approved: bytes of this size and hash, unread
		WriteJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "unseen": true, "bodySize": req.BodySize, "bodySha256": req.BodySHA256, "docs": delegationDocs})
		return
	case errors.Is(err, delegation.ErrNoRequest):
		WriteError(w, http.StatusNotFound, err.Error(), delegationDocs)
		return
	case errors.Is(err, delegation.ErrNotPending):
		WriteError(w, http.StatusConflict, err.Error(), delegationDocs)
		return
	case err != nil:
		WriteError(w, http.StatusBadRequest, err.Error(), delegationDocs)
		return
	}
	if req.State == delegation.Approved {
		// a stop that began between the check above and this approval: what
		// it would have voided goes now (the stop sets its switch before it
		// voids, so one of the two sees the other)
		if refusal, msg := s.decideRefusal(a.user, req.Agent); refusal != "" {
			_, _ = s.Delegation.VoidTickets(a.user)
			if ap != nil {
				_, _ = s.Delegation.DropApprovals(a.user, func(x delegation.Approval) bool { return x.ID == ap.ID })
			}
			s.delegateEvent(a.user, "request", req.ID)
			refuseDecision(w, refusal, msg)
			return
		}
		s.notifyAt(req.Expires, a.user, req.ID, req.Agent) // a ticket nobody used
	}
	out := map[string]any{"request": req}
	if ap != nil {
		out["approval"] = ap
		s.delegateEvent(a.user, "approval", ap.ID)
	}
	under := req.Grant
	if ap != nil {
		under += " " + ap.Scope
	}
	s.delegateAudit(delegation.Call{User: a.user, Agent: req.Agent, Method: req.Method, Path: req.Path(), Tile: req.Tile,
		Class: "decision:" + req.State, Effect: true, Status: http.StatusOK, Ticket: req.ID, Under: under})
	s.delegateEvent(a.user, "request", req.ID)
	WriteJSON(w, http.StatusOK, out)
}

// apiDelegateRequestWithdraw — DELETE /delegate/requests/{id}: the agent
// (it no longer wants it) or the person.
func (s *Server) apiDelegateRequestWithdraw(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, false, true)
	if !ok {
		return
	}
	req, err := s.Delegation.Withdraw(a.user, r.PathValue("id"))
	switch {
	case errors.Is(err, delegation.ErrNoRequest):
		WriteError(w, http.StatusNotFound, err.Error(), delegationDocs)
		return
	case err != nil:
		WriteError(w, http.StatusConflict, err.Error(), delegationDocs)
		return
	}
	s.delegateEvent(a.user, "request", req.ID)
	WriteJSON(w, http.StatusOK, req)
}

// ---- standing approvals ----

// apiDelegateApprovals — GET /delegate/approvals: the person, their agent's
// page or their agent → the standing approvals that hold now.
func (s *Server) apiDelegateApprovals(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, true, true)
	if !ok {
		return
	}
	rows, err := s.Delegation.Approvals(a.user)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "the approvals can't be read", delegationDocs)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"approvals": rows})
}

// apiDelegateApprovalAdd — POST /delegate/approvals {tile, grant: "hour" |
// "always"}: the person, in their own session, lets the agent call that
// tile's API without asking each time. The tile must be one they can read.
func (s *Server) apiDelegateApprovalAdd(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, false, false)
	if !ok {
		return
	}
	var body struct {
		Tile   string `json:"tile"`
		Grant  string `json:"grant"`
		Scope  string `json:"scope"`
		Method string `json:"method"`
		Path   string `json:"path"`
	}
	// the scope is said, never guessed: one call (its method and path), or
	// — explicitly — the whole tile
	const need = `need {tile, grant: "hour" | "always", scope: "call", method, path} or {tile, grant, scope: "tile"}`
	if err := DecodeJSON(r, &body); err != nil || body.Tile == "" || body.Scope != delegation.ScopeCall && body.Scope != delegation.ScopeTile {
		WriteError(w, http.StatusBadRequest, need, delegationDocs)
		return
	}
	tile := strings.Trim(body.Tile, "/")
	if util.SealedPath(tile) { // no standing approval names one: a person's agent does nothing in it (D227)
		WriteError(w, http.StatusBadRequest, SealedRefusal(util.CanonPath(tile), sealedNoAgentWhat), SealedDocs)
		return
	}
	if body.Scope == delegation.ScopeCall {
		u, err := url.ParseRequestURI(body.Path)
		if err != nil || u.RawQuery != "" || body.Method == "" || isRead(strings.ToUpper(body.Method)) || !strings.HasPrefix(u.Path, "/api/"+tile+"/") {
			WriteError(w, http.StatusBadRequest, need+" — path is the call's path under /api/"+tile+"/, without a query; method a writing one", delegationDocs)
			return
		}
		body.Method, body.Path = strings.ToUpper(body.Method), u.EscapedPath()
	}
	known := s.Reg == nil
	if s.Reg != nil {
		_, known = s.Reg.Component(tile)
	}
	if !known || !a.p.CanReadTile(tile) {
		WriteError(w, http.StatusNotFound, "no tile "+tile+" that you can read", delegationDocs)
		return
	}
	if refusal, msg := s.decideRefusal(a.user, ""); refusal != "" { // as for allowing a request "always"
		refuseDecision(w, refusal, msg)
		return
	}
	ap, err := s.Delegation.AddApproval(delegation.Approval{User: a.user, Tile: tile, Scope: body.Scope, Method: body.Method, Path: body.Path}, body.Grant, a.via)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error(), delegationDocs)
		return
	}
	s.delegateAudit(delegation.Call{User: a.user, Method: "POST", Path: "/api/xbin/delegate/approvals", Tile: tile, Class: "decision:approval", Effect: true, Status: http.StatusOK, Under: body.Grant + " " + body.Scope})
	s.delegateEvent(a.user, "approval", ap.ID)
	WriteJSON(w, http.StatusOK, ap)
}

// apiDelegateApprovalRevoke — DELETE /delegate/approvals/{id}: the person.
// The next call needs a ticket again.
func (s *Server) apiDelegateApprovalRevoke(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, false, false)
	if !ok {
		return
	}
	found, err := s.Delegation.RevokeApproval(a.user, r.PathValue("id"))
	switch {
	case err != nil:
		WriteError(w, http.StatusInternalServerError, "the approvals can't be written", delegationDocs)
		return
	case !found:
		WriteError(w, http.StatusNotFound, "no such approval", delegationDocs)
		return
	}
	s.SweepDelegation() // a call running under it ends now
	s.delegateEvent(a.user, "approval", r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}
