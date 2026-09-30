// harness_park.go — a coding agent waiting for a person (D147 §3.4,
// §4.3.4): a permission request or a form question parks the run
// (waiting_input, the card data in pendingState.harness, the call's row
// awaiting approval); one that comes while another is parked waits in
// harness_sessions.queue and becomes the park when the one before is
// answered. Answering is the pass's (harness_pass.go); the answer's
// resolution event clears the park here.
package main

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

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
	park := &hPark{CallID: s.sid(tc.ID), PID: d.PID, RPCID: string(ev.Wire.RPCID)}
	kind := tc.Kind
	if kind == "" && c != nil {
		kind = c.meta.Kind
	}
	for _, o := range d.Options {
		park.Options = append(park.Options, hOption{OptionID: o.OptionID, Name: o.Name, Kind: o.Kind, Explicit: s.raises(o, kind)})
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
		if park.Plan != "" {
			tool.Content = nil // the call's content is the plan: said once, as `plan`
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
	if d["action"] == "complete" {
		s.onURLComplete()
		return
	}
	if id == "" {
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

// onURLComplete: the adapter says a url question's out-of-band step is done
// (elicitation/complete) — a device-code sign-in the person finished. The
// sign-in this process started hears it from its authenticate's answer
// (signedIn). One a predecessor started lost that answer with the handoff
// (it went to the predecessor's request id): the run is woken as a Retry
// wakes it — a fresh adapter reads the sign-in it stored, the held prompt
// goes again (a still signed-out one parks the run on its sign-in again).
func (s *hsess) onURLComplete() {
	if s.auth() != nil {
		return
	}
	woke := false
	_ = s.e.fenced(func(t *DB) error {
		hs, err := t.harnessSession(s.run)
		if err != nil || hs == nil || hs.Gen != s.gen || hs.State != hsLogin || !loginDevice(hs.Login) || s.isHalted() {
			return err
		}
		if _, _, err := t.enqueue(s.run, inboxWake, inboxBody{Reason: "signed in"}, ""); err != nil {
			return err
		}
		woke = true
		return nil
	})
	if woke {
		s.e.Poke(s.run)
	}
}

// --- answers on their way -------------------------------------------------------------

// hAnswer is an answer to a request the adapter waits on (a permission's
// option, a question's action), recorded in harness_sessions.answers
// before it goes and forgotten once the adapter has it. The client clears
// the park as it answers (its resolution precedes the reply, and the
// consumer commits it): a handoff while the reply is still on its way —
// a stdin POST retrying, a stdio frame no pong acknowledged yet — would
// otherwise leave the adapter waiting for an answer that was given, with
// nothing left to answer. A successor answers each recorded one again
// (reanswer); an answer to a request it already has is ignored by the
// adapter (a JSON-RPC response to an id it no longer waits on).
type hAnswer struct {
	Gen     int             `json:"gen"`              // the adapter's generation (another one's requests are its own)
	Kind    string          `json:"kind"`             // approval | question
	Park    *hPark          `json:"park"`             // the request: its pid or eid, its rpc id
	Option  string          `json:"option,omitempty"` // approval: the option ("": the cancelled outcome)
	Action  string          `json:"action,omitempty"` // question: accept | decline | cancel
	Content json.RawMessage `json:"content,omitempty"`
	By      string          `json:"by"`
}

// hDeliveredFor bounds the wait for a stdio pong after an answer's reply
// (unacknowledged, the answer stays recorded: a successor sends it again).
const hDeliveredFor = 10 * time.Second

var errAnswered = errors.New("answered already")

func parseAnswers(raw string) []hAnswer {
	var out []hAnswer
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// answersOf are the answers recorded for generation gen's adapter (an
// older binary rolled back to never clears them when it starts another).
func answersOf(raw string, gen int) []hAnswer {
	var out []hAnswer
	for _, a := range parseAnswers(raw) {
		if a.Gen == gen && a.Park != nil {
			out = append(out, a)
		}
	}
	return out
}

// rpc is the request an answer is to: the adapter's rpc id (else the
// client's pid or eid).
func (a hAnswer) rpc() string {
	switch {
	case a.Park == nil:
		return ""
	case a.Park.RPCID != "":
		return idKey(a.Park.RPCID)
	}
	return a.Kind + ":" + a.Park.PID + a.Park.EID
}

// answer sends a, once: recorded, answered, forgotten when the adapter
// has it (a reply that failed stays recorded for a successor).
func (s *hsess) answer(a hAnswer) {
	a.Gen = s.gen
	if a.Park == nil || !s.noteAnswer(a) {
		return // answered already, or the session isn't this process's any more
	}
	s.sendAnswer(a, false)
}

// sendAnswer answers a through the client — again (a successor's): a
// permission is found by its rpc id (pendingRPC), and one no longer
// pending here (its park cleared before the handoff) is answered by its
// rpc id alone — and forgets it once delivered.
func (s *hsess) sendAnswer(a hAnswer, again bool) {
	err := s.respond(a, again)
	switch {
	case errors.Is(err, errAnswered):
		s.forgetAnswer(a)
	case err != nil:
		logf("run #%d: answering %s's %s: %v", s.run, s.prov.Name, orStr(a.Kind, "request"), err)
	case s.pipe.delivered(hDeliveredFor):
		s.forgetAnswer(a)
	}
}

func (s *hsess) respond(a hAnswer, again bool) error {
	h := a.Park
	if a.Kind == "question" {
		err := s.c.RespondElicitation(h.EID, orStr(a.Action, "decline"), a.Content, a.By)
		if errors.Is(err, acp.ErrNoElicitation) {
			return errAnswered
		}
		return err
	}
	pid := h.PID
	if again {
		// a predecessor's answer names its request by rpc id: this
		// client's pids start over, so another request may be pending
		// here under the recorded pid — never answered with this one
		if h.RPCID == "" {
			return errAnswered
		}
		pid = s.pendingRPC(h.RPCID)
	}
	var res *acp.Resolution
	switch {
	case again && pid == "": // its park cleared before the handoff: the reply alone (no pid: it names no park here)
		res = &acp.Resolution{OptionID: a.Option, By: a.By, RPCID: json.RawMessage(h.RPCID), Cancel: a.Option == ""}
	case a.Option != "":
		r, err := s.perms.Resolve(pid, a.Option, "", a.By)
		if err != nil {
			return errAnswered
		}
		res = r
	default:
		if res = s.perms.CancelByRPC(json.RawMessage(h.RPCID)); res == nil {
			return errAnswered
		}
		res.By = a.By
	}
	return s.c.RespondPermission(res)
}

// noteAnswer records a (false: an answer to the same request is recorded
// already, or this process no longer owns the session).
func (s *hsess) noteAnswer(a hAnswer) bool {
	ok := false
	_ = s.e.fenced(func(t *DB) error {
		if s.isHalted() {
			return errHarnessGone
		}
		hs, err := t.harnessSession(s.run)
		if err != nil || hs == nil || hs.Gen != s.gen {
			return errHarnessStale
		}
		list := parseAnswers(hs.Answers)
		for _, x := range list {
			if x.rpc() == a.rpc() {
				return nil
			}
		}
		b, _ := json.Marshal(append(list, a))
		ok = true
		_, err = t.q.Exec(`UPDATE harness_sessions SET answers=? WHERE run_id=?`, string(b), s.run)
		return err
	})
	return ok
}

// forgetAnswer: the adapter has a (or it needs none any more).
func (s *hsess) forgetAnswer(a hAnswer) {
	_ = s.e.fenced(func(t *DB) error {
		hs, err := t.harnessSession(s.run)
		if err != nil || hs == nil || hs.Gen != s.gen {
			return err
		}
		list := parseAnswers(hs.Answers)
		kept := list[:0]
		for _, x := range list {
			if x.rpc() != a.rpc() {
				kept = append(kept, x)
			}
		}
		raw := ""
		if len(kept) > 0 {
			b, _ := json.Marshal(kept)
			raw = string(b)
		}
		_, err = t.q.Exec(`UPDATE harness_sessions SET answers=? WHERE run_id=?`, raw, s.run)
		return err
	})
}

// reanswer is a successor's: the answers its predecessor recorded, sent
// again (their requests restored at the attach: attachHarness).
func (s *hsess) reanswer(list []hAnswer) {
	for _, a := range list {
		if s.isHalted() {
			return
		}
		s.sendAnswer(a, true)
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

// pendingRPC is the pid the permission request under the adapter's rpc id
// waits for an answer under here ("": none does).
func (s *hsess) pendingRPC(rpc string) string {
	for _, p := range s.perms.List() {
		if id := p.RPCID(); len(id) > 0 && idKey(string(id)) == idKey(rpc) {
			return p.PID
		}
	}
	return ""
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

// raises: permission option o of a call of kind switches the session to a
// mode harnessModeOpen doesn't open — the root owner's only (§4.2.9);
// optionRaises says which.
func (s *hsess) raises(o acp.PermissionOption, kind string) bool {
	var st acp.SessionState
	if s.c != nil {
		st = s.c.State()
	}
	var start string
	_ = s.e.db.q.QueryRow(`SELECT start_mode FROM harness_sessions WHERE run_id=?`, s.run).Scan(&start)
	return optionRaises(s.prov, st, start, o, kind)
}

// optionRaises: only an allow can raise. Its mode is the one it names — its
// id when that is one of the agent's modes, else the catalog's OptionModes
// (claude's plan approval: exit-plan-bypass is bypassPermissions) — and it
// raises when harnessModeOpen doesn't open that mode. An allow_always of a
// mode switch (switch_mode: a plan approval) that names no mode the catalog
// knows raises too (default-deny: that is how an adapter offers "yes, and
// switch to ‹mode›"); any other option that names no mode raises nothing
// (an allow_once of a plan approval keeps the mode it had before planning).
func optionRaises(prov acp.Provider, st acp.SessionState, start string, o acp.PermissionOption, kind string) bool {
	if !strings.HasPrefix(o.Kind, "allow") {
		return false
	}
	mode := ""
	if harnessModeIDs(prov, st)[o.OptionID] {
		mode = o.OptionID
	} else if m := prov.OptionModes[o.OptionID]; m != "" {
		mode = m
	}
	if mode == "" {
		return kind == acp.KindSwitchMode && o.Kind == acp.AllowAlways
	}
	return !harnessModeOpen(prov, start, mode)
}
