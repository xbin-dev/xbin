//go:build linux && integration

package isolated

// wsagent_nomode_test.go — a plan starts a coding agent whose catalog
// entry names no default mode, for real: the scripted coding agent stands
// in for one (sdk/acp's Fake), on an xbind as an install runs one. Three
// shapes (D250; its re-review; D251):
//
//   - no default mode, one asking mode it lists (--no-default-mode): the
//     line shows its work mode, "auto", which acts without asking — a
//     fact, no warning (D251) — and the session runs in it, asking its
//     person nothing; "start without asking" doesn't start it there (no
//     sheet, no mode that acts without asking), and starts it once the
//     person names the asking mode — the open puts the session IN it: the
//     agent opens in another (--start-mode=auto) and is switched before
//     the brief goes (a delegate's start is strict);
//   - an agent that refuses that switch (--refuse-mode), the line in the
//     asking mode the agent suggested: the start fails, and the brief
//     never reaches it in its own mode;
//   - no mode that asks at all (--no-asking-mode: OpenCode's shape): the
//     line shows its work mode, not asking, an ordinary line (D251) — and
//     ticked, it opens in it; the preference never starts it.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// noModeEnv is an xbind whose scripted coding agent runs with args, ada
// its admin with a main conversation, and the plan that starts it
// proposed. chosen: ada chose it for the tile herself first — a coding
// agent with no mode that asks is never taken from the agent's suggestion
// (D250 review M2), only from its person's own choice (re-review item 3).
func noModeEnv(t *testing.T, chosen bool, mode string, args ...string) (*wsEnv, *wsConv, wsPlanSet) {
	t.Helper()
	e := startWsAgentAt(t, wsNoteTile, args...)
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
	if chosen {
		d.Must(t, "PUT", "/api/xbin/agent/prefs", map[string]any{"agentSessions": map[string]any{"tiles": map[string]any{wsNoteTile: map[string]any{"harness": "fake"}}}}, 200, e.as(wsAdmin)...)
	}
	line := map[string]any{"tile": wsNoteTile, "harness": "fake", "goal": "fakeacp-steps: ls", "done_when": "the files are listed"}
	if mode != "" {
		line["mode"] = mode // the agent's suggestion: the line's mode (D251)
	}
	plan, _ := json.Marshal(map[string]any{"title": "List the note tile", "changes": []any{
		map[string]any{"kind": "agent.start", "args": line, "why": "A coding agent looks"},
	}})
	turn := ada.say("ws: plan_propose " + string(plan))
	if got := turn.tool("plan_propose", 0); !strings.Contains(got, "Proposed as one plan") {
		t.Fatalf("plan_propose: %s", got)
	}
	all := e.sets(e.as(wsAdmin)...)
	if len(all) != 1 || len(all[0].Items) != 1 || all[0].Items[0].Blocked != "" || all[0].Items[0].Start.Provider != "fake" {
		t.Fatalf("ada's change sets: %+v, want one plan of one start of the fake coding agent", all)
	}
	return e, ada, all[0]
}

// runsPlanStart applies the plan's line as ada and waits for its tile
// agent to end its turn, answering what it asks: the session, and how many
// times it asked.
func (e *wsEnv) runsPlanStart(t *testing.T, cs wsPlanSet) (wsTileSession, int) {
	t.Helper()
	d := e.d
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
		for _, p := range detail.Permissions { // its person answers what it asks
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
	return mine, answers
}

// unasked is the same start with no ticket — refused with no preference —
// then under ada's "start without asking" preference (with modes, the
// modes she named for agent-started sessions): its answer then.
func (e *wsEnv) unasked(t *testing.T, ada *wsConv, sessAPI string, modes map[string]any) wsDelegateCall {
	t.Helper()
	start := `POST /api/xbin/term/sessions {"kind":"agent","cwd":"` + wsNoteTile + `","provider":"fake","prompt":"ls"}`
	got := e.asDelegate(wsAdmin, ada.handle, "GET "+sessAPI, start)
	if got[0].Status != 200 {
		t.Fatalf("the agent's credential reads the session it started: %d %s", got[0].Status, got[0].Body)
	}
	if got[1].Status != 403 || !strings.Contains(got[1].Body, "needs-approval") {
		t.Fatalf("a start with no ticket and no preference: %d %s, want 403 needs-approval", got[1].Status, got[1].Body)
	}
	pref := map[string]any{"start": "allow"}
	if modes != nil {
		pref["modes"] = modes
	}
	e.d.Must(t, "PUT", "/api/xbin/agent/prefs", map[string]any{"agentSessions": pref}, 200, e.as(wsAdmin)...)
	got = e.asDelegate(wsAdmin, ada.handle, "GET "+sessAPI, start)
	t.Logf("the start under the preference: %d %s", got[1].Status, got[1].Body)
	return got[1]
}

// A coding agent with no default mode that lists one asking mode: shown,
// opened and run in its work mode, which acts without asking (D251) — and
// put in its asking mode before its brief when its person's preference
// names it, though it opens in another.
func TestWorkspaceAgentStartsAHarnessWithNoDefaultMode(t *testing.T) {
	e, ada, cs := noModeEnv(t, false, "", "--no-default-mode", "--auto-mode", "--start-mode=auto")
	it := cs.Items[0]
	if it.Start.Mode != "auto" || it.Start.Asks || !strings.Contains(it.Line, `in its mode "auto", which acts without asking`) || len(it.Warnings) != 0 {
		t.Fatalf("the start line of a coding agent with no default mode: %+v — want it shown in its work mode, acting without asking, no warning", it)
	}
	mine, answers := e.runsPlanStart(t, cs)
	if mine.Mode != "auto" || answers != 0 {
		t.Fatalf("the session the plan opened: mode %q, %d questions to its person — want it in its work mode, asking nothing", mine.Mode, answers)
	}
	// "start without asking" with no mode named: a start that would act without asking goes to the sheet (D251)
	if got := e.unasked(t, ada, "/api/xbin/term/sessions/"+mine.ID, nil); got.Status != 403 || !strings.Contains(got.Body, "every start is approved on the sheet") {
		t.Fatalf("a start under ada's preference, no mode named: %d %s — want it refused to the sheet", got.Status, got.Body)
	}
	// …and with the asking mode named, it starts — in that mode, put there before the brief
	if got := e.unasked(t, ada, "/api/xbin/term/sessions/"+mine.ID, map[string]any{"fake": "ask"}); got.Status != 200 {
		t.Fatalf("a start under ada's preference in the asking mode: %d %s — want it opened, in its asking mode", got.Status, got.Body)
	}
	xbindtest.Eventually(t, time.Minute, "the start under the preference is up, in the asking mode", func() (bool, string) {
		ss := e.tileAgents(wsAdmin)
		for _, s := range ss {
			if s.ID != mine.ID && s.Status != "starting" {
				return s.Mode == "ask", fmt.Sprintf("%+v", s)
			}
		}
		return false, fmt.Sprintf("%+v", ss)
	})
}

// One that refuses to be put in the mode its line shows: the start fails,
// and its brief never reaches it in its own.
func TestWorkspaceAgentHarnessRefusingItsAskingModeNeverGetsTheBrief(t *testing.T) {
	e, _, cs := noModeEnv(t, false, "ask", "--no-default-mode", "--auto-mode", "--start-mode=auto", "--refuse-mode")
	if it := cs.Items[0]; it.Start.Mode != "ask" || !it.Start.Asks {
		t.Fatalf("the start line: %+v, want it in the asking mode the agent suggested", it)
	}
	var done wsPlanSet
	e.d.Must(t, "POST", "/api/xbin/delegate/changesets/"+cs.ID+"/approve", map[string]any{"items": []int{1}}, 200, e.as(wsAdmin)...).Decode(t, &done)
	if done.Items[0].Result != "failed" || !strings.Contains(done.Items[0].Error, "can't be put in mode ask") {
		t.Fatalf("applying a start the agent won't be put in its asking mode for: %s (%s) — want it failed", done.Items[0].Result, done.Items[0].Error)
	}
	// what the open left of it never took a prompt: the brief didn't reach it in its own mode
	var rows []struct {
		ID, Status, Mode string
		Turn             int
	}
	e.d.Must(t, "GET", "/api/xbin/term/sessions?origin=agent&compact=1", nil, 200, e.as(wsAdmin)...).Decode(t, &rows)
	for _, r := range rows {
		if r.Turn != 0 || r.Status == "idle" || r.Status == "running" {
			t.Fatalf("a session after the refused mode took a prompt, or runs: %+v", r)
		}
	}
}

// One with no mode that asks at all (OpenCode's shape): an ordinary line
// (D251), opened in its own mode when ticked; never by the preference.
func TestWorkspaceAgentStartsAHarnessWithNoAskingMode(t *testing.T) {
	e, ada, cs := noModeEnv(t, true, "", "--no-asking-mode", "--auto-mode", "--start-mode=auto")
	it := cs.Items[0]
	// (the scripted agent lists its modes, so its work mode is its own "auto"; OpenCode lists none: no mode named)
	if it.Start.Mode != "auto" || it.Start.Asks || !strings.Contains(it.Line, `in its mode "auto", which acts without asking`) || len(it.Warnings) != 0 {
		t.Fatalf("the start line of a coding agent with no mode that asks: %+v — want it shown in its work mode, not asking, no warning (D251)", it)
	}
	mine, _ := e.runsPlanStart(t, cs)
	if mine.Mode != "auto" {
		t.Fatalf("the session the plan opened: mode %q — want the one it opens in itself", mine.Mode)
	}
	got := e.unasked(t, ada, "/api/xbin/term/sessions/"+mine.ID, nil)
	if got.Status != 403 || !strings.Contains(got.Body, "every start is approved on the sheet") {
		t.Fatalf("a start under ada's \"start without asking\" preference: %d %s — want it refused: no mode of this coding agent asks", got.Status, got.Body)
	}
	if n := len(e.tileAgents(wsAdmin)); n != 1 {
		t.Fatalf("%d tile agents after the refused start, want the plan's one", n)
	}
}
