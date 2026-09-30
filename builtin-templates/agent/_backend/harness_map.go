// harness_map.go — a coding agent's events as AgTT rows (D-harness §3.4),
// applied in order by the session's consumer (harness_engine.go):
//
//   - text and thinking are the run's draft (the `text`/`thinking` draft
//     events, the /view snapshot) until the next tool call, park, turn end
//     or loss flushes them to an assistant row (thinking in Meta.reasoning);
//     a harness-internal subagent's (a chunk under _meta…parentToolUseId) is
//     kept apart and flushed with Meta.harness.parent when that call makes
//     its next call or completes — not streamed live;
//   - each tool call is ONE assistant row (toolCalls: [{id: "h<gen>:<id>",
//     name: "acp:<kind>", arguments}]) and its tool row, "(running…)" until
//     the call ends (then its result text, compare-and-swapped), the call
//     itself in the tool row's Meta.harness (lifted to `acp`);
//   - a plan, the mode, options, commands, usage and title are the session
//     row and the snapshot, and the `harness` event;
//   - a permission request or a form question parks the run (§4.3.4,
//     harness_park.go);
//   - the turn's end ends the run's turn (harness_engine.go).
package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// hPublishEvery bounds how often a call's streamed output is published.
const hPublishEvery = 250 * time.Millisecond

// hOutputMax is the terminal output a call keeps (its last bytes); the
// result text carries hResultTail of it.
const (
	hOutputMax  = 64 << 10
	hResultTail = 8 << 10
	hContentMax = 64 << 10
)

// hcall is a tool call of the session as the engine last wrote (or holds)
// it: its assistant row and its tool row.
type hcall struct {
	id      string // the stored id: "h<gen>:<acp id>"
	asst    *Message
	row     *Message
	meta    harnessMeta
	counted bool // its diffs are in the counts
}

// hToolEv is a tool.call / tool.update event's data (sdk/acp updates.go).
type hToolEv struct {
	ID          string          `json:"id"`
	Title       *string         `json:"title"`
	Kind        *string         `json:"kind"`
	Status      string          `json:"status"`
	Content     json.RawMessage `json:"content"`
	Locations   json.RawMessage `json:"locations"`
	RawInput    json.RawMessage `json:"rawInput"`
	Name        string          `json:"name"`
	Label       string          `json:"label"`
	Parent      string          `json:"parent"`
	Subagent    bool            `json:"subagent"`
	PlanReview  bool            `json:"planReview"`
	Output      *string         `json:"output"`
	OutputDelta string          `json:"outputDelta"`
	ExitCode    *int            `json:"exitCode"`
}

// sid is the stored id of an adapter's call id (a respawn may reuse ids):
// "h<gen>:<id>", and "h<gen>:<id>#<n>" for the n-th call of an adapter that
// used the id again for a new call once the earlier one had ended (ACP says
// an id is unique in a session; a scripted agent reuses its ids from turn
// to turn — see onTool).
func (s *hsess) sid(id string) string {
	if id == "" {
		return ""
	}
	s.reuseMu.Lock()
	n := s.reuse[id]
	s.reuseMu.Unlock()
	if n > 1 {
		return fmt.Sprintf("h%d:%s#%d", s.gen, id, n)
	}
	return fmt.Sprintf("h%d:%s", s.gen, id)
}

// reused notes a new call under an id an ended call of this generation
// had: from now on the id is its n-th call.
func (s *hsess) reused(id string) {
	s.reuseMu.Lock()
	if s.reuse == nil {
		s.reuse = map[string]int{}
	}
	s.reuse[id] = max(s.reuse[id], 1) + 1
	s.reuseMu.Unlock()
}

// harnessReuse is what sid needs to know of a generation's reused ids after
// a handoff: the highest n of each "h<gen>:<id>#<n>" stored in run.
func (d *DB) harnessReuse(run int64, gen int) map[string]int {
	out := map[string]int{}
	pre := fmt.Sprintf("h%d:", gen)
	rows, err := d.q.Query(`SELECT tool_call_id FROM messages WHERE run_id=? AND role='tool' AND tool_call_id LIKE ? ESCAPE '\'`,
		run, strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(pre)+"%#%")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			continue
		}
		rest := strings.TrimPrefix(id, pre)
		i := strings.LastIndexByte(rest, '#') // (an adapter's id may hold a # of its own)
		if n, err := strconv.Atoi(rest[i+1:]); i > 0 && err == nil && n > out[rest[:i]] {
			out[rest[:i]] = n
		}
	}
	return out
}

// apply is one event, in the adapter's order.
func (s *hsess) apply(ev acp.Event) {
	if ev.Wire != nil && ev.Wire.Replay {
		return // a session/load replaying earlier turns: the rows are there
	}
	switch ev.Type {
	case acp.EvMessageDelta:
		s.onChunk(ev, false)
	case acp.EvThoughtDelta:
		s.onChunk(ev, true)
	case acp.EvToolCall, acp.EvToolUpdate:
		s.onTool(ev)
	case acp.EvPlan:
		_ = s.commit(&ev, func(t *DB, hs *harnessSession) error { hs.Plan = string(ev.Data); return nil })
		s.publishSummary()
	case acp.EvPermissionRequest:
		s.onPermission(ev)
	case acp.EvPermissionResolved:
		s.onResolved(ev, "pid")
	case acp.EvElicitRequest:
		s.onQuestion(ev)
	case acp.EvElicitResolved:
		s.onResolved(ev, "eid")
	case acp.EvTurnEnd:
		s.onTurnEnd(ev)
	case acp.EvStatus:
		s.onStatus(ev)
	}
}

// --- text and thinking ------------------------------------------------------------

func (s *hsess) onChunk(ev acp.Event, thought bool) {
	var d struct {
		Role, Text, Parent string
	}
	if json.Unmarshal(ev.Data, &d) != nil || d.Text == "" || (!thought && d.Role != "agent") {
		return // the prompt's echo (the user row is the pass's), a steer's
	}
	s.mu.Lock()
	dr := &s.draft
	if d.Parent != "" {
		if dr.Subs == nil {
			dr.Subs = map[string]*hsDraft{}
		}
		p := s.sid(d.Parent)
		if dr.Subs[p] == nil {
			dr.Subs[p] = &hsDraft{}
		}
		dr = dr.Subs[p]
	}
	if dr.Started == 0 {
		dr.Started = nowMs()
	}
	if thought {
		dr.Think += d.Text
	} else {
		dr.Text += d.Text
	}
	s.mu.Unlock()
	if d.Parent == "" {
		s.showDraft()
		kind := "writing"
		if thought {
			kind = "thinking"
		}
		s.activity(kind, "")
	}
}

// showDraft publishes the run's draft as the built-in draft events (and
// keeps it for /view).
func (s *hsess) showDraft() {
	s.mu.Lock()
	text, think, started := s.draft.Text, s.draft.Think, s.draft.Started
	s.mu.Unlock()
	e := s.e
	e.mu.Lock()
	d := e.drafts[s.run]
	if d == nil {
		d = &draft{Run: s.run, Model: s.prov.Name, Started: started, root: s.root}
		e.drafts[s.run] = d
	}
	d.Text, d.Thinking = text, think
	if think != "" && d.ThinkStart == 0 {
		d.ThinkStart = started
	}
	e.mu.Unlock()
	if think != "" {
		e.hub.publish(&Event{Type: evThinking, Run: s.run, Root: s.root, key: "thinking:" + itoa(s.run),
			Data: map[string]any{"text": think, "started": started}})
	}
	if text != "" || think == "" {
		e.hub.publish(&Event{Type: evText, Run: s.run, Root: s.root, key: "text:" + itoa(s.run),
			Data: map[string]any{"text": text, "model": s.prov.Name, "started": started}})
	}
}

// takeDraft empties the draft of parent's subagent ("": the run's own) and
// returns it.
func (s *hsess) takeDraft(parent string) hsDraft {
	s.mu.Lock()
	defer s.mu.Unlock()
	if parent == "" {
		d := s.draft
		s.draft = hsDraft{Subs: d.Subs}
		d.Subs = nil
		return d
	}
	var d hsDraft
	if p := s.draft.Subs[parent]; p != nil {
		d = *p
		delete(s.draft.Subs, parent)
	}
	return d
}

// endDraft tells the tile the run's draft is gone (flushed to a row).
func (s *hsess) endDraft() {
	e := s.e
	e.mu.Lock()
	_, had := e.drafts[s.run]
	delete(e.drafts, s.run)
	e.mu.Unlock()
	if had {
		e.hub.publish(&Event{Type: evDraftEnd, Run: s.run, Root: s.root})
	}
}

// textRowTx writes a flushed draft as a text-only assistant row (none when
// it is empty); parent: a subagent's.
func (s *hsess) textRowTx(t *DB, d hsDraft, parent string) error {
	if d.Text == "" && d.Think == "" {
		return nil
	}
	meta := msgMeta{Reasoning: d.Think}
	if parent != "" {
		meta.Harness = &harnessMeta{Parent: parent}
	}
	m := &Message{RunID: s.run, Role: "assistant", Content: d.Text}
	m.Meta, _ = json.Marshal(meta)
	if _, err := t.addMessage(m); err != nil {
		return err
	}
	s.e.emitMessage(t, s.root, m)
	return nil
}

// flushDraft writes the run's draft and every subagent's (a park, the
// turn's end, a loss) in one durable step.
func (s *hsess) flushDraft() {
	s.mu.Lock()
	empty := s.draft.empty()
	s.mu.Unlock()
	if empty {
		return
	}
	_ = s.commit(nil, func(t *DB, hs *harnessSession) error { return s.flushAllTx(t) })
}

// flushAllTx is flushDraft inside a caller's durable step.
func (s *hsess) flushAllTx(t *DB) error {
	main := s.takeDraft("")
	s.mu.Lock()
	var subs []string
	for p := range s.draft.Subs {
		subs = append(subs, p)
	}
	s.mu.Unlock()
	if err := s.textRowTx(t, main, ""); err != nil {
		return err
	}
	for _, p := range subs {
		if err := s.textRowTx(t, s.takeDraft(p), p); err != nil {
			return err
		}
	}
	t.AfterCommit(s.endDraft)
	return nil
}

// --- tool calls -------------------------------------------------------------------

// call is the session's call id: held, else read back from the transcript
// (a successor, a restart) — nil when it was never written.
func (s *hsess) call(id string) *hcall {
	sid := s.sid(id)
	s.mu.Lock()
	c := s.calls[sid]
	s.mu.Unlock()
	if c != nil {
		return c
	}
	row, asst := s.e.db.harnessCallRows(s.run, sid)
	if row == nil {
		return nil
	}
	c = &hcall{id: sid, row: row, asst: asst, counted: true}
	var mm msgMeta
	if json.Unmarshal(row.Meta, &mm) == nil && mm.Harness != nil {
		c.meta = *mm.Harness
	}
	c.counted = terminal(c.meta.Status)
	s.mu.Lock()
	s.calls[sid] = c
	s.mu.Unlock()
	return c
}

// harnessCallRows are a stored call's tool row and its assistant row (the
// one right before it: they are written together).
func (d *DB) harnessCallRows(run int64, id string) (row, asst *Message) {
	rid, _, err := d.toolResultRow(run, id)
	if err != nil {
		return nil, nil
	}
	if row, err = d.messageByID(rid); err != nil {
		return nil, nil
	}
	rows, err := d.q.Query(`SELECT `+msgCols+` FROM messages WHERE run_id=? AND seq=?`, run, row.Seq-1)
	if err == nil {
		if rows.Next() {
			asst, _ = scanMessage(rows.Scan)
		}
		rows.Close()
	}
	if asst != nil && !strings.Contains(asst.ToolCalls, `"`+id+`"`) {
		asst = nil
	}
	return row, asst
}

// cached is a call the session holds (inside a transaction: no reads).
func (s *hsess) cached(sid string) *hcall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[sid]
}

// heldContent keeps a held call's row content in step with a placeholder
// the transcript moved to.
func (s *hsess) heldContent(sid, content string) {
	s.mu.Lock()
	if c := s.calls[sid]; c != nil && c.row != nil {
		c.row.Content = content
	}
	s.mu.Unlock()
}

func terminal(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

// onTool is a tool.call or tool.update: the call's rows written, or
// merged. Only a terminal output delta is not durable: it changes the held
// row, published within hPublishEvery and stored with the next durable
// event.
func (s *hsess) onTool(ev acp.Event) {
	var d hToolEv
	if json.Unmarshal(ev.Data, &d) != nil || d.ID == "" {
		return
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(ev.Data, &keys)
	c := s.call(d.ID)
	if c != nil && ev.Type == acp.EvToolCall && terminal(c.meta.Status) {
		// a new call under the id of one that ended: its own rows, not a
		// rewrite of the earlier call's card
		s.reused(d.ID)
		c = nil
	}
	if c == nil {
		s.newCall(ev, d)
		return
	}
	s.mu.Lock()
	was := c.meta.Status
	titleWas, rawWas := c.meta.Title, string(c.meta.RawInput)
	mergeTool(&c.meta, d, keys, s.sid)
	durable := false
	for k := range keys {
		if k != "id" && k != "outputDelta" && !(k == "status" && d.Status == was) {
			durable = true
		}
	}
	if terminal(c.meta.Status) {
		c.row.Content = s.rowText(c)
	}
	if !durable {
		s.dirty[c.id] = true
		if s.timer == nil && !s.halted {
			s.timer = time.AfterFunc(hPublishEvery, s.publishDirty)
		}
		s.mu.Unlock()
		return
	}
	s.dirty[c.id] = true // stored by the commit below
	argsChanged := c.meta.Title != titleWas || string(c.meta.RawInput) != rawWas
	ended := terminal(c.meta.Status) && !terminal(was)
	s.mu.Unlock()
	var sub hsDraft
	if ended && c.meta.Subagent {
		sub = s.takeDraft(c.id) // what the subagent said since its last call
	}
	_ = s.commit(&ev, func(t *DB, hs *harnessSession) error {
		if ended {
			ok, err := t.casToolResult(s.run, c.id, c.row.Content)
			if err != nil {
				return err
			}
			if !ok { // settled some other way (a stop): that text stays
				if _, cur, err := t.toolResultRow(s.run, c.id); err == nil {
					s.mu.Lock()
					c.row.Content = cur
					s.mu.Unlock()
				}
			}
			s.countTx(hs, c)
		}
		if argsChanged && c.asst != nil {
			c.asst.ToolCalls = callsJSON(c.id, c.meta)
			if _, err := t.q.Exec(`UPDATE messages SET tool_calls=? WHERE id=?`, c.asst.ToolCalls, c.asst.ID); err != nil {
				return err
			}
			s.e.emitMessage(t, s.root, c.asst)
		}
		if err := s.textRowTx(t, sub, c.id); err != nil {
			return err
		}
		s.e.emitMessageID(t, s.root, s.run, c.row.ID)
		return nil
	})
	s.toolActivity()
}

// newCall writes a call's two rows (flushing the text before it into the
// assistant row): the call's start.
func (s *hsess) newCall(ev acp.Event, d hToolEv) {
	c := &hcall{id: s.sid(d.ID)}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(ev.Data, &keys)
	mergeTool(&c.meta, d, keys, s.sid)
	if c.meta.Status == "" {
		c.meta.Status = "pending"
	}
	parent := c.meta.Parent
	dr := s.takeDraft(parent)
	c.asst = &Message{RunID: s.run, Role: "assistant", Content: dr.Text, ToolCalls: callsJSON(c.id, c.meta)}
	am := msgMeta{Reasoning: dr.Think}
	if parent != "" {
		am.Harness = &harnessMeta{Parent: parent}
	}
	c.asst.Meta, _ = json.Marshal(am)
	c.row = &Message{RunID: s.run, Role: "tool", Name: "acp:" + orStr(c.meta.Kind, "other"), ToolCallID: c.id}
	c.row.Content = s.rowText(c)
	c.row.Meta = rowMeta(c.meta)
	err := s.commit(&ev, func(t *DB, hs *harnessSession) error {
		if parent == "" {
			if err := s.flushSubsTx(t); err != nil {
				return err
			}
		}
		if _, err := t.addMessage(c.asst); err != nil {
			return err
		}
		if _, err := t.addMessage(c.row); err != nil {
			return err
		}
		s.e.emitMessage(t, s.root, c.asst)
		s.e.emitMessage(t, s.root, c.row)
		var n hCounts
		_ = json.Unmarshal([]byte(hs.Counts), &n)
		n.Tools++
		b, _ := json.Marshal(n)
		hs.Counts = string(b)
		if terminal(c.meta.Status) {
			s.countTx(hs, c)
		}
		if parent == "" {
			t.AfterCommit(s.endDraft)
		}
		return nil
	})
	if err == nil {
		s.mu.Lock()
		s.calls[c.id] = c
		s.mu.Unlock()
	}
	s.toolActivity()
}

// flushSubsTx writes nothing: a subagent's text is flushed with its own
// next call (newCall takes it) or its end (onTool); a steer flushes
// everything first (flushDraft).
func (s *hsess) flushSubsTx(*DB) error { return nil }

// countTx adds an ended call's edits to the counts (once).
func (s *hsess) countTx(hs *harnessSession, c *hcall) {
	if c.counted {
		return
	}
	c.counted = true
	var n hCounts
	_ = json.Unmarshal([]byte(hs.Counts), &n)
	for _, df := range c.meta.Diffs {
		n.Add += df.Add
		n.Del += df.Del
		n.addFile(df.Path)
	}
	b, _ := json.Marshal(n)
	hs.Counts = string(b)
}

// toolActivity says what the agent does now: the newest call in progress,
// else thinking.
func (s *hsess) toolActivity() {
	s.mu.Lock()
	var cur *hcall
	for _, c := range s.calls {
		if !terminal(c.meta.Status) && (cur == nil || c.row.ID > cur.row.ID) {
			cur = c
		}
	}
	s.mu.Unlock()
	if cur != nil {
		s.activity("tool", orStr(cur.meta.Label, cur.meta.Title))
	} else {
		s.activity("thinking", "")
	}
}

// publishDirty is the one-shot timer: the held rows of calls whose output
// streamed since, as message upserts (not stored).
func (s *hsess) publishDirty() {
	s.mu.Lock()
	s.timer = nil
	if s.halted {
		s.mu.Unlock()
		return
	}
	var views []map[string]any
	for id := range s.dirty {
		if c := s.calls[id]; c != nil {
			m := *c.row
			m.Meta = rowMeta(c.meta)
			views = append(views, messageView(&m))
		}
	}
	s.mu.Unlock()
	for _, v := range views {
		s.e.hub.publish(&Event{Type: evMessage, Run: s.run, Root: s.root, Data: v})
	}
}

// dirtyRowsLocked are the held tool rows not stored yet (s.mu held).
func (s *hsess) dirtyRowsLocked() []hRow {
	var out []hRow
	for id := range s.dirty {
		if c := s.calls[id]; c != nil && c.row != nil && c.row.ID != 0 {
			out = append(out, hRow{call: id, id: c.row.ID, content: c.row.Content, meta: rowMeta(c.meta)})
		}
	}
	return out
}

type hRow struct {
	call    string
	id      int64
	content string
	meta    json.RawMessage
}

// putHarnessRow stores a held tool row's call (its meta); the content is
// written where it changes (a placeholder moved, a result: casToolResult).
func (d *DB) putHarnessRow(r hRow) error {
	_, err := d.q.Exec(`UPDATE messages SET meta=? WHERE id=?`, string(r.meta), r.id)
	return err
}

func rowMeta(h harnessMeta) json.RawMessage {
	b, _ := json.Marshal(msgMeta{Harness: &h})
	return b
}

// callsJSON is the assistant row's toolCalls for one call (§4.3.5):
// arguments are rawInput's fields plus summary (a rawInput summary is
// rawSummary; one that isn't an object is {summary, rawInput}).
func callsJSON(id string, h harnessMeta) string {
	summary := orStr(h.Label, orStr(h.Title, orStr(h.Kind, "other")))
	args := map[string]any{}
	if len(h.RawInput) > 0 && string(h.RawInput) != "null" {
		var obj map[string]json.RawMessage
		if json.Unmarshal(h.RawInput, &obj) == nil {
			for k, v := range obj {
				if k == "summary" {
					k = "rawSummary"
				}
				args[k] = v
			}
		} else {
			args["rawInput"] = h.RawInput
		}
	}
	args["summary"] = summary
	a, _ := json.Marshal(args)
	tc := toolCall{ID: id, Type: "function"}
	tc.Function.Name = "acp:" + orStr(h.Kind, "other")
	tc.Function.Arguments = string(a)
	b, _ := json.Marshal([]toolCall{tc})
	return string(b)
}

// mergeTool folds an event into the call (only the fields it carries).
func mergeTool(h *harnessMeta, d hToolEv, keys map[string]json.RawMessage, sid func(string) string) {
	if d.Title != nil {
		h.Title = *d.Title
	}
	if d.Kind != nil {
		h.Kind = *d.Kind
	}
	if d.Status != "" {
		h.Status = d.Status
	}
	if d.Name != "" {
		h.Tool = d.Name
	}
	if d.Label != "" {
		h.Label = d.Label
	}
	if d.Parent != "" && h.Parent == "" {
		h.Parent = sid(d.Parent)
	}
	if d.Subagent {
		h.Subagent = true
	}
	if d.PlanReview {
		h.PlanReview = true
	}
	if _, ok := keys["rawInput"]; ok {
		h.RawInput = d.RawInput
	}
	if _, ok := keys["locations"]; ok {
		var locs []hLocation
		if json.Unmarshal(d.Locations, &locs) == nil {
			h.Locations = locs
		}
	}
	if _, ok := keys["content"]; ok {
		var blocks []struct {
			Type    string           `json:"type"`
			Content acp.ContentBlock `json:"content"`
			Path    string           `json:"path"`
			OldText *string          `json:"oldText"`
			NewText string           `json:"newText"`
		}
		if json.Unmarshal(d.Content, &blocks) == nil {
			var text []string
			var diffs []hDiff
			for _, b := range blocks {
				switch b.Type {
				case "content":
					if b.Content.Type == "text" && b.Content.Text != "" {
						text = append(text, b.Content.Text)
					}
				case "diff":
					diffs = append(diffs, harnessDiff(b.Path, b.OldText, b.NewText, h.Kind == "delete"))
				}
			}
			if len(text) > 0 {
				h.Text = clip(strings.Join(text, "\n\n"), hContentMax)
			}
			if len(diffs) > 0 {
				h.Diffs = diffs
			}
		}
	}
	if d.Output != nil {
		h.Output, h.OutputTruncated = "", 0
		appendOutput(h, *d.Output)
	}
	if d.OutputDelta != "" {
		appendOutput(h, d.OutputDelta)
	}
	if d.ExitCode != nil {
		c := *d.ExitCode
		h.ExitCode = &c
	}
	h.Files = nil
	for _, l := range h.Locations {
		if l.Path != "" && !hasStr(h.Files, l.Path) {
			h.Files = append(h.Files, l.Path)
		}
	}
	for _, df := range h.Diffs {
		if df.Path != "" && !hasStr(h.Files, df.Path) {
			h.Files = append(h.Files, df.Path)
		}
	}
}

// appendOutput keeps the last hOutputMax bytes of a call's terminal output.
func appendOutput(h *harnessMeta, s string) {
	h.Output += s
	if over := len(h.Output) - hOutputMax; over > 0 {
		h.Output = h.Output[over:]
		h.OutputTruncated += int64(over)
	}
}

// rowText is the tool row's content: a placeholder until the call ends,
// then its result (§4.3.5).
func (s *hsess) rowText(c *hcall) string {
	h := c.meta
	switch h.Status {
	case "cancelled":
		return "(cancelled)"
	case "failed":
		return "error: " + orStr(h.Text, orStr(tailOf(h.Output, hResultTail), "the call failed"))
	case "completed":
	default:
		return toolRunning
	}
	var parts []string
	for _, df := range h.Diffs {
		parts = append(parts, fmt.Sprintf("edited %s (+%d −%d)", df.Path, df.Add, df.Del))
	}
	switch {
	case len(parts) > 0:
		return strings.Join(parts, "\n")
	case h.Output != "" || h.ExitCode != nil:
		out := tailOf(h.Output, hResultTail)
		if h.ExitCode != nil {
			out += fmt.Sprintf("\n[exit %d]", *h.ExitCode)
		}
		return strings.TrimLeft(out, "\n")
	case h.Text != "":
		return h.Text
	}
	return orStr(h.Title, "done")
}

func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// --- status, title, the turn's end ------------------------------------------------

func (s *hsess) onStatus(ev acp.Event) {
	var d struct {
		Status  string          `json:"status"`
		Title   string          `json:"title"`
		Usage   json.RawMessage `json:"usage"`
		Options json.RawMessage `json:"options"`
		Login   *struct {
			Needed bool `json:"needed"`
		} `json:"login"`
	}
	if json.Unmarshal(ev.Data, &d) != nil || d.Status == acp.StatusExited {
		return // the end is ended()'s
	}
	_ = s.commit(&ev, func(t *DB, hs *harnessSession) error {
		if d.Login != nil && d.Login.Needed && hs.State == hsLive && hs.PromptState == "" && s.auth() == nil {
			// signed out (_auth/status_update) with no turn: park on the
			// sign-in now (mid-turn, the prompt's failure does)
			if run, err := t.getRun(s.run); err == nil && run.Status != statusRunning && run.Status != statusWaiting {
				if err := s.loginTx(t, hs, nil); err != nil {
					return err
				}
			}
		}
		if len(d.Usage) > 0 && string(d.Usage) != "null" {
			hs.Usage = string(d.Usage)
		}
		if len(d.Options) > 0 && string(d.Options) != "null" && string(d.Options) != "[]" {
			_ = t.setHarnessOptions(s.prov.ID, d.Options)
		}
		if d.Title != "" && d.Title != hs.Title {
			hs.Title = d.Title
			// the conversation's title follows the adapter's while it is
			// the first message clipped
			if res, err := t.q.Exec(`UPDATE runs SET title=? WHERE id=? AND title_src='clip'`, clip(d.Title, 120), s.run); err == nil && rowsAffected(res) > 0 {
				s.e.emitRun(t, s.run)
			}
		}
		return nil
	})
	if d.Status == acp.StatusIdle {
		s.activity("idle", "")
	}
	s.publishSummary()
}

// onTurnEnd is the prompt's answer: the draft flushed, the turn over — or,
// refused signed out, the run parked on its sign-in with the prompt held;
// while the adapter runs a turn of its own (detached), that goes on.
func (s *hsess) onTurnEnd(ev acp.Event) {
	var d struct {
		StopReason string `json:"stopReason"`
		Error      string `json:"error"`
		Turn       uint64 `json:"turn"`
	}
	if json.Unmarshal(ev.Data, &d) != nil {
		return
	}
	if ev.Wire != nil {
		if why := s.takeAbandoned(ev.Wire.RPCID); why != "" {
			d.Error = why // the pipe gave up on it: said in people's words
		}
	}
	signedOut := d.StopReason == "error" && s.c != nil && s.c.State().AuthNeeded
	detached := s.isDetached()
	rests := false
	_ = s.commit(&ev, func(t *DB, hs *harnessSession) error {
		if err := s.flushAllTx(t); err != nil {
			return err
		}
		inFlight := hs.PromptState != ""
		if inFlight && hs.PromptRPC != "" && ev.Wire != nil && len(ev.Wire.RPCID) > 0 && idKey(hs.PromptRPC) != idKey(string(ev.Wire.RPCID)) {
			return nil // another prompt's (it can't be: one at a time) — ignored
		}
		held := s.heldForLogin(t)
		hs.PromptState, hs.PromptRPC = "", ""
		if d.Turn > 0 {
			hs.Turn = int64(d.Turn)
		}
		run, err := t.getRun(s.run)
		if err != nil {
			return err
		}
		if !inFlight && run.Status != statusRunning {
			return nil // not a turn of ours (a steered one's end) — nothing to end
		}
		switch {
		case signedOut:
			return s.loginTx(t, hs, held)
		case detached:
			return nil // the adapter's own turn goes on (harness_steer.go)
		}
		rests = true
		return s.e.endHarnessTurnTx(t, s.run, d.StopReason, d.Error)
	})
	s.setInflight(nil)
	s.activity("idle", "")
	s.publishSummary()
	if rests {
		s.armIdle()
	}
}

// idKey compares request ids whatever their JSON spelling.
func idKey(raw string) string {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return raw
	}
	return fmt.Sprint(v)
}
