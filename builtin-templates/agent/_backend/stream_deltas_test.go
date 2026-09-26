package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func textEv(run int64, text string, started int64) *Event {
	return &Event{Type: evText, Run: run, Root: run, key: "text:" + itoa(run),
		Data: map[string]any{"text": text, "model": "m", "started": started}}
}

func thinkEv(run int64, text string) *Event {
	return &Event{Type: evThinking, Run: run, Root: run, key: "thinking:" + itoa(run),
		Data: map[string]any{"text": text, "started": int64(5)}}
}

// applyDraft is what a deltas client does with one draft event: a full event
// replaces, a delta appends at `at` — and a mismatch means it is out of step.
func applyDraft(t *testing.T, cur map[string]string, ev *Event) {
	t.Helper()
	b, _ := json.Marshal(ev) // what goes on the wire
	var wire struct {
		Type string `json:"type"`
		Run  int64  `json:"run"`
		Data struct {
			Text  *string `json:"text"`
			Delta string  `json:"delta"`
			At    int     `json:"at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	kind, isDelta := strings.CutSuffix(wire.Type, ".delta")
	key := kind + ":" + itoa(wire.Run)
	if !isDelta {
		cur[key] = *wire.Data.Text
		return
	}
	have := cur[key]
	if n := len(utf16.Encode([]rune(have))); n != wire.Data.At {
		t.Fatalf("%s: delta at %d but the client holds %d units (%q)", key, wire.Data.At, n, have)
	}
	cur[key] = have + wire.Data.Delta
}

// A deltas connection writes what each draft event appended; the client
// rebuilds exactly the text a full-text client sees, whatever the provider
// did — extend, rewrite, start a new call.
func TestDeltaRewrite(t *testing.T) {
	s := deltaState{}
	cur := map[string]string{}
	step := func(ev *Event, wantType string) *Event {
		t.Helper()
		out := s.rewrite(ev)
		if out.Type != wantType {
			t.Fatalf("event %v → %s, want %s", ev.Data, out.Type, wantType)
		}
		applyDraft(t, cur, out)
		if full, _ := ev.Data.(map[string]any)["text"].(string); cur[ev.Type+":"+itoa(ev.Run)] != full {
			t.Fatalf("client holds %q, the draft is %q", cur[ev.Type+":"+itoa(ev.Run)], full)
		}
		return out
	}
	step(textEv(7, "", 1), "text") // the call starts: full, carries model/started
	if d := step(textEv(7, "Hel", 1), "text.delta").Data.(map[string]any); d["delta"] != "Hel" || d["at"] != 0 {
		t.Fatalf("first piece: %v", d)
	}
	if d := step(textEv(7, "Hello", 1), "text.delta").Data.(map[string]any); d["delta"] != "lo" || d["at"] != 3 {
		t.Fatalf("second piece: %v", d)
	}
	// UTF-16 offsets: an astral emoji is two units, é one
	step(textEv(7, "Hello 😀", 1), "text.delta")
	if d := step(textEv(7, "Hello 😀 é", 1), "text.delta").Data.(map[string]any); d["at"] != 8 {
		t.Fatalf("offset after an emoji is in UTF-16 units: %v", d)
	}
	step(textEv(7, "Hello 😀 é", 1), "text")    // nothing appended: sent whole (rare, harmless)
	step(textEv(7, "Goodbye", 1), "text")      // rewritten, not extended: whole
	step(textEv(7, "Goodbye, all", 2), "text") // another call (new start time): whole
	step(textEv(7, "Goodbye, all!", 2), "text.delta")
	step(thinkEv(7, "hmm"), "thinking") // kinds are independent
	step(thinkEv(7, "hmm, so"), "thinking.delta")
	step(textEv(8, "other run", 1), "text") // runs are independent
	step(textEv(7, "Goodbye, all!!", 2), "text.delta")

	// draft.end forgets that run; reset forgets everything
	if out := s.rewrite(&Event{Type: evDraftEnd, Run: 7, Root: 7}); out.Type != evDraftEnd {
		t.Fatal("draft.end passes through")
	}
	step(textEv(7, "Goodbye, all!!!", 2), "text")
	step(thinkEv(7, "hmm, so then"), "thinking")
	step(textEv(8, "other run, more", 1), "text.delta")
	s.rewrite(&Event{Type: evReset})
	step(textEv(8, "other run, more+", 1), "text")

	// everything else is untouched
	for _, ev := range []*Event{{Type: evMessage, Run: 7, Data: map[string]any{"text": "x"}}, {Type: evToolArgs, Run: 7, Data: map[string]any{"args": "{"}}, {Type: evText, Run: 7, Data: "odd"}} {
		if out := s.rewrite(ev); out != ev {
			t.Fatalf("%s was rewritten", ev.Type)
		}
	}
}

// readSSEAt reads a /stream response for a target until stop says so. stop
// may publish more events (it runs on the reading side).
func readSSEAt(t *testing.T, target string, stop func(evs []Event) bool) []Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := httptest.NewRequest("GET", target, nil).WithContext(ctx)
	pr, pw := ioPipe()
	w := &streamRecorder{ResponseRecorder: httptest.NewRecorder(), pw: pw}
	done := make(chan struct{})
	go func() { handleStream(w, r); pw.Close(); close(done) }()
	var evs []Event
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev Event
		if json.Unmarshal([]byte(data), &ev) == nil {
			evs = append(evs, ev)
			if stop(evs) {
				break
			}
		}
	}
	cancel()
	go func() {
		for sc.Scan() {
		}
	}()
	<-done
	return evs
}

// Through the handler: a draft in flight at connect arrives whole, what is
// appended after arrives as deltas, and a client without the flag still gets
// the accumulated text.
func TestStreamDeltasOverSSE(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	id, _ := db.createRun("t", "", 0)
	h := ag.eng.hub
	// a draft the engine holds when the client connects
	ag.eng.mu.Lock()
	ag.eng.drafts[id] = &draft{Run: id, Model: "m", Started: 1, Text: "Once", Tools: map[int]*draftTool{}, root: id}
	ag.eng.mu.Unlock()
	t.Cleanup(func() {
		ag.eng.mu.Lock()
		delete(ag.eng.drafts, id)
		ag.eng.mu.Unlock()
	})
	script := []string{"Once upon", "Once upon a time", "Once upon a time 😀", "Once upon a time 😀 the end"}
	run := func(target string) ([]Event, map[string]string) {
		cur := map[string]string{}
		next := 0
		evs := readSSEAt(t, target, func(evs []Event) bool {
			ev := evs[len(evs)-1]
			if ev.Type == evText || ev.Type == evText+".delta" {
				applyDraft(t, cur, &ev)
				if next == len(script) {
					return true
				}
				// publish the next piece only once the last one arrived, so
				// nothing coalesces and every step is observed
				h.publish(textEv(id, script[next], 1))
				next++
			}
			return false
		})
		return evs, cur
	}

	evs, cur := run(fmt.Sprintf("/stream?run=%d&deltas=1", id))
	if evs[0].Type != "hello" || evs[0].Data.(map[string]any)["deltas"] != true {
		t.Fatalf("hello should say deltas: %+v", evs[0])
	}
	var types []string
	for _, ev := range evs[1:] {
		types = append(types, ev.Type)
	}
	if got := strings.Join(types, " "); got != "text text.delta text.delta text.delta text.delta" {
		t.Fatalf("deltas stream: %s", got)
	}
	if cur["text:"+itoa(id)] != script[len(script)-1] {
		t.Fatalf("rebuilt %q", cur["text:"+itoa(id)])
	}

	// the same run, no flag: accumulated text, as before
	ag.eng.mu.Lock()
	ag.eng.drafts[id].Text = "Once"
	ag.eng.mu.Unlock()
	evs, cur = run(fmt.Sprintf("/stream?run=%d", id))
	if _, ok := evs[0].Data.(map[string]any)["deltas"]; ok {
		t.Fatal("hello without the flag must not change")
	}
	for _, ev := range evs[1:] {
		if ev.Type != evText {
			t.Fatalf("a client without the flag got %s", ev.Type)
		}
	}
	if cur["text:"+itoa(id)] != script[len(script)-1] {
		t.Fatalf("full-text client holds %q", cur["text:"+itoa(id)])
	}
}

// With the real engine: a streamed answer seen through deltas=1 ends as the
// message, and the draft events before draft.end rebuild its text.
func TestStreamDeltasFromTheEngine(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	f := fakeOf(ag)
	f.on(lastUser("tell"), say("It was a dark night")).
		emit(LLMEvent{Kind: "thinking", Text: "plot"}, LLMEvent{Kind: "thinking", Text: "plot twist"},
			LLMEvent{Kind: "text", Text: "It was"}, LLMEvent{Kind: "text", Text: "It was a dark night"}).
		block("g")
	id := newRun(t, ag, Config{}, "tell")
	waitFor(t, "the call", func() bool { return f.inFlight() == 1 })
	cur := map[string]string{}
	released := false
	evs := readSSEAt(t, fmt.Sprintf("/stream?run=%d&deltas=1", id), func(evs []Event) bool {
		ev := evs[len(evs)-1]
		switch ev.Type {
		case evText, evThinking, evText + ".delta", evThinking + ".delta":
			applyDraft(t, cur, &ev)
			if !released && cur["text:"+itoa(id)] == "It was a dark night" {
				released = true
				f.release("g")
			}
		}
		return ev.Type == evMessage && ev.Data.(map[string]any)["role"] == "assistant"
	})
	if !hasEvent(evs, evDraftEnd, nil) {
		t.Fatal("no draft.end")
	}
	if cur["thinking:"+itoa(id)] != "plot twist" || cur["text:"+itoa(id)] != "It was a dark night" {
		t.Fatalf("rebuilt drafts: %v", cur)
	}
}

func toolEv(run int64, index int, id, name, args string) *Event {
	return &Event{Type: evToolArgs, Run: run, Root: run, key: "tool:" + itoa(run) + ":" + itoa(int64(index)),
		Data: map[string]any{"index": index, "id": id, "name": name, "args": args}}
}

// applyTool is what a deltas client does with a tool-call draft event: a full
// `tool` event replaces the call's arguments, a `tool.delta` appends at `at`.
func applyTool(t *testing.T, cur map[string]string, ev *Event) {
	t.Helper()
	b, _ := json.Marshal(ev)
	var wire struct {
		Type string `json:"type"`
		Run  int64  `json:"run"`
		Data struct {
			Index int     `json:"index"`
			Args  *string `json:"args"`
			Delta string  `json:"delta"`
			At    int     `json:"at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("%d:%d", wire.Run, wire.Data.Index)
	switch wire.Type {
	case evToolArgs:
		cur[key] = *wire.Data.Args
	case evToolArgs + ".delta":
		have := cur[key]
		if n := len(utf16.Encode([]rune(have))); n != wire.Data.At {
			t.Fatalf("tool %s: delta at %d but the client holds %d units (%q)", key, wire.Data.At, n, have)
		}
		cur[key] = have + wire.Data.Delta
	default:
		t.Fatalf("not a tool event: %s", wire.Type)
	}
}

// A tool call's arguments stream as deltas per call index; a new id or name
// (another call at that index), rewritten arguments, draft.end and reset all
// send the call whole again.
func TestToolDeltaRewrite(t *testing.T) {
	s := deltaState{}
	cur := map[string]string{}
	step := func(ev *Event, wantType string) map[string]any {
		t.Helper()
		out := s.rewrite(ev)
		if out.Type != wantType {
			t.Fatalf("event %v → %s, want %s", ev.Data, out.Type, wantType)
		}
		applyTool(t, cur, out)
		d := ev.Data.(map[string]any)
		if key := fmt.Sprintf("%d:%v", ev.Run, d["index"]); cur[key] != d["args"] {
			t.Fatalf("client holds %q, the call's arguments are %q", cur[key], d["args"])
		}
		return out.Data.(map[string]any)
	}
	step(toolEv(7, 0, "c1", "file_write", ""), "tool") // the call starts: whole, carries id and name
	if d := step(toolEv(7, 0, "c1", "file_write", `{"path":`), "tool.delta"); d["delta"] != `{"path":` || d["at"] != 0 || d["index"] != 0 {
		t.Fatalf("first piece: %v", d)
	}
	if d := step(toolEv(7, 0, "c1", "file_write", `{"path":"é😀`), "tool.delta"); d["at"] != 8 {
		t.Fatalf("second piece: %v", d)
	}
	if d := step(toolEv(7, 0, "c1", "file_write", `{"path":"é😀.txt"`), "tool.delta"); d["at"] != 12 {
		t.Fatalf("offsets are UTF-16 units: %v", d)
	}
	if _, ok := step(toolEv(7, 0, "c1", "file_write", `{"path":"é😀.txt","content":"x"`), "tool.delta")["id"]; ok {
		t.Fatal("a delta carries no id or name: they did not change")
	}
	step(toolEv(7, 1, "c2", "shell", `{"cmd"`), "tool") // another index: its own
	step(toolEv(7, 1, "c2", "shell", `{"cmd":"ls"}`), "tool.delta")
	step(toolEv(7, 0, "c1", "file_write", `{"path":"b"}`), "tool")  // rewritten, not extended
	step(toolEv(7, 0, "c3", "file_write", `{"path":"b"}!`), "tool") // a new id at that index
	step(toolEv(7, 0, "c3", "file_read", `{"path":"b"}!!`), "tool") // a new name
	step(toolEv(8, 0, "c9", "shell", `{`), "tool")                  // runs are independent
	if out := s.rewrite(textEv(7, "hi", 1)); out.Type != evText {   // …and kinds
		t.Fatalf("text: %s", out.Type)
	}
	step(toolEv(7, 0, "c3", "file_read", `{"path":"b"}!!!`), "tool.delta")

	// draft.end forgets that run's calls (and only that run's)
	s.rewrite(&Event{Type: evDraftEnd, Run: 7, Root: 7})
	step(toolEv(7, 0, "c3", "file_read", `{"path":"b"}!!!!`), "tool")
	step(toolEv(7, 1, "c2", "shell", `{"cmd":"ls"}+`), "tool")
	step(toolEv(8, 0, "c9", "shell", `{"x`), "tool.delta")
	s.rewrite(&Event{Type: evReset})
	step(toolEv(8, 0, "c9", "shell", `{"x"`), "tool")
}

// Through the handler, with the engine's draft: a call in flight at connect
// arrives whole, what the model appends arrives as tool.delta, and a client
// without the flag gets the whole arguments every time.
func TestStreamToolDeltasOverSSE(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	id, _ := db.createRun("t", "", 0)
	h := ag.eng.hub
	reset := func() {
		ag.eng.mu.Lock()
		ag.eng.drafts[id] = &draft{Run: id, Model: "m", Started: 1, Tools: map[int]*draftTool{
			0: {Index: 0, ID: "c1", Name: "file_write", Args: `{"path"`}}, root: id}
		ag.eng.mu.Unlock()
	}
	reset()
	t.Cleanup(func() {
		ag.eng.mu.Lock()
		delete(ag.eng.drafts, id)
		ag.eng.mu.Unlock()
	})
	script := []string{`{"path":"a.txt"`, `{"path":"a.txt","content":"`, `{"path":"a.txt","content":"😀 done"}`}
	run := func(target string) ([]string, map[string]string) {
		cur := map[string]string{}
		next := 0
		var types []string
		readSSEAt(t, target, func(evs []Event) bool {
			ev := evs[len(evs)-1]
			if ev.Type != evToolArgs && ev.Type != evToolArgs+".delta" {
				return false
			}
			types = append(types, ev.Type)
			applyTool(t, cur, &ev)
			if next == len(script) {
				return true
			}
			h.publish(toolEv(id, 0, "c1", "file_write", script[next]))
			next++
			return false
		})
		return types, cur
	}
	types, cur := run(fmt.Sprintf("/stream?run=%d&deltas=1", id))
	if got := strings.Join(types, " "); got != "tool tool.delta tool.delta tool.delta" {
		t.Fatalf("deltas stream: %s", got)
	}
	if cur[itoa(id)+":0"] != script[len(script)-1] {
		t.Fatalf("rebuilt %q", cur[itoa(id)+":0"])
	}
	reset()
	types, cur = run(fmt.Sprintf("/stream?run=%d", id))
	if got := strings.Join(types, " "); got != "tool tool tool tool" {
		t.Fatalf("a client without the flag: %s", got)
	}
	if cur[itoa(id)+":0"] != script[len(script)-1] {
		t.Fatalf("full client holds %q", cur[itoa(id)+":0"])
	}
}
