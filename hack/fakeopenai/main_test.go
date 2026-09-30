package main

import (
	"strings"
	"testing"
)

// The coding-agent keywords (D147 §7.2) win over the words they hold
// ("steer", "fan out"), and follow the conversation: the spawn's answer
// line, three background coding agents, the newest one steered.
func TestHarnessKeywords(t *testing.T) {
	user := func(s string) turn { return turn{Role: "user", Text: s} }
	tool := func(name, s string) turn { return turn{Role: "tool", Tool: name, Text: s} }

	p := script([]turn{user("harness spawn")}, "")
	if len(p.Calls) != 1 || p.Calls[0].Name != "subagent_spawn" || p.Calls[0].Args["harness"] != "fake" || p.Calls[0].Args["task"] != "perm" ||
		p.Calls[0].Args["label"] != "fake coder" || p.Calls[0].Args["wait"] != nil {
		t.Fatalf("harness spawn: %+v", p)
	}
	p = script([]turn{user("harness spawn"), {Role: "assistant"}, tool("subagent_spawn", "--- #7 fake coder (done) ---\nlisted\nmore")}, "")
	if p.Text != "The coding agent said: listed" {
		t.Fatalf("harness spawn's answer: %q", p.Text)
	}

	p = script([]turn{user("Harness fan out please")}, "")
	var tasks []string
	for _, c := range p.Calls {
		if c.Name != "subagent_spawn" || c.Args["harness"] != "fake" || c.Args["wait"] != false {
			t.Fatalf("harness fan out: %+v", c)
		}
		tasks = append(tasks, c.Args["task"].(string))
	}
	if strings.Join(tasks, ",") != "perm,slow,todo" {
		t.Fatalf("harness fan out's tasks: %v", tasks)
	}
	if p = script([]turn{user("harness fan out"), {Role: "assistant"}, tool("subagent_spawn", "started coding agent #9 …")}, ""); p.Text != "Started three coding agents." {
		t.Fatalf("harness fan out's answer: %q", p.Text)
	}

	conv := []turn{user("harness fan out"), {Role: "assistant"},
		tool("subagent_spawn", "started coding agent #9 (Fake agent (tests)) in the background"),
		tool("subagent_spawn", "started coding agent #11 (Fake agent (tests)) in the background"),
		tool("subagent_spawn", "started coding agent #10 (Fake agent (tests)) in the background"),
		{Role: "assistant", Text: "Started three coding agents."}, user("harness steer")}
	p = script(conv, "")
	if len(p.Calls) != 1 || p.Calls[0].Name != "subagent_message" || p.Calls[0].Args["id"] != 11 || p.Calls[0].Args["text"] != "steer: use tabs" {
		t.Fatalf("harness steer: %+v", p)
	}
	if p = script(append(conv, turn{Role: "assistant"}, tool("subagent_message", "steered into #11's running turn")), ""); p.Text != "Steered it." {
		t.Fatalf("harness steer's answer: %q", p.Text)
	}
	// the plain words still play theirs
	if p = script([]turn{user("steer: faster")}, ""); !strings.HasPrefix(p.Text, "Got your steer") {
		t.Fatalf("steer: %q", p.Text)
	}
}
