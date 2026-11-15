// ws_mode.go — workspace mode (docs/workspace-agent.md; D231): the same
// backend started as a person's WORKSPACE AGENT, the sealed component at
// xbin/wsagent. xbind sets XBIN_AGENT_MODE=workspace in its environment —
// from its own table of sealed components, never from anything a workspace
// wrote — and the process is then a different product with the same engine:
//
//   - its only conversations are the workspace profile's (coord_workspace.go):
//     nothing else can be made, by a route or by the engine;
//   - its only class is the fixed, built-in `workspace` class: no sandbox,
//     no web tools, no xbin_call, no coding agent of its own, and no stored
//     class is read (there is no editor);
//   - its only routes are the ones in wsMay — the conversation's own
//     (transcript, stream, message, stop, notes) and the profile's under
//     /ws/ — and every other route of the template answers 404, for every
//     route there is and every route added later: wsGate wraps the mux.
//
// Outside workspace mode nothing here is reachable: the /ws/ routes are not
// registered, the class doesn't exist, and nothing stamps a conversation
// with the profile's origin — a normal instance of the template is as it
// was.
package main

import (
	"fmt"
	"net/http"
	"os"
	"sync/atomic"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// wsModeEnv is the variable the sealed-components table sets; wsModeValue
// its one value. Unset: a normal instance. Anything else: the process
// refuses to serve (a mode this build doesn't know is never run as a
// normal instance).
const (
	wsModeEnv   = "XBIN_AGENT_MODE"
	wsModeValue = "workspace"
)

var wsModeOn atomic.Bool

// wsMode: this process is a workspace agent.
func wsMode() bool { return wsModeOn.Load() }

// detectWsMode reads the mode from the environment (main, before routes).
func detectWsMode() error {
	switch v := os.Getenv(wsModeEnv); v {
	case "":
		wsModeOn.Store(false)
	case wsModeValue:
		wsModeOn.Store(true)
	default:
		return fmt.Errorf("%s %q is a mode this agent doesn't know", wsModeEnv, v)
	}
	return nil
}

// classWorkspace is the workspace agent's class: the subagent toolset only
// (its research subagents are waited for and asked more through it), so it
// has no file, REPL, web, internal, sandbox, schedule, thread, skill or
// coding-agent tool. Its lane is private and it reaches nothing by itself:
// what the agent reaches goes through the profile's tools, as its person,
// under xbind's delegation.
const classWorkspace = "workspace"

func wsClass() agentClass {
	return agentClass{ID: classWorkspace, Name: "Workspace agent", Icon: "◇",
		Description: "Plans across the workspace, hands building to tile agents and asks before anything changes.",
		Toolsets:    []string{tsSubagents}}
}

// wsClasses is the class set of a workspace agent: its one class, whatever
// is stored.
var wsClasses = &classState{list: []agentClass{wsClass()}, def: classWorkspace, stored: map[string]bool{}}

// wsDeny are the core and subagent tools the workspace agent never has:
// it starts no general subagent (research is its one kind), schedules
// nothing, and yields to no sandbox job.
var wsDeny = []string{"subagent_spawn", "subagent_spawn_many", "yield", "schedule", "unschedule", "skill_manage", "attach_to_reply"}

// wsMay is every route of a workspace agent, by its pattern as registered
// — a closed list; wsRoutes' own are added as they register. Of the
// template's routes: who is calling and whether it is up, a conversation's
// transcript and live stream, talking to it and stopping it, its notes,
// and the engine's own handoff.
var wsMay = map[string]bool{
	"GET /me": true, "GET /health": true, "GET /halt": true,
	"GET /runs/{id}": true, "GET /runs/{id}/view": true, "GET /runs/{id}/stream": true, "GET /stream": true,
	"GET /runs/{id}/tree": true, "GET /runs/{id}/asks": true,
	"POST /runs/{id}/message": true, "POST /runs/{id}/answer": true, "DELETE /runs/{id}/inbox/{iid}": true,
	"POST /runs/{id}/interrupt": true, "POST /runs/{id}/cancel": true, "POST /runs/{id}/resume": true,
	"POST /runs/{id}/compact": true, "POST /runs/{id}/read": true,
	"PUT /runs/{id}/memory": true, "DELETE /runs/{id}/memory": true,
	"GET /engine/hold": true,
}

// wsGate is h for a workspace agent: a route that isn't in wsMay answers
// 404, before its own guard runs. Outside workspace mode it is h.
func wsGate(mux *http.ServeMux, h http.Handler) http.Handler {
	if !wsMode() {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); !wsMay[pattern] {
			xbin.WriteError(w, http.StatusNotFound, "not found")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// errWsOnly: a workspace agent holds its own conversations and no other.
var errWsOnly = fmt.Errorf("a workspace agent holds only its own conversations")

// wsRefusesRun says whether a top-level run with this origin may not be
// made here (startRunTx): in workspace mode everything but the profile's.
func wsRefusesRun(origin string) bool { return wsMode() && origin != originWorkspace }
