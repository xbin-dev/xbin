package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseAgentRun(t *testing.T) {
	t.Setenv("XBIN_COMPONENT", "apps/here")
	t.Setenv("XBIN_AGENT_PROVIDER", "")
	o, err := parseAgentRun([]string{"--mode", "plan", "list", "the", "files"})
	if err != nil || o.tile != "apps/here" || o.provider != "claude" || o.mode != "plan" || strings.Join(o.rest, " ") != "list the files" {
		t.Fatalf("%+v %v", o, err)
	}
	o, err = parseAgentRun([]string{"--tile", "apps/x", "-p", "opencode", "--net", "none", "--name", "n", "hi"})
	if err != nil || o.tile != "apps/x" || o.provider != "opencode" || o.net != "none" || o.name != "n" {
		t.Fatalf("%+v %v", o, err)
	}
	o, err = parseAgentRun([]string{"--model", "sonnet", "--option", "effort=high", "-o", "fast=on", "go"})
	if err != nil || o.options["model"] != "sonnet" || o.options["effort"] != "high" || o.options["fast"] != "on" {
		t.Fatalf("options: %+v %v", o.options, err)
	}
	if _, err := parseAgentRun([]string{"--option", "novalue", "go"}); err == nil {
		t.Fatal("--option without = accepted")
	}
	if got := optionsLine([]any{map[string]any{"id": "mode", "currentValue": "ask"}, map[string]any{"id": "model", "currentValue": "sonnet"}, map[string]any{"id": "effort", "currentValue": "high"}}); got != " · model sonnet · effort high" {
		t.Fatalf("optionsLine: %q", got)
	}
	if _, err := parseAgentRun([]string{"--bogus", "x"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if _, err := parseAgentRun([]string{"--tile"}); err == nil {
		t.Fatal("dangling flag accepted")
	}
	t.Setenv("XBIN_COMPONENT", "")
	if _, err := parseAgentRun([]string{"hi"}); err == nil || !strings.Contains(err.Error(), "--tile") {
		t.Fatalf("no tile: %v", err)
	}
}

func ev(typ string, data any) agentEvent {
	b, _ := json.Marshal(data)
	return agentEvent{Seq: 1, Type: typ, Data: b}
}

func TestAgentRenderer(t *testing.T) {
	var out bytes.Buffer
	r := newAgentRenderer(&out, "s1")
	r.render(ev("message.delta", map[string]any{"role": "user", "text": "list files"}), true)
	r.render(ev("status", map[string]any{"status": "running"}), true)
	r.render(ev("message.delta", map[string]any{"role": "agent", "text": "Sure, "}), true)
	r.render(ev("message.delta", map[string]any{"role": "agent", "text": "listing."}), true)
	r.render(ev("tool.call", map[string]any{"id": "t1", "title": "run ls", "kind": "execute"}), true)
	r.render(ev("permission.request", map[string]any{"pid": "p1", "toolCall": map[string]any{"title": "run ls"},
		"options": []map[string]any{{"optionId": "once", "name": "Allow once", "kind": "allow_once"}}}), true)
	if r.lastPending() != "p1" {
		t.Fatal("pending not tracked")
	}
	r.render(ev("permission.resolved", map[string]any{"pid": "p1", "optionId": "once", "by": "user:dev1"}), true)
	if r.lastPending() != "" {
		t.Fatal("resolution not tracked")
	}
	r.render(ev("tool.update", map[string]any{"id": "t1", "status": "completed",
		"content": []map[string]any{{"type": "diff", "path": "a.go", "oldText": "x\n", "newText": "y\nz\n"}}}), true)
	code, done := r.render(ev("turn.end", map[string]any{"turn": 1, "stopReason": "end_turn", "usage": map[string]any{"used": 12, "size": 200}}), true)
	if !done || code != 0 {
		t.Fatalf("turn.end: %d %v", code, done)
	}
	s := out.String()
	for _, want := range []string{"> list files\n", "Sure, listing.\n", "⚙ t1 run ls [execute]", "⚠ permission p1: run ls",
		"bx agent permit s1 p1 once|always|deny", "→ p1: once by user:dev1", "t1 completed  a.go +2/-1", "— turn 1 ended (end_turn)  usage 12/200"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	// exit codes: cancelled 130, error 3; an error status ends the stream
	if code, _ := r.render(ev("turn.end", map[string]any{"turn": 2, "stopReason": "cancelled"}), true); code != 130 {
		t.Fatalf("cancelled → %d", code)
	}
	if code, _ := r.render(ev("turn.end", map[string]any{"turn": 3, "stopReason": "refusal"}), true); code != 3 {
		t.Fatalf("refusal → %d", code)
	}
	code, done = r.render(ev("status", map[string]any{"status": "error", "detail": "auth"}), false)
	if !done || code != 3 || !strings.Contains(out.String(), "[error] auth") {
		t.Fatalf("error status: %d %v", code, done)
	}
	// attach mode: a turn's end does not stop the stream
	if _, done := r.render(ev("turn.end", map[string]any{"turn": 4, "stopReason": "end_turn"}), false); done {
		t.Fatal("attach stops at turn.end")
	}
}

// A plan approval prints the plan and its options, answerable by option id;
// a shell call prints its description, not a multi-line heredoc.
func TestAgentRendererPlan(t *testing.T) {
	var out bytes.Buffer
	r := newAgentRenderer(&out, "s1")
	r.render(ev("tool.call", map[string]any{"id": "t1", "title": "python3 - <<'EOF'\nprint(1)\nEOF", "kind": "execute", "label": "Rewrite the handler"}), true)
	r.render(ev("permission.request", map[string]any{"pid": "p1", "meta": map[string]any{"title": "Ready to code?"},
		"toolCall": map[string]any{"title": "Approve Plan", "kind": "switch_mode", "rawInput": map[string]any{"plan": "# P\n1. x"},
			"content": []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": "# P\n1. x\n"}}}},
		"options": []map[string]any{{"optionId": "auto", "name": "Yes, and use auto mode", "kind": "allow_always"},
			{"optionId": "reject", "name": "No, keep planning", "kind": "reject_once"}}}), true)
	s := out.String()
	for _, want := range []string{"⚙ t1 Rewrite the handler [execute]", "⚠ plan p1: Ready to code?", "  │ # P\n  │ 1. x\n",
		"auto           Yes, and use auto mode", "bx agent permit s1 p1 <option id>"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	r.render(ev("files.changed", map[string]any{"toolCallId": "t1", "changes": []map[string]any{{"path": "made.txt", "status": "added", "add": 1, "del": 0},
		{"path": "b.go", "oldPath": "a.go", "status": "renamed", "add": 0, "del": 0}}}), true)
	if !strings.Contains(out.String(), "  t1 changed made.txt (added) +1/-0, a.go → b.go +0/-0\n") {
		t.Fatalf("files.changed line:\n%s", out.String())
	}
	if !r.plan["p1"] {
		t.Fatal("the plan pid is not marked (the 's' key must not answer it)")
	}
}
