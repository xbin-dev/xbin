// fakeacp is a scripted ACP agent for tests and the UI harness (provider
// "fake", registered when xbind runs with XBIN_AGENT_FAKE=<path to this
// binary>). It speaks ACP v1 over stdio the way the real adapters do and
// plays a script chosen by words in the prompt:
//
//	(default)   one agent_message_chunk "echo: <text>", usage, end_turn
//	perm        a tool_call + session/request_permission (once/always/no);
//	            selected → the tool completes, cancelled → the turn ends cancelled
//	term        terminal/create `sh -c 'echo hi; printenv FAKE_API_KEY | wc -c'`,
//	            wait, output → a chunk "term: <output>"
//	run: <cmd>  terminal/create `sh -c '<cmd>'` the same way → "run: <output>"
//	env         a chunk "HOME=<home> key=<yes|no> settings=<~/.claude/settings.json via fs/read_text_file>"
//	write       fs/write_text_file <cwd>/fake-wrote.txt
//	slow        ten chunks 200 ms apart (cancel lands mid-turn)
//	burst       fifty one-character chunks back to back (the daemon coalesces them)
//	fail        the prompt fails with -32000 (auth required)
//	crash       exits 3 mid-turn
//
// Mode "yolo" skips the permission request. session/cancel ends the turn
// with stopReason cancelled.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/agent/acp"
)

type fake struct {
	conn   *acp.Conn
	mu     sync.Mutex
	mode   string
	cwd    string
	prompt json.RawMessage // in-flight prompt id
	cancel chan struct{}
}

func main() {
	f := &fake{mode: "ask"}
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
			AuthMethods: []acp.AuthMethod{{ID: "api-key", Name: "API key"}}}, nil
	case acp.MAuthenticate:
		return map[string]any{}, nil
	case acp.MSessionNew:
		var p acp.SessionNewParams
		_ = json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.cwd = p.Cwd
		f.mu.Unlock()
		return acp.SessionNewResult{SessionID: "fake-1", Modes: &acp.SessionModes{CurrentModeID: "ask",
			AvailableModes: []acp.ModeEntry{{ID: "ask", Name: "Ask"}, {ID: "yolo", Name: "Yolo"}}}}, nil
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
		text := ""
		if len(p.Prompt) > 0 {
			text = p.Prompt[0].Text
		}
		f.mu.Lock()
		f.prompt = m.ID
		f.cancel = make(chan struct{})
		f.mu.Unlock()
		go f.turn(text)
		return nil, nil
	}
	return nil, &acp.Error{Code: acp.ErrNotFound, Message: "method not found: " + m.Method}
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

func (f *fake) cancelled() bool {
	f.mu.Lock()
	c := f.cancel
	f.mu.Unlock()
	select {
	case <-c:
		return true
	default:
		return false
	}
}

func (f *fake) turn(text string) {
	f.mu.Lock()
	mode, cwd := f.mode, f.cwd
	f.mu.Unlock()
	switch {
	case strings.Contains(text, "fail"):
		f.mu.Lock()
		id := f.prompt
		f.prompt = nil
		f.mu.Unlock()
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
			if f.cancelled() {
				return
			}
			f.say(fmt.Sprintf("tick %d ", i))
			time.Sleep(200 * time.Millisecond)
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
		f.say(fmt.Sprintf("HOME=%s key=%s settings=%s", os.Getenv("HOME"), key, settings))
	case strings.Contains(text, "write"):
		err := f.conn.Call(acp.MFsWrite, acp.FsWriteParams{SessionID: "fake-1", Path: cwd + "/fake-wrote.txt", Content: "written by fakeacp\n"}, nil)
		if err != nil {
			f.say("write error: " + err.Error())
		} else {
			f.say("wrote fake-wrote.txt")
		}
	default:
		f.say("echo: " + text)
	}
	if f.cancelled() {
		return
	}
	f.update(map[string]any{"sessionUpdate": acp.UpUsage, "used": 42, "size": 1000})
	f.end("end_turn")
}

func strp(s string) *string { return &s }
