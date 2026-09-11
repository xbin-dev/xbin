package broker

import (
	"sort"
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
		out.Sets = b.termNetSets(p, ceil) // a workspace admin's sets, on any tile
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
	out.Sets = b.termNetSets(p, ceil)
	return out
}

// termNetSets lists the named sets this principal may pick on this tile
// (D65). A workspace admin: every workspace set, anywhere — strictly less
// than the host scope they already hold on every tile. Everyone else: the
// sets attached to the OWNING org, and only when they would get the org
// scope at all (an org tile with sets, no deny-net row) — each a narrowing
// of the union they can already pick. Provider-only sets reach nothing for
// a terminal and are skipped, as the org scope skips them.
func (b *Broker) termNetSets(p auth.Principal, ceil users.Ceiling) []term.NetSetScope {
	var names []string
	switch {
	case p.IsAdmin():
		for n := range b.Users.NetSets() {
			names = append(names, n)
		}
		sort.Strings(names)
	case ceil.OwnerOrg() != "" && ceil.HasNetSets() && !ceil.Denies(users.PolicyDenyNet):
		names = ceil.NetSets()
	default:
		return nil
	}
	attached := map[string]bool{}
	for _, n := range ceil.NetSets() {
		attached[n] = true
	}
	var out []term.NetSetScope
	for _, n := range names {
		ns, ok := b.Users.NetSet(n)
		if !ok {
			continue
		}
		targets, host := netRuleTargets(ns.Rules)
		if !host && len(targets) == 0 {
			continue
		}
		head := "network set " + n + ":"
		if attached[n] {
			head = "network set " + n + " (attached to org:" + ceil.OwnerOrg() + "):"
		}
		lines := []string{head}
		for _, r := range ns.Rules {
			lines = append(lines, netRuleText(r))
		}
		out = append(out, term.NetSetScope{Name: n, Rules: targets, Host: host, Label: "net set: " + n, Desc: strings.Join(lines, "\n")})
	}
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
