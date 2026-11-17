//go:build linux && integration

package isolated

// wsagent_nomode_test.go — a plan starts a coding agent whose catalog
// entry names no default mode (OpenCode's: it opens as it opens itself),
// for real: the scripted coding agent stands in for one (sdk/acp's Fake
// with --no-default-mode), on an xbind as an install runs one.
//
//   - the plan's start line shows no mode ("in its own default mode"), is
//     warned ACTS WITHOUT ASKING — xbind can't say it asks — and says the
//     session doesn't ask; the review starts a warned line unticked;
//   - ticked and applied by its person, the line opens the REAL session
//     with Mode "": the tile agent runs, and its start is on record under
//     the plan with no mode;
//   - the person's "start without asking" preference never starts it: with
//     no mode known to ask, a start under the preference is refused.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

func TestWorkspaceAgentStartsAHarnessWithNoDefaultMode(t *testing.T) {
	e := startWsAgentAt(t, wsNoteTile, "--no-default-mode")
	d := e.d
	e.staticTile(wsNoteTile, "Note")
	d.Must(t, "PUT", "/api/xbin/agents/policy", map[string]any{"credentials": "connector-only"}, 200)
	e.person(wsAdmin, "admin", nil)
	d.Must(t, "PUT", "/api/xbin/delegate/workspace", map[string]any{"enabled": true, "who": "everyone"}, 200, e.as(wsAdmin)...)
	ada := e.open(wsAdmin, map[string]any{"main": true})
	t.Cleanup(func() {
		if t.Failed() {
			ada.transcript(t)
		}
	})

	// the catalog entry as this xbind has it: no default mode
	var provs []struct{ ID, DefaultMode string }
	d.Must(t, "GET", "/api/xbin/agent/providers", nil, 200, e.as(wsAdmin)...).Decode(t, &provs)
	found := false
	for _, p := range provs {
		if p.ID == "fake" {
			found = true
			if p.DefaultMode != "" {
				t.Fatalf("the scripted coding agent's default mode is %q: this test needs one with none", p.DefaultMode)
			}
		}
	}
	if !found {
		t.Fatalf("no fake coding agent among xbind's providers: %+v", provs)
	}

	plan, _ := json.Marshal(map[string]any{"title": "List the note tile", "changes": []any{
		map[string]any{"kind": "agent.start", "args": map[string]any{"tile": wsNoteTile, "harness": "fake", "goal": "fakeacp-steps: ls", "done_when": "the files are listed"}, "why": "A coding agent looks"},
	}})
	turn := ada.say("ws: plan_propose " + string(plan))
	if got := turn.tool("plan_propose", 0); !strings.Contains(got, "Proposed as one plan") {
		t.Fatalf("plan_propose: %s", got)
	}
	all := e.sets(e.as(wsAdmin)...)
	if len(all) != 1 || len(all[0].Items) != 1 {
		t.Fatalf("ada's change sets: %+v, want one plan of one line", all)
	}
	cs := all[0]
	it := cs.Items[0]
	if it.Blocked != "" || it.Start.Provider != "fake" || it.Start.Mode != "" || it.Start.Asks || !strings.Contains(it.Line, "in its own default mode") ||
		len(it.Warnings) != 1 || !strings.HasPrefix(it.Warnings[0], "ACTS WITHOUT ASKING: ") {
		t.Fatalf("the start line of a coding agent with no default mode: %+v — want it shown with no mode, not asking, warned ACTS WITHOUT ASKING (the review starts a warned line unticked)", it)
	}

	// its person ticks it and applies: the real session opens, with no mode
	var done wsPlanSet
	d.Must(t, "POST", "/api/xbin/delegate/changesets/"+cs.ID+"/approve", map[string]any{"items": []int{1}}, 200, e.as(wsAdmin)...).Decode(t, &done)
	if done.Items[0].Result != "applied" || done.Items[0].Out.Session == "" {
		t.Fatalf("applying the start: %s (%s), session %q", done.Items[0].Result, done.Items[0].Error, done.Items[0].Out.Session)
	}
	sid := done.Items[0].Out.Session
	sessAPI := "/api/xbin/term/sessions/" + sid
	var mine wsTileSession
	answers := 0
	xbindtest.Eventually(t, 4*time.Minute, "the tile agent ran its step and ended its turn", func() (bool, string) {
		var detail struct {
			Permissions []struct{ Pid string }
		}
		r := d.Call(t, "GET", sessAPI, nil, e.as(wsAdmin)...)
		if r.Status != 200 || json.Unmarshal(r.Body, &detail) != nil {
			return false, r.String()
		}
		for _, p := range detail.Permissions { // it asks or not as it opened itself: its person answers what it asks
			if d.Call(t, "POST", sessAPI+"/permissions/"+p.Pid, map[string]any{"optionId": "once"}, e.as(wsAdmin)...).Status == 200 {
				answers++
			}
		}
		for _, s := range e.tileAgents(wsAdmin) {
			if s.ID == sid {
				mine = s
			}
		}
		return mine.Status == "idle", fmt.Sprintf("session %+v, %d answers", mine, answers)
	})
	if mine.Provider != "fake" || mine.Cwd != wsNoteTile || mine.Origin == "" {
		t.Fatalf("the session the plan opened: %+v, want the fake coding agent on %s, started by ada's agent", mine, wsNoteTile)
	}
	started := false
	for _, row := range e.audit(wsAdmin) {
		started = started || row.Class == "changeset:agent.start" && row.Ticket == cs.ID && row.Status == 200
	}
	if !started {
		t.Errorf("ada's log has no start under the plan %s", cs.ID)
	}

	// the same start with no ticket: refused until ada allows starts without asking — and refused then too,
	// because nothing says this coding agent asks before it acts
	start := `POST /api/xbin/term/sessions {"kind":"agent","cwd":"` + wsNoteTile + `","provider":"fake","prompt":"ls"}`
	got := e.asDelegate(wsAdmin, ada.handle, "GET "+sessAPI, start)
	if got[0].Status != 200 {
		t.Fatalf("the agent's credential reads the session it started: %d %s", got[0].Status, got[0].Body)
	}
	if got[1].Status != 403 || !strings.Contains(got[1].Body, "needs-approval") {
		t.Fatalf("a start with no ticket and no preference: %d %s, want 403 needs-approval", got[1].Status, got[1].Body)
	}
	d.Must(t, "PUT", "/api/xbin/agent/prefs", map[string]any{"agentSessions": map[string]any{"start": "allow"}}, 200, e.as(wsAdmin)...)
	got = e.asDelegate(wsAdmin, ada.handle, "GET "+sessAPI, start)
	// (the preference doesn't reach a coding agent with no mode it can be held to — the start still needs a
	// ticket — and the open itself refuses one that got that far: "needs a mode known to ask its person")
	if got[1].Status != 403 || !strings.Contains(got[1].Body, "needs-approval") && !strings.Contains(got[1].Body, "needs a mode known to ask its person") {
		t.Fatalf("a start under ada's \"start without asking\" preference: %d %s — want it refused: no mode of this coding agent is known to ask", got[1].Status, got[1].Body)
	}
	t.Logf("the start under the preference: %d %s", got[1].Status, got[1].Body)
	if n := len(e.tileAgents(wsAdmin)); n != 1 {
		t.Fatalf("%d tile agents after the refused start, want the plan's one", n)
	}
}
