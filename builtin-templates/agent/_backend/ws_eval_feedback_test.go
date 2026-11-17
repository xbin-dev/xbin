package main

import (
	"fmt"
	"strings"
	"testing"
)

// What the feedback run (2026-11-17) found plainly unclear in what the
// tools answer — each from a real model's own account, checked against
// its transcript — and what they answer now.
func TestWsEvalFeedbackWordings(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)

	// A plan with a tile agent's start. Two conversations read Codex's mode "read-only" in the start line as
	// "it can't write" and warned their person; one approved a brief the template wrote and it never saw.
	got := fx.tool(t, s, "plan_propose", map[string]any{"title": "A dice tool", "dry_run": true, "changes": []any{
		map[string]any{"kind": "tile.create", "args": map[string]any{"template": "starter", "name": "Dice"}, "why": "the tool's tile"},
		map[string]any{"kind": "agent.start", "args": map[string]any{"tile": "Dice", "template": "mcp-tool",
			"tool": map[string]any{"name": "roll_dice", "arguments": "sides: integer", "result": "each roll"}}, "why": "a coding agent builds it"}}})
	// (and the brief itself: the coding agent that built from it said "three files" was wrong — a tile that
	// exposes a role ships an API.md — and that it found the build's errors and bx's naming by trial)
	for _, want := range []string{"Line 2's brief, as the template wrote it", "roll_dice", "Done when:", "an API.md saying what the endpoint is", "bx logs $XBIN_COMPONENT",
		"The mode named in a start line is the coding agent's own word for how it runs, not a limit on what it may change"} {
		if !strings.Contains(got, want) {
			t.Errorf("the plan's dry run lacks %q:\n%s", want, got)
		}
	}
	// a brief it wrote itself is not repeated to it; a plan with no start says nothing of modes
	got = fx.tool(t, s, "plan_propose", map[string]any{"title": "Notes", "dry_run": true, "changes": []any{
		map[string]any{"kind": "tile.create", "args": map[string]any{"template": "starter", "name": "Notes"}, "why": "a place for notes"}}})
	if strings.Contains(got, "brief, as the template wrote it") || strings.Contains(got, "The mode named in a start line") {
		t.Errorf("a plan with no start: %s", got)
	}
	// a line with no why: the error says what is missing, not what a why is
	got = fx.tool(t, s, "plan_propose", map[string]any{"title": "Notes", "changes": []any{
		map[string]any{"kind": "tile.create", "args": map[string]any{"template": "starter", "name": "Notes"}}}})
	if !strings.Contains(got, `line 1 has no "why"`) {
		t.Errorf("a plan line with no why: %s", got)
	}

	// The notebook: its list (in the prompt) shows a page the moment it is written, and two conversations
	// then believed the page had been there and that their write had replaced it.
	got = fx.tool(t, s, "notebook_write", map[string]any{"slug": "prices", "title": "Prices", "summary": "the supplier's prices", "body": "Flour 18.40"})
	if !strings.Contains(got, "as a new page (nothing was replaced)") {
		t.Errorf("a first write: %s", got)
	}
	_ = fx.tool(t, s, "notebook_read", map[string]any{"slug": "prices"})
	got = fx.tool(t, s, "notebook_write", map[string]any{"slug": "prices", "title": "Prices", "summary": "the supplier's prices", "body": "Flour 18.90"})
	if !strings.Contains(got, "now version 2 (version 1 is kept in its history)") {
		t.Errorf("a second write: %s", got)
	}

	// A tool result hidden to save context: the stub's first and last words were taken for a summary.
	stub := maskStub(&Message{Name: "research", Seq: 15, Content: strings.Repeat("the report says a great deal. ", 80)}, 0)
	for _, want := range []string{"only its first and last words, not a summary", `message_get {"seq": 15} shows it whole — read it again before you rely on it`, "begins: ", "ends: "} {
		if !strings.Contains(stub, want) {
			t.Errorf("the stub lacks %q:\n%s", want, stub)
		}
	}

	// What the session can't do is said before the work, not found at its last step: with MCP tools off no
	// tool call can be made (an MCP tile's own /mcp through tile_call neither), and an xbind that takes no
	// previews says so once it is known — one conversation spent a call to learn each.
	prompt := func() string {
		r, _ := fx.ag.db.getRun(s.Run)
		cfg, _ := fx.ag.db.runConfig(s.Run)
		return fx.ag.wsPromptFor(r, cfg)
	}
	if p := prompt(); !strings.Contains(p, "an MCP tile's own tools/call through tile_call neither") || !strings.Contains(p, "that it answers is then the tile agent's word") || strings.Contains(p, "Seeing a page (preview)") {
		t.Errorf("the session block, MCP off, previews not yet tried:\n%s", p)
	}
	fx.x.mu.Lock()
	fx.x.noPreview = true
	fx.x.mu.Unlock()
	if got := fx.tool(t, s, "preview", map[string]any{"tile": "apps/orders"}); !strings.Contains(got, "Previews aren't available on this xbin") {
		t.Fatalf("a preview where there are none: %s", got)
	}
	if p := prompt(); !strings.Contains(p, "Seeing a page (preview): not available on this xbin — don't call preview") {
		t.Errorf("the session block after a preview was refused:\n%s", p)
	}

	// the notebook's list marks what this conversation wrote
	if p := prompt(); !strings.Contains(p, "- prices — Prices") || !strings.Contains(p, "written in THIS conversation, not before it)") {
		t.Errorf("the notebook's list after this conversation's write:\n%s", p)
	}

	// A tile agent's result leads with its edits: a real run listed 407 files of a build cache the coding agent
	// made inside the tile, and none of the three it had edited.
	row, sid := fx.seedAgent(t, s, "apps/orders")
	files := []wsFileStat{{Path: "index.html", Add: 12, Del: 1}, {Path: "backend/main.go", Add: 30, Del: 2}}
	for i := range 60 {
		files = append(files, wsFileStat{Path: fmt.Sprintf(".verify/gocache/%02d/obj-d", i)})
	}
	fx.x.mu.Lock()
	fx.x.reports[sid] = wsAgentReport{Status: "end_turn", Answer: "done", Turn: 1, Files: files, Verified: []string{"go vet ./... → exit 0"}}
	fx.x.mu.Unlock()
	fx.x.setAgent(sid, func(a *wsAgent) { a.Status = "idle" })
	got = fx.tool(t, s, "tile_agent_result", map[string]any{"agent": fmt.Sprintf("#%d", row.ID)})
	if !strings.Contains(got, "Files changed (2):\n- index.html (+12 −1)\n- backend/main.go (+30 −2)\n(and 60 file(s) with no line of text changed") || strings.Contains(got, "obj-d (+0") || !strings.Contains(got, "go vet ./... → exit 0") {
		t.Errorf("a result with build output among its files:\n%s", got)
	}

	// A read of a tile whose backend crashes on it: xbin's 502 comes with the log's last lines.
	fx.x.api["GET /api/apps/orders/items"] = func([]byte) (int, string) { return 502, `{"docs":"/docs/protocol.md","error":"backend error: EOF"}` }
	fx.x.mu.Lock()
	fx.x.logs["apps/orders"] = "panic: assignment to entry in nil map\n\tmain.go:26 +0x1c4"
	fx.x.mu.Unlock()
	got = fx.tool(t, s, "tile_call", map[string]any{"tile": "apps/orders", "method": "GET", "path": "/items"})
	for _, want := range []string{"GET /items → 502", "That is xbin's answer, not the tile's", "panic: assignment to entry in nil map", "main.go:26"} {
		if !strings.Contains(got, want) {
			t.Errorf("a read the backend crashed on lacks %q:\n%s", want, got)
		}
	}
	// where the person may not read the tile's log, the answer still says whose 502 it is, and where to look
	fx.x.api["GET /api/apps/invoices/items"] = func([]byte) (int, string) { return 502, `{"error":"backend error: EOF"}` }
	if got := fx.tool(t, s, "tile_call", map[string]any{"tile": "apps/invoices", "method": "GET", "path": "/items"}); !strings.Contains(got, "That is xbin's answer, not the tile's") || !strings.Contains(got, "tile_logs may say why") {
		t.Errorf("a crashed read where the log can't be read: %s", got)
	}
	// a tile's own 404 or 500 is the tile's answer: no log with it
	fx.x.api["GET /api/apps/invoices/nope"] = func([]byte) (int, string) { return 404, "no such route" }
	if got := fx.tool(t, s, "tile_call", map[string]any{"tile": "apps/invoices", "method": "GET", "path": "/nope"}); strings.Contains(got, "xbin's answer, not the tile's") {
		t.Errorf("a tile's own 404: %s", got)
	}

	// "idle" in a tile's status: neither "healthy" nor "down"
	fx.x.mu.Lock()
	fx.x.tiles["apps/orders"].Status = "idle"
	fx.x.mu.Unlock()
	if got := fx.tool(t, s, "tile_status", map[string]any{"tile": "apps/orders"}); !strings.Contains(got, `"idle" says only that its backend isn't running now`) || !strings.Contains(got, "nothing here shows that it works") {
		t.Errorf("an idle tile's status: %s", got)
	}

	// a tile with an agent session: open, not necessarily working
	fx.x.mu.Lock()
	fx.x.tiles["apps/orders"].Running = true
	fx.x.mu.Unlock()
	if got := fx.tool(t, s, "tile_status", map[string]any{"tile": "apps/orders"}); !strings.Contains(got, "a tile agent's session is open here — working or at rest") {
		t.Errorf("a tile with an agent session: %s", got)
	}
}
