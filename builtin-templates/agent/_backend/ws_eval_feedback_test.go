package main

import (
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
	for _, want := range []string{"Line 2's brief, as the template wrote it", "roll_dice", "Done when:",
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
}
