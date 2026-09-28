package term

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

// A command's output streamed as hundreds of chunks is logged as a few
// events (D130) — the fold's output is the same text — and the log pages:
// the tail page carries the request waiting now, and the running call is
// never cut from its updates.
func TestAgentOutputCoalescedAndPaged(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	ctx := context.Background()
	info, code, err := m.OpenAgent(auth.Principal{Owner: true}, "apps/x", "", "fake", "", "", "", nil)
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	id := info.ID
	r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	if _, err := m.AgentPrompt(ctx, id, "chatty 300"); err != nil {
		t.Fatal(err)
	}
	r.until(t, ofType(agent.EvTurnEnd))
	r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	evs, _, _, _ := m.AgentEvents(id, 0)
	var out strings.Builder
	chunks := 0
	for _, e := range evs {
		d := edata(e)
		if e.Type == agent.EvToolUpdate && d["id"] == "chatty" && d["outputDelta"] != nil {
			chunks++
			out.WriteString(d["outputDelta"].(string))
		}
	}
	var want strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&want, "line %d\n", i)
	}
	if out.String() != want.String() {
		t.Fatalf("the coalesced output differs from the chunks sent (%d bytes vs %d)", out.Len(), want.Len())
	}
	if chunks == 0 || chunks > 60 {
		t.Fatalf("300 chunks logged as %d events; want a few", chunks)
	}

	// the tail page of a small limit still holds the whole call: its start
	// is the first safe cut at or before the limit
	p, err := m.AgentPage(id, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Events) == 0 || p.Next != evs[len(evs)-1].Seq || !p.HasOlder || p.NextBefore != p.Events[0].Seq {
		t.Fatalf("tail page: %d events, next %d, older %v, nextBefore %d", len(p.Events), p.Next, p.HasOlder, p.NextBefore)
	}
	// a pending permission is in the tail page and in its state header
	if _, err := m.AgentPrompt(ctx, id, "perm"); err != nil {
		t.Fatal(err)
	}
	req := r.until(t, ofType(agent.EvPermissionRequest))
	p, _ = m.AgentPage(id, 0, 1)
	if len(p.State.Permissions) != 1 || p.Events[0].Seq > req.Seq {
		t.Fatalf("a waiting request: state %+v, page from %d (request %d)", p.State.Permissions, p.Events[0].Seq, req.Seq)
	}
	older, _ := m.AgentPage(id, p.NextBefore, 1000)
	if older.State.Permissions != nil || older.Next != p.NextBefore-1 || older.HasOlder {
		t.Fatalf("an older page: %+v", older)
	}
	if _, err := m.AgentPage("nope", 0, 10); err != ErrNoSession {
		t.Fatalf("unknown session: %v", err)
	}
}

// ListHistory reads a file's head: the meta, not the transcript.
func TestHistoryMetaFromHead(t *testing.T) {
	meta := HistoryMeta{ID: "h1", Cwd: "apps/x", Provider: "fake", Turns: 3, Preview: "hi"}
	var evs []agent.Event
	for i := 0; i < 20000; i++ {
		evs = append(evs, agent.Event{Seq: uint64(i + 1), Type: agent.EvMessageDelta, Data: json.RawMessage(`{"role":"agent","text":"` + strings.Repeat("x", 200) + `"}`)})
	}
	b, _ := json.Marshal(historyFile{Meta: meta, Events: evs})
	cr := &countingReader{r: bytes.NewReader(b)}
	got, ok := historyMetaFrom(cr)
	if !ok || got != meta {
		t.Fatalf("meta: %+v %v", got, ok)
	}
	if cr.n > 64<<10 || len(b) < 4<<20 {
		t.Fatalf("read %d bytes of a %d-byte file to list it", cr.n, len(b))
	}
	// a file with its events first (not what saveHistory writes) still reads
	mb, _ := json.Marshal(meta)
	if got, ok := historyMetaFrom(strings.NewReader(`{"events":[{"seq":1,"type":"status","data":{"x":[1,{"y":2}]}}],"meta":` + string(mb) + `}`)); !ok || got != meta {
		t.Fatalf("events first: %+v %v", got, ok)
	}
	for _, bad := range []string{``, `[]`, `{"events":[]}`, `{"meta":{"cwd":"x"}}`, `{"meta":`} {
		if _, ok := historyMetaFrom(strings.NewReader(bad)); ok {
			t.Fatalf("%q read as a meta", bad)
		}
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// The snapshots still to come are open to a page cut: marked before the
// event that asks for them is logged, held while queued, dropped when done.
func TestSnapperPendingMarks(t *testing.T) {
	s := &snapper{pending: map[string]int{}, kinds: map[string]string{}, settled: map[string]bool{}, jobs: make(chan diffJob, 4)}
	done := agent.New(agent.EvToolUpdate, map[string]any{"id": "t1", "kind": "execute", "status": "completed"})
	s.expect(done)
	if o := s.Open(); !o.Tools["t1"] {
		t.Fatalf("expected before logging: %+v", o)
	}
	s.observe(done)
	if o := s.Open(); !o.Tools["t1"] || s.pending["tool:t1"] != 1 {
		t.Fatalf("queued: %+v %v", o, s.pending)
	}
	s.expect(done) // settled: no second job
	s.observe(done)
	if s.pending["tool:t1"] != 1 {
		t.Fatalf("a settled call asks nothing more: %v", s.pending)
	}
	j := <-s.jobs
	s.settle(j.key()) // the worker ran it
	end := agent.New(agent.EvTurnEnd, map[string]any{"turn": 3})
	s.observe(end) // observe alone (the tests' path) holds only the job's own mark
	if o := s.Open(); len(o.Tools) != 0 || !o.Turns[3] || len(s.pending) != 1 {
		t.Fatalf("after the call's job ran, the turn's is pending: %+v %v", o, s.pending)
	}
	s.settle((<-s.jobs).key())
	if len(s.pending) != 0 {
		t.Fatalf("nothing pending: %v", s.pending)
	}
	var nilSnap *snapper
	if o := nilSnap.Open(); o.Tools == nil || len(o.Tools) != 0 {
		t.Fatal("no snapper: nothing open")
	}
}
