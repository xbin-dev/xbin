package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// readSSE collects events from a /stream response until stop says so.
func readSSE(t *testing.T, ag *Agent, runID int64, since string, stop func(evs []Event) bool) []Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	target := fmt.Sprintf("/stream?run=%d", runID)
	if since != "" {
		target += "&since=" + since
	}
	r := httptest.NewRequest("GET", target, nil).WithContext(ctx)
	pr, pw := ioPipe()
	w := &streamRecorder{ResponseRecorder: httptest.NewRecorder(), pw: pw}
	done := make(chan struct{})
	go func() { handleStream(w, r); pw.Close(); close(done) }()
	var evs []Event
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev Event
		if json.Unmarshal([]byte(data), &ev) == nil {
			evs = append(evs, ev)
			if stop(evs) {
				cancel()
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

func hasEvent(evs []Event, typ string, pred func(Event) bool) bool {
	for _, e := range evs {
		if e.Type == typ && (pred == nil || pred(e)) {
			return true
		}
	}
	return false
}

// The view's cursor, handed back to /stream, loses nothing that happened in
// between: the run's answer arrives as events.
func TestSnapshotThenStreamHasNoGap(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	f := fakeOf(ag)
	f.on(lastUser("hello"), say("streamed answer")).block("g")
	id := newRun(t, ag, Config{}, "hello")
	waitFor(t, "the call", func() bool { return f.inFlight() == 1 })
	v, err := ag.eng.runView(id)
	if err != nil {
		t.Fatal(err)
	}
	cursor := v["cursor"].(string)
	f.release("g") // happens between the view and the stream
	evs := readSSE(t, ag, id, cursor, func(evs []Event) bool {
		return hasEvent(evs, evMessage, func(e Event) bool {
			b, _ := json.Marshal(e.Data)
			return strings.Contains(string(b), "streamed answer")
		})
	})
	if hasEvent(evs, evReset, nil) {
		t.Fatal("a fresh cursor produced a reset")
	}
}

// Everything a subagent does arrives on its root's stream, so the parent's
// session can show it inline.
func TestChildEventsReachTheRootStream(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	f := fakeOf(ag)
	f.on(isSubagent, say("CHILD WORDS")).block("child")
	f.on(lastUser("go"), callTools(tc("s", "subagent_spawn", `{"task":"x"}`))).once()
	id := newRun(t, ag, Config{Subagents: true}, "go")
	waitStatus(t, db, id, statusAwait)
	kid := children(db, id)[0].ID
	go func() { time.Sleep(50 * time.Millisecond); f.release("child") }()
	settled := func(e Event) bool {
		b, _ := json.Marshal(e.Data)
		return strings.Contains(string(b), `"state":"done"`)
	}
	evs := readSSE(t, ag, id, "", func(evs []Event) bool {
		return hasEvent(evs, evLink, settled)
	})
	if !hasEvent(evs, evMessage, func(e Event) bool {
		b, _ := json.Marshal(e.Data)
		return e.Run == kid && strings.Contains(string(b), "CHILD WORDS")
	}) {
		t.Fatal("the child's answer never reached the root stream")
	}
}

func TestForeignOrStaleCursorResets(t *testing.T) {
	h := newEventHub("genA")
	if _, _, ok := h.subscribe(1, h.parseCursor("genB.5")); ok {
		t.Fatal("another process's cursor must reset")
	}
	for i := 0; i < ringSize+10; i++ {
		h.publish(&Event{Type: evStep, Run: 1, Root: 1})
	}
	if _, _, ok := h.subscribe(1, 3); ok {
		t.Fatal("an evicted cursor must reset")
	}
	s, missed, ok := h.subscribe(1, h.now()-2)
	if !ok || len(missed) != 2 {
		t.Fatalf("a recent cursor: ok=%v missed=%d", ok, len(missed))
	}
	h.unsubscribe(s)
}

// Drafts coalesce per subscriber: a slow reader sees the latest text, never
// a backlog.
func TestDraftsCoalesce(t *testing.T) {
	h := newEventHub("g")
	s, _, _ := h.subscribe(7, h.now())
	for i := 0; i < 100; i++ {
		h.publish(&Event{Type: evText, Run: 7, Root: 7, key: "text:7", Data: map[string]any{"text": strings.Repeat("x", i)}})
	}
	h.publish(&Event{Type: evMessage, Run: 7, Root: 7})
	q := s.drain()
	if len(q) != 2 || q[0].Type != evText || q[1].Type != evMessage {
		t.Fatalf("queue = %d events", len(q))
	}
	if txt := q[0].Data.(map[string]any)["text"].(string); len(txt) != 99 {
		t.Fatalf("the coalesced draft is not the latest (%d)", len(txt))
	}
}

func ioPipe() (*io.PipeReader, *io.PipeWriter) { return io.Pipe() }

// streamRecorder is a ResponseWriter whose body is a pipe the test reads as
// the handler writes (httptest's recorder only hands the body over at the
// end).
type streamRecorder struct {
	*httptest.ResponseRecorder
	pw *io.PipeWriter
}

func (s *streamRecorder) Write(b []byte) (int, error) { return s.pw.Write(b) }
func (s *streamRecorder) Flush()                      {}
