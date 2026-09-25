// links.go — delegating to subagents.
//
// A LINK is one delegation: a parent run asked a child run for something and
// wants the answer back. The split of who writes what is the whole design:
//
//   - the CHILD settles its open link exactly once, when its turn ends
//     (answered, finished, blocked, errored, cancelled, interrupted):
//     state/outcome/result/settled, WHERE state='running';
//   - the PARENT decides what to do with it: deliver it (rewriting the
//     placeholder of the call that spawned it, in call order), or — if it
//     waited too long, or the owner spoke meanwhile — DEMOTE it to the
//     background (mode/delivered/demoted_at), where its answer arrives later
//     as a notice at a step boundary.
//
// Because the two never write the same columns, a child finishing at the very
// instant its parent's deadline fires resolves consistently either way, and
// every result is delivered exactly once. Nothing waits forever: a foreground
// wait has a deadline, a child that needs a human surfaces to the root
// session, and a subagent's turn end settles its link whatever the reason.
package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Link states (written by the child) and outcomes.
const (
	linkRunning  = "running"
	linkDone     = "done"
	linkError    = "error"
	linkCanceled = "canceled"

	outcomeDone        = "done"        // finish(result)
	outcomeAnswered    = "answered"    // a plain final answer
	outcomeIncomplete  = "incomplete"  // blocked, or out of steps
	outcomeError       = "error"       // the turn failed
	outcomeCanceled    = "canceled"    // stopped
	outcomeInterrupted = "interrupted" // stopped by the owner, run kept
)

// A delivered child result is clipped harder than a tool result: eight
// children at the 16 KiB tool cap would be a compaction storm. The full text
// stays readable with subagent_result.
const maxChildResult = 4 << 10

type Link struct {
	ID         int64  `json:"id"`
	ParentID   int64  `json:"parentId"`
	ChildID    int64  `json:"childId"`
	ToolCallID string `json:"toolCallId"`
	Mode       string `json:"mode"` // fg | bg
	State      string `json:"state"`
	Outcome    string `json:"outcome"`
	Result     string `json:"result"`
	Deadline   int64  `json:"deadline"`
	Delivered  bool   `json:"delivered"`
	DemotedAt  int64  `json:"demotedAt"`
	Label      string `json:"label"`
	Created    int64  `json:"created"`
	Settled    int64  `json:"settled"`
}

const linkCols = `id, parent_id, child_id, tool_call_id, mode, state, outcome, result, deadline, delivered, demoted_at, label, created, settled`

func scanLink(scan func(dest ...any) error) (*Link, error) {
	l := &Link{}
	var del int
	if err := scan(&l.ID, &l.ParentID, &l.ChildID, &l.ToolCallID, &l.Mode, &l.State, &l.Outcome, &l.Result,
		&l.Deadline, &del, &l.DemotedAt, &l.Label, &l.Created, &l.Settled); err != nil {
		return nil, err
	}
	l.Delivered = del != 0
	return l, nil
}

func (d *DB) queryLinks(where string, args ...any) []*Link {
	rows, err := d.q.Query(`SELECT `+linkCols+` FROM links `+where, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*Link
	for rows.Next() {
		if l, err := scanLink(rows.Scan); err == nil {
			out = append(out, l)
		}
	}
	return out
}

func (d *DB) getLink(id int64) (*Link, error) {
	ls := d.queryLinks(`WHERE id=?`, id)
	if len(ls) == 0 {
		return nil, sql.ErrNoRows
	}
	return ls[0], nil
}

// latestLink is a child's most recent link (its current delegation).
func (d *DB) latestLink(childID int64) *Link {
	ls := d.queryLinks(`WHERE child_id=? ORDER BY id DESC LIMIT 1`, childID)
	if len(ls) == 0 {
		return nil
	}
	return ls[0]
}

// hasNotices reports background results waiting to be delivered to a run.
func (d *DB) hasNotices(runID int64) bool {
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM links WHERE parent_id=? AND mode='bg' AND delivered=0 AND state<>'running'`, runID).Scan(&n)
	return n > 0
}

// earliestDeadline is the soonest foreground deadline a run is waiting on.
func (d *DB) earliestDeadline(runID int64) int64 {
	var n sql.NullInt64
	_ = d.q.QueryRow(`SELECT MIN(deadline) FROM links WHERE parent_id=? AND mode='fg' AND delivered=0 AND state='running' AND deadline>0`, runID).Scan(&n)
	return n.Int64
}

// --- the child side --------------------------------------------------------------

// settleOwnLink is a child reporting back: its open link settles exactly once
// (a second call finds no running link and does nothing). The parent and any
// run started `after` this one are poked after the commit.
func (e *Engine) settleOwnLink(t *DB, child *Run, state, outcome, result string) {
	var id, parent int64
	err := t.q.QueryRow(`UPDATE links SET state=?, outcome=?, result=?, settled=? WHERE child_id=? AND state='running'
		RETURNING id, parent_id`, state, outcome, result, e.unix(), child.ID).Scan(&id, &parent)
	if err != nil {
		return
	}
	// Mirror onto the run row for readers of the old columns (the tree view).
	_, _ = t.q.Exec(`UPDATE runs SET outcome=?, settled_at=? WHERE id=?`, outcome, e.unix(), child.ID)
	e.emitLink(t, rootOf(child), id)
	deps := scanIDs(t.q.Query(`SELECT child_id FROM link_deps WHERE dep_link=?`, id))
	t.AfterCommit(func() {
		e.Poke(parent)
		for _, d := range deps {
			e.Poke(d)
		}
	})
}

// --- the parent side -------------------------------------------------------------

// resolveAwait looks at what a parked run waits for: delivers settled
// foreground results into their placeholders, demotes the ones past their
// deadline (or all of them when the owner sent a message), resolves
// subagent_wait calls, and resumes the run when nothing is left. true = the
// run is running again.
func (e *Engine) resolveAwait(run *Run, ownerSpoke bool) bool {
	p := parsePending(run.Pending)
	if p.Kind == "deps" {
		return e.resolveDeps(run)
	}
	root := rootOf(run)
	resumed := false
	var wake int64
	err := e.fenced(func(t *DB) error {
		now := e.unix()
		fg := t.queryLinks(`WHERE parent_id=? AND mode='fg' AND delivered=0 ORDER BY id`, run.ID)
		budget := inlineBudget(len(fg))
		open := 0
		for _, l := range fg {
			switch {
			case l.State != linkRunning:
				e.deliverFg(t, run, root, l, formatLinkResult(t, l, budget))
			case ownerSpoke || (l.Deadline > 0 && l.Deadline <= now):
				why := "the owner sent a new message"
				if !ownerSpoke {
					why = fmt.Sprintf("still running after %s", humanDur(now-l.Created))
				}
				e.demote(t, run, root, l, why)
			default:
				open++
			}
		}
		var keep []waitEntry
		for _, w := range p.Waits {
			ls := t.queryLinksIn(w.Links)
			if !waitDone(ls, w.Need) && !ownerSpoke && (w.Deadline == 0 || w.Deadline > now) {
				keep = append(keep, w)
				continue
			}
			e.settleCall(t, &turnState{run: run, root: root}, toolCall{ID: w.TC}, e.waitResult(t, ls))
		}
		if open == 0 && len(keep) == 0 {
			if err := t.setStatus(run.ID, statusRunning, 0, run.Result, ""); err != nil {
				return err
			}
			resumed = true
		} else {
			deadline := t.earliestDeadline(run.ID)
			for _, w := range keep {
				if w.Deadline > 0 && (deadline == 0 || w.Deadline < deadline) {
					deadline = w.Deadline
				}
			}
			raw := mustJSON(pendingState{Kind: "await", Waits: keep})
			if err := t.setStatus(run.ID, statusAwait, deadline, run.Result, raw); err != nil {
				return err
			}
			wake = deadline
		}
		e.emitRun(t, run.ID)
		return nil
	})
	if err != nil {
		return false
	}
	if resumed {
		e.disarmTimer(run.ID)
		run.Status, run.Pending = statusRunning, ""
	} else if wake > 0 {
		e.armTimer(run.ID, wake)
	}
	return resumed
}

func waitDone(ls []*Link, need string) bool {
	settled := 0
	for _, l := range ls {
		if l.State != linkRunning {
			settled++
		}
	}
	if need == "any" {
		return settled > 0 || len(ls) == 0
	}
	return settled == len(ls)
}

func (d *DB) queryLinksIn(ids []int64) []*Link {
	var out []*Link
	for _, id := range ids {
		if l, err := d.getLink(id); err == nil {
			out = append(out, l)
		}
	}
	return out
}

// deliverFg hands a settled foreground result to the call that spawned it.
func (e *Engine) deliverFg(t *DB, run *Run, root int64, l *Link, content string) {
	if l.ToolCallID != "" {
		e.settleCall(t, &turnState{run: run, root: root}, toolCall{ID: l.ToolCallID}, content)
	}
	_, _ = t.q.Exec(`UPDATE links SET delivered=1 WHERE id=? AND delivered=0`, l.ID)
	e.emitLink(t, root, l.ID)
}

// demote moves a foreground link to the background: the call that spawned it
// is answered now with a progress digest, and the answer arrives later as a
// notice.
func (e *Engine) demote(t *DB, run *Run, root int64, l *Link, why string) {
	res, err := t.q.Exec(`UPDATE links SET mode='bg', demoted_at=? WHERE id=? AND mode='fg' AND delivered=0`, e.unix(), l.ID)
	if err != nil || rowsAffected(res) == 0 {
		return
	}
	text := fmt.Sprintf("(moved to the background — %s. #%d keeps working; its answer will arrive as a message when it finishes. "+
		"Use subagent_status to look in, subagent_wait to wait for it, subagent_cancel to stop it.)\n\n%s",
		why, l.ChildID, e.childDigest(t, l.ChildID, true))
	if l.ToolCallID != "" {
		e.settleCall(t, &turnState{run: run, root: root}, toolCall{ID: l.ToolCallID}, text)
	}
	e.emitStep(t, root, t.journal(run.ID, "note", map[string]string{"text": fmt.Sprintf("subagent #%d moved to the background: %s", l.ChildID, why)}))
	e.emitLink(t, root, l.ID)
}

// demoteStep demotes everything the current step started waiting for — the
// step is ending some other way (ask_user, yield, finish).
func (e *Engine) demoteStep(t *DB, ts *turnState, why string) {
	for _, l := range t.queryLinks(`WHERE parent_id=? AND mode='fg' AND delivered=0 AND state='running'`, ts.run.ID) {
		e.demote(t, ts.run, ts.root, l, why)
	}
	for _, l := range t.queryLinks(`WHERE parent_id=? AND mode='fg' AND delivered=0 AND state<>'running'`, ts.run.ID) {
		e.deliverFg(t, ts.run, ts.root, l, formatLinkResult(t, l, maxChildResult))
	}
	for _, w := range ts.waits {
		e.settleCall(t, ts, toolCall{ID: w.TC}, "(wait abandoned — "+why+")\n\n"+e.waitResult(t, t.queryLinksIn(w.Links)))
	}
	ts.waits = nil
}

// deliverNotices appends ONE message with every background result that has
// settled since the last boundary. Returns how many it delivered.
func (e *Engine) deliverNotices(t *DB, ts *turnState) int {
	ls := t.queryLinks(`WHERE parent_id=? AND mode='bg' AND delivered=0 AND state<>'running' ORDER BY settled, id`, ts.run.ID)
	if len(ls) == 0 {
		return 0
	}
	budget := inlineBudget(len(ls))
	var b strings.Builder
	b.WriteString("[subagent results — your workflow layer reporting, not the owner]\n")
	for _, l := range ls {
		b.WriteString("\n")
		b.WriteString(formatLinkResult(t, l, budget))
		b.WriteString("\n")
	}
	m := &Message{RunID: ts.run.ID, Role: "user", Content: strings.TrimSpace(b.String())}
	if _, err := t.addMessage(m); err != nil {
		return 0
	}
	e.emitMessage(t, ts.root, m)
	for _, l := range ls {
		_, _ = t.q.Exec(`UPDATE links SET delivered=1 WHERE id=?`, l.ID)
		e.emitLink(t, ts.root, l.ID)
	}
	return len(ls)
}

// resolveDeps starts a child that was waiting for its `after` runs, handing
// it their results. A failed or cancelled dependency is reported, not waited
// on.
func (e *Engine) resolveDeps(run *Run) bool {
	type dep struct {
		link *Link
		run  int64
	}
	var deps []dep
	rows, err := e.db.q.Query(`SELECT dep_link, dep_run FROM link_deps WHERE child_id=? ORDER BY dep_link`, run.ID)
	if err != nil {
		return false
	}
	var pairs [][2]int64
	for rows.Next() {
		var l, r int64
		if rows.Scan(&l, &r) == nil {
			pairs = append(pairs, [2]int64{l, r})
		}
	}
	rows.Close()
	for _, p := range pairs {
		l, _ := e.db.getLink(p[0])
		if l != nil && l.State == linkRunning {
			return false // still waiting
		}
		deps = append(deps, dep{link: l, run: p[1]})
	}
	resumed := false
	err = e.fenced(func(t *DB) error {
		var b strings.Builder
		b.WriteString("[results of the runs you were started after]\n")
		budget := inlineBudget(len(deps))
		for _, d := range deps {
			b.WriteString("\n")
			if d.link == nil {
				fmt.Fprintf(&b, "--- #%d (gone: deleted before it finished) ---\n", d.run)
				continue
			}
			b.WriteString(formatLinkResult(t, d.link, budget))
			b.WriteString("\n")
		}
		if len(deps) > 0 {
			m := &Message{RunID: run.ID, Role: "user", Content: strings.TrimSpace(b.String())}
			if _, err := t.addMessage(m); err != nil {
				return err
			}
			e.emitMessage(t, rootOf(run), m)
		}
		if err := t.setStatus(run.ID, statusRunning, 0, run.Result, ""); err != nil {
			return err
		}
		e.emitRun(t, run.ID)
		resumed = true
		return nil
	})
	if err == nil && resumed {
		run.Status, run.Pending = statusRunning, ""
	}
	return resumed
}

// --- formatting ---------------------------------------------------------------------

// inlineBudget splits one report between the runs being reported, so N
// children cost one report rather than N × the tool-result cap.
func inlineBudget(n int) int {
	if n < 1 {
		n = 1
	}
	b := (8 << 10) / n
	if b < 512 {
		b = 512
	}
	if b > maxChildResult {
		b = maxChildResult
	}
	return b
}

func formatLinkResult(t *DB, l *Link, budget int) string {
	body := strings.TrimSpace(l.Result)
	if body == "" {
		body = "(no result)"
	}
	if len(body) > budget {
		body = strings.ToValidUTF8(body[:budget], "") +
			fmt.Sprintf("\n…[%d bytes elided — subagent_result %d for the whole thing]", len(body)-budget, l.ChildID)
	}
	label := l.Label
	if label == "" {
		if r, err := t.getRun(l.ChildID); err == nil {
			label = r.Title
		}
	}
	return fmt.Sprintf("--- #%d %s (%s) ---\n%s", l.ChildID, label, outcomeWord(l), body)
}

func outcomeWord(l *Link) string {
	switch l.State {
	case linkRunning:
		return "running"
	case linkError:
		return "failed"
	case linkCanceled:
		if l.Outcome == outcomeInterrupted {
			return "interrupted"
		}
		return "cancelled"
	}
	if l.Outcome == outcomeIncomplete {
		return "incomplete"
	}
	return "done"
}

// waitResult reports a subagent_wait: results for what settled (and marks
// them delivered — the wait consumed them), digests for what still runs.
func (e *Engine) waitResult(t *DB, ls []*Link) string {
	if len(ls) == 0 {
		return "nothing to wait for"
	}
	budget := inlineBudget(len(ls))
	var parts []string
	for _, l := range ls {
		if l.State == linkRunning {
			parts = append(parts, e.childDigest(t, l.ChildID, len(ls) <= 3))
			continue
		}
		parts = append(parts, formatLinkResult(t, l, budget))
		_, _ = t.q.Exec(`UPDATE links SET delivered=1 WHERE id=?`, l.ID)
	}
	return strings.Join(parts, "\n\n")
}

// childDigest says what a child is doing right now — the parent's window into
// its subagents: phase, time, cost, its last few tool calls (by their
// summaries) and its latest text.
func (e *Engine) childDigest(t *DB, childID int64, detail bool) string {
	r, err := t.getRun(childID)
	if err != nil {
		return fmt.Sprintf("#%d: gone", childID)
	}
	l := t.latestLink(childID)
	since := r.Created
	if l != nil {
		since = l.Created
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s — %s for %s, %d model call(s)", r.ID, r.Title, phaseWord(r, l), humanDur(e.unix()-since), r.LLMCalls)
	if !detail {
		return b.String()
	}
	msgs, _ := t.messages(childID, false)
	var recent []string
	for i := len(msgs) - 1; i >= 0 && len(recent) < 3; i-- {
		m := msgs[i]
		if m.Role != "assistant" || m.ToolCalls == "" {
			continue
		}
		var calls []toolCall
		if decodeCalls(m.ToolCalls, &calls) {
			for j := len(calls) - 1; j >= 0 && len(recent) < 3; j-- {
				c := calls[j]
				res := ""
				for _, tm := range msgs[i+1:] {
					if tm.Role == "tool" && tm.ToolCallID == c.ID {
						res = oneLine(tm.Content)
						break
					}
				}
				recent = append(recent, fmt.Sprintf("  - %s → %s", callHeadline(c), clip(res, 100)))
			}
		}
	}
	if len(recent) > 0 {
		b.WriteString("\n  recent:\n")
		for i := len(recent) - 1; i >= 0; i-- {
			b.WriteString(recent[i] + "\n")
		}
	}
	if txt := t.lastAssistant(childID); txt != "" {
		fmt.Fprintf(&b, "  latest text: %s\n", clip(oneLine(txt), 240))
	}
	if parsePending(r.Pending).Kind == "approval" {
		b.WriteString("  waiting for the owner to approve a tool call\n")
	}
	var queued int
	_ = t.q.QueryRow(`SELECT count(*) FROM inbox WHERE run_id=? AND kind='user' AND delivered_at=0`, childID).Scan(&queued)
	if queued > 0 {
		fmt.Fprintf(&b, "  %d message(s) queued for it\n", queued)
	}
	return strings.TrimRight(b.String(), "\n")
}

// phaseWord maps a run onto a small vocabulary a model can act on.
func phaseWord(r *Run, l *Link) string {
	switch r.Status {
	case statusRunning:
		return "working"
	case statusAwait:
		if parsePending(r.Pending).Kind == "deps" {
			return "waiting for the runs it was started after"
		}
		return "waiting on its own subagents"
	case statusWaiting:
		if parsePending(r.Pending).Kind == "approval" {
			return "waiting for approval"
		}
		return "waiting for the owner"
	case statusSleep:
		return "sleeping"
	}
	if l != nil && l.State != linkRunning {
		return outcomeWord(l)
	}
	return r.Status
}

// callHeadline is a call's one-line description: its summary, else its name.
func callHeadline(c toolCall) string {
	args := decodeArgs(c.Function.Arguments)
	if s, _ := args[summaryParam].(string); strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	return c.Function.Name
}

func humanDur(secs int64) string {
	d := time.Duration(secs) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", secs)
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
	}
	return fmt.Sprintf("%dh%02dm", secs/3600, (secs%3600)/60)
}

// linkView is a link as the tile sees it, with its child's summary.
func (e *Engine) linkView(l *Link) map[string]any {
	v := map[string]any{
		"id": l.ID, "parentId": l.ParentID, "childId": l.ChildID, "toolCallId": l.ToolCallID,
		"mode": l.Mode, "state": l.State, "outcome": l.Outcome, "result": clip(l.Result, 600),
		"deadline": l.Deadline, "delivered": l.Delivered, "demotedAt": l.DemotedAt, "label": l.Label,
		"created": l.Created, "settled": l.Settled,
	}
	if r, err := e.db.getRun(l.ChildID); err == nil {
		v["child"] = runSummary(r)
		v["phase"] = phaseWord(r, l)
	}
	return v
}

// --- child config ---------------------------------------------------------------------

// childConfig derives a node's config from its parent's. The capability lane
// is re-normalized FROM THE PARENT and never from arguments.
func childConfig(parent Config, system string) Config {
	c := parent
	c.Toolset = parent.toolset()
	c.System = parent.System
	if strings.TrimSpace(system) != "" {
		c.System = system
	}
	c.System += subagentContract
	if parent.Features != nil {
		c.Features = make(map[string]bool, len(parent.Features))
		for k, v := range parent.Features {
			c.Features[k] = v
		}
	}
	if parent.MCP != nil {
		c.MCP = append([]MCPServer(nil), parent.MCP...)
	}
	c.Deny = append([]string(nil), parent.Deny...)
	return c
}

const subagentContract = "\n\nYou are a subagent working for a parent run. When you are done, answer with the result " +
	"(or call finish with it): that answer is what your parent receives, so make it self-contained — the answer, the " +
	"sources you used (ids/URLs/paths), and anything you could not determine. Keep it under about 400 words unless the " +
	"task asks for more. Your parent may send you messages while you work; treat them as instructions. You cannot " +
	"ask the human anything; if you are blocked, say exactly what is missing. If you start subagents of your own, " +
	"wait for them before you answer — anything still running when you answer is stopped."
