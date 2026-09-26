// view_page.go — GET /runs/{id}/view?before=<seq>&limit=<n>: the view one
// page of transcript at a time, newest first.
//
// Without before/limit the view is the whole run, as it always was. With
// them, `messages` is the newest `limit` messages with seq < before (from the
// end when before is absent), and the rest of the transcript-shaped fields
// follow the page, so that the union of the pages a client loaded, newest
// first, folds exactly as the whole view does from there on:
//
//   - a page never starts with a tool result: it reaches back to the
//     assistant message that made the call (so it may hold a few more than
//     limit), and a call's results are never split from it;
//   - `steps` are those in the page's time window: at or after its first
//     message's time when older messages exist (steps interleave with
//     messages by time, a step at a message's second after it), and before
//     the first message of the next newer page (the one at seq `before`);
//     the oldest page takes every earlier step — pages partition the steps;
//   - `links` are the page's subagents (by the calls in its messages and the
//     spawn steps in its steps), every link of each such child; `linkCount`
//     counts all of the run's links;
//   - `messageFiles` covers the page's messages.
//
// Pages carry only what the chat shows: messages compacted out of the live
// window and the stored system prompt are left out (`compacted` counts the
// former — the "earlier turns were compacted" line), so `hasOlder` is true
// only when there is more to show. Everything else in the view (the run,
// drafts, queued, files, memory, chain, config, cursor) is the run's current
// state, the same in every page; a client keeps the stream it opened from
// its newest page, and on `reset` or `resync` drops the older pages and
// re-reads the newest.
package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
)

const (
	defaultViewPage = 50
	maxViewPage     = 500
)

// viewPage asks for one page (nil: the whole transcript).
type viewPage struct {
	before int // message seq; < 0: from the end
	limit  int
}

// parseViewPage reads ?before=&limit= (nil, nil: neither given).
func parseViewPage(q url.Values) (*viewPage, error) {
	b, l := q.Get("before"), q.Get("limit")
	if b == "" && l == "" {
		return nil, nil
	}
	pg := &viewPage{before: -1, limit: defaultViewPage}
	if b != "" {
		n, err := strconv.Atoi(b)
		if err != nil || n < 0 {
			return nil, errors.New("before must be a message seq (an integer ≥ 0)")
		}
		pg.before = n
	}
	if l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 {
			return nil, errors.New("limit must be a positive integer")
		}
		pg.limit = min(n, maxViewPage)
	}
	return pg, nil
}

// transcriptPart is the transcript-shaped half of a view.
type transcriptPart struct {
	messages []map[string]any
	steps    []*Step
	links    []map[string]any
	files    map[int64][]string
	extra    map[string]any // the paged form's own fields
}

// wholeTranscript is today's view: every message, step and link.
func (e *Engine) wholeTranscript(id int64) *transcriptPart {
	msgs, _ := e.db.messages(id, false)
	p := &transcriptPart{messages: make([]map[string]any, 0, len(msgs)), links: []map[string]any{}}
	for _, m := range msgs {
		p.messages = append(p.messages, messageView(m))
	}
	p.steps, _ = e.db.steps(id)
	if p.steps == nil {
		p.steps = []*Step{}
	}
	for _, l := range e.db.queryLinks(`WHERE parent_id=? ORDER BY id`, id) {
		p.links = append(p.links, e.linkView(l))
	}
	p.files = e.db.messageFiles(id)
	return p
}

// shownMsgs is the WHERE clause of the messages a page may hold.
const shownMsgs = `run_id=? AND compacted=0 AND role<>'system'`

// pagedTranscript is one page of it.
func (e *Engine) pagedTranscript(id int64, pg *viewPage) *transcriptPart {
	d := e.db
	msgs, _ := d.pageMessages(id, pg.before, pg.limit)
	hasOlder := false
	var lo int64 = -1 << 62
	if len(msgs) > 0 {
		hasOlder = d.hasShownBefore(id, msgs[0].Seq)
		if hasOlder {
			lo = msgs[0].Created
		}
	}
	var hi int64 = 1 << 62
	if pg.before >= 0 {
		if t, ok := d.firstShownFrom(id, pg.before); ok {
			hi = t
		}
	}
	p := &transcriptPart{messages: make([]map[string]any, 0, len(msgs)), links: []map[string]any{}, files: map[int64][]string{}}
	calls, kids := map[string]bool{}, map[int64]bool{}
	all := d.messageFiles(id)
	for _, m := range msgs {
		p.messages = append(p.messages, messageView(m))
		if f, ok := all[m.ID]; ok {
			p.files[m.ID] = f
		}
		if m.ToolCalls != "" {
			var cs []toolCall
			if json.Unmarshal([]byte(m.ToolCalls), &cs) == nil {
				for _, c := range cs {
					calls[c.ID] = true
				}
			}
		}
	}
	p.steps = d.stepsBetween(id, lo, hi)
	for _, s := range p.steps {
		if s.Kind == "spawn" {
			var det struct {
				RunID int64 `json:"runId"`
			}
			if json.Unmarshal([]byte(s.Detail), &det) == nil && det.RunID != 0 {
				kids[det.RunID] = true
			}
		}
	}
	links := d.queryLinks(`WHERE parent_id=? ORDER BY id`, id)
	for _, l := range links {
		if l.ToolCallID != "" && calls[l.ToolCallID] {
			kids[l.ChildID] = true
		}
	}
	for _, l := range links {
		if kids[l.ChildID] {
			p.links = append(p.links, e.linkView(l))
		}
	}
	p.extra = map[string]any{
		"hasOlder": hasOlder, "compacted": d.compactedCount(id), "linkCount": len(links),
	}
	if hasOlder {
		p.extra["nextBefore"] = msgs[0].Seq
	}
	return p
}

// pageMessages is the newest `limit` shown messages with seq < before
// (before < 0: no bound), oldest first, reaching back so that the page does
// not start with a tool result.
func (d *DB) pageMessages(runID int64, before, limit int) ([]*Message, error) {
	if before < 0 {
		before = int(^uint(0) >> 1)
	}
	out, err := d.queryMessages(`WHERE `+shownMsgs+` AND seq<? ORDER BY seq DESC, id DESC LIMIT ?`, runID, before, limit)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	// A call's results follow it at once; the page takes the call too. The
	// walk ends at the first message that is not a result (or the start).
	for len(out) > 0 && out[0].Role == "tool" {
		prev, err := d.queryMessages(`WHERE `+shownMsgs+` AND seq<? ORDER BY seq DESC, id DESC LIMIT 1`, runID, out[0].Seq)
		if err != nil || len(prev) == 0 {
			break
		}
		out = append(prev, out...)
	}
	return out, nil
}

func (d *DB) queryMessages(where string, args ...any) ([]*Message, error) {
	rows, err := d.q.Query(`SELECT `+msgCols+` FROM messages `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// hasShownBefore: is there a shown message older than seq?
func (d *DB) hasShownBefore(runID int64, seq int) bool {
	var n int
	_ = d.q.QueryRow(`SELECT EXISTS(SELECT 1 FROM messages WHERE `+shownMsgs+` AND seq<?)`, runID, seq).Scan(&n)
	return n != 0
}

// firstShownFrom is the time of the first shown message at or after seq —
// where the next newer page starts.
func (d *DB) firstShownFrom(runID int64, seq int) (int64, bool) {
	var t int64
	if err := d.q.QueryRow(`SELECT created FROM messages WHERE `+shownMsgs+` AND seq>=? ORDER BY seq, id LIMIT 1`, runID, seq).Scan(&t); err != nil {
		return 0, false // sql.ErrNoRows: nothing newer — no upper bound
	}
	return t, true
}

// compactedCount counts the messages compacted out of the live window (what
// the chat's "earlier turns were compacted" line is about).
func (d *DB) compactedCount(runID int64) int {
	var n int
	_ = d.q.QueryRow(`SELECT COUNT(*) FROM messages WHERE run_id=? AND compacted=1 AND role<>'system'`, runID).Scan(&n)
	return n
}

// stepsBetween is a run's steps with lo ≤ created < hi, in journal order.
func (d *DB) stepsBetween(runID, lo, hi int64) []*Step {
	rows, err := d.q.Query(`SELECT id, run_id, seq, kind, detail, created FROM steps WHERE run_id=? AND created>=? AND created<? ORDER BY seq, id`, runID, lo, hi)
	if err != nil {
		return []*Step{}
	}
	defer rows.Close()
	out := []*Step{}
	for rows.Next() {
		s := &Step{}
		if rows.Scan(&s.ID, &s.RunID, &s.Seq, &s.Kind, &s.Detail, &s.Created) == nil {
			out = append(out, s)
		}
	}
	return out
}
