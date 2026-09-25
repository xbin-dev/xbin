// events.go — the live feed behind GET /stream (stream.go).
//
// Every change the engine commits becomes an event: a run's summary moved, a
// message was added or rewritten, a journal step, the inbox queue, a link,
// and — not durable — the draft of a model call in flight (text, thinking,
// tool arguments). Events carry a process-wide seq; a subscriber gives back
// the last one it saw and gets what it missed from a per-tree ring, or a
// `reset` when the ring no longer has it (or the process changed: the cursor
// is "<gen>.<seq>"), and re-snapshots.
//
// Drafts are the hot path and are COALESCED per subscriber: a draft event
// replaces the not-yet-written one for the same run and kind in place, so a
// client that keeps up sees every token and one that falls behind sees the
// latest text — never a backlog, never a timer.
package main

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

type Event struct {
	Seq  int64  `json:"seq"`
	Type string `json:"type"`
	Run  int64  `json:"run"`
	Root int64  `json:"root"`
	TS   int64  `json:"ts"` // ms
	Data any    `json:"data,omitempty"`
	// key identifies a coalescable draft event ("" = never coalesced).
	key string
}

// Event types.
const (
	evRun      = "run"      // a run's summary row changed
	evMessage  = "message"  // a message added or rewritten (upsert by id)
	evStep     = "step"     // a journal step
	evInbox    = "inbox"    // the run's queued inputs changed
	evLink     = "link"     // a subagent link changed
	evText     = "text"     // draft: accumulated answer text
	evThinking = "thinking" // draft: accumulated reasoning
	evToolArgs = "tool"     // draft: a tool call being written
	evDraftEnd = "draft.end"
	evReset    = "reset"
	evBye      = "bye"
)

const ringSize = 4000

type eventHub struct {
	gen string

	mu    sync.Mutex
	seq   int64
	rings map[int64]*ring // per root
	subs  map[*subscriber]bool
}

type ring struct {
	evs     []*Event
	evicted int64 // highest seq dropped from this ring (0 = none)
	used    time.Time
}

type subscriber struct {
	root int64 // the tree it follows (0 = run list only)
	mu   sync.Mutex
	q    []*Event
	pos  map[string]int // draft key → index in q
	wake chan struct{}
	dead bool
	full bool
}

func newEventHub(gen string) *eventHub {
	return &eventHub{gen: gen, rings: map[int64]*ring{}, subs: map[*subscriber]bool{}}
}

func (h *eventHub) cursor(seq int64) string { return h.gen + "." + strconv.FormatInt(seq, 10) }

// parseCursor returns the seq a cursor names, or -1 when it belongs to
// another process (or is not a cursor).
func (h *eventHub) parseCursor(c string) int64 {
	g, s, ok := strings.Cut(c, ".")
	if !ok || g != h.gen {
		return -1
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func (h *eventHub) now() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}

// publish stamps and fans out one event. Run-list events (evRun of a root)
// reach every subscriber; the rest reach the subscribers of their tree.
func (h *eventHub) publish(ev *Event) {
	h.mu.Lock()
	h.seq++
	ev.Seq = h.seq
	ev.TS = time.Now().UnixMilli()
	if ev.key == "" {
		r := h.rings[ev.Root]
		if r == nil {
			r = &ring{}
			h.rings[ev.Root] = r
		}
		r.evs = append(r.evs, ev)
		if len(r.evs) > ringSize {
			drop := len(r.evs) - ringSize
			r.evicted = r.evs[drop-1].Seq
			r.evs = append([]*Event(nil), r.evs[drop:]...)
		}
		r.used = time.Now()
	}
	var targets []*subscriber
	for s := range h.subs {
		if s.root == ev.Root || (ev.Type == evRun && ev.Run == ev.Root) {
			targets = append(targets, s)
		}
	}
	h.gcLocked()
	h.mu.Unlock()
	for _, s := range targets {
		s.push(ev)
	}
}

// gcLocked forgets rings nobody has touched for a while (a deleted tree).
func (h *eventHub) gcLocked() {
	if len(h.rings) < 256 {
		return
	}
	cut := time.Now().Add(-30 * time.Minute)
	for root, r := range h.rings {
		if r.used.Before(cut) {
			delete(h.rings, root)
		}
	}
}

// subscribe registers a subscriber for a tree and returns what it missed
// since `since` (-1: nothing to replay) — or ok=false when the ring no longer
// holds it and the client must re-snapshot. The replay and the registration
// happen under one lock, so no event falls between them.
func (h *eventHub) subscribe(root, since int64) (s *subscriber, missed []*Event, ok bool) {
	s = &subscriber{root: root, pos: map[string]int{}, wake: make(chan struct{}, 1)}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs[s] = true
	if since < 0 || since > h.seq {
		return s, nil, false // another process's cursor (or garbage)
	}
	if r := h.rings[root]; r != nil {
		if r.evicted > since {
			return s, nil, false // what it missed is no longer held
		}
		for _, ev := range r.evs {
			if ev.Seq > since {
				missed = append(missed, ev)
			}
		}
	}
	// Run summaries of OTHER trees are not replayed: the client re-reads its
	// run list whenever it (re)connects.
	return s, missed, true
}

func (h *eventHub) unsubscribe(s *subscriber) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
	s.close()
}

// closeAll tells every subscriber to go away (shutdown): they get `bye` and
// reconnect — to the successor process.
func (h *eventHub) closeAll() {
	h.mu.Lock()
	subs := make([]*subscriber, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()
	for _, s := range subs {
		s.push(&Event{Type: evBye})
		s.close()
	}
}

// push queues an event for this subscriber, coalescing drafts. The wake-up
// is sent under the lock close() takes, so it can never hit a closed channel.
func (s *subscriber) push(ev *Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return
	}
	if i, ok := s.pos[ev.key]; ev.key != "" && ok {
		s.q[i] = ev
	} else {
		if ev.key != "" {
			s.pos[ev.key] = len(s.q)
		}
		s.q = append(s.q, ev)
		if len(s.q) > 20000 { // a client that stopped reading: make it re-snapshot
			s.q = []*Event{{Type: evReset}}
			s.pos = map[string]int{}
			s.full = true
		}
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// drain takes everything queued.
func (s *subscriber) drain() []*Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.q
	s.q = nil
	s.pos = map[string]int{}
	return q
}

func (s *subscriber) close() {
	s.mu.Lock()
	if !s.dead {
		s.dead = true
		close(s.wake)
	}
	s.mu.Unlock()
}

// --- what the engine publishes ------------------------------------------------

// emitRun publishes a run's summary after the enclosing commit.
func (e *Engine) emitRun(t *DB, runID int64) {
	t.AfterCommit(func() {
		r, err := e.db.getRun(runID)
		if err != nil {
			return
		}
		e.hub.publish(&Event{Type: evRun, Run: r.ID, Root: rootOf(r), Data: runSummary(r)})
	})
}

func (e *Engine) emitMessage(t *DB, root int64, m *Message) {
	t.AfterCommit(func() {
		e.hub.publish(&Event{Type: evMessage, Run: m.RunID, Root: root, Data: messageView(m)})
	})
}

// emitMessageID re-reads a rewritten message (a settled tool result).
func (e *Engine) emitMessageID(t *DB, root, runID, msgID int64) {
	t.AfterCommit(func() {
		if m, err := e.db.messageByID(msgID); err == nil {
			e.hub.publish(&Event{Type: evMessage, Run: runID, Root: root, Data: messageView(m)})
		}
	})
}

func (e *Engine) emitStep(t *DB, root int64, s *Step) {
	if s == nil {
		return
	}
	t.AfterCommit(func() {
		e.hub.publish(&Event{Type: evStep, Run: s.RunID, Root: root, Data: s})
	})
}

func (e *Engine) emitInbox(t *DB, root, runID int64) {
	t.AfterCommit(func() {
		e.hub.publish(&Event{Type: evInbox, Run: runID, Root: root, Data: map[string]any{"queued": e.db.queuedView(runID)}})
	})
}

func (e *Engine) emitLink(t *DB, root, linkID int64) {
	t.AfterCommit(func() {
		if l, err := e.db.getLink(linkID); err == nil {
			e.hub.publish(&Event{Type: evLink, Run: l.ParentID, Root: root, Data: e.linkView(l)})
		}
	})
}

// runSummary is the list/stream shape of a run (no transcript).
func runSummary(r *Run) map[string]any {
	return map[string]any{
		"id": r.ID, "title": r.Title, "kind": r.Kind, "status": r.Status, "parentId": r.ParentID,
		"rootId": r.RootID, "depth": r.Depth, "wakeAt": r.WakeAt, "result": clip(r.Result, 400),
		"pending": r.Pending != "", "llmCalls": r.LLMCalls, "promptTokens": r.PromptTokens,
		"completionTokens": r.CompletionTokens, "turnSteps": r.TurnSteps, "turnStarted": r.TurnStarted,
		"created": r.Created, "updated": r.Updated,
	}
}

func rootOf(r *Run) int64 {
	if r.RootID != 0 {
		return r.RootID
	}
	return r.ID
}
