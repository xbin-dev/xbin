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
// READS what its person may read, and of everything else only the routes
// in agentMay — it may stop a run and mark one read. Every other POST, PUT,
// PATCH and DELETE is refused before the route's own guard runs, for every
// route there is and every route added later (agentRole wraps them all:
// the route table, the hosted conversations', the homes'). To open a route
// to agents, add its pattern to agentMay — after asking what the most an
// agent could do with it is.
//
// A read is a GET or HEAD that doesn't upgrade: a WebSocket (a terminal
// relay, a live preview's socket) is a channel to write on. And one thing
// is a person's even to read: a waiting device-code sign-in's page and
// code (handleGetHarness) — the agent sees that one waits, as everyone
// else in the conversation does.
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

// agentMay is every route that isn't a read which a person's workspace
// agent may use: stopping is always safe, and a read mark is the person's
// own bookkeeping.
var agentMay = map[string]bool{
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
	// a GET that upgrades (a terminal relay, a live preview's socket) is a
	// channel to write on, not a read
	read := (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.Header.Get("Upgrade") == ""
	if by == "" || read || agentMay[pattern] {
		return false
	}
	w.Header().Set("X-Agent-Refused", "person-only")
	xbin.WriteError(w, http.StatusForbidden, "this is "+xbin.Caller(r).User+"'s to do themselves: here a person's workspace agent ("+by+") reads, and may stop a run — it answers no permission request, question, approval or sign-in, starts nothing and changes no settings")
	return true
}
