package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// the plan the prompt and the tool ask for (D238, D243): the tool's tile —
// empty: there is no template to stamp one from —, the tile agent that
// builds the MCP server in it, the assistant, its model, and the binding
// that waits for what the tile agent writes — one call, five lines.
const wsDicePlan = `{"title":"A dice tool and an assistant that can use it","changes":[
	{"kind":"tile.create","args":{"template":"starter","name":"Dice"},"why":"The tool lives in a tile of its own"},
	{"kind":"agent.start","args":{"tile":"Dice","template":"mcp-tool","tool":{"name":"roll_dice","arguments":"sides: integer, default 6","result":"each roll and their sum"}},"why":"A coding agent builds the tool"},
	{"kind":"tile.create","args":{"template":"agent","name":"Assistant"},"why":"The assistant you will chat with"},
	{"kind":"binding.set","args":{"component":"Assistant","slot":"llm","provider":"xbin:llm"},"why":"The assistant needs a model to answer at all"},
	{"kind":"binding.set","args":{"component":"apps/assistant","slot":"mcp","provider":"Dice"},"why":"This is what gives the assistant the tool"}]}`

func planOf(t *testing.T, fx *wsFix) *wsChangeSet {
	t.Helper()
	fx.x.mu.Lock()
	defer fx.x.mu.Unlock()
	var out *wsChangeSet
	for _, cs := range fx.x.sets {
		if cs.State == "proposed" {
			if out != nil {
				t.Fatal("more than one plan waits")
			}
			out = cs
		}
	}
	if out == nil {
		t.Fatal("no plan waits")
	}
	return out
}

// Scenario — the dice plan, end to end (the owner's benchmark): one prompt,
// one plan, one review. The model proposes the five steps as ONE plan and
// files no single request; the tiles' names resolve to the paths the plan
// creates; the person applies it; the tile agent it started is this
// conversation's to follow; when that reports, the model reads its result
// and checks the hook-up itself — the assistant's own /tools — before it
// says done.
func TestWsEvalDicePlan(t *testing.T) {
	fx := newWsFix(t)
	fx.x.mu.Lock()
	fx.x.state.CanAdminChanges = true // an admin who never opted in: the review offers it
	fx.x.api["GET /api/apps/assistant/tools"] = func([]byte) (int, string) {
		return 200, `{"classes":[{"name":"Internal","tools":[{"name":"roll_dice","source":"apps/dice"}]}]}`
	}
	fx.x.mu.Unlock()
	s := fx.main(t)
	f := fakeOf(fx.ag)
	var offered []toolSpec
	f.on(forRun(s.Run, lastUser("rolls dice")), func(r LLMRequest) (LLMReply, error) {
		offered = r.Tools
		return callTools(tc("p1", "plan_propose", wsDicePlan))(r)
	}).once()
	f.on(forRun(s.Run, lastTool("plan_propose")), say("I've proposed one plan of five changes: review it and apply what you want.")).once()
	fx.say(t, s, "Make a tool that rolls dice and give it to a new assistant I can chat with.")
	fx.idle(t, s)

	for _, o := range offered {
		if o.Function.Name == "plan_propose" && !strings.Contains(o.Function.Description, "never a chain of single requests") {
			t.Errorf("plan_propose as offered doesn't say what it replaces: %s", o.Function.Description)
		}
	}
	if !strings.Contains(wsSystem(), "are ONE plan (plan_propose), never a chain of single requests") || !strings.Contains(wsSystem(), "Applied is not done") || !strings.Contains(wsSystem(), "built, not stamped") {
		t.Error("the prompt doesn't teach the plan")
	}
	// one prompt, one review: a steps plan shown first ends the turn on "wait for their go-ahead", and the person
	// says go twice — a real model did, one journey in four (the real-model run, 2026-11-17). The prompt's rule
	// about showing a plan and the steps tool both say the proposed plan is not preceded by one.
	if !strings.Contains(wsSystem(), "Not for changes that create or bind: propose those at once (Plans, below) — xbin's review is the plan shown, and their go-ahead.") {
		t.Error("the prompt's \"show the plan first\" doesn't except the changes a plan_propose carries")
	}
	for _, o := range f.callsFor(s.Run)[0].Tools {
		if o.Function.Name == "plan" && !strings.Contains(o.Function.Description, "Not before plan_propose") {
			t.Errorf("the steps plan as offered doesn't say it is not a step before plan_propose: %s", o.Function.Description)
		}
	}
	got := fx.toolResult(t, s, "plan_propose")
	for _, want := range []string{
		"1. [tile.create] Create the tile apps/dice from the template starter — yours",
		"2. [agent.start] Start a tile agent in apps/dice",
		"4. [binding.set] Bind apps/assistant's llm to xbin:llm — WARNING: SPEND:",
		"5. [binding.set] Bind apps/assistant's mcp to apps/dice",
		"Line 5 waits by design: your person approves it on the review, and xbin applies it by itself once apps/dice provides mcp",
		"Line 4 takes an admin's rights",
		"Proposed as one plan; nothing is applied.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the plan's result lacks %q:\n%s", want, got)
		}
	}
	// one proposal, and not one single request
	if fx.x.count("ProposePlan") != 1 || fx.x.count("Ask") != 0 || fx.x.count("Propose cs") != 0 || len(fx.x.requests) != 0 {
		t.Fatalf("what was filed: %v", fx.x.calls)
	}
	cs := planOf(t, fx)
	// the brief the tile agent will be told is the template's, complete — it is the line's own argument
	// — and it tells a coding agent in an EMPTY tile what makes it an MCP server: the provide xbind's waiting line is pinned to, and where the interface is
	brief := str(fx.x.planArgs[cs.ID+"/2"]["brief"])
	for _, want := range []string{"Done when: this tile's xbin.json declares \"provides\": {\"mcp\": {\"kind\": \"http\", \"service\": \"mcp\", \"role\": \"reader\"}}", "tools/list on this tile's /mcp names `roll_dice`",
		"$XBIN_URL/docs/mcp.md?raw=1", "a wider role is not bound", "handed to you by alice's workspace agent"} {
		if !strings.Contains(brief, want) {
			t.Fatalf("the start line's brief lacks %q:\n%s", want, brief)
		}
	}
	if l := cs.Items[4]; len(l.Needs) != 3 || l.Wait == nil || l.Wait.Tile != "apps/dice" || l.Wait.Service != "mcp" {
		t.Fatalf("line 5 waits on the tile, its tile agent and the assistant: needs %v, wait %+v", l.Needs, l.Wait)
	}

	// the person ticks the SPEND line and applies all five: four are applied, the binding waits
	f.on(forRun(s.Run, lastUser("Line 2 started tile agent #1")), say("Applied. The tile agent is building the tool; the binding waits for it. I'll check when it reports.")).once()
	fx.x.applyPlan(cs.ID, []int{1, 2, 3, 4, 5}, 0)
	fx.notify(t)
	waitFor(t, "the decision's update", func() bool { return strings.HasPrefix(fx.lastAssistant(t, s), "Applied.") })
	fx.idle(t, s)
	up := fx.updates(t, s)
	for _, want := range []string{"your person decided", "Line 2 started tile agent #1 in apps/dice (Claude Code, asks before acting)", "The work isn't done until the tile agent reports and you have checked",
		"5. [binding.set] Bind apps/assistant's mcp to apps/dice — approved, and waiting", "Line 5 is approved and waits: xbin applies it by itself, as your person, once apps/dice provides mcp", "don't say it is"} {
		if !strings.Contains(up[0], want) {
			t.Errorf("the update lacks %q:\n%s", want, up[0])
		}
	}
	rows := fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)
	if len(rows) != 1 || rows[0].Tile != "apps/dice" || rows[0].CallID != "p1" {
		t.Fatalf("the tile agent the plan started isn't this conversation's to follow: %+v", rows)
	}
	// the plan's row is followed on while its line waits; its card says what the line waits for
	if r := fx.ag.db.wsReqs(`WHERE run_id=?`, s.Run); len(r) != 1 || r[0].State != wsReqWaiting || !strings.Contains(r[0].Title, "a plan of 5 change(s)") || r[0].Result != "4 of 5 changes applied" {
		t.Fatalf("the plan's row: %+v", r)
	}
	var meta wsPlanMeta
	_ = json.Unmarshal(fx.ag.db.wsReqs(`WHERE run_id=?`, s.Run)[0].Body, &meta)
	if l := meta.Lines[4]; l.Result != "waiting" || l.Waits != "apps/dice to provide mcp" || l.UntilMs == 0 {
		t.Fatalf("the card's waiting line: %+v", l)
	}

	// the tile agent reports; the model reads the report — the binding still waits, so it isn't done
	fx.x.mu.Lock()
	fx.x.reports[rows[0].Session] = wsAgentReport{Status: "end_turn", Answer: "Wrote the MCP server; tools/list names roll_dice.", Turn: 1}
	fx.x.mu.Unlock()
	f.on(forRun(s.Run, lastUser("ended its turn")), callTools(tc("v1", "tile_agent_result", `{"agent":"#1"}`))).once()
	f.on(forRun(s.Run, lastTool("tile_agent_result")), say("The tile agent reports the server is written. The binding to your assistant still waits for xbin; I'll check once it is applied.")).once()
	fx.x.setAgent(rows[0].Session, func(a *wsAgent) { a.Status, a.Last = "idle", "Wrote the MCP server." })
	fx.notify(t)
	waitFor(t, "the report", func() bool { return strings.HasPrefix(fx.lastAssistant(t, s), "The tile agent reports") })
	fx.idle(t, s)

	// xbind applies the waiting line by itself: the conversation is told, and checks the hook-up before it says done
	f.on(forRun(s.Run, lastUser("was applied by xbin now")), callTools(tc("v2", "tile_call", `{"tile":"apps/assistant","method":"GET","path":"/tools"}`))).once()
	f.on(forRun(s.Run, lastTool("tile_call")), say("Done: Dice has roll_dice and your assistant lists it. Checked: the tile agent's report and the assistant's tool list. Not checked: a conversation in the assistant.")).once()
	fx.x.waited(cs.ID, 5, "applied", "")
	fx.notify(t)
	waitFor(t, "the waiting line applied", func() bool { return strings.HasPrefix(fx.lastAssistant(t, s), "Done: Dice has roll_dice") })
	fx.idle(t, s)
	if up := fx.updates(t, s); !strings.Contains(up[len(up)-1], "Line 5 of the plan, which waited for apps/dice to provide mcp, was applied by xbin now: Bind apps/assistant's mcp to apps/dice") ||
		!strings.Contains(up[len(up)-1], "Check the hook-up yourself before you say the outcome is reached") {
		t.Fatalf("the update when the waiting line applied: %s", up[len(up)-1])
	}
	if r := fx.ag.db.wsReqs(`WHERE run_id=?`, s.Run); r[0].State != "applied" || r[0].Result != "5 of 5 changes applied" {
		t.Fatalf("the plan's row once nothing waits: %+v", r[0])
	}
	if got := fx.toolResult(t, s, "tile_call"); !strings.Contains(got, "roll_dice") || fx.x.count("Call GET /api/apps/assistant/tools") != 1 {
		t.Fatalf("the check of the hook-up: %q %v", got, fx.x.calls)
	}
	// from the prompt to done: one message of the person's, no request, no question
	if msgs := fx.userMsgs(t, s); len(msgs) != 4 || fx.x.count("Ask") != 0 {
		t.Fatalf("the conversation took %d user-side messages (1 prompt and 3 updates wanted): %q", len(msgs), msgs)
	}
}

// Scenario — a person who is no admin. The line that binds the workspace's
// LLM connector needs an admin: the tool says so, and that only their
// person can ask one; they apply the rest; the update says the line waits
// and what doesn't work until then; once they asked and an admin applied
// it, admin_changes shows it.
func TestWsEvalPlanNeedsAnAdmin(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	f := fakeOf(fx.ag)
	f.on(forRun(s.Run, lastUser("rolls dice")), callTools(tc("p1", "plan_propose", wsDicePlan))).once()
	f.on(forRun(s.Run, lastTool("plan_propose")), say("One plan of five changes waits for you. Line 4 needs an admin: you can send it to one from the review.")).once()
	fx.say(t, s, "Make a tool that rolls dice and give it to a new assistant")
	fx.idle(t, s)
	got := fx.toolResult(t, s, "plan_propose")
	for _, want := range []string{"4. [binding.set] Bind apps/assistant's llm to xbin:llm — needs an admin", "Line 4 needs an admin, and your person is none", "you can't ask for them"} {
		if !strings.Contains(got, want) {
			t.Errorf("the plan's result lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "SPEND") {
		t.Errorf("a line that needs an admin carries more than its sentence: %s", got)
	}
	cs := planOf(t, fx)
	f.on(forRun(s.Run, lastUser("needs an admin was not applied")), say("Four changes are applied. The model binding waits for an admin — until then the assistant can't answer.")).once()
	fx.x.applyPlan(cs.ID, []int{1, 2, 3, 5}, 0)
	fx.notify(t)
	waitFor(t, "the update", func() bool { return strings.HasPrefix(fx.lastAssistant(t, s), "Four changes are applied") })
	fx.idle(t, s)
	if up := fx.updates(t, s); !strings.Contains(up[0], "\"Ask an admin\"") || !strings.Contains(up[0], "Until an admin applies it, what depends on it doesn't work") || !strings.Contains(up[0], "started tile agent #1") {
		t.Fatalf("the update: %s", up[0])
	}
	// the person asks; an admin applies; the conversation can read where it stands
	fx.x.askAdmin(cs.ID)
	if got := fx.tool(t, s, "admin_changes", map[string]any{}); !strings.Contains(got, "sent to the admins by your person; not applied yet") {
		t.Fatalf("while it waits for an admin: %s", got)
	}
	fx.x.adminApplies(cs.ID)
	if got := fx.tool(t, s, "admin_changes", map[string]any{}); !strings.Contains(got, "4. [binding.set] Bind apps/assistant's llm to xbin:llm — applied") {
		t.Fatalf("after the admin applied it: %s", got)
	}
	if fx.x.count("Ask") != 0 {
		t.Fatalf("a single request was filed: %v", fx.x.calls)
	}
}

// Scenario — a line refused when the plan is applied, and no falling back.
// The failure skips the lines after it and the update says so in xbind's
// words; the model then proposes a new plan for what is left — and a single
// request for a step that an open plan holds is not filed at all.
func TestWsEvalPlanLineRefusedAtApply(t *testing.T) {
	fx := newWsFix(t)
	fx.x.mu.Lock()
	fx.x.state.CanAdminChanges, fx.x.state.AdminChanges = true, true
	fx.x.mu.Unlock()
	s := fx.main(t)
	f := fakeOf(fx.ag)
	f.on(forRun(s.Run, lastUser("rolls dice")), callTools(tc("p1", "plan_propose", wsDicePlan))).once()
	f.on(forRun(s.Run, lastTool("plan_propose")), say("One plan waits for you.")).once()
	fx.say(t, s, "Make a tool that rolls dice and give it to a new assistant")
	fx.idle(t, s)
	cs := planOf(t, fx)

	// while the plan waits, its steps are not filed one by one
	const single = "That step is a line of the plan you proposed"
	if got := fx.tool(t, s, "tile_create", map[string]any{"template": "starter", "name": "Dice", "why": "the tool"}); !strings.Contains(got, single) || !strings.Contains(got, "Nothing was filed") {
		t.Fatalf("a single tile_create for a step of the open plan: %s", got)
	}
	if got := fx.tool(t, s, "tile_create", map[string]any{"template": "starter", "name": "Something else", "why": "unrelated", "dry_run": true}); strings.Contains(got, single) {
		t.Fatalf("an unrelated creation is held back by the plan: %s", got)
	}
	if fx.x.count("Ask") != 0 {
		t.Fatalf("a single request was filed beside the plan: %v", fx.x.calls)
	}

	// line 3 fails when applied: 1 and 2 stay, 4 and 5 are skipped
	replan := `{"title":"The assistant, again","changes":[
		{"kind":"tile.create","args":{"template":"agent","name":"Helper"},"why":"The assistant, at another place"},
		{"kind":"binding.set","args":{"component":"Helper","slot":"mcp","provider":"apps/dice"},"why":"The tool"}]}`
	f.on(forRun(s.Run, lastUser("A line failed")), callTools(tc("p2", "plan_propose", replan))).once()
	f.on(forRun(s.Run, lastTool("plan_propose")), say("The assistant couldn't be created where I proposed it, so the rest was skipped. Dice exists. I've proposed a new plan for the assistant.")).once()
	fx.x.applyPlan(cs.ID, []int{1, 2, 3, 4, 5}, 3)
	fx.notify(t)
	waitFor(t, "the new plan", func() bool { return strings.HasPrefix(fx.lastAssistant(t, s), "The assistant couldn't be created") })
	fx.idle(t, s)
	up := fx.updates(t, s)
	for _, want := range []string{"3. [tile.create] Create the tile apps/assistant from the template agent — yours — failed (xbin said: the tile's org turned agents away meanwhile)", "— skipped",
		"A line failed, so the lines after it were skipped and the earlier ones stay", "don't file the skipped steps as single requests"} {
		if !strings.Contains(up[0], want) {
			t.Errorf("the update lacks %q:\n%s", want, up[0])
		}
	}
	if fx.x.count("ProposePlan") != 2 || fx.x.count("Ask") != 0 {
		t.Fatalf("after a failed line: %v", fx.x.calls)
	}
	// the plan's card keeps each line's outcome (xbin's review closes itself when nothing failed; a
	// failure stays here with xbind's reason): xbind's sentences, never the agent's reasons
	var cards struct {
		Requests []struct {
			Ref   string       `json:"ref"`
			Plan  bool         `json:"plan"`
			State string       `json:"state"`
			Lines []wsPlanLine `json:"lines"`
		} `json:"requests"`
	}
	fx.json(t, fx.call(t, wsAlice, "GET", fmt.Sprintf("/ws/sessions/%d/cards", s.Run), nil), 200, &cards)
	seen := false
	for _, q := range cards.Requests {
		switch {
		case q.Ref == cs.ID:
			seen = true
			got := ""
			for _, l := range q.Lines {
				got += fmt.Sprintf("%d:%s ", l.N, l.Result)
			}
			if !q.Plan || got != "1:applied 2:applied 3:failed 4:skipped 5:skipped " || q.Lines[2].Error != "the tile's org turned agents away meanwhile" ||
				q.Lines[0].Line != "Create the tile apps/dice from the template starter — yours" {
				t.Errorf("the decided plan's card: plan %v, lines %q, %+v", q.Plan, got, q.Lines)
			}
		case q.State == "proposed" && (!q.Plan || len(q.Lines) != 0):
			t.Errorf("a plan that still waits: %+v", q)
		}
	}
	if !seen {
		t.Errorf("the decided plan is not among the cards: %+v", cards.Requests)
	}
	// the new plan names what the first one made, and creates nothing twice
	next := planOf(t, fx)
	if len(next.Items) != 2 || next.Items[1].Line != "Bind apps/helper's mcp to apps/dice" || len(next.Items[1].Needs) != 1 {
		t.Fatalf("the new plan: %+v", next.Items)
	}
	if rows := fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run); len(rows) != 1 || rows[0].Tile != "apps/dice" {
		t.Fatalf("the tile agent line 2 started is the conversation's to follow, and no other: %+v", rows)
	}
}

// What plan_propose refuses before xbind is asked, and what its dry run says.
func TestWsPlanToolRules(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	for want, args := range map[string]map[string]any{
		"title: what the plan is for":                  {"changes": []any{map[string]any{"kind": "tile.create", "args": map[string]any{}, "why": "x"}}},
		`no kind "users.delete"`:                       {"title": "t", "changes": []any{map[string]any{"kind": "users.delete", "args": map[string]any{}, "why": "x"}}},
		`has no "why": add the reason for this change`: {"title": "t", "changes": []any{map[string]any{"kind": "tile.create", "args": map[string]any{"template": "agent", "name": "A"}}}},
		`there is no template "nope"`:                  {"title": "t", "changes": []any{map[string]any{"kind": "tile.create", "args": map[string]any{"template": "nope", "name": "A"}, "why": "x"}}},
		"name — what the new tile":                     {"title": "t", "changes": []any{map[string]any{"kind": "tile.create", "args": map[string]any{"template": "agent"}, "why": "x"}}},
		"done_when: what is true":                      {"title": "t", "changes": []any{map[string]any{"kind": "agent.start", "args": map[string]any{"tile": "apps/orders", "goal": "g"}, "why": "x"}}},
		"an earlier tile.create line":                  {"title": "t", "changes": []any{map[string]any{"kind": "agent.start", "args": map[string]any{"tile": "apps/nope", "goal": "g", "done_when": "d"}, "why": "x"}}},
		"This would propose 1 change(s)":               {"title": "t", "dry_run": true, "changes": []any{map[string]any{"kind": "agent.start", "args": map[string]any{"tile": "Orders", "goal": "g", "done_when": "d"}, "why": "x"}}},
	} {
		if got := fx.tool(t, s, "plan_propose", args); !strings.Contains(got, want) {
			t.Errorf("want %q, got %q", want, got)
		}
	}
	if fx.x.count("ProposePlan") != 0 {
		t.Fatalf("something was proposed: %v", fx.x.calls)
	}
}

// A binding that takes an admin's rights can't wait for a tile to be built:
// xbind says so on the line, and the tool tells the model what to do with
// it — leave it out, and propose it alone once the tile agent has reported.
func TestWsPlanLineThatMayNotWait(t *testing.T) {
	fx := newWsFix(t)
	fx.x.mu.Lock()
	fx.x.state.CanAdminChanges = true
	fx.x.mu.Unlock()
	s := fx.main(t)
	got := fx.tool(t, s, "plan_propose", map[string]any{"title": "A model for the dice tile", "changes": []any{
		map[string]any{"kind": "tile.create", "args": map[string]any{"template": "starter", "name": "Dice"}, "why": "the tile"},
		map[string]any{"kind": "agent.start", "args": map[string]any{"tile": "Dice", "goal": "g", "done_when": "d"}, "why": "build it"},
		// (the fake's admin line: a binding to the connector, written as one that would wait)
		map[string]any{"kind": "binding.set", "args": map[string]any{"component": "apps/orders", "slot": "llm", "provider": "Dice"}, "why": "x"},
	}})
	_ = got
	cs := planOf(t, fx)
	if len(cs.Items) != 3 {
		t.Fatalf("the plan: %+v", cs.Items)
	}
	// (the fake marks a provider xbin:llm as an admin's; the words of the tool are what this test holds)
	it := cs.Items[2]
	it.Later = "apps/dice provides no mcp yet, and this binding takes an admin's rights"
	notes := wsPlanNotes(wsChangeSet{Items: []xbin.ChangeSetItem{it}})
	for _, want := range []string{"Line 3 can't be part of this plan — xbin says: apps/dice provides no mcp yet", "propose that one line as a plan of its own"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the tool's note lacks %q: %s", want, notes)
		}
	}
}

// The real-model run (2026-11-17) — a dry run of a plan says what xbin
// would say. A model that looks before it files (the prompt tells it to
// when unsure) got "this would propose 5 change(s)" for a plan two of
// whose lines xbin could not apply, filed it, and left its person a review
// with dead lines — then proposed a second plan beside it. The dry run now
// asks xbind to describe the plan without proposing it: the lines as its
// person would see them, the ones that can't be applied named, nothing
// kept and nobody told.
func TestWsEvalPlanDryRunDescribes(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	plan := func(name string, dry bool) string {
		return fx.tool(t, s, "plan_propose", map[string]any{"title": "A place for notes", "dry_run": dry, "changes": []any{
			map[string]any{"kind": "tile.create", "args": map[string]any{"template": "starter", "name": name}, "why": "somewhere to keep them"},
			map[string]any{"kind": "agent.start", "args": map[string]any{"tile": name, "goal": "a notes page", "done_when": "the page lists notes"}, "why": "its tile agent builds the page"}}})
	}
	got := plan("Orders", true) // a tile is there already
	for _, want := range []string{"As xbin would describe it", "(not proposed)", "a dry run", "1. [tile.create] Create the tile apps/orders", "can't be applied",
		"xbin can't apply line 1 as proposed", "propose a plan without it", "This would propose 2 change(s)", "Nothing was proposed."} {
		if !strings.Contains(got, want) {
			t.Errorf("the dry run of a plan with a dead line lacks %q:\n%s", want, got)
		}
	}
	if fx.x.count("DescribePlan") != 1 || fx.x.count("ProposePlan") != 0 || len(fx.x.sets) != 0 {
		t.Fatalf("a dry run proposed something: xbind saw %v, %d set(s)", fx.x.calls, len(fx.x.sets))
	}
	// mended, the dry run has no dead line — and the plan is then proposed once
	if got := plan("Notes", true); strings.Contains(got, "can't be applied") || !strings.Contains(got, "1. [tile.create] Create the tile apps/notes") {
		t.Fatalf("the dry run of the mended plan: %s", got)
	}
	if got := plan("Notes", false); !strings.Contains(got, "Proposed as one plan") || fx.x.count("ProposePlan") != 1 || len(fx.x.sets) != 1 {
		t.Fatalf("proposing it: %s (%v)", got, fx.x.calls)
	}
}
