package broker

import (
	"fmt"
	"strings"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// EgressFor returns the network egress policy a component is granted via its
// `net` interface binding (plans/interfaces.md). Egress is owner-authorized: a
// component can never self-grant it — an unbound `net` interface yields no rule,
// so the sandbox's netns stays default-deny. Builtin egress providers resolve
// to a relay policy here: "internet" (public-only), the FILTERED
// "internet:<host|ip|cidr>[:port][,…]" form (D35 — each spec becomes one
// rule; hostname specs are enforced by the relay's DNS pinning), and
// "lan:<cidr>". A "host" binding is HostNet (NetHostShare) and a provider
// *tile* is a splice — neither is a relay policy, so both add nothing here.
// Handed to the runner, which enables the TUN + userspace relay when the
// policy is non-empty.
// validateFilteredInternet checks an "internet:<spec>[,…]" binding ref
// (D35): every spec must parse as a hostname, address or CIDR (optional
// :port) and stay in INTERNET class — private/loopback addresses are the
// lan:<cidr> binding's job, and a hostname resolving privately is dropped
// by the relay (pins are public-only), so the class holds end to end.
func validateFilteredInternet(ref string) error {
	rest := strings.TrimPrefix(ref, "internet:")
	if strings.TrimSpace(rest) == "" {
		return fmt.Errorf("internet: filter needs at least one host, address or CIDR")
	}
	for _, spec := range strings.Split(rest, ",") {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			return fmt.Errorf("empty spec in %q", ref)
		}
		r, err := sandbox.ParseRule("net:" + spec)
		if err != nil {
			return fmt.Errorf("bad internet filter spec %q: %w", spec, err)
		}
		if r.Internet {
			return fmt.Errorf("%q inside an internet: filter is redundant — bind plain `internet` for unfiltered egress", spec)
		}
		if r.Net.IsValid() {
			a := r.Net.Addr()
			if a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() {
				return fmt.Errorf("%q is not a public address — LAN reach is a lan:<cidr> binding", spec)
			}
		}
		if r.Host != "" && strings.Contains(r.Host, "*") {
			return fmt.Errorf("%q — bindings name concrete hosts (globs live in org allowances)", spec)
		}
	}
	return nil
}

func (b *Broker) EgressFor(c *registry.Component) sandbox.EgressPolicy {
	var targets []string
	switch nb := b.netBinding(c.Path); {
	case nb == "internet":
		targets = append(targets, "net:internet")
	case strings.HasPrefix(nb, "internet:"):
		for _, spec := range strings.Split(strings.TrimPrefix(nb, "internet:"), ",") {
			if spec = strings.TrimSpace(spec); spec != "" {
				targets = append(targets, "net:"+spec)
			}
		}
	case strings.HasPrefix(nb, "lan:"):
		targets = append(targets, "net:"+strings.TrimPrefix(nb, "lan:"))
	}
	pol, _ := sandbox.Parse(targets)
	return pol
}
