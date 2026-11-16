package server

// mcpext.go — a person's workspace agent with the MCP servers OUTSIDE the
// workspace an admin listed (mcp.mode "listed", D240; docs/protocol/
// delegation.md §Outside MCP servers). xbind is the client
// (internal/mcpclient): the agent names a server "ext:<id>" and never sees
// its address beyond the host, its secret or its session.
//
//   - GET /delegate/mcp/servers: the servers this person's agent may use,
//     each with the conversation's state (on, asks each call, off);
//   - POST /delegate/mcp/tools {server}: xbind's initialize and tools/list
//     — free: nothing of the conversation is sent;
//   - POST /delegate/mcp/call {server: "ext:<id>", …} (delegatemcp.go's
//     route, here for an outside server): judged as a tile's tool is — the
//     tool's first call asks, a standing approval of it pinned to its
//     definition — and, because the arguments leave the workspace, as a
//     URL is: from a conversation that holds an org's data each call asks
//     unless the org trusts the list (its rule listed) or the server is
//     listed with it; the org's never and org rules refuse;
//   - POST /delegate/mcp/test {id}: an admin's initialize and tools/list.
//
// What a server answers taints no org — it isn't the workspace's data —
// and marks the conversation as having read the web.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/agentpolicy"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/mcpclient"
)

const (
	mcpServersPattern = "GET /delegate/mcp/servers"
	mcpToolsPattern   = "POST /delegate/mcp/tools"
	mcpTestPattern    = "POST /delegate/mcp/test"
)

func (s *Server) registerDelegateMCPExtAPI() {
	s.RegisterAPI(mcpServersPattern, s.apiDelegateMCPServers)
	s.RegisterAPI(mcpToolsPattern, s.apiDelegateMCPTools)
	s.RegisterAPI(mcpTestPattern, s.apiDelegateMCPTest)
}

// mcpExtRuntime holds xbind's sessions with outside servers: one per
// person, server and conversation, in memory, never answered.
type mcpExtRuntime struct {
	mu   sync.Mutex
	sess map[string]mcpExtSession
}

type mcpExtSession struct {
	s  mcpclient.Session
	at time.Time
}

const (
	mcpExtSessionIdle = 30 * time.Minute
	mcpExtSessionMax  = 4096
)

func mcpExtSessionKey(user string, m delegation.MCPServer, conv string) string {
	return user + "\x00" + m.Key() + "\x00" + conv
}

func (r *mcpExtRuntime) get(k string) (mcpclient.Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.sess[k]
	if !ok || time.Since(e.at) > mcpExtSessionIdle {
		delete(r.sess, k)
		return mcpclient.Session{}, false
	}
	e.at = time.Now()
	r.sess[k] = e
	return e.s, true
}

func (r *mcpExtRuntime) put(k string, s mcpclient.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sess == nil {
		r.sess = map[string]mcpExtSession{}
	}
	if len(r.sess) >= mcpExtSessionMax { // the ones unused longest go first: here, every one idle past half the limit, else all
		for key, e := range r.sess {
			if time.Since(e.at) > mcpExtSessionIdle/2 {
				delete(r.sess, key)
			}
		}
		if len(r.sess) >= mcpExtSessionMax {
			r.sess = map[string]mcpExtSession{}
		}
	}
	r.sess[k] = mcpExtSession{s: s, at: time.Now()}
}

func (r *mcpExtRuntime) drop(k string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sess, k)
}

// mcpExtClient is the client outside servers are spoken to with (tests set
// MCPClient: a resolver of their own, their server's CA).
func (s *Server) mcpExtClient() *mcpclient.Client {
	if s.MCPClient != nil {
		return s.MCPClient
	}
	return defaultMCPClient
}

var defaultMCPClient = &mcpclient.Client{}

// mcpExtTarget is m as the client reaches it, with its secret — or why it
// can't be reached now.
func (s *Server) mcpExtTarget(m delegation.MCPServer) (mcpclient.Server, int, string, string) {
	u, err := url.Parse(m.URL)
	if err != nil || u.Host == "" {
		return mcpclient.Server{}, http.StatusBadGateway, "mcp-unreachable", m.Name + "'s address can't be used"
	}
	if s.ownHost(u.Hostname()) {
		return mcpclient.Server{}, http.StatusForbidden, "mcp-address", u.Hostname() + " is this workspace's own address: its MCP tiles are used as tiles"
	}
	t := mcpclient.Server{URL: u, Private: m.PrivateNetwork}
	sec, err := s.Delegation.MCPSecret(m)
	if err != nil {
		var su *delegation.ErrSecretUnreadable
		if errors.As(err, &su) && su.Sealed {
			return t, http.StatusServiceUnavailable, "mcp-sealed", su.Msg
		}
		return t, http.StatusBadGateway, "mcp-auth", err.Error()
	}
	switch m.Auth {
	case delegation.AuthBearer:
		t.Header, t.Value = "Authorization", "Bearer "+sec.Reveal()
	case delegation.AuthHeader:
		t.Header, t.Value = m.Header, sec.Reveal()
	}
	t.Secret = sec.Reveal()
	return t, 0, "", ""
}

// mcpExtOpen is the conversation's session with m and m's tools: the one
// kept, or a new one (a server that forgot it gets a new one, once).
func (s *Server) mcpExtOpen(ctx context.Context, key string, t mcpclient.Server) (mcpclient.Session, []mcpTool, error) {
	c := s.mcpExtClient()
	for try := 0; ; try++ {
		sess, kept := s.mcpExt.get(key)
		if !kept {
			var err error
			if sess, err = c.Open(ctx, t); err != nil {
				return sess, nil, err
			}
			if key != "" {
				s.mcpExt.put(key, sess)
			}
		}
		defs, err := c.Tools(ctx, t, sess)
		if errors.Is(err, mcpclient.ErrSessionGone) && try == 0 {
			s.mcpExt.drop(key)
			continue
		}
		if err != nil {
			return sess, nil, err
		}
		tools := make([]mcpTool, 0, len(defs))
		for _, def := range defs {
			raw, _ := json.Marshal(def)
			var tl mcpTool
			if json.Unmarshal(raw, &tl) != nil {
				return sess, nil, &mcpclient.Error{Code: "mcp-protocol", Status: http.StatusBadGateway, Msg: "the server's tools/list can't be read", Heard: true}
			}
			tl.def = def
			tools = append(tools, tl)
		}
		return sess, tools, nil
	}
}

// mcpExtFail is a client error as a refusal: xbind's own sentence (msg,
// never anything the server wrote), what the server itself wrote that the
// agent may read as an outsider's words (said), and an excerpt of its error
// page for an admin's Test alone (page).
type mcpExtFail struct {
	status          int
	code, msg, said string
	page            string
}

func mcpExtErr(err error) mcpExtFail {
	var e *mcpclient.Error
	switch {
	case errors.As(err, &e):
		return mcpExtFail{e.Status, e.Code, e.Msg, e.Said, e.Page}
	case errors.Is(err, mcpclient.ErrSessionGone):
		return mcpExtFail{status: http.StatusBadGateway, code: "mcp-unreachable", msg: "the server ended the session twice"}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return mcpExtFail{status: http.StatusGatewayTimeout, code: "mcp-timeout", msg: "the call ended before the server answered"}
	}
	return mcpExtFail{status: http.StatusBadGateway, code: "mcp-unreachable", msg: "the server didn't answer"}
}

// mcpExtRefuse answers a refusal of an outside server's list or call: the
// server's own words, when there are any, apart (said) — the agent frames
// them as untrusted data, never as xbind's.
func mcpExtRefuse(w http.ResponseWriter, row *delegation.Call, f mcpExtFail) {
	if row != nil {
		row.Refused = f.msg
	}
	out := map[string]any{"error": f.msg, "refusal": f.code, "class": "mcp", "docs": delegationDocs}
	if f.said != "" {
		out["said"] = f.said
	}
	WriteJSON(w, f.status, out)
}

// mcpExtHeard marks the conversation under h as having read an outside
// server — its words, whatever they were, reached xbind (err nil: an
// answer; an error after an answer too).
func (s *Server) mcpExtHeard(p auth.Principal, h delegation.AgentSession, err error) {
	if mcpclient.Heard(err) {
		_ = s.Delegation.MarkWeb(p.UserID, p.Agent, h.ID)
	}
}

// mcpExtVisible is the outside server named server ("ext:<id>") in force
// and visible to user, or false — one answer for none and not theirs.
func (s *Server) mcpExtVisible(user, server string) (delegation.MCPServer, bool) {
	for _, m := range s.mcpVisible(user) {
		if m.Server() == server {
			return m, true
		}
	}
	return delegation.MCPServer{}, false
}

func noExtServer(server string) string {
	return "no outside MCP server " + clipText(server, 80) + " for your person's agent (GET /api/xbin/delegate/mcp/servers lists the ones it may use)"
}

// mcpExtOrgRule is what the orgs whose data the conversation holds say of
// m: a refusal (never; org for a server not listed with it), or the orgs
// for which each call asks (any, for a server not listed with it).
func (s *Server) mcpExtOrgRule(h delegation.AgentSession, m delegation.MCPServer) (each []string, why string) {
	for _, org := range h.Tainted {
		rule, reason := agentpolicy.MCPAny, ""
		if nr, ok := s.AgentWorld.(agentNetRules); ok {
			rule, reason = nr.AgentMCPIn(org)
		}
		if rule == "" { // no MCP rule of its own: an org that keeps its data off the web keeps it from outside servers too
			if web, wreason := s.orgWeb(org); web == agentpolicy.WebNever {
				return nil, orStr(wreason, orgName(org)+" keeps its data off the web") + ", and this conversation holds its data: an outside server is the web — " +
					m.Name + " isn't for this conversation (the org can trust the listed servers with mcp: listed)"
			}
		}
		switch rule {
		case agentpolicy.MCPNever:
			return nil, orStr(reason, orgName(org)+" keeps its data from every MCP server") + ", and this conversation holds its data — a new session that hasn't read it can use " + m.Name
		case agentpolicy.MCPOrg:
			if !m.Inside(org) {
				return nil, orStr(reason, orgName(org)+" keeps its data from MCP servers outside it") + ", and this conversation holds its data — " + m.Name + " isn't listed with " + orgName(org)
			}
		case agentpolicy.MCPListed:
		default:
			if !m.Inside(org) {
				each = append(each, org)
			}
		}
	}
	return each, ""
}

// eachWhy is why each call to m asks.
func eachWhy(m delegation.MCPServer, orgs []string) string {
	return "This conversation holds data of " + strings.Join(mapNames(orgs), ", ") + ": what is sent to " + m.Name + " (" + m.Host() +
		") leaves the workspace, so each call is one you approve"
}

// mcpExtJudged is an outside call as judged.
type mcpExtJudged struct {
	m      delegation.MCPServer
	t      mcpclient.Server
	sess   mcpclient.Session
	tool   mcpTool
	under  string   // "approval:<id>" | "preference" | "": a ticket
	each   []string // the orgs for which each call asks
	status int
	code   string
	msg    string
	said   string        // the server's own words, apart (mcpExtFail)
	wait   time.Duration // rate-limited: until the next one
}

func (j mcpExtJudged) fail() mcpExtFail {
	return mcpExtFail{status: j.status, code: j.code, msg: j.msg, said: j.said}
}

// mcpExtJudge is mcpJudge for an outside server: the reach, the server
// (visible to the person), the orgs' rules, the tool (listed by the
// server), and what covers the call without a ticket — never while it
// asks each call. Asking the server is counted against the MCP limit (a
// dry run's and a request's filing too); a dry run pins nothing.
func (s *Server) mcpExtJudge(ctx context.Context, p auth.Principal, h delegation.AgentSession, c mcpCallBody, dry bool) (j mcpExtJudged) {
	refuse := func(status int, code, msg string) mcpExtJudged {
		j.status, j.code, j.msg = status, code, msg
		return j
	}
	if reach := s.mcpReach(p, h); reach.State == reachOff {
		return refuse(http.StatusForbidden, reach.Code, reach.Why)
	}
	m, ok := s.mcpExtVisible(p.UserID, c.Server)
	if !ok {
		return refuse(http.StatusNotFound, "mcp-server", noExtServer(c.Server))
	}
	j.m = m
	each, why := s.mcpExtOrgRule(h, m)
	if why != "" {
		return refuse(http.StatusForbidden, "mcp-org", why)
	}
	j.each = each
	t, status, code, msg := s.mcpExtTarget(m)
	if msg != "" {
		return refuse(status, code, msg)
	}
	j.t = t
	// the limit before the server is asked — and before a ticket is spent
	if ok, wait, whose := s.delegateRT.allowNet("mcp", p.UserID, h.ID); !ok {
		j.wait = wait
		return refuse(http.StatusTooManyRequests, "rate-limited", netLimitWords("mcp", whose, wait))
	}
	sess, tools, err := s.mcpExtOpen(ctx, mcpExtSessionKey(p.UserID, m, h.ID), t)
	s.mcpExtHeard(p, h, err)
	if err != nil {
		f := mcpExtErr(err)
		j.said = f.said
		return refuse(f.status, f.code, f.msg)
	}
	j.sess = sess
	i := slices.IndexFunc(tools, func(tl mcpTool) bool { return tl.Name == c.Tool })
	if i < 0 {
		return refuse(http.StatusNotFound, "mcp-tool", m.Name+" has no tool "+clipText(c.Tool, 200)+" (mcp tools lists its tools)")
	}
	j.tool = tools[i]
	key, def := m.Key(), j.tool.pin()
	seen, had := s.Delegation.ToolPinOf(p.UserID, key, c.Tool)
	if !had && dry {
		seen = def // (a dry run judges as the first sight would, and keeps nothing)
	} else if !had {
		if err := s.Delegation.PinTool(p.UserID, key, c.Tool, def); err == nil {
			slog.Info("delegation: an outside tool's definition pinned at first sight", "user", p.UserID, "server", m.Server(), "tool", c.Tool, "pin", def.Hash)
		}
		seen = def
	}
	if a, ok := s.Delegation.Covers(p.UserID, key, delegation.MCPMethod, c.Tool); ok {
		if a.Pin != nil && a.Pin.Hash == def.Hash {
			if len(each) == 0 {
				j.under = "approval:" + a.ID
			}
			return j
		}
		j.tool.changed = changedSince(a.Pin, def)
	}
	if seen.Hash != def.Hash && j.tool.changed == nil {
		j.tool.changed = seen.Changed(def)
	}
	// an outside server's read-only mark counts only where its admin trusts it
	if len(each) == 0 && m.TrustReadOnly && j.tool.Annotations.ReadOnlyHint && seen.Hash == def.Hash && s.Delegation.State(p.UserID).MCPReadOnly {
		j.under = "preference"
	}
	return j
}

// mcpExtRow fills the audit row for an outside server: the server and the
// URL's scheme and host — never its path, which can hold a token.
func mcpExtRow(row *delegation.Call, m delegation.MCPServer, server, tool string) {
	if row == nil {
		return
	}
	row.Tile, row.Server, row.Tool = "", server, tool
	if u, err := url.Parse(m.URL); err == nil && m.ID != "" {
		row.Path = u.Scheme + "://" + u.Host
	} else {
		row.Path = server
	}
}

// apiDelegateMCPExtCall is POST /delegate/mcp/call for an outside server
// (apiDelegateMCPCall hands it over once it has read the body).
func (s *Server) apiDelegateMCPExtCall(w http.ResponseWriter, r *http.Request, p auth.Principal, h delegation.AgentSession, row *delegation.Call, c mcpCallBody, body []byte) {
	mcpExtRow(row, delegation.MCPServer{}, c.Server, c.Tool)
	ctx, cancel := context.WithTimeout(r.Context(), mcpCallTime+5*time.Second)
	defer cancel()
	j := s.mcpExtJudge(ctx, p, h, c, false)
	mcpExtRow(row, j.m, c.Server, c.Tool)
	if j.msg != "" {
		if j.wait > 0 { // the limit came before the ticket: a call over it spends nothing the person gave
			w.Header().Set("Retry-After", strconv.Itoa(int(j.wait/time.Second)+1))
		}
		mcpExtRefuse(w, row, j.fail())
		return
	}
	if j.under == "" {
		ticket := strings.TrimSpace(r.Header.Get(HeaderTicket))
		if ticket == "" {
			why := "calling " + c.Tool + " on " + j.m.Name + " (outside this workspace: " + j.m.Host() + ") needs " + p.UserID + "'s approval"
			switch {
			case len(j.each) > 0:
				why += " — each time: " + eachWhy(j.m, j.each)
			case len(j.tool.changed) > 0:
				why += " — the tool changed since " + p.UserID + " last allowed or first met it (" + changedWords(j.tool.changed) + "), so they are asked again"
			}
			mcpRefuse(w, row, http.StatusForbidden, "needs-approval", why+" — ask: POST /api/xbin/delegate/requests {method: \"POST\", path: \"/api/xbin/delegate/mcp/call\", body}, then send the same body with its id as X-XBin-Ticket")
			return
		}
		if asked, ok := s.Delegation.Request(p.UserID, ticket); ok {
			if pin := delegation.PinFromPreview(asked.Preview["pin"]); pin != nil && pin.Hash != j.tool.pin().Hash || asked.Tile != j.m.Key() {
				mcpRefuse(w, row, http.StatusForbidden, "ticket", c.Tool+" on "+j.m.Name+" changed since "+p.UserID+" allowed this call: ask again")
				return
			}
		}
		req, err := s.Delegation.Redeem(p.UserID, ticket, http.MethodPost, "/api/xbin"+requestTarget(r), j.m.Key(), contentTypeOf(r.Header.Get("Content-Type")), body)
		if err != nil {
			mcpRefuse(w, row, http.StatusForbidden, "ticket", err.Error())
			return
		}
		if row != nil {
			row.Ticket = req.ID
		}
		s.delegateEvent(p.UserID, "request", req.ID)
		if pin := delegation.PinFromPreview(req.Preview["pin"]); pin != nil && pin.Hash == j.tool.pin().Hash {
			_ = s.Delegation.PinTool(p.UserID, j.m.Key(), c.Tool, j.tool.pin())
		}
	} else if row != nil {
		row.Under = j.under
	}
	res, err := s.mcpExtClient().Call(ctx, j.t, j.sess, c.Tool, c.Arguments)
	if errors.Is(err, mcpclient.ErrSessionGone) { // the session went between the list and the call: once more, in a new one
		key := mcpExtSessionKey(p.UserID, j.m, h.ID)
		s.mcpExt.drop(key)
		if sess, _, oerr := s.mcpExtOpen(ctx, key, j.t); oerr == nil {
			res, err = s.mcpExtClient().Call(ctx, j.t, sess, c.Tool, c.Arguments)
		}
	}
	s.mcpExtHeard(p, h, err) // what an outside server says is the web's words in the conversation
	var re *mcpclient.Error
	switch {
	case errors.As(err, &re) && re.RPC:
		// the tool's own failure: its words are the result, framed by the agent as the server's
		WriteJSON(w, http.StatusOK, map[string]any{"server": c.Server, "name": j.m.Name, "host": j.m.Host(), "outside": true, "tool": c.Tool, "isError": true,
			"content": []map[string]any{{"type": "text", "text": re.Said}}})
		return
	case err != nil:
		mcpExtRefuse(w, row, mcpExtErr(err))
		return
	}
	if row != nil {
		row.Bytes = int64(len(res))
	}
	var out struct {
		Content    []map[string]any `json:"content"`
		IsError    bool             `json:"isError"`
		Structured json.RawMessage  `json:"structuredContent,omitempty"`
	}
	_ = json.Unmarshal(res, &out)
	ans := map[string]any{"server": c.Server, "name": j.m.Name, "host": j.m.Host(), "outside": true, "tool": c.Tool, "isError": out.IsError, "content": orContent(out.Content)}
	if len(out.Structured) > 0 && string(out.Structured) != "null" {
		ans["structured"] = out.Structured
	}
	WriteJSON(w, http.StatusOK, ans)
}

// mcpExtRequestPreview is mcpRequestPreview for an outside server: what
// the person is shown, the request's key (its tile), or why it can't be
// asked.
func (s *Server) mcpExtRequestPreview(ctx context.Context, p auth.Principal, h delegation.AgentSession, c mcpCallBody) (map[string]any, string, int, string) {
	ctx, cancel := context.WithTimeout(ctx, mcpCallTime)
	defer cancel()
	j := s.mcpExtJudge(ctx, p, h, c, false)
	switch {
	case j.msg != "":
		return nil, "", j.status, j.msg
	case j.under != "":
		return nil, "", http.StatusBadRequest, "this tool's call needs no approval (" + j.under + "): make it"
	}
	argsText, _ := json.MarshalIndent(orArgs(c.Arguments), "", "  ")
	out := map[string]any{"class": "mcp", "server": c.Server, "serverName": j.m.Name, "host": j.m.Host(), "outside": true, "tool": j.tool.Name,
		"title": orStr(j.tool.Title, j.tool.Annotations.Title), "description": clipText(j.tool.Description, 1000), "readOnly": j.tool.Annotations.ReadOnlyHint,
		"readOnlyTrusted": j.m.TrustReadOnly, "arguments": string(argsText), "reversible": false, "pin": j.tool.pin(), "changed": orNone(j.tool.changed)}
	if j.m.Note != "" {
		out["note"] = j.m.Note
	}
	if len(j.each) > 0 {
		// what leaves the workspace from such a conversation is approved call by call
		out["each"], out["orgs"], out["why"] = true, j.each, eachWhy(j.m, j.each)
	} else {
		out["standing"] = map[string]string{delegation.ScopeTool: j.tool.Name + " on " + j.m.Name + " (outside this workspace: " + j.m.Host() + ") — this tool again, as it is described now, with any arguments, without asking"}
	}
	return out, j.m.Key(), 0, ""
}

// ---- GET /delegate/mcp/servers ----

func (s *Server) apiDelegateMCPServers(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, false, false, true)
	if !ok {
		return
	}
	p := a.p
	h, ok := s.handleOf(p)
	if !ok {
		mcpRefuse(w, delegateRowOf(r), http.StatusForbidden, "no-session", "the servers are listed for an agent session (X-XBin-Agent-Session)")
		return
	}
	reach := s.mcpReach(p, h)
	list := []map[string]any{}
	if reach.State != reachOff {
		for _, m := range s.mcpVisible(p.UserID) {
			row := map[string]any{"server": m.Server(), "name": m.Name, "host": m.Host(), "trustReadOnly": m.TrustReadOnly, "state": reachOn}
			if m.Note != "" {
				row["note"] = m.Note
			}
			switch each, why := s.mcpExtOrgRule(h, m); {
			case why != "":
				row["state"], row["code"], row["why"] = reachOff, "mcp-org", why
			case len(each) > 0:
				row["state"], row["orgs"], row["why"] = reachAsks, each, eachWhy(m, each)
			}
			list = append(list, row)
		}
	}
	out := map[string]any{"servers": list}
	if reach.State == reachOff {
		out["code"], out["why"] = reach.Code, reach.Why
	}
	WriteJSON(w, http.StatusOK, out)
}

// ---- POST /delegate/mcp/tools {server} ----

// mcpToolView is a tool as the agent reads it.
func mcpToolView(t mcpTool) map[string]any {
	v := map[string]any{"name": t.Name, "readOnly": t.Annotations.ReadOnlyHint}
	if tt := orStr(t.Title, t.Annotations.Title); tt != "" {
		v["title"] = clipText(tt, 200)
	}
	if t.Description != "" {
		v["description"] = clipText(t.Description, 1000)
	}
	if len(t.InputSchema) > 0 {
		v["inputSchema"] = t.InputSchema
	}
	return v
}

func (s *Server) apiDelegateMCPTools(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, false, false, true)
	if !ok {
		return
	}
	p := a.p
	row := delegateRowOf(r)
	if row != nil {
		row.Class = "mcp-read"
	}
	h, ok := s.handleOf(p)
	if !ok {
		mcpRefuse(w, row, http.StatusForbidden, "no-session", "an outside server's tools are listed under an agent session (X-XBin-Agent-Session)")
		return
	}
	var in struct {
		Server string `json:"server"`
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil || json.Unmarshal(b, &in) != nil || !delegation.IsExt(in.Server) {
		mcpRefuse(w, row, http.StatusBadRequest, "mcp-call", `need {server: "ext:<id>"} — an outside server as GET /api/xbin/delegate/mcp/servers names it (a tile's tools are read on the tile's own endpoint)`)
		return
	}
	mcpExtRow(row, delegation.MCPServer{}, in.Server, "")
	if reach := s.mcpReach(p, h); reach.State == reachOff {
		mcpRefuse(w, row, http.StatusForbidden, reach.Code, reach.Why)
		return
	}
	m, ok := s.mcpExtVisible(p.UserID, in.Server)
	if !ok {
		mcpRefuse(w, row, http.StatusNotFound, "mcp-server", noExtServer(in.Server))
		return
	}
	mcpExtRow(row, m, in.Server, "")
	if _, why := s.mcpExtOrgRule(h, m); why != "" {
		mcpRefuse(w, row, http.StatusForbidden, "mcp-org", why)
		return
	}
	if ok, wait, whose := s.delegateRT.allowNet("mcp", p.UserID, h.ID); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
		mcpRefuse(w, row, http.StatusTooManyRequests, "rate-limited", netLimitWords("mcp", whose, wait))
		return
	}
	t, status, code, msg := s.mcpExtTarget(m)
	if msg != "" {
		mcpRefuse(w, row, status, code, msg)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*mcpclient.ListTimeout)
	defer cancel()
	_, tools, err := s.mcpExtOpen(ctx, mcpExtSessionKey(p.UserID, m, h.ID), t)
	s.mcpExtHeard(p, h, err) // a server's words — its tools', or its error's — are an outsider's
	if err != nil {
		mcpExtRefuse(w, row, mcpExtErr(err))
		return
	}
	views := make([]map[string]any, 0, len(tools))
	for _, tl := range tools {
		views = append(views, mcpToolView(tl))
	}
	WriteJSON(w, http.StatusOK, map[string]any{"server": in.Server, "name": m.Name, "host": m.Host(), "outside": true, "tools": views})
}

// dryMCPTools: whether a listing of an outside server's tools would run —
// the reach, the server visible to the person, the orgs' rules — without
// asking the server anything.
func dryMCPTools(s *Server, p auth.Principal, _ *http.Request, body []byte, _ *delegation.Call, _ map[string]any) (string, string) {
	h, ok := s.handleOf(p)
	if !ok {
		return "", ""
	}
	var in struct {
		Server string `json:"server"`
	}
	if json.Unmarshal(body, &in) != nil || !delegation.IsExt(in.Server) {
		return "mcp", `need {server: "ext:<id>"}`
	}
	if reach := s.mcpReach(p, h); reach.State == reachOff {
		return "mcp", reach.Why
	}
	m, ok := s.mcpExtVisible(p.UserID, in.Server)
	if !ok {
		return "mcp", noExtServer(in.Server)
	}
	if _, why := s.mcpExtOrgRule(h, m); why != "" {
		return "mcp", why
	}
	return "", ""
}

// ---- POST /delegate/mcp/test {id} ----

// apiDelegateMCPTest — an admin tries a listed server: initialize and
// tools/list, no session kept. Always 200: {ok, …} or {ok: false,
// refusal, error} — the card shows the tools or the exact error.
func (s *Server) apiDelegateMCPTest(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if !s.settingsAdmin(r) || p.Agent != "" {
		WriteError(w, http.StatusForbidden, "admin only", delegationDocs)
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := DecodeJSON(r, &in); err != nil || in.ID == "" {
		WriteError(w, http.StatusBadRequest, "need {id}: a server of the list, saved", delegationDocs)
		return
	}
	m, ok := s.Delegation.MCPServerNamed(delegation.ExtPrefix + in.ID)
	if !ok {
		WriteError(w, http.StatusNotFound, "no outside server "+in.ID+" in the list in force (save it first)", delegationDocs)
		return
	}
	out := map[string]any{"server": map[string]any{"id": m.ID, "name": m.Name, "host": m.Host()}}
	fail := func(code, msg string) {
		out["ok"], out["refusal"], out["error"] = false, code, msg
		slog.Info("delegation: an admin tested an outside MCP server", "by", p.From(), "server", m.Server(), "ok", false, "refusal", code)
		WriteJSON(w, http.StatusOK, out)
	}
	t, _, code, msg := s.mcpExtTarget(m)
	if msg != "" {
		fail(code, msg)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*mcpclient.ListTimeout)
	defer cancel()
	c := s.mcpExtClient()
	sess, err := c.Open(ctx, t)
	var tools []map[string]any
	if err == nil {
		tools, err = c.Tools(ctx, t, sess)
	}
	if err != nil {
		f := mcpExtErr(err)
		if f.said != "" {
			out["said"] = f.said // the server's words, apart from xbind's
		}
		if f.page != "" {
			out["page"] = f.page // its error page: the admin's to read, scrubbed
		}
		fail(f.code, f.msg)
		return
	}
	views := make([]map[string]any, 0, len(tools))
	for _, def := range tools {
		raw, _ := json.Marshal(def)
		var tl mcpTool
		_ = json.Unmarshal(raw, &tl)
		views = append(views, mcpToolView(tl))
	}
	out["ok"], out["protocolVersion"], out["tools"] = true, sess.Version, views
	if sess.ServerName != "" {
		out["serverInfo"] = map[string]any{"name": sess.ServerName, "version": sess.ServerVersion}
	}
	slog.Info("delegation: an admin tested an outside MCP server", "by", p.From(), "server", m.Server(), "ok", true, "tools", len(views))
	WriteJSON(w, http.StatusOK, out)
}

// mcpExtCount is how many outside servers the conversation may use now
// (for the reach's "listed").
func (s *Server) mcpExtCount(p auth.Principal, h delegation.AgentSession) int {
	n := 0
	for _, m := range s.mcpVisible(p.UserID) {
		if _, why := s.mcpExtOrgRule(h, m); why == "" {
			n++
		}
	}
	return n
}
