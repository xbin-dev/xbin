package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// While the agent's switch is off — xbind says which, by refusing the
// agent's read of its state — a person's message is answered without the
// model: a line in the transcript naming the switch, who can change it and
// where; nothing queued, nothing sent; the words stay with the person (409
// kept). Sent again once the switch is back, it runs — and only then.
func TestWsOffAnswersWithoutTheModel(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	f := fakeOf(fx.ag)
	post := func(text string) map[string]any {
		t.Helper()
		var out map[string]any
		w := fx.call(t, wsAlice, "POST", fmt.Sprintf("/runs/%d/message", s.Run), map[string]any{"text": text})
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		out["status"] = w.Code
		return out
	}
	lastOff := func() string {
		t.Helper()
		var d string
		_ = fx.ag.db.q.QueryRow(`SELECT detail FROM steps WHERE run_id=? AND kind='off' ORDER BY id DESC LIMIT 1`, s.Run).Scan(&d)
		return d
	}
	users := func() int {
		return int(fx.ag.db.wsOne(`SELECT count(*) FROM messages WHERE run_id=? AND role='user'`, s.Run))
	}
	before, calls := users(), len(f.callsFor(s.Run))
	for _, c := range []struct{ off, words, who string }{
		{"paused", "Your agent is paused", "only you can resume it: Resume in the workspace agent's dock"},
		{"workspace-off", "Workspace agents are turned off", "A workspace admin can turn them on again"},
		{"no-agent", "You haven't been given a workspace agent", "An admin chooses who has one"},
		{"held", "Your agent is held on this xbin", "An admin can see why and clear it"},
	} {
		fx.x.mu.Lock()
		fx.x.paused, fx.x.off = c.off == "paused", map[bool]string{true: "", false: c.off}[c.off == "paused"]
		fx.x.mu.Unlock()
		wsOffForget()
		got := post("what's overdue? (" + c.off + ")")
		if got["status"] != 409 || got["code"] != "agent-off" || got["refusal"] != c.off || got["kept"] != true || !strings.Contains(fmt.Sprint(got["error"]), c.words) {
			t.Fatalf("%s: the answer to the message: %v", c.off, got)
		}
		if d := lastOff(); !strings.Contains(d, c.words) || !strings.Contains(d, c.who) || !strings.Contains(d, "Nothing was sent to the agent or its model") || !strings.Contains(d, `"switch":"`+c.off+`"`) {
			t.Fatalf("%s: the transcript's line: %s", c.off, d)
		}
		if users() != before || len(f.callsFor(s.Run)) != calls || fx.ag.db.wsOne(`SELECT count(*) FROM inbox WHERE run_id=? AND delivered_at=0`, s.Run) != 0 {
			t.Fatalf("%s: the message was kept, queued or sent to the model", c.off)
		}
		// a turn it is woken for meets the switch before the model does
		if err := wsOffRefuses(LLMRequest{Run: s.Run}); err == nil || !strings.Contains(err.Error(), c.words) || !strings.Contains(err.Error(), "Nothing was sent to the model") {
			t.Fatalf("%s: a model call: %v", c.off, err)
		}
	}
	// back on: nothing runs by itself; sent again, it runs — once
	fx.x.mu.Lock()
	fx.x.paused, fx.x.off = false, ""
	fx.x.mu.Unlock()
	wsOffForget()
	f.on(forRun(s.Run, lastUser("again")), say("Done: again")).once()
	if got := post("again"); got["status"] != 200 {
		t.Fatalf("a message once the agent runs: %v", got)
	}
	fx.idle(t, s)
	if n := users(); n != before+1 {
		t.Fatalf("%d messages reached the conversation after the switch came back, want only the one sent again", n-before)
	}
	for _, c := range f.callsFor(s.Run)[calls:] {
		for _, m := range c.Msgs {
			if strings.Contains(asString(m.Content), "what's overdue?") {
				t.Fatalf("the model was sent a message from while the agent was off: %q", asString(m.Content))
			}
		}
	}
}

// The empty state of a session whose agent can't act — its person (an
// admin, say) wasn't given one, or it is paused — says so first, and lists
// nothing it could do.
func TestWsEmptyStateSaysTheSwitch(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	var e struct {
		Line, Off string
		Can       []string
		Starters  []map[string]any
	}
	fx.json(t, fx.call(t, wsAlice, "GET", fmt.Sprintf("/ws/empty?session=%d", s.Run), nil), 200, &e)
	if e.Off != "" || len(e.Can) == 0 || strings.Contains(e.Line, "asks you before anything changes") {
		t.Fatalf("the empty state while the agent runs: %+v", e)
	}
	fx.x.mu.Lock()
	fx.x.off = "no-agent"
	fx.x.mu.Unlock()
	e.Off, e.Can = "", nil
	fx.json(t, fx.call(t, wsAlice, "GET", fmt.Sprintf("/ws/empty?session=%d", s.Run), nil), 200, &e)
	if e.Off != "no-agent" || len(e.Can) != 0 || len(e.Starters) != 0 || !strings.Contains(e.Line, "An admin chooses who has one") {
		t.Fatalf("the empty state without an agent: %+v", e)
	}
}
