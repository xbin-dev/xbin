package server

// delegatesweep.go — what delegation holds is judged again when people,
// access or tiles change, and what a stop ends (docs/protocol/delegation.md
// "What each switch stops"; D228).

import (
	"log/slog"

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
func (s *Server) delegationStopped(user string, keepApprovals bool) {
	s.CutDelegateStreams(user)
	if s.Delegation == nil {
		return
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
