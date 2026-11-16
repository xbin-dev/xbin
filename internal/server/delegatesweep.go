package server

// delegatesweep.go — what delegation holds is judged again when people,
// access or tiles change, and what a stop ends (docs/protocol/delegation.md
// "What each switch stops"; D228).

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/xbin-dev/xbin/internal/delegation"
)

// SweepDelegation re-judges what delegation holds, now: every delegate call
// in flight (it ends if its person was disabled or deleted, lost the tile,
// paused, or revoked the approval it runs under), and every standing
// approval (it goes when its tile is gone or its person can no longer read
// it — an approval never comes back with a tile recreated at the path, or
// with access given again). Boot calls it when people or tiles change; the
// calls in flight are also asked every delegateRecheck.
func (s *Server) SweepDelegation() {
	for _, c := range s.delegateRT.running("") {
		if c.still() != "" {
			c.cancel()
		}
	}
	if s.Delegation == nil || s.Auth == nil || s.Auth.Users == nil {
		return
	}
	held := s.agentHeldKind(s.agentHolder()) != ""
	for _, user := range s.Delegation.People() {
		if held { // nothing approved before the hold is used after it; what stands is kept for when it clears
			s.delegationStopped(user, true)
		}
		acc, ok := s.Auth.Users.Access(user)
		u, live := s.Auth.Users.Get(user)
		gone := func(a delegation.Approval) bool {
			return !ok || !live || u.Disabled || !s.tileExists(a.Tile) || !acc.CanReadTile(a.Tile)
		}
		if n, _ := s.Delegation.DropApprovals(user, gone); n > 0 {
			s.delegateEvent(user, "approval", "")
		}
		switch holder := s.agentHolder(); {
		case !ok || !live:
			// the account is gone: what was kept under its id is no later
			// account's to inherit (delegation/purge.go)
			s.DelegationPersonGone(user)
		case u.Disabled, holder != "" && !s.userHasAgent(user, holder):
			// disabled, or no longer given an agent: nothing approved before carries over
			s.delegationStopped(user, false)
		default:
			// the id was reused by another account (a deletion xbind didn't see)
			if purged, err := s.Delegation.Claim(user, u.Created); err != nil {
				slog.Warn("delegation: the account's stamp can't be kept", "user", user, "err", err)
			} else if purged {
				s.delegationStopped(user, false)
				s.delegateEvent(user, "request", "")
			}
		}
	}
}

// DelegationPersonGone ends everything delegation holds for a deleted
// account: what was running is cut, and every record kept under its id —
// requests and tickets, standing approvals, agent sessions, change sets,
// the pause and the opt-in, the audit log — is taken out of use, so an
// account made later under the same id starts with nothing (boot calls it
// when a person is deleted; SweepDelegation for one it finds gone).
func (s *Server) DelegationPersonGone(user string) {
	s.delegationStopped(user, false)
	if s.Delegation == nil {
		return
	}
	if err := s.Delegation.Purge(user); err != nil {
		slog.Warn("delegation: a deleted person's records can't be put away", "user", user, "err", err)
	}
}

// delegationStopped ends what was set in motion for user's agent before it
// was stopped — by a pause, the workspace's switch going off, or the person
// being disabled or deleted (docs/protocol/delegation.md "What each switch
// stops"): the calls in flight are cut, every approved-but-unused ticket is
// void (resuming doesn't bring it back), and the messages the agent had
// queued for tile agents it started are taken back. Standing approvals go
// too unless keepApprovals: a pause keeps them — the person resumes on
// purpose — a switch-off or the person's end does not.
//
// A stop for good (keepApprovals false: the switch off, the person no
// longer given an agent, disabled or deleted) also withdraws what waits for
// the person — pending requests and proposed change sets — so nothing is
// left for them to approve that would run the moment the agent is back.
// The agent's backend hears of each once, as of any request or set that
// changed. A pause or a hold keeps them pending: the person decides after
// resuming (and can't allow them before: decideRefusal).
func (s *Server) delegationStopped(user string, keepApprovals bool) {
	s.CutDelegateStreams(user)
	if s.Delegation == nil {
		return
	}
	if !keepApprovals {
		// before the tickets are voided: an approval racing this one then
		// lands on a withdrawn request (409), never on a ticket left behind
		ids, _ := s.Delegation.WithdrawPending(user)
		for _, id := range ids {
			s.delegateEvent(user, "request", id)
		}
		sets, _ := s.Delegation.WithdrawOpenSets(user)
		for _, id := range sets {
			s.delegateEvent(user, "changeset", id)
		}
		s.withdrawAsksFrom(user) // and what they asked the admins to apply for it (plan.go)
	}
	changed := false
	if n, _ := s.Delegation.VoidTickets(user); n > 0 {
		changed = true
	}
	if !keepApprovals {
		if n, _ := s.Delegation.DropApprovals(user, func(delegation.Approval) bool { return true }); n > 0 {
			changed = true
		}
	}
	if s.Term != nil {
		s.Term.DropAgentQueues(user, "")
	}
	if changed {
		s.delegateEvent(user, "request", "")
	}
}

// decideRefusal is why user may not ALLOW anything for their agent now —
// approve a request (once or always), give a standing approval, apply a
// change set: the workspace's switch is off, they paused it, they have no
// agent, it is held (or delegation can't run here at all). It answers the
// refusal the delegate gate gives for the same switch ("workspace-off",
// "paused", "no-agent", "held", "no-sandbox") and xbind's sentence, or ""
// when they may. Denying is never refused: it widens nothing. holder is
// the agent that asked ("": the one this xbind ships).
//
// Without this, a ticket or an "always" given while the agent was stopped
// was live the moment it was back: a stop voids only what was approved
// before it.
func (s *Server) decideRefusal(user, holder string) (refusal, msg string) {
	if s.Delegation == nil {
		return "", ""
	}
	const deny = " — denying it works now"
	switch {
	case !s.Delegation.Workspace().Enabled:
		return "workspace-off", "workspace agents are turned off for this workspace: nothing can be allowed until an admin turns them on again" + deny
	case s.Delegation.State(user).Paused:
		return "paused", "you paused your agent: resume it first, then allow this" + deny
	}
	if holder == "" {
		holder = s.agentHolder()
	}
	var off *OffError
	if err := s.DelegationOff(holder, user); errors.As(err, &off) {
		return off.Code, off.Msg + deny
	} else if err != nil {
		return "off", err.Error() + deny
	}
	return "", ""
}

// refuseDecision writes decideRefusal's 409: {error, refusal, docs}.
func refuseDecision(w http.ResponseWriter, refusal, msg string) {
	WriteJSON(w, http.StatusConflict, map[string]string{"error": msg, "refusal": refusal, "docs": delegationDocs})
}
