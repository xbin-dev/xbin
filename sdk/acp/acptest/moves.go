package acptest

// The scripts and methods added when the engine left hack/fakeacp: the
// steering extension, perm-edit, perm2, todo, cards, steer (stall is
// inline in turn). None of them changes what an existing script plays.

import (
	"encoding/json"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// mSteering is claude-agent-acp's and codex-acp's steering extension: a
// message into the running turn (advertised as InitializeResult
// _meta.steering.supported).
const mSteering = "_session/steering"

type steerParams struct {
	SessionID string             `json:"sessionId"`
	Prompt    []acp.ContentBlock `json:"prompt"`
	Meta      struct {
		Steering struct {
			IdleBehavior string `json:"idleBehavior"`
		} `json:"steering"`
	} `json:"_meta"`
}

type steerResult struct {
	Outcome string `json:"outcome"` // injected | promptRequired | startedNewTurn
	Reason  string `json:"reason,omitempty"`
}

// steer serves _session/steering (--steer) as claude-agent-acp does: into
// the running turn, else promptRequired when the client opted in, else a
// new turn of its own.
func (f *fake) steer(m *acp.Message) (any, *acp.Error) {
	var p steerParams
	if err := json.Unmarshal(m.Params, &p); err != nil || p.SessionID == "" || len(p.Prompt) == 0 {
		return nil, &acp.Error{Code: acp.CodeInvalidParams, Message: "steer params require a non-empty sessionId and prompt"}
	}
	idle := p.Meta.Steering.IdleBehavior
	if idle != "" && idle != "promptRequired" {
		return nil, &acp.Error{Code: acp.CodeInvalidParams, Message: "unsupported steering idleBehavior"}
	}
	text, files := readPrompt(p.Prompt)
	f.mu.Lock()
	if t := f.cur; t != nil {
		t.pending = append(t.pending, text)
		t.steered = append(t.steered, text)
		f.mu.Unlock()
		return steerResult{Outcome: "injected"}, nil
	}
	if idle == "promptRequired" {
		f.mu.Unlock()
		return steerResult{Outcome: "promptRequired", Reason: "noRunningTurn"}, nil
	}
	t := &turnState{cancel: make(chan struct{})} // a turn with no prompt to answer
	f.cur = t
	f.mu.Unlock()
	f.record(map[string]any{"sessionUpdate": acp.UpUserChunk, "content": acp.ContentBlock{Type: "text", Text: text}})
	_ = f.conn.Reply(m.ID, steerResult{Outcome: "startedNewTurn"}, nil) // before the turn's updates
	go f.echoTurn(t, text, files)
	return nil, nil
}

// echoTurn is the turn a steer starts when none runs: the default script's
// echo and usage, with no prompt to answer.
func (f *fake) echoTurn(t *turnState, text string, files []string) {
	if len(files) > 0 {
		f.say("echo: " + text + " " + strings.Join(files, " "))
	} else {
		f.say("echo: " + text)
	}
	if !cancelled(t.cancel) {
		f.flushSteers()
		f.update(map[string]any{"sessionUpdate": acp.UpUsage, "used": 42, "size": 1000})
	}
	f.mu.Lock()
	if f.cur == t {
		f.cur = nil
	}
	f.mu.Unlock()
}

// flushSteers says what was steered into the running turn since its last chunk.
func (f *fake) flushSteers() {
	f.mu.Lock()
	var steers []string
	if f.cur != nil {
		steers, f.cur.pending = f.cur.pending, nil
	}
	f.mu.Unlock()
	for _, s := range steers {
		f.chunk("steered: " + s)
	}
}

// steered is the steer… script: slow's ticks, then every steer it took;
// false when the turn was cancelled.
func (f *fake) steered(t *turnState) bool {
	for i := 0; i < 10; i++ {
		if cancelled(t.cancel) {
			return false
		}
		f.say("tick " + strconv.Itoa(i) + " ")
		f.sleep(200 * time.Millisecond)
	}
	f.flushSteers()
	f.mu.Lock()
	all := append([]string(nil), t.steered...)
	f.mu.Unlock()
	if len(all) == 0 {
		f.say("steers: none")
	} else {
		f.say("steers: " + strings.Join(all, " | "))
	}
	return true
}

// permEdit is an edit that asks first (not in auto or yolo); false when
// the turn already ended.
func (f *fake) permEdit(mode, cwd string) bool {
	file := inCwd(cwd, "hello.txt")
	diff := []map[string]any{{"type": "diff", "path": file, "oldText": "hello\n", "newText": "hello, world\n"}}
	f.update(map[string]any{"sessionUpdate": acp.UpToolCall, "toolCallId": "edit1", "title": "Edit hello.txt", "kind": "edit", "status": "pending",
		"content": diff, "locations": []map[string]any{{"path": file}},
		"rawInput": map[string]string{"file_path": file, "old_string": "hello", "new_string": "hello, world"}})
	if mode != "yolo" && mode != "auto" {
		var res acp.RequestPermissionResult
		err := f.conn.Call(acp.MRequestPermission, map[string]any{"sessionId": f.session(),
			"toolCall": map[string]any{"toolCallId": "edit1", "title": "Edit hello.txt", "kind": "edit", "content": diff},
			"options": []acp.PermissionOption{{OptionID: "once", Name: "Allow once", Kind: "allow_once"},
				{OptionID: "always", Name: "Allow always", Kind: "allow_always"}, {OptionID: "no", Name: "Reject", Kind: "reject_once"}}}, &res)
		if err != nil || res.Outcome.Outcome != "selected" {
			f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "edit1", "status": "failed"})
			f.end("cancelled")
			return false
		}
		if res.Outcome.OptionID == "no" {
			f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "edit1", "status": "failed"})
			f.say("denied")
			f.end("end_turn")
			return false
		}
	}
	f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "edit1", "status": "completed"})
	f.say("edited hello.txt")
	return true
}

// permPair is two calls that ask at once, as Claude's parallel tool calls
// do: both requests are out before either is answered, and the turn waits
// for both. Each allow completes its call, "no" fails it; a request that
// is cancelled (or fails) fails both calls and ends the turn cancelled —
// false then.
func (f *fake) permPair(mode string) bool {
	calls := []struct{ id, title, kind string }{{"t1", "run ls", "execute"}, {"t2", "rm -rf build", "delete"}}
	for _, c := range calls {
		f.update(map[string]any{"sessionUpdate": acp.UpToolCall, "toolCallId": c.id, "title": c.title, "kind": c.kind, "status": "pending"})
	}
	answers := []string{"yolo", "yolo"}
	if mode != "yolo" {
		opts := []acp.PermissionOption{{OptionID: "once", Name: "Allow once", Kind: "allow_once"},
			{OptionID: "always", Name: "Allow always", Kind: "allow_always"}, {OptionID: "no", Name: "Reject", Kind: "reject_once"}}
		// sent as an adapter with requests in flight sends them: each
		// awaited by its id, the second written before the first's answer
		var waits []<-chan *acp.Message
		for _, c := range calls {
			f.mu.Lock()
			f.asks++
			id, _ := json.Marshal("perm2-" + strconv.Itoa(f.asks))
			f.mu.Unlock()
			params, _ := json.Marshal(acp.RequestPermissionParams{SessionID: f.session(),
				ToolCall: acp.ToolCallUpdate{ToolCallID: c.id, Title: strp(c.title), Kind: strp(c.kind)}, Options: opts})
			waits = append(waits, f.conn.Expect(id))
			_ = f.conn.Send(&acp.Message{ID: id, Method: acp.MRequestPermission, Params: params})
		}
		ok := true
		for i, ch := range waits {
			var res acp.RequestPermissionResult
			m := <-ch // closed when Serve returns
			if m == nil || m.Error != nil || json.Unmarshal(m.Result, &res) != nil || res.Outcome.Outcome != "selected" {
				answers[i], ok = "cancelled", false
				continue
			}
			answers[i] = res.Outcome.OptionID
		}
		if !ok {
			for _, c := range calls {
				f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": c.id, "status": "failed"})
			}
			f.end("cancelled")
			return false
		}
	}
	for i, c := range calls {
		status := "completed"
		if answers[i] == "no" {
			status = "failed"
		}
		f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": c.id, "status": status})
	}
	f.say("perm2: " + strings.Join(answers, " "))
	return true
}

// todo is the agent's plan moving along; false when cancelled.
func (f *fake) todo(cancel chan struct{}) bool {
	steps := [][3]string{{"pending", "pending", "pending"}, {"in_progress", "pending", "pending"}, {"completed", "completed", "completed"}}
	for i, st := range steps {
		if i > 0 {
			f.sleep(200 * time.Millisecond)
		}
		if cancelled(cancel) {
			return false
		}
		f.update(map[string]any{"sessionUpdate": acp.UpPlan, "entries": []map[string]string{
			{"content": "Read the code", "priority": "high", "status": st[0]},
			{"content": "Change main.go", "priority": "medium", "status": st[1]},
			{"content": "Run the tests", "priority": "low", "status": st[2]}}})
	}
	f.say("todo done")
	return true
}

// cards is one completed tool call of every kind, shaped like the real
// adapters' (paths absolute under the session's cwd).
func (f *fake) cards(cwd string) {
	hello := inCwd(cwd, "hello.txt")
	text := func(s string) []map[string]any {
		return []map[string]any{{"type": "content", "content": acp.ContentBlock{Type: "text", Text: s}}}
	}
	calls := []struct {
		id, title, kind string
		start, done     map[string]any
	}{
		{"card-read", "Read hello.txt", "read",
			map[string]any{"locations": []map[string]any{{"path": hello, "line": 1}}, "rawInput": map[string]string{"file_path": hello}},
			map[string]any{"content": text("```\nhello\n```")}},
		{"card-edit", "Edit hello.txt", "edit",
			map[string]any{"locations": []map[string]any{{"path": hello}}, "rawInput": map[string]string{"file_path": hello},
				"content": []map[string]any{{"type": "diff", "path": hello, "oldText": "hello\n", "newText": "hello, world\n"}}},
			nil},
		{"card-delete", "Delete old.txt", "delete",
			map[string]any{"locations": []map[string]any{{"path": inCwd(cwd, "old.txt")}}, "rawInput": map[string]string{"path": inCwd(cwd, "old.txt")}},
			nil},
		{"card-move", "Move draft.md → notes.md", "move",
			map[string]any{"locations": []map[string]any{{"path": inCwd(cwd, "notes.md")}},
				"rawInput": map[string]string{"from": inCwd(cwd, "draft.md"), "to": inCwd(cwd, "notes.md")}},
			nil},
		{"card-search", "grep TODO", "search",
			map[string]any{"rawInput": map[string]string{"pattern": "TODO", "path": inCwd(cwd, ".")}},
			map[string]any{"content": text("hello.txt:3: // TODO: greet the world")}},
		{"card-exec", "go test ./...", "execute",
			map[string]any{"rawInput": map[string]string{"command": "go test ./..."}, "content": []map[string]any{{"type": "terminal", "terminalId": "card-exec"}}},
			map[string]any{"_meta": map[string]any{"terminal_output": map[string]any{"terminal_id": "card-exec", "data": "ok  \texample\t0.01s\n"},
				"terminal_exit": map[string]any{"terminal_id": "card-exec", "exit_code": 0}}}},
		{"card-fetch", "Fetch https://example.invalid/docs", "fetch",
			map[string]any{"rawInput": map[string]string{"url": "https://example.invalid/docs"}},
			map[string]any{"content": text("# Docs\n\nA page about greetings.")}},
		{"card-think", "Think it over", "think",
			map[string]any{"rawInput": map[string]string{"thought": "Which greeting fits?"}},
			map[string]any{"content": text("Weighing two greetings.")}},
		{"card-other", "Do something else", "other",
			map[string]any{"rawInput": map[string]string{}},
			map[string]any{"content": text("done")}},
	}
	for _, c := range calls {
		start := map[string]any{"sessionUpdate": acp.UpToolCall, "toolCallId": c.id, "title": c.title, "kind": c.kind, "status": "in_progress"}
		for k, v := range c.start {
			start[k] = v
		}
		f.update(start)
		done := map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": c.id, "status": "completed"}
		for k, v := range c.done {
			done[k] = v
		}
		f.update(done)
	}
	f.say("cards done")
}

// inCwd is name under the session's cwd (as it is when there is none).
func inCwd(cwd, name string) string {
	if cwd == "" {
		return name
	}
	return path.Join(cwd, name)
}

func (f *fake) session() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sid
}
