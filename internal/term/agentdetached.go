package term

// THE AGENT'S OWN TURN (codex's detached turn). A message steered into a
// turn that had just ended makes codex-acp start a turn of its own with it
// (sdk/acp SteerStartedNewTurn): the client runs no prompt, so no turn.end
// reports that turn's end, and every status the client restates meanwhile
// says idle. sdk/acp's DetachedTurn is the rule — the same one the agent
// template's engine follows — and this file is what the session does with
// it:
//
//   - from the steer's answer the session is running: a `status` event
//     says so, and a status of the client's that says idle while the turn
//     goes on is logged as running (waiting_permission while a request
//     waits) — so the tab shows a turn, a prompt is refused as busy, and a
//     message is held for the turn's end instead of being prompted into it;
//   - each event of the agent's puts the end off; once it has been quiet
//     for detachedQuiet — and nothing waits for an answer — the turn is
//     over: xbind logs a `turn.end` ({stopReason, detached: true}, no turn
//     number: it was no prompt's) and an idle `status`, and what was held
//     goes in;
//   - a cancel ends it here only (the client has no prompt to cancel): the
//     requests it waits on are answered cancelled, the turn.end says
//     cancelled.

import (
	"encoding/json"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	sdkacp "github.com/xbin-dev/xbin/sdk/acp"
)

// detachedQuiet is how long the agent's own turn goes without an event
// before it is over (a var: tests shorten it).
var detachedQuiet = sdkacp.DetachedQuiet

// beginDetached: the agent started a turn of its own with a steered message.
func (s *Session) beginDetached(m *Manager) {
	st := s.agent
	st.statusMu.Lock() // a status being logged lands before the turn began, or says it runs (restated)
	st.mu.Lock()
	st.det.Quiet = detachedQuiet
	st.det.Begin(time.Now())
	s.armQuietLocked(m)
	st.mu.Unlock()
	st.statusMu.Unlock()
	s.logEvent(m, agent.New(agent.EvStatus, map[string]any{"status": agent.StatusRunning}))
}

// armQuietLocked (re)arms the look at the turn's deadline (st.mu held).
func (s *Session) armQuietLocked(m *Manager) {
	st := s.agent
	if st.quiet != nil {
		st.quiet.Stop()
	}
	st.quiet = time.AfterFunc(time.Until(st.det.Deadline()), func() { s.quietDue(m) })
}

// waits: requests of the agent's wait for an answer.
func (st *agentState) waits() bool {
	return st.perms.Count() > 0 || len(st.drv.PendingElicitations()) > 0
}

// quietDue is the deadline: the turn is over when the agent has been quiet
// — a turn that waits on a person is not, however long that takes.
func (s *Session) quietDue(m *Manager) {
	st := s.agent
	waits := st.waits()
	st.mu.Lock()
	if !st.det.Running() {
		st.mu.Unlock()
		return
	}
	now := time.Now()
	if waits {
		st.det.Event(now)
	}
	if !st.det.Over(now) { // an event came meanwhile, or it waits: look again later
		s.armQuietLocked(m)
		st.mu.Unlock()
		return
	}
	st.mu.Unlock()
	s.endDetached(m, "end_turn")
}

// detachedEvent is an event of the driver's while the agent's own turn
// runs (the pump, before the event is logged): the turn goes on.
func (s *Session) detachedEvent(m *Manager, e agent.Event) agent.Event {
	st := s.agent
	st.mu.Lock()
	if st.det.Event(time.Now()) {
		s.armQuietLocked(m)
	}
	st.mu.Unlock()
	return e
}

// restated is a status about to be logged (logEvent, st.statusMu held —
// so the turn can't begin between this look and the log): while the
// agent's own turn runs, one that says the session rests — the client runs
// no prompt, so each one it restates does — says what the turn does
// instead. The turn's own end says idle once it is over (endDetached).
func (s *Session) restated(e agent.Event) agent.Event {
	st := s.agent
	st.mu.Lock()
	on := st.det.Running()
	st.mu.Unlock()
	if !on {
		return e
	}
	var d map[string]json.RawMessage
	var status string
	if json.Unmarshal(e.Data, &d) != nil || json.Unmarshal(d["status"], &status) != nil || status != agent.StatusIdle {
		return e
	}
	now := agent.StatusRunning
	if st.waits() {
		now = agent.StatusWaiting
	}
	d["status"], _ = json.Marshal(now)
	e.Data, _ = json.Marshal(d)
	return e
}

// endDetached ends the agent's own turn (why: end_turn when it went quiet,
// cancelled for an interrupt): its turn.end and the idle status that lets
// what was held go in. False when none ran.
func (s *Session) endDetached(m *Manager, why string) bool {
	if !s.stopDetached() {
		return false
	}
	s.logEvent(m, agent.New(agent.EvTurnEnd, map[string]any{"stopReason": why, "detached": true}))
	s.logEvent(m, agent.New(agent.EvStatus, map[string]any{"status": agent.StatusIdle}))
	return true
}

// stopDetached stops following the turn without a word (the session's
// end); false when none ran.
func (s *Session) stopDetached() bool {
	st := s.agent
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.quiet != nil {
		st.quiet.Stop()
		st.quiet = nil
	}
	return st.det.End()
}

// cancelTurn interrupts what runs: the client's turn (session/cancel), or
// the agent's own — which only ends here: the requests it waits on are
// answered cancelled, and its turn.end says cancelled.
func (m *Manager) cancelTurn(s *Session) error {
	st := s.agent
	st.mu.Lock()
	detached := st.det.Running()
	st.mu.Unlock()
	if !detached {
		return st.drv.Cancel()
	}
	for _, res := range st.perms.CancelAll() {
		_ = st.drv.RespondPermission(res)
	}
	for _, q := range st.drv.PendingElicitations() {
		_ = st.drv.RespondElicitation(q.EID, "cancel", nil, "cancel")
	}
	s.endDetached(m, "cancelled")
	return nil
}
