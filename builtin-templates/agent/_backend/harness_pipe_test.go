package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
	"github.com/xbin-dev/xbin/sdk/acp/acptest"
)

// This test binary is also the scripted coding agent: the fake manager
// runs a sandbox's commands on the host, so a harness's argv is
// acptest.Command() — this binary, re-executed as the adapter.
func TestMain(m *testing.M) {
	acptest.MainIfAdapter()
	os.Exit(m.Run())
}

// pipeSandbox binds a fake manager (hack/fakesandbox), tuned by tune, makes
// alice a sandbox there and returns the target a harness runs at — over the
// stdio socket, or the exec routes (a manager without stdio).
func pipeSandbox(t *testing.T, wrap func(http.Handler) http.Handler, stdio bool, tune func(*fsbManager)) (hpTarget, *sbxTestManager) {
	t.Helper()
	m := bindSbxWith(t, wrap, "apps/cs")["apps/cs"]
	if !stdio {
		m.Caps = []string{"exec", "files", "tar"}
	}
	if tune != nil {
		tune(m.fsbManager)
	}
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{})
	conn, err := sbxDial("apps/cs", "alice")
	if err != nil {
		t.Fatal(err)
	}
	h, err := managerHello(context.Background(), conn.M)
	if err != nil {
		t.Fatal(err)
	}
	tg := harnessTarget(&sbxUse{Conn: conn, ID: box.ID, Box: box, Hello: h})
	if tg.Stdio != stdio {
		t.Fatalf("stdio offered: %v", tg.Stdio)
	}
	return tg, m
}

// transports runs f over the exec routes and over the stdio socket.
func transports(t *testing.T, f func(t *testing.T, stdio bool)) {
	for _, stdio := range []bool{false, true} {
		name := "exec"
		if stdio {
			name = "stdio"
		}
		t.Run(name, func(t *testing.T) { f(t, stdio) })
	}
}

// fakeSpawn is the scripted agent as generation gen of run's harness
// (GORACE: a -race build of this binary waits a second as it exits).
func fakeSpawn(run int64, gen int) hpSpawn {
	return hpSpawn{Run: run, Root: 3, Gen: gen, Provider: "fake", Argv: acptest.Command(),
		Env: map[string]string{"FAKE_X": "1", "GORACE": "atexit_sleep_ms=0"}}
}

// hpEvents keeps a client's events as they come.
type hpEvents struct {
	mu     sync.Mutex
	evs    []acp.Event
	closed bool
	ch     chan struct{}
}

func watchEvents(c *acp.Client) *hpEvents {
	l := &hpEvents{ch: make(chan struct{})}
	go func() {
		for e := range c.Events() {
			l.mu.Lock()
			l.evs = append(l.evs, e)
			close(l.ch)
			l.ch = make(chan struct{})
			l.mu.Unlock()
		}
		l.mu.Lock()
		l.closed = true
		close(l.ch)
		l.mu.Unlock()
	}()
	return l
}

func (l *hpEvents) all() []acp.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]acp.Event(nil), l.evs...)
}

func (l *hpEvents) dump() string {
	var b strings.Builder
	for _, e := range l.all() {
		fmt.Fprintf(&b, "\n  %s %.200s", e.Type, e.Data)
	}
	return b.String()
}

// wait returns the first event pred matches, waiting for it.
func (l *hpEvents) wait(t *testing.T, what string, pred func(acp.Event) bool) acp.Event {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		l.mu.Lock()
		for _, e := range l.evs {
			if pred(e) {
				l.mu.Unlock()
				return e
			}
		}
		closed, ch := l.closed, l.ch
		l.mu.Unlock()
		if closed {
			t.Fatalf("the events ended before %s:%s", what, l.dump())
		}
		select {
		case <-ch:
		case <-deadline:
			t.Fatalf("no %s in 20 s:%s", what, l.dump())
		}
	}
}

func evIs(typ, key, contains string) func(acp.Event) bool {
	return func(e acp.Event) bool {
		if e.Type != typ {
			return false
		}
		if key == "" {
			return true
		}
		var d map[string]any
		_ = json.Unmarshal(e.Data, &d)
		s, _ := d[key].(string)
		return strings.Contains(s, contains)
	}
}

// pipeClient drives p with an acp.Client as AgTT sets one up (no fs, no
// terminals: the adapter works in the sandbox itself) — attaching to a
// running session when st is given — and waits for it to be ready.
func pipeClient(t *testing.T, p *harnessPipe, prefix string, st *acp.SessionState) (*acp.Client, *hpEvents) {
	t.Helper()
	caps := acp.ClientCapabilities{Meta: map[string]any{"terminal_output": true}, Elicitation: &acp.ElicitationCaps{Form: &struct{}{}}}
	c := acp.NewWith(acp.ClientOptions{Caps: &caps, IDPrefix: prefix, Attach: st})
	argv := acptest.Command()
	ev := watchEvents(c)
	cfg := acp.Config{Provider: acp.Fake(argv), Argv: argv, Perms: acp.NewPermissions(), Log: func(s string) { t.Log("acp:", s) },
		Spawn: func(context.Context, acp.Config) (*acp.Process, error) { return p.Process(), nil }}
	if err := c.Start(context.Background(), cfg); err != nil {
		t.Fatalf("start: %v%s", err, ev.dump())
	}
	if st == nil {
		ev.wait(t, "idle", evIs(acp.EvStatus, "status", acp.StatusIdle))
	}
	return c, ev
}

func hpWaitDone(t *testing.T, p *harnessPipe, what string) {
	t.Helper()
	select {
	case <-p.Done():
	case <-time.After(20 * time.Second):
		t.Fatalf("%s: the pipe's reader never reached the end", what)
	}
}

// An adapter started through the pipe answers initialize, session/new and
// a prompt whose frame is bigger than the manager's stdin limit (sent in
// chunks); its stderr is the harness log; closing the client ends it.
func TestHarnessPipeTurn(t *testing.T) {
	transports(t, func(t *testing.T, stdio bool) {
		tg, m := pipeSandbox(t, nil, stdio, func(m *fsbManager) { m.StdinMax = 1000 })
		if tg.StdinMax != 1000 {
			t.Fatalf("hello's stdinMax: %d", tg.StdinMax)
		}
		ctx := context.Background()
		p, err := startHarnessPipe(ctx, tg, fakeSpawn(7, 1))
		if err != nil {
			t.Fatal(err)
		}
		var req struct {
			Argv     []string          `json:"argv"`
			Env      map[string]string `json:"env"`
			Stdin    bool              `json:"stdin"`
			Split    bool              `json:"split"`
			Label    string            `json:"label"`
			ClientID string            `json:"clientId"`
		}
		for _, c := range m.Calls() {
			if c.Method == "POST" && c.Path == "/sbx/sandboxes/"+tg.ID+"/execs" {
				_ = json.Unmarshal([]byte(c.Body), &req)
				if c.SbxUser != "alice" {
					t.Fatalf("started as %q", c.SbxUser)
				}
			}
		}
		cmd := strings.Join(acptest.Command(), " ")
		wantArgv := "sh -c " + harnessWrapper + " h 7-1 " + cmd
		if stdio {
			wantArgv = cmd
		}
		if strings.Join(req.Argv, " ") != wantArgv || !req.Stdin || req.Split != stdio || req.Label != "harness fake · #3" || req.ClientID != "harness:7:1" ||
			req.Env["IS_SANDBOX"] != "1" || req.Env["NO_COLOR"] != "1" || req.Env["FAKE_X"] != "1" {
			t.Fatalf("the exec: %+v", req)
		}
		if again, err := tg.Conn.ExecStart(ctx, tg.ID, harnessExecReq(tg, fakeSpawn(7, 1))); err != nil || again.ID != p.ExecID() {
			t.Fatalf("a repeated start is the same exec: %+v %v", again, err)
		}

		c, ev := pipeClient(t, p, "h7.1", nil)
		if p.Stdio() != stdio {
			t.Fatalf("the transport: stdio %v", p.Stdio())
		}
		text := "echo " + strings.Repeat("x", 3000)
		if err := c.Send(ctx, text); err != nil {
			t.Fatal(err)
		}
		ev.wait(t, "the echo", evIs(acp.EvMessageDelta, "text", "echo: "+text))
		end := ev.wait(t, "turn.end", evIs(acp.EvTurnEnd, "stopReason", "end_turn"))
		if end.Wire == nil || end.Wire.Off != p.Off() {
			t.Fatalf("turn.end at %+v, read %d", end.Wire, p.Off())
		}
		if !stdio {
			n := 0
			for _, c := range m.Calls() {
				if c.Method == "POST" && strings.HasSuffix(c.Path, "/stdin") {
					if len(c.Body) > 1000 {
						t.Fatalf("a stdin POST of %d bytes", len(c.Body))
					}
					n++
				}
			}
			if n < 5 {
				t.Fatalf("the prompt went in %d POSTs", n)
			}
		}
		log, err := harnessLogTail(ctx, tg.Conn, tg.ID, p.ExecID(), 7, 1, 0)
		if err != nil || !strings.Contains(string(log), "fakeacp: up") {
			t.Fatalf("the log: %q %v", log, err)
		}
		if log, err := harnessLogTail(ctx, tg.Conn, tg.ID, p.ExecID(), 7, 1, 5); err != nil || len(log) != 5 {
			t.Fatalf("5 bytes of the log: %q %v", log, err)
		}
		if _, err := harnessLogTail(ctx, tg.Conn, tg.ID, "", 7, 2, 0); !errors.Is(err, errNoHarnessLog) {
			t.Fatalf("another generation's log: %v", err)
		}

		c.Close() // stdin eof: the agent exits
		hpWaitDone(t, p, "close")
		ev.wait(t, "exited", evIs(acp.EvStatus, "status", acp.StatusExited))
		if err := p.Wait(); err != nil || p.Lost() || p.Exit().State != "exited" {
			t.Fatalf("the end: %v %+v", err, p.Exit())
		}
	})
}

// A successor attaches to the exec at the offset its predecessor committed,
// mid-turn: it reads on from there — nothing twice — and gets the turn's
// end on the predecessor's prompt. Over stdio the newest attacher wins.
func TestHarnessPipeReattach(t *testing.T) {
	transports(t, func(t *testing.T, stdio bool) {
		tg, _ := pipeSandbox(t, nil, stdio, nil)
		ctx := context.Background()
		p1, err := startHarnessPipe(ctx, tg, fakeSpawn(7, 1))
		if err != nil {
			t.Fatal(err)
		}
		c1, ev1 := pipeClient(t, p1, "h7.1", nil)
		if err := c1.Send(ctx, "stall"); err != nil {
			t.Fatal(err)
		}
		stalling := ev1.wait(t, "stalling", evIs(acp.EvMessageDelta, "text", "stalling"))
		committed := stalling.Wire.Off
		st := c1.State()
		if committed <= 0 || st.PromptRPC == nil {
			t.Fatalf("committed %d, state %+v", committed, st)
		}
		time.Sleep(100 * time.Millisecond) // the agent's stderr is read on (stdio: errOff)
		errOff := p1.ErrOff()

		p1.Detach()
		hpWaitDone(t, p1, "detach")
		ev1.wait(t, "exited", evIs(acp.EvStatus, "status", acp.StatusExited))
		c1.Close() // Kill: nothing, it was let go
		time.Sleep(300 * time.Millisecond)
		if ex, err := tg.Conn.ExecGet(ctx, tg.ID, p1.ExecID()); err != nil || ex.State != "running" {
			t.Fatalf("the exec after a detach: %+v %v", ex, err)
		}
		if !errors.Is(p1.Err(), errPipeDetached) || p1.Lost() {
			t.Fatalf("detached: %v, lost %v", p1.Err(), p1.Lost())
		}

		p2 := attachHarnessPipe(ctx, tg, p1.ExecID(), committed, errOff)
		c2, ev2 := pipeClient(t, p2, "h7.2", &st)
		ev2.wait(t, "running", evIs(acp.EvStatus, "status", acp.StatusRunning))
		if err := c2.Cancel(); err != nil {
			t.Fatal(err)
		}
		end := ev2.wait(t, "turn.end", evIs(acp.EvTurnEnd, "stopReason", "cancelled"))
		if string(end.Wire.RPCID) != string(st.PromptRPC) || end.Wire.Off <= committed {
			t.Fatalf("turn.end %+v, prompt %s", end.Wire, st.PromptRPC)
		}
		for _, e := range ev2.all() {
			if evIs(acp.EvMessageDelta, "text", "stalling")(e) {
				t.Fatalf("read twice:%s", ev2.dump())
			}
			if e.Wire != nil && e.Wire.Off != 0 && e.Wire.Off <= committed {
				t.Fatalf("an event from before the attach point:%s", ev2.dump())
			}
		}
		if err := c2.Send(ctx, "again"); err != nil {
			t.Fatal(err)
		}
		ev2.wait(t, "the successor's turn", evIs(acp.EvMessageDelta, "text", "echo: again"))

		last := c2
		if stdio { // a third process attaches: the second's socket is closed, the third holds stdin
			ev2.wait(t, "its end", func(e acp.Event) bool {
				return e.Type == acp.EvTurnEnd && e.Wire != nil && string(e.Wire.RPCID) != string(st.PromptRPC)
			})
			time.Sleep(100 * time.Millisecond)
			st2 := c2.State()
			p3 := attachHarnessPipe(ctx, tg, p2.ExecID(), p2.Off(), p2.ErrOff())
			c3, ev3 := pipeClient(t, p3, "h7.3", &st2)
			ev3.wait(t, "idle", evIs(acp.EvStatus, "status", acp.StatusIdle))
			if err := c3.Send(ctx, "third"); err != nil {
				t.Fatal(err)
			}
			hpWaitDone(t, p2, "replaced")
			if !errors.Is(p2.Err(), errPipeReplaced) || p2.Lost() {
				t.Fatalf("replaced: %v", p2.Err())
			}
			ev3.wait(t, "the third's turn", evIs(acp.EvMessageDelta, "text", "echo: third"))
			last, p2 = c3, p3
		}
		last.Close()
		hpWaitDone(t, p2, "close")
		if p2.Exit().State != "exited" {
			t.Fatalf("the end: %+v", p2.Exit())
		}
	})
}

// hpFrames speaks JSON-RPC over a pipe without a client.
type hpFrames struct {
	t   *testing.T
	in  io.Writer
	dec *acp.Decoder
}

func (r *hpFrames) send(id int, method string, params any) {
	r.t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if _, err := r.in.Write(append(b, '\n')); err != nil {
		r.t.Fatal(err)
	}
}

// answer reads frames until the response to id.
func (r *hpFrames) answer(id int) json.RawMessage {
	r.t.Helper()
	for {
		m, err := r.dec.Next()
		if err != nil {
			r.t.Fatalf("waiting for %d: %v", id, err)
		}
		if string(m.ID) == fmt.Sprint(id) && m.Method == "" {
			return m.Result
		}
	}
}

// Output the ring dropped before anyone read it is a gap: the reader gets
// *acp.Gap, the decoder resyncs at the next newline, and every frame after
// it reads whole.
func TestHarnessPipeGap(t *testing.T) {
	transports(t, func(t *testing.T, stdio bool) {
		tg, _ := pipeSandbox(t, nil, stdio, func(m *fsbManager) { m.Ring = 4096 })
		ctx := context.Background()
		p1, err := startHarnessPipe(ctx, tg, fakeSpawn(7, 1))
		if err != nil {
			t.Fatal(err)
		}
		proc := p1.Process()
		r := &hpFrames{t: t, in: proc.Stdin, dec: acp.NewDecoderAt(proc.Stdout, proc.Off)}
		r.send(1, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}})
		r.answer(1)
		r.send(2, "session/new", map[string]any{"cwd": "/", "mcpServers": []any{}})
		var sess struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(r.answer(2), &sess)
		for { // the agent's commands follow the session: then it is quiet
			m, err := r.dec.Next()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(m.Params), "available_commands_update") {
				break
			}
		}
		r.send(3, "session/prompt", map[string]any{"sessionId": sess.SessionID, "prompt": []map[string]string{{"type": "text", "text": "long40"}}})
		off := r.dec.Offset()
		p1.Detach() // nobody reads while the agent writes ~80 KiB into a 4 KiB ring

		var total int64
		for deadline := time.Now().Add(20 * time.Second); ; {
			ex, err := tg.Conn.ExecGet(ctx, tg.ID, p1.ExecID())
			if err != nil {
				t.Fatal(err)
			}
			tail, err := tg.Conn.ExecOutput(ctx, tg.ID, p1.ExecID(), max(0, ex.Total-300), 300, 0, false)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(tail.Data, `"stopReason":"end_turn"`) {
				total = ex.Total
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the turn never ended: %d bytes, %q", ex.Total, tail.Data)
			}
			time.Sleep(50 * time.Millisecond)
		}

		p2 := attachHarnessPipe(ctx, tg, p1.ExecID(), off, 0)
		proc = p2.Process()
		dec := acp.NewDecoderAt(proc.Stdout, proc.Off)
		if _, err := dec.Next(); !errors.Is(err, acp.ErrGap) {
			t.Fatalf("the first read: %v", err)
		}
		lost := dec.Offset() - off
		if lost < 40<<10 {
			t.Fatalf("lost %d bytes", lost)
		}
		frames := 0
		for {
			m, err := dec.Next()
			if err != nil {
				t.Fatalf("after the gap, frame %d: %v", frames, err)
			}
			frames++
			if string(m.ID) == "3" {
				if !strings.Contains(string(m.Result), "end_turn") {
					t.Fatalf("the prompt's answer: %s", m.Result)
				}
				break
			}
		}
		if frames < 2 || dec.Offset() != total || p2.Off() != total {
			t.Fatalf("%d frames after the gap, read to %d / %d of %d", frames, dec.Offset(), p2.Off(), total)
		}
		p2.Kill()
		if _, err := dec.Next(); err != io.EOF {
			t.Fatalf("after the kill: %v", err)
		}
		if p2.Exit().State != "exited" {
			t.Fatalf("stdin's eof ends it: %+v", p2.Exit())
		}
	})
}

// hpCutter records the stdio sockets a manager serves, to drop them.
type hpCutter struct {
	mu    sync.Mutex
	conns []net.Conn
}

func (k *hpCutter) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stdio") {
			w = &hpHijack{ResponseWriter: w, k: k}
		}
		h.ServeHTTP(w, r)
	})
}

func (k *hpCutter) cut() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := len(k.conns)
	for _, c := range k.conns {
		c.Close()
	}
	k.conns = nil
	return n
}

type hpHijack struct {
	http.ResponseWriter
	k *hpCutter
}

func (h *hpHijack) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := http.NewResponseController(h.ResponseWriter).Hijack()
	if err == nil {
		h.k.mu.Lock()
		h.k.conns = append(h.k.conns, c)
		h.k.mu.Unlock()
	}
	return c, rw, err
}

// An exec that is gone — the manager says lost (410), or doesn't know it
// (an attach after it was forgotten) — ends the stream with Lost; a stdio
// socket that drops is attached again from where reading got.
func TestHarnessPipeLost(t *testing.T) {
	t.Run("410", func(t *testing.T) {
		tg, m := pipeSandbox(t, nil, false, nil)
		ctx := context.Background()
		p, err := startHarnessPipe(ctx, tg, fakeSpawn(7, 1))
		if err != nil {
			t.Fatal(err)
		}
		m.FailNext("output", http.StatusGone, "lost", "the exec is gone (its sandbox restarted)")
		if b, err := io.ReadAll(p.Process().Stdout); err != nil || len(b) != 0 {
			t.Fatalf("read %q, %v", b, err)
		}
		if !p.Lost() || !errors.Is(p.Wait(), errPipeLost) || p.Err() != nil {
			t.Fatalf("lost %v, wait %v, err %v", p.Lost(), p.Wait(), p.Err())
		}
		_ = tg.Conn.ExecDelete(ctx, tg.ID, p.ExecID())
	})
	t.Run("forgotten", func(t *testing.T) {
		transports(t, func(t *testing.T, stdio bool) {
			tg, _ := pipeSandbox(t, nil, stdio, nil)
			p := attachHarnessPipe(context.Background(), tg, "e99", 120, 0)
			if b, err := io.ReadAll(p.Process().Stdout); err != nil || len(b) != 0 || !p.Lost() {
				t.Fatalf("an exec it doesn't know: %q %v lost %v", b, err, p.Lost())
			}
			if _, err := p.Process().Stdin.Write([]byte("{}\n")); !errors.Is(err, errPipeEnded) {
				t.Fatalf("a write to a lost exec: %v", err)
			}
		})
	})
	t.Run("stdio drop", func(t *testing.T) {
		k := &hpCutter{}
		tg, m := pipeSandbox(t, k.wrap, true, nil)
		ctx := context.Background()
		p, err := startHarnessPipe(ctx, tg, fakeSpawn(7, 1))
		if err != nil {
			t.Fatal(err)
		}
		c, ev := pipeClient(t, p, "h7.1", nil)
		if n := k.cut(); n != 1 {
			t.Fatalf("%d sockets", n)
		}
		if err := c.Send(ctx, "after the drop"); err != nil {
			t.Fatal(err)
		}
		ev.wait(t, "the turn after a drop", evIs(acp.EvMessageDelta, "text", "echo: after the drop"))
		ev.wait(t, "its end", evIs(acp.EvTurnEnd, "", ""))
		m.FailNext("stdio", http.StatusGone, "lost", "the exec is gone (its sandbox restarted)")
		k.cut()
		hpWaitDone(t, p, "lost")
		ev.wait(t, "exited", evIs(acp.EvStatus, "status", acp.StatusExited))
		if !p.Lost() || p.Err() != nil {
			t.Fatalf("lost %v, err %v", p.Lost(), p.Err())
		}
		c.Close()
		_ = tg.Conn.ExecDelete(ctx, tg.ID, p.ExecID())
	})
}

// A stdin POST the manager answers 503 (the command isn't reading) is sent
// again; the frame arrives once.
func TestHarnessPipeStdinRetry(t *testing.T) {
	tg, m := pipeSandbox(t, nil, false, nil)
	ctx := context.Background()
	p, err := startHarnessPipe(ctx, tg, fakeSpawn(7, 1))
	if err != nil {
		t.Fatal(err)
	}
	c, ev := pipeClient(t, p, "h7.1", nil)
	before := m.count("POST", "/sbx/sandboxes/"+tg.ID+"/execs/"+p.ExecID()+"/stdin")
	m.FailNext("stdin", http.StatusServiceUnavailable, "unavailable", "the command isn't reading its input")
	if err := c.Send(ctx, "retried"); err != nil {
		t.Fatal(err)
	}
	ev.wait(t, "the echo", evIs(acp.EvMessageDelta, "text", "echo: retried"))
	ev.wait(t, "its end", evIs(acp.EvTurnEnd, "stopReason", "end_turn"))
	if n := m.count("POST", "/sbx/sandboxes/"+tg.ID+"/execs/"+p.ExecID()+"/stdin") - before; n != 2 {
		t.Fatalf("%d stdin POSTs for one frame", n)
	}
	c.Close()
	hpWaitDone(t, p, "close")
}

// Kill escalates: stdin's eof, TERM, then DELETE for a command that
// ignores both — and a command deleted by Kill was killed, not lost.
func TestHarnessPipeKill(t *testing.T) {
	tg, m := pipeSandbox(t, nil, false, nil)
	s := fakeSpawn(7, 1)
	s.Argv = []string{"sh", "-c", `trap "" TERM; while :; do sleep 0.05; done`}
	p, err := startHarnessPipe(context.Background(), tg, s)
	if err != nil {
		t.Fatal(err)
	}
	p.killStep = 100 * time.Millisecond
	read := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(p.Process().Stdout)
		read <- err
	}()
	p.Kill()
	select {
	case err := <-read:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Kill never ended it")
	}
	path := "/sbx/sandboxes/" + tg.ID + "/execs/" + p.ExecID()
	if m.count("POST", path+"/stdin") != 1 || m.count("POST", path+"/signal") != 1 || m.count("DELETE", path) != 1 {
		t.Fatalf("eof %d, signal %d, delete %d", m.count("POST", path+"/stdin"), m.count("POST", path+"/signal"), m.count("DELETE", path))
	}
	if p.Lost() || p.Exit().State != "killed" || p.Wait() == nil {
		t.Fatalf("the end: %+v %v", p.Exit(), p.Wait())
	}
}
