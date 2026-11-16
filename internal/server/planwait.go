package server

// planwait.go — a plan's lines that wait (D243; docs/protocol/delegation.md
// §Plans → Lines that wait).
//
// One kind of line can be approved before it can be applied: a binding to
// a tile that provides nothing of what the slot wants YET, because a tile
// agent an earlier line of the same plan starts is still to write it ("bind
// the assistant's mcp slot to the dice tile" before the dice tile is an MCP
// server). The person approves it on the plan's one review; xbind applies
// it by itself, as them, the moment the tile's manifest declares exactly
// what was approved — and never otherwise:
//
//   - it is pinned (delegation.PlanWait) to the consumer's slot, the
//     provider tile, and the provide's kind, service and role. A tile that
//     declares anything else is not bound: the line stays waiting, says
//     why (ItemWait.Held), and its person is told;
//   - it lapses delegation.MaxWait after the plan was applied;
//   - it is cancelled when the person's agent stops for good or they lose
//     it, when the person takes the plan back (reject, undo) or their
//     agent does, when either tile is deleted or changes owner, when the
//     slot was bound by someone meanwhile — and when, at the moment it
//     would apply, any check a line gets at Apply refuses it: the person's
//     own rights (the handler's check, run as them now), the conversation's
//     scope and hidden tiles, both tiles' agent levels, the orgs' agent
//     rules (lineAgentRefusal);
//   - while the person's agent is paused or held it neither applies nor
//     ends: it waits. Every other stop — workspace agents off, the agent
//     taken from the person, anything else the gate refuses with — ends
//     it, judged here at each look (waitStopped), whatever the path that
//     stopped the agent did;
//   - it applies at most once (waitMu, and the item's result), and the
//     call is audited as a line of the person's approved plan.
//
// Nothing else waits: only binding.set carries a PlanWait
// (TestOnlyABindingWaits), and an admin's line to the LLM connector is
// applied at Apply as ever.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
)

// WaitPreviewer is the broker's side of lines that wait: a plan's line
// described after the set's earlier starts, and a waiting line judged as it
// stands now.
type WaitPreviewer interface {
	PreviewPlan(p auth.Principal, op delegation.ChangeOp, planned []delegation.PlannedTile, starts []delegation.PlannedStart) (delegation.ChangePreview, error)
	PreviewWait(p auth.Principal, op delegation.ChangeOp, w delegation.PlanWait) (delegation.ChangePreview, delegation.WaitState, string, error)
}

// startsOf are cs's agent.start lines before line n that are (applied:
// were) started — the tile agents that may write what line n waits for.
func startsOf(items []delegation.ChangeItem, n int, applied bool) []delegation.PlannedStart {
	var out []delegation.PlannedStart
	for _, it := range items {
		if it.N >= n || it.Kind != delegation.KindAgentStart || it.Blocked != "" || it.NeedsAdmin || applied && it.Result != delegation.ItemApplied {
			continue
		}
		if tile := shownTile(delegation.ChangePreview{Start: it.Start}); tile != "" {
			out = append(out, delegation.PlannedStart{N: it.N, Tile: tile})
		}
	}
	return out
}

// personByID is user as a person acting in their own session: the one a
// waiting line they approved is applied as. False: no such account now.
func (s *Server) personByID(user string) (auth.Principal, bool) {
	if s.Auth == nil || s.Auth.Users == nil || user == "" {
		return auth.Principal{}, false
	}
	u, ok := s.Auth.Users.Get(user)
	if !ok || u.Disabled {
		return auth.Principal{}, false
	}
	acc, _ := s.Auth.Users.Access(user)
	return auth.Principal{UserID: user, User: u, Access: acc, Via: "session"}, true
}

// startWaiting marks a chosen line that waits as approved and waiting: from
// now it lapses in MaxWait, and is held to the tiles' owners and the
// slot's binding as they are. It is watched once its set is written
// (watchWaits) — not from here: a look at the waiting lines before the
// write would find the person and nothing of theirs waiting, and drop them.
func (s *Server) startWaiting(it *delegation.ChangeItem, pv delegation.ChangePreview) {
	w := *it.Wait
	w.Until = time.Now().UTC().Truncate(time.Second).Add(delegation.MaxWait)
	w.Owners = []string{s.policy().OwnerOf(w.Component), s.policy().OwnerOf(w.Tile)}
	w.Was, w.Held = string(pv.Before), ""
	it.Wait, it.Result, it.Error = &w, delegation.ItemWaiting, ""
}

// watchWaits has user's waiting lines looked at from now on, and looks at
// them once: called when a set with a waiting line has been written (a
// scan that landed while the plan was being applied saw nothing of it, and
// the next may be far off).
func (s *Server) watchWaits(user string) {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	if s.waitUsers == nil {
		s.waitUsers = map[string]bool{}
	}
	if s.waitUsers[user] = true; !s.checkWaitsLocked(user) {
		delete(s.waitUsers, user)
	}
}

// waitStopped is why a waiting line of user's ends because their agent is
// stopped — "" while it runs, or is only paused or held (the line waits
// that out). Judged at the line itself, every time it is looked at: a
// stop cancels what waits wherever it was made, and whether or not that
// path remembered the waiting lines (delegationStopped does, for those
// that call it).
func (s *Server) waitStopped(user, holder string) string {
	switch refusal, _ := s.decideRefusal(user, holder); refusal {
	case "", "paused", "held":
		return ""
	case "workspace-off":
		return "workspace agents were turned off for this workspace before it was applied: nothing was bound"
	case "no-agent":
		return "you no longer have a workspace agent, and it was its plan: nothing was bound"
	default:
		return "your workspace agent was stopped before it was applied: nothing was bound"
	}
}

// CheckPlanWaits looks at every waiting line: applies the ones whose tile
// now provides what was approved, ends the ones that can no longer apply.
// Called after each registry scan (a manifest changed) and at boot; cheap
// when nothing waits.
func (s *Server) CheckPlanWaits() {
	if s == nil || s.Delegation == nil {
		return
	}
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	if !s.waitsLoaded { // once: what waited when xbind last stopped
		s.waitsLoaded = true
		if s.waitUsers == nil {
			s.waitUsers = map[string]bool{}
		}
		for _, user := range s.Delegation.People() {
			sets, _ := s.Delegation.ChangeSets(user)
			for _, cs := range sets {
				if cs.Waiting() {
					s.waitUsers[user] = true
				}
			}
		}
	}
	for user := range s.waitUsers {
		if !s.checkWaitsLocked(user) {
			delete(s.waitUsers, user)
		}
	}
}

// checkWaitsLocked judges user's waiting lines (waitMu held); false: none
// is left waiting.
func (s *Server) checkWaitsLocked(user string) bool {
	sets, _ := s.Delegation.ChangeSets(user) // (a line whose time is up lapses on this read)
	left := false
	for _, cs := range sets {
		for i, it := range cs.Items {
			if it.Result != delegation.ItemWaiting || it.Wait == nil {
				continue
			}
			if s.judgeWait(cs, i) {
				left = true
			}
		}
	}
	return left
}

// settleWait writes a waiting line's end (result, why) — or, with result
// "waiting", what holds it — if it still waits, and tells its person.
func (s *Server) settleWait(cs delegation.ChangeSet, i int, result, why string, change func(*delegation.ChangeItem)) {
	_, err := s.Delegation.UpdateChangeSet(cs.User, cs.ID, func(c *delegation.ChangeSet) error {
		if i >= len(c.Items) || c.Items[i].Result != delegation.ItemWaiting || c.Items[i].Wait == nil {
			return delegation.ErrSetNotOpen
		}
		c.Items = append([]delegation.ChangeItem(nil), c.Items...)
		w := *c.Items[i].Wait
		if result == delegation.ItemWaiting {
			w.Held = why
		} else {
			w.Held = ""
			c.Items[i].Result, c.Items[i].Error = result, why
		}
		c.Items[i].Wait = &w
		if change != nil {
			change(&c.Items[i])
		}
		return nil
	})
	if err != nil {
		return
	}
	s.delegateEvent(cs.User, "changeset", cs.ID)
	if cs.From != "" { // an admin's copy of someone's request: the result goes back to the asker's line
		if done, ok := s.Delegation.ChangeSet(cs.User, cs.ID); ok && result != delegation.ItemWaiting {
			s.askDecided(done)
		}
	}
}

// judgeWait judges one waiting line as it stands now and applies or ends
// it (waitMu held). True: it still waits.
func (s *Server) judgeWait(cs delegation.ChangeSet, i int) bool {
	it := cs.Items[i]
	w := *it.Wait
	end := func(why string) bool {
		s.settleWait(cs, i, delegation.ItemCancelled, why, nil)
		s.waitAudit(cs, it, http.StatusConflict, why)
		return false
	}
	// stays: the tile provides nothing of it yet, or something other than was approved — the line waits on
	stays := func(state delegation.WaitState, why string) bool {
		if state == delegation.WaitPending {
			why = ""
		}
		if w.Held != why { // said once: the line, Needs you and the log
			s.settleWait(cs, i, delegation.ItemWaiting, why, nil)
			if why != "" {
				s.waitAudit(cs, it, http.StatusConflict, why)
			}
		}
		return true
	}
	wp, ok := s.ChangeSets.(WaitPreviewer)
	if !ok {
		return true
	}
	if it.Admin || cs.From != "" { // never: only a person's own binding waits. One on disk all the same ends here
		return end("a line that takes an admin's rights doesn't wait: nothing was bound")
	}
	person, ok := s.personByID(cs.User)
	if !ok {
		return end("the account that approved this can no longer apply changes")
	}
	if why := s.waitStopped(cs.User, cs.Agent); why != "" {
		return end(why)
	}
	if len(w.Owners) == 2 && (s.policy().OwnerOf(w.Component) != w.Owners[0] || s.policy().OwnerOf(w.Tile) != w.Owners[1]) {
		for _, t := range []string{w.Component, w.Tile} {
			if _, there := s.tileThere(t); !there {
				return end(t + " no longer exists")
			}
		}
		return end("a tile of this line changed owner since you approved it")
	}
	pv, state, why, err := wp.PreviewWait(person, it.ChangeOp, w.PlanWait)
	switch {
	case err != nil:
		return end(err.Error())
	case state == delegation.WaitGone:
		return end(why)
	case state == delegation.WaitPending, state == delegation.WaitHeld:
		return stays(state, why)
	}
	// the tile provides exactly what was approved: every check a line gets
	// when it is applied, as things are now
	agent := s.agentOf(cs, person)
	if refusal, _ := s.decideRefusal(cs.User, cs.Agent); refusal != "" {
		return true // paused or held: the line waits it out (every other stop ended it above, waitStopped)
	}
	switch {
	case agent.gone != "":
		return end(agent.gone)
	case s.lineAgentRefusal(agent, []string{w.Component, w.Tile}) != "":
		return end(s.lineAgentRefusal(agent, []string{w.Component, w.Tile}))
	case pv.Blocked != "":
		return end(pv.Blocked)
	case pv.Admin:
		return end("this now takes an admin's rights — it was yours to do when you approved it: nothing was bound")
	case string(pv.Before) != w.Was:
		return end(fmt.Sprintf("%s's %s was bound by someone since you approved this: nothing was changed", w.Component, w.Slot))
	}
	// (the bind reads the tile once more, under the lock it writes under, and records the approved role: WithBindWait)
	status, msg, _ := s.adminCallOut(delegation.WithBindWait(context.Background(), w.PlanWait), person, pv.Call)
	if status == http.StatusConflict { // the tile changed between the look above and the bind's own, under its lock: judged again as it is
		if _, state, why, err := wp.PreviewWait(person, it.ChangeOp, w.PlanWait); err == nil && (state == delegation.WaitPending || state == delegation.WaitHeld) {
			return stays(state, why)
		}
	}
	if status >= 300 {
		s.settleWait(cs, i, delegation.ItemFailed, msg, nil)
		s.waitAudit(cs, it, status, msg)
		return false
	}
	s.settleWait(cs, i, delegation.ItemApplied, "", func(done *delegation.ChangeItem) { done.Inverse = pv.Inverse })
	s.waitAudit(cs, it, http.StatusOK, "")
	slog.Info("plan: a waiting line applied", "user", cs.User, "set", cs.ID, "line", it.N, "tile", w.Tile)
	return false
}

// tileThere reports whether tile is a component now.
func (s *Server) tileThere(tile string) (string, bool) {
	if s.Reg == nil {
		return tile, true
	}
	_, ok := s.Reg.Component(tile)
	return tile, ok
}

// waitAudit records what came of a waiting line — applied, or refused and
// why — as a line of the person's approved plan (under: "waited").
func (s *Server) waitAudit(cs delegation.ChangeSet, it delegation.ChangeItem, status int, refused string) {
	row := delegation.Call{User: cs.User, Agent: cs.Agent, Method: http.MethodPost, Path: "/api/xbin/bindings", Tile: it.Wait.Component,
		Class: "changeset:" + it.Kind, Effect: true, Status: status, Refused: refused, Ticket: cs.ID, Session: cs.Session, Under: "waited"}
	if cs.From != "" {
		row.From, row.Session = cs.From, ""
		s.delegateAudit(delegation.Call{User: cs.From, Agent: cs.Agent, Method: row.Method, Path: row.Path, Tile: row.Tile, Class: row.Class, Effect: true,
			Status: status, Refused: refused, Ticket: cs.AskOf, Session: cs.Session, By: cs.User, Under: "waited"})
	}
	s.delegateAudit(row)
}

// cancelWaitsFrom ends the waiting lines of the admins' copies of asker's
// requests: the asker's agent stopped for good.
func (s *Server) cancelWaitsFrom(asker, why string) {
	for _, adm := range s.admins() {
		sets, _ := s.Delegation.ChangeSets(adm.UserID)
		for _, cs := range sets {
			if cs.From == asker && cs.Waiting() {
				s.cancelWaits(adm.UserID, cs.ID, why)
			}
		}
	}
}

// cancelWaits ends the waiting lines of user's set id ("" every set of
// theirs) with why, and answers how many there were.
func (s *Server) cancelWaits(user, id, why string) int {
	if s.Delegation == nil {
		return 0
	}
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	n := 0
	sets, _ := s.Delegation.ChangeSets(user)
	for _, cs := range sets {
		if id != "" && cs.ID != id {
			continue
		}
		for i, it := range cs.Items {
			if it.Result == delegation.ItemWaiting && it.Wait != nil {
				s.settleWait(cs, i, delegation.ItemCancelled, why, nil)
				n++
			}
		}
	}
	return n
}
