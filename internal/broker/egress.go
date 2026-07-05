package broker

import (
	"strings"

	"github.com/magik6k/xbin/internal/registry"
	"github.com/magik6k/xbin/internal/sandbox"
)

// EgressFor returns the network egress policy a component is granted via its
// `net` interface binding (plans/interfaces.md). Egress is owner-authorized: a
// component can never self-grant it — an unbound `net` interface yields no rule,
// so the sandbox's netns stays default-deny. Only builtin egress providers
// resolve to a relay policy here: "internet" (public-only) and "lan:<cidr>". A
// "host" binding is HostNet (NetHostShare) and a provider *tile* is a splice —
// neither is a relay policy, so both add nothing here. Handed to the runner,
// which enables the TUN + userspace relay when the policy is non-empty.
func (b *Broker) EgressFor(c *registry.Component) sandbox.EgressPolicy {
	var targets []string
	switch nb := b.netBinding(c.Path); {
	case nb == "internet":
		targets = append(targets, "net:internet")
	case strings.HasPrefix(nb, "lan:"):
		targets = append(targets, "net:"+strings.TrimPrefix(nb, "lan:"))
	}
	pol, _ := sandbox.Parse(targets)
	return pol
}
