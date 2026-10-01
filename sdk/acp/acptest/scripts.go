package acptest

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// turn plays the script the prompt's words choose (doc.go has the list).
func (f *fake) turn(t *turnState, text string, files []string) {
	cancel := t.cancel
	f.mu.Lock()
	mode, cwd, sid := f.mode, f.cwd, f.sid
	f.mu.Unlock()
	switch {
	case strings.HasPrefix(text, "perm-edit"):
		if !f.permEdit(mode, cwd) {
			return
		}
	case strings.HasPrefix(text, "todo"):
		if !f.todo(cancel) {
			return
		}
	case strings.HasPrefix(text, "stall"):
		f.say("stalling")
		select { // nothing more until session/cancel
		case <-cancel:
		case <-f.done:
		}
		return
	case strings.HasPrefix(text, "cards"):
		f.cards(cwd)
	case strings.HasPrefix(text, "steer"):
		if !f.steered(t) {
			return
		}
	case strings.Contains(text, "fail"):
		f.mu.Lock()
		id := f.prompt
		f.prompt = nil
		f.cur = nil
		f.mu.Unlock()
		// a signed-out agent: push the sign-out status (Claude does this), then
		// fail the turn with the auth error — the client shows the sign-in banner
		_ = f.conn.Notify(acp.MAuthStatus, map[string]any{"authStatus": map[string]any{"kind": "none"}})
		_ = f.conn.Reply(id, nil, &acp.Error{Code: acp.CodeAuthRequired, Message: "Please run /login"})
		return
	case strings.Contains(text, "crash"):
		f.say("going down")
		f.exit(3)
		return
	case strings.HasPrefix(text, "paras"):
		n := 8
		fmt.Sscanf(strings.TrimPrefix(text, "paras"), "%d", &n)
		for i := 1; i <= n; i++ {
			if cancelled(cancel) {
				return
			}
			f.update(map[string]any{"sessionUpdate": acp.UpAgentChunk, "messageId": "paras", "content": acp.ContentBlock{Type: "text",
				Text: fmt.Sprintf("Paragraph %d of the answer, streamed.\n\n", i)}})
			f.sleep(250 * time.Millisecond)
		}
	case strings.HasPrefix(text, "chatty"):
		n := 200
		fmt.Sscanf(strings.TrimPrefix(text, "chatty"), "%d", &n)
		f.update(map[string]any{"sessionUpdate": acp.UpToolCall, "toolCallId": "chatty", "title": "yes | head", "kind": "execute", "status": "in_progress"})
		for i := 1; i <= n; i++ {
			f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "chatty",
				"_meta": map[string]any{"terminal_output_delta": map[string]any{"terminal_id": "chatty", "data": fmt.Sprintf("line %d\n", i)}}})
		}
		f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "chatty", "status": "completed",
			"_meta": map[string]any{"terminal_exit": map[string]any{"terminal_id": "chatty", "exit_code": 0}}})
		f.say(fmt.Sprintf("chatty: %d lines", n))
	case strings.HasPrefix(text, "long"):
		n := 100
		fmt.Sscanf(strings.TrimPrefix(text, "long"), "%d", &n)
		for i := 1; i <= n; i++ {
			if cancelled(cancel) {
				return
			}
			if i%10 == 1 {
				f.update(map[string]any{"sessionUpdate": acp.UpThoughtChunk, "content": acp.ContentBlock{Type: "text", Text: fmt.Sprintf("Planning **unit %d**: which file next.", i)}})
			}
			f.update(map[string]any{"sessionUpdate": acp.UpAgentChunk, "messageId": fmt.Sprintf("long-%d", i), "content": acp.ContentBlock{Type: "text",
				Text: fmt.Sprintf("### Unit %d\n\nChanging `file%d.go`:\n\n- rename the helper\n- keep **behaviour**\n\n```go\nfunc helper%d() int { return %d }\n```\n", i, i, i, i)}})
			var before, after strings.Builder
			for l := 0; l < 30; l++ {
				fmt.Fprintf(&before, "line %d of file%d\n", l, i)
				if l%7 == 3 {
					fmt.Fprintf(&after, "line %d of file%d, changed\n", l, i)
				} else {
					fmt.Fprintf(&after, "line %d of file%d\n", l, i)
				}
			}
			id := fmt.Sprintf("long%d", i)
			f.update(map[string]any{"sessionUpdate": acp.UpToolCall, "toolCallId": id, "title": fmt.Sprintf("Edit file%d.go", i), "kind": "edit", "status": "in_progress",
				"content": []map[string]any{{"type": "diff", "path": fmt.Sprintf("file%d.go", i), "oldText": before.String(), "newText": after.String()}}})
			f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": id, "status": "completed"})
		}
		f.say(fmt.Sprintf("done: %d units", n))
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
			f.sleep(200 * time.Millisecond)
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
		err := f.conn.Call(acp.MElicitCreate, map[string]any{"mode": "form", "sessionId": sid, "toolCallId": "ask1",
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
		f.sleep(200 * time.Millisecond)
		f.update(map[string]any{"sessionUpdate": acp.UpThoughtChunk, "content": acp.ContentBlock{Type: "text", Text: "Looking for the entry point."}, "_meta": sub})
		f.update(map[string]any{"sessionUpdate": acp.UpToolCall, "toolCallId": "read1", "title": "Read main.go", "kind": "read", "status": "in_progress",
			"_meta": map[string]any{"claudeCode": map[string]any{"toolName": "Read", "parentToolUseId": "task1"}}})
		f.sleep(200 * time.Millisecond)
		f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "read1", "status": "completed", "_meta": sub})
		f.update(map[string]any{"sessionUpdate": acp.UpAgentChunk, "content": acp.ContentBlock{Type: "text", Text: "main starts in main.go"}, "messageId": "sub-m", "_meta": sub})
		f.sleep(200 * time.Millisecond)
		f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": "task1", "status": "completed",
			"content": []map[string]any{{"type": "content", "content": acp.ContentBlock{Type: "text", Text: "`main` is in **main.go**."}}}})
		f.say("the subagent found it")
	case strings.HasPrefix(text, "think"):
		for i := 0; i < 6; i++ {
			if cancelled(cancel) {
				return
			}
			f.update(map[string]any{"sessionUpdate": acp.UpThoughtChunk, "content": acp.ContentBlock{Type: "text", Text: fmt.Sprintf("**step %d** — weighing it. ", i)}})
			f.sleep(300 * time.Millisecond)
		}
		f.say("thought it through")
	case strings.HasPrefix(text, "plan"):
		if !f.plan() {
			return
		}
	case strings.HasPrefix(text, "perm2"):
		if !f.permPair(mode) {
			return
		}
	case strings.Contains(text, "perm"):
		f.update(map[string]any{"sessionUpdate": acp.UpToolCall, "toolCallId": "t1", "title": "run ls", "kind": "execute", "rawInput": map[string]string{"cmd": "ls"}})
		if mode != "yolo" {
			var res acp.RequestPermissionResult
			err := f.conn.Call(acp.MRequestPermission, acp.RequestPermissionParams{SessionID: sid,
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
		if err := f.conn.Call(acp.MTermCreate, acp.TermCreateParams{SessionID: sid, Command: "sh",
			Args: []string{"-c", script + " 2>&1"}}, &cr); err != nil {
			f.say(label + " error: " + err.Error())
			break
		}
		var st acp.ExitStatus
		_ = f.conn.Call(acp.MTermWait, acp.TermIDParams{SessionID: sid, TerminalID: cr.TerminalID}, &st)
		var out acp.TermOutputResult
		_ = f.conn.Call(acp.MTermOutput, acp.TermIDParams{SessionID: sid, TerminalID: cr.TerminalID}, &out)
		_ = f.conn.Call(acp.MTermRelease, acp.TermIDParams{SessionID: sid, TerminalID: cr.TerminalID}, nil)
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
		if f.getenv("FAKE_API_KEY") != "" {
			key = "yes"
		}
		var rd acp.FsReadResult
		settings := "<none>"
		if err := f.conn.Call(acp.MFsRead, acp.FsReadParams{SessionID: sid, Path: f.getenv("HOME") + "/.claude/settings.json"}, &rd); err == nil {
			settings = strings.TrimSpace(rd.Content)
		}
		f.mu.Lock()
		model := f.model
		f.mu.Unlock()
		f.say(fmt.Sprintf("HOME=%s key=%s settings=%s model=%s", f.getenv("HOME"), key, settings, model))
	case strings.Contains(text, "write"):
		err := f.conn.Call(acp.MFsWrite, acp.FsWriteParams{SessionID: sid, Path: cwd + "/fake-wrote.txt", Content: "written by fakeacp\n"}, nil)
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
	f.flushSteers()
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
	f.mu.Lock()
	sid := f.sid
	f.mu.Unlock()
	const md = "# Fake plan\n\n1. **Read** the code\n2. Change `main.go`\n"
	body := []map[string]any{{"type": "content", "content": acp.ContentBlock{Type: "text", Text: md}}}
	raw := map[string]string{"plan": md, "planFilePath": "/tmp/fake-plan.md"}
	f.update(map[string]any{"sessionUpdate": acp.UpToolCall, "toolCallId": "plan1", "title": "Ready to code?", "kind": "switch_mode",
		"status": "pending", "content": body, "rawInput": raw, "_meta": map[string]any{"claudeCode": map[string]any{"toolName": "ExitPlanMode"}}})
	var res acp.RequestPermissionResult
	err := f.conn.Call(acp.MRequestPermission, map[string]any{"sessionId": sid,
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
