package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
)

// fakeAgent is the agent side of ACP over in-process pipes: it answers the
// handshake and, on a prompt, plays a script — chunks, a tool call, a
// permission request it waits on, an update, usage, then the turn's end.
type fakeAgent struct {
	conn   *Conn
	mu     sync.Mutex
	prompt json.RawMessage // the in-flight prompt's request id
	mode   string
	authed bool
	script func(f *fakeAgent, text string) // what a prompt does
	sid    string
	model  string          // the one config option
	sets   []string        // set_config_option calls seen ("id=value")
	noLoad bool            // don't advertise loadSession (resume unsupported)
	caps   json.RawMessage // initialize's clientCapabilities
	meta   json.RawMessage // session/new's _meta
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
	f := &fakeAgent{script: script, mode: "ask"}
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
		var p struct {
			Caps json.RawMessage `json:"clientCapabilities"`
		}
		_ = json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.caps = p.Caps
		f.mu.Unlock()
		return InitializeResult{ProtocolVersion: 1, AgentInfo: &Info{Name: "fake-agent", Version: "1"}, AuthMethods: []AuthMethod{{ID: "api-key", Name: "API key"}},
			AgentCapabilities: &AgentCapabilities{LoadSession: !f.noLoad}}, nil
	case MAuthenticate:
		f.mu.Lock()
		f.authed = true
		f.mu.Unlock()
		return map[string]any{}, nil
	case MSessionNew:
		var p struct {
			Meta json.RawMessage `json:"_meta"`
		}
		_ = json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.meta = p.Meta
		f.mu.Unlock()
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
		f.mu.Lock()
		f.sets = append(f.sets, p.ConfigID+"="+p.Value)
		if p.ConfigID == "model" {
			f.model = p.Value
		}
		f.mu.Unlock()
		if p.ConfigID != "model" {
			return nil, &Error{Code: ErrInvalidParam, Message: "no such option"}
		}
		opts := f.opts()
		f.update(map[string]any{"sessionUpdate": UpConfigOption, "configOptions": opts}) // as the real adapters do
		return SetConfigResult{ConfigOptions: opts}, nil
	case MSessionSetMode:
		var p SetModeParams
		_ = json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.mode = p.ModeID
		f.mu.Unlock()
		return map[string]any{}, nil
	case MSessionPrompt:
		var p PromptParams
		_ = json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.prompt = m.ID
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
	f, spawn := newFake(script)
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

func TestHandshakeAuthAndMode(t *testing.T) {
	c, f, _, _ := rig(t, standard, "yolo")
	defer c.Close()
	es := collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusIdle })
	if types(es) != "status status" {
		t.Fatalf("handshake events: %s", types(es))
	}
	f.mu.Lock()
	authed, mode := f.authed, f.mode
	f.mu.Unlock()
	if authed {
		t.Fatal("authenticate must not be called — the CLI authenticates from its own $HOME")
	}
	if mode != "yolo" || c.Mode() != "yolo" {
		t.Fatalf("mode: agent %q client %q", mode, c.Mode())
	}
	if d := data(es[1]); d["currentMode"] != "yolo" || d["modes"] == nil {
		t.Fatalf("idle status carries modes: %v", d)
	}
}

func TestTurnWithPermission(t *testing.T) {
	c, _, perms, _ := rig(t, standard, "")
	defer c.Close()
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusIdle })
	if err := c.Send(context.Background(), "list files"); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(context.Background(), "again"); err == nil {
		t.Fatal("a second prompt while a turn runs must fail")
	}
	es := collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvPermissionRequest })
	want := "message.delta status message.delta thought.delta tool.call permission.request"
	if types(es) != want {
		t.Fatalf("up to the request: %s\nwant %s", types(es), want)
	}
	if d := data(es[0]); d["role"] != "user" || d["text"] != "list files" {
		t.Fatalf("the prompt is logged as a user delta: %v", d)
	}
	if d := data(es[4]); d["status"] != "pending" || d["kind"] != "execute" {
		t.Fatalf("tool.call: %v", d)
	}
	req := data(es[5])
	if req["pid"] != "p1" || perms.Count() != 1 {
		t.Fatalf("pending: %v", req)
	}
	// status waiting follows the request
	es = collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus })
	if data(es[len(es)-1])["status"] != agent.StatusWaiting {
		t.Fatal("no waiting status")
	}
	res, err := perms.Resolve("p1", "", agent.AllowOnce, "user:dev1")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RespondPermission(res); err != nil {
		t.Fatal(err)
	}
	es = collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvTurnEnd })
	got := types(es)
	for _, w := range []string{"permission.resolved", "tool.update", "message.delta", "status", "turn.end"} {
		if !strings.Contains(got, w) {
			t.Fatalf("after the answer: %s (missing %s)", got, w)
		}
	}
	end := data(es[len(es)-1])
	if end["stopReason"] != "end_turn" || end["turn"] != float64(1) || end["usage"] == nil {
		t.Fatalf("turn.end: %v", end)
	}
	if u := end["usage"].(map[string]any); u["used"] != float64(120) {
		t.Fatalf("usage: %v", u)
	}
	es = collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus })
	if data(es[len(es)-1])["status"] != agent.StatusIdle {
		t.Fatal("idle after the turn")
	}
	// the second turn: the "always" rule auto-resolves the same request
	perms2 := perms
	if err := c.Send(context.Background(), "once more"); err != nil {
		t.Fatal(err)
	}
	es = collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvPermissionRequest })
	_ = perms2
	res, err = perms.Resolve("p2", "", agent.AllowAlways, "user:dev1")
	if err != nil {
		t.Fatal(err)
	}
	c.RespondPermission(res)
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvTurnEnd })
	if err := c.Send(context.Background(), "third"); err != nil {
		t.Fatal(err)
	}
	es = collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvTurnEnd })
	got = types(es)
	if !strings.Contains(got, "permission.request permission.resolved") || perms.Count() != 0 {
		t.Fatalf("auto-allowed third turn: %s", got)
	}
	for _, e := range es {
		if e.Type == agent.EvPermissionResolved && data(e)["by"] != "auto" {
			t.Fatalf("resolved by %v, want auto", data(e)["by"])
		}
	}
}

func TestCancelResolvesPermissionAndEndsTurn(t *testing.T) {
	c, _, perms, _ := rig(t, standard, "")
	defer c.Close()
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusIdle })
	_ = c.Send(context.Background(), "go")
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvPermissionRequest })
	if err := c.Cancel(); err != nil {
		t.Fatal(err)
	}
	es := collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvTurnEnd })
	got := types(es)
	if !strings.Contains(got, "permission.resolved") || !strings.Contains(got, "tool.update") {
		t.Fatalf("cancel: %s", got)
	}
	for _, e := range es {
		if e.Type == agent.EvPermissionResolved && data(e)["by"] != "cancel" {
			t.Fatalf("resolved by %v", data(e)["by"])
		}
		if e.Type == agent.EvToolUpdate && data(e)["status"] != "cancelled" && data(e)["status"] != "failed" {
			t.Fatalf("tool marked %v", data(e)["status"])
		}
	}
	if data(es[len(es)-1])["stopReason"] != "cancelled" || perms.Count() != 0 {
		t.Fatalf("turn end: %v", data(es[len(es)-1]))
	}
	if err := c.Cancel(); err != nil {
		t.Fatal("cancel when idle is a no-op")
	}
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

// loginOf returns a status event's login map (nil when it carries none).
func loginOf(e agent.Event) map[string]any {
	lg, _ := data(e)["login"].(map[string]any)
	return lg
}

// A signed-out agent pushes _auth/status_update{kind:none} and fails the turn
// with -32000; the client marks the status with login{needed,provider,command}
// so the UI can offer a one-click sign-in. A later successful turn clears it.
func TestSignedOutLoginInStatus(t *testing.T) {
	var n int
	c, _, _, _ := rig(t, func(f *fakeAgent, text string) {
		f.mu.Lock()
		id := f.prompt
		f.prompt = nil
		n++
		first := n == 1
		f.mu.Unlock()
		if first {
			_ = f.conn.Notify(MAuthStatus, map[string]any{"authStatus": map[string]any{"kind": "none"}})
			_ = f.conn.Reply(id, nil, &Error{Code: ErrAuthRequired, Message: "Please run /login"})
			return
		}
		f.update(map[string]any{"sessionUpdate": UpAgentChunk, "content": ContentBlock{Type: "text", Text: "ok"}, "messageId": "m1"})
		_ = f.conn.Reply(id, PromptResult{StopReason: "end_turn"}, nil)
	}, "")
	defer c.Close()
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusIdle })

	if err := c.Send(context.Background(), "hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	es := collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvTurnEnd }) // turn 1 done
	var lg map[string]any
	for _, e := range es {
		if l := loginOf(e); l != nil {
			lg = l
		}
	}
	if lg == nil || lg["needed"] != true || lg["command"] != "fake-login" {
		t.Fatalf("a signed-out status should carry the sign-in command: %v", lg)
	}

	// a successful turn clears the signed-out flag: the idle status carries no login
	if err := c.Send(context.Background(), "hi again"); err != nil {
		t.Fatalf("second send: %v", err)
	}
	es = collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusIdle })
	if loginOf(es[len(es)-1]) != nil {
		t.Fatalf("a successful turn should clear login: %v", data(es[len(es)-1]))
	}
}

func TestAgentExitAndBadLines(t *testing.T) {
	f, spawn := newFake(standard)
	perms := agent.NewPermissions()
	var logs []string
	var lmu sync.Mutex
	c := New()
	cfg := agent.Config{Provider: agent.Provider{ID: "fake"}, Cwd: "/w", Spawn: spawn, Perms: perms,
		Log: func(s string) { lmu.Lock(); logs = append(logs, s); lmu.Unlock() }}
	if err := c.Start(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusIdle })
	// a junk line from the agent is logged and skipped
	f.conn.wmu.Lock()
	_, _ = f.conn.w.Write([]byte("not json at all\n"))
	f.conn.wmu.Unlock()
	f.update(map[string]any{"sessionUpdate": UpCurrentMode, "currentModeId": "yolo"})
	es := collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["currentMode"] == "yolo" })
	if len(es) == 0 {
		t.Fatal("mode update lost after the bad line")
	}
	lmu.Lock()
	sawBad := false
	for _, l := range logs {
		if strings.Contains(l, "not json") {
			sawBad = true
		}
	}
	lmu.Unlock()
	if !sawBad {
		t.Fatal("the bad line was not logged")
	}
	// the agent dies: status exited, the channel closes
	f.conn.w.(*io.PipeWriter).Close()
	es = collect(t, c, func(e agent.Event) bool { return false })
	if len(es) == 0 || es[len(es)-1].Type != agent.EvStatus || data(es[len(es)-1])["status"] != agent.StatusExited {
		t.Fatalf("after exit: %s", types(es))
	}
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed")
	}
	if err := c.Send(context.Background(), "x"); err == nil {
		t.Fatal("send after exit must fail")
	}
}

func TestRPCCodec(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":3,"method":"x","params":{}}`), &m); err != nil || !m.IsRequest() || m.IsNotification() {
		t.Fatal("request")
	}
	m = Message{}
	if err := json.Unmarshal([]byte(`{"jsonrpc":"2.0","method":"n"}`), &m); err != nil || !m.IsNotification() {
		t.Fatal("notification")
	}
	m = Message{}
	if err := json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":"a","result":null}`), &m); err != nil || !m.IsResponse() {
		t.Fatal("response")
	}
	d := NewDecoder(strings.NewReader("\n{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"a\"}\r\ngarbage\n{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}"))
	if m, err := d.Next(); err != nil || m.Method != "a" {
		t.Fatalf("first: %v %v", m, err)
	}
	if _, err := d.Next(); !errors.Is(err, ErrBadLine) {
		t.Fatalf("garbage: %v", err)
	}
	if m, err := d.Next(); err != nil || !m.IsResponse() {
		t.Fatalf("last (no trailing newline): %v %v", m, err)
	}
	if _, err := d.Next(); !errors.Is(err, io.EOF) {
		t.Fatal("eof")
	}
}

// The agent's config options (model, effort, …): carried on the idle
// status, a requested one applied at start, settable later, refreshed by
// the agent's own updates; an option the agent does not offer is refused
// and never sent.
func TestConfigOptions(t *testing.T) {
	f, spawn := newFake(standard)
	c := New()
	cfg := agent.Config{Provider: agent.Provider{ID: "fake", Login: "fake-login"}, Cwd: "/w", Spawn: spawn, Perms: agent.NewPermissions(),
		Options: map[string]string{"model": "m-fast", "nope": "x"}, Log: func(string) {}}
	if err := c.Start(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	es := collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusIdle })
	idle := data(es[len(es)-1])
	opts, _ := idle["options"].([]any)
	if len(opts) != 1 {
		t.Fatalf("the idle status carries the options: %v", idle)
	}
	if o := opts[0].(map[string]any); o["id"] != "model" || o["currentValue"] != "m-fast" {
		t.Fatalf("the requested model was applied at start: %v", o)
	}
	if idle["agent"] == nil {
		t.Fatalf("the idle status names the agent: %v", idle)
	}
	f.mu.Lock()
	sets := append([]string(nil), f.sets...)
	f.mu.Unlock()
	if len(sets) != 1 || sets[0] != "model=m-fast" {
		t.Fatalf("set calls %v: only offered options are sent", sets)
	}
	if err := c.SetOption(context.Background(), "model", "m-default"); err != nil {
		t.Fatal(err)
	}
	es = collect(t, c, func(e agent.Event) bool {
		os, _ := data(e)["options"].([]any)
		return e.Type == agent.EvStatus && len(os) > 0 && os[0].(map[string]any)["currentValue"] == "m-default"
	})
	if len(es) == 0 {
		t.Fatal("no status with the new value")
	}
	if err := c.SetOption(context.Background(), "nope", "x"); err == nil {
		t.Fatal("an option the agent does not offer was accepted")
	}
}

// A session rule is never a wildcard and never recorded on a fallback.
func TestPermissionRuleScope(t *testing.T) {
	perms := agent.NewPermissions()
	// no kind, no title: allow_always must not remember "everything"
	pd, _ := perms.Request(agent.ToolCallRef{ID: "t0"}, []agent.PermissionOption{{OptionID: "always", Kind: agent.AllowAlways}}, nil)
	if _, err := perms.Resolve(pd.PID, "", agent.AllowAlways, "u"); err != nil {
		t.Fatal(err)
	}
	if _, auto := perms.Request(agent.ToolCallRef{ID: "t1", Kind: "execute", Title: "rm -rf /"}, []agent.PermissionOption{{OptionID: "always", Kind: agent.AllowAlways}}, nil); auto != nil {
		t.Fatal("a rule from a kind-less, title-less call auto-approved an unrelated call")
	}
	// the agent offered no allow_always: the fallback allows once and remembers nothing
	perms = agent.NewPermissions()
	pd, _ = perms.Request(agent.ToolCallRef{ID: "t2", Kind: "execute", Title: "ls"}, []agent.PermissionOption{{OptionID: "once", Kind: agent.AllowOnce}}, nil)
	if res, err := perms.Resolve(pd.PID, "", agent.AllowAlways, "u"); err != nil || res.OptionID != "once" {
		t.Fatalf("fallback: %+v %v", res, err)
	}
	if _, auto := perms.Request(agent.ToolCallRef{ID: "t3", Kind: "execute", Title: "ls"}, []agent.PermissionOption{{OptionID: "once", Kind: agent.AllowOnce}}, nil); auto != nil {
		t.Fatal("a fallback to allow_once recorded a rule")
	}
}

// A spawn that fails (the host killed while starting) still ends the event
// stream — with the error status — so the session's pump tears down instead
// of waiting forever (it used to: the session never left the directory).
func TestSpawnFailureEndsEvents(t *testing.T) {
	c := New()
	err := c.Start(context.Background(), agent.Config{Provider: agent.Provider{ID: "fake"}, Perms: agent.NewPermissions(),
		Spawn: func(context.Context, agent.Config) (*agent.Process, error) {
			return nil, errors.New("write |1: broken pipe")
		}})
	if err == nil {
		t.Fatal("Start succeeded")
	}
	var last agent.Event
	timeout := time.After(2 * time.Second)
	for {
		select {
		case e, ok := <-c.Events():
			if !ok {
				if d := data(last); last.Type != agent.EvStatus || d["status"] != agent.StatusError || !strings.Contains(fmt.Sprint(d["detail"]), "broken pipe") {
					t.Fatalf("last event: %s %v", last.Type, d)
				}
				return
			}
			last = e
		case <-timeout:
			t.Fatal("the event stream never closed")
		}
	}
}

// Slash commands ride a status event when the agent advertises them, and
// every idle after — normalized to {name, description, hint}.
func TestSlashCommands(t *testing.T) {
	c, _, _, _ := rig(t, func(f *fakeAgent, text string) {
		f.update(map[string]any{"sessionUpdate": UpAvailableCmds, "availableCommands": []map[string]any{
			{"name": "review", "description": "Review changes", "input": map[string]string{"hint": "what to focus on"}},
			{"name": "compact", "description": "Compact the conversation"}, {"name": ""}}})
		f.end("end_turn")
	}, "")
	defer c.Close()
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusIdle })
	if err := c.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	want := `[{"name":"review","description":"Review changes","hint":"what to focus on"},{"name":"compact","description":"Compact the conversation"}]`
	var seen, idle bool
	collect(t, c, func(e agent.Event) bool {
		if e.Type != agent.EvStatus {
			return false
		}
		var d struct {
			Status   string
			Commands json.RawMessage
		}
		_ = json.Unmarshal(e.Data, &d)
		if string(d.Commands) == want {
			if seen && d.Status == agent.StatusIdle {
				idle = true
			}
			seen = true
		}
		return idle
	})
}

// A question (elicitation/create, form mode — Claude's AskUserQuestion) is
// held like a permission: an elicitation.request event, the session waits,
// the first answer goes back as the reply; a second answer finds nothing;
// cancelling the turn answers "cancel". The client advertises form support.
func TestElicitation(t *testing.T) {
	answers := make(chan string, 4)
	c, f, _, _ := rig(t, func(f *fakeAgent, text string) {
		var res struct {
			Action  string          `json:"action"`
			Content json.RawMessage `json:"content"`
		}
		err := f.conn.Call(MElicitCreate, map[string]any{"mode": "form", "sessionId": f.sid, "toolCallId": "ask1", "message": "Pick one",
			"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{"question_0": map[string]any{"type": "string",
				"oneOf": []any{map[string]string{"const": "A", "title": "A"}}}}}}, &res)
		if err != nil {
			answers <- "error: " + err.Error()
		} else {
			answers <- res.Action + " " + string(res.Content)
		}
		f.end("end_turn")
	}, "")
	defer c.Close()
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusIdle })
	f.mu.Lock()
	caps := string(f.caps)
	f.mu.Unlock()
	if !strings.Contains(caps, `"elicitation":{"form":{}}`) {
		t.Fatalf("form elicitation not advertised: %s", caps)
	}
	if err := c.Send(context.Background(), "ask"); err != nil {
		t.Fatal(err)
	}
	var eid string
	es := collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusWaiting })
	for _, e := range es {
		if e.Type == agent.EvElicitRequest {
			d := data(e)
			eid, _ = d["eid"].(string)
			if d["toolCallId"] != "ask1" || d["message"] != "Pick one" || d["schema"] == nil {
				t.Fatalf("request: %v", d)
			}
		}
	}
	if eid == "" {
		t.Fatalf("no elicitation.request: %s", types(es))
	}
	if err := c.RespondElicitation(eid, "maybe", nil, "u"); err == nil {
		t.Fatal("an unknown action must fail")
	}
	if err := c.RespondElicitation(eid, "accept", json.RawMessage(`{"question_0":"A"}`), "user:a"); err != nil {
		t.Fatal(err)
	}
	if got := <-answers; got != `accept {"question_0":"A"}` {
		t.Fatalf("the agent got %q", got)
	}
	if err := c.RespondElicitation(eid, "decline", nil, "user:b"); !errors.Is(err, agent.ErrNoElicitation) {
		t.Fatalf("second answer: %v", err)
	}
	es = collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvTurnEnd })
	if types(es) == "" || !strings.Contains(types(es), "elicitation.resolved") {
		t.Fatalf("events after the answer: %s", types(es))
	}
	// cancelling the turn answers a pending question "cancel"
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusIdle })
	if err := c.Send(context.Background(), "ask again"); err != nil {
		t.Fatal(err)
	}
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvElicitRequest })
	if err := c.Cancel(); err != nil {
		t.Fatal(err)
	}
	if got := <-answers; got != "cancel " {
		t.Fatalf("cancel answered %q", got)
	}
}

// The client asks for the adapter extensions it renders (terminal output in
// the tool call's _meta), and a provider's SessionMeta rides session/new —
// Claude's summarized thinking display, without which no thought streams.
func TestClientAndSessionMeta(t *testing.T) {
	f, spawn := newFake(standard)
	claude, _ := agent.Lookup("claude")
	c := New()
	cfg := agent.Config{Provider: agent.Provider{ID: "fake", SessionMeta: claude.SessionMeta}, Cwd: "/w/apps/x", Spawn: spawn,
		Perms: agent.NewPermissions(), Version: "test", Log: func(string) {}}
	if err := c.Start(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusIdle })
	f.mu.Lock()
	caps, meta := string(f.caps), string(f.meta)
	f.mu.Unlock()
	if !strings.Contains(caps, `"terminal_output":true`) || !strings.Contains(caps, `"terminal_output_delta":true`) || !strings.Contains(caps, `"subagent-transcript":true`) {
		t.Fatalf("clientCapabilities._meta: %s", caps)
	}
	if meta != `{"claudeCode":{"options":{"thinking":{"display":"summarized","type":"adaptive"}}}}` {
		t.Fatalf("session/new _meta: %s", meta)
	}
}

// A plan approval (switch_mode — Claude's ExitPlanMode, Codex's plan review)
// is never scoped: its allow_always options are modes ("clear context and
// use auto mode"), so answering one records no rule and the next plan is
// asked again, never approved unseen with the first allow_always option.
func TestPlanApprovalNeverScoped(t *testing.T) {
	perms := agent.NewPermissions()
	plan := agent.ToolCallRef{ID: "t1", Name: "ExitPlanMode", Kind: agent.KindSwitchMode, Title: "Approve Plan"}
	opts := []agent.PermissionOption{{OptionID: "exit-plan-clear-auto", Kind: agent.AllowAlways}, {OptionID: "auto", Kind: agent.AllowAlways},
		{OptionID: "exit-plan-default", Kind: agent.AllowOnce}, {OptionID: "reject", Kind: agent.RejectOnce}}
	if plan.Rule() {
		t.Fatal("a switch_mode call must not be scopable")
	}
	pd, _ := perms.Request(plan, opts, nil)
	if res, err := perms.Resolve(pd.PID, "auto", "", "u"); err != nil || res.OptionID != "auto" {
		t.Fatalf("resolve: %+v %v", res, err)
	}
	plan.ID = "t2"
	if pd, auto := perms.Request(plan, opts, nil); auto != nil || pd == nil {
		t.Fatalf("the second plan was auto-answered: %+v", auto)
	}
}

// The adapters' tool-call _meta lifts into plain event fields.
func TestToolExtras(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name string
		u    ToolCallUpdate
		want map[string]any
	}{
		{"claude bash: name, description as label, terminal output + exit",
			ToolCallUpdate{ToolCallID: "a", RawInput: json.RawMessage(`{"command":"ls","description":"List files"}`),
				Meta: json.RawMessage(`{"claudeCode":{"toolName":"Bash"},"terminal_output":{"terminal_id":"a","data":"x\n"},"terminal_exit":{"terminal_id":"a","exit_code":2,"signal":null}}`)},
			map[string]any{"name": "Bash", "label": "List files", "output": "x\n", "exitCode": 2}},
		{"claude subagent child + the Task call itself",
			ToolCallUpdate{ToolCallID: "b", Name: str("Task"), Meta: json.RawMessage(`{"claudeCode":{"toolName":"Task","parentToolUseId":"p0","title":"Explore the repo"}}`)},
			map[string]any{"name": "Task", "label": "Explore the repo", "parent": "p0", "subagent": true}},
		{"codex: streamed delta, plan review",
			ToolCallUpdate{ToolCallID: "c", Meta: json.RawMessage(`{"terminal_output_delta":{"terminal_id":"c","data":"chunk"},"codex":{"kind":"plan_review"}}`)},
			map[string]any{"outputDelta": "chunk", "planReview": true}},
		{"codex: a finished command's formatted output",
			ToolCallUpdate{ToolCallID: "d", RawOutput: json.RawMessage(`{"formatted_output":"done","exit_code":0}`)},
			map[string]any{"output": "done"}},
		{"an unknown _meta shape is ignored",
			ToolCallUpdate{ToolCallID: "e", Meta: json.RawMessage(`{"claudeCode":"weird","terminal_output":7}`)},
			map[string]any{}},
	}
	for _, c := range cases {
		d := map[string]any{}
		addToolExtras(d, c.u)
		got, _ := json.Marshal(d)
		want, _ := json.Marshal(c.want)
		if string(got) != string(want) {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, want)
		}
	}
	if d := withParent(map[string]any{"text": "hi"}, json.RawMessage(`{"_meta":{"claudeCode":{"parentToolUseId":"p9"}}}`)); d["parent"] != "p9" {
		t.Fatalf("withParent: %+v", d)
	}
}

// session_info_update titles the session; a non-text chunk leaves a
// placeholder; "mode" is settable through SetOption even without a mode
// config option (session/set_mode).
func TestTitleModeAndPlaceholders(t *testing.T) {
	c, f, _, _ := rig(t, standard, "")
	defer c.Close()
	collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusIdle })
	f.update(map[string]any{"sessionUpdate": UpSessionInfo, "title": "Fix the build"})
	es := collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["title"] == "Fix the build" })
	if len(es) == 0 {
		t.Fatal("no title status")
	}
	f.update(map[string]any{"sessionUpdate": UpAgentChunk, "content": map[string]any{"type": "image", "mimeType": "image/png", "data": "AAAA"}})
	f.update(map[string]any{"sessionUpdate": UpAgentChunk, "content": map[string]any{"type": "resource_link", "uri": "file:///w/a.go", "name": "a.go"}})
	es = collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvMessageDelta && data(e)["text"] == "[link: a.go]" })
	if len(es) < 2 || data(es[len(es)-2])["text"] != "[image]" {
		t.Fatalf("placeholders: %v", types(es))
	}
	if err := c.SetOption(context.Background(), "mode", "yolo"); err != nil {
		t.Fatal(err)
	}
	es = collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["currentMode"] == "yolo" })
	if len(es) == 0 || c.Mode() != "yolo" {
		t.Fatalf("mode via SetOption: %s / %s", types(es), c.Mode())
	}
	f.mu.Lock()
	mode := f.mode
	f.mu.Unlock()
	if mode != "yolo" {
		t.Fatal("set_mode did not reach the agent")
	}
	if err := c.SetOption(context.Background(), "mode", "nope"); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
}

// Resume: with ResumeID the handshake calls session/load instead of new; the
// agent replays the earlier turns as updates (they land in the log before
// idle), the session id is the reopened one, and Session() says it is
// loadable. An agent without loadSession refuses with ErrResumeUnsupported.
func TestSessionLoadResume(t *testing.T) {
	_, spawn := newFake(standard)
	c := New()
	cfg := agent.Config{Provider: agent.Provider{ID: "fake", Login: "fake-login"}, Cwd: "/w", ResumeID: "s-old", Spawn: spawn,
		Perms: agent.NewPermissions(), Version: "test", Meta: map[string]string{"tile": "apps/x"}}
	if err := c.Start(context.Background(), cfg); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Close()
	es := collect(t, c, func(e agent.Event) bool { return e.Type == agent.EvStatus && data(e)["status"] == agent.StatusIdle })
	replayed := false
	for _, e := range es {
		txt, _ := data(e)["text"].(string)
		if e.Type == agent.EvMessageDelta && data(e)["role"] == "user" && strings.Contains(txt, "earlier: s-old") {
			replayed = true
		}
	}
	if !replayed {
		t.Fatalf("the earlier turns replay into the log before idle: %s", types(es))
	}
	if id, ok := c.Session(); id != "s-old" || !ok {
		t.Fatalf("Session() = %q,%v; want the reopened id, loadable", id, ok)
	}

	// an agent that did not advertise loadSession cannot resume
	f2, spawn2 := newFake(standard)
	f2.noLoad = true
	cfg.Spawn = spawn2
	if err := New().Start(context.Background(), cfg); err != agent.ErrResumeUnsupported {
		t.Fatalf("resume on a non-loadable agent: %v", err)
	}
}
