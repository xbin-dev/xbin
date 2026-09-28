package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

// logOf numbers events 1.. as the Log would.
func logOf(evs ...Event) []Event {
	for i := range evs {
		evs[i].Seq, evs[i].TS = uint64(i+1), int64(i+1)
	}
	return evs
}

func ev(typ string, data any) Event { return New(typ, data) }

func safeIdx(evs []Event, open Open) []int {
	ms := make([]evMeta, len(evs))
	for i, e := range evs {
		ms[i] = metaOf(e)
	}
	var out []int
	for i, ok := range safeCuts(ms, open) {
		if ok {
			out = append(out, i)
		}
	}
	return out
}

// A page never starts inside a card: a message run, a tool call and its
// updates, a request and its answer, a thought and what ends it, a turn's
// plan, a turn's end and its files.changed, a subagent and everything under
// it — and nothing that may still change is cut off (the running turn's
// open call).
func TestSafeCuts(t *testing.T) {
	evs := logOf(
		ev(EvStatus, map[string]any{"status": "running"}),                                  // 0
		ev(EvMessageDelta, map[string]any{"role": "user", "text": "go"}),                   // 1 ← turn 1
		ev(EvThoughtDelta, map[string]any{"text": "hm"}),                                   // 2
		ev(EvStatus, map[string]any{"status": "running", "usage": 1}),                      // 3 (a status ends no thought)
		ev(EvThoughtDelta, map[string]any{"text": "m"}),                                    // 4
		ev(EvMessageDelta, map[string]any{"role": "agent", "messageId": "a", "text": "x"}), // 5 ends the thought
		ev(EvMessageDelta, map[string]any{"role": "agent", "messageId": "a", "text": "y"}), // 6
		ev(EvPlan, map[string]any{"entries": []any{}}),                                     // 7
		ev(EvToolCall, map[string]any{"id": "t1", "kind": "edit", "status": "pending"}),    // 8
		ev(EvPermissionRequest, map[string]any{"pid": "p1"}),                               // 9
		ev(EvPermissionResolved, map[string]any{"pid": "p1", "optionId": "ok"}),            // 10
		ev(EvToolUpdate, map[string]any{"id": "t1", "status": "completed"}),                // 11
		ev(EvPlan, map[string]any{"entries": []any{}}),                                     // 12 the turn's plan again
		ev(EvTurnEnd, map[string]any{"turn": 1, "stopReason": "end_turn"}),                 // 13
		ev(evFilesChanged, map[string]any{"turn": 1, "changes": []any{}}),                  // 14 before turn 1's marker
		ev(EvStatus, map[string]any{"status": "idle"}),                                     // 15
		ev(EvMessageDelta, map[string]any{"role": "user", "text": "more"}),                 // 16 ← turn 2
		ev(EvToolCall, map[string]any{"id": "S", "subagent": true, "status": "in_progress"}),
		ev(EvThoughtDelta, map[string]any{"parent": "S", "text": "inner"}),                      // 18
		ev(EvToolCall, map[string]any{"id": "r", "parent": "S", "status": "in_progress"}),       // 19
		ev(EvMessageDelta, map[string]any{"role": "agent", "text": "between"}),                  // 20 main thread, inside S's span
		ev(EvToolUpdate, map[string]any{"id": "r", "parent": "S", "status": "completed"}),       // 21 nested: touches S
		ev(EvToolUpdate, map[string]any{"id": "S", "status": "completed"}),                      // 22
		ev(EvMessageDelta, map[string]any{"role": "agent", "messageId": "b", "text": "done"}),   // 23
		ev(EvToolCall, map[string]any{"id": "t2", "kind": "execute", "status": "completed"}),    // 24
		ev(evFilesChanged, map[string]any{"toolCallId": "t2", "changes": []any{}}),              // 25 its snapshot
		ev(EvMessageDelta, map[string]any{"role": "agent", "messageId": "c", "text": "and"}),    // 26
		ev(EvTurnEnd, map[string]any{"turn": 2, "stopReason": "end_turn"}),                      // 27
		ev(EvMessageDelta, map[string]any{"role": "user", "text": "third"}),                     // 28 ← turn 3, running
		ev(EvToolCall, map[string]any{"id": "run", "kind": "execute", "status": "in_progress"}), // 29 open
		ev(EvStatus, map[string]any{"status": "running"}),                                       // 30
	)
	got := safeIdx(evs, Open{})
	want := []int{0, 1, 2, 7, 13, 15, 16, 17, 23, 24, 26, 27, 28, 29}
	// 1: a status touches nothing; 2: before the thought (the user's message
	// is closed by it); 7: after the message run; 8–12 inside the plan's
	// span (it spans the turn); 13: the end marker opens its own span with
	// files.changed; 15, 16: a new turn; 17: the subagent's call; 18–22
	// inside it; 23 a message; 24 the call; 25 its snapshot is inside;
	// 26 the next message; 27 the end; 28 the running turn; 29 its open
	// call — after it nothing (30 is inside it).
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("safe cuts\n got %v\nwant %v", got, want)
	}

	// the snapshots still to come keep their call or turn open
	withOpen := safeIdx(evs[:28], Open{Tools: map[string]bool{"t2": true}})
	if last := withOpen[len(withOpen)-1]; last != 24 {
		t.Fatalf("a pending snapshot of t2 keeps it open to the end: last cut %d, want 24 (%v)", last, withOpen)
	}
	withTurn := safeIdx(evs[:16], Open{Turns: map[uint64]bool{1: true}})
	if last := withTurn[len(withTurn)-1]; last != 13 {
		t.Fatalf("a pending turn snapshot keeps the turn's end open: last cut %d, want 13 (%v)", last, withTurn)
	}
	// an unanswered request is open while its turn runs
	pend := logOf(ev(EvMessageDelta, map[string]any{"role": "user", "text": "go"}), ev(EvPermissionRequest, map[string]any{"pid": "p"}),
		ev(EvStatus, map[string]any{"status": "waiting_permission"}))
	if got := safeIdx(pend, Open{}); !reflect.DeepEqual(got, []int{0, 1}) {
		t.Fatalf("an unanswered request: cuts %v, want [0 1]", got)
	}
}

// turns builds n turns: a prompt, a two-delta answer, a call and its update,
// the end.
func turns(n int) []Event {
	var evs []Event
	for i := 1; i <= n; i++ {
		evs = append(evs,
			ev(EvMessageDelta, map[string]any{"role": "user", "text": fmt.Sprint("prompt ", i)}),
			ev(EvMessageDelta, map[string]any{"role": "agent", "messageId": fmt.Sprint("m", i), "text": "a"}),
			ev(EvMessageDelta, map[string]any{"role": "agent", "messageId": fmt.Sprint("m", i), "text": "b"}),
			ev(EvToolCall, map[string]any{"id": "t", "status": "in_progress"}),
			ev(EvToolUpdate, map[string]any{"id": "t", "status": "completed"}),
			ev(EvTurnEnd, map[string]any{"turn": i, "stopReason": "end_turn"}),
			ev(EvStatus, map[string]any{"status": "idle"}),
		)
	}
	return logOf(evs...)
}

func TestPageOfWalksTheLog(t *testing.T) {
	evs := turns(40) // 280 events
	cuts := map[uint64]bool{}
	for _, i := range safeIdx(evs, Open{}) {
		if i < len(evs) {
			cuts[evs[i].Seq] = true
		}
	}
	var all []Event
	before, pages := uint64(0), 0
	for {
		p := PageOf(evs, nil, before, 20, Open{})
		if len(p.Events) == 0 || len(p.Events) > 20 {
			t.Fatalf("page %d: %d events (limit 20)", pages, len(p.Events))
		}
		if !cuts[p.Events[0].Seq] {
			t.Fatalf("page %d starts at seq %d, not a safe cut", pages, p.Events[0].Seq)
		}
		if pages == 0 && (p.Next != 280 || p.Last != 280) {
			t.Fatalf("the tail page: next %d last %d", p.Next, p.Last)
		}
		if p.HasOlder != (p.NextBefore != 0) || (p.HasOlder && p.NextBefore != p.Events[0].Seq) {
			t.Fatalf("page %d: hasOlder %v nextBefore %d first %d", pages, p.HasOlder, p.NextBefore, p.Events[0].Seq)
		}
		all = append(append([]Event{}, p.Events...), all...)
		pages++
		if !p.HasOlder {
			if p.Truncated {
				t.Fatal("the whole log is in the ring: not truncated")
			}
			break
		}
		before = p.NextBefore
	}
	if !reflect.DeepEqual(all, evs) || pages < 14 {
		t.Fatalf("the pages put together are the log: %d events in %d pages", len(all), pages)
	}
	// limits: 0 → the default, above the cap → the cap; a limit past the log → all of it
	if p := PageOf(evs, nil, 0, 0, Open{}); len(p.Events) > DefaultPageLimit || !p.HasOlder {
		t.Fatalf("default limit: %d events", len(p.Events))
	}
	if p := PageOf(evs, nil, 0, 1<<30, Open{}); len(p.Events) != len(evs) || p.HasOlder || p.NextBefore != 0 {
		t.Fatalf("a limit past the log: %d events, older %v", len(p.Events), p.HasOlder)
	}
	// before past the end is the tail; a card longer than the limit is kept whole
	if p := PageOf(evs, nil, 9999, 20, Open{}); p.Next != 280 {
		t.Fatalf("before past the end: next %d", p.Next)
	}
	long := []Event{ev(EvMessageDelta, map[string]any{"role": "user", "text": "go"}), ev(EvToolCall, map[string]any{"id": "x", "status": "in_progress"})}
	for i := 0; i < 50; i++ {
		long = append(long, ev(EvToolUpdate, map[string]any{"id": "x", "outputDelta": "line\n"}))
	}
	long = logOf(append(long, ev(EvToolUpdate, map[string]any{"id": "x", "status": "completed"}))...)
	if p := PageOf(long, nil, 0, 10, Open{}); len(p.Events) != 52 || p.Events[0].Seq != 2 || p.NextBefore != 2 {
		t.Fatalf("a call longer than the limit: a page of %d from seq %d (want 52 from 2)", len(p.Events), p.Events[0].Seq)
	}
	if p := PageOf(nil, nil, 0, 10, Open{}); p.Events == nil || len(p.Events) != 0 || p.HasOlder || p.Truncated {
		t.Fatalf("an empty log: %+v", p)
	}
}

// The state header: what a fold of the events before the page holds —
// each status field from the last status that carried it, login only from
// the last one, the last turn's usage and number.
func TestPageState(t *testing.T) {
	evs := logOf(
		ev(EvStatus, map[string]any{"status": "starting"}),
		ev(EvStatus, map[string]any{"status": "idle", "currentMode": "ask", "options": []any{map[string]any{"id": "model"}}, "modes": []any{"ask"},
			"commands": []any{map[string]any{"name": "review"}}, "login": map[string]any{"needed": true}}),
		ev(EvMessageDelta, map[string]any{"role": "user", "text": "hi"}),
		ev(EvStatus, map[string]any{"status": "running", "detail": "thinking"}),
		ev(EvTurnEnd, map[string]any{"turn": 1, "stopReason": "end_turn", "usage": map[string]any{"used": 5, "size": 10}}),
		ev(EvStatus, map[string]any{"status": "idle", "title": "a name"}),
		ev(EvMessageDelta, map[string]any{"role": "user", "text": "again"}),
		ev(EvStatus, map[string]any{"status": "running"}),
		ev(EvTurnEnd, map[string]any{"turn": 2, "stopReason": "end_turn"}),
	)
	p := PageOf(evs, nil, 7, 1, Open{}) // the page of seq 6 (the second prompt's status): state as of seq 6
	if len(p.Events) != 1 || p.Events[0].Seq != 6 {
		t.Fatalf("page: %+v", p.Events)
	}
	var st map[string]any
	if err := json.Unmarshal(p.State.Status, &st); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"status": "running", "detail": "thinking", "currentMode": "ask", "options": []any{map[string]any{"id": "model"}},
		"modes": []any{"ask"}, "commands": []any{map[string]any{"name": "review"}}}
	if !reflect.DeepEqual(st, want) {
		t.Fatalf("status digest\n got %v\nwant %v", st, want)
	}
	if p.State.Turn != 1 || string(p.State.Usage) != `{"size":10,"used":5}` {
		t.Fatalf("turn %d usage %s", p.State.Turn, p.State.Usage)
	}
	if p := PageOf(evs, nil, 2, 5, Open{}); p.State.Status != nil || p.State.Turn != 0 || p.HasOlder {
		t.Fatalf("the first page has an empty state: %+v", p.State)
	}
}

// The ring's pages: the metas kept beside the ring stay aligned as it trims,
// and a ring that dropped its oldest events says so on its oldest page.
func TestLogPage(t *testing.T) {
	l := NewLog(30, 0)
	src := turns(10)
	for _, e := range src[:20] {
		l.Append(Event{Type: e.Type, Data: e.Data})
	}
	if p := l.Page(0, 10, Open{}); !p.HasOlder || p.Truncated {
		t.Fatalf("before trimming: %+v", p)
	}
	for _, e := range src[20:] {
		l.Append(Event{Type: e.Type, Data: e.Data})
	}
	kept, _ := l.Since(0)
	for _, lim := range []int{5, 10, 30} {
		got, want := l.Page(0, lim, Open{}), PageOf(kept, nil, 0, lim, Open{})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("limit %d: the ring's page differs from a fresh cut\n got %+v\nwant %+v", lim, got, want)
		}
	}
	p := l.Page(0, 1000, Open{})
	if p.HasOlder || !p.Truncated || p.Events[0].Seq != 41 {
		t.Fatalf("the oldest page of a trimmed ring: older %v truncated %v from %d", p.HasOlder, p.Truncated, p.Events[0].Seq)
	}
}

func TestParsePageQuery(t *testing.T) {
	has := func(keys ...string) func(string) bool {
		return func(k string) bool {
			for _, x := range keys {
				if x == k {
					return true
				}
			}
			return false
		}
	}
	if _, _, ok := ParsePageQuery("", "", has("since")); ok {
		t.Fatal("since alone is the replay, not a page")
	}
	if b, n, ok := ParsePageQuery("12", "", has("before")); !ok || b != 12 || n != 0 {
		t.Fatal("before alone")
	}
	if b, n, ok := ParsePageQuery("", "50", has("limit")); !ok || b != 0 || n != 50 {
		t.Fatal("limit alone: the tail")
	}
}
