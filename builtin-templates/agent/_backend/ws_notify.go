// ws_notify.go — how the workspace agent learns that something it waits
// for changed (coord_workspace.go; docs/workspace-agent.md §How approvals
// work). xbind calls POST /delegate/notify on this backend with ids and
// kinds only — a request was allowed, denied, expired or withdrawn; a
// standing approval came or went; a change set was decided or applied; a
// session it started changed status or ended a turn; the agent was paused,
// resumed or switched off. A notification is an untrusted wake-up: nothing
// in its body is believed or even read beyond counting it. It asks for a
// reconcile, which reads the state again through xbind's API, as the
// person.
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
// under its ticket — and what came of it settles the tool call parked on
// it (coord_park.go); when no call is parked on it any more (the person
// wrote meanwhile, the turn was stopped) the outcome is owed to the
// conversation as a workspace update, and wakes it (coord_wake.go).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// The notification route and the principal xbind calls it as.
const (
	wsNotifyPath = "POST /delegate/notify"
	wsNotifyFrom = "xbin/delegate"
)

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

// handleWsNotify is the doorbell. Its body is counted, never trusted.
func handleWsNotify(w http.ResponseWriter, r *http.Request) {
	if from := xbin.Caller(r).From; from != wsNotifyFrom && principal(r).kind != whoSystem {
		xbin.WriteError(w, http.StatusForbidden, "only xbin rings this")
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 64<<10))
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
	// a session parked on a decision that landed just before the stop: look again
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

// wsReconcileSession reads s's open requests and live tile agents again.
// boot: the first pass after a start — a tile agent found gone was stopped
// by the restart, not by its work ending.
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
		ag.wsOutcome(r, cs.State, "your person decided — "+wsChangeSetText(cs))
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
		ag.wsDecide(ctx, s, r, xr.Grant)
	case "denied":
		ag.wsOutcome(r, "denied", "denied by your person. Nothing ran. Don't ask for it again unless they say so; if you don't know what they want instead, ask them.")
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
		ag.wsDecide(ctx, s, r, "")
	}
}

// wsDecide makes the call an approved request allows — once — and records
// what came of it.
func (ag *Agent) wsDecide(ctx context.Context, s *wsSession, r *wsReq, grant string) {
	if !ag.wsClaim(r) {
		return
	}
	ticket := r.Ref
	if wsLocalRef(ticket) {
		ticket = "" // confirmed on the brief; the person's preferences let the start through
	}
	how := "approved by your person"
	switch grant {
	case "hour":
		how = "approved by your person, for an hour"
	case "always":
		how = "approved by your person, from now on"
	}
	state, outcome := "used", ""
	fail := func(err error) {
		state = "failed"
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
		if a.Status >= 400 {
			outcome += "\nThe tile answered with an error: the change may not have happened. Say so."
		}
	case wsReqSession:
		var start wsAgentStart
		_ = json.Unmarshal(r.Body, &start)
		if r.Brief != 0 && start.Resume == "" { // the brief as it stands now: the bytes the approval was of
			if b, err := ag.db.wsBrief(r.Run, r.Brief); err == nil && ticket == "" {
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
		outcome = fmt.Sprintf("%s — tile agent #%d started in %s (%s, %s). It is your person's session: they can open it and only they answer its questions. You are told when it ends its turn, fails or waits for them; until then go on with what doesn't depend on it.",
			how, row.ID, row.Tile, orStr(row.Harness, "coding agent"), map[bool]string{true: "asks before acting", false: "acts without asking"}[row.Asks])
		if err != nil { // it opened, but was not told its brief
			outcome += " But its brief did not reach it (" + wsRefusal(err) + "): send it with tile_agent_message."
		}
	case wsReqTemplate:
		var create wsTileCreate
		_ = json.Unmarshal(r.Body, &create)
		var path string
		if err := ag.wsWith(ctx, s, func(sid string) (err error) { path, _, err = wsX.TileCreate(ctx, sid, create, ticket, false); return }); err != nil {
			fail(err)
			break
		}
		outcome = fmt.Sprintf("%s — the tile %s now exists, made from %s. It is your person's. workspace_map lists it; a tile agent can build in it (tile_agent_brief).", how, path, create.Template)
	}
	ag.wsOutcome(r, state, outcome)
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
		ag.wsDecide(ctx, s, r, xr.Grant)
		return false
	}
	ag.wsOutcome(r, "used", "approved by your person, and the call was probably made — this agent restarted before it could read the answer. Check what it did (a read, tile_status, tile_agents) before you say it worked, and don't repeat it blindly.")
	return false
}

// wsOutcome records what came of r and gets it to the conversation: into
// the tool call parked on it, or — when none is — as an update it is owed.
func (ag *Agent) wsOutcome(r *wsReq, state, outcome string) {
	res, err := ag.db.q.Exec(`UPDATE ws_requests SET state=?, outcome=?, decided_ms=? WHERE id=? AND state IN ('pending','approved','proposed','applying','running')`,
		state, outcome, nowMs(), r.ID)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return // decided already
	}
	if r.Brief != 0 && state != "used" {
		_, _ = ag.db.q.Exec(`UPDATE ws_briefs SET state=?, updated_ms=? WHERE id=? AND state='asked'`,
			map[bool]string{true: "denied", false: "draft"}[state == "denied"], nowMs(), r.Brief)
	}
	cur, err := ag.db.wsReq(r.ID)
	if err != nil {
		return
	}
	e := projEng()
	settled := false
	if cur.CallID != "" && e != nil {
		settled = e.coordSettle(cur.Run, cur.CallID, fmt.Sprintf("Request %d (%s): %s", cur.ID, cur.Title, outcome))
	}
	if !settled {
		_, _ = ag.db.q.Exec(`UPDATE ws_requests SET owed=1, call_id='' WHERE id=?`, cur.ID)
		if e != nil {
			e.Poke(cur.Run)
		}
	}
	ag.wsChanged(cur.Run, "request")
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
	if boot && (row.Status == "running" || row.Status == "starting") && !a.live() {
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
