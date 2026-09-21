package acp

import (
	"context"
	"encoding/json"
	"errors"
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
	model  string   // the one config option
	sets   []string // set_config_option calls seen ("id=value")
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
		return InitializeResult{ProtocolVersion: 1, AgentInfo: &Info{Name: "fake-agent", Version: "1"}, AuthMethods: []AuthMethod{{ID: "api-key", Name: "API key"}}}, nil
	case MAuthenticate:
		f.mu.Lock()
		f.authed = true
		f.mu.Unlock()
		return map[string]any{}, nil
	case MSessionNew:
		f.sid = "s-1"
		return SessionNewResult{SessionID: "s-1", Modes: &SessionModes{CurrentModeID: "ask", AvailableModes: []ModeEntry{{ID: "ask", Name: "Ask"}, {ID: "yolo", Name: "Yolo"}}},
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
