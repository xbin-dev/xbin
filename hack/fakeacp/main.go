// fakeacp is a scripted ACP agent for tests and the UI harness (provider
// "fake", registered when xbind runs with XBIN_AGENT_FAKE=<path to this
// binary>). It speaks ACP v1 over stdio the way the real adapters do and
// plays a script chosen by words in the prompt:
//
//	(default)   one agent_message_chunk "echo: <text>" — plus, for a prompt
//	            with attachments, a description of each non-text block
//	            ([image <type> <n>B], [resource <name> <n>B], [file <name>
//	            <n>B] read from the linked path) — usage, end_turn
//	perm        a tool_call + session/request_permission (once/always/no);
//	            selected → the tool completes, cancelled → the turn ends cancelled
//	plan…       (a prefix) Claude's plan approval: an ExitPlanMode tool_call
//	            (kind switch_mode, the plan as text content + rawInput.plan) and
//	            a request_permission with its mode options (two allow_always)
//	            and _meta.permission.title "Ready to code?"; approve →
//	            "plan approved: <option>", reject → the turn ends cancelled
//	ask…        (a prefix) Claude's AskUserQuestion: a tool_call, then
//	            elicitation/create (form: a single-select with descriptions, a
//	            multi-select, each with its "Other" box, as claude-agent-acp
//	            builds it); accept → "answers: <content json>", decline →
//	            "skipped", cancel → the turn ends cancelled
//	subagent…   (a prefix) Claude's Task call: a tool_call (name Task, kind
//	            think, _meta.claudeCode.subagent) and, tagged with its id as
//	            _meta.claudeCode.parentToolUseId, the subagent's thought, a Read
//	            call and its text; then the Task completes with its answer
//	think…      (a prefix) six agent_thought_chunks 300 ms apart, then a chunk
//	            "thought it through"
//	term        terminal/create `sh -c 'echo hi; printenv FAKE_API_KEY | wc -c'`,
//	            wait, output → a chunk "term: <output>"
//	run: <cmd>  terminal/create `sh -c '<cmd>'` the same way → "run: <output>",
//	            wrapped like a real adapter's shell call: an execute tool_call
//	            (rawInput.command) completed with _meta.terminal_output +
//	            terminal_exit (failed on a non-zero exit)
//	env         a chunk "HOME=<home> key=<yes|no> settings=<~/.claude/settings.json via fs/read_text_file>"
//	write       fs/write_text_file <cwd>/fake-wrote.txt
//	slow        ten chunks 200 ms apart (cancel lands mid-turn)
//	burst       fifty one-character chunks back to back (the daemon coalesces them)
//	fail        pushes _auth/status_update{kind:none}, then the prompt fails
//	            with -32000 (auth required) — a signed-out agent, as Claude does
//	crash       exits 3 mid-turn
//
// After session/new it advertises three slash commands (review, compact,
// init). Mode "yolo" skips the permission request. session/cancel ends the turn
// with stopReason cancelled. The session advertises one config option,
// `model` (fake-default | fake-fast), settable with
// session/set_config_option (the response carries the refreshed list, and
// a config_option_update follows) — the "env" script reports the current
// value too, so a test can see a requested model applied. It advertises
// promptCapabilities image + embeddedContext, as the real adapters do. It advertises
// loadSession: session/load replays one canned earlier turn ("resumed <id>"
// and the agent's echo) before answering — the resume tests.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/agent/acp"
)

type fake struct {
	conn   *acp.Conn
	mu     sync.Mutex
	mode   string
	model  string
	titled bool
	cwd    string
	prompt json.RawMessage // in-flight prompt id
	cancel chan struct{}
}

func main() {
	f := &fake{mode: "ask", model: "fake-default"}
	f.conn = acp.NewConn(os.Stdin, os.Stdout)
	f.conn.OnRequest = f.onRequest
	f.conn.OnNotify = f.onNotify
	fmt.Fprintln(os.Stderr, "fakeacp: up")
	_ = f.conn.Serve()
}

func (f *fake) onRequest(m *acp.Message) (any, *acp.Error) {
	switch m.Method {
	case acp.MInitialize:
		return acp.InitializeResult{ProtocolVersion: 1, AgentInfo: &acp.Info{Name: "fakeacp", Version: "1"},
			AgentCapabilities: &acp.AgentCapabilities{LoadSession: true, // session/load replays a canned history (resume tests)
				PromptCapabilities: &acp.PromptCapabilities{Image: true, EmbeddedContext: true}}, // as the real adapters do
			AuthMethods: []acp.AuthMethod{{ID: "api-key", Name: "API key"}}}, nil
	case acp.MAuthenticate:
		return map[string]any{}, nil
	case acp.MSessionNew:
		var p acp.SessionNewParams
		_ = json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.cwd = p.Cwd
		f.mu.Unlock()
		go func() { // the adapters advertise their slash commands just after session/new
			time.Sleep(50 * time.Millisecond)
			f.update(map[string]any{"sessionUpdate": acp.UpAvailableCmds, "availableCommands": []map[string]any{
				{"name": "review", "description": "Review the pending changes", "input": map[string]string{"hint": "what to focus on"}},
				{"name": "compact", "description": "Summarize the conversation to free context"},
				{"name": "init", "description": "Write a CLAUDE.md for this project"}}})
		}()
		return acp.SessionNewResult{SessionID: "fake-1", Modes: &acp.SessionModes{CurrentModeID: "ask",
			AvailableModes: []acp.ModeEntry{{ID: "ask", Name: "Ask"}, {ID: "yolo", Name: "Yolo"}}},
			ConfigOptions: f.configOptions()}, nil
	case acp.MSessionLoad:
		// resume: the prior turns stream back as session/update BEFORE the
		// answer — a user line and the agent's echo of it, tagged with the id
		var p acp.SessionLoadParams
		_ = json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.cwd = p.Cwd
		f.mu.Unlock()
		f.update(map[string]any{"sessionUpdate": acp.UpUserChunk, "content": acp.ContentBlock{Type: "text", Text: "resumed " + p.SessionID}})
		f.update(map[string]any{"sessionUpdate": acp.UpAgentChunk, "content": acp.ContentBlock{Type: "text", Text: "echo: resumed " + p.SessionID}, "messageId": "m0"})
		return acp.SessionLoadResult{Modes: &acp.SessionModes{CurrentModeID: "ask",
			AvailableModes: []acp.ModeEntry{{ID: "ask", Name: "Ask"}, {ID: "yolo", Name: "Yolo"}}},
			ConfigOptions: f.configOptions()}, nil
	case acp.MSessionSetConfig:
		var p acp.SetConfigParams
		_ = json.Unmarshal(m.Params, &p)
		if p.ConfigID != "model" || (p.Value != "fake-default" && p.Value != "fake-fast") {
			return nil, &acp.Error{Code: acp.ErrInvalidParam, Message: "unknown option or value"}
		}
		f.mu.Lock()
		f.model = p.Value
		f.mu.Unlock()
		opts := f.configOptions()
		f.update(map[string]any{"sessionUpdate": acp.UpConfigOption, "configOptions": opts})
		return acp.SetConfigResult{ConfigOptions: opts}, nil
	case acp.MSessionSetMode:
		var p acp.SetModeParams
		_ = json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.mode = p.ModeID
		f.mu.Unlock()
		f.update(map[string]any{"sessionUpdate": acp.UpCurrentMode, "currentModeId": p.ModeID})
		return map[string]any{}, nil
	case acp.MSessionPrompt:
		var p acp.PromptParams
		_ = json.Unmarshal(m.Params, &p)
		text, files := readPrompt(p.Prompt)
		f.mu.Lock()
		f.prompt = m.ID
		f.cancel = make(chan struct{})
		c := f.cancel
		f.mu.Unlock()
		go f.turn(text, files, c)
		return nil, nil
	}
	return nil, &acp.Error{Code: acp.ErrNotFound, Message: "method not found: " + m.Method}
}

// configOptions is the one advertised setting, with its current value.
func (f *fake) configOptions() []acp.ConfigOption {
	f.mu.Lock()
	cur := f.model
	f.mu.Unlock()
	return []acp.ConfigOption{{ID: "model", Name: "Model", Category: "model", Type: "select", CurrentValue: cur,
		Options: []acp.ConfigValue{{Value: "fake-default", Name: "Fake (default)"}, {Value: "fake-fast", Name: "Fake fast"}}}}
}

func (f *fake) onNotify(m *acp.Message) {
	if m.Method == acp.MSessionCancel {
		f.mu.Lock()
		c := f.cancel
		f.mu.Unlock()
		if c != nil {
			select {
			case <-c:
			default:
				close(c)
			}
		}
		f.end("cancelled")
	}
}

func (f *fake) update(v any) {
	_ = f.conn.Notify(acp.MSessionUpdate, map[string]any{"sessionId": "fake-1", "update": v})
}

func (f *fake) say(text string) {
	f.update(map[string]any{"sessionUpdate": acp.UpAgentChunk, "content": acp.ContentBlock{Type: "text", Text: text}, "messageId": "m"})
}

func (f *fake) end(reason string) {
	f.mu.Lock()
	id := f.prompt
	f.prompt = nil
	f.mu.Unlock()
	if id != nil {
		_ = f.conn.Reply(id, acp.PromptResult{StopReason: reason}, nil)
	}
}

// cancelled reports whether THIS turn was cancelled (c is the turn's own
// channel: a cancelled turn still sleeping must not see the next turn's).
func cancelled(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// readPrompt is a prompt's text (its text blocks) and a description of
// every other block — what the default script echoes: [image <type>
// <bytes>B], [resource <name> <bytes>B] for embedded text, [file <name>
// <bytes>B] for a resource_link, read from the path it names (the file the
// host dropped in the sandbox: the size proves it arrived).
func readPrompt(blocks []acp.ContentBlock) (string, []string) {
	var text []string
	var files []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text = append(text, b.Text)
		case "image":
			raw, err := base64.StdEncoding.DecodeString(b.Data)
			if err != nil {
				files = append(files, "[image "+b.MimeType+" undecodable]")
				continue
			}
			files = append(files, fmt.Sprintf("[image %s %dB]", b.MimeType, len(raw)))
		case "resource":
			if b.Resource != nil {
				files = append(files, fmt.Sprintf("[resource %s %dB]", path.Base(b.Resource.URI), len(b.Resource.Text)))
			}
		case "resource_link":
			u, err := url.Parse(b.URI)
			if err != nil || u.Scheme != "file" {
				files = append(files, "[link "+b.URI+"]")
				continue
			}
			data, err := os.ReadFile(u.Path)
			if err != nil {
				files = append(files, "[file "+b.Name+" unreadable]")
				continue
			}
			files = append(files, fmt.Sprintf("[file %s %dB]", b.Name, len(data)))
		default:
			files = append(files, "["+b.Type+"]")
		}
	}
	return strings.Join(text, "\n"), files
}

func (f *fake) turn(text string, files []string, cancel chan struct{}) {
	f.mu.Lock()
	mode, cwd := f.mode, f.cwd
	f.mu.Unlock()
	switch {
	case strings.Contains(text, "fail"):
		f.mu.Lock()
		id := f.prompt
		f.prompt = nil
		f.mu.Unlock()
		// a signed-out agent: push the sign-out status (Claude does this), then
		// fail the turn with the auth error — the client shows the sign-in banner
		_ = f.conn.Notify(acp.MAuthStatus, map[string]any{"authStatus": map[string]any{"kind": "none"}})
		_ = f.conn.Reply(id, nil, &acp.Error{Code: acp.ErrAuthRequired, Message: "Please run /login"})
		return
	case strings.Contains(text, "crash"):
		f.say("going down")
		os.Exit(3)
	case strings.Contains(text, "burst"):
		for i := 0; i < 50; i++ {
			f.say("x")
		}
	case strings.Contains(text, "slow"):
		for i := 0; i < 10; i++ {
			if cancelled(cancel) {
				return
			}
			f.say(fmt.Sprintf("tick %d ", i))
			time.Sleep(200 * time.Millisecond)
		}
	case strings.HasPrefix(text, "ask"):
		f.update(map[string]any{"sessionUpdate": acp.UpToolCall, "toolCallId": "ask1", "title": "AskUserQuestion", "kind": "other", "status": "pending",
			"_meta": map[string]any{"claudeCode": map[string]any{"toolName": "AskUserQuestion"}}})
		var res struct {
			Action  string          `json:"action"`
			Content json.RawMessage `json:"content"`
		}
		opt := func(label, desc string) map[string]string {
			return map[string]string{"const": label, "title": label, "description": desc}
		}
		other := func(q string) map[string]any {
			return map[string]any{"type": "string", "title": "Other", "description": "Type your own answer (optional).",
				"_meta": map[string]any{"_askUserQuestionCustomAnswer": map[string]any{"questionId": q, "isCustomAnswer": true}}}
		}
		err := f.conn.Call(acp.MElicitCreate, map[string]any{"mode": "form", "sessionId": "fake-1", "toolCallId": "ask1",
			"message": "Please answer the following questions.", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{
				"question_0": map[string]any{"type": "string", "title": "Database", "description": "Which database should the service use?",
					"oneOf": []any{opt("Postgres", "Relational, the default"), opt("SQLite", "One file, no server")}},
				"question_0_custom": other("question_0"),
				"question_1": map[string]any{"type": "array", "title": "Extras", "description": "What else should it ship with?",
					"items": map[string]any{"anyOf": []any{opt("Metrics", ""), opt("Tracing", ""), opt("Admin UI", "")}}},
				"question_1_custom": other("question_1")}}}, &res)
		if err != nil || res.Action == "cancel" {
			f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "ask1", "status": "failed"})
			f.end("cancelled")
			return
		}
		f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "ask1", "status": "completed"})
		if res.Action == "decline" {
			f.say("skipped")
		} else {
			f.say("answers: " + string(res.Content))
		}
	case strings.HasPrefix(text, "subagent"):
		sub := map[string]any{"claudeCode": map[string]any{"parentToolUseId": "task1"}}
		f.update(map[string]any{"sessionUpdate": acp.UpToolCall, "toolCallId": "task1", "title": "Explore the repo", "kind": "think", "status": "in_progress",
			"rawInput": map[string]string{"description": "Explore the repo", "prompt": "Find where **main** starts.", "subagent_type": "Explore"},
			"content":  []map[string]any{{"type": "content", "content": acp.ContentBlock{Type: "text", Text: "Find where **main** starts."}}},
			"_meta":    map[string]any{"claudeCode": map[string]any{"toolName": "Task", "subagent": true}}})
		time.Sleep(200 * time.Millisecond)
		f.update(map[string]any{"sessionUpdate": acp.UpThoughtChunk, "content": acp.ContentBlock{Type: "text", Text: "Looking for the entry point."}, "_meta": sub})
		f.update(map[string]any{"sessionUpdate": acp.UpToolCall, "toolCallId": "read1", "title": "Read main.go", "kind": "read", "status": "in_progress",
			"_meta": map[string]any{"claudeCode": map[string]any{"toolName": "Read", "parentToolUseId": "task1"}}})
		time.Sleep(200 * time.Millisecond)
		f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "read1", "status": "completed", "_meta": sub})
		f.update(map[string]any{"sessionUpdate": acp.UpAgentChunk, "content": acp.ContentBlock{Type: "text", Text: "main starts in main.go"}, "messageId": "sub-m", "_meta": sub})
		time.Sleep(200 * time.Millisecond)
		f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "task1", "status": "completed",
			"content": []map[string]any{{"type": "content", "content": acp.ContentBlock{Type: "text", Text: "`main` is in **main.go**."}}}})
		f.say("the subagent found it")
	case strings.HasPrefix(text, "think"):
		for i := 0; i < 6; i++ {
			if cancelled(cancel) {
				return
			}
			f.update(map[string]any{"sessionUpdate": acp.UpThoughtChunk, "content": acp.ContentBlock{Type: "text", Text: fmt.Sprintf("**step %d** — weighing it. ", i)}})
			time.Sleep(300 * time.Millisecond)
		}
		f.say("thought it through")
	case strings.HasPrefix(text, "plan"):
		if !f.plan() {
			return
		}
	case strings.Contains(text, "perm"):
		f.update(map[string]any{"sessionUpdate": acp.UpToolCall, "toolCallId": "t1", "title": "run ls", "kind": "execute", "rawInput": map[string]string{"cmd": "ls"}})
		if mode != "yolo" {
			var res acp.RequestPermissionResult
			err := f.conn.Call(acp.MRequestPermission, acp.RequestPermissionParams{SessionID: "fake-1",
				ToolCall: acp.ToolCallUpdate{ToolCallID: "t1", Title: strp("run ls"), Kind: strp("execute")},
				Options: []acp.PermissionOption{{OptionID: "once", Name: "Allow once", Kind: "allow_once"},
					{OptionID: "always", Name: "Allow always", Kind: "allow_always"}, {OptionID: "no", Name: "Reject", Kind: "reject_once"}}}, &res)
			if err != nil || res.Outcome.Outcome != "selected" {
				f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "t1", "status": "failed"})
				f.end("cancelled")
				return
			}
			if res.Outcome.OptionID == "no" {
				f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "t1", "status": "failed"})
				f.say("denied")
				f.end("end_turn")
				return
			}
		}
		f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "t1", "status": "completed",
			"content": []map[string]any{{"type": "content", "content": acp.ContentBlock{Type: "text", Text: "a.txt b.txt"}}}})
		f.say("listed")
	case strings.HasPrefix(text, "run:"), strings.Contains(text, "term"):
		label, script := "term", "echo hi; printenv FAKE_API_KEY | wc -c"
		if strings.HasPrefix(text, "run:") {
			label, script = "run", strings.TrimSpace(strings.TrimPrefix(text, "run:"))
		}
		if label == "run" {
			f.update(map[string]any{"sessionUpdate": acp.UpToolCall, "toolCallId": "run1", "title": script, "kind": "execute",
				"status": "in_progress", "rawInput": map[string]string{"command": script}})
		}
		var cr acp.TermCreateResult
		if err := f.conn.Call(acp.MTermCreate, acp.TermCreateParams{SessionID: "fake-1", Command: "sh",
			Args: []string{"-c", script + " 2>&1"}}, &cr); err != nil {
			f.say(label + " error: " + err.Error())
			break
		}
		var st acp.ExitStatus
		_ = f.conn.Call(acp.MTermWait, acp.TermIDParams{SessionID: "fake-1", TerminalID: cr.TerminalID}, &st)
		var out acp.TermOutputResult
		_ = f.conn.Call(acp.MTermOutput, acp.TermIDParams{SessionID: "fake-1", TerminalID: cr.TerminalID}, &out)
		_ = f.conn.Call(acp.MTermRelease, acp.TermIDParams{SessionID: "fake-1", TerminalID: cr.TerminalID}, nil)
		if label == "run" {
			code, status := 0, "completed"
			if st.ExitCode != nil && *st.ExitCode != 0 {
				code, status = *st.ExitCode, "failed"
			}
			f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "run1", "status": status,
				"content": []map[string]any{{"type": "terminal", "terminalId": "run1"}},
				"_meta":   map[string]any{"terminal_output": map[string]any{"terminal_id": "run1", "data": out.Output}, "terminal_exit": map[string]any{"terminal_id": "run1", "exit_code": code}}})
		}
		f.say(label + ": " + strings.Join(strings.Fields(out.Output), " "))
	case strings.Contains(text, "env"):
		key := "no"
		if os.Getenv("FAKE_API_KEY") != "" {
			key = "yes"
		}
		var rd acp.FsReadResult
		settings := "<none>"
		if err := f.conn.Call(acp.MFsRead, acp.FsReadParams{SessionID: "fake-1", Path: os.Getenv("HOME") + "/.claude/settings.json"}, &rd); err == nil {
			settings = strings.TrimSpace(rd.Content)
		}
		f.mu.Lock()
		model := f.model
		f.mu.Unlock()
		f.say(fmt.Sprintf("HOME=%s key=%s settings=%s model=%s", os.Getenv("HOME"), key, settings, model))
	case strings.Contains(text, "write"):
		err := f.conn.Call(acp.MFsWrite, acp.FsWriteParams{SessionID: "fake-1", Path: cwd + "/fake-wrote.txt", Content: "written by fakeacp\n"}, nil)
		if err != nil {
			f.say("write error: " + err.Error())
		} else {
			f.say("wrote fake-wrote.txt")
		}
	default:
		if len(files) > 0 {
			f.say("echo: " + text + " " + strings.Join(files, " "))
		} else {
			f.say("echo: " + text)
		}
	}
	if cancelled(cancel) {
		return
	}
	f.update(map[string]any{"sessionUpdate": acp.UpUsage, "used": 42, "size": 1000})
	f.mu.Lock()
	first := !f.titled
	f.titled = true
	f.mu.Unlock()
	if first { // the adapters title a session from its first prompt
		t := text
		if len(t) > 40 {
			t = t[:40]
		}
		f.update(map[string]any{"sessionUpdate": acp.UpSessionInfo, "title": "fake: " + t})
	}
	f.end("end_turn")
}

// plan plays claude-agent-acp's ExitPlanMode approval; false when the turn
// already ended (rejected: Claude ends it as cancelled).
func (f *fake) plan() bool {
	const md = "# Fake plan\n\n1. **Read** the code\n2. Change `main.go`\n"
	body := []map[string]any{{"type": "content", "content": acp.ContentBlock{Type: "text", Text: md}}}
	raw := map[string]string{"plan": md, "planFilePath": "/tmp/fake-plan.md"}
	f.update(map[string]any{"sessionUpdate": acp.UpToolCall, "toolCallId": "plan1", "title": "Ready to code?", "kind": "switch_mode",
		"status": "pending", "content": body, "rawInput": raw, "_meta": map[string]any{"claudeCode": map[string]any{"toolName": "ExitPlanMode"}}})
	var res acp.RequestPermissionResult
	err := f.conn.Call(acp.MRequestPermission, map[string]any{"sessionId": "fake-1",
		"toolCall": map[string]any{"toolCallId": "plan1", "title": "Approve Plan", "kind": "switch_mode", "content": body, "rawInput": raw},
		"options": []acp.PermissionOption{{OptionID: "exit-plan-default", Name: "Yes, manually approve edits", Kind: "allow_once"},
			{OptionID: "exit-plan-clear-auto", Name: "Yes, clear context (13% used) and use auto mode", Kind: "allow_always"},
			{OptionID: "auto", Name: "Yes, and use auto mode", Kind: "allow_always"},
			{OptionID: "reject", Name: "No, keep planning", Kind: "reject_once"}},
		"_meta": map[string]any{"permission": map[string]any{"title": "Ready to code?"}}}, &res)
	if err != nil || res.Outcome.Outcome != "selected" || res.Outcome.OptionID == "reject" {
		f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "plan1", "status": "failed"})
		f.end("cancelled")
		return false
	}
	f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "plan1", "status": "completed"})
	f.say("plan approved: " + res.Outcome.OptionID)
	return true
}

func strp(s string) *string { return &s }
