package server

// delegatemcp.go — a person's workspace agent with MCP servers (D239;
// docs/protocol/delegation.md §Web and MCP). An MCP tile is one whose
// manifest provides an http interface of service "mcp"; its endpoint is
// /api/<tile>/mcp.
//
// Two ways in, each judged by xbind:
//
//   - THE READS, on the tile's own endpoint (mcpGate, from the delegate
//     gate): a POST whose body is exactly one JSON-RPC request of
//     initialize, ping or tools/list is a read — the agent checks a
//     hook-up itself, nobody asked — under the tile's own rules (the
//     person's level, its "agent" level, the org rules) and the session's
//     MCP reach. Any other POST to that endpoint is refused there: a
//     tool's call has a route of its own, so a generic tile-call ticket or
//     standing approval of "POST …/mcp" can never become every tool of the
//     server, and the MCP policy can't be stepped around by tile_call.
//   - A TOOL'S CALL, POST /delegate/mcp/call {server, tool, arguments}:
//     xbind is the MCP client. It lists the server's tools as the person
//     (the tool must be there; its readOnlyHint is the server's word), and
//     calls it under the person's approval — a ticket bound to the body,
//     a standing approval of that tool ("always for this tool"), or, for a
//     tool the server marks read-only, the person's own "read-only tools
//     don't ask". A tile at "agent": "read" takes read-only tools only.
//     What the tool answers taints the conversation with the tile's orgs.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/agentpolicy"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/registry"
)

// mcpService is the interface service an MCP server provides.
const mcpService = "mcp"

const mcpCallPattern = "POST /delegate/mcp/call"

// The read methods: what the agent may ask an MCP server without anyone's
// approval.
var mcpReadMethods = map[string]bool{"initialize": true, "ping": true, "tools/list": true}

// Bounds.
const (
	mcpBodyMax   = 64 << 10 // a JSON-RPC request the gate reads
	mcpAnswerMax = 1 << 20  // what xbind reads of a server's answer
	mcpCallTime  = 60 * time.Second
)

// mcpForwards are the request headers of the streamable-HTTP transport a
// read on an MCP endpoint carries to the tile, beside delegateForwards'.
var mcpForwards = map[string]bool{"Mcp-Session-Id": true, "Mcp-Protocol-Version": true}

func (s *Server) registerDelegateMCPAPI() {
	s.RegisterAPI(mcpCallPattern, s.apiDelegateMCPCall)
}

// mcpTiles lists the tiles of this workspace that provide an MCP server,
// by path.
func (s *Server) mcpTiles() []*registry.Component {
	if s.Reg == nil {
		return nil
	}
	var out []*registry.Component
	for _, c := range s.Reg.Components() {
		if isMCPTile(c) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func isMCPTile(c *registry.Component) bool {
	if c == nil || c.IsTemplate() || c.Sealed || isChrome(c.Path) {
		return false
	}
	for _, pr := range c.Manifest.Provides {
		if pr.Kind == "http" && pr.Service == mcpService && !pr.Instances {
			return true
		}
	}
	return false
}

// mcpTilesNone reports a workspace with no MCP tile at all.
func (s *Server) mcpTilesNone() bool { return len(s.mcpTiles()) == 0 }

// mcpEndpoint reports a request path that is tile's declared MCP endpoint.
func (s *Server) mcpEndpoint(tile, path string) bool {
	if tile == "" || s.Reg == nil || path != "/api/"+tile+"/mcp" {
		return false
	}
	c, ok := s.Reg.Component(tile)
	return ok && isMCPTile(c)
}

// mcpOrgRefusal is why the conversation under h may not use tile's MCP
// server (""; it may): an org whose data it holds keeps its data from MCP
// servers outside it, and tile isn't under that org.
func (s *Server) mcpOrgRefusal(h delegation.AgentSession, tile string) string {
	nr, ok := s.AgentWorld.(agentNetRules)
	if !ok {
		return ""
	}
	under := s.orgsOf(tile)
	for _, org := range h.Tainted {
		if rule, reason := nr.AgentMCPIn(org); rule == agentpolicy.MCPOrg && !slices.Contains(under, org) {
			return orStr(reason, orgName(org)+" keeps its data from MCP servers outside it") + ", and this conversation holds its data — this server isn't " + orgName(org) + "'s"
		}
	}
	return ""
}

// rpcRequest is one JSON-RPC request as the gate reads it.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// mcpGate judges a delegate's POST to a declared MCP endpoint (the
// delegate gate, before the tile's rules): read says it is one of the read
// methods; otherwise code and msg are the refusal. r's body is read and
// put back.
func (s *Server) mcpGate(w http.ResponseWriter, r *http.Request, p auth.Principal, h delegation.AgentSession, tile string) (read bool, code, msg string) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mcpBodyMax))
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	if err != nil {
		return false, "mcp-call", fmt.Sprintf("a request to an MCP server is at most %d KiB", mcpBodyMax>>10)
	}
	if reach := s.mcpReach(p, h); reach.State == reachOff {
		return false, reach.Code, reach.Why
	}
	if why := s.mcpOrgRefusal(h, tile); why != "" {
		return false, "mcp-org", why
	}
	var rq rpcRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields() // nothing beside the four members a server might act on
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || dec.Decode(&rq) != nil || dec.More() || rq.JSONRPC != "2.0" {
		return false, "mcp-call", "an agent sends an MCP server one JSON-RPC request at a time (no batch)"
	}
	if mcpReadMethods[rq.Method] && len(rq.ID) > 0 {
		return true, "", ""
	}
	return false, "mcp-call", "only initialize, ping and tools/list are read on an MCP server; a tool is called with POST /api/xbin/delegate/mcp/call {server, tool, arguments}, which xbin shows your person"
}

// ---- xbind's own MCP client: the person's agent calling the tile, through the component proxy ----

// mcpWriter is a bounded ResponseWriter: what the tile answers, up to
// mcpAnswerMax.
type mcpWriter struct {
	hdr    http.Header
	status int
	buf    bytes.Buffer
	over   bool
}

func (m *mcpWriter) Header() http.Header { return m.hdr }
func (m *mcpWriter) WriteHeader(c int) {
	if m.status == 0 {
		m.status = c
	}
}
func (m *mcpWriter) Write(b []byte) (int, error) {
	if m.status == 0 {
		m.status = http.StatusOK
	}
	if m.buf.Len()+len(b) > mcpAnswerMax {
		m.over = true
		return 0, errors.New("the MCP server's answer is too long")
	}
	return m.buf.Write(b)
}
func (m *mcpWriter) Flush() {}

// mcpError is a JSON-RPC error a server answered, or why its answer
// couldn't be read.
type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *mcpError) Error() string { return e.Message }

// mcpRPC makes one JSON-RPC call to tile's MCP endpoint as p — the person's
// agent — through the component proxy, where the broker's Route judges it
// as any call of the agent's to the tile. It answers the result and the
// session id the server gave.
func (s *Server) mcpRPC(ctx context.Context, p auth.Principal, tile, session string, id int, method string, params any) (json.RawMessage, string, error) {
	if s.ComponentAPI == nil {
		return nil, session, errors.New("component backends are not enabled on this xbind")
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	req, err := http.NewRequestWithContext(auth.WithPrincipal(ctx, p), http.MethodPost, "/api/"+tile+"/mcp", bytes.NewReader(body))
	if err != nil {
		return nil, session, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	mw := &mcpWriter{hdr: http.Header{}}
	s.ComponentAPI.ServeHTTP(mw, req)
	if sid := mw.hdr.Get("Mcp-Session-Id"); sid != "" {
		session = sid
	}
	if mw.over {
		return nil, session, &mcpError{Message: fmt.Sprintf("%s answered more than %d KiB", tile, mcpAnswerMax>>10)}
	}
	raw := mw.buf.Bytes()
	if mw.status != http.StatusOK {
		var e struct{ Error string }
		_ = json.Unmarshal(raw, &e)
		return nil, session, &mcpError{Code: mw.status, Message: fmt.Sprintf("%s answered %d: %s", tile, mw.status, clipText(orStr(e.Error, strings.TrimSpace(string(raw))), 300))}
	}
	if strings.Contains(mw.hdr.Get("Content-Type"), "text/event-stream") {
		raw = sseData(raw)
	}
	var rr struct {
		Result json.RawMessage `json:"result"`
		Error  *mcpError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, session, &mcpError{Message: tile + "'s MCP answer can't be read"}
	}
	if rr.Error != nil {
		return nil, session, rr.Error
	}
	return rr.Result, session, nil
}

// sseData is the first data: frame of an event stream that holds a JSON
// object.
func sseData(raw []byte) []byte {
	for _, line := range strings.Split(string(raw), "\n") {
		if d, ok := strings.CutPrefix(strings.TrimSpace(line), "data:"); ok && strings.HasPrefix(strings.TrimSpace(d), "{") {
			return []byte(strings.TrimSpace(d))
		}
	}
	return raw
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// mcpTool is one tool of a server as xbind reads its tools/list.
type mcpTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
	Annotations struct {
		Title        string `json:"title,omitempty"`
		ReadOnlyHint bool   `json:"readOnlyHint,omitempty"`
	} `json:"annotations"`
}

// mcpOpen initializes a session with tile's server as p and lists its
// tools.
func (s *Server) mcpOpen(ctx context.Context, p auth.Principal, tile string) (session string, tools []mcpTool, err error) {
	_, session, err = s.mcpRPC(ctx, p, tile, "", 1, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "xbin-workspace-agent", "version": "1"}})
	if err != nil {
		return "", nil, err
	}
	res, session, err := s.mcpRPC(ctx, p, tile, session, 2, "tools/list", map[string]any{})
	if err != nil {
		return session, nil, err
	}
	var list struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := json.Unmarshal(res, &list); err != nil {
		return session, nil, &mcpError{Message: tile + "'s tools/list can't be read"}
	}
	return session, list.Tools, nil
}

// ---- POST /delegate/mcp/call ----

// mcpCallBody is a tool's call as the agent asks for it: the exact bytes a
// ticket binds.
type mcpCallBody struct {
	Server    string         `json:"server"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

func readMCPCall(body []byte) (mcpCallBody, error) {
	var c mcpCallBody
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil || strings.TrimSpace(c.Server) == "" || strings.TrimSpace(c.Tool) == "" || len(c.Tool) > 200 {
		return c, errors.New(`need {server: "<tile>", tool: "<name>", arguments: {…}}`)
	}
	c.Server = strings.Trim(c.Server, "/")
	return c, nil
}

// mcpJudge is what a tool's call by p's conversation under h needs, or why
// it is refused: the server (a tile p can read, in the session's scope, an
// MCP tile), the session's reach and the org rules, the tool (listed by
// the server, as p), the tile's agent level for a tool that isn't
// read-only. It answers the tool and how it may run without a ticket
// ("approval:<id>" or "preference"; "" it needs one).
func (s *Server) mcpJudge(ctx context.Context, p auth.Principal, h delegation.AgentSession, c mcpCallBody) (tool mcpTool, under, session string, status int, code, msg string) {
	if reach := s.mcpReach(p, h); reach.State == reachOff {
		return tool, "", "", http.StatusForbidden, reach.Code, reach.Why
	}
	if s.tileHidden(p, c.Server) {
		return tool, "", "", http.StatusForbidden, "tile", noTileFor(p.UserID)
	}
	comp, _ := s.Reg.Component(c.Server)
	switch {
	case !s.inSessionScope(h, c.Server):
		return tool, "", "", http.StatusForbidden, "hidden", hiddenByScope(p.UserID)
	case !isMCPTile(comp):
		return tool, "", "", http.StatusBadRequest, "mcp-server", c.Server + " isn't an MCP server: its manifest provides no mcp interface"
	}
	if why := s.mcpOrgRefusal(h, c.Server); why != "" {
		return tool, "", "", http.StatusForbidden, "mcp-org", why
	}
	dp, ok := s.policy().(DelegatePolicy)
	if !ok {
		return tool, "", "", http.StatusForbidden, "not-delegated", "this xbind has no tile routing for a person's agent"
	}
	if _, _, _, err := dp.DelegateTile(p, c.Server, false); err != nil {
		return tool, "", "", http.StatusForbidden, "tile", err.Error()
	}
	session, tools, err := s.mcpOpen(ctx, p, c.Server)
	if err != nil {
		return tool, "", "", http.StatusBadGateway, "mcp-unreachable", "the MCP server didn't answer: " + err.Error()
	}
	i := slices.IndexFunc(tools, func(t mcpTool) bool { return t.Name == c.Tool })
	if i < 0 {
		return tool, "", session, http.StatusNotFound, "mcp-tool", c.Server + " has no tool " + c.Tool + " (tools/list names its tools)"
	}
	tool = tools[i]
	readOnly := tool.Annotations.ReadOnlyHint
	if !readOnly {
		if _, _, _, err := dp.DelegateTile(p, c.Server, true); err != nil {
			return tool, "", session, http.StatusForbidden, "tile", err.Error() + ": only its tools marked read-only"
		}
	}
	if a, ok := s.Delegation.Covers(p.UserID, c.Server, delegation.MCPMethod, c.Tool); ok {
		return tool, "approval:" + a.ID, session, 0, "", ""
	}
	if readOnly && s.Delegation.State(p.UserID).MCPReadOnly {
		return tool, "preference", session, 0, "", ""
	}
	return tool, "", session, 0, "", ""
}

func mcpRefuse(w http.ResponseWriter, row *delegation.Call, status int, refusal, msg string) {
	if row != nil {
		row.Refused = msg
	}
	WriteJSON(w, status, map[string]any{"error": msg, "refusal": refusal, "class": "mcp", "docs": delegationDocs})
}

// apiDelegateMCPCall — POST /delegate/mcp/call {server, tool, arguments}:
// the person's agent calls one tool of an MCP tile, as the person. →
// {server, tool, isError, content, structured?}.
func (s *Server) apiDelegateMCPCall(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, false, false, true)
	if !ok {
		return
	}
	p := a.p
	row := delegateRowOf(r)
	if row != nil {
		row.Class = "mcp"
	}
	h, ok := s.handleOf(p)
	if !ok {
		mcpRefuse(w, row, http.StatusForbidden, "no-session", "a tool's call runs under an agent session (X-XBin-Agent-Session)")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, delegation.MaxBody))
	if err != nil {
		mcpRefuse(w, row, http.StatusRequestEntityTooLarge, "too-large", fmt.Sprintf("a tool's call carries at most %d KiB", delegation.MaxBody>>10))
		return
	}
	c, err := readMCPCall(body)
	if err != nil {
		mcpRefuse(w, row, http.StatusBadRequest, "mcp-call", err.Error())
		return
	}
	if row != nil {
		row.Tile, row.Path = c.Server, "/api/"+c.Server+"/mcp"
	}
	ctx, cancel := context.WithTimeout(r.Context(), mcpCallTime)
	defer cancel()
	tool, under, session, status, code, msg := s.mcpJudge(ctx, p, h, c)
	if msg != "" {
		mcpRefuse(w, row, status, code, msg)
		return
	}
	if under == "" {
		ticket := strings.TrimSpace(r.Header.Get(HeaderTicket))
		if ticket == "" {
			mcpRefuse(w, row, http.StatusForbidden, "needs-approval", "calling "+c.Tool+" on "+c.Server+" needs "+p.UserID+"'s approval — ask: POST /api/xbin/delegate/requests {method: \"POST\", path: \"/api/xbin/delegate/mcp/call\", body}, then send the same body with its id as X-XBin-Ticket")
			return
		}
		req, err := s.Delegation.Redeem(p.UserID, ticket, http.MethodPost, "/api/xbin"+requestTarget(r), c.Server, contentTypeOf(r.Header.Get("Content-Type")), body)
		if err != nil {
			mcpRefuse(w, row, http.StatusForbidden, "ticket", err.Error())
			return
		}
		if row != nil {
			row.Ticket = req.ID
		}
		s.delegateEvent(p.UserID, "request", req.ID)
	} else if row != nil {
		row.Under = under
	}
	args := c.Arguments
	if args == nil {
		args = map[string]any{}
	}
	res, _, err := s.mcpRPC(ctx, p, c.Server, session, 3, "tools/call", map[string]any{"name": c.Tool, "arguments": args})
	s.taint(p, c.Server) // what the tool answered is the tile's data in the conversation
	if err != nil {
		var me *mcpError
		if errors.As(err, &me) {
			WriteJSON(w, http.StatusOK, map[string]any{"server": c.Server, "tool": tool.Name, "isError": true, "content": []map[string]any{{"type": "text", "text": me.Message}}})
			return
		}
		mcpRefuse(w, row, http.StatusBadGateway, "mcp-unreachable", "the MCP server didn't answer: "+err.Error())
		return
	}
	var out struct {
		Content    []map[string]any `json:"content"`
		IsError    bool             `json:"isError"`
		Structured json.RawMessage  `json:"structuredContent,omitempty"`
	}
	_ = json.Unmarshal(res, &out)
	ans := map[string]any{"server": c.Server, "tool": tool.Name, "isError": out.IsError, "content": orContent(out.Content)}
	if len(out.Structured) > 0 && string(out.Structured) != "null" {
		ans["structured"] = out.Structured
	}
	WriteJSON(w, http.StatusOK, ans)
}

func orContent(c []map[string]any) []map[string]any {
	if c == nil {
		return []map[string]any{}
	}
	return c
}

// mcpRequestPreview is what a person is shown of a tool's call their agent
// asks to make — or why it can't be asked: one that needs no approval
// (400), one refused whatever approves it (403).
func (s *Server) mcpRequestPreview(ctx context.Context, p auth.Principal, body []byte) (map[string]any, string, int, string) {
	h, ok := s.handleOf(p)
	if !ok {
		return nil, "", http.StatusForbidden, "a tool's call is asked for under an agent session (X-XBin-Agent-Session)"
	}
	c, err := readMCPCall(body)
	if err != nil {
		return nil, "", http.StatusBadRequest, "a tool's call's body is " + err.Error()[len("need "):] + ", exactly as it will be sent"
	}
	ctx, cancel := context.WithTimeout(ctx, mcpCallTime)
	defer cancel()
	tool, under, _, status, _, msg := s.mcpJudge(ctx, p, h, c)
	switch {
	case msg != "":
		return nil, "", status, msg
	case under != "":
		return nil, "", http.StatusBadRequest, "this tool's call needs no approval (" + under + "): make it"
	}
	argsText, _ := json.MarshalIndent(orArgs(c.Arguments), "", "  ")
	name := c.Server
	if comp, ok := s.Reg.Component(c.Server); ok {
		name = orStr(comp.Manifest.DisplayTitle(), c.Server)
	}
	return map[string]any{"class": "mcp", "tile": c.Server, "server": c.Server, "serverName": name, "tool": tool.Name,
		"title": orStr(tool.Title, tool.Annotations.Title), "description": clipText(tool.Description, 1000), "readOnly": tool.Annotations.ReadOnlyHint,
		"arguments": string(argsText), "reversible": false,
		"standing": map[string]string{delegation.ScopeTool: tool.Name + " on " + name + " — this tool again, with any arguments, without asking"}}, c.Server, 0, ""
}

func orArgs(a map[string]any) map[string]any {
	if a == nil {
		return map[string]any{}
	}
	return a
}
