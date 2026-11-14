package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
)

// The ACP client is sdk/acp's and its behaviour is tested there
// (sdk/acp/*_test.go, against the same fake agent). The tests in this
// package cover only what xbind adds (driver.go): a prompt's files dropped
// through the host (_xbin/attach, prompt_test.go), the host's log lines
// (_xbin/log), the sign-in hint that names the tile's terminal, and the
// frames an agent sees from xbind's client, byte for byte (wire_test.go).

// fakeAgent is the agent side of ACP over in-process pipes — and the host's
// half of _xbin/attach: it answers the handshake and, on a prompt, plays a
// script — chunks, a tool call, a permission request it waits on, an update,
// usage, then the turn's end.
type fakeAgent struct {
	conn   *Conn
	mu     sync.Mutex
	prompt json.RawMessage                 // the in-flight prompt's request id
	script func(f *fakeAgent, text string) // what a prompt does
	sid    string
	model  string // the one config option
	// prompts with attachments (prompt_test.go)
	promptCaps *PromptCapabilities // advertised in initialize
	blocks     json.RawMessage     // the last session/prompt's content
	attached   []string            // _xbin/attach calls seen ("name:len")
	attachErr  bool                // answer _xbin/attach with an error (no host)
	attachGate chan struct{}       // answer _xbin/attach only once this is closed (a slow host)
	nprompts   int                 // session/prompt calls seen
}

// opts is the fake's config options: one select, "model".
func (f *fakeAgent) opts() []ConfigOption {
	f.mu.Lock()
	cur := f.model
	f.mu.Unlock()
	if cur == "" {
		cur = "m-default"
	}
	return []ConfigOption{{ID: "model", Name: "Model", Category: "model", Type: "select", CurrentValue: cur,
		Options: []ConfigValue{{Value: "m-default", Name: "Default"}, {Value: "m-fast", Name: "Fast"}}}}
}

func newFake(script func(f *fakeAgent, text string)) (*fakeAgent, agent.Spawner) {
	f := &fakeAgent{script: script}
	spawn := func(ctx context.Context, cfg agent.Config) (*agent.Process, error) {
		inR, inW := io.Pipe()   // client → agent
		outR, outW := io.Pipe() // agent → client
		f.conn = NewConn(inR, outW)
		f.conn.OnRequest = f.onRequest
		f.conn.OnNotify = f.onNotify
		go func() { _ = f.conn.Serve(); outW.Close() }()
		return &agent.Process{Stdin: inW, Stdout: outR, Kill: func() { inW.Close(); outW.Close() }}, nil
	}
	return f, spawn
}

func (f *fakeAgent) onRequest(m *Message) (any, *Error) {
	switch m.Method {
	case MInitialize:
		return InitializeResult{ProtocolVersion: 1, AgentInfo: &Info{Name: "fake-agent", Version: "1"}, AuthMethods: []AuthMethod{{ID: "api-key", Name: "API key"}},
			AgentCapabilities: &AgentCapabilities{LoadSession: true, PromptCapabilities: f.promptCaps}}, nil
	case MXbinAttach: // the host's half, stood in for (prompt_test.go)
		var p AttachParams
		_ = json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.attachErr {
			return nil, &Error{Code: ErrNotFound, Message: "method not found: " + m.Method}
		}
		f.attached = append(f.attached, fmt.Sprintf("%s:%d", p.Name, len(p.Data)))
		if gate := f.attachGate; gate != nil {
			go func() {
				<-gate
				_ = f.conn.Reply(m.ID, AttachResult{Path: "/tmp/xbin-attachments-1/" + p.Name}, nil)
			}()
			return nil, nil // answered when the gate opens
		}
		return AttachResult{Path: "/tmp/xbin-attachments-1/" + p.Name}, nil
	case MAuthenticate:
		return map[string]any{}, nil
	case MSessionNew:
		f.sid = "s-1"
		return SessionNewResult{SessionID: "s-1", Modes: &SessionModes{CurrentModeID: "ask", AvailableModes: []ModeEntry{{ID: "ask", Name: "Ask"}, {ID: "yolo", Name: "Yolo"}}},
			ConfigOptions: f.opts()}, nil
	case MSessionLoad:
		// resume: the earlier turns stream back as updates before the answer
		var p SessionLoadParams
		_ = json.Unmarshal(m.Params, &p)
		f.sid = p.SessionID
		f.update(map[string]any{"sessionUpdate": UpUserChunk, "content": ContentBlock{Type: "text", Text: "earlier: " + p.SessionID}})
		f.update(map[string]any{"sessionUpdate": UpAgentChunk, "content": ContentBlock{Type: "text", Text: "echo earlier"}, "messageId": "m0"})
		return SessionLoadResult{Modes: &SessionModes{CurrentModeID: "ask", AvailableModes: []ModeEntry{{ID: "ask", Name: "Ask"}, {ID: "yolo", Name: "Yolo"}}},
			ConfigOptions: f.opts()}, nil
	case MSessionSetConfig:
		var p SetConfigParams
		_ = json.Unmarshal(m.Params, &p)
		if p.ConfigID != "model" {
			return nil, &Error{Code: ErrInvalidParam, Message: "no such option"}
		}
		f.mu.Lock()
		f.model = p.Value
		f.mu.Unlock()
		opts := f.opts()
		f.update(map[string]any{"sessionUpdate": UpConfigOption, "configOptions": opts}) // as the real adapters do
		return SetConfigResult{ConfigOptions: opts}, nil
	case MSessionSetMode:
		return map[string]any{}, nil
	case MSessionPrompt:
		var p PromptParams
		_ = json.Unmarshal(m.Params, &p)
		var raw struct {
			Prompt json.RawMessage `json:"prompt"`
		}
		_ = json.Unmarshal(m.Params, &raw)
		f.mu.Lock()
		f.prompt = m.ID
		f.blocks = raw.Prompt
		f.nprompts++
		f.mu.Unlock()
		go f.script(f, p.Prompt[0].Text)
		return nil, nil // answered by the script
	}
	return nil, &Error{Code: ErrNotFound, Message: m.Method}
}

func (f *fakeAgent) onNotify(m *Message) {
	if m.Method == MSessionCancel {
		f.end("cancelled")
	}
}

func (f *fakeAgent) update(v any) {
	_ = f.conn.Notify(MSessionUpdate, map[string]any{"sessionId": f.sid, "update": v})
}

func (f *fakeAgent) end(reason string) {
	f.mu.Lock()
	id := f.prompt
	f.prompt = nil
	f.mu.Unlock()
	if id != nil {
		_ = f.conn.Reply(id, PromptResult{StopReason: reason}, nil)
	}
}

// askPermission sends session/request_permission and returns the outcome.
func (f *fakeAgent) askPermission(title string) (PermissionOutcome, error) {
	var res RequestPermissionResult
	err := f.conn.Call(MRequestPermission, RequestPermissionParams{SessionID: f.sid,
		ToolCall: ToolCallUpdate{ToolCallID: "t1", Title: strp(title), Kind: strp("execute")},
		Options:  []PermissionOption{{OptionID: "once", Name: "Once", Kind: "allow_once"}, {OptionID: "always", Name: "Always", Kind: "allow_always"}, {OptionID: "no", Name: "No", Kind: "reject_once"}}}, &res)
	return res.Outcome, err
}

func strp(s string) *string { return &s }

// the standard script: two chunks, a tool call, a permission request, the update, usage, end
func standard(f *fakeAgent, text string) {
	f.update(map[string]any{"sessionUpdate": UpAgentChunk, "content": ContentBlock{Type: "text", Text: "hello "}, "messageId": "m1"})
	f.update(map[string]any{"sessionUpdate": UpThoughtChunk, "content": ContentBlock{Type: "text", Text: "thinking"}})
	f.update(map[string]any{"sessionUpdate": UpToolCall, "toolCallId": "t1", "title": "run ls", "kind": "execute", "rawInput": map[string]string{"cmd": "ls"}})
	out, err := f.askPermission("run ls")
	if err != nil || out.Outcome != "selected" { // cancelled: the turn ends cancelled, as the protocol asks
		f.update(map[string]any{"sessionUpdate": UpToolCallUpdate, "toolCallId": "t1", "status": "failed"})
		f.end("cancelled")
		return
	}
	f.update(map[string]any{"sessionUpdate": UpToolCallUpdate, "toolCallId": "t1", "status": "completed", "content": []map[string]any{{"type": "content", "content": ContentBlock{Type: "text", Text: "a b"}}}})
	f.update(map[string]any{"sessionUpdate": UpAgentChunk, "content": ContentBlock{Type: "text", Text: "world"}, "messageId": "m1"})
	f.update(map[string]any{"sessionUpdate": UpUsage, "used": 120, "size": 200000})
	f.update(map[string]any{"sessionUpdate": "something_new", "x": 1})
	f.end("end_turn")
}

func rig(t *testing.T, script func(f *fakeAgent, text string), mode string, env ...string) (*Client, *fakeAgent, *agent.Permissions, []string) {
	t.Helper()
	return rigWith(t, script, mode, nil, env...)
}

// rigWith is rig with the fake set up before the handshake.
func rigWith(t *testing.T, script func(f *fakeAgent, text string), mode string, setup func(f *fakeAgent), env ...string) (*Client, *fakeAgent, *agent.Permissions, []string) {
	t.Helper()
	f, spawn := newFake(script)
	if setup != nil {
		setup(f)
	}
	perms := agent.NewPermissions()
	var logs []string
	c := New()
	cfg := agent.Config{Provider: agent.Provider{ID: "fake", Login: "fake-login"}, Mode: mode, Cwd: "/w/apps/x",
		Env: env, Spawn: spawn, Perms: perms, Version: "test", Log: func(s string) { logs = append(logs, s) }, Meta: map[string]string{"tile": "apps/x"}}
	if err := c.Start(context.Background(), cfg); err != nil {
		t.Fatalf("start: %v", err)
	}
	return c, f, perms, logs
}

// collect drains events until a predicate holds or the timeout passes.
func collect(t *testing.T, c *Client, until func(e agent.Event) bool) []agent.Event {
	t.Helper()
	var out []agent.Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-c.Events():
			if !ok {
				return out
			}
			out = append(out, e)
			if until(e) {
				return out
			}
		case <-deadline:
			t.Fatalf("timed out; events so far: %s", types(out))
		}
	}
}

func types(es []agent.Event) string {
	var s []string
	for _, e := range es {
		s = append(s, e.Type)
	}
	return strings.Join(s, " ")
}

func data(e agent.Event) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(e.Data, &m)
	return m
}

func TestAuthErrorNamesTheLogin(t *testing.T) {
	c, _, _, _ := rig(t, func(f *fakeAgent, text string) {
		f.mu.Lock()
		id := f.prompt
		f.prompt = nil
		f.mu.Unlock()
		_ = f.conn.Reply(id, nil, &Error{Code: ErrAuthRequired, Message: "Please run /login"})
	}, "")
	defer c.Close()
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusIdle })
	_ = c.Send(context.Background(), "hi")
	es := collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusError })
	d := data(es[len(es)-1])
	detail := d["detail"].(string)
	if !strings.Contains(detail, "fake-login") || !strings.Contains(detail, "apps/x") || strings.Contains(detail, "vault") {
		t.Fatalf("the auth error points at the home login, not a vault key: %v", detail)
	}
	for _, e := range es {
		if e.Type == agent.EvTurnEnd && data(e)["stopReason"] != "error" {
			t.Fatal("turn.end stopReason error")
		}
	}
}

// The host's _xbin/log lines (a spawn failure, a line the agent printed
// that was not a frame) land in the session's log; they are the host's, so
// nothing reports them as an ignored notification.
func TestHostLogLines(t *testing.T) {
	f, spawn := newFake(echoEnd)
	var mu sync.Mutex
	var logs []string
	c := New()
	cfg := agent.Config{Provider: agent.Provider{ID: "fake"}, Cwd: "/w/apps/x", Spawn: spawn, Perms: agent.NewPermissions(),
		Log: func(s string) { mu.Lock(); logs = append(logs, s); mu.Unlock() }}
	if err := c.Start(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	collect(t, c, idle)
	_ = f.conn.Notify(MXbinLog, LogParams{Text: "agent stdout: not json at all"})
	// frames are handled in order: once this update is out, the log line was
	f.update(map[string]any{"sessionUpdate": UpCurrentMode, "currentModeId": "yolo"})
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["currentMode"] == "yolo" })
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(logs, "|") != "agent stdout: not json at all" {
		t.Fatalf("the session log: %q", logs)
	}
}
