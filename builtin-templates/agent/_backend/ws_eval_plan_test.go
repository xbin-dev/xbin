package main

import (
	"fmt"
	"strings"
	"testing"
)

// the plan the prompt and the tool ask for (D238):
// a tool tile, an assistant, its two slots, and the tile agent that builds
// the tool — one call, five lines.
const wsDicePlan = `{"title":"A dice tool and an assistant that can use it","changes":[
	{"kind":"tile.create","args":{"template":"mcp-server","name":"Dice"},"why":"The tool lives in a tile of its own"},
	{"kind":"tile.create","args":{"template":"agent","name":"Assistant"},"why":"The assistant you will chat with"},
	{"kind":"binding.set","args":{"component":"Assistant","slot":"llm","provider":"xbin:llm"},"why":"The assistant needs a model to answer at all"},
	{"kind":"binding.set","args":{"component":"apps/assistant","slot":"mcp","provider":"Dice"},"why":"This is what gives the assistant the tool"},
	{"kind":"agent.start","args":{"tile":"Dice","template":"mcp-tool","tool":{"name":"roll_dice","arguments":"sides: integer, default 6","result":"each roll and their sum"}},"why":"A coding agent builds the tool"}]}`

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
	if !strings.Contains(wsSystem(), "propose them as ONE plan (plan_propose), never as a chain of single requests") {
		t.Error("the prompt doesn't teach the plan")
	}
	got := fx.toolResult(t, s, "plan_propose")
	for _, want := range []string{
		"1. [tile.create] Create the tile apps/dice from the template mcp-server — yours",
		"3. [binding.set] Bind apps/assistant's llm to xbin:llm — WARNING: SPEND:",
		"4. [binding.set] Bind apps/assistant's mcp to apps/dice",
		"5. [agent.start] Start a tile agent in apps/dice",
		"Line 3 takes an admin's rights",
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
	if brief := str(fx.x.planArgs[cs.ID+"/5"]["brief"]); !strings.Contains(brief, "Done when: tools/list on this tile's /mcp names `roll_dice`") || !strings.Contains(brief, "handed to you by alice's workspace agent") {
		t.Fatalf("the start line's brief:\n%s", brief)
	}
	if n := cs.Items[3].Needs; len(n) != 2 {
		t.Fatalf("line 4 needs %v", n)
	}

	// the person ticks the SPEND line and applies all five
	f.on(forRun(s.Run, lastUser("Line 5 started tile agent #1")), say("Applied. The tile agent is building the tool; I'll check when it reports.")).once()
	fx.x.applyPlan(cs.ID, []int{1, 2, 3, 4, 5}, 0)
	fx.notify(t)
	waitFor(t, "the decision's update", func() bool { return strings.HasPrefix(fx.lastAssistant(t, s), "Applied.") })
	fx.idle(t, s)
	up := fx.updates(t, s)
	for _, want := range []string{"your person decided", "Line 5 started tile agent #1 in apps/dice (Claude Code, asks before acting)", "The work isn't done until the tile agent reports and you have checked"} {
		if !strings.Contains(up[0], want) {
			t.Errorf("the update lacks %q:\n%s", want, up[0])
		}
	}
	rows := fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)
	if len(rows) != 1 || rows[0].Tile != "apps/dice" || rows[0].CallID != "p1" {
		t.Fatalf("the tile agent the plan started isn't this conversation's to follow: %+v", rows)
	}
	if r := fx.ag.db.wsReqs(`WHERE run_id=?`, s.Run); len(r) != 1 || r[0].State != "applied" || !strings.Contains(r[0].Title, "a plan of 5 change(s)") {
		t.Fatalf("the plan's row: %+v", r)
	}

	// the tile agent reports; the model reads the report and checks the hook-up itself
	fx.x.mu.Lock()
	fx.x.reports[rows[0].Session] = wsAgentReport{Status: "end_turn", Answer: "Added roll_dice; tools/list names it.", Turn: 1}
	fx.x.mu.Unlock()
	f.on(forRun(s.Run, lastUser("ended its turn")), callTools(tc("v1", "tile_agent_result", `{"agent":"#1"}`),
		tc("v2", "tile_call", `{"tile":"apps/assistant","method":"GET","path":"/tools"}`))).once()
	f.on(forRun(s.Run, lastTool("tile_call")), say("Done: Dice has roll_dice and your assistant lists it. Checked: the tile agent's report and the assistant's tool list. Not checked: a conversation in the assistant.")).once()
	fx.x.setAgent(rows[0].Session, func(a *wsAgent) { a.Status, a.Last = "idle", "Added roll_dice." })
	fx.notify(t)
	waitFor(t, "the report", func() bool { return strings.HasPrefix(fx.lastAssistant(t, s), "Done: Dice has roll_dice") })
	fx.idle(t, s)
	if got := fx.toolResult(t, s, "tile_call"); !strings.Contains(got, "roll_dice") || fx.x.count("Call GET /api/apps/assistant/tools") != 1 {
		t.Fatalf("the check of the hook-up: %q %v", got, fx.x.calls)
	}
	// from the prompt to done: one message of the person's, no request, no question
	if msgs := fx.userMsgs(t, s); len(msgs) != 3 || fx.x.count("Ask") != 0 {
		t.Fatalf("the conversation took %d user-side messages (1 prompt and 2 updates wanted): %q", len(msgs), msgs)
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
	f.on(forRun(s.Run, lastTool("plan_propose")), say("One plan of five changes waits for you. Line 3 needs an admin: you can send it to one from the review.")).once()
	fx.say(t, s, "Make a tool that rolls dice and give it to a new assistant")
	fx.idle(t, s)
	got := fx.toolResult(t, s, "plan_propose")
	for _, want := range []string{"3. [binding.set] Bind apps/assistant's llm to xbin:llm — needs an admin", "Line 3 needs an admin, and your person is none", "you can't ask for them"} {
		if !strings.Contains(got, want) {
			t.Errorf("the plan's result lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "SPEND") {
		t.Errorf("a line that needs an admin carries more than its sentence: %s", got)
	}
	cs := planOf(t, fx)
	f.on(forRun(s.Run, lastUser("needs an admin was not applied")), say("Four changes are applied. The model binding waits for an admin — until then the assistant can't answer.")).once()
	fx.x.applyPlan(cs.ID, []int{1, 2, 4, 5}, 0)
	fx.notify(t)
	waitFor(t, "the update", func() bool { return strings.HasPrefix(fx.lastAssistant(t, s), "Four changes are applied") })
	fx.idle(t, s)
	if up := fx.updates(t, s); !strings.Contains(up[0], "\"Ask an admin\"") || !strings.Contains(up[0], "Until an admin applies it, what depends on it doesn't work") || !strings.Contains(up[0], "4 of 4 changes applied") && !strings.Contains(up[0], "started tile agent #1") {
		t.Fatalf("the update: %s", up[0])
	}
	// the person asks; an admin applies; the conversation can read where it stands
	fx.x.askAdmin(cs.ID)
	if got := fx.tool(t, s, "admin_changes", map[string]any{}); !strings.Contains(got, "sent to the admins by your person; not applied yet") {
		t.Fatalf("while it waits for an admin: %s", got)
	}
	fx.x.adminApplies(cs.ID)
	if got := fx.tool(t, s, "admin_changes", map[string]any{}); !strings.Contains(got, "3. [binding.set] Bind apps/assistant's llm to xbin:llm — applied") {
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
	if got := fx.tool(t, s, "tile_create", map[string]any{"template": "mcp-server", "name": "Dice", "why": "the tool"}); !strings.Contains(got, single) || !strings.Contains(got, "Nothing was filed") {
		t.Fatalf("a single tile_create for a step of the open plan: %s", got)
	}
	if got := fx.tool(t, s, "tile_create", map[string]any{"template": "mcp-server", "name": "Something else", "why": "unrelated", "dry_run": true}); strings.Contains(got, single) {
		t.Fatalf("an unrelated creation is held back by the plan: %s", got)
	}
	if fx.x.count("Ask") != 0 {
		t.Fatalf("a single request was filed beside the plan: %v", fx.x.calls)
	}

	// line 2 fails when applied: 1 stays, 3–5 are skipped
	replan := `{"title":"The assistant, again","changes":[
		{"kind":"tile.create","args":{"template":"agent","name":"Helper"},"why":"The assistant, at another place"},
		{"kind":"binding.set","args":{"component":"Helper","slot":"mcp","provider":"apps/dice"},"why":"The tool"}]}`
	f.on(forRun(s.Run, lastUser("A line failed")), callTools(tc("p2", "plan_propose", replan))).once()
	f.on(forRun(s.Run, lastTool("plan_propose")), say("The assistant couldn't be created where I proposed it, so the rest was skipped. Dice exists. I've proposed a new plan for the assistant.")).once()
	fx.x.applyPlan(cs.ID, []int{1, 2, 3, 4, 5}, 2)
	fx.notify(t)
	waitFor(t, "the new plan", func() bool { return strings.HasPrefix(fx.lastAssistant(t, s), "The assistant couldn't be created") })
	fx.idle(t, s)
	up := fx.updates(t, s)
	for _, want := range []string{"2. [tile.create] Create the tile apps/assistant from the template agent — yours — failed (xbin said: the tile's org turned agents away meanwhile)", "— skipped",
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
			if !q.Plan || got != "1:applied 2:failed 3:skipped 4:skipped 5:skipped " || q.Lines[1].Error != "the tile's org turned agents away meanwhile" ||
				q.Lines[0].Line != "Create the tile apps/dice from the template mcp-server — yours" {
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
	if rows := fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run); len(rows) != 0 {
		t.Fatalf("a skipped start left a tile agent: %+v", rows)
	}
}

// What plan_propose refuses before xbind is asked, and what its dry run says.
func TestWsPlanToolRules(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	for want, args := range map[string]map[string]any{
		"title: what the plan is for":    {"changes": []any{map[string]any{"kind": "tile.create", "args": map[string]any{}, "why": "x"}}},
		`no kind "users.delete"`:         {"title": "t", "changes": []any{map[string]any{"kind": "users.delete", "args": map[string]any{}, "why": "x"}}},
		"why — the reason for it":        {"title": "t", "changes": []any{map[string]any{"kind": "tile.create", "args": map[string]any{"template": "agent", "name": "A"}}}},
		`there is no template "nope"`:    {"title": "t", "changes": []any{map[string]any{"kind": "tile.create", "args": map[string]any{"template": "nope", "name": "A"}, "why": "x"}}},
		"name — what the new tile":       {"title": "t", "changes": []any{map[string]any{"kind": "tile.create", "args": map[string]any{"template": "agent"}, "why": "x"}}},
		"done_when: what is true":        {"title": "t", "changes": []any{map[string]any{"kind": "agent.start", "args": map[string]any{"tile": "apps/orders", "goal": "g"}, "why": "x"}}},
		"an earlier tile.create line":    {"title": "t", "changes": []any{map[string]any{"kind": "agent.start", "args": map[string]any{"tile": "apps/nope", "goal": "g", "done_when": "d"}, "why": "x"}}},
		"This would propose 1 change(s)": {"title": "t", "dry_run": true, "changes": []any{map[string]any{"kind": "agent.start", "args": map[string]any{"tile": "Orders", "goal": "g", "done_when": "d"}, "why": "x"}}},
	} {
		if got := fx.tool(t, s, "plan_propose", args); !strings.Contains(got, want) {
			t.Errorf("want %q, got %q", want, got)
		}
	}
	if fx.x.count("ProposePlan") != 0 {
		t.Fatalf("something was proposed: %v", fx.x.calls)
	}
}
