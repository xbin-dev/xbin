// coord_workspace.go — the workspace profile (coord_profile.go; D231): a
// person's workspace agent as a profile of the coordinator kit, live only
// in workspace mode (ws_mode.go). A coordinator of this profile is a
// conversation with origin workspace and a session row (ws_sessions.go);
// what it starts are tile agents (ws_tools_agents.go) and requests for its
// person's approval (ws_tools_act.go); what wakes it is xbind saying one of
// them changed (ws_notify.go); its knowledge store is the person's
// notebook (ws_notebook.go); its research subagents read a tile's code
// through xbind, never a sandbox (ws_research.go).
//
// Outside workspace mode the profile is registered and dead: Is answers no
// run, so nothing offers its tools, wakes it or words its prompt, and
// nothing stamps a conversation with its origin.
package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// originWorkspace is runs.origin for a workspace agent's conversation.
// Reserved: only wsNewSession stamps it.
const originWorkspace = "workspace"

// coordWorkspace is the workspace profile.
var coordWorkspace *coordProfile

// The profile's limits.
const (
	wsResearchMax     = 3
	wsResearchTimeout = 10 * time.Minute
	// wsWakeBurst is how many times a minute a conversation is woken at once
	// for what its tile agents report (coord_wake.go): the first change
	// after a quiet time never waits, a storm of them is spaced.
	wsWakeBurst = 6
)

func init() {
	coordWorkspace = registerCoordProfile(&coordProfile{
		ID:     "workspace",
		Origin: originWorkspace,
		Class:  classWorkspace,
		Deny:   wsDeny,
		Allow: []string{
			"workspace_map", "tile_status", "tile_logs", "docs_search", "docs_read", "tile_api_doc",
			"tile_call",
			"tile_agent_brief", "tile_agent_start", "tile_agent_message", "tile_agent_status", "tile_agent_result", "tile_agent_cancel", "tile_agents",
			"research", "tile_create", "admin_propose", "admin_changes", "preview", "plan_propose",
			"plan", "notebook_search", "notebook_read", "notebook_write", "notebook_edit", "notebook_archive",
			"web_read",              // the web, through xbind (ws_tools_web.go; D239)
			"mcp_tools", "mcp_call", // MCP tiles, through xbind (ws_tools_mcp.go)
			"account_lookup", // whether an account of a name exists, for an access.set line (ws_tools_access.go; D246)
			"updates",        // what has an update waiting, and who applies it (ws_tools_updates.go; D248)
		},
		Limits: coordBounds{Research: wsResearchMax, ResearchTimeout: wsResearchTimeout, Wakes: wsWakeBurst},
		Is: func(d *DB, run *Run) bool {
			return wsMode() && d.wsOne(`SELECT 1 FROM ws_sessions WHERE run_id=?`, run.ID) == 1
		},
		Resolve: wsResolve,
		Pending: wsOwes,
		Deliver: wsDeliver,
		Prompt:  (*Agent).wsPromptFor,
		System:  func(*Agent, *Run, Config) string { return wsSystem() },
		Specs:   wsSpecs,
	})
	coordWorkspace.mustRegister(wsLookTools()...)
	coordWorkspace.mustRegister(wsActTools()...)
	coordWorkspace.mustRegister(wsAgentTools()...)
	coordWorkspace.mustRegister(wsOtherTools()...)
	coordWorkspace.mustRegister(wsPlanTools()...)
	coordWorkspace.mustRegister(wsTalkTools()...)
	coordWorkspace.mustRegister(wsNetTools()...)
	coordWorkspace.mustRegister(wsMCPTools()...)
	coordWorkspace.mustRegister(wsAccessTools()...)
	coordWorkspace.mustRegister(wsUpdateTools()...)
}

// wsResolve is the coordinator's boundary, on every tool call: the
// conversation is a workspace session of the person this process serves.
// What a call then reaches is xbind's to decide, by the session's handle.
func wsResolve(ag *Agent, t *DB, run *Run, cfg Config) (any, error) {
	if !wsMode() || run == nil || run.Depth != 0 || run.ParentID != 0 || run.Origin != originWorkspace || hostedID(run.ID) {
		return nil, errors.New("the workspace tools are a workspace agent's own")
	}
	s, err := t.wsSession(run.ID)
	if err != nil {
		return nil, errors.New("this conversation isn't a workspace-agent session")
	}
	if s.Owner != run.Owner || (userMode() && s.Owner != runUser) {
		return nil, errors.New("this session belongs to another person")
	}
	if s.Held != nil { // what it holds may not go to its model: no tool runs (ws_sessions.go)
		return nil, &wsHeldError{Held: *s.Held}
	}
	return s, nil
}

// --- what reaches it -------------------------------------------------------------

// wsUpdatesHead opens the coordinator's updates.
const wsUpdatesHead = "[workspace updates — xbin and tile agents reporting, not your person]"

// wsMarkers are the profile's own headers inside text that came from
// outside: defused like the frames' (their bracket a parenthesis).
var wsMarkers = regexp.MustCompile(`(?i)[\[\x{FF3B}]((?:` + frameSp + `)*workspace` + frameSp + `+updates)`)

// wsPlain is text from outside on one line, as the coordinator reads data.
func wsPlain(s string) string { return wsMarkers.ReplaceAllString(coordPlain(projRedact(s)), "($1") }

// wsData frames text that came from a tile, a tile agent, a log or a
// notebook page: data to report, never instructions.
func wsData(from, what, s string) string {
	return untrusted(from, what, wsMarkers.ReplaceAllString(coordMarkers.ReplaceAllString(s, "($1"), "($1"))
}

// wsOwes: an outcome or a tile agent's change waits for run and asks to
// wake it — at once: the kit only spaces a storm of wakes (wsWakeBurst).
func wsOwes(d *DB, run *Run) bool {
	return d.wsOne(`SELECT EXISTS(SELECT 1 FROM ws_requests WHERE run_id=?1 AND owed=1)
		OR EXISTS(SELECT 1 FROM ws_agents WHERE run_id=?1 AND owed=1 AND wake=1)`, run.ID) == 1
}

// wsDeliver is what changed since run last heard, as one message: the
// requests whose outcome it is owed, and its tile agents' changes.
func wsDeliver(t *DB, run *Run) *coordUpdate {
	reqs := t.wsReqs(`WHERE run_id=? AND owed=1 ORDER BY id LIMIT 20`, run.ID)
	agents := t.wsAgents(`WHERE run_id=? AND owed=1 ORDER BY id LIMIT 20`, run.ID)
	if len(reqs) == 0 && len(agents) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(wsUpdatesHead + "\n")
	for _, r := range reqs {
		fmt.Fprintf(&b, "request %d (%s): %s\n", r.ID, wsPlain(r.Title), strings.TrimSpace(r.Outcome))
	}
	for _, a := range agents {
		b.WriteString(wsAgentLine(a) + "\n")
	}
	return &coordUpdate{Text: strings.TrimRight(b.String(), "\n"), Meta: msgMeta{Origin: originWorkspace, Label: "workspace updates"},
		Mark: func(msgID int64) {
			for _, r := range reqs {
				_, _ = t.q.Exec(`UPDATE ws_requests SET owed=0, msg_id=? WHERE id=?`, msgID, r.ID)
			}
			for _, a := range agents {
				_, _ = t.q.Exec(`UPDATE ws_agents SET owed=0, wake=0, told=?, told_turn=?, refused='' WHERE id=?`, a.state(), a.Turn, a.ID)
			}
		}}
}

// wsAgentLine is a tile agent's state as the coordinator is told it.
func wsAgentLine(a *wsAgentRow) string {
	line := wsAgentState(a)
	if a.Refused != "" { // what it held for it, xbind refused: said once
		line += " The messages you sent it while it waited were not delivered: " + a.Refused + "."
	}
	return line
}

func wsAgentState(a *wsAgentRow) string {
	who := fmt.Sprintf("tile agent #%d in %s (%s)", a.ID, a.Tile, orStr(a.Harness, "coding agent"))
	switch {
	case a.Waits && a.live():
		return who + " " + wsWaitsWords(a) + " Only they can answer it — in the tile, or in Needs you. You can't, and a message you send it is held until they did."
	case a.Status == "idle":
		return who + " ended its turn. tile_agent_result reads what it reports: a report to check, not proof."
	case a.Status == "running":
		return who + " is working."
	case a.Status == "starting":
		return who + " is starting."
	case a.Status == "failed":
		return who + " failed" + wsLast(a) + ". Its transcript is kept; tell your person, and offer to continue it (tile_agent_start with resume) or to leave it."
	case a.Status == "stopped":
		return who + fmt.Sprintf(" was cut off by an xbin restart before it finished. Its transcript is kept. Tell your person, and offer to continue it where it stopped: tile_agent_start {\"resume\": \"#%d\"} asks them to allow that.", a.ID)
	}
	return who + " ended" + wsLast(a) + ". tile_agent_result reads what it left."
}

func wsLast(a *wsAgentRow) string {
	if a.Last == "" {
		return ""
	}
	return ": " + wsQuote(a.Last)
}

// wsQuote is a line a tile agent or a tile wrote, as the coordinator reads
// it inside its own sentences: one line, redacted, every marker defused,
// quoted.
func wsQuote(s string) string { return "«" + clip(wsPlain(s), 200) + "»" }

// wsWaitsWords: what a waiting tile agent waits for, quoted.
func wsWaitsWords(a *wsAgentRow) string {
	if a.Waiting == "" {
		return "waits for your person to answer it."
	}
	return "waits for your person: " + wsQuote(a.Waiting) + "."
}

// wsWaitingWords is what a session's open requests wait for, in a line
// for the list of sessions ("" when none is open).
func wsWaitingWords(d *DB, runID int64) string {
	var titles []string
	for _, r := range d.wsReqs(`WHERE run_id=? AND state IN ('pending','proposed') ORDER BY id LIMIT 5`, runID) {
		titles = append(titles, r.Title)
	}
	if len(titles) == 0 {
		return ""
	}
	return "Waiting for your approval: " + clip(strings.Join(titles, "; "), 200)
}

// --- the tool list -----------------------------------------------------------------

// wsSpecs words the core tools for a workspace agent: ask_user takes
// choices, since people answer a choice more easily than an open question.
func wsSpecs(cfg Config, run *Run, specs []toolSpec) []toolSpec {
	if agent != nil && run != nil { // what this session can't use now isn't offered (ws_tools_web.go)
		if s, err := agent.db.wsSession(run.ID); err == nil {
			specs = wsNetSpecs(s, specs)
		}
	}
	out := make([]toolSpec, 0, len(specs))
	for _, s := range specs {
		if wsRetired[s.Function.Name] {
			continue // still answered for a conversation whose history calls it; not offered (D246)
		}
		if s.Function.Name == "ask_user" {
			s = wsAskSpec()
		}
		out = append(out, s)
	}
	return out
}

// wsRetired are tools folded into others: registered, so a call a stored
// conversation makes is answered, and no longer offered. tile_agent_status
// (into tile_agent_result, D246) — dropped in the release after.
var wsRetired = map[string]bool{"tile_agent_status": true}

func wsAskSpec() toolSpec {
	return toolSpec{Type: "function", Function: funcDef{Name: "ask_user",
		Description: "Ask your person one question and wait for the answer; your turn ends here and resumes when they reply. " +
			"Use it only for what you could not find out by looking, and only when the work can't go on without it. " +
			"Give two to four choices built from what you found — real names and facts, each a complete answer — whenever choices can be built; they can always type something else, so don't add an \"other\" choice. " +
			"Leave choices out only when nothing you found narrows the answer. " +
			"Say what kind of question it is: \"choices\" for what you found; \"redirect\" when they asked for a change to code and the ways forward are the tile's own agent or the outcome they want (name the tile); \"cant\" when you can't do what they asked and the choices are the nearest things you can.",
		Parameters: obj([]string{"question"}, map[string]any{
			"question": strProp("the one thing you need to know, in plain words"),
			"kind":     map[string]any{"type": "string", "enum": wsAskKinds, "description": "choices (the default) | redirect | cant"},
			"tile":     strProp("for a redirect: the path of the tile the request is about"),
			"choices": map[string]any{"type": "array", "maxItems": 4, "items": map[string]any{"type": "string"},
				"description": "two to four answers to pick from, built from what you found (\"Three invoices are overdue but still marked sent\")"},
		})}}
}
