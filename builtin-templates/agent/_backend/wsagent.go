// wsagent.go — what a person's WORKSPACE agent may do through this tile's
// API (xbind's delegation, docs/auth.md §Delegation). xbind lets a person's
// own agent call the tiles they can read, as them: such a call arrives with
// X-XBin-From "user:<id>", the person in X-XBin-User — and X-XBin-Agent,
// the component that is acting (xbin.Caller(r).Agent). Without that header
// the caller is the person; with it, software a model drives.
//
// This tile's API is full of answers that are a person's to give: a coding
// agent's permission request (POST /runs/{id}/harness/answer), the built-in
// agent's question and its approvals (/answer, /message, /approve), a
// sign-in (/harness/authenticate, /signins/…), what a run may do (PATCH
// /runs/{id}/harness: its mode; POST /runs with a class or a harness), who
// sees a conversation, the tile's settings. An agent that could give them
// would approve its own way through every gate here.
//
// So the rule is closed, not a list of what to refuse: a workspace agent
// uses the routes in agentMay and no other — reads included, since a read
// here can be a device code, a token in the settings or a request to a
// sandbox's port. Everything else is refused before the route's own guard
// runs, for every route there is and every route added later: agentRole
// wraps the route tables (and the hosted conversations', the homes'),
// agentRuled the routes outside it (adapters, the bus, the mailbox), and
// main wraps the whole mux. To open a route to agents, add its pattern to
// agentMay — after asking what the most an agent could do with it is.
//
// A GET that upgrades is never a read: a WebSocket (a terminal relay, a
// live preview's socket) is a channel to write on. And in a route it may
// read, one thing is still the person's: a waiting device-code sign-in's
// page and code (handleGetHarness) — the agent sees that one waits, as
// everyone else in the conversation does.
//
// Not yet: messaging a conversation here as a person's agent, each message
// under the person's ticket. A message answers a parked question, and a
// start picks a class, a harness and a mode — each needs its own design
// before its route goes into agentMay.
//
// The manifest says "agent": "read", so xbind refuses an agent's writes
// before they get here; this file is what holds if someone widens that.
// (Every route also needs the admin role, which xbind never gives an
// agent's call — an accident of how the tile is called, not a rule to lean
// on.)
package main

import (
	"net/http"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// agentMay is every route a person's workspace agent may use: a closed
// list of patterns, reads included. What it reads is a conversation as its
// person sees it — the list, a run and its view, stream, tree, tasks,
// members and files — and what the tile offers (features, models, classes,
// schedules, skills, automations, the halt). What it may do is stop a run
// and mark one read.
//
// Deliberately absent, reads too: GET /config (it holds MCP servers'
// tokens) and /usage; every sign-in route (a device code is a credential
// for whoever reads it first); the sandbox port proxy and the port probes
// (/runs/{id}/live/…, /ports: a GET there is a request to whatever listens
// in the sandbox); a run's terminal and its log; sandboxes, projects, scm,
// channels and triggers; and everything outside the admin gate — the
// adapters' routes, the bus trigger and the mailbox are for the channel
// role, the bus and xbind, never for a person's agent.
var agentMay = map[string]bool{
	"GET /me": true, "GET /health": true, "GET /features": true, "GET /models": true, "GET /classes": true,
	"GET /halt": true, "GET /schedules": true, "GET /skills": true,
	"GET /runs": true, "GET /conversations": true, "GET /needs": true, "GET /stream": true,
	"GET /runs/{id}": true, "GET /runs/{id}/view": true, "GET /runs/{id}/stream": true, "GET /runs/{id}/tree": true,
	"GET /runs/{id}/asks": true, "GET /runs/{id}/members": true, "GET /runs/{id}/harness": true,
	"GET /runs/{id}/files": true, "GET /runs/{id}/file": true, "GET /runs/{id}/raw": true, "GET /runs/{id}/thumb": true,
	"GET /runs/{id}/dl/{path...}": true, "GET /runs/{id}/zip": true,
	"GET /automations": true, "GET /automations/{kind}/{aid}": true, "GET /automations/{kind}/{aid}/runs": true,

	"POST /runs/{id}/interrupt": true,
	"POST /runs/{id}/cancel":    true,
	"POST /runs/{id}/read":      true,
}

// workspaceAgent is the component acting for the caller's person — their
// workspace agent — or "" when the caller is the person, a tile or xbind.
func workspaceAgent(r *http.Request) string { return xbin.Caller(r).Agent }

// refuseWorkspaceAgent answers 403 and reports true when r is a workspace
// agent's call to a route it may not use (the file comment). pattern is
// the route as registered ("POST /runs/{id}/approve").
func refuseWorkspaceAgent(w http.ResponseWriter, r *http.Request, pattern string) bool {
	by := workspaceAgent(r)
	if by == "" {
		return false
	}
	// a listed route — and never a GET that upgrades: a WebSocket is a
	// channel to write on, whatever its route
	if agentMay[pattern] && r.Header.Get("Upgrade") == "" {
		return false
	}
	w.Header().Set("X-Agent-Refused", "person-only")
	xbin.WriteError(w, http.StatusForbidden, "this is "+xbin.Caller(r).User+"'s to do themselves: here a person's workspace agent ("+by+") reads a conversation as they see it, and may stop a run — it answers no permission request, question, approval or sign-in, starts nothing, and reads no settings, sign-ins, terminals or sandbox ports")
	return true
}

// agentRuled is h under the workspace-agent rule: for the routes mounted
// outside agentRole (the adapters', the bus trigger, the mailbox) and, in
// main, for the whole mux — so a route someone mounts without any guard is
// still not an agent's.
func agentRuled(mux *http.ServeMux, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pattern := r.Pattern
		if pattern == "" && mux != nil {
			_, pattern = mux.Handler(r)
		}
		if refuseWorkspaceAgent(w, r, pattern) {
			return
		}
		h.ServeHTTP(w, r)
	})
}
