// harness_park.go — a coding agent waiting for a person (D-harness §3.4,
// §4.3.4): a permission request or a form question parks the run
// (waiting_input, the card data in pendingState.harness, the call's row
// awaiting approval); one that comes while another is parked waits in
// harness_sessions.queue and becomes the park when the one before is
// answered. Answering is the pass's (harness_pass.go); the answer's
// resolution event clears the park here.
package main

import (
	"encoding/json"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// onPermission parks the run on a permission request (§4.3.4) — or queues
// it behind the park in force. Answering is the pass's (approve rows).
func (s *hsess) onPermission(ev acp.Event) {
	var d struct {
		PID      string                 `json:"pid"`
		ToolCall acp.ToolCallRef        `json:"toolCall"`
		Options  []acp.PermissionOption `json:"options"`
		Rule     struct {
			Kind  string `json:"kind"`
			Title string `json:"title"`
		} `json:"rule"`
		Meta map[string]any `json:"meta"`
	}
	if json.Unmarshal(ev.Data, &d) != nil || d.PID == "" || !s.pendingPID(d.PID) {
		return // a rule answered it, or a cancel did: its resolution follows
	}
	tc := d.ToolCall
	c := s.call(tc.ID)
	if c == nil && tc.ID != "" { // asked before the adapter announced the call
		title, kind := tc.Title, tc.Kind
		s.newCall(acp.Event{Type: acp.EvToolCall}, hToolEv{ID: tc.ID, Title: &title, Kind: &kind, Status: "pending",
			Name: tc.Name, RawInput: tc.RawInput, Content: tc.Content})
		c = s.call(tc.ID)
	}
	explicit := map[string]bool{}
	for _, m := range s.prov.Modes {
		explicit[m.ID] = m.Explicit
	}
	park := &hPark{CallID: s.sid(tc.ID), PID: d.PID, RPCID: string(ev.Wire.RPCID)}
	for _, o := range d.Options {
		park.Options = append(park.Options, hOption{OptionID: o.OptionID, Name: o.Name, Kind: o.Kind, Explicit: explicit[o.OptionID]})
	}
	tool := &hParkTool{Title: tc.Title, Kind: orStr(tc.Kind, "other"), Name: tc.Name, RawInput: tc.RawInput}
	if len(tc.Content) <= hContentMax {
		tool.Content = tc.Content
	}
	var raw map[string]any
	_ = json.Unmarshal(tc.RawInput, &raw)
	if cmd, ok := raw["command"].(string); ok {
		tool.Command = cmd
	} else if cmd, ok := raw["cmd"].(string); ok {
		tool.Command = cmd
	}
	if c != nil {
		tool.Label = c.meta.Label
		tool.Title = orStr(tool.Title, c.meta.Title)
	}
	park.Tool = tool
	if tc.Kind != acp.KindSwitchMode && (d.Rule.Kind != "" || d.Rule.Title != "") {
		park.Rule = &acp.Rule{Kind: d.Rule.Kind, Title: d.Rule.Title}
	}
	park.DefaultToNo, _ = d.Meta["defaultToNo"].(bool)
	park.Description, _ = d.Meta["description"].(string)
	if tc.Kind == acp.KindSwitchMode || (c != nil && c.meta.PlanReview) {
		park.PlanApproval = true
		if p, ok := raw["plan"].(string); ok {
			park.Plan = p
		} else if c != nil {
			park.Plan = c.meta.Text
		}
	}
	s.parkOrQueue(ev, "approval", park, c)
}

// onQuestion parks the run on a form question; a url one is honoured only
// during a sign-in AgTT started (its device code: harness_login.go) and
// declined otherwise.
func (s *hsess) onQuestion(ev acp.Event) {
	var q acp.Elicitation
	if json.Unmarshal(ev.Data, &q) != nil || q.EID == "" {
		return
	}
	if q.Mode == "url" {
		if a := s.auth(); a != nil { // a device code, during a sign-in AgTT started
			s.onDevice(ev, q, a)
			return
		}
		go func() { _ = s.c.RespondElicitation(q.EID, "decline", nil, "agtt") }()
		return
	}
	park := &hPark{EID: q.EID, Message: q.Message, Schema: q.Schema}
	if q.ToolCallID != "" {
		park.CallID = s.sid(q.ToolCallID)
	}
	if ev.Wire != nil {
		park.RPCID = string(ev.Wire.RPCID)
	}
	s.parkOrQueue(ev, "question", park, nil)
}

// parkOrQueue makes p the run's park (waiting_input) — or, while another
// is parked, queues it.
func (s *hsess) parkOrQueue(ev acp.Event, kind string, p *hPark, c *hcall) {
	s.disarmIdle()
	_ = s.commit(&ev, func(t *DB, hs *harnessSession) error {
		if err := s.flushAllTx(t); err != nil { // the text before it, in the same step
			return err
		}
		run, err := t.getRun(s.run)
		if err != nil {
			return err
		}
		if cur := parsePending(run.Pending); run.Status == statusWaiting && cur.Harness != nil {
			var q []hQueued
			_ = json.Unmarshal([]byte(hs.Queue), &q)
			q = append(q, hQueued{Kind: kind, Perm: p})
			b, _ := json.Marshal(q)
			hs.Queue = string(b)
			return nil
		}
		return s.parkTx(t, run, kind, p, c)
	})
	s.activity("waiting", "")
	s.publishSummary()
}

// parkTx parks run on p.
func (s *hsess) parkTx(t *DB, run *Run, kind string, p *hPark, c *hcall) error {
	ps := pendingState{Kind: kind, Park: newPark(), Harness: p}
	if c != nil && c.asst != nil {
		_ = json.Unmarshal([]byte(c.asst.ToolCalls), &ps.ToolCalls)
	}
	raw, _ := json.Marshal(ps)
	if err := t.setStatus(run.ID, statusWaiting, 0, run.Result, string(raw)); err != nil {
		return err
	}
	if kind == "approval" && p.CallID != "" {
		if ok, _ := t.setToolPlaceholder(run.ID, p.CallID, toolAwaitingApproval); ok {
			s.heldContent(p.CallID, toolAwaitingApproval)
			if id, _, err := t.toolResultRow(run.ID, p.CallID); err == nil {
				s.e.emitMessageID(t, s.root, run.ID, id)
			}
		}
	}
	if run.ParentID == 0 {
		t.bumpActivity(run.ID)
	}
	s.e.emitRun(t, run.ID)
	return nil
}

// onResolved clears the park a request's answer settled (key: "pid" of a
// permission, "eid" of a question) — or drops it from the queue — and makes
// the next queued request the park.
func (s *hsess) onResolved(ev acp.Event, key string) {
	var d map[string]any
	if json.Unmarshal(ev.Data, &d) != nil {
		return
	}
	id, _ := d[key].(string)
	if id == "" || d["action"] == "complete" {
		return
	}
	match := func(p *hPark) bool {
		return p != nil && ((key == "pid" && p.PID == id) || (key == "eid" && p.EID == id))
	}
	if hs, _ := s.e.db.harnessSession(s.run); hs != nil { // the calls a queued request may park on, read now
		var q []hQueued
		_ = json.Unmarshal([]byte(hs.Queue), &q)
		for _, it := range q {
			if it.Perm != nil && it.Perm.CallID != "" {
				s.call(acpCallID(it.Perm.CallID))
			}
		}
	}
	rests, cleared := false, false
	_ = s.commit(&ev, func(t *DB, hs *harnessSession) error {
		var q []hQueued
		_ = json.Unmarshal([]byte(hs.Queue), &q)
		kept := q[:0]
		for _, it := range q {
			if !match(it.Perm) {
				kept = append(kept, it)
			}
		}
		q = kept
		defer func() {
			b, _ := json.Marshal(q)
			hs.Queue = string(b)
			if len(q) == 0 {
				hs.Queue = ""
			}
		}()
		run, err := t.getRun(s.run)
		if err != nil {
			return err
		}
		cur := parsePending(run.Pending)
		if run.Status != statusWaiting || !match(cur.Harness) {
			return nil
		}
		if cur.Kind == "approval" && cur.Harness.CallID != "" {
			if ok, _ := t.casToolResult(run.ID, cur.Harness.CallID, toolRunning); ok {
				s.heldContent(cur.Harness.CallID, toolRunning)
				if mid, _, err := t.toolResultRow(run.ID, cur.Harness.CallID); err == nil {
					s.e.emitMessageID(t, s.root, run.ID, mid)
				}
			}
		}
		if len(q) > 0 {
			next := q[0]
			q = q[1:]
			var c *hcall
			if next.Perm != nil && next.Perm.CallID != "" {
				c = s.cached(next.Perm.CallID) // warmed before the transaction
			}
			return s.parkTx(t, run, next.Kind, next.Perm, c)
		}
		status := statusRunning
		if hs.PromptState == "" && !s.isDetached() {
			status, rests = statusIdle, true
		}
		if err := t.setStatus(run.ID, status, 0, run.Result, ""); err != nil {
			return err
		}
		s.e.emitRun(t, run.ID)
		cleared = true
		return nil
	})
	s.toolActivity()
	s.publishSummary()
	if cleared { // a message that waited on the park is steered (or waits) now
		s.e.Poke(s.run)
	}
	if rests {
		s.armIdle()
	}
}

// pendingPID: the permission request is still waiting for an answer here.
func (s *hsess) pendingPID(pid string) bool {
	for _, p := range s.perms.List() {
		if p.PID == pid {
			return true
		}
	}
	return false
}

// settleParkTx answers the park and the queue with text in the transcript
// (a loss, a stop): the parked call's row stops waiting.
func (s *hsess) settleParkTx(t *DB, run *Run, text string) {
	p := parsePending(run.Pending)
	if p.Harness != nil && p.Harness.CallID != "" {
		if ok, _ := t.casToolResult(run.ID, p.Harness.CallID, text); ok {
			if id, _, err := t.toolResultRow(run.ID, p.Harness.CallID); err == nil {
				s.e.emitMessageID(t, s.root, run.ID, id)
			}
		}
	}
	_, _ = t.q.Exec(`UPDATE harness_sessions SET queue='' WHERE run_id=?`, run.ID)
	for _, id := range t.placeholderRows(run.ID) {
		_ = t.rewriteMessage(run.ID, id, text)
		s.e.emitMessageID(t, s.root, run.ID, id)
	}
}

// placeholderRows are a run's tool rows still waiting for a result.
func (d *DB) placeholderRows(run int64) []int64 {
	rows, err := d.q.Query(`SELECT id, content FROM messages WHERE run_id=? AND role='tool'`, run)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		var c string
		if rows.Scan(&id, &c) == nil && isPlaceholder(c) {
			out = append(out, id)
		}
	}
	return out
}
