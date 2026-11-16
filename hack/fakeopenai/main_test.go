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

// A coding CLI's turn (coder.go): the shell tool the request offers, then
// the tool's output read back; the template's own turns are untouched.
func TestCoderKeyword(t *testing.T) {
	user := func(s string) turn { return turn{Role: "user", Text: s} }
	for tools, want := range map[string]string{"read,bash,edit": "bash", "shell,apply_patch": "shell", "exec_command": "exec_command", "local_shell": "local_shell"} {
		p := pick([]turn{user("Please shell echo now")}, "You are a coding agent", strings.Split(tools, ","))
		if len(p.Calls) != 1 || p.Calls[0].Name != want {
			t.Fatalf("tools %s: %+v", tools, p)
		}
	}
	if p := pick([]turn{user("shell echo")}, "", []string{"note"}); p.Text != "No shell tool offered." {
		t.Fatalf("no shell tool: %+v", p)
	}
	conv := []turn{user("shell echo"), {Role: "assistant"}, {Role: "tool", Tool: "bash", Text: "<stdout>\nconnector-tool-ok\n</stdout>"}}
	if p := pick(conv, "", []string{"bash"}); p.Text != "Shell said: connector-tool-ok" {
		t.Fatalf("the answer: %q", p.Text)
	}
	if p := pick([]turn{user("hello")}, "", []string{"bash"}); p.Text != "Hello from the fake model." {
		t.Fatalf("hello: %+v", p)
	}
	// a title request that quotes the message is still a title
	if p := pick([]turn{user("First message:\nshell echo")}, "Name this conversation", []string{"bash"}); !strings.HasPrefix(p.Text, "Titled") {
		t.Fatalf("title: %+v", p)
	}
}

// A "ws:" line of a person's message is a script of tool calls (ws.go):
// one a request, in order, then a summary of what each answered.
func TestWsScript(t *testing.T) {
	msg := "[context your person attached to this message: the tile \"Orders\" (apps/orders)]\nws: workspace_map ;; tile_call {\"tile\":\"apps/Orders\",\"method\":\"GET\",\"path\":\"/Note\"}"
	conv := []turn{{Role: "user", Text: "hello"}, {Role: "assistant", Text: "Hello"}, {Role: "user", Text: msg}}
	p := script(conv, "")
	if len(p.Calls) != 1 || p.Calls[0].Name != "workspace_map" || len(p.Calls[0].Args) != 0 {
		t.Fatalf("the first step: %+v", p)
	}
	conv = append(conv, turn{Role: "tool", Tool: "workspace_map", Text: "5 tiles\nmore"})
	p = script(conv, "")
	if len(p.Calls) != 1 || p.Calls[0].Name != "tile_call" || p.Calls[0].Args["tile"] != "apps/Orders" || p.Calls[0].Args["path"] != "/Note" {
		t.Fatalf("the second step (arguments keep their case): %+v", p)
	}
	conv = append(conv, turn{Role: "tool", Tool: "tile_call", Text: "200 note=first"})
	if p = script(conv, ""); len(p.Calls) != 0 || p.Text != "Done: 5 tiles | 200 note=first" {
		t.Fatalf("the summary: %+v", p)
	}
	// a later message that is no script is answered as any other
	conv = append(conv, turn{Role: "assistant", Text: p.Text}, turn{Role: "user", Text: "[workspace updates] a request was approved"})
	if p = script(conv, ""); len(p.Calls) != 0 || !strings.HasPrefix(p.Text, "ok: ") {
		t.Fatalf("a message after the script: %+v", p)
	}
	// the agent's own message landing after the script — at the turn's first
	// step boundary, or between two steps — doesn't end it
	mid := []turn{{Role: "user", Text: "ws: tile_agents ;; workspace_map"}, {Role: "user", Text: "[workspace updates] a tile agent waits"}}
	if p = script(mid, ""); len(p.Calls) != 1 || p.Calls[0].Name != "tile_agents" {
		t.Fatalf("a script with the agent's updates after it: %+v", p)
	}
	mid = append(mid, turn{Role: "tool", Tool: "tile_agents", Text: "none"}, turn{Role: "user", Text: "[workspace updates] it ended its turn"})
	if p = script(mid, ""); len(p.Calls) != 1 || p.Calls[0].Name != "workspace_map" {
		t.Fatalf("updates between two steps: %+v", p)
	}
	mid = append(mid, turn{Role: "tool", Tool: "workspace_map", Text: "5 tiles"})
	if p = script(mid, ""); p.Text != "Done: none | 5 tiles" {
		t.Fatalf("the summary past the updates: %+v", p)
	}
	if p = script([]turn{{Role: "user", Text: "ws: tile_call {not json"}}, ""); len(p.Calls) != 1 || p.Calls[0].Name != "invalid_ws_script" {
		t.Fatalf("a script that doesn't parse: %+v", p)
	}
}

// A "wait" step ends the turn until xbin's update arrives — with the words
// the step names, when it names some — and the script goes on from there.
func TestWsScriptWait(t *testing.T) {
	user := func(s string) turn { return turn{Role: "user", Text: s} }
	tool := func(name, s string) turn { return turn{Role: "tool", Tool: name, Text: s} }
	update := func(s string) turn {
		return user(wsUpdatesLead + " — xbin and tile agents reporting, not your person]\n" + s)
	}
	conv := []turn{user(`ws: tile_create {"template":"starter","name":"Dice"} ;; wait now exists ;; workspace_map`)}
	if p := script(conv, ""); len(p.Calls) != 1 || p.Calls[0].Name != "tile_create" {
		t.Fatalf("the first step: %+v", p)
	}
	conv = append(conv, turn{Role: "assistant"}, tool("tile_create", "Pending — request 1 (create the tile) waits for your person's decision in xbin."))
	if p := script(conv, ""); p.Text != wsWaitWords || len(p.Calls) != 0 {
		t.Fatalf("at the wait: %+v", p)
	}
	conv = append(conv, turn{Role: "assistant", Text: wsWaitWords}, update("Tile agent #1 ended its turn."))
	if p := script(conv, ""); p.Text != wsWaitWords {
		t.Fatalf("an update without the words ends no wait: %+v", p)
	}
	conv = append(conv, turn{Role: "assistant", Text: wsWaitWords}, update("approved by your person — the tile apps/dice now exists, made from starter."))
	if p := script(conv, ""); len(p.Calls) != 1 || p.Calls[0].Name != "workspace_map" {
		t.Fatalf("after the update: %+v", p)
	}
	conv = append(conv, turn{Role: "assistant"}, tool("workspace_map", "This session: 3 tiles"))
	if p := script(conv, ""); p.Text != wsDone+wsPendingWords+" | This session: 3 tiles" {
		t.Fatalf("the summary: %q", p.Text)
	}
}
