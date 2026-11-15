package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// A person's workspace agent (xbind's delegation: X-XBin-Agent on a call
// attributed to the person) reads here and answers nothing: every route
// that isn't a read is refused to it unless agentMay names it — the
// permission, question, approval and sign-in answers and the mode change
// first of all (the 2026-11 security review's finding 5), for every role
// its call could carry. The person's own calls are untouched.
func TestWorkspaceAgentAnswersNothing(t *testing.T) {
	ag, mux := accessFixture(t)
	adapterRoutes(mux) // as main mounts them: the adapters' routes and the bus trigger
	id := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	param := regexp.MustCompile(`\{[^}]*\}`)
	call := func(pattern, role, agent string) *httptest.ResponseRecorder {
		t.Helper()
		method, path, ok := strings.Cut(pattern, " ")
		if !ok { // a route for every method: asked as a write
			method, path = "POST", pattern
		}
		path = strings.Replace(path, "{id}", strconv.FormatInt(id, 10), 1)
		path = param.ReplaceAllString(path, "x")
		r := httptest.NewRequest(method, path, strings.NewReader(`{"text":"yes","goal":"g","optionId":"allow","decision":"approve"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-XBin-From", "user:alice")
		r.Header.Set("X-XBin-Role", role)
		r.Header.Set("X-XBin-User", "alice")
		r.Header.Set("X-XBin-User-Level", "terminal")
		if agent != "" {
			r.Header.Set("X-XBin-Agent", agent)
		}
		if method == "GET" && pattern != "GET /runs/{id}" {
			// a read is only asked whether the agent rule lets it through: the
			// streams among them would wait for events that never come
			ctx, cancel := context.WithCancel(r.Context())
			cancel()
			r = r.WithContext(ctx)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	refused := func(w *httptest.ResponseRecorder) bool {
		return w.Code == http.StatusForbidden && w.Header().Get("X-Agent-Refused") != ""
	}

	// the answers, by name
	for _, pattern := range []string{
		"POST /runs/{id}/harness/answer",       // a coding agent's permission request
		"POST /runs/{id}/harness/authenticate", // its sign-in
		"PATCH /runs/{id}/harness",             // its mode and options
		"POST /runs/{id}/answer",               // the built-in agent's question
		"POST /runs/{id}/message",              // … which a message answers too
		"POST /runs/{id}/approve",              // its approvals
		"POST /runs", "POST /ask",              // a run with a class, a harness, a mode of the agent's choosing
		"PUT /config", "PUT /halt", "POST /runs/{id}/members", "POST /runs/{id}/links",
	} {
		for _, role := range []string{"admin", "writer", "reader"} {
			if w := call(pattern, role, "xbin/wsagent"); !refused(w) && w.Code != http.StatusForbidden {
				t.Errorf("alice's agent (%s) %s: %d %s", role, pattern, w.Code, w.Body.String())
			}
		}
		if w := call(pattern, "admin", "xbin/wsagent"); !refused(w) {
			t.Errorf("alice's agent %s isn't refused as an agent: %d %s", pattern, w.Code, w.Body.String())
		}
	}

	// the rule is closed: every route in every table that isn't a read
	table := append(append(append(append(append(append(routeTable(), sandboxRoutes()...), liveRoutes()...), probeRoutes()...), harnessRoutes()...),
		harnessRelayRoutes()...), append(harnessAPIRoutes(), signinRoutes()...)...)
	for _, more := range routeTables {
		table = append(table, more()...)
	}
	writes, allowed, reads := 0, 0, 0
	for _, rt := range table {
		method, _, ok := strings.Cut(rt.pattern, " ")
		if !ok {
			method = "POST"
		}
		w := call(rt.pattern, "admin", "xbin/wsagent")
		switch {
		case (method == "GET" || method == "HEAD") && agentMay[rt.pattern]:
			reads++
			if refused(w) {
				t.Errorf("alice's agent reads %s (in agentMay): refused as an agent", rt.pattern)
			}
		case method == "GET" || method == "HEAD":
			if !refused(w) { // a read nobody listed: not an agent's
				t.Errorf("alice's agent reads %s: %d — not refused, and not in agentMay", rt.pattern, w.Code)
			}
		case agentMay[rt.pattern]:
			allowed++
			if refused(w) {
				t.Errorf("alice's agent %s (in agentMay): refused", rt.pattern)
			}
		default:
			writes++
			if !refused(w) {
				t.Errorf("alice's agent %s: %d %s — not refused, and not in agentMay", rt.pattern, w.Code, w.Body.String())
			}
		}
	}
	if writes < 60 || allowed+reads != len(agentMay) {
		t.Fatalf("%d routes refused, %d of agentMay's %d found in the tables", writes, allowed+reads, len(agentMay))
	}
	// reads an agent never makes, by name: settings that hold MCP tokens, a
	// sign-in's device code, the sandbox port proxy, a terminal — and the
	// routes that sit outside the admin gate (adapters, the bus, the mailbox)
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", strings.Replace(path, "{id}", strconv.FormatInt(id, 10), 1), nil)
		for k, v := range map[string]string{"X-XBin-From": "user:alice", "X-XBin-Role": "admin", "X-XBin-User": "alice", "X-XBin-User-Level": "terminal", "X-XBin-Agent": "xbin/wsagent"} {
			r.Header.Set(k, v)
		}
		ctx, cancel := context.WithCancel(r.Context())
		cancel()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r.WithContext(ctx))
		return w
	}
	for _, path := range []string{"/config", "/usage", "/projects/scm/signin", "/projects/scm/signin/p1", "/prefs/harness-signins", "/runs/{id}/live/sb/8080/x", "/runs/{id}/live/sb/8080",
		"/runs/{id}/harness/terminal", "/runs/{id}/ports", "/channels/c1/peers", "/triggers/unmatched", "/engine/hold",
		"/adapter/outbox", "/adapter/triggers", "/adapter/links"} {
		if w := get(path); !refused(w) {
			t.Errorf("alice's agent GET %s: %d %s — not refused as an agent", path, w.Code, w.Body.String())
		}
	}
	for _, pattern := range []string{"POST /adapter/message", "POST /adapter/event", "POST /adapter/link", "POST /trigger/bus/{id}", "POST /mailbox"} {
		if w := call(pattern, "writer", "xbin/wsagent"); !refused(w) {
			t.Errorf("alice's agent %s: %d %s — not refused as an agent", pattern, w.Code, w.Body.String())
		}
	}
	if run, _ := ag.db.getRun(id); run == nil || len(mustQueued(t, ag, id)) != 0 {
		t.Fatalf("an agent's call left something behind: %+v", mustQueued(t, ag, id))
	}

	// a GET that upgrades is no read: a socket is written on
	up := httptest.NewRequest("GET", "/runs/"+strconv.FormatInt(id, 10)+"/stream", nil)
	for k, v := range map[string]string{"X-XBin-From": "user:alice", "X-XBin-Role": "admin", "X-XBin-User": "alice", "X-XBin-User-Level": "terminal",
		"X-XBin-Agent": "xbin/wsagent", "Connection": "Upgrade", "Upgrade": "websocket"} {
		up.Header.Set(k, v)
	}
	if w := httptest.NewRecorder(); true {
		mux.ServeHTTP(w, up)
		if !refused(w) {
			t.Errorf("alice's agent opens a socket: %d %s", w.Code, w.Body.String())
		}
	}

	// what it may do, and the person themselves
	if w := call("GET /runs/{id}", "admin", "xbin/wsagent"); w.Code != 200 {
		t.Errorf("alice's agent reads her conversation: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST /runs/{id}/interrupt", "admin", "xbin/wsagent"); refused(w) || w.Code >= 500 {
		t.Errorf("alice's agent stops a run: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST /runs/{id}/message", "admin", ""); w.Code != 200 {
		t.Errorf("alice herself: %d %s", w.Code, w.Body.String())
	}
}

// mustQueued is what waits in run id's inbox.
func mustQueued(t *testing.T, ag *Agent, id int64) []int64 {
	t.Helper()
	rows, err := ag.db.q.Query(`SELECT id FROM inbox WHERE run_id = ?`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var i int64
		if err := rows.Scan(&i); err != nil {
			t.Fatal(err)
		}
		out = append(out, i)
	}
	return out
}
