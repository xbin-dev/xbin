// Package agent is the model of an AGENT SESSION (D74): a terminal session
// whose sandbox runs a coding-agent CLI instead of a shell. The daemon
// drives the agent through a Driver, keeps every typed thing the agent said
// in an append-only Log any client replays by cursor, and holds the
// permission requests until a client answers. Nothing here knows about
// sandboxes, HTTP or a particular protocol — internal/agent/acp is the one
// Driver, internal/term wires a session around it, internal/server serves
// it. docs/protocol.md §/api/xbin (term/sessions) and §/ws/events
// describe the wire.
package agent

import (
	"encoding/json"
	"sync"
	"time"
)

// Event types, as they appear on the wire (docs/protocol.md).
const (
	EvMessageDelta       = "message.delta"       // {role, text, messageId?}
	EvThoughtDelta       = "thought.delta"       // {text}
	EvPlan               = "plan"                // {entries:[{content, priority, status}]}
	EvToolCall           = "tool.call"           // {id, title, kind, status, content, locations, rawInput}
	EvToolUpdate         = "tool.update"         // {id, …partial}
	EvPermissionRequest  = "permission.request"  // {pid, toolCall, options}
	EvPermissionResolved = "permission.resolved" // {pid, optionId, by}
	EvTurnEnd            = "turn.end"            // {turn, stopReason, usage?}
	EvStatus             = "status"              // {status, detail?, modes?, currentMode?, options?, usage?}
	// EvGap is never logged: a follow stream inserts it when the cursor
	// predates the ring, so a client shows "earlier events dropped".
	EvGap = "gap"
)

// Session statuses (SessionInfo.status, status events).
const (
	StatusStarting   = "starting"
	StatusIdle       = "idle"
	StatusRunning    = "running"
	StatusWaiting    = "waiting_permission"
	StatusCancelling = "cancelling" // session/cancel sent, the turn's end pending
	StatusError      = "error"
	StatusExited     = "exited"
)

// Event is one entry of a session's log. Seq is assigned by the Log (1, 2,
// …); TS is unix milliseconds; Data is the typed payload for Type.
type Event struct {
	Seq  uint64          `json:"seq"`
	TS   int64           `json:"ts"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// New builds an event with its payload marshalled; Seq/TS are set by the
// Log on Append (a driver never numbers).
func New(typ string, data any) Event {
	b, _ := json.Marshal(data)
	return Event{Type: typ, Data: b}
}

// Log is a session's append-only event log: a ring bounded by count and
// bytes, replayable by cursor. In memory — sessions die with the daemon.
type Log struct {
	mu       sync.Mutex
	events   []Event // oldest first
	bytes    int
	next     uint64
	maxCount int
	maxBytes int
	now      func() time.Time
	waiters  []chan struct{} // closed on the next append (Wait)
}

// NewLog bounds the ring; 0 for either means the default (5000 events, 8 MiB).
func NewLog(maxCount, maxBytes int) *Log {
	if maxCount <= 0 {
		maxCount = 5000
	}
	if maxBytes <= 0 {
		maxBytes = 8 << 20
	}
	return &Log{maxCount: maxCount, maxBytes: maxBytes, next: 1, now: time.Now}
}

// Append numbers and stores e (its Seq/TS are overwritten), trimming the
// oldest entries past the bounds, and wakes waiters. Returns the stored event.
func (l *Log) Append(e Event) Event {
	l.mu.Lock()
	e.Seq = l.next
	l.next++
	e.TS = l.now().UnixMilli()
	l.events = append(l.events, e)
	l.bytes += len(e.Data) + 48
	for len(l.events) > 1 && (len(l.events) > l.maxCount || l.bytes > l.maxBytes) {
		l.bytes -= len(l.events[0].Data) + 48
		l.events = l.events[1:]
	}
	ws := l.waiters
	l.waiters = nil
	l.mu.Unlock()
	for _, w := range ws {
		close(w)
	}
	return e
}

// Since returns the events with Seq > cursor, oldest first, and whether the
// cursor predates the oldest kept event (the client should show a gap).
// Never nil.
func (l *Log) Since(cursor uint64) (out []Event, truncated bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out = []Event{}
	if len(l.events) > 0 && cursor+1 < l.events[0].Seq && cursor < l.events[0].Seq-1 {
		truncated = true
	}
	for _, e := range l.events {
		if e.Seq > cursor {
			out = append(out, e)
		}
	}
	return out, truncated
}

// Last is the newest Seq (0 when empty).
func (l *Log) Last() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.next - 1
}

// Wait returns a channel closed on the next Append (for followers).
func (l *Log) Wait() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch := make(chan struct{})
	l.waiters = append(l.waiters, ch)
	return ch
}
