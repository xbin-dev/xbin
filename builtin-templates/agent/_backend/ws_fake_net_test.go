package main

import (
	"context"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// fakeNet is xbind's web for the workspace agent, from memory: the
// workspace policy, the person's setting, the orgs' web rules, and the
// pages there are.
type fakeNet struct {
	webOff  string            // the policy's or the person's refusal: web-off | web-person ("": on)
	orgWeb  map[string]string // org → ask | allow | never (unset: ask)
	pages   map[string]wsWebPage
	fetched []string // URLs read, in order
	mcpOff  string   // the policy's or the person's refusal of MCP: mcp-off | mcp-person ("": on)
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
		r.MCP = wsReachOne{State: "asks", Why: "Each tool asks you the first time"}
	}
	return r
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
