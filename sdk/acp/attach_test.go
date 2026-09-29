package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// A process hands a session to its successor mid-turn: the successor
// attaches from the committed offset with the state and the pending
// permission, files nothing twice when it reads the request again, answers
// the restored permission, gets the turn's end on the predecessor's prompt
// id, and prompts on with ids that cannot collide.
func TestReattachMidTurn(t *testing.T) {
	p := newPeer(t)
	perms1 := NewPermissions()
	c1, f1, err := p.start(ClientOptions{IDPrefix: "h7.1"}, perms1, 0, Config{})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, c1, idle)
	if err := c1.Prompt(context.Background(), Prompt{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	es := collect(t, c1, isText("working"))
	echo, working := es[0], es[len(es)-1]
	if echo.Wire == nil || string(echo.Wire.RPCID) != `"h7.1-3"` || echo.Wire.Off != 0 {
		t.Fatalf("the prompt's echo names the prompt: %+v", echo.Wire)
	}
	frameAt(t, p.out.bytes(), working.Wire.Off, `"working"`)
	committed := working.Wire.Off // an embedder commits this with the chunk (were it durable)

	outcome := p.ask("rm -rf build")
	req := collect(t, c1, isType(EvPermissionRequest))
	pr := req[len(req)-1]
	if pr.Wire == nil || string(pr.Wire.RPCID) == "" || pr.Wire.Off <= committed {
		t.Fatalf("permission.request wire: %+v", pr.Wire)
	}
	frameAt(t, p.out.bytes(), pr.Wire.Off, MRequestPermission)
	st := c1.State()
	pend := perms1.List()
	if len(pend) != 1 || string(pend[0].RPCID()) != string(pr.Wire.RPCID) || string(st.PromptRPC) != `"h7.1-3"` || st.Turn != 1 {
		t.Fatalf("state %+v, pending %+v", st, pend)
	}
	b, err := json.Marshal(st) // what an embedder stores
	if err != nil {
		t.Fatal(err)
	}
	var saved SessionState
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	pb, _ := json.Marshal(pend[0])
	rpc := append(json.RawMessage(nil), pend[0].RPCID()...)

	// the handoff: the first process goes, the agent works on
	f1.detach()
	collect(t, c1, func(Event) bool { return false }) // its stream ends
	p.chunk("while away")
	nframes := len(p.seen())

	perms2 := NewPermissions()
	var restored Pending
	_ = json.Unmarshal(pb, &restored)
	perms2.Restore(restored, rpc)
	perms2.SetRules(perms1.Rules())
	c2, _, err := p.start(ClientOptions{IDPrefix: "h7.2", Attach: &saved}, perms2, committed, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	es = collect(t, c2, isText("while away"))
	if es[0].Type != EvStatus || data(es[0])["status"] != StatusWaiting {
		t.Fatalf("attached status: %s %s", types(es), es[0].Data)
	}
	for _, e := range es {
		if e.Type == EvPermissionRequest {
			t.Fatal("the request read again was filed twice")
		}
	}
	if last := es[len(es)-1]; last.Wire.Off <= pr.Wire.Off {
		t.Fatalf("offsets go on from the attach point: %d after %d", last.Wire.Off, pr.Wire.Off)
	}
	if got := len(p.seen()); got != nframes {
		t.Fatalf("attaching sent the agent %d frames (a handshake?)", got-nframes)
	}
	if perms2.Count() != 1 || c2.Status() != StatusWaiting {
		t.Fatalf("pending %d, status %s", perms2.Count(), c2.Status())
	}

	// the restored permission is answered to the agent's original request
	res, err := perms2.Resolve(restored.PID, "", AllowOnce, "user:a")
	if err != nil {
		t.Fatal(err)
	}
	if err := c2.RespondPermission(res); err != nil {
		t.Fatal(err)
	}
	select {
	case o := <-outcome:
		if o.Outcome != "selected" || o.OptionID != "once" {
			t.Fatalf("the agent got %+v", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the agent never got its answer")
	}
	// the predecessor's prompt ends the turn here
	p.end("end_turn")
	es = collect(t, c2, isType(EvTurnEnd))
	end := es[len(es)-1]
	if d := data(end); d["turn"] != float64(1) || d["stopReason"] != "end_turn" || string(end.Wire.RPCID) != `"h7.1-3"` {
		t.Fatalf("turn.end %s %+v", end.Data, end.Wire)
	}
	frameAt(t, p.out.bytes(), end.Wire.Off, `"end_turn"`)
	collect(t, c2, idle)
	if st := c2.State(); st.PromptRPC != nil {
		t.Fatalf("no prompt in flight: %s", st.PromptRPC)
	}

	// prompting on: a new turn, an id of this generation
	if err := c2.Prompt(context.Background(), Prompt{Text: "next"}); err != nil {
		t.Fatal(err)
	}
	collect(t, c2, isText("working"))
	p.end("end_turn")
	es = collect(t, c2, isType(EvTurnEnd))
	if d := data(es[len(es)-1]); d["turn"] != float64(2) {
		t.Fatalf("second turn %v", d)
	}
	ids := map[string]string{}
	for _, m := range p.seen() {
		if !m.IsRequest() {
			continue
		}
		if prev, dup := ids[idKey(m.ID)]; dup {
			t.Fatalf("id %s used for %s and %s", m.ID, prev, m.Method)
		}
		ids[idKey(m.ID)] = m.Method
	}
	if ids[`"h7.2-1"`] != MSessionPrompt || ids[`"h7.1-1"`] != MInitialize {
		t.Fatalf("ids: %v", ids)
	}
}

// A turn cancelled after the handoff: the attached client knows the turn
// runs (Cancel reaches the agent), and the successor's Wire offsets count
// from Process.Off.
func TestReattachThenCancel(t *testing.T) {
	p := newPeer(t)
	c1, f1, err := p.start(ClientOptions{IDPrefix: "a.1"}, NewPermissions(), 0, Config{})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, c1, idle)
	_ = c1.Prompt(context.Background(), Prompt{Text: "go"})
	w := collect(t, c1, isText("working"))
	off := w[len(w)-1].Wire.Off
	st := c1.State()
	f1.detach()
	collect(t, c1, func(Event) bool { return false })

	c2, _, err := p.start(ClientOptions{IDPrefix: "a.2", Attach: &st}, NewPermissions(), off, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if es := collect(t, c2, isType(EvStatus)); data(es[0])["status"] != StatusRunning {
		t.Fatalf("attached mid-turn: %s", es[0].Data)
	}
	if err := c2.Prompt(context.Background(), Prompt{Text: "again"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("a turn runs: %v", err)
	}
	if err := c2.Cancel(); err != nil {
		t.Fatal(err)
	}
	es := collect(t, c2, isType(EvTurnEnd))
	if d := data(es[len(es)-1]); d["stopReason"] != "cancelled" || d["turn"] != float64(1) {
		t.Fatalf("cancelled turn: %v", d)
	}
}

// Without a prefix, ids are numbers as they always were; an adopted id is
// never reused while it waits; string ids match however the peer escapes
// them; the loop's end fails what still waits.
func TestConnIDsAndExpect(t *testing.T) {
	agentOut, clientIn := newStream(), newStream()
	c := NewConn(agentOut.follow(0), clientIn)
	adopted := c.Expect(json.RawMessage(`2`))
	str := c.Expect(json.RawMessage(`"h1.1-9"`))
	go func() { _ = c.Serve() }()
	done := make(chan error, 3)
	call := func(method string) { go func() { done <- c.Call(method, nil, nil) }() }
	call("a")
	call("b")
	d := NewDecoder(clientIn.follow(0))
	ids := map[string]bool{}
	for i := 0; i < 2; i++ {
		m, err := d.Next()
		if err != nil {
			t.Fatal(err)
		}
		ids[string(m.ID)] = true
	}
	if !ids["1"] || !ids["3"] {
		t.Fatalf("ids %v: numbers, skipping the adopted 2", ids)
	}
	for _, id := range []string{"3", "2", "1"} {
		_ = Encode(agentOut, &Message{ID: json.RawMessage(id), Result: json.RawMessage(`{"id":` + id + `}`)})
	}
	_, _ = agentOut.Write([]byte(`{"jsonrpc":"2.0","id":"h1.1\u002d9","result":{"s":1}}` + "\n"))
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	for want, ch := range map[string]<-chan *Message{`{"id":2}`: adopted, `{"s":1}`: str} {
		select {
		case m := <-ch:
			if m == nil || string(m.Result) != want {
				t.Fatalf("adopted response %+v, want %s", m, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("an adopted call's response never came")
		}
	}
	call("c")
	if _, err := d.Next(); err != nil {
		t.Fatal(err)
	}
	agentOut.close()
	if err := <-done; !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("a call waiting when the loop ends: %v", err)
	}
	if _, ok := <-c.Expect(json.RawMessage(`5`)); ok {
		t.Fatal("an Expect after the loop's end is closed at once")
	}
}

// session/load's replay is flagged, what follows its answer is not; every
// frame-caused event's offset is its own frame's end.
func TestReplayFlagAndOffsets(t *testing.T) {
	p := newPeer(t)
	c, _, err := p.start(ClientOptions{}, NewPermissions(), 0, Config{ResumeID: "s-old"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	es := collect(t, c, isText("live"))
	buf := p.out.bytes()
	var replayed, live []string
	var last int64
	for _, e := range es {
		if e.Type != EvMessageDelta {
			continue
		}
		if e.Wire == nil || e.Wire.Off <= last {
			t.Fatalf("%s: wire %+v after %d", e.Data, e.Wire, last)
		}
		last = e.Wire.Off
		text := data(e)["text"].(string)
		frameAt(t, buf, e.Wire.Off, text)
		if e.Wire.Replay {
			replayed = append(replayed, text)
		} else {
			live = append(live, text)
		}
	}
	if strings.Join(replayed, "|") != "old question|old answer" || strings.Join(live, "|") != "live" {
		t.Fatalf("replayed %v, live %v", replayed, live)
	}
	// the default client's ids stay numbers
	for _, m := range p.seen() {
		if m.IsRequest() && strings.HasPrefix(string(m.ID), `"`) {
			t.Fatalf("a string id without a prefix: %s", m.ID)
		}
	}
}
