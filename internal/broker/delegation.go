package broker

// delegation.go — a delegate's calls to tiles (docs/auth.md §Delegation;
// auth/delegate.go; D228). A delegate is a person's principal (Component
// "", Via "delegate") whose Agent names the sealed system component acting
// for them: the person's workspace agent. Not to be confused with delegated
// approval (delegated.go, D26), an org admin's.
//
// Route's rule for one (routeDelegate) is the tile's frame's, minus the
// self-role: a person reaches a tile's API through its frame — as the
// tile, attributed to them (D29) — and by themselves only as an admin
// (rule 3). Their agent reaches /api/<tile>/… where they can read the
// tile, and the call is attributed like an F5 call (partitionglobal.go):
// X-XBin-User and -User-Level the person's, X-XBin-Role clamped to that
// level (reader, or writer for write and terminal) — never admin, an
// admin's agent included — and X-XBin-Agent the component. X-XBin-From is
// "user:<id>", what a person calling in person has always been.
//
// Checked on every call, in order (DelegateTile): the principal is a
// delegate of a sealed holder; the target isn't the holder itself; the
// person is live and can read the target (personLive); the tile's manifest
// lets agents in ("agent": off | read | full); the workspace policy ceiling
// lets the holder call the target (ceilingBlockMsg: a mayCall row on the
// holder's path narrows every person's agent at once); the target's org
// lets the workspace agent in, on the model it says it is on (AgentPolicy:
// the agent-policy package's answer, refused without one). The URL must be
// bare: a delegate names no deployment and reaches the primary. Then the
// partition rules as for the person (routePartition): their own partition
// of a partitioned target — with their consent for holder → target while
// the workspace asks people first (addressedPartition) — and the call is
// counted in the person's partition's egress ledger.
//
// The other direction is closed: no tile's call, grant or binding reaches
// a sealed holder (holderRefusal) — a grant on it would hand another
// tile's code each person's agent.

import (
	"fmt"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/registry"
)

// agentPolicy is the installed agent policy (Broker.AgentPolicy), or the
// one that refuses everything.
func (b *Broker) agentPolicy() delegation.Policy { return delegation.PolicyOr(b.AgentPolicy) }

// DelegateTile is routeDelegate's gates for a call by delegate p to tile,
// without a routing decision: the level p's person holds there, the role
// the call would carry and whether the tile lets agents read only — or why
// it is refused. write says the call isn't a GET or HEAD. The server's
// delegate gate asks it before a call (and for its dry run); Route asks it
// again when the call is routed.
func (b *Broker) DelegateTile(p auth.Principal, tile string, write bool) (level, role string, readOnly bool, err error) {
	switch {
	case p.Agent == "" || p.Via != "delegate" || p.Component != "" || p.UserID == "" || p.Impersonator != "" || !auth.HoldsDelegation(p.Agent):
		return "", "", false, fmt.Errorf("this credential acts for nobody on %s", tile)
	case tile == p.Agent:
		return "", "", false, fmt.Errorf("%s calls itself with its own credential, not as %s", p.Agent, p.UserID)
	case auth.HoldsDelegation(tile):
		return "", "", false, fmt.Errorf("%s is a sealed system component: nothing calls it but its own pages", tile)
	}
	if err := b.personLive(p.UserID, tile); err != nil {
		return "", "", false, err
	}
	if level = b.personLevel(p.UserID, tile); level == "" {
		return "", "", false, fmt.Errorf("%s can't read %s: their agent reaches only tiles they can read", p.UserID, tile)
	}
	if c, ok := b.Reg.Component(tile); ok {
		switch c.Manifest.AgentAccess() {
		case registry.AgentOff:
			return "", "", false, fmt.Errorf("%s doesn't let a person's agent use it (its manifest says \"agent\": \"off\")", tile)
		case registry.AgentRead:
			if write {
				return "", "", false, fmt.Errorf("%s lets a person's agent read only (its manifest says \"agent\": \"read\")", tile)
			}
			readOnly = true
		}
	}
	if msg := b.ceilingBlockMsg(p.Agent, tile); msg != "" {
		return "", "", false, fmt.Errorf("%s's agent can't call %s: %s", p.UserID, tile, msg)
	}
	if err := b.agentPolicy().TileAllows(delegation.Subject{Holder: p.Agent, User: p.UserID, Tile: tile, Model: p.AgentModel}); err != nil {
		return "", "", false, err
	}
	return level, attributedRole(level), readOnly, nil
}

// routeDelegate is Route's deployment rule for a delegate (the file
// comment).
func (b *Broker) routeDelegate(p auth.Principal, target *registry.Component, qualifier string) Decision {
	t := target.Path
	if qualifier != "" {
		return Decision{Deny: fmt.Errorf("%s's agent calls %s by its bare URL, /api/%s/, which reaches its primary: deployment URLs are for the people who work on the tile", p.UserID, t, t)}
	}
	level, role, readOnly, err := b.DelegateTile(p, t, false)
	if err != nil {
		return Decision{Deny: err}
	}
	return Decision{Deployment: b.primaryOf(t), Role: role,
		Attribute: &auth.Attribution{UserID: p.UserID, Level: level, Role: role, Agent: p.Agent, ReadOnly: readOnly}}
}

// countDelegate counts an allowed delegate call in the egress ledger of the
// person's partition of the holder (partitionledger.go): what left it for
// another tile, as for any partitioned tile's code.
func (b *Broker) countDelegate(p auth.Principal, target string, d Decision) {
	if d.Deny == nil && p.Agent != "" && p.UserID != "" {
		partitionEdgeSeam(b, p.UserID, p.Agent, ledgerTarget(target, d))
	}
}

// holderRefusal is why tile principal caller may not reach, hold a grant on
// or bind target: target is a sealed system component that holds
// delegation, and caller another tile. "" otherwise — the holder's own
// principals (its frames and backend), people and deliveries pass.
func holderRefusal(caller, target string) string {
	if caller == "" || caller == target || !auth.HoldsDelegation(target) {
		return ""
	}
	return fmt.Sprintf("%s is a sealed system component that acts for people: no tile calls it, holds a grant on it or binds to it", target)
}

// DelegateTile is DelegateTile for the server's delegate gate
// (server.DelegatePolicy).
func (p brokerPolicy) DelegateTile(pr auth.Principal, tile string, write bool) (level, role string, readOnly bool, err error) {
	return p.b.DelegateTile(pr, tile, write)
}
