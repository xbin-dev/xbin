package agent

// PAGES of a session's log (D130): a client that opens a long conversation
// asks for its tail and for older pages as the reader scrolls
// (`GET …/events?before=&limit=`), instead of replaying the whole ring.
//
// A page is folded on its own — the clients' folds (web/agent-fold.js, the
// app's) build one card per tool call, message run, permission request and
// so on, and later events update the card they belong to. So a page may
// only start where no card begun before it is touched after it: a SAFE CUT.
// safeCuts mirrors the fold's bookkeeping to find them — which event opened
// the card each later event lands on — and treats what can still change as
// open to the end of the log: the message or thought being written, a tool
// call of the running turn not finished yet, an unanswered request, the
// running turn's plan, and the snapshots the daemon has yet to report
// (Open). Folding the pages one by one, each after its predecessor's
// state header, gives exactly the blocks of folding the whole log.

import (
	"encoding/json"
	"strconv"
)

// EvReplayed is never logged: when a resumed session's replay (session/load)
// is over, the live hub carries one `replayed` event instead of every
// replayed entry — clients re-read the tail (docs/protocol.md).
const EvReplayed = "replayed"

// evFilesChanged is internal/term's snapshot diff event, as the fold reads it.
const evFilesChanged = "files.changed"

// DefaultPageLimit and MaxPageLimit bound a page's event count.
const (
	DefaultPageLimit = 200
	MaxPageLimit     = 5000
)

// Page is one page of a log: events[lo:hi], oldest first, cut at a safe
// boundary (docs/protocol.md §Agent session events → Pages).
type Page struct {
	Events     []Event   `json:"events"`
	HasOlder   bool      `json:"hasOlder"`   // the ring holds events before this page
	NextBefore uint64    `json:"nextBefore"` // before= for the next older page (the page's first seq; 0 when none)
	Truncated  bool      `json:"truncated"`  // the oldest page, and the ring dropped events before it
	Next       uint64    `json:"next"`       // the page's newest seq (the tail page: the follow cursor)
	Last       uint64    `json:"last"`       // the log's newest seq
	State      PageState `json:"state"`
}

// PageState is what a fold needs besides the page's own events: the status
// digest and the last turn's usage and number as of the page's first event,
// and — on the tail page of a live session — the requests waiting now.
type PageState struct {
	// Status is a status event's data: the last status before the page, with
	// status, detail, currentMode, options, modes and commands each taken
	// from the latest earlier status that carried it (the fold's digest). A
	// client folds it first, as a status event, then the page.
	Status       json.RawMessage `json:"status,omitempty"`
	Usage        json.RawMessage `json:"usage,omitempty"` // the last turn.end's usage before the page
	Turn         uint64          `json:"turn"`            // the last turn number ended before the page
	Permissions  []Pending       `json:"permissions,omitempty"`
	Elicitations []Elicitation   `json:"elicitations,omitempty"`
}

// Open names what may still touch the log's cards: the tool calls and turns
// whose files.changed snapshot the daemon has not reported yet.
type Open struct {
	Tools map[string]bool
	Turns map[uint64]bool
}

// evMeta is what the cut finder reads of an event (parsed once per event).
type evMeta struct {
	typ, id, parent, pid, eid, role, mid, status, toolCallID string
	turn                                                     uint64
	hasTurn, attach, subagent                                bool
}

func metaOf(e Event) evMeta {
	m := evMeta{typ: e.Type}
	switch e.Type {
	case EvMessageDelta, EvThoughtDelta, EvToolCall, EvToolUpdate, evFilesChanged, EvPermissionRequest,
		EvPermissionResolved, EvElicitRequest, EvElicitResolved, EvTurnEnd:
	default:
		return m
	}
	var d struct {
		ID          string          `json:"id"`
		Parent      string          `json:"parent"`
		PID         json.RawMessage `json:"pid"`
		EID         json.RawMessage `json:"eid"`
		Role        string          `json:"role"`
		MessageID   string          `json:"messageId"`
		Status      string          `json:"status"`
		ToolCallID  string          `json:"toolCallId"`
		Turn        *float64        `json:"turn"`
		Attachments json.RawMessage `json:"attachments"`
		Subagent    bool            `json:"subagent"`
	}
	_ = json.Unmarshal(e.Data, &d)
	m.id, m.parent, m.role, m.mid, m.status, m.toolCallID, m.subagent = d.ID, d.Parent, d.Role, d.MessageID, d.Status, d.ToolCallID, d.Subagent
	m.pid, m.eid = string(d.PID), string(d.EID) // raw: the fold keys its maps by the value as sent
	if m.role == "" {
		m.role = "agent"
	}
	m.attach = len(d.Attachments) > 0 && string(d.Attachments) != "null"
	if d.Turn != nil && *d.Turn > 0 {
		m.turn, m.hasTurn = uint64(*d.Turn), true
	}
	return m
}

// safeCuts reports for every i in [0, len(ms)] whether a page may start at
// event i: no card opened before i is touched at or after i (nor may be,
// per open). safe[0] is true; safe[len] is false while anything is open.
func safeCuts(ms []evMeta, open Open) []bool {
	n := len(ms)
	ext := make([]int, n) // ext[a]: the last event that touches the card event a opened
	for i := range ext {
		ext[i] = i
	}
	up := map[int]int{} // a subagent's nested call → the subagent call (its card holds it)
	touch := func(a, j int) {
		for a >= 0 {
			if j > ext[a] {
				ext[a] = j
			}
			p, ok := up[a]
			if !ok {
				return
			}
			a = p
		}
	}
	type cur struct {
		idx          int
		thought      bool
		role, mid    string
		files, ended bool
	}
	type tkey struct {
		turn float64
		id   string
	}
	var c *cur // the main thread's current message or thought
	turn := 0.0
	tools := map[tkey]int{}
	byID := map[string]int{}
	sub := map[int]bool{}
	tstatus := map[int]string{}
	perms, asks := map[string]int{}, map[string]int{}
	answered := map[int]bool{}
	turnEnds := map[uint64]int{}
	plan, lastEnd := -1, -1
	for j, m := range ms {
		if m.typ != EvThoughtDelta && m.typ != EvStatus && m.typ != evFilesChanged && c != nil && c.thought && !c.ended {
			touch(c.idx, j) // anything else ends a thought: its duration
			c.ended = true
		}
		switch m.typ {
		case EvMessageDelta, EvThoughtDelta:
			if b, ok := byID[m.parent]; ok && m.parent != "" && sub[b] {
				touch(b, j) // a subagent's text lives in its call's card
				continue
			}
			thought := m.typ == EvThoughtDelta
			switch {
			case thought && c != nil && c.thought:
				touch(c.idx, j)
			case !thought && c != nil && !c.thought && c.role == m.role && c.mid == m.mid && !c.files && !m.attach:
				touch(c.idx, j)
			default:
				c = &cur{idx: j, thought: thought, role: m.role, mid: m.mid, files: m.attach}
			}
		case EvToolCall, EvToolUpdate:
			k := tkey{turn, m.id}
			t, seen := tools[k]
			box := -1
			if b, ok := byID[m.parent]; ok && m.parent != "" && sub[b] && (!seen || b != t) {
				box = b
			}
			if box >= 0 {
				touch(box, j)
			} else {
				c = nil
			}
			if !seen {
				t = j
				tools[k] = j
				if box >= 0 {
					up[j] = box
				}
			} else {
				touch(t, j)
			}
			byID[m.id] = t
			if m.subagent {
				sub[t] = true
			}
			if m.status != "" {
				tstatus[t] = m.status
			}
		case evFilesChanged:
			if m.toolCallID != "" {
				if t, ok := tools[tkey{turn, m.toolCallID}]; ok {
					touch(t, j)
				} else if t, ok := byID[m.toolCallID]; ok {
					touch(t, j)
				}
			} else if k, ok := turnEnds[m.turn]; ok && m.hasTurn {
				touch(k, j) // the turn's changes card goes before its end marker
			}
		case EvPlan:
			c = nil
			if plan < 0 {
				plan = j
			} else {
				touch(plan, j)
			}
		case EvPermissionRequest:
			c = nil
			perms[m.pid] = j
		case EvElicitRequest:
			c = nil
			asks[m.eid] = j
		case EvPermissionResolved:
			if a, ok := perms[m.pid]; ok {
				touch(a, j)
				answered[a] = true
			}
		case EvElicitResolved:
			if a, ok := asks[m.eid]; ok {
				touch(a, j)
				answered[a] = true
			}
		case EvTurnEnd:
			c, plan = nil, -1
			if m.hasTurn {
				turn = float64(m.turn) + 0.5
				turnEnds[m.turn] = j
			} else {
				turn += 0.5
			}
			lastEnd = j
		case EvGap:
			c = nil
		}
	}
	// what may still change: open to the end of the log
	if c != nil {
		touch(c.idx, n)
	}
	if plan >= 0 {
		touch(plan, n)
	}
	for _, t := range tools {
		if s := tstatus[t]; t > lastEnd && s != "completed" && s != "failed" && s != "cancelled" {
			touch(t, n)
		}
	}
	for _, reqs := range []map[string]int{perms, asks} {
		for _, a := range reqs {
			if a > lastEnd && !answered[a] {
				touch(a, n)
			}
		}
	}
	for id := range open.Tools {
		if t, ok := byID[id]; ok {
			touch(t, n)
		}
	}
	for tn := range open.Turns {
		if k, ok := turnEnds[tn]; ok {
			touch(k, n)
		}
	}
	safe := make([]bool, n+1)
	reach := -1
	for i := 0; i <= n; i++ {
		safe[i] = reach < i
		if i < n && ext[i] > reach {
			reach = ext[i]
		}
	}
	return safe
}

// PageOf cuts a page out of a log (evs oldest first; ms their metas, or nil
// to parse them): the events before seq `before` (0 = the tail), at most
// about `limit` of them, starting at the latest safe cut that keeps the page
// within limit — or, when a card spans more than that, at the safe cut
// before it (a page is never empty while there are events before `before`).
func PageOf(evs []Event, ms []evMeta, before uint64, limit int, open Open) Page {
	if limit <= 0 {
		limit = DefaultPageLimit
	}
	limit = min(limit, MaxPageLimit)
	if ms == nil {
		ms = make([]evMeta, len(evs))
		for i, e := range evs {
			ms[i] = metaOf(e)
		}
	}
	hi := len(evs)
	if before > 0 {
		for hi > 0 && evs[hi-1].Seq >= before {
			hi--
		}
	}
	safe := safeCuts(ms, open)
	lo := 0
	if hi > 0 {
		lo = -1
		for i := max(0, hi-limit); i < hi; i++ {
			if safe[i] {
				lo = i
				break
			}
		}
		for i := hi - limit - 1; lo < 0 && i >= 0; i-- {
			if safe[i] {
				lo = i
			}
		}
		lo = max(lo, 0)
	}
	p := Page{Events: append([]Event{}, evs[lo:hi]...), HasOlder: lo > 0}
	if lo > 0 {
		p.NextBefore = evs[lo].Seq
	}
	if n := len(evs); n > 0 {
		p.Last = evs[n-1].Seq
		p.Truncated = lo == 0 && evs[0].Seq > 1
	}
	if hi > lo {
		p.Next = evs[hi-1].Seq
	}
	p.State = stateBefore(evs[:lo])
	return p
}

// stateBefore digests the status events and turn ends of evs the way the
// fold does (web/agent-fold.js Fold.st).
func stateBefore(evs []Event) PageState {
	var st PageState
	var last map[string]json.RawMessage
	digest := map[string]json.RawMessage{}
	for _, e := range evs {
		switch e.Type {
		case EvStatus:
			var d map[string]json.RawMessage
			if json.Unmarshal(e.Data, &d) != nil {
				continue
			}
			last = d
			for _, k := range []string{"status", "detail", "currentMode"} { // a non-empty string sticks
				var s string
				if json.Unmarshal(d[k], &s) == nil && s != "" {
					digest[k] = d[k]
				}
			}
			for _, k := range []string{"options", "modes", "commands"} { // the last list sent
				if v := d[k]; len(v) > 0 && v[0] == '[' {
					digest[k] = v
				}
			}
		case EvTurnEnd:
			var d struct {
				Turn  uint64          `json:"turn"`
				Usage json.RawMessage `json:"usage"`
			}
			if json.Unmarshal(e.Data, &d) == nil {
				if d.Turn > 0 {
					st.Turn = d.Turn
				}
				if len(d.Usage) > 0 && string(d.Usage) != "null" {
					st.Usage = d.Usage
				}
			}
		}
	}
	if last != nil {
		for k, v := range digest {
			last[k] = v
		}
		st.Status, _ = json.Marshal(last)
	}
	return st
}

// Page cuts a page of this log (PageOf); open names the snapshots still to
// come. The events' metas are parsed once and kept beside the ring.
func (l *Log) Page(before uint64, limit int, open Open) Page {
	l.mu.Lock()
	for len(l.metas) < len(l.events) {
		l.metas = append(l.metas, metaOf(l.events[len(l.metas)]))
	}
	evs, ms := l.events, l.metas
	l.mu.Unlock()
	return PageOf(evs, ms, before, limit, open) // the slices are append-only views: safe to read unlocked
}

// ParsePageQuery reads ?before= and ?limit= (either one asks for a page);
// ok false when neither is present — the replay by ?since= answers.
func ParsePageQuery(before, limit string, has func(string) bool) (b uint64, n int, ok bool) {
	if !has("before") && !has("limit") {
		return 0, 0, false
	}
	b, _ = strconv.ParseUint(before, 10, 64)
	n, _ = strconv.Atoi(limit)
	return b, n, true
}
