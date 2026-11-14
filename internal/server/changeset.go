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
	b, _ := json.Marshal([]any{pv.Before, pv.After, pv.Line, pv.Effects, pv.Warnings})
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
		it := delegation.ChangeItem{ChangeOp: op, Line: pv.Line, Before: pv.Before, After: pv.After, Effects: pv.Effects,
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
	case err != nil:
		WriteError(w, http.StatusBadRequest, err.Error(), delegationDocs)
		return
	}
	s.delegateEvent(a.user, "changeset", cs.ID)
	s.delegatePush(a.user, a.p.Agent, "agent.changeset", "Your agent proposes admin changes",
		fmt.Sprintf("%d change(s) wait for your approval. Nothing is applied until you approve it.", len(cs.Items)), "agent-changeset:"+cs.ID)
	WriteJSON(w, http.StatusOK, cs)
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
	WriteJSON(w, http.StatusOK, map[string]any{"changesets": sets})
}

// apiChangeSet — GET /delegate/changesets/{id}: one set.
func (s *Server) apiChangeSet(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, true, true)
	if !ok {
		return
	}
	cs, found := s.Delegation.ChangeSet(a.user, r.PathValue("id"))
	if !found {
		WriteError(w, http.StatusNotFound, "no such change set", delegationDocs)
		return
	}
	WriteJSON(w, http.StatusOK, cs)
}

// endChangeSet moves an open set to state (withdrawn, rejected).
func (s *Server) endChangeSet(w http.ResponseWriter, a delegateActor, id, state string) {
	cs, err := s.Delegation.UpdateChangeSet(a.user, id, func(cs *delegation.ChangeSet) error {
		if !cs.Open() {
			return delegation.ErrSetNotOpen
		}
		cs.State, cs.Decided = state, time.Now().UTC().Truncate(time.Second)
		return nil
	})
	switch {
	case errors.Is(err, delegation.ErrNoChangeSet):
		WriteError(w, http.StatusNotFound, err.Error(), delegationDocs)
		return
	case errors.Is(err, delegation.ErrSetNotOpen):
		WriteError(w, http.StatusConflict, err.Error(), delegationDocs)
		return
	case err != nil:
		WriteError(w, http.StatusInternalServerError, "the change set can't be written", delegationDocs)
		return
	}
	s.delegateEvent(a.user, "changeset", cs.ID)
	WriteJSON(w, http.StatusOK, cs)
}

// apiChangeSetWithdraw — DELETE /delegate/changesets/{id}: the agent takes
// its proposal back (or the person dismisses it).
func (s *Server) apiChangeSetWithdraw(w http.ResponseWriter, r *http.Request) {
	if a, ok := s.delegateCaller(w, r, true, false, true); ok {
		s.endChangeSet(w, a, r.PathValue("id"), delegation.SetWithdrawn)
	}
}

// apiChangeSetReject — POST /delegate/changesets/{id}/reject: the person.
func (s *Server) apiChangeSetReject(w http.ResponseWriter, r *http.Request) {
	if a, ok := s.delegateCaller(w, r, true, false, false); ok {
		s.endChangeSet(w, a, r.PathValue("id"), delegation.SetRejected)
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
		// Items: absent — every operation that isn't blocked; present — these
		// and no others, so an empty list applies nothing
		Items    *[]int `json:"items"`
		Password string `json:"password"`
	}
	if r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := DecodeJSON(r, &body); err != nil {
			WriteError(w, http.StatusBadRequest, "need {items?: [n, …], password?}", delegationDocs)
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
	var chosen []int
	if body.Items != nil {
		chosen = *body.Items
	}
	for _, n := range chosen {
		if n < 1 || n > len(cs.Items) {
			WriteError(w, http.StatusBadRequest, fmt.Sprintf("the set has no operation %d", n), delegationDocs)
			return
		}
	}
	// the step-up — except under --dev / --no-auth, where an agent is being
	// developed and (--no-auth) nobody signs in at all
	switch {
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
	s.applyChangeSet(r.Context(), a.p, &cs, chosen, body.Items == nil)
	cs, err = s.Delegation.UpdateChangeSet(a.user, id, func(stored *delegation.ChangeSet) error {
		*stored = cs
		return nil
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "the change set's results can't be written (what was applied stays applied: see the audit log)", delegationDocs)
		return
	}
	s.delegateEvent(a.user, "changeset", cs.ID)
	WriteJSON(w, http.StatusOK, cs)
}

// applyChangeSet applies the chosen operations of cs — every one that
// isn't blocked when all is set — in order as approver, filling each
// item's result and the set's state. An empty choice applies nothing.
func (s *Server) applyChangeSet(ctx context.Context, approver auth.Principal, cs *delegation.ChangeSet, chosen []int, all bool) {
	failed, problems, applied := false, 0, 0
	for i := range cs.Items {
		it := &cs.Items[i]
		switch {
		case it.Blocked != "":
			it.Result = delegation.ItemBlocked
			if all || slices.Contains(chosen, it.N) {
				problems++
			}
			continue
		case !all && !slices.Contains(chosen, it.N):
			it.Result = delegation.ItemRejected
			continue
		case failed:
			it.Result = delegation.ItemSkipped
			problems++
			continue
		}
		// what the person saw must still be what is there
		pv, err := s.ChangeSets.PreviewChange(approver, it.ChangeOp)
		switch {
		case err != nil:
			it.Result, it.Error = delegation.ItemFailed, err.Error()
		case pv.Blocked != "":
			it.Result, it.Error = delegation.ItemFailed, pv.Blocked
		case changeBasis(pv) != it.Basis:
			it.Result, it.Error = delegation.ItemStale, "it changed since the change set was proposed: nothing was applied — ask your agent to propose it again"
		default:
			if status, msg := s.adminCall(ctx, approver, pv.Call); status >= 300 {
				it.Result, it.Error = delegation.ItemFailed, msg
			} else {
				it.Result, it.Inverse = delegation.ItemApplied, pv.Inverse
			}
		}
		s.delegateAudit(delegation.Call{User: cs.User, Agent: cs.Agent, Method: pv.Call.Method, Path: "/api/xbin" + pv.Call.Path,
			Class: "changeset:" + it.Kind, Effect: true, Status: resultStatus(it.Result), Refused: it.Error, Ticket: cs.ID})
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
	if p.Agent != "" || p.Component != "" || p.Impersonator != "" || p.UserID == "" && !s.devOwner(p) {
		return http.StatusForbidden, "admin changes are applied as the person who approved them"
	}
	req, err := http.NewRequestWithContext(auth.WithPrincipal(ctx, p), c.Method, "/api/xbin"+c.Path, bytes.NewReader(c.Body))
	if err != nil {
		return http.StatusBadRequest, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	rec := &memWriter{header: http.Header{}, status: http.StatusOK}
	s.handleAPI(rec, req)
	if rec.status >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(rec.body.Bytes(), &e) != nil || e.Error == "" {
			e.Error = fmt.Sprintf("refused (%d)", rec.status)
		}
		return rec.status, e.Error
	}
	return rec.status, ""
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
	var ops []delegation.ChangeOp
	for i := len(cs.Items) - 1; i >= 0; i-- {
		if it := cs.Items[i]; it.Result == delegation.ItemApplied && it.Inverse != nil {
			ops = append(ops, *it.Inverse)
		}
	}
	if len(ops) == 0 {
		WriteError(w, http.StatusConflict, "nothing of this change set can be undone by xbind: change it back by hand", delegationDocs)
		return
	}
	items, err := s.describeItems(a.p, ops, nil)
	if err != nil {
		WriteError(w, http.StatusConflict, err.Error(), delegationDocs)
		return
	}
	title := "Undo: " + cs.Title
	undo, err := s.Delegation.NewChangeSet(delegation.ChangeSet{User: a.user, Title: title, UndoOf: cs.ID, Items: items})
	if err != nil {
		WriteError(w, http.StatusConflict, err.Error(), delegationDocs)
		return
	}
	s.delegateEvent(a.user, "changeset", undo.ID)
	WriteJSON(w, http.StatusOK, undo)
}
