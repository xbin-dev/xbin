package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// lastFrame is the last frame the peer received with this method.
func (p *peer) lastFrame(method string) *Message {
	var out *Message
	for _, m := range p.seen() {
		if m.Method == method {
			out = m
		}
	}
	return out
}

// Steering: detected from initialize's _meta.steering.supported (the
// adapters' shape), sent with idleBehavior promptRequired, each outcome
// reported; idle asks nothing; an agent without it is refused.
func TestSteer(t *testing.T) {
	p := newPeer(t)
	p.steering = true
	c, _, err := p.start(ClientOptions{}, NewPermissions(), 0, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	collect(t, c, idle)
	if !c.State().Steering {
		t.Fatal("steering not detected")
	}
	ctx := context.Background()
	if out, err := c.Steer(ctx, Prompt{Text: "idle"}); err != nil || out != SteerPromptRequired || p.lastFrame(MSessionSteering) != nil {
		t.Fatalf("idle: %q %v (the agent must not be asked)", out, err)
	}
	_ = c.Prompt(ctx, Prompt{Text: "go"})
	collect(t, c, isText("working"))

	out, err := c.Steer(ctx, Prompt{Text: "use tabs"})
	if err != nil || out != SteerInjected {
		t.Fatalf("in a turn: %q %v", out, err)
	}
	var sp map[string]any
	_ = json.Unmarshal(p.lastFrame(MSessionSteering).Params, &sp)
	if b, _ := json.Marshal(sp["_meta"]); string(b) != `{"steering":{"idleBehavior":"promptRequired"}}` || sp["sessionId"] != "s-1" {
		t.Fatalf("steer params %v", sp)
	}
	es := collect(t, c, func(e Event) bool { return e.Type == EvMessageDelta && data(e)["role"] == "user" })
	var echo *Event
	for i, e := range es {
		if e.Type == EvMessageDelta && data(e)["role"] == "user" {
			echo = &es[i]
		}
	}
	if echo == nil || data(*echo)["steered"] != true || data(*echo)["text"] != "use tabs" || echo.Wire == nil || len(echo.Wire.RPCID) == 0 {
		t.Fatalf("the injected message is echoed: %s", types(es))
	}

	for _, tc := range []struct{ agent, want string }{{SteerPromptRequired, SteerPromptRequired}, {SteerStartedNewTurn, SteerStartedNewTurn}, {"failed", "failed"}} {
		p.mu.Lock()
		p.steerNext = tc.agent
		p.mu.Unlock()
		out, err := c.Steer(ctx, Prompt{Text: "x"})
		if out != tc.want || (tc.agent == "failed") != (err != nil) {
			t.Fatalf("agent %s: %q %v", tc.agent, out, err)
		}
	}
	p.end("end_turn")
	collect(t, c, isType(EvTurnEnd))

	p2 := newPeer(t) // no steering advertised
	c2, _, err := p2.start(ClientOptions{}, NewPermissions(), 0, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	collect(t, c2, idle)
	_ = c2.Prompt(ctx, Prompt{Text: "go"})
	collect(t, c2, isText("working"))
	if _, err := c2.Steer(ctx, Prompt{Text: "x"}); !errors.Is(err, ErrSteeringUnsupported) {
		t.Fatalf("unsupported: %v", err)
	}
}

// Authenticate: the methods with their _meta; an agent that refuses the
// session signed out stays up with AwaitLogin, and signing in opens it.
func TestAuthenticate(t *testing.T) {
	p := newPeer(t)
	p.signedOut = true
	p.methods = []AuthMethod{{ID: "api-key", Name: "API Key", Meta: map[string]any{"api-key": map[string]any{"provider": "openai"}}},
		{ID: "login", Name: "Log in", Type: "terminal", Args: []string{"--cli"}}}
	c, _, err := p.start(ClientOptions{AwaitLogin: true}, NewPermissions(), 0, Config{})
	var re *Error
	if !errors.As(err, &re) || re.Code != CodeAuthRequired {
		t.Fatalf("start signed out: %v", err)
	}
	defer c.Close()
	es := collect(t, c, func(e Event) bool { return e.Type == EvStatus && data(e)["status"] == StatusError })
	if loginOf(es[len(es)-1]) == nil {
		t.Fatalf("the error status carries the login: %s", es[len(es)-1].Data)
	}
	select {
	case <-c.Done():
		t.Fatal("AwaitLogin keeps the agent running")
	default:
	}
	ms := c.AuthMethods()
	if len(ms) != 2 || ms[0].Meta["api-key"] == nil || ms[1].Args[0] != "--cli" {
		t.Fatalf("methods %+v", ms)
	}
	ctx := context.Background()
	if err := c.Authenticate(ctx, "bad", nil); !errors.As(err, &re) || re.Message != "invalid API key" {
		t.Fatalf("a refused sign-in: %v", err)
	}
	if err := c.Authenticate(ctx, "api-key", map[string]any{"api-key": map[string]any{"apiKey": "sk-1"}}); err != nil {
		t.Fatal(err)
	}
	var ap map[string]any
	_ = json.Unmarshal(p.lastFrame(MAuthenticate).Params, &ap)
	if b, _ := json.Marshal(ap); string(b) != `{"_meta":{"api-key":{"apiKey":"sk-1"}},"methodId":"api-key"}` {
		t.Fatalf("authenticate params %s", b)
	}
	es = collect(t, c, idle)
	if loginOf(es[len(es)-1]) != nil || c.State().SessionID != "s-1" {
		t.Fatalf("signed in, the session opens: %s", es[len(es)-1].Data)
	}

	// without AwaitLogin a signed-out agent ends the client, as before
	p2 := newPeer(t)
	p2.signedOut = true
	c2, _, err := p2.start(ClientOptions{}, NewPermissions(), 0, Config{})
	if err == nil {
		t.Fatal("signed out must fail Start")
	}
	select {
	case <-c2.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the client stays up without AwaitLogin")
	}
}

// URL elicitation (a device-code sign-in): advertised with
// ElicitationCaps.URL, surfaced as elicitation.request mode url during
// Authenticate, accepted without content, resolved by the agent's
// elicitation/complete; State keeps it; not advertised, it is declined.
func TestURLElicitation(t *testing.T) {
	caps := ClientCapabilities{Elicitation: &ElicitationCaps{Form: &struct{}{}, URL: &struct{}{}}, Meta: map[string]any{"terminal-auth": true}}
	p := newPeer(t)
	c, _, err := p.start(ClientOptions{Caps: &caps}, NewPermissions(), 0, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	collect(t, c, idle)
	var init map[string]any
	_ = json.Unmarshal(p.lastFrame(MInitialize).Params, &init)
	if b, _ := json.Marshal(init["clientCapabilities"]); !strings.Contains(string(b), `"elicitation":{"form":{},"url":{}}`) {
		t.Fatalf("caps %s", b)
	}
	authErr := make(chan error, 1)
	go func() { authErr <- c.Authenticate(context.Background(), "device", nil) }()
	es := collect(t, c, isType(EvElicitRequest))
	req := es[len(es)-1]
	d := data(req)
	if d["mode"] != "url" || d["url"] != "https://example.invalid/device" || d["elicitationId"] != "login-1" || !strings.Contains(d["message"].(string), "FAKE-1234") {
		t.Fatalf("request %s", req.Data)
	}
	if c.Status() != StatusIdle {
		t.Fatalf("a sign-in holds no turn up: %s", c.Status())
	}
	st := c.State()
	if len(st.Elicitations) != 1 || st.Elicitations[0].Mode != "url" || len(st.Elicitations[0].RPCID) == 0 {
		t.Fatalf("state %+v", st.Elicitations)
	}
	eid := d["eid"].(string)
	if err := c.RespondElicitation(eid, "accept", json.RawMessage(`{"x":1}`), "user:a"); err != nil {
		t.Fatal(err)
	}
	if got := <-p.elicited; len(got) != 1 || got["action"] != "accept" {
		t.Fatalf("the agent got %v (a url accept has no content)", got)
	}
	es = collect(t, c, func(e Event) bool { return e.Type == EvElicitResolved && data(e)["action"] == "complete" })
	var actions []string
	for _, e := range es {
		if e.Type == EvElicitResolved {
			actions = append(actions, data(e)["action"].(string)+"/"+data(e)["by"].(string))
			if data(e)["eid"] != eid {
				t.Fatalf("resolved %s", e.Data)
			}
		}
	}
	if strings.Join(actions, " ") != "accept/user:a complete/agent" {
		t.Fatalf("resolutions %v", actions)
	}
	if err := <-authErr; err != nil {
		t.Fatal(err)
	}
	if len(c.State().Elicitations) != 0 {
		t.Fatal("a completed url elicitation is forgotten")
	}

	// the agent completes before anyone answers: resolved, its request cancelled
	go func() { authErr <- c.Authenticate(context.Background(), "device-fast", nil) }()
	collect(t, c, isType(EvElicitRequest))
	close(p.gate)
	es = collect(t, c, func(e Event) bool { return e.Type == EvElicitResolved })
	if d := data(es[len(es)-1]); d["action"] != "complete" || d["by"] != "agent" {
		t.Fatalf("complete first: %s", es[len(es)-1].Data)
	}
	if got := <-p.elicited; got["action"] != "cancel" {
		t.Fatalf("the moot request got %v", got)
	}
	if err := <-authErr; err != nil {
		t.Fatal(err)
	}

	// not advertised: declined, no event
	p2 := newPeer(t)
	c2, _, err := p2.start(ClientOptions{}, NewPermissions(), 0, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	collect(t, c2, idle)
	go func() { authErr <- c2.Authenticate(context.Background(), "device", nil) }()
	if got := <-p2.elicited; got["action"] != "decline" {
		t.Fatalf("not advertised: %v", got)
	}
	<-authErr
	for {
		select {
		case e := <-c2.Events():
			if e.Type == EvElicitRequest || e.Type == EvElicitResolved {
				t.Fatalf("a declined url elicitation surfaced: %s", e.Type)
			}
			continue
		default:
		}
		break
	}
}

// A form question read again after a handoff is not filed twice.
func TestElicitationIdempotent(t *testing.T) {
	var e elicits
	a, dup := e.add(json.RawMessage(`7`), Elicitation{Message: "q"})
	b, dup2 := e.add(json.RawMessage(`7`), Elicitation{Message: "q"})
	c, dup3 := e.add(nil, Elicitation{Message: "q"})
	d, dup4 := e.add(nil, Elicitation{Message: "q"})
	if dup || !dup2 || a != b || dup3 || dup4 || c == d || e.count() != 3 {
		t.Fatalf("%s %v %s %v %s %v %s %v", a, dup, b, dup2, c, dup3, d, dup4)
	}
}

// Abandon: a prompt the embedder's transport dropped ends its turn with
// the error at once (turn.end, stopReason error), the client takes the
// next prompt; a call the agent already answered isn't touched.
func TestAbandon(t *testing.T) {
	p := newPeer(t)
	c, _, err := p.start(ClientOptions{}, NewPermissions(), 0, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	collect(t, c, idle)
	ctx := context.Background()
	if err := c.Prompt(ctx, Prompt{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	collect(t, c, isText("working"))
	id := c.State().PromptRPC
	if !c.Abandon(id, "the message was lost on its way") {
		t.Fatal("nothing waited on the prompt")
	}
	es := collect(t, c, isType(EvTurnEnd))
	end := data(es[len(es)-1])
	if end["stopReason"] != "error" || !strings.Contains(end["error"].(string), "the message was lost on its way") {
		t.Fatalf("the turn's end: %v", end)
	}
	if c.State().PromptRPC != nil || c.Abandon(id, "again") {
		t.Fatal("the abandoned prompt is still in flight")
	}
	p.end("end_turn") // the agent's late answer is discarded
	if err := c.Prompt(ctx, Prompt{Text: "go"}); err != nil {
		t.Fatalf("the next prompt: %v", err)
	}
	collect(t, c, isText("working"))
	p.end("end_turn")
	collect(t, c, isType(EvTurnEnd))
}
