package broker

import (
	"slices"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/users"
)

// TermNetFor answers the terminal manager's question for one principal on
// one tile (D54/D88): does the tile's OWNER have network sets — and so an
// owner scope ("org" on an org tile, "personal" on a personal tile) — what
// relay rules that scope carries, and which of internet/host the principal
// may pick. On an org tile with sets the org network replaces plain
// internet; on a personal tile the owner's personal network is ADDED to it
// (D88: termNet internet stays, and a personal set holding full internet
// offers it too — narrowing the personal network is always safe). Tiles
// whose owner has no sets keep the pre-D54 rules: termNet → internet, host
// admin-only.
func (b *Broker) TermNetFor(p auth.Principal, comp string) term.TermNet {
	admin := p.IsAdmin()
	out := term.TermNet{HostOK: admin, InternetOK: admin || p.CanTermNet()}
	if b.Users == nil {
		return out
	}
	ceil := b.Users.Ceiling(comp)
	if ceil.Denies(users.PolicyDenyNet) {
		out.Sets = b.termNetSets(p, nil, "")
		return out
	}
	var owner, desc, note string
	var sets, rules []string
	switch uid, psets, prules := b.personalNet(comp); {
	case ceil.OwnerOrg() != "" && ceil.HasNetSets():
		owner, sets, rules = "org:"+ceil.OwnerOrg(), ceil.NetSets(), ceil.NetRules()
		out.OrgLabel, desc, note = "org network", owner+"'s network sets:", "attached to "+owner
		out.InternetOK = admin // the org network replaces plain internet for members
	case uid != "":
		owner, sets, rules = "user:"+uid, psets, prules
		out.OwnerScope = term.NetPersonal
		out.OrgLabel, desc, note = "personal network", owner+"'s personal network sets:", owner+"'s personal network"
		out.InternetOK = out.InternetOK || slices.Contains(rules, "internet") // a union, not a replacement
	default:
		out.Sets = b.termNetSets(p, nil, "") // a workspace admin's sets, on any tile
		return out
	}
	targets, host := netRuleTargets(rules)
	out.Rules = targets
	out.OrgHost = host
	out.OrgOK = host || len(targets) > 0
	out.HostOK = admin || host
	out.OrgLabel += " (" + strings.Join(sets, " + ") + ")"
	if host {
		out.OrgLabel += " — host networking"
	}
	lines := make([]string, 0, len(rules)+1)
	lines = append(lines, desc)
	for _, r := range rules {
		lines = append(lines, netRuleText(r))
	}
	out.OrgDesc = strings.Join(lines, "\n")
	out.Sets = b.termNetSets(p, sets, note)
	return out
}

// termNetSets lists the named sets this principal may pick on this tile
// (D65). A workspace admin: every workspace set, anywhere — strictly less
// than the host scope they already hold on every tile. Everyone else: the
// sets making up the tile OWNER's network (ownerSets — the owning org's, or
// the personal tile's owner's, D88), each a narrowing of the owner scope
// they can already pick. Provider-only sets reach nothing for a terminal
// and are skipped, as the owner scope skips them.
func (b *Broker) termNetSets(p auth.Principal, ownerSets []string, note string) []term.NetSetScope {
	var names []string
	switch {
	case p.IsAdmin():
		for n := range b.Users.NetSets() {
			names = append(names, n)
		}
		sort.Strings(names)
	case len(ownerSets) > 0:
		names = ownerSets
	default:
		return nil
	}
	attached := map[string]bool{}
	for _, n := range ownerSets {
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
			head = "network set " + n + " (" + note + "):"
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
