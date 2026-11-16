package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Scenario — a tool for an assistant (the journey's dice tool, docs/mcp.md):
// the person asks for a tool an agent instance should get; the model names
// the brief template "mcp-tool" with only what it knows — the tool's name,
// arguments and result — and the brief the person sees, and that xbind
// tells the tile agent first, carries the template's done-when, check-by
// and leave-alone: tools/list and tools/call from the tile's own sandbox,
// the manifest's provides untouched. What a real model does with the same
// prompt is the manual run's question.
func TestWsEvalMCPToolBrief(t *testing.T) {
	fx := newWsFix(t)
	fx.x.addTile(wsTile{Path: "apps/dice", Title: "Dice", Kind: "go", Status: "running", Agent: "full", Level: "terminal"})
	s := fx.main(t)
	f := fakeOf(fx.ag)
	var offered []toolSpec
	f.on(forRun(s.Run, lastUser("dice tool")), func(r LLMRequest) (LLMReply, error) {
		offered = r.Tools
		return callTools(tc("c1", "tile_agent_brief", `{"tile":"apps/dice","template":"mcp-tool",
			"tool":{"name":"roll_dice","arguments":"sides: integer, default 6; count: integer, default 1","result":"each roll and their sum"}}`),
			tc("c2", "tile_agent_start", `{"brief":1}`))(r)
	}).once()
	f.on(forRun(s.Run, lastTool("tile_agent_start")), say("I've written the brief for Dice's own agent: it starts once you allow it.")).once()

	fx.say(t, s, "Make a dice tool and give it to the assistant")
	fx.idle(t, s)

	// what the model was offered: the template, by its id, and the tool's three fields
	var spec map[string]any
	for _, o := range offered {
		if o.Function.Name == "tile_agent_brief" {
			spec = o.Function.Parameters
		}
	}
	props, _ := spec["properties"].(map[string]any)
	tmpl, _ := props["template"].(map[string]any)
	if tmpl == nil || !strings.Contains(asString(tmpl["description"]), "MCP server in a tile of its own") || props["tool"] == nil {
		t.Fatalf("tile_agent_brief as offered: %v", spec)
	}
	if got := fx.toolResult(t, s, "tile_agent_brief"); !strings.HasPrefix(got, "Brief 1 for Dice is in front of your person") {
		t.Fatalf("the brief's result: %q", got)
	}
	var b struct{ goal, done, leave, check, known string }
	if err := fx.ag.db.q.QueryRow(`SELECT goal, done_when, leave_alone, check_by, known FROM ws_briefs WHERE run_id=? AND id=1`, s.Run).
		Scan(&b.goal, &b.done, &b.leave, &b.check, &b.known); err != nil {
		t.Fatal(err)
	}
	for what, want := range map[string][]string{
		"goal":        {b.goal, "an MCP server with a tool `roll_dice`", "sides: integer, default 6", "each roll and their sum"},
		"done when":   {b.done, `"provides": {"mcp": {"kind": "http", "service": "mcp", "role": "reader"}}`, `"runtime": "go"`, "tools/list", "names `roll_dice`", "tools/call of `roll_dice`"},
		"check by":    {b.check, "$XBIN_URL/api/$XBIN_COMPONENT/mcp", `"method":"tools/list"`, `"name":"roll_dice"`},
		"leave alone": {b.leave, "everything outside this tile", `"reader"`, "a wider role is not bound"},
		"known":       {b.known, "The tile may be empty", `"$XBIN_URL/docs/mcp.md?raw=1"`, "go.mod", "backend/main.go"},
	} {
		for _, w := range want[1:] {
			if !strings.Contains(want[0], w) {
				t.Errorf("the brief's %s lacks %q: %q", what, w, want[0])
			}
		}
	}
	// … and it is what the start asks xbind for: the brief as the tile agent is told it first
	req := fx.x.pending(t)
	var asked wsAgentStart
	_ = json.Unmarshal(req.call.Body, &asked)
	if req.Kind != "session" || asked.Tile != "apps/dice" || !strings.Contains(asked.Prompt, "Done when: this tile's xbin.json declares \"provides\"") || !strings.Contains(asked.Prompt, "tools/list on this tile's /mcp names `roll_dice`") ||
		!strings.Contains(asked.Prompt, "Leave alone: everything outside this tile") || !strings.Contains(asked.Prompt, "Check by: the two calls from this tile's own sandbox") {
		t.Fatalf("the start asked for:\n%s", asked.Prompt)
	}
}

// The template's rules: it needs the tool; a field the model gave is kept;
// leave_alone and known always carry the kind's lines; no template, no tool.
func TestWsBriefTemplate(t *testing.T) {
	f := wsBriefFields{DoneWhen: "a d20 rolls 1 to 20", LeaveAlone: "the page"}
	if err := wsApplyBriefTemplate("mcp-tool", &wsBriefToolAsk{Name: "roll_dice", Result: "the rolls"}, &f); err != nil {
		t.Fatal(err)
	}
	if f.DoneWhen != "a d20 rolls 1 to 20" || !strings.HasSuffix(f.LeaveAlone, "; the page") || !strings.Contains(f.Goal, "Arguments: none") || f.CheckBy == "" {
		t.Fatalf("filled: %+v", f)
	}
	for _, c := range []struct {
		id   string
		tool *wsBriefToolAsk
	}{{"mcp-tool", nil}, {"mcp-tool", &wsBriefToolAsk{Name: "roll dice!", Result: "x"}}, {"mcp-tool", &wsBriefToolAsk{Name: "x"}},
		{"other", &wsBriefToolAsk{Name: "x", Result: "y"}}, {"", &wsBriefToolAsk{Name: "x", Result: "y"}}} {
		if err := wsApplyBriefTemplate(c.id, c.tool, &wsBriefFields{}); err == nil {
			t.Errorf("%q %+v: no error", c.id, c.tool)
		}
	}
	if err := wsApplyBriefTemplate("", nil, &wsBriefFields{Goal: "g"}); err != nil {
		t.Fatalf("no template: %v", err)
	}
}
