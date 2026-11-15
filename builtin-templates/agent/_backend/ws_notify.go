// ws_notify.go — how the workspace agent learns that something it waits
// for changed (coord_workspace.go; docs/workspace-agent.md §How approvals
// work). xbind posts a notice to this backend, as partition mail's
// principal, with ids and kinds only — request, approval, changeset,
// state, session, resync. A notice is an untrusted hint: nothing in it is
// acted on. It asks for a reconcile, which reads the state again through
// xbind's API, as the person. And no notice is relied on: xbind's expiry
// notices don't survive its restart, and a session a restart ended looks
// like any other end — so everything open is read again at start-up
// (wsBoot) and by a timer while anything is open.
//
// The reconcile is one worker for the process: a notification that arrives
// while it runs marks it dirty and it runs once more — a storm of them is
// two passes, not one per notification. It is idempotent: a request is
// decided once (its row is claimed before its ticket is used), a tile
// agent's change is told once (what the conversation was last told is on
// its row). While anything is still open a one-shot timer asks for another
// pass (wsPollEvery), so a notification that never arrived costs a delay,
// not a stuck conversation; the engine itself has no ticker.
//
// What a decided request does: an approved one is made — that one call,
// under its ticket — and what came of it is owed to the conversation as a
// workspace update, which wakes it at once (coord_wake.go). No tool call
// waits on a request, so a conversation with requests open still takes its
// tile agents' updates.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// wsNotifyPath is the notice path: what xbind's table of sealed components
// names for this holder. Notices arrive as partition mail's principal
// (xbin.ReadHolderNotice checks it).
const wsNotifyPath = "POST /delegate/notify"

// wsPollEvery is how long after a pass that left something open the next
// one runs without a notification (a var: tests shorten it).
var wsPollEvery atomic.Int64

func init() { wsPollEvery.Store(int64(45 * time.Second)) }

// wsSync is the reconcile worker's state.
var wsSync = struct {
	sync.Mutex
	running, dirty bool
	timer          *time.Timer
	passes         atomic.Int64 // passes run (tests count them)
	idle           chan struct{}
}{}

// handleWsNotify is the doorbell: answered at once, the reading done
// after. What the notice lists is not acted on — any notice, a resync
// among them, asks for the same reconcile of everything open.
func handleWsNotify(w http.ResponseWriter, r *http.Request) {
	n, err := xbin.ReadHolderNotice(r)
	if err != nil {
		if principal(r).kind != whoSystem { // the tile itself may ring it too
			xbin.WriteError(w, http.StatusForbidden, "only xbin rings this")
			return
		}
	} else if userMode() && n.Partition != "" && n.Partition != partitionKey() {
		xbin.WriteError(w, http.StatusForbidden, "a notice for another partition")
		return
	}
	wsKick()
	xbin.WriteJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

// wsKick asks for a reconcile: now, or once more after the one running.
func wsKick() {
	wsSync.Lock()
	if wsSync.running {
		wsSync.dirty = true
		wsSync.Unlock()
		return
	}
	wsSync.running = true
	wsSync.Unlock()
	go func() {
		for {
			ag := projAg()
			open := false
			if ag != nil && ag.db != nil && wsMode() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				open = ag.wsReconcile(ctx)
				cancel()
			}
			wsSync.passes.Add(1)
			wsSync.Lock()
			if wsSync.dirty {
				wsSync.dirty = false
				wsSync.Unlock()
				continue
			}
			wsSync.running = false
			wsSync.Unlock()
			if open {
				wsArmPoll()
			}
			return
		}
	}()
}

// wsArmPoll arms the one-shot timer for the next pass (nothing when one is
// armed already).
func wsArmPoll() {
	wsSync.Lock()
	defer wsSync.Unlock()
	if wsSync.timer != nil {
		return
	}
	wsSync.timer = time.AfterFunc(time.Duration(wsPollEvery.Load()), func() {
		wsSync.Lock()
		wsSync.timer = nil
		wsSync.Unlock()
		wsKick()
	})
}

// wsBoot catches up after a start: what was decided, and which tile agents
// are gone, while this backend was down.
func wsBoot() {
	ag := projAg()
	if ag == nil || !wsMode() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, s := range ag.wsOpenSessions() {
		ag.wsReconcileSession(ctx, s, true)
	}
	// an outcome owed since before the stop: look again
	if e := projEng(); e != nil {
		for _, id := range scanIDs(ag.db.q.Query(`SELECT run_id FROM ws_sessions`)) {
			e.Poke(id)
		}
	}
	wsKick()
}

// wsOpenSessions are the sessions with a request or a tile agent that may
// still change.
func (ag *Agent) wsOpenSessions() []*wsSession {
	rows, err := ag.db.q.Query(`SELECT DISTINCT run_id FROM ws_requests WHERE state IN ('pending','approved','proposed','applying','running')
		UNION SELECT DISTINCT run_id FROM ws_agents WHERE status IN ('starting','running','idle')`)
	if err != nil {
		return nil
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	var out []*wsSession
	for _, id := range ids {
		if s, err := ag.db.wsSession(id); err == nil {
			out = append(out, s)
		}
	}
	return out
}

// wsReconcile is one pass over every session with something open. True:
// something is still open.
func (ag *Agent) wsReconcile(ctx context.Context) bool {
	open := false
	for _, s := range ag.wsOpenSessions() {
		if ag.wsReconcileSession(ctx, s, false) {
			open = true
		}
	}
	return open
}

// wsReconcileSession reads s's open requests and live tile agents again
// (boot: the first pass after a start).
func (ag *Agent) wsReconcileSession(ctx context.Context, s *wsSession, boot bool) (open bool) {
	for _, r := range ag.db.wsReqs(`WHERE run_id=? AND state IN ('pending','approved','proposed','applying','running') ORDER BY id`, s.Run) {
		if ag.wsCheckReq(ctx, s, r) {
			open = true
		}
	}
	for _, row := range ag.db.wsAgents(`WHERE run_id=? AND status IN ('starting','running','idle') ORDER BY id`, s.Run) {
		var a wsAgent
		if err := ag.wsWith(ctx, s, func(sid string) (err error) { a, err = wsX.Agent(ctx, sid, row.Session); return }); err != nil {
			open = true // paused, off, unreachable: as it was
			continue
		}
		ag.wsSeen(ctx, s, row, a, boot)
		if cur, err := ag.db.wsAgentOf(s.Run, row.Session); err == nil && (cur.Status == "starting" || cur.Status == "running") {
			open = true
		}
	}
	return open
}

// --- requests ------------------------------------------------------------------------

// wsCheckReq reads one open request's state at xbind and acts on a
// decision. True: it is still open.
func (ag *Agent) wsCheckReq(ctx context.Context, s *wsSession, r *wsReq) bool {
	if r.Kind == wsReqChangeSet {
		var cs wsChangeSet
		if err := ag.wsWith(ctx, s, func(sid string) (err error) { cs, err = wsX.ChangeSet(ctx, sid, r.Ref); return }); err != nil {
			if wsMissing(err) {
				ag.wsOutcome(r, "expired", "xbin no longer keeps this proposal: it expired or was withdrawn. Nothing was applied.")
				return false
			}
			return true
		}
		if !cs.decided() {
			return true
		}
		applied, chosen := 0, 0
		for _, it := range cs.Items {
			if it.Result != "" && it.Result != "rejected" {
				chosen++
			}
			if it.Result == "applied" {
				applied++
			}
		}
		done := wsDone{State: cs.State, By: "person", Outcome: "your person decided — " + wsChangeSetText(cs)}
		if chosen > 0 || applied > 0 {
			done.Result = fmt.Sprintf("%d of %d changes applied", applied, chosen)
		}
		ag.wsSettled(r, done)
		return false
	}
	if r.State == "running" {
		// a ticket was being used when this backend stopped: xbind knows whether it was spent
		return ag.wsLostTicket(ctx, s, r)
	}
	if wsLocalRef(r.Ref) {
		return true // waits for Start on the brief's card (ws_routes.go), not for xbind
	}
	var xr wsRequest
	if err := ag.wsWith(ctx, s, func(sid string) (err error) { xr, err = wsX.Request(ctx, sid, r.Ref); return }); err != nil {
		if wsMissing(err) { // xbind dropped it: decided long ago, or its state was reset
			ag.wsOutcome(r, "expired", "xbin no longer keeps this request, so it was never approved. Nothing ran. Ask again if it is still wanted.")
			return false
		}
		return true
	}
	switch xr.State {
	case "pending":
		return true
	case "approved":
		ag.wsDecide(ctx, s, r, xr)
	case "denied":
		ag.wsSettled(r, wsDone{State: "denied", By: "person", Via: xr.Via,
			Outcome: "denied by your person. Nothing ran. Don't ask for it again unless they say so; if you don't know what they want instead, ask them."})
	case "expired":
		ag.wsOutcome(r, "expired", "expired — not approved within 24 hours. Nothing ran. Mention it when your person is back; file it again only if they still want it.")
	case "withdrawn":
		ag.wsOutcome(r, "withdrawn", "withdrawn before it was decided. Nothing ran.")
	case "used":
		ag.wsOutcome(r, "used", "the approved call was made, but its answer was lost. Check what it did (a read, tile_status) before you say it worked, and don't repeat it blindly.")
	}
	return false
}

// wsClaim takes r for running, once: the row leaves pending/approved for
// "running" in one statement, so two passes (or a pass and a click) never
// both use its ticket.
func (ag *Agent) wsClaim(r *wsReq) bool {
	res, err := ag.db.q.Exec(`UPDATE ws_requests SET state='running' WHERE id=? AND state IN ('pending','approved')`, r.ID)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// wsDecideByID runs request id now (a start its person's preferences let
// through).
func (ag *Agent) wsDecideByID(id int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r, err := ag.db.wsReq(id)
	if err != nil {
		return
	}
	if s, err := ag.db.wsSession(r.Run); err == nil {
		ag.wsDecide(ctx, s, r, wsRequest{})
	}
}

// wsDecide makes the call an approved request allows — once — and records
// what came of it.
func (ag *Agent) wsDecide(ctx context.Context, s *wsSession, r *wsReq, xr wsRequest) {
	if !ag.wsClaim(r) {
		return
	}
	ticket := r.Ref
	if wsLocalRef(ticket) {
		ticket = "" // confirmed on the brief; the person's preferences let the start through
	}
	how := "approved by your person"
	switch xr.Grant {
	case "hour":
		how = "approved by your person, for an hour"
	case "always":
		how = "approved by your person, from now on"
	}
	// the same in fields, for the page: how it was allowed (a standing
	// approval's scope and end are xbind's record of it), by whom, via what
	done := wsDone{State: "used", Grant: "once", By: "person", Via: orStr(xr.Via, map[bool]string{true: "page", false: ""}[ticket == ""])}
	if xr.Grant == "hour" || xr.Grant == "always" {
		done.Grant = "call"
		var standing []wsStanding
		if err := ag.wsWith(ctx, s, func(sid string) (err error) { standing, err = wsX.Standing(ctx, sid); return }); err == nil {
			for _, st := range standing {
				if st.Request == xr.ID {
					done.Grant, done.Until = orStr(st.Scope, "call"), st.Until*1000
				}
			}
		}
	}
	state, outcome := "used", ""
	fail := func(err error) {
		state = "failed"
		done.Result = clip(wsPlain(wsRefusal(err)), 240)
		switch {
		case errors.Is(err, xbin.ErrBadTicket):
			outcome = how + ", but the approval no longer covered the call when it was made (it expired after 15 minutes, or the tile changed). Nothing ran. Ask again if it is still wanted."
		default:
			outcome = how + ", but it failed: " + wsRefusal(err) + ". Check what state it left before trying again."
		}
	}
	switch r.Kind {
	case wsReqCall:
		c := wsCall{Method: r.Method, Path: r.Path, Body: r.Body, ContentType: r.CType, Ticket: ticket}
		var a wsAnswer
		if err := ag.wsWith(ctx, s, func(sid string) (err error) { a, err = wsX.Call(ctx, sid, c); return }); err != nil {
			fail(err)
			break
		}
		outcome = how + " — it ran: " + wsAnswerText(r.Tile, c, a)
		done.Status, done.Result = a.Status, fmt.Sprintf("%s %s → %d", c.Method, strings.TrimPrefix(c.Path, "/api/"+r.Tile), a.Status)
		if a.Status >= 400 {
			outcome += "\nThe tile answered with an error: the change may not have happened. Say so."
		}
	case wsReqSession:
		var start wsAgentStart
		_ = json.Unmarshal(r.Body, &start)
		// with a ticket: exactly the start that was asked — its brief is part
		// of what the person approved (an edit after the request was filed
		// refiles it, ws_routes.go). Confirmed on the brief's card instead:
		// the brief as it stands at the click.
		if ticket == "" && r.Brief != 0 && start.Resume == "" {
			if b, err := ag.db.wsBrief(r.Run, r.Brief); err == nil {
				start = wsStartOf(b, s.Owner)
			}
		}
		var a wsAgent
		err := ag.wsWith(ctx, s, func(sid string) (err error) { a, _, err = wsX.AgentStart(ctx, sid, start, ticket, false); return })
		if err != nil && a.ID == "" {
			fail(err)
			if r.Brief != 0 {
				_, _ = ag.db.q.Exec(`UPDATE ws_briefs SET state='draft', updated_ms=? WHERE id=?`, nowMs(), r.Brief)
			}
			break
		}
		row := ag.wsAddAgent(s, r, a)
		outcome = fmt.Sprintf("%s — tile agent #%d started in %s (%s, %s) and was told the brief. It is your person's session: they can open it and only they answer its questions. You are told when it ends its turn, fails or waits for them; until then go on with what doesn't depend on it.",
			how, row.ID, row.Tile, orStr(row.Harness, "coding agent"), map[bool]string{true: "asks before acting", false: "acts without asking"}[row.Asks])
		done.Result = fmt.Sprintf("tile agent #%d started in %s (%s)", row.ID, row.Tile, orStr(row.Harness, "coding agent"))
	case wsReqTemplate:
		var create wsTileCreate
		_ = json.Unmarshal(r.Body, &create)
		var path string
		if err := ag.wsWith(ctx, s, func(sid string) (err error) { path, _, err = wsX.TileCreate(ctx, sid, create, ticket, false); return }); err != nil {
			fail(err)
			break
		}
		outcome = fmt.Sprintf("%s — the tile %s now exists, made from %s. It is your person's. workspace_map lists it; a tile agent can build in it (tile_agent_brief).", how, path, create.Template)
		done.Result = "the tile " + path + " now exists"
	}
	done.State, done.Outcome = state, outcome
	ag.wsSettled(r, done)
	ag.wsRefresh(ctx, s, true)
}

// wsLostTicket: r was claimed and this backend stopped before recording
// what came of it. xbind says whether the ticket was spent.
func (ag *Agent) wsLostTicket(ctx context.Context, s *wsSession, r *wsReq) bool {
	var xr wsRequest
	err := ag.wsWith(ctx, s, func(sid string) (err error) { xr, err = wsX.Request(ctx, sid, r.Ref); return })
	switch {
	case err != nil && !wsMissing(err):
		return true
	case err == nil && xr.State == "approved": // never used: use it now
		_, _ = ag.db.q.Exec(`UPDATE ws_requests SET state='approved' WHERE id=? AND state='running'`, r.ID)
		r.State = "approved"
		ag.wsDecide(ctx, s, r, xr)
		return false
	}
	ag.wsOutcome(r, "used", "approved by your person, and the call was probably made — this agent restarted before it could read the answer. Check what it did (a read, tile_status, tile_agents) before you say it worked, and don't repeat it blindly.")
	return false
}

// wsDone is what came of a request: Outcome in the words the conversation
// is told, and the same in fields for the page (wsReq's).
type wsDone struct {
	State   string
	Outcome string
	Grant   string // once | call | tile
	Until   int64  // a standing approval's end, ms (0: until taken back)
	By      string // person | agent | xbin
	Via     string
	Status  int    // the HTTP status of the call that ran
	Result  string // one line
}

// wsOutcome is wsSettled for an end nobody chose a grant for: who ended it
// follows from how it ended.
func (ag *Agent) wsOutcome(r *wsReq, state, outcome string) {
	by := map[string]string{"expired": "xbin", "withdrawn": "agent", "denied": "person", "used": "person"}[state]
	ag.wsSettled(r, wsDone{State: state, Outcome: outcome, By: by})
}

// wsSettled records what came of r and owes it to the conversation as a
// workspace update. A decision is its person's act, like a message of
// theirs: it wakes the conversation now, whatever the coalescing window
// (which is for tile agents' chatter) — an idle one starts a turn, one in
// a turn reads it at its next step.
func (ag *Agent) wsSettled(r *wsReq, d wsDone) {
	res, err := ag.db.q.Exec(`UPDATE ws_requests SET state=?, outcome=?, decided_ms=?, owed=1,
		grant_scope=?, grant_until_ms=?, decided_by=?, decided_via=?, result_status=?, result=?
		WHERE id=? AND state IN ('pending','approved','proposed','applying','running')`,
		d.State, d.Outcome, nowMs(), d.Grant, d.Until, d.By, d.Via, d.Status, d.Result, r.ID)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return // decided already
	}
	if r.Brief != 0 && d.State != "used" {
		_, _ = ag.db.q.Exec(`UPDATE ws_briefs SET state=?, updated_ms=? WHERE id=? AND state='asked'`,
			map[bool]string{true: "denied", false: "draft"}[d.State == "denied"], nowMs(), r.Brief)
	}
	coordForget(r.Run)
	if e := projEng(); e != nil {
		e.Poke(r.Run)
	}
	ag.wsChanged(r.Run, "request")
}

// --- tile agents ---------------------------------------------------------------------

// wsAddAgent records a tile agent a request started.
func (ag *Agent) wsAddAgent(s *wsSession, r *wsReq, a wsAgent) *wsAgentRow {
	row := &wsAgentRow{Run: s.Run, Session: a.ID, Tile: orStr(a.Tile, r.Tile), Harness: orStr(a.Name, a.Provider), Mode: a.Mode, Asks: a.Asks || a.Mode == "",
		Brief: r.Brief, Status: orStr(a.Status, "starting"), Started: nowMs()}
	_ = ag.db.Tx(func(t *DB) error {
		if err := t.q.QueryRow(`INSERT INTO ws_agents (run_id, session, tile, harness, mode, asks, brief_id, status, told, started_ms, updated_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(session) DO UPDATE SET status=excluded.status, updated_ms=excluded.updated_ms RETURNING id`,
			row.Run, row.Session, row.Tile, row.Harness, row.Mode, row.Asks, row.Brief, row.Status, row.state(), row.Started, row.Started).Scan(&row.ID); err != nil {
			return err
		}
		if r.Brief != 0 {
			_, err := t.q.Exec(`UPDATE ws_briefs SET state='started', session=?, updated_ms=? WHERE id=?`, row.Session, nowMs(), r.Brief)
			return err
		}
		return nil
	})
	wsArmPoll()
	return row
}

// wsSeen brings a tile agent's row up to date with what xbind says, notes
// a change the conversation hasn't been told (each asks for a wake: a turn
// that ended, a failure, an end, a question for the person — "working" is
// never news), and delivers the messages held behind a question once the
// person answered it.
func (ag *Agent) wsSeen(ctx context.Context, s *wsSession, row *wsAgentRow, a wsAgent, boot bool) {
	next := *row
	next.Status = orStr(a.Status, "ended")
	if a.EndedBy == "restart" && !a.live() { // xbind says its own restart ended it: cut off, not finished
		next.Status = "stopped"
	}
	// just opened and not yet on its brief: an idle read is "about to work"
	if next.Status == "idle" && a.Turn == 0 && row.Turn == 0 && row.Status == "starting" && nowMs()-row.Started < wsStartGrace.Milliseconds() {
		next.Status = "starting"
	}
	next.Waits, next.Waiting = a.waits() && a.live(), ""
	if next.Waits {
		next.Waiting = clip(a.Waiting, needTextMax)
	}
	if a.Last != "" {
		next.Last = clip(a.Last, 300)
	}
	if a.Name != "" {
		next.Harness = a.Name
	}
	if a.Mode != "" {
		next.Mode, next.Asks = a.Mode, a.Asks
	}
	if a.Turn > 0 {
		next.Turn = a.Turn
	}
	next.Owed, next.Wake = next.state() != row.Told, next.state() != row.Told
	if next.state() == wsWorking {
		next.Told, next.Owed, next.Wake = wsWorking, false, false
	}
	if next == *row {
		ag.wsFlushHeld(ctx, s, &next)
		return
	}
	_, _ = ag.db.q.Exec(`UPDATE ws_agents SET status=?, waiting=?, waits=?, last=?, turn=?, harness=?, mode=?, asks=?, told=?, owed=?, wake=?, updated_ms=? WHERE id=?`,
		next.Status, next.Waiting, next.Waits, next.Last, next.Turn, next.Harness, next.Mode, next.Asks, next.Told, next.Owed, next.Wake, nowMs(), row.ID)
	ag.wsFlushHeld(ctx, s, &next)
	ag.wsChanged(row.Run, "agent")
	if next.Owed && next.Wake {
		if e := projEng(); e != nil {
			e.Poke(row.Run)
		}
	}
}

// wsStartGrace is how long a session just opened may read idle before its
// first turn shows.
const wsStartGrace = 15 * time.Second

// wsFlushHeld delivers the messages held behind a tile agent's question,
// in order, once it no longer waits for its person; a session that ended
// meanwhile takes none, and the conversation is told.
func (ag *Agent) wsFlushHeld(ctx context.Context, s *wsSession, row *wsAgentRow) {
	if row.Waits {
		return
	}
	ids, texts := ag.db.wsHeld(row.ID)
	if len(ids) == 0 {
		return
	}
	if !row.live() {
		_, _ = ag.db.q.Exec(`DELETE FROM ws_held WHERE agent_id=?`, row.ID)
		return
	}
	for i, id := range ids {
		if err := ag.wsWith(ctx, s, func(sid string) (err error) { _, err = wsX.AgentSay(ctx, sid, row.Session, texts[i]); return }); err != nil {
			return // the rest stay held, in order
		}
		_, _ = ag.db.q.Exec(`DELETE FROM ws_held WHERE id=?`, id)
	}
}

// wsChanged tells the conversation's page that what it shows beside the
// transcript changed (its cards: requests, briefs, tile agents): the run's
// own event, which the page answers by reading its cards again.
func (ag *Agent) wsChanged(runID int64, what string) {
	e := projEng()
	if e == nil {
		return
	}
	_ = e.db.Tx(func(t *DB) error {
		e.emitRun(t, runID)
		return nil
	})
}

// wsLocalRef: the request waits for a click on the brief's card here, not
// for an approval at xbind.
func wsLocalRef(ref string) bool {
	return ref == "" || strings.HasPrefix(ref, "brief:") || strings.HasPrefix(ref, "resume:")
}
