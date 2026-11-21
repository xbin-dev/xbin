package server

// plan.go — plans (docs/protocol/delegation.md §Plans; plans/wsagent/
// journey.md §6c): multi-step work as ONE change set. A person's agent
// proposes the whole of it — create these tiles, bind these slots, start
// this tile agent — and the person reviews it once, in xbin's own dialog.
//
// A plan is a change set (changeset.go) with two more kinds, tile.create
// and agent.start, and three rules of its own:
//
//   - LATER LINES MAY NAME EARLIER ONES' TILES. xbind describes each line
//     with the earlier tile.create lines taken as made (from the template's
//     manifest), applies the lines in order, and judges each for real — the
//     handler's own check — once the tiles it names exist. A line is applied
//     only together with the lines it needs (ChangeItem.Needs).
//   - PERSONAL OR AN ADMIN'S. A line the person could do with their own
//     hands and no admin is personal: a plan of personal lines needs no
//     opt-in and no fresh sign-in — it is the sheets they would get one by
//     one, on one page. A line that takes an admin's rights (ChangeItem.
//     Admin) is applied only for an admin who turned on "Let my agent
//     propose admin changes" (the review offers it), after a recent
//     sign-in. For a person who is no admin it is marked needsAdmin, shows
//     xbind's line and nothing of the state behind it, and can be sent to
//     the admins (…/ask): each admin gets a copy to review as a request
//     FROM that person, applied with the admin's own rights and step-up.
//   - BOUND TO ITS CONVERSATION. The set records the agent session that
//     proposed it; an agent.start line starts under that session — the
//     tile agent is the conversation's to follow — and is refused when the
//     conversation's taint forbids briefing a session there, exactly as a
//     single start is.
//
// Every line is held, when it is described and again when it is applied,
// to what the person's AGENT may touch: the tile's own "agent" level and
// its orgs' agent rules (the broker's AgentLineAllows). A personal line is
// applied only while it is still personal. Nothing here applies anything
// by itself: only the person's own approve does (apiChangeSetApprove).

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/util"
)

// PlanPreviewer describes a plan's line with the tiles earlier lines make
// taken as made (the broker's PreviewPlanned).
type PlanPreviewer interface {
	PreviewPlanned(p auth.Principal, op delegation.ChangeOp, planned []delegation.PlannedTile) (delegation.ChangePreview, error)
}

// AgentLineChecker answers whether a person's agent may propose a change
// to a tile at all (the broker's AgentLineAllows: a tile whose agent level
// is off, the ceiling, its orgs' rules).
type AgentLineChecker interface {
	AgentLineAllows(holder, user, model, tile string) error
}

func (s *Server) registerPlanAPI() {
	s.RegisterAPI("POST /delegate/plans", s.apiPlanNew)
	s.RegisterAPI("POST /delegate/changesets/{id}/ask", s.apiChangeSetAsk)
}

// lineBlockedForAgent is all an agent is told of why xbind can't apply a
// line: the reason is the person's to read, on xbin's review.
const lineBlockedForAgent = "xbin can't apply this line as proposed: your person sees why on xbin's review"

// linePowerForAgent is all an agent reads of the power rule's refusal
// (ChangeItem.Power; the broker's accessreach.go): no holder, no grant, no
// tile beyond the line's own — those are shown in xbin's own review, to a
// person who may see them.
const linePowerForAgent = "this change needs an admin to make it by hand: the tile, or code tied to it, holds workspace powers"

// lineHeldForAgent is all an agent is told of why a waiting line is not
// applied though its tile declares something: that it does. What the tile
// declared — its kind, service and role, words its coding agent wrote —
// is the person's to read, on xbin's card and in Needs you.
const lineHeldForAgent = "the tile declared something other than what was approved"

// optedIn reports whether user is an admin (of the workspace or of an org)
// who lets their agent read admin state and propose admin changes.
func (s *Server) optedIn(user string) bool {
	if s.Delegation == nil || !s.Delegation.State(user).AdminChanges {
		return false
	}
	if s.DelegationDev && s.Auth != nil && s.Auth.NoAuth() {
		return true // --no-auth: the implicit owner
	}
	if s.Auth == nil || s.Auth.Users == nil {
		return false
	}
	u, ok := s.Auth.Users.Get(user)
	if !ok || u.Disabled {
		return false
	}
	acc, _ := s.Auth.Users.Access(user)
	return u.IsAdmin() || acc != nil && len(acc.AdminOrgs()) > 0
}

// setFor is cs as caller a may read it. The person's own session — xbin's
// chrome — reads all of it (why a waiting line is held included: the full
// mismatch is theirs). Their agent, its conversation and its page's
// frame read, of a line that takes an admin's rights or that xbind can't
// apply, xbind's one sentence and nothing else: not what is there now
// (before, after, the inverse), not what it would open (effects,
// warnings), not why it is refused or failed — unless the person is an
// admin who let their agent read admin state (adminChanges). Otherwise a
// proposal would be a way to read grants and bindings: propose a line
// about any tile and read its state from the answer.
func (s *Server) setFor(a delegateActor, cs delegation.ChangeSet) delegation.ChangeSet {
	if a.own {
		return cs
	}
	cs.Items = slices.Clone(cs.Items)
	// A line that waits, whoever's it is — a personal line above all, the
	// only kind that does: what it waits for is the line's own arguments,
	// and of a held one the agent reads one fixed sentence. Never what the
	// tile declared (the tile's coding agent chose those words), nor
	// xbind's own bookkeeping.
	for i, it := range cs.Items {
		if it.Wait == nil {
			continue
		}
		w := *it.Wait
		w.Owners, w.Ids, w.Was = nil, nil, ""
		if w.Held != "" {
			w.Held = lineHeldForAgent
		}
		cs.Items[i].Wait = &w
	}
	// a person's access: its arguments and its outcome, whoever the person
	// is — never the tile's access list, the account or the level before
	// (D246; changeset_access.go)
	for i, it := range cs.Items {
		switch it.Kind {
		case delegation.KindAccessSet:
			cs.Items[i] = accessForAgent(it)
		case delegation.KindBuiltinUpdate: // applied, proposed or failed — nothing of the files (D248; changeset_builtin.go)
			cs.Items[i] = builtinForAgent(it)
		}
	}
	if s.optedIn(a.user) {
		return cs
	}
	for i, it := range cs.Items {
		if !it.Admin && !it.NeedsAdmin && it.Blocked == "" || it.Kind == delegation.KindBuiltinUpdate { // (a builtin's update: builtinForAgent said all of it, above)
			continue
		}
		red := delegation.ChangeItem{N: it.N, ChangeOp: it.ChangeOp, Why: it.Why, Line: it.Line, Admin: it.Admin, NeedsAdmin: it.NeedsAdmin,
			Needs: it.Needs, Deferred: it.Deferred, Result: it.Result, Out: it.Out, Of: it.Of, Later: it.Later, Wait: it.Wait}
		if it.Blocked != "" {
			red.Blocked = lineBlockedForAgent
		}
		if it.Power {
			red.Blocked, red.Power = linePowerForAgent, true
		}
		cs.Items[i] = red
	}
	return cs
}

// planAgent is who a plan's lines are judged for beside the person: the
// agent that proposed it, as a delegate principal of its conversation.
// zero (Agent ""): a set xbind made for the person (an undo), which no
// agent rule holds.
type planAgent struct {
	p    auth.Principal // the delegate: starts run as it
	gone string         // why there is none any more ("" there is)
}

// agentOf rebuilds the delegate that proposed cs, for person (whose
// principal the approving request carries; an admin applying another
// person's request gives the asker's id instead): the conversation's
// handle must still be there.
func (s *Server) agentOf(cs delegation.ChangeSet, person auth.Principal) planAgent {
	if cs.Agent == "" {
		return planAgent{}
	}
	user := cs.User
	if cs.From != "" {
		user = cs.From
	}
	d := auth.Principal{UserID: user, Via: "delegate", Agent: cs.Agent}
	if person.UserID == user {
		d.User, d.Access = person.User, person.Access
	} else if s.Auth != nil && s.Auth.Users != nil {
		// an admin applying another person's request: the lines are held to
		// what the ASKER's agent may see, so the asker's own access is read
		if u, ok := s.Auth.Users.Get(user); ok && !u.Disabled {
			d.User = u
			d.Access, _ = s.Auth.Users.Access(user)
		}
	}
	h, ok := s.Delegation.AgentSession(user, cs.Agent, cs.Session)
	if !ok {
		return planAgent{p: d, gone: "the conversation that proposed this is over: ask your agent to propose it again"}
	}
	d.AgentSession, d.AgentModel = h.ID, agentModel(h)
	return planAgent{p: d}
}

// currentPerson is p with its account and access read again (false: the
// account is gone or disabled). A principal that is no account's — the
// implicit owner under --no-auth — is as it was.
func (s *Server) currentPerson(p auth.Principal) (auth.Principal, bool) {
	if p.UserID == "" || p.User == nil || s.Auth == nil || s.Auth.Users == nil {
		return p, true
	}
	u, ok := s.Auth.Users.Get(p.UserID)
	if !ok || u.Disabled {
		return p, false
	}
	p.User = u
	p.Access, _ = s.Auth.Users.Access(p.UserID)
	return p, true
}

// lineAgentRefusal is why the agent may not propose a line that names
// tiles ("": it may). Each tile the line names — the one it changes, the
// providers it binds, a grant's two ends, a start's tile, a template that
// is a tile — is held to what a call by this conversation would be held
// to: the person can read it (one answer for a tile that isn't there and
// one they can't see), it is inside the conversation's scope, it doesn't
// turn agents away ("off"), and its orgs' rules let the agent in
// (AgentLineChecker). A plan is no way around the scope a person gave a
// conversation, nor to a tile hidden from it.
func (s *Server) lineAgentRefusal(a planAgent, tiles []string) string {
	if a.p.Agent == "" {
		return ""
	}
	lc, ok := s.ChangeSets.(AgentLineChecker)
	if !ok {
		return "this xbind applies no plans"
	}
	h, hasHandle := s.handleOf(a.p)
	for _, t := range tiles {
		t = strings.Trim(t, "/")
		switch {
		case !s.tileExists(t):
			continue // nothing there: the line's own check says so (in its own words)
		case !a.p.CanReadTile(t):
			return noTileFor(a.p.UserID)
		case hasHandle && !s.inSessionScope(h, t):
			return hiddenByScope(a.p.UserID)
		}
		if err := lc.AgentLineAllows(a.p.Agent, a.p.UserID, a.p.AgentModel, t); err != nil {
			return err.Error()
		}
	}
	return ""
}

// createScopeRefusal is why a conversation limited to some orgs or tiles
// may not create the tile a line would make ("": it may): the new tile
// would be outside what its person let it see.
func (s *Server) createScopeRefusal(a planAgent, made *delegation.PlannedTile) string {
	h, ok := s.handleOf(a.p)
	if made == nil || !ok || h.Scope == nil {
		return ""
	}
	orgs := s.orgsOf(made.Path)
	if org, isOrg := strings.CutPrefix(made.Owner, "org:"); isOrg {
		orgs = []string{org}
	}
	for _, o := range orgs {
		if slices.Contains(h.Scope.Orgs, o) {
			return ""
		}
	}
	return "this conversation is limited to part of the workspace, and the new tile would be outside it: " + hiddenByScope(a.p.UserID)
}

// previewLine describes one line of a plan as person would apply it now.
func (s *Server) previewLine(person auth.Principal, a planAgent, op delegation.ChangeOp, planned []delegation.PlannedTile, starts ...delegation.PlannedStart) (delegation.ChangePreview, error) {
	if op.Kind == delegation.KindAgentStart {
		if a.p.Agent == "" {
			return delegation.ChangePreview{}, errors.New("a tile agent is started for the conversation that asks for it")
		}
		return s.previewStart(a, op, planned)
	}
	if wp, ok := s.ChangeSets.(WaitPreviewer); ok && len(starts) > 0 {
		return wp.PreviewPlan(person, op, planned, starts) // a line after a start may wait for what that tile agent writes (planwait.go)
	}
	if pp, ok := s.ChangeSets.(PlanPreviewer); ok {
		return pp.PreviewPlanned(person, op, planned)
	}
	return s.ChangeSets.PreviewChange(person, op)
}

// nameOK: a name xbind writes into a line — letters, digits and the
// punctuation of paths and ids (the broker's lineNames).
func nameOK(v string) bool {
	if len(v) > 200 {
		return false
	}
	for _, c := range []byte(v) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("._~:@#/*+=,[]-", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// describePlan previews ops as person and builds a plan's items; a is the
// agent that proposes them (zero for a set xbind makes). The error is the
// first line that isn't one.
// started are tile agents already running that a line may wait on (an
// admin's copy of a request: the asker's applied starts).
func (s *Server) describePlan(person auth.Principal, a planAgent, ops []delegation.ChangeOp, whys []string, started ...delegation.PlannedStart) ([]delegation.ChangeItem, error) {
	plain := !mayOptInAdmin(person) && !s.devOwner(person) // no admin of anything: an admin's line is never theirs to apply
	var planned []delegation.PlannedTile
	items := make([]delegation.ChangeItem, 0, len(ops))
	for i, op := range ops {
		n := i + 1
		pv, err := s.previewLine(person, a, op, planned, append(slices.Clone(started), startsOf(items, n, false)...)...)
		if err != nil {
			return nil, fmt.Errorf("line %d (%s): %w", n, op.Kind, err)
		}
		it := delegation.ChangeItem{ChangeOp: op, Line: pv.Line, Before: pv.Before, After: pv.After, BeforeText: pv.BeforeText, AfterText: pv.AfterText,
			Effects: pv.Effects, Warnings: pv.Warnings, Blocked: pv.Blocked, Power: pv.Power, Inverse: pv.Inverse, Basis: changeBasis(pv),
			Admin: pv.Admin, Needs: pv.Uses, Deferred: pv.Deferred, Start: pv.Start, UndoByHand: pv.UndoByHand, Pin: pv.Pin}
		if pv.Waits != nil {
			it.Wait = &delegation.ItemWait{PlanWait: *pv.Waits}
		}
		it.Later = pv.Later
		if i < len(whys) {
			if it.Why = whys[i]; len(it.Why) > 500 {
				it.Why = it.Why[:500]
			}
		}
		// what the conversation may not see comes first: nothing is said of
		// such a tile, the line's own refusal included — and a person's own
		// access is never a line of their agent's, whoever describes it
		// argsLine is the line as its arguments say it — for a kind whose
		// line is the state behind it (a builtin's files, its verdict), all
		// a person who may not see that state reads
		argsLine := pv.Line
		if op.Kind == delegation.KindBuiltinUpdate {
			argsLine = delegation.BuiltinArgsLine(op.Args)
		}
		refused := false
		if why := cmpOr(s.lineAgentRefusal(a, pv.Tiles), s.createScopeRefusal(a, pv.Creates), s.accessSelfRefusal(person, a, pv, op)); why != "" {
			// blocked it stays, whoever reads it: nothing below turns it into
			// a line to ask an admin for (review of 2026-11-20, (a))
			it = delegation.ChangeItem{ChangeOp: op, Why: it.Why, Line: argsLine, Blocked: why, Admin: pv.Admin, Needs: pv.Uses, Deferred: pv.Deferred}
			refused = true
		}
		waits := 0 // a line it needs that waits for an admin
		for _, dep := range it.Needs {
			if items[dep-1].NeedsAdmin {
				waits = dep
			}
		}
		// a line only a workspace admin applies is no org admin's either: theirs to ask for (changeset_builtin.go)
		linePlain := plain || needsWorkspaceAdmin(op) && !person.IsAdmin() && !s.devOwner(person)
		switch {
		case refused:
		case plain && pv.Later != "":
			// it may not wait, and asking an admin now would ask for something
			// xbind can't apply: said as it is (the words name the line's own
			// tiles and nothing else), to be proposed again later
			it = delegation.ChangeItem{ChangeOp: op, Why: it.Why, Line: pv.Line, Admin: true, Blocked: pv.Later, Later: pv.Later, Needs: pv.Uses}
		case linePlain && (pv.Admin || waits != 0 && op.Kind != delegation.KindAgentStart):
			// not theirs to apply — and not theirs to read: the line, and
			// nothing of the state an admin would see behind it
			it = delegation.ChangeItem{ChangeOp: op, Why: it.Why, Line: argsLine, Admin: true, NeedsAdmin: true, Needs: pv.Uses}
		case waits != 0:
			it.Blocked = fmt.Sprintf("line %d needs an admin first: ask your agent to propose this start again once the tile exists", waits)
		}
		if pv.Creates != nil && it.Blocked == "" {
			c := *pv.Creates
			c.N = n
			planned = append(planned, c)
		}
		it.N = n // (the store numbers them the same: a later line reads the earlier starts by their numbers)
		items = append(items, it)
	}
	return items, nil
}

// apiPlanNew — POST /delegate/plans {title, items: [{kind, args, why?}]}:
// a person's agent proposes a plan (the file comment). Nothing is applied:
// → the change set (plan: true), each line with xbind's line, before,
// after, effects, warnings, blocked, and admin / needsAdmin / needs /
// deferred / start. 400 for a line of no known kind; 409 past 10 open sets.
func (s *Server) apiPlanNew(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, false, false, true)
	if !ok {
		return
	}
	if _, ok := s.ChangeSets.(PlanPreviewer); !ok {
		WriteError(w, http.StatusNotImplemented, "this xbind applies no plans", delegationDocs)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, delegation.MaxSetBytes)
	var in changeSetBody
	if err := DecodeJSON(r, &in); err != nil || len(in.Items) == 0 {
		WriteError(w, http.StatusBadRequest, "need {title, items: [{kind, args, why?}], dryRun?} with at least one line", delegationDocs)
		return
	}
	if len(in.Items) > delegation.MaxItems {
		WriteError(w, http.StatusBadRequest, delegation.ErrTooManyItems.Error(), delegationDocs)
		return
	}
	if a.p.AgentSession == "" {
		WriteError(w, http.StatusForbidden, "a plan is proposed by a conversation: send its agent session ("+HeaderAgentSession+")", delegationDocs)
		return
	}
	if in.DryRun { // nothing is kept, so no cap on open plans bounds it: the conversation's share does (allowDry)
		if ok, wait := s.delegateRT.allowDry(a.p.UserID, a.p.AgentSession); !ok {
			secs := int(wait/time.Second) + 1
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			WriteError(w, http.StatusTooManyRequests, fmt.Sprintf("this conversation is dry-running plans too fast (%d at once, one more every %d s): it may try again in %d s",
				planDryShareBurst, int(planDryShareRefill/time.Second), secs), delegationDocs)
			return
		}
	}
	ops, whys := make([]delegation.ChangeOp, len(in.Items)), make([]string, len(in.Items))
	for i, it := range in.Items {
		ops[i], whys[i] = delegation.ChangeOp{Kind: it.Kind, Args: it.Args}, it.Why
	}
	items, err := s.describePlan(personOf(a.p), planAgent{p: a.p}, ops, whys)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error(), delegationDocs)
		return
	}
	if in.DryRun {
		// what proposing it would answer, line for line (the same redaction: an agent reads of a line it can't
		// apply, or that is an admin's, xbind's sentence and no more) — so an agent that looks before it files
		// learns which lines would be dead on its person's review, and files a plan without them
		for i := range items {
			items[i].N = i + 1
		}
		dry := s.setFor(a, delegation.ChangeSet{User: a.user, Agent: a.p.Agent, Title: in.Title, Items: items, Plan: true, Session: a.p.AgentSession, State: delegation.SetProposed})
		WriteJSON(w, http.StatusOK, struct {
			delegation.ChangeSet
			DryRun bool `json:"dryRun"`
		}{dry, true})
		return
	}
	cs, err := s.Delegation.NewChangeSet(delegation.ChangeSet{User: a.user, Agent: a.p.Agent, Title: in.Title, Items: items, Plan: true, Session: a.p.AgentSession})
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

// planChosen judges what a person chose of a plan before anything runs:
// "" and whether an admin's line is among it, or the refusal (400).
func planChosen(cs delegation.ChangeSet, chosen []int) (admin bool, refusal string) {
	for _, n := range chosen {
		it := cs.Items[n-1]
		switch {
		case it.NeedsAdmin:
			return false, fmt.Sprintf("line %d needs an admin: ask one (…/ask), it is not yours to apply", n)
		case it.Result != "":
			return false, fmt.Sprintf("line %d is already decided (%s)", n, it.Result)
		}
		for _, dep := range it.Needs {
			if !slices.Contains(chosen, dep) && cs.Items[dep-1].Result != delegation.ItemApplied {
				return false, fmt.Sprintf("line %d needs line %d: apply them together", n, dep)
			}
		}
		admin = admin || it.Admin
	}
	return admin, ""
}

// lineStale reports whether what xbind says of a line now (pv) is no
// longer what its person was shown (it). A line described before the tiles
// it names existed is held to its sentence and to its warnings: a warning
// that was not shown is not one the person approved.
func lineStale(it delegation.ChangeItem, pv delegation.ChangePreview) bool {
	if it.Kind == delegation.KindAgentStart && it.Pin != "" {
		// a start is held to what it resolves to — the coding agent, mode, network, API access, model, an
		// xbind-written brief (Pin) — and to its warnings, not to its sentence: an upgrade may word a line
		// otherwise (D251 review L3: v0.3.84's lines didn't say ", which acts without asking")
		return pv.Pin != it.Pin || warnedAnew(it, pv)
	}
	if !it.Deferred {
		return changeBasis(pv) != it.Basis
	}
	if pv.Line != it.Line || pv.Pin != it.Pin {
		return true // another sentence — or, for a start, another coding agent, mode, network or API access than was shown
	}
	return warnedAnew(it, pv)
}

// warnedAnew: pv carries a warning the line didn't when its person saw it.
func warnedAnew(it delegation.ChangeItem, pv delegation.ChangePreview) bool {
	for _, w := range pv.Warnings {
		if !slices.Contains(it.Warnings, w) {
			return true
		}
	}
	return false
}

// earlyStale: a waiting line whose tile provides before Apply reaches it
// (D251) is still the line proposed only while the slot's binding is as it
// was then and it carries no warning it didn't (D251 review M1).
func earlyStale(it delegation.ChangeItem, pv delegation.ChangePreview) bool {
	return string(pv.Before) != string(it.Before) || warnedAnew(it, pv)
}

// pinStart is an agent.start line as its person was shown it: where the
// line named no coding agent or mode, the ones xbind resolved and showed
// (start) are what is asked for when it is applied — a default that moved
// since never starts something else. A mode is filled in only for the
// coding agent the line named (or none): where the person's choice for the
// tile outranked the agent's suggestion, the shown mode is another coding
// agent's, and the line is re-described as it was proposed (D250 review
// H1) — what opens is the shown start itself (planStart), and any change
// since makes the line stale.
func pinStart(op delegation.ChangeOp, start json.RawMessage) delegation.ChangeOp {
	var shown struct{ Provider, Mode string }
	var args map[string]any
	if op.Kind != delegation.KindAgentStart || json.Unmarshal(start, &shown) != nil || json.Unmarshal(op.Args, &args) != nil {
		return op
	}
	named, _ := args["harness"].(string)
	if named == "" && shown.Provider != "" {
		args["harness"], named = shown.Provider, shown.Provider
	}
	if m, _ := args["mode"].(string); m == "" && shown.Mode != "" && named == shown.Provider {
		args["mode"] = shown.Mode
	}
	op.Args, _ = json.Marshal(args)
	return op
}

func cmpOr(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// errorOf is a refused internal call's error text.
func errorOf(rec *memWriter) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(rec.body.Bytes(), &e) != nil || e.Error == "" {
		return fmt.Sprintf("refused (%d)", rec.status)
	}
	return e.Error
}

// ---- Ask an admin ----

// admins are the workspace's enabled admins, as principals of their own.
func (s *Server) admins() []auth.Principal {
	if s.Auth == nil || s.Auth.Users == nil {
		return nil
	}
	var out []auth.Principal
	for _, u := range s.Auth.Users.List() {
		if u.IsAdmin() && !u.Disabled {
			u := u
			out = append(out, auth.Principal{UserID: u.ID, User: &u, Via: "session"})
		}
	}
	return out
}

// apiChangeSetAsk — POST /delegate/changesets/{id}/ask [{items: [n, …]}]:
// the person, in their own session — never their agent or its page. The
// lines of their plan that need an admin (all of them, or the ones named)
// go to every workspace admin as a change set FROM that person, described
// again as that admin would apply it; the first admin to decide ends the
// others' copies, and the lines' results come back to the person's set.
// A line is asked once the lines it needs are applied or asked with it.
// → the person's set, the asked lines marked "asked". 409 while their
// agent is stopped, when there is no admin, or past 3 requests waiting.
func (s *Server) apiChangeSetAsk(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, false, false)
	if !ok {
		return
	}
	var body struct {
		Items []int `json:"items"`
		// Retract: take the request back — the admins' copies end, the
		// asked lines read "left out". Narrowing: never refused.
		Retract bool `json:"retract"`
	}
	if r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := DecodeJSON(r, &body); err != nil {
			WriteError(w, http.StatusBadRequest, "need {items?: [n, …]} or {retract: true}", delegationDocs)
			return
		}
	}
	id := r.PathValue("id")
	cs, found := s.Delegation.ChangeSet(a.user, id)
	switch {
	case !found:
		WriteError(w, http.StatusNotFound, "no such change set", delegationDocs)
		return
	case !cs.Plan || cs.From != "" || cs.State != delegation.SetProposed && cs.State != delegation.SetApplied && cs.State != delegation.SetPartial:
		WriteError(w, http.StatusConflict, "nothing of this change set can be asked of an admin", delegationDocs)
		return
	}
	if body.Retract {
		if s.retractAsks(a.user, id, "you took the request back") == 0 {
			WriteError(w, http.StatusConflict, "no line of this plan is with the admins", delegationDocs)
			return
		}
		cs, _ = s.Delegation.ChangeSet(a.user, id)
		s.delegateAudit(delegation.Call{User: a.user, Agent: cs.Agent, Method: http.MethodPost, Path: "/api/xbin/delegate/changesets/" + id + "/ask",
			Class: "decision:retract", Effect: true, Status: http.StatusOK, Ticket: cs.ID})
		WriteJSON(w, http.StatusOK, cs)
		return
	}
	if refusal, msg := s.decideRefusal(a.user, cs.Agent); refusal != "" {
		refuseDecision(w, refusal, msg) // asking widens what may happen: as allowing does
		return
	}
	chosen := body.Items
	if len(chosen) == 0 {
		for _, it := range cs.Items {
			if it.NeedsAdmin && it.Result == "" {
				chosen = append(chosen, it.N)
			}
		}
	}
	slices.Sort(chosen)
	chosen = slices.Compact(chosen)
	if len(chosen) == 0 {
		WriteError(w, http.StatusConflict, "no line of this plan waits for an admin", delegationDocs)
		return
	}
	var ops []delegation.ChangeOp
	var whys []string
	for _, n := range chosen {
		if n < 1 || n > len(cs.Items) {
			WriteError(w, http.StatusBadRequest, fmt.Sprintf("the plan has no line %d", n), delegationDocs)
			return
		}
		it := cs.Items[n-1]
		if !it.NeedsAdmin || it.Result != "" {
			WriteError(w, http.StatusBadRequest, fmt.Sprintf("line %d is not one that waits for an admin", n), delegationDocs)
			return
		}
		for _, dep := range it.Needs {
			if cs.Items[dep-1].Result != delegation.ItemApplied {
				WriteError(w, http.StatusConflict, fmt.Sprintf("line %d needs line %d, which isn't applied yet: apply it first, then ask", n, dep), delegationDocs)
				return
			}
		}
		ops, whys = append(ops, it.ChangeOp), append(whys, it.Why)
	}
	admins := s.admins()
	if len(admins) == 0 {
		WriteError(w, http.StatusConflict, "this workspace has no admin account to ask", delegationDocs)
		return
	}
	waiting := map[string]bool{}
	for _, adm := range admins {
		sets, _ := s.Delegation.ChangeSets(adm.UserID)
		for _, o := range sets {
			if o.Open() && o.From == a.user {
				waiting[o.AskID] = true
			}
		}
	}
	if len(waiting) >= delegation.MaxAsked {
		WriteError(w, http.StatusConflict, fmt.Sprintf("%d of your requests already wait for an admin", len(waiting)), delegationDocs)
		return
	}
	agent := s.agentOf(cs, a.p)
	ask, sent := util.RandomToken(12), 0
	for _, adm := range admins {
		// as this admin would apply them, now — and still held to what the asker's agent may touch
		// (no line of a request waits: an admin's approval, given with a recent sign-in, applies now or not at all)
		items, err := s.describePlan(adm, agent, ops, whys)
		if err != nil {
			continue
		}
		for i := range items {
			items[i].Of = chosen[i]
		}
		copy, err := s.Delegation.NewChangeSet(delegation.ChangeSet{User: adm.UserID, Agent: cs.Agent, Title: cs.Title, Items: items,
			Plan: true, Session: cs.Session, From: a.user, AskOf: cs.ID, AskID: ask})
		if err != nil {
			continue // that admin's list is full: the others still get it
		}
		sent++
		s.delegateEvent(adm.UserID, "changeset", copy.ID)
	}
	if sent == 0 {
		WriteError(w, http.StatusConflict, "no admin could be asked now (their lists are full): try again later", delegationDocs)
		return
	}
	cs, err := s.Delegation.UpdateChangeSet(a.user, id, func(c *delegation.ChangeSet) error {
		for _, n := range chosen {
			c.Items[n-1].Result = delegation.ItemAsked
		}
		return nil
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "the change set can't be written", delegationDocs)
		return
	}
	s.delegateAudit(delegation.Call{User: a.user, Agent: cs.Agent, Method: http.MethodPost, Path: "/api/xbin/delegate/changesets/" + id + "/ask",
		Class: "decision:ask", Effect: true, Status: http.StatusOK, Ticket: cs.ID})
	s.delegateEvent(a.user, "changeset", cs.ID)
	WriteJSON(w, http.StatusOK, cs)
}

// askDecided carries an admin's decision on a request (done: their copy,
// decided) to everyone else it concerns: the other admins' copies end
// (decidedBy), and the asker's lines get their results.
func (s *Server) askDecided(done delegation.ChangeSet) {
	if done.From == "" || done.AskID == "" {
		return
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, adm := range s.admins() {
		if adm.UserID == done.User {
			continue
		}
		sets, _ := s.Delegation.ChangeSets(adm.UserID)
		for _, o := range sets {
			if o.AskID != done.AskID || !o.Open() {
				continue
			}
			if _, err := s.Delegation.UpdateChangeSet(adm.UserID, o.ID, func(c *delegation.ChangeSet) error {
				c.State, c.Decided, c.DecidedBy = delegation.SetWithdrawn, now, done.User
				return nil
			}); err == nil {
				s.delegateEvent(adm.UserID, "changeset", o.ID)
			}
		}
	}
	_, err := s.Delegation.UpdateChangeSet(done.From, done.AskOf, func(c *delegation.ChangeSet) error {
		for _, it := range done.Items {
			if it.Of < 1 || it.Of > len(c.Items) || c.Items[it.Of-1].Result != delegation.ItemAsked && c.Items[it.Of-1].Result != delegation.ItemWaiting {
				continue
			}
			mine := &c.Items[it.Of-1]
			mine.Result, mine.Error = it.Result, it.Error
			if (it.Kind == delegation.KindAccessSet || it.Power) && (it.Result == delegation.ItemBlocked || it.Result == delegation.ItemFailed) {
				// the admin's review resolved the name — no such account, or
				// several — weighed the account's level against the power
				// rule, or the store refused it: the asker learns no more of
				// the workspace's accounts, levels or powers than of a line
				// left out
				mine.Result, mine.Error = delegation.ItemRejected, ""
			}
			if it.Result == delegation.ItemWaiting { // an admin approved it, and it waits: where it stands is theirs to read
				mine.Error = "an admin (" + done.User + ") approved it: xbin applies it by itself once " + it.Wait.Tile + " provides " + it.Wait.Service
			}
			if mine.Result == delegation.ItemRejected || mine.Result == "" {
				mine.Result, mine.Error = delegation.ItemRejected, "an admin ("+done.User+") left it out"
			}
		}
		return nil
	})
	if err == nil {
		s.delegateEvent(done.From, "changeset", done.AskOf)
	}
}

// retractAsks ends what user asked the admins for from their set id — the
// admins' open copies are withdrawn (decidedBy: the asker), the set's
// asked lines read "left out" with why — and answers how many lines that
// were. The asker's own act: taking the request back, or ending the set.
func (s *Server) retractAsks(user, id, why string) int {
	now := time.Now().UTC().Truncate(time.Second)
	for _, adm := range s.admins() {
		sets, _ := s.Delegation.ChangeSets(adm.UserID)
		for _, o := range sets {
			if o.From != user || o.AskOf != id {
				continue
			}
			if !o.Open() { // decided by an admin: what of it still waits for xbind ends too
				if o.Waiting() {
					s.cancelWaits(adm.UserID, o.ID, user+" took the request back before it was applied")
				}
				continue
			}
			if _, err := s.Delegation.UpdateChangeSet(adm.UserID, o.ID, func(c *delegation.ChangeSet) error {
				c.State, c.Decided, c.DecidedBy = delegation.SetWithdrawn, now, user
				return nil
			}); err == nil {
				s.delegateEvent(adm.UserID, "changeset", o.ID)
			}
		}
	}
	n := 0
	_, _ = s.Delegation.UpdateChangeSet(user, id, func(c *delegation.ChangeSet) error {
		for i := range c.Items {
			// (a line an admin approved that waits there reads "waiting" here, with no Wait of its own: the admin's copy holds it)
			if c.Items[i].Result == delegation.ItemAsked || c.Items[i].Result == delegation.ItemWaiting && c.Items[i].Wait == nil {
				c.Items[i].Result, c.Items[i].Error = delegation.ItemRejected, why
				n++
			}
		}
		return nil
	})
	if n > 0 {
		s.delegateEvent(user, "changeset", id)
	}
	return n
}

// unsettled: a decided set still has a line waiting — for xbind to apply
// it, or with the admins.
func unsettled(cs delegation.ChangeSet) bool {
	return cs.Waiting() || slices.ContainsFunc(cs.Items, func(it delegation.ChangeItem) bool { return it.Result == delegation.ItemAsked })
}

// takeBack ends everything of user's set id that is not settled: its own
// lines that wait for xbind, what it asked the admins for — their open
// copies, and the lines of copies an admin approved that still wait.
func (s *Server) takeBack(user, id, why string) {
	s.cancelWaits(user, id, why)
	s.retractAsks(user, id, why)
}

// askWithdrawn is why an admin's copy of a request (cs, From someone) asks
// for nothing any more ("": it still does): the asker's set is gone,
// expired, rejected or withdrawn, or none of the copy's lines is still
// asked there.
func (s *Server) askWithdrawn(cs delegation.ChangeSet) string {
	if cs.From == "" || cs.AskOf == "" {
		return ""
	}
	asker, ok := s.Delegation.ChangeSet(cs.From, cs.AskOf)
	const gone = "this request was taken back: the plan it came from is no longer waiting for an admin"
	if !ok || asker.State == delegation.SetRejected || asker.State == delegation.SetWithdrawn || asker.State == delegation.SetExpired {
		return gone
	}
	for _, it := range cs.Items {
		if it.Of >= 1 && it.Of <= len(asker.Items) && asker.Items[it.Of-1].Result == delegation.ItemAsked {
			return ""
		}
	}
	return gone
}

// withdrawAsksFrom ends the requests user has waiting with the admins:
// their agent stopped for good, and nothing it proposed stays to be
// applied (delegationStopped).
func (s *Server) withdrawAsksFrom(user string) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, adm := range s.admins() {
		sets, _ := s.Delegation.ChangeSets(adm.UserID)
		for _, o := range sets {
			if o.From != user || !o.Open() {
				continue
			}
			if _, err := s.Delegation.UpdateChangeSet(adm.UserID, o.ID, func(c *delegation.ChangeSet) error {
				c.State, c.Decided, c.WithdrawnBy = delegation.SetWithdrawn, now, delegation.WithdrawnByXbin
				return nil
			}); err == nil {
				s.delegateEvent(adm.UserID, "changeset", o.ID)
			}
		}
	}
}
