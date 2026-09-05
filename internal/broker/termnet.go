package broker

import (
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/users"
)

// TermNetFor answers the terminal manager's question for one principal on
// one tile (D54): does the tile's owning org have network sets — and so an
// `org` scope — what relay rules that scope carries, and which of
// internet/host the principal may pick. Personal and workspace tiles keep
// the pre-D54 rules: termNet → internet, host admin-only.
func (b *Broker) TermNetFor(p auth.Principal, comp string) term.TermNet {
	admin := p.IsAdmin()
	out := term.TermNet{HostOK: admin, InternetOK: admin || p.CanTermNet()}
	if b.Users == nil {
		return out
	}
	ceil := b.Users.Ceiling(comp)
	org := ceil.OwnerOrg()
	if org == "" || !ceil.HasNetSets() || ceil.Denies(users.PolicyDenyNet) {
		return out
	}
	targets, host := netRuleTargets(ceil.NetRules())
	out.Rules = targets
	out.OrgHost = host
	out.OrgOK = host || len(targets) > 0
	out.HostOK = admin || host
	// On an org tile with sets the org network replaces plain internet for
	// members; admins keep every scope.
	out.InternetOK = admin
	out.OrgLabel = "org network (" + strings.Join(ceil.NetSets(), " + ") + ")"
	if host {
		out.OrgLabel += " — host networking"
	}
	lines := make([]string, 0, len(ceil.NetRules())+1)
	lines = append(lines, "org:"+org+"'s network sets:")
	for _, r := range ceil.NetRules() {
		lines = append(lines, netRuleText(r))
	}
	out.OrgDesc = strings.Join(lines, "\n")
	return out
}

// netRuleText renders one set rule for people.
func netRuleText(r string) string {
	switch {
	case r == "internet":
		return "🌐 all public internet"
	case r == "host":
		return "⚠ host networking (no relay, no filtering)"
	case strings.HasPrefix(r, "internet:"):
		return "→ " + strings.TrimPrefix(r, "internet:")
	case strings.HasPrefix(r, "lan:"):
		return "🖧 LAN " + strings.TrimPrefix(r, "lan:")
	case strings.HasPrefix(r, "provider:"):
		return "⇢ via " + strings.TrimPrefix(r, "provider:") + " (bind the provider explicitly)"
	}
	return r
}
