package main

import (
	"context"
	"encoding/json"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// fakeNet is xbind's web for the workspace agent, from memory: the
// workspace policy, the person's setting, the orgs' web rules, and the
// pages there are.
type fakeNet struct {
	webOff      string            // the policy's or the person's refusal: web-off | web-person ("": on)
	orgWeb      map[string]string // org → ask | allow | never (unset: ask)
	pages       map[string]wsWebPage
	fetched     []string               // URLs read, in order
	mcpOff      string                 // the policy's or the person's refusal of MCP: mcp-off | mcp-person ("": on)
	mcpTools    map[string][]wsMCPTool // tile → what its server lists
	mcpReadOnly bool                   // the person lets read-only tools through
	mcpStanding map[string]bool        // "tile tool" the person allowed for good
	mcpCalled   []string               // "tile tool" that ran, in order
	// outside servers (D240): what GET /delegate/mcp/servers answers, their
	// tools, and a refusal a server's call meets (server → refusal code)
	ext       []wsMCPServer
	extTools  map[string][]wsMCPTool
	extRefuse map[string]string
}

// reach is the web for handle h, as xbind's webReach says it.
func (f *fakeXbin) reach(h *wsHandle) wsReach {
	var r wsReach
	switch f.net.webOff {
	case "web-off":
		r.Web = wsReachOne{State: "off", Code: "web-off", Why: "Reading the web is off for this workspace — an admin can change it (the workspace agent's settings)"}
	case "web-person":
		r.Web = wsReachOne{State: "off", Code: "web-person", Why: "Reading the web is off in your agent's settings — you can turn it on in Preferences"}
	}
	if r.Web.State == "" {
		var asks []string
		for _, o := range h.Tainted {
			switch f.net.orgWeb[o] {
			case "never":
				if r.Web.State == "" {
					r.Web = wsReachOne{State: "off", Code: "web-org", Orgs: []string{o}, Why: f.orgs[o].Name + " keeps its data off the web, and this conversation holds it — a new session that hasn't read it can read the web"}
				}
			case "allow":
			default:
				asks = append(asks, o)
			}
		}
		switch {
		case r.Web.State != "":
		case len(asks) > 0:
			r.Web = wsReachOne{State: "asks", Orgs: asks, Why: "This conversation holds data of " + strings.Join(asks, ", ")}
		default:
			r.Web = wsReachOne{State: "on"}
		}
	}
	r.Search = wsReachOne{State: "off", Code: "search-off"}
	if f.net.mcpOff != "" {
		r.MCP = wsReachOne{State: "off", Code: f.net.mcpOff, Why: "Using MCP tools is off for this workspace — an admin can change it (the workspace agent's settings)"}
	} else {
		r.MCP = wsReachOne{State: "asks", Why: "Each tool asks you the first time", Listed: len(f.net.ext)}
	}
	return r
}

func (f *fakeXbin) MCPServers(_ context.Context, sid string) ([]wsMCPServer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, err := f.handle(sid)
	if err != nil {
		return nil, err
	}
	if err := f.mcpRefusal(h); err != nil {
		return nil, err
	}
	f.note("MCPServers")
	out := append([]wsMCPServer(nil), f.net.ext...)
	for i := range out {
		if len(h.Tainted) > 0 && out[i].State == "on" { // xbind's: a conversation holding an org's data asks each call
			out[i].State, out[i].Orgs, out[i].Why = "asks", h.Tainted, "This conversation holds data of "+strings.Join(h.Tainted, ", ")+": what is sent leaves the workspace"
		}
	}
	return out, nil
}

func (f *fakeXbin) MCPServerTools(_ context.Context, sid, server string) ([]wsMCPTool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, err := f.handle(sid)
	if err != nil {
		return nil, err
	}
	if err := f.mcpRefusal(h); err != nil {
		return nil, err
	}
	f.note("MCPServerTools %s", server)
	h.Web = true // an outsider's words
	return append([]wsMCPTool(nil), f.net.extTools[server]...), nil
}

func (f *fakeXbin) WebFetch(_ context.Context, sid, rawURL, ticket string) (wsWebPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, err := f.handle(sid)
	if err != nil {
		return wsWebPage{}, err
	}
	switch r := f.reach(h).Web; {
	case r.State == "off":
		f.note("WebFetch refused %s %s", r.Code, rawURL)
		return wsWebPage{}, &xbin.DelegateError{Status: 403, Refusal: r.Code, Message: r.Why}
	case r.State == "asks" && ticket == "":
		f.note("WebFetch needs-approval %s", rawURL)
		return wsWebPage{}, &xbin.DelegateError{Status: 403, Refusal: "needs-approval", Message: r.Why + ": ask for this one"}
	case r.State == "asks":
		req := f.requests[ticket]
		if req == nil || req.State != "approved" || req.used || string(req.call.Body) != string(wsWebBody(rawURL)) {
			return wsWebPage{}, &xbin.DelegateError{Status: 403, Refusal: "ticket", Message: "the ticket doesn't cover this fetch"}
		}
		req.used, req.State = true, "used"
	}
	p, ok := f.net.pages[rawURL]
	if !ok {
		f.note("WebFetch unreachable %s", rawURL)
		return wsWebPage{}, &xbin.DelegateError{Status: 502, Refusal: "web-unreachable", Message: wsWebHost(rawURL) + ": no such host"}
	}
	f.note("WebFetch %s ticket=%s", rawURL, ticket)
	f.net.fetched = append(f.net.fetched, rawURL)
	h.Web = true
	if p.URL == "" {
		p.URL = rawURL
	}
	if p.FinalURL == "" {
		p.FinalURL = rawURL
	}
	if p.Status == 0 {
		p.Status = 200
	}
	return p, nil
}

func (f *fakeXbin) AskWeb(_ context.Context, sid, rawURL, summary string) (wsRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ask(sid, wsCall{Method: "POST", Path: wsWebFetchPath, Body: wsWebBody(rawURL), ContentType: "application/json"}, "web", summary)
}

func (f *fakeXbin) MarkWeb(_ context.Context, sid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, err := f.handle(sid)
	if err != nil {
		return err
	}
	f.note("MarkWeb %s", sid)
	h.Web = true
	return nil
}

// webDry is xbind's dry run of a fetch: the reach, not the page.
func (f *fakeXbin) webDry(sid string, c wsCall) (wsAnswer, error) {
	h, err := f.handle(sid)
	if err != nil {
		return wsAnswer{}, err
	}
	d := &wsDry{Allowed: true, Class: "act", SideEffect: true}
	switch r := f.reach(h).Web; r.State {
	case "off":
		d.Allowed, d.Refusal, d.Error = false, "tile", r.Why
	case "asks":
		d.NeedsApproval = true
	}
	return wsAnswer{Status: 200, Dry: d}, nil
}

func (f *fakeXbin) mcpRefusal(h *wsHandle) error {
	if r := f.reach(h).MCP; r.State == "off" {
		return &xbin.DelegateError{Status: 403, Refusal: r.Code, Message: r.Why}
	}
	return nil
}

func (f *fakeXbin) MCPTools(_ context.Context, sid, tile string) ([]wsMCPTool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, err := f.handle(sid)
	if err != nil {
		return nil, err
	}
	if err := f.mcpRefusal(h); err != nil {
		f.note("MCPTools refused %s", tile)
		return nil, err
	}
	if _, err := f.tile(sid, tile, true); err != nil {
		return nil, err
	}
	f.note("MCPTools %s", tile)
	return append([]wsMCPTool(nil), f.net.mcpTools[tile]...), nil
}

func (f *fakeXbin) MCPCall(_ context.Context, sid string, c wsMCPCall, ticket string, dry bool) (wsMCPResult, *wsDry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, err := f.handle(sid)
	if err != nil {
		return wsMCPResult{}, nil, err
	}
	if err := f.mcpRefusal(h); err != nil {
		f.note("MCPCall refused %s %s", c.Server, c.Tool)
		return wsMCPResult{}, nil, err
	}
	tools := f.net.mcpTools
	if wsIsExt(c.Server) { // an outside server: no tile, its own refusals; each call asks while the conversation holds data
		tools = f.net.extTools
		if code := f.net.extRefuse[c.Server]; code != "" {
			f.note("MCPCall refused %s %s %s", code, c.Server, c.Tool)
			return wsMCPResult{}, nil, &xbin.DelegateError{Status: 502, Refusal: code, Message: "the server refused xbin's credentials (401): an admin checks this server's secret on the workspace agent's card"}
		}
	} else if _, err := f.tile(sid, c.Server, !dry); err != nil {
		return wsMCPResult{}, nil, err
	}
	var tool *wsMCPTool
	for i, t := range tools[c.Server] {
		if t.Name == c.Tool {
			tool = &tools[c.Server][i]
		}
	}
	if tool == nil {
		return wsMCPResult{}, nil, &xbin.DelegateError{Status: 404, Refusal: "mcp-tool", Message: c.Server + " has no tool " + c.Tool}
	}
	key := c.Server + " " + c.Tool
	free := f.net.mcpStanding[key] || tool.ReadOnly && f.net.mcpReadOnly
	if wsIsExt(c.Server) {
		free = f.net.mcpStanding[key] && len(h.Tainted) == 0 // an outside server's read-only mark isn't trusted here
		h.Web = true
	}
	if dry {
		return wsMCPResult{}, &wsDry{Allowed: true, Class: "act", SideEffect: true, NeedsApproval: !free}, nil
	}
	if !free {
		if ticket == "" {
			f.note("MCPCall needs-approval %s", key)
			return wsMCPResult{}, nil, &xbin.DelegateError{Status: 403, Refusal: "needs-approval", Message: "calling " + c.Tool + " needs the person's approval"}
		}
		r := f.requests[ticket]
		if r == nil || r.State != "approved" || r.used || string(r.call.Body) != string(wsMCPBody(c)) {
			return wsMCPResult{}, nil, &xbin.DelegateError{Status: 403, Refusal: "ticket", Message: "the ticket doesn't cover this call"}
		}
		r.used, r.State = true, "used"
		if r.Grant == "always" || r.Grant == "hour" {
			if f.net.mcpStanding == nil {
				f.net.mcpStanding = map[string]bool{}
			}
			f.net.mcpStanding[key] = true
		}
	}
	f.note("MCPCall %s ticket=%s", key, ticket)
	f.net.mcpCalled = append(f.net.mcpCalled, key)
	var out wsMCPResult
	_ = json.Unmarshal([]byte(`{"content":[{"type":"text","text":"rolled a 4"}]}`), &out)
	return out, nil, nil
}

func (f *fakeXbin) AskMCP(_ context.Context, sid string, c wsMCPCall, summary string) (wsRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ask(sid, wsCall{Method: "POST", Path: wsMCPCallPath, Body: wsMCPBody(c), ContentType: "application/json"}, "mcp", summary)
}
