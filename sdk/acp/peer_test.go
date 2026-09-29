package acp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
)

// stream is an agent's output as a sandbox exec keeps it: every byte
// written stays, and a reader follows it from any offset — several client
// generations in turn, as a pipe resuming after a handoff does.
type stream struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
}

func newStream() *stream {
	s := &stream{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *stream) Write(b []byte) (int, error) {
	s.mu.Lock()
	s.buf = append(s.buf, b...)
	s.mu.Unlock()
	s.cond.Broadcast()
	return len(b), nil
}

func (s *stream) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cond.Broadcast()
}

func (s *stream) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf...)
}

// follower reads a stream from an offset until the stream closes or the
// follower is detached (the reading process goes away).
type follower struct {
	s        *stream
	off      int64
	detached bool
}

func (s *stream) follow(off int64) *follower { return &follower{s: s, off: off} }

func (f *follower) Read(p []byte) (int, error) {
	s := f.s
	s.mu.Lock()
	defer s.mu.Unlock()
	for f.off >= int64(len(s.buf)) && !s.closed && !f.detached {
		s.cond.Wait()
	}
	if f.detached || f.off >= int64(len(s.buf)) {
		return 0, io.EOF
	}
	n := copy(p, s.buf[f.off:])
	f.off += int64(n)
	return n, nil
}

func (f *follower) detach() {
	f.s.mu.Lock()
	f.detached = true
	f.s.mu.Unlock()
	f.s.cond.Broadcast()
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// peer is a scripted agent for the extension tests: it outlives the
// clients that drive it (a handoff attaches a new one to the same agent),
// records every frame it receives, and is otherwise driven by the test —
// a prompt stays open until the test ends it.
type peer struct {
	t    *testing.T
	out  *stream
	inR  *io.PipeReader
	inW  *io.PipeWriter
	conn *Conn

	mu        sync.Mutex
	frames    []*Message      // requests and notifications received, in order
	prompt    json.RawMessage // the open session/prompt's id
	steering  bool            // advertise _session/steering
	steerNext string          // the next steer's outcome ("": injected in a turn, else promptRequired)
	signedOut bool            // session/new answers -32000 until authenticate
	methods   []AuthMethod
	elicited  chan map[string]any // the client's answers to url elicitations
	gate      chan struct{}       // device-fast completes once this closes (the client has the request)
}

func newPeer(t *testing.T) *peer {
	p := &peer{t: t, out: newStream(), elicited: make(chan map[string]any, 4), gate: make(chan struct{})}
	p.inR, p.inW = io.Pipe()
	p.conn = NewConn(p.inR, p.out)
	p.conn.OnRequest = p.onRequest
	p.conn.OnNotify = p.onNotify
	go func() { _ = p.conn.Serve(); p.out.close() }()
	t.Cleanup(func() { p.inW.Close(); p.out.close() })
	return p
}

// spawner connects a client to the running agent, reading its output from
// off; detach ends that client's view of it (its process going away).
func (p *peer) spawner(off int64) (Spawner, *follower) {
	f := p.out.follow(off)
	return func(context.Context, Config) (*Process, error) {
		return &Process{Stdin: nopCloser{p.inW}, Stdout: f, Off: off, Kill: f.detach}, nil
	}, f
}

// start runs a client against the peer from offset off.
func (p *peer) start(o ClientOptions, perms *Permissions, off int64, cfg Config) (*Client, *follower, error) {
	spawn, f := p.spawner(off)
	cfg.Provider, cfg.Cwd, cfg.Spawn, cfg.Perms, cfg.Version = Provider{ID: "peer", Login: "peer login"}, "/w", spawn, perms, "test"
	c := NewWith(o)
	err := c.Start(context.Background(), cfg)
	return c, f, err
}

func (p *peer) record(m *Message) {
	p.mu.Lock()
	p.frames = append(p.frames, m)
	p.mu.Unlock()
}

// seen is the methods received, in order, with their ids.
func (p *peer) seen() []*Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*Message(nil), p.frames...)
}

func (p *peer) onRequest(m *Message) (any, *Error) {
	p.record(m)
	switch m.Method {
	case MInitialize:
		p.mu.Lock()
		defer p.mu.Unlock()
		res := InitializeResult{ProtocolVersion: 1, AgentInfo: &Info{Name: "peer", Version: "1"}, AuthMethods: p.methods,
			AgentCapabilities: &AgentCapabilities{LoadSession: true}}
		if p.steering {
			res.Meta = map[string]any{"steering": map[string]any{"supported": true}}
		}
		return res, nil
	case MSessionNew:
		p.mu.Lock()
		out := p.signedOut
		p.mu.Unlock()
		if out {
			return nil, &Error{Code: CodeAuthRequired, Message: "Authentication required"}
		}
		return SessionNewResult{SessionID: "s-1", Modes: &SessionModes{CurrentModeID: "ask", AvailableModes: []ModeEntry{{ID: "ask", Name: "Ask"}}}}, nil
	case MSessionLoad:
		// the earlier turns stream back before the answer, a live update after it
		go func() {
			p.update(map[string]any{"sessionUpdate": UpUserChunk, "content": ContentBlock{Type: "text", Text: "old question"}})
			p.update(map[string]any{"sessionUpdate": UpAgentChunk, "content": ContentBlock{Type: "text", Text: "old answer"}})
			_ = p.conn.Reply(m.ID, SessionLoadResult{}, nil)
			p.update(map[string]any{"sessionUpdate": UpAgentChunk, "content": ContentBlock{Type: "text", Text: "live"}})
		}()
		return nil, nil
	case MSessionPrompt:
		p.mu.Lock()
		p.prompt = m.ID
		p.mu.Unlock()
		p.chunk("working")
		return nil, nil // the test ends the turn (end)
	case MSessionSteering:
		var sp struct {
			Prompt []ContentBlock `json:"prompt"`
		}
		_ = json.Unmarshal(m.Params, &sp)
		p.mu.Lock()
		out, running := p.steerNext, p.prompt != nil
		p.steerNext = ""
		p.mu.Unlock()
		if out == "" {
			out = SteerPromptRequired
			if running {
				out = SteerInjected
			}
		}
		if out == SteerInjected {
			p.chunk("steered: " + sp.Prompt[0].Text)
		}
		return SteerResult{Outcome: out}, nil
	case MAuthenticate:
		var ap AuthenticateParams
		_ = json.Unmarshal(m.Params, &ap)
		switch ap.MethodID {
		case "bad":
			return nil, &Error{Code: CodeInvalidParams, Message: "invalid API key"}
		case "device", "device-fast":
			go p.device(m.ID, ap.MethodID == "device-fast")
			return nil, nil
		}
		p.signIn()
		return map[string]any{}, nil
	}
	return nil, &Error{Code: CodeMethodNotFound, Message: m.Method}
}

func (p *peer) onNotify(m *Message) {
	p.record(m)
	if m.Method == MSessionCancel {
		p.end("cancelled")
	}
}

func (p *peer) signIn() {
	p.mu.Lock()
	p.signedOut = false
	p.mu.Unlock()
}

// device is codex-acp's device-code sign-in: a url elicitation naming the
// authenticate request, then (once the person answered it — or, fast, once
// the test opens the gate, unanswered) elicitation/complete and
// authenticate's answer.
func (p *peer) device(authID json.RawMessage, fast bool) {
	params := map[string]any{"mode": "url", "requestId": authID, "url": "https://example.invalid/device",
		"message": "Enter code FAKE-1234 at https://example.invalid/device", "elicitationId": "login-1"}
	answered := make(chan struct{})
	go func() {
		var res map[string]any
		if err := p.conn.Call(MElicitCreate, params, &res); err == nil {
			p.elicited <- res
		}
		close(answered)
	}()
	if fast {
		<-p.gate
	} else {
		<-answered
	}
	_ = p.conn.Notify(MElicitComplete, ElicitCompleteParams{ElicitationID: "login-1"})
	p.signIn()
	_ = p.conn.Reply(authID, map[string]any{}, nil)
}

func (p *peer) update(v any) {
	_ = p.conn.Notify(MSessionUpdate, map[string]any{"sessionId": "s-1", "update": v})
}

func (p *peer) chunk(text string) {
	p.update(map[string]any{"sessionUpdate": UpAgentChunk, "content": ContentBlock{Type: "text", Text: text}})
}

// end answers the open prompt.
func (p *peer) end(reason string) {
	p.mu.Lock()
	id := p.prompt
	p.prompt = nil
	p.mu.Unlock()
	if id != nil {
		_ = p.conn.Reply(id, PromptResult{StopReason: reason}, nil)
	}
}

// ask sends a permission request; its outcome arrives on the channel.
func (p *peer) ask(title string) <-chan PermissionOutcome {
	ch := make(chan PermissionOutcome, 1)
	go func() {
		var res RequestPermissionResult
		err := p.conn.Call(MRequestPermission, RequestPermissionParams{SessionID: "s-1",
			ToolCall: ToolCallUpdate{ToolCallID: "t1", Title: strp(title), Kind: strp("execute")},
			Options:  []PermissionOption{{OptionID: "once", Name: "Once", Kind: AllowOnce}, {OptionID: "no", Name: "No", Kind: RejectOnce}}}, &res)
		if err != nil {
			res.Outcome.Outcome = "error: " + err.Error()
		}
		ch <- res.Outcome
	}()
	return ch
}

// isText: a message.delta of this text.
func isText(text string) func(Event) bool {
	return func(e Event) bool { return e.Type == EvMessageDelta && data(e)["text"] == text }
}

func isType(typ string) func(Event) bool { return func(e Event) bool { return e.Type == typ } }

// frameAt says the stream holds a whole frame ending at off, containing s.
func frameAt(t *testing.T, buf []byte, off int64, s string) {
	t.Helper()
	if off <= 0 || off > int64(len(buf)) || buf[off-1] != '\n' {
		t.Fatalf("offset %d is not a frame's end (stream of %d bytes)", off, len(buf))
	}
	start := strings.LastIndexByte(string(buf[:off-1]), '\n') + 1
	if line := string(buf[start:off]); !strings.Contains(line, s) {
		t.Fatalf("the frame ending at %d is %q, want one with %q", off, line, s)
	}
}
