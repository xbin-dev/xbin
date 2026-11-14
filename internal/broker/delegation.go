package broker

// delegation.go — a delegate's calls to tiles (plans/wsagent/
// s2-delegation.md; auth/delegate.go). A delegate is a person's principal
// (Component "", Via "delegate") whose Agent names the sealed system
// component acting for them: the person's workspace agent. Not to be
// confused with delegated approval (delegated.go, D26), an org admin's.
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
// Checked on every call, in order: the principal is a delegate of a sealed
// holder; the URL is bare (a delegate names no deployment and reaches the
// primary); the target isn't the holder itself; the person is live and can
// read the target (personLive); the workspace policy ceiling lets the
// holder call the target (ceilingBlockMsg: a mayCall row on the holder's
// path narrows every person's agent at once); the target's org lets the
// workspace agent in (delegateTileGate, the seam plans/workspace-agent.md
// §5.6 fills). Then the partition rules as for the person (routePartition):
// their own partition of a partitioned target — with their consent for
// holder → target while the workspace asks people first (addressedPartition).

import (
	"fmt"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
)

// delegateTileGate answers why the workspace agent holder acting for
// userID may not reach tile now, by the constraints of the org that owns
// TILE (§5.6: agent on or off, the model rule, the harness list), or nil.
// The default lets every call through: no org declares constraints yet.
var delegateTileGate = func(b *Broker, holder, userID, tile string) error { return nil }

// DelegateTile is routeDelegate's gates for a call by delegate p to tile,
// without a routing decision: the level p's person holds there and the
// role the call would carry, or why it is refused. The server's delegate
// gate asks it for its preview (what would this call reach).
func (b *Broker) DelegateTile(p auth.Principal, tile string) (level, role string, err error) {
	switch {
	case p.Agent == "" || p.Via != "delegate" || p.Component != "" || p.UserID == "" || p.Impersonator != "" || !auth.HoldsDelegation(p.Agent):
		return "", "", fmt.Errorf("this credential acts for nobody on %s", tile)
	case tile == p.Agent:
		return "", "", fmt.Errorf("%s calls itself with its own credential, not as %s", p.Agent, p.UserID)
	}
	if err := b.personLive(p.UserID, tile); err != nil {
		return "", "", err
	}
	if level = b.personLevel(p.UserID, tile); level == "" {
		return "", "", fmt.Errorf("%s can't read %s: their agent reaches only tiles they can read", p.UserID, tile)
	}
	if msg := b.ceilingBlockMsg(p.Agent, tile); msg != "" {
		return "", "", fmt.Errorf("%s's agent can't call %s: %s", p.UserID, tile, msg)
	}
	if err := delegateTileGate(b, p.Agent, p.UserID, tile); err != nil {
		return "", "", err
	}
	return level, attributedRole(level), nil
}

// routeDelegate is Route's deployment rule for a delegate (the file
// comment).
func (b *Broker) routeDelegate(p auth.Principal, target *registry.Component, qualifier string) Decision {
	t := target.Path
	if qualifier != "" {
		return Decision{Deny: fmt.Errorf("%s's agent calls %s by its bare URL, /api/%s/, which reaches its primary: deployment URLs are for the people who work on the tile", p.UserID, t, t)}
	}
	level, role, err := b.DelegateTile(p, t)
	if err != nil {
		return Decision{Deny: err}
	}
	return Decision{Deployment: b.primaryOf(t), Role: role,
		Attribute: &auth.Attribution{UserID: p.UserID, Level: level, Role: role, Agent: p.Agent}}
}

// DelegateTile is DelegateTile for the server's delegate gate
// (server.DelegatePolicy): what a dry run says a tile call would carry.
func (p brokerPolicy) DelegateTile(pr auth.Principal, tile string) (level, role string, err error) {
	return p.b.DelegateTile(pr, tile)
}
