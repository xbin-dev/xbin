package broker

import (
	"strings"

	"github.com/magik6k/buxon/internal/registry"
	"github.com/magik6k/buxon/internal/sandbox"
)

// EgressFor returns the network egress policy a component is *granted*
// (plans/isolation.md §3): the net:* targets in its manifest uses that the
// owner has approved. An element can never self-grant egress — an unapproved
// net:* use yields no rule, so the sandbox's netns stays default-deny. Handed
// to the runner, which enables the TUN + userspace relay when non-empty.
func (b *Broker) EgressFor(c *registry.Component) sandbox.EgressPolicy {
	var targets []string
	for _, u := range c.Manifest.Uses {
		if !strings.HasPrefix(u.Target, "net:") {
			continue
		}
		if _, ok := b.grantedRole(c.Path, u.Target); ok {
			targets = append(targets, u.Target)
		}
	}
	pol, _ := sandbox.Parse(targets)
	return pol
}
