package server

// changeset.go — admin change sets (docs/protocol/delegation.md §Change
// sets; D228). A delegate never calls the admin API. The agent of an admin
// who opted in PROPOSES a batch of admin operations, of a closed list of
// kinds (ChangePreviewer knows them: grants, bindings, org shares, limits —
// there is no "call this admin route" operation, so passwords, devices,
// SSO, the vault and view-as can't be in one). For each operation xbind
// writes its own plain-language line and reads the before/after itself, by
// running the checks of the handler that would apply it and nothing else.
//
// The PERSON whose agent proposed the set — nobody else, and never through
// the agent, its page, a tile, view-as or the root token — approves all,
// some or none, after a recent sign-in (the step-up device enrolment
// uses). xbind then applies the approved operations in order, as that
// person, through the handlers everyone uses (handleAPI: the same checks,
// restarts, events and audit line as if they had clicked). An operation
// whose state changed since the person saw it is stale and not applied; on
// the first failure the rest are skipped; what was applied stays, and each
// applied operation that can be undone is offered back as a new set
// (…/undo), which is approved like any other.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
)

// ChangePreviewer describes one change-set operation as person p would
// apply it now, changing nothing (the broker's PreviewChange). The error is
// for an operation that isn't one: an unknown kind, arguments that don't
// parse. An operation the handler would refuse comes back with Blocked set.
type ChangePreviewer interface {
	PreviewChange(p auth.Principal, op delegation.ChangeOp) (delegation.ChangePreview, error)
}

func (s *Server) registerChangeSetAPI() {
	s.RegisterAPI("POST /delegate/changesets", s.apiChangeSetNew)
	s.RegisterAPI("GET /delegate/changesets", s.apiChangeSets)
	s.RegisterAPI("GET /delegate/changesets/{id}", s.apiChangeSet)
	s.RegisterAPI("DELETE /delegate/changesets/{id}", s.apiChangeSetWithdraw)
	s.RegisterAPI("POST /delegate/changesets/{id}/approve", s.apiChangeSetApprove)
	s.RegisterAPI("POST /delegate/changesets/{id}/reject", s.apiChangeSetReject)
	s.RegisterAPI("POST /delegate/changesets/{id}/undo", s.apiChangeSetUndo)
	s.registerPlanAPI() // POST /delegate/plans, …/ask (plan.go)
}

// personOf is the person a delegate acts for, as a principal of their own:
// who a preview is judged as. It is never put on a request.
func personOf(p auth.Principal) auth.Principal {
	p.Agent, p.AgentModel = "", ""
	return p
}

// changeBasis is what the person was shown about an operation, hashed: the
// state it replaces AND xbind's line, effects and warnings. An item whose
// basis moved between the proposal and the approval is stale — a warning
// that appeared since (the tile was published meanwhile, a network set
// grew) is not something the person approved.
func changeBasis(pv delegation.ChangePreview) string {
	parts := []any{pv.Before, pv.After, pv.Line, pv.Effects, pv.Warnings}
	if pv.Pin != "" { // what else the line was described from (a template's manifest, a start's resolved settings)
		parts = append(parts, pv.Pin)
	}
	b, _ := json.Marshal(parts)
	return delegation.BodyHash(b)
}

// describeItems previews ops as person and builds the set's items; the
// error is the first operation that isn't one.
func (s *Server) describeItems(person auth.Principal, ops []delegation.ChangeOp, whys []string) ([]delegation.ChangeItem, error) {
	items := make([]delegation.ChangeItem, 0, len(ops))
	for i, op := range ops {
		pv, err := s.ChangeSets.PreviewChange(person, op)
		if err != nil {
			return nil, fmt.Errorf("operation %d (%s): %w", i+1, op.Kind, err)
		}
		it := delegation.ChangeItem{ChangeOp: op, Line: pv.Line, Before: pv.Before, After: pv.After, BeforeText: pv.BeforeText, AfterText: pv.AfterText, Effects: pv.Effects,
			Warnings: pv.Warnings, Blocked: pv.Blocked, Inverse: pv.Inverse, Basis: changeBasis(pv)}
		if i < len(whys) {
			it.Why = whys[i]
			if len(it.Why) > 500 {
				it.Why = it.Why[:500]
			}
		}
		items = append(items, it)
	}
	return items, nil
}

// apiChangeSetNew — POST /delegate/changesets {title, items: [{kind, args,
// why?}]}: the agent of an admin who opted in (PUT /delegate/state
// adminChanges) proposes a batch. Nothing is applied: → the set, each
// operation with xbind's line, before, after, effects, warnings and, when
// the handler would refuse it, blocked. 403 without the opt-in; 400 for an
// operation of no known kind; 409 past 10 open sets.
func (s *Server) apiChangeSetNew(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, false, false, true)
	if !ok {
		return
	}
	if s.ChangeSets == nil {
		WriteError(w, http.StatusNotImplemented, "this xbind applies no change sets", delegationDocs)
		return
	}
	person := personOf(a.p)
	if !mayOptInAdmin(person) || !s.Delegation.State(a.user).AdminChanges {
		WriteError(w, http.StatusForbidden, "an agent proposes admin changes only for an admin who let it (their agent's settings)", delegationDocs)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var in struct {
		Title string `json:"title"`
		Items []struct {
			Kind string          `json:"kind"`
			Args json.RawMessage `json:"args"`
			Why  string          `json:"why"`
		} `json:"items"`
	}
	if err := DecodeJSON(r, &in); err != nil || len(in.Items) == 0 {
		WriteError(w, http.StatusBadRequest, "need {title, items: [{kind, args, why?}]} with at least one operation", delegationDocs)
		return
	}
	if len(in.Items) > delegation.MaxItems {
		WriteError(w, http.StatusBadRequest, delegation.ErrTooManyItems.Error(), delegationDocs)
		return
	}
	ops, whys := make([]delegation.ChangeOp, len(in.Items)), make([]string, len(in.Items))
	for i, it := range in.Items {
		ops[i], whys[i] = delegation.ChangeOp{Kind: it.Kind, Args: it.Args}, it.Why
	}
	items, err := s.describeItems(person, ops, whys)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error(), delegationDocs)
		return
	}
	cs, err := s.Delegation.NewChangeSet(delegation.ChangeSet{User: a.user, Agent: a.p.Agent, Title: in.Title, Items: items})
	switch {
	case errors.Is(err, delegation.ErrTooManySets):
		WriteError(w, http.StatusConflict, err.Error(), delegationDocs)
		return
	case errors.Is(err, delegation.ErrSetTooLarge):
		WriteError(w, http.StatusRequestEntityTooLarge, err.Error(), delegationDocs)
		return
	case err != nil:
		WriteError(w, http.StatusBadRequest, err.Error(), delegationDocs)
		return
	}
	s.delegateEvent(a.user, "changeset", cs.ID)
	WriteJSON(w, http.StatusOK, s.setFor(a, cs))
}

// apiChangeSets — GET /delegate/changesets: the person, their agent's page
// or their agent → the person's sets, oldest first.
func (s *Server) apiChangeSets(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, true, true)
	if !ok {
		return
	}
	sets, err := s.Delegation.ChangeSets(a.user)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "the change sets can't be read", delegationDocs)
		return
	}
	if !a.own { // another person's request to an admin is the admin's to read — not their agent's, nor its page's
		sets = slices.DeleteFunc(slices.Clone(sets), func(cs delegation.ChangeSet) bool { return cs.From != "" })
		for i := range sets {
			sets[i] = s.setFor(a, sets[i]) // …and of an admin's or a blocked line, xbind's sentence only (plan.go)
		}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"changesets": sets})
}

// apiChangeSet — GET /delegate/changesets/{id}: one set.
func (s *Server) apiChangeSet(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, true, true)
	if !ok {
		return
	}
	cs, found := s.Delegation.ChangeSet(a.user, r.PathValue("id"))
	if !found || cs.From != "" && !a.own {
		WriteError(w, http.StatusNotFound, "no such change set", delegationDocs)
		return
	}
	WriteJSON(w, http.StatusOK, s.setFor(a, cs))
}

// endChangeSet moves an open set to state (withdrawn, rejected).
func (s *Server) endChangeSet(w http.ResponseWriter, a delegateActor, id, state string) bool {
	if cur, ok := s.Delegation.ChangeSet(a.user, id); ok && !cur.Open() && unsettled(cur) && (cur.From == "" || a.own) {
		// decided, with a line that still waits — for xbind, or with the
		// admins: taking the plan back ends both, here and in every admin's
		// copy. The person's own act, or their agent's (it only narrows)
		why := "you cancelled it before it was applied"
		if !a.own {
			why = "your agent took the plan back before it was applied"
		}
		s.takeBack(a.user, id, why)
		cur, _ = s.Delegation.ChangeSet(a.user, id)
		WriteJSON(w, http.StatusOK, s.setFor(a, cur))
		return true
	}
	cs, err := s.Delegation.UpdateChangeSet(a.user, id, func(cs *delegation.ChangeSet) error {
		switch {
		case cs.From != "" && !a.own:
			return delegation.ErrNoChangeSet // another person's request: the admin's own to decide
		case !cs.Open():
			return delegation.ErrSetNotOpen
		}
		cs.State, cs.Decided = state, time.Now().UTC().Truncate(time.Second)
		if cs.From != "" {
			cs.State = delegation.SetRejected // an admin turning a request down, whichever button said it
		}
		return nil
	})
	if err == nil {
		s.askDecided(cs)
		if cs.From == "" && cs.Plan { // the asker ends their own set: what they asked the admins for goes with it
			s.retractAsks(a.user, cs.ID, "the plan was ended before an admin decided")
			cs, _ = s.Delegation.ChangeSet(a.user, cs.ID)
		}
	}
	switch {
	case errors.Is(err, delegation.ErrNoChangeSet):
		WriteError(w, http.StatusNotFound, err.Error(), delegationDocs)
		return false
	case errors.Is(err, delegation.ErrSetNotOpen):
		WriteError(w, http.StatusConflict, err.Error(), delegationDocs)
		return false
	case err != nil:
		WriteError(w, http.StatusInternalServerError, "the change set can't be written", delegationDocs)
		return false
	}
	s.delegateEvent(a.user, "changeset", cs.ID)
	WriteJSON(w, http.StatusOK, s.setFor(a, cs))
	return true
}

// apiChangeSetWithdraw — DELETE /delegate/changesets/{id}: the agent takes
// its proposal back (or the person dismisses it).
func (s *Server) apiChangeSetWithdraw(w http.ResponseWriter, r *http.Request) {
	if a, ok := s.delegateCaller(w, r, true, false, true); ok {
		s.endChangeSet(w, a, r.PathValue("id"), delegation.SetWithdrawn)
	}
}

// apiChangeSetReject — POST /delegate/changesets/{id}/reject: the person.
// {via: "notification"} says the person rejected it from a notification's
// action (D241): the log says so.
func (s *Server) apiChangeSetReject(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, false, false)
	if !ok {
		return
	}
	var body struct {
		Via string `json:"via"`
	}
	if r.ContentLength != 0 {
		if err := DecodeJSON(r, &body); err != nil || body.Via != "" && body.Via != viaNotification {
			WriteError(w, http.StatusBadRequest, `need {via?: "notification"}`, delegationDocs)
			return
		}
	}
	if s.endChangeSet(w, a, r.PathValue("id"), delegation.SetRejected) && body.Via != "" {
		s.delegateAudit(delegation.Call{User: a.user, Method: "POST", Path: "/api/xbin/delegate/changesets/" + r.PathValue("id") + "/reject",
			Class: "decision:" + delegation.SetRejected, Effect: true, Status: http.StatusOK, Ticket: r.PathValue("id"), Via: body.Via})
	}
}

// apiChangeSetApprove — POST /delegate/changesets/{id}/approve {items?:
// [n, …], password?}: the person whose agent proposed the set, in their
// own session, app or device — with a sign-in under 10 minutes old, or
// their password in the body (403 {stepUp} otherwise, as for adding a
// device). items names the operations to apply (absent: every one that
// isn't blocked); the rest are left out. xbind applies them in order as
// the person, through the handlers everyone uses. → the set, each
// operation with its result: applied | failed (error) | stale | skipped |
// rejected | blocked.
func (s *Server) apiChangeSetApprove(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, false, false)
	if !ok {
		return
	}
	if s.ChangeSets == nil {
		WriteError(w, http.StatusNotImplemented, "this xbind applies no change sets", delegationDocs)
		return
	}
	var body struct {
		// Items are the operations to apply, by number: these and no others.
		// Applying is never a default — no body, an absent key, null and []
		// all name nothing, and nothing is applied (400; the set stays open).
		Items    []int  `json:"items"`
		Password string `json:"password"`
	}
	if r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := DecodeJSON(r, &body); err != nil {
			WriteError(w, http.StatusBadRequest, "need {items: [n, …], password?}", delegationDocs)
			return
		}
	}
	id := r.PathValue("id")
	cs, found := s.Delegation.ChangeSet(a.user, id)
	switch {
	case !found:
		WriteError(w, http.StatusNotFound, "no such change set", delegationDocs)
		return
	case !cs.Open():
		WriteError(w, http.StatusConflict, delegation.ErrSetNotOpen.Error(), delegationDocs)
		return
	}
	// applying is refused while the agent is stopped, as allowing a request
	// is (rejecting never is). A request from another person is held to the
	// state of THEIR agent, which proposed it.
	whose := a.user
	if cs.From != "" {
		whose = cs.From
		if !a.p.IsAdmin() {
			WriteError(w, http.StatusForbidden, "a request to the admins is applied by a workspace admin", delegationDocs)
			return
		}
		// …and to the asker's own set as it stands now: a plan they took
		// back, that expired or that is gone asks for nothing any more —
		// whatever path ended it (the copies are withdrawn where xbind sees
		// the end happen; this holds for the ones it did not)
		if why := s.askWithdrawn(cs); why != "" {
			now := time.Now().UTC().Truncate(time.Second)
			_, _ = s.Delegation.UpdateChangeSet(a.user, id, func(c *delegation.ChangeSet) error {
				c.State, c.Decided, c.DecidedBy = delegation.SetWithdrawn, now, cs.From
				return nil
			})
			s.delegateEvent(a.user, "changeset", id)
			WriteError(w, http.StatusConflict, why, delegationDocs)
			return
		}
	}
	if refusal, msg := s.decideRefusal(whose, cs.Agent); refusal != "" {
		refuseDecision(w, refusal, msg)
		return
	}
	chosen := body.Items
	if len(chosen) == 0 {
		WriteError(w, http.StatusBadRequest, "say which operations to apply: {items: [n, …]} — nothing is applied by default (to turn the set down: …/reject)", delegationDocs)
		return
	}
	for _, n := range chosen {
		if n < 1 || n > len(cs.Items) {
			WriteError(w, http.StatusBadRequest, fmt.Sprintf("the set has no operation %d", n), delegationDocs)
			return
		}
	}
	// a plan: what was chosen must be the person's to apply, with the lines
	// it needs; only an admin's line takes the step-up (plan.go). An admin
	// who ticks an admin's line and applies it, on a recent sign-in, has
	// consented to that line of that plan: the standing opt-in
	// (adminChanges) is what lets the agent READ admin state and propose
	// admin-only change sets, and is not asked for here — without it the
	// agent proposed the line blind and learns of it only whether it was
	// applied (setFor). A request from another person is an admin's act
	// too: the step-up.
	stepUp := true
	if cs.Plan {
		admin, refusal := planChosen(cs, chosen)
		if refusal != "" {
			WriteError(w, http.StatusBadRequest, refusal, delegationDocs)
			return
		}
		stepUp = admin || cs.From != ""
	}
	// the step-up — except under --dev / --no-auth, where an agent is being
	// developed and (--no-auth) nobody signs in at all
	switch {
	case !stepUp:
	case s.DelegationDev:
	case a.p.User == nil || s.Auth.Users == nil:
		WriteError(w, http.StatusForbidden, "admin changes are approved by a person with an account", delegationDocs)
		return
	case !s.stepUp(w, r, a.p, body.Password,
		fmt.Sprintf("approving admin changes needs a recent sign-in — sign in again, then approve within %d minutes", int(auth.EnrollFreshLogin/time.Minute)),
		"confirm your password to approve admin changes"):
		return
	}
	// claim the set: only one approval applies it
	cs, err := s.Delegation.UpdateChangeSet(a.user, id, func(cs *delegation.ChangeSet) error {
		if !cs.Open() {
			return delegation.ErrSetNotOpen
		}
		cs.State, cs.Decided = delegation.SetApplying, time.Now().UTC().Truncate(time.Second)
		return nil
	})
	if err != nil {
		WriteError(w, http.StatusConflict, delegation.ErrSetNotOpen.Error(), delegationDocs)
		return
	}
	if refusal, msg := s.decideRefusal(whose, cs.Agent); refusal != "" {
		// stopped while the step-up ran: the set waits again, nothing applied
		// (a stop for good withdraws it the moment it is open again)
		_, _ = s.Delegation.UpdateChangeSet(a.user, id, func(c *delegation.ChangeSet) error {
			c.State, c.Decided = delegation.SetProposed, time.Time{}
			return nil
		})
		if refusal == "workspace-off" || refusal == "no-agent" {
			s.delegationStopped(whose, false) // the stop's own withdrawal missed it while it was claimed
		}
		refuseDecision(w, refusal, msg)
		return
	}
	s.applyChangeSet(r.Context(), a.p, &cs, chosen)
	cs, err = s.Delegation.UpdateChangeSet(a.user, id, func(stored *delegation.ChangeSet) error {
		*stored = cs
		return nil
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "the change set's results can't be written (what was applied stays applied: see the audit log)", delegationDocs)
		return
	}
	if cs.Waiting() { // on record now: watched from here, and looked at once (planwait.go)
		s.watchWaits(a.user)
		if now, ok := s.Delegation.ChangeSet(a.user, cs.ID); ok {
			cs = now
		}
	}
	s.askDecided(cs)
	s.delegateEvent(a.user, "changeset", cs.ID)
	WriteJSON(w, http.StatusOK, cs)
}

// applyChangeSet applies the chosen operations of cs (by number), in order
// as approver, filling each item's result and the set's state; an
// operation that wasn't chosen is rejected.
func (s *Server) applyChangeSet(ctx context.Context, approver auth.Principal, cs *delegation.ChangeSet, chosen []int) {
	// its own lines: the store's copy changes when the results are written, not line by line under its readers
	cs.Items = slices.Clone(cs.Items)
	failed, problems, applied := false, 0, 0
	agent := planAgent{}
	if cs.Plan {
		agent = s.agentOf(*cs, approver)
	}
	for i := range cs.Items {
		it := &cs.Items[i]
		switch {
		case it.Result == delegation.ItemAsked: // with the admins: their decision fills it in (askDecided)
			continue
		case it.Result == delegation.ItemApplied: // applied before (a plan's lines an admin was asked for arrive later)
			applied++
			continue
		case it.Blocked != "":
			it.Result = delegation.ItemBlocked
			if slices.Contains(chosen, it.N) {
				problems++
			}
			continue
		case it.NeedsAdmin: // not theirs to apply: it waits to be asked of an admin
			continue
		case !slices.Contains(chosen, it.N):
			it.Result = delegation.ItemRejected
			continue
		case failed:
			it.Result = delegation.ItemSkipped
			problems++
			continue
		}
		// what the person saw must still be what is there — and, for a plan,
		// what their agent may touch, and theirs to do without an admin if
		// that is what it was. Each line is judged with the person's rights
		// as they are NOW: an earlier line of this very set may have made the
		// tile this one is about (their level on it didn't exist when the
		// request came in), and an account disabled meanwhile applies nothing
		if cs.Plan {
			cur, ok := s.currentPerson(approver)
			if !ok {
				it.Result, it.Error = delegation.ItemFailed, "this account can no longer apply changes"
				failed = true
				problems++
				continue
			}
			approver, agent = cur, s.agentOf(*cs, cur)
		}
		var started []delegation.PlannedStart
		if it.Wait != nil && cs.From == "" { // it waits on a tile agent this plan started (no line of a request to the admins waits)
			started = startsOf(cs.Items, it.N, true)
		}
		pv, err := s.previewLine(approver, agent, pinStart(it.ChangeOp, it.Start), nil, started...)
		why := ""
		if err == nil && cs.Plan {
			why = cmpOr(s.lineAgentRefusal(agent, pv.Tiles), s.createScopeRefusal(agent, pv.Creates))
		}
		switch {
		case err != nil:
			it.Result, it.Error = delegation.ItemFailed, err.Error()
		case why != "": // before the line's own refusal: nothing is said of a tile the conversation may not see
			it.Result, it.Error = delegation.ItemFailed, why
		case pv.Blocked != "":
			it.Result, it.Error = delegation.ItemFailed, pv.Blocked
		case cs.Plan && cs.From == "" && !it.Admin && pv.Admin:
			it.Result, it.Error = delegation.ItemFailed, "this now takes an admin's rights — it was yours to do when it was proposed: nothing was applied; ask your agent to propose it again"
		case cs.Plan && lineStale(*it, pv), !cs.Plan && changeBasis(pv) != it.Basis:
			it.Result, it.Error = delegation.ItemStale, "it changed since the change set was proposed: nothing was applied — ask your agent to propose it again"
		case it.Wait != nil && (it.Admin || pv.Admin || cs.From != ""):
			// only a person's own binding waits (planwait.go): never an admin's line, never a line of a request
			it.Result, it.Error = delegation.ItemFailed, "a line that takes an admin's rights doesn't wait: nothing was applied — ask your agent to propose it again once the tile provides what it binds"
		case it.Wait != nil && pv.Waits != nil:
			// approved, and it waits: xbind applies it by itself once the
			// tile provides exactly what was approved (planwait.go)
			s.startWaiting(it, pv)
			s.delegateAudit(delegation.Call{User: cs.User, Agent: cs.Agent, Method: pv.Call.Method, Path: "/api/xbin" + pv.Call.Path, Tile: it.Wait.Component,
				Class: "changeset:" + it.Kind, Effect: true, Status: http.StatusAccepted, Ticket: cs.ID, Session: cs.Session, Under: "waits", From: cs.From})
			applied++
			continue
		default:
			status, msg, out := 0, "", json.RawMessage(nil)
			if it.Kind == delegation.KindAgentStart {
				status, msg, out = s.planStart(ctx, agent, pv, it.Start)
			} else {
				status, msg, out = s.adminCallOut(ctx, approver, pv.Call)
				if it.Kind != delegation.KindTileCreate {
					out = nil
				} else {
					var made struct {
						Path string `json:"path"`
					}
					_ = json.Unmarshal(out, &made)
					out, _ = json.Marshal(map[string]string{"path": made.Path})
				}
			}
			if status >= 300 {
				it.Result, it.Error = delegation.ItemFailed, msg
			} else {
				it.Result, it.Inverse, it.Out = delegation.ItemApplied, pv.Inverse, out
			}
		}
		row := delegation.Call{User: cs.User, Agent: cs.Agent, Method: pv.Call.Method, Path: "/api/xbin" + pv.Call.Path,
			Class: "changeset:" + it.Kind, Effect: true, Status: resultStatus(it.Result), Refused: it.Error, Ticket: cs.ID, Session: cs.Session}
		if cs.From != "" {
			// an admin applied what another person asked for: the admin's log
			// says whose request (and whose agent proposed it), and the
			// asker's log says who applied their line — under their own set
			row.From, row.Session = cs.From, ""
			s.delegateAudit(delegation.Call{User: cs.From, Agent: cs.Agent, Method: row.Method, Path: row.Path, Class: row.Class, Effect: true,
				Status: row.Status, Refused: row.Refused, Ticket: cs.AskOf, Session: cs.Session, By: cs.User})
		}
		s.delegateAudit(row)
		if it.Result != delegation.ItemApplied {
			failed = true
			problems++
		} else {
			applied++
		}
	}
	switch {
	case problems > 0:
		cs.State = delegation.SetPartial
	case applied == 0:
		cs.State = delegation.SetRejected // every operation was left out
	default:
		cs.State = delegation.SetApplied
	}
}

func resultStatus(result string) int {
	if result == delegation.ItemApplied {
		return http.StatusOK
	}
	return http.StatusConflict
}

// adminCall runs one admin API request as p, through handleAPI — the
// deployment and partition gates, the handler, its audit line — and
// answers its status and, for a refusal, its error text. p is the
// approving person's own principal, from their own request: never a
// delegate's.
func (s *Server) adminCall(ctx context.Context, p auth.Principal, c delegation.AdminCall) (int, string) {
	status, msg, _ := s.adminCallOut(ctx, p, c)
	return status, msg
}

// adminCallOut is adminCall with the call's answer (a tile.create line
// keeps where the tile was made).
func (s *Server) adminCallOut(ctx context.Context, p auth.Principal, c delegation.AdminCall) (int, string, json.RawMessage) {
	if p.Agent != "" || p.Component != "" || p.Impersonator != "" || p.UserID == "" && !s.devOwner(p) {
		return http.StatusForbidden, "admin changes are applied as the person who approved them", nil
	}
	req, err := http.NewRequestWithContext(auth.WithPrincipal(ctx, p), c.Method, "/api/xbin"+c.Path, bytes.NewReader(c.Body))
	if err != nil {
		return http.StatusBadRequest, err.Error(), nil
	}
	req.Header.Set("Content-Type", "application/json")
	rec := &memWriter{header: http.Header{}, status: http.StatusOK}
	s.handleAPI(rec, req)
	if rec.status >= 300 {
		return rec.status, errorOf(rec), nil
	}
	return rec.status, "", rec.body.Bytes()
}

// memWriter is a ResponseWriter that keeps a small answer in memory.
type memWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (m *memWriter) Header() http.Header { return m.header }
func (m *memWriter) WriteHeader(c int)   { m.status = c }
func (m *memWriter) Write(b []byte) (int, error) {
	if m.body.Len() < 64<<10 {
		m.body.Write(b)
	}
	return len(b), nil
}

// apiChangeSetUndo — POST /delegate/changesets/{id}/undo: the person. For
// a set that was applied (or partly), xbind proposes the set that undoes
// its applied operations, newest first — those that can be undone — as a
// new change set of the person's own, to approve like any other. → the new
// set. 409 when nothing of it can be undone.
func (s *Server) apiChangeSetUndo(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, false, false)
	if !ok {
		return
	}
	if s.ChangeSets == nil {
		WriteError(w, http.StatusNotImplemented, "this xbind applies no change sets", delegationDocs)
		return
	}
	cs, found := s.Delegation.ChangeSet(a.user, r.PathValue("id"))
	if !found {
		WriteError(w, http.StatusNotFound, "no such change set", delegationDocs)
		return
	}
	if unsettled(cs) && cs.From == "" { // undoing a plan ends what of it still waits — for xbind, or with the admins
		s.takeBack(a.user, cs.ID, "you undid the plan before it was applied")
		cs, _ = s.Delegation.ChangeSet(a.user, cs.ID)
	}
	var ops []delegation.ChangeOp
	var byHand []string // what was applied that xbind can't undo: said, not done
	for i := len(cs.Items) - 1; i >= 0; i-- {
		switch it := cs.Items[i]; {
		case it.Result != delegation.ItemApplied:
		case it.Inverse != nil:
			ops = append(ops, *it.Inverse)
		case it.UndoByHand != "":
			byHand = append(byHand, it.UndoByHand)
		}
	}
	if len(ops) == 0 {
		msg := "nothing of this change set can be undone by xbind: change it back by hand"
		if len(byHand) > 0 {
			msg = "nothing of this plan can be undone by xbind — " + strings.Join(byHand, "; ")
		}
		WriteJSON(w, http.StatusConflict, map[string]any{"error": msg, "byHand": byHand, "docs": delegationDocs})
		return
	}
	var items []delegation.ChangeItem
	var err error
	if cs.Plan { // a plan's lines may be personal: its undo is judged the same way
		items, err = s.describePlan(a.p, planAgent{}, ops, nil)
	} else {
		items, err = s.describeItems(a.p, ops, nil)
	}
	if err != nil {
		WriteError(w, http.StatusConflict, err.Error(), delegationDocs)
		return
	}
	title := "Undo: " + cs.Title
	undo, err := s.Delegation.NewChangeSet(delegation.ChangeSet{User: a.user, Title: title, UndoOf: cs.ID, Items: items, Plan: cs.Plan, ByHand: byHand})
	if err != nil {
		WriteError(w, http.StatusConflict, err.Error(), delegationDocs)
		return
	}
	s.delegateEvent(a.user, "changeset", undo.ID)
	WriteJSON(w, http.StatusOK, undo)
}
