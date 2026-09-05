// netsets.go — organisation network sets (plans/DECISIONS.md D54).
//
// A NetSet is a ws-admin-managed, named list of reach rules; an org attaches
// sets by reference and its network is their union. The union is consumed in
// three places, all through Ceiling (orgs.go): the broker refuses bindings on
// org-owned tiles that it doesn't cover, org admins may bind anything inside
// it (ResolvedAllow folds the rules in as allowance entries), and it is what
// the `org` binding — the default for org-owned tiles that declare a net
// interface — and org terminals resolve to. Personal and workspace tiles are
// untouched by sets.
package users

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// ValidateNetRules checks a set's rules: the net: allowance grammar without
// the prefix, plus the concrete-value checks the relay needs (a CIDR must
// parse, ports are 1-65535, one destination per rule, hostname globs have
// one '*' and a dotted remainder, LAN rules are addresses only).
func ValidateNetRules(rules []string) error {
	seen := map[string]bool{}
	for i, r := range rules {
		nr, err := parseNetRule(r)
		if err != nil {
			return fmt.Errorf("rules[%d]: %w", i, err)
		}
		if seen[nr] {
			return fmt.Errorf("rules[%d]: %q listed twice", i, nr)
		}
		seen[nr] = true
	}
	return nil
}

// NormalizeNetRules trims/lowercases rules for storage (validated first).
func NormalizeNetRules(rules []string) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		if nr, err := parseNetRule(r); err == nil {
			out = append(out, nr)
		}
	}
	return out
}

// parseNetRule validates one rule and returns its normalized spelling.
func parseNetRule(rule string) (string, error) {
	r := strings.ToLower(strings.TrimSpace(rule))
	if r == "" {
		return "", fmt.Errorf("empty rule")
	}
	if _, err := parseAllowEntry("net:" + r); err != nil {
		return "", fmt.Errorf("%q — rules are internet, internet:<host|host-glob|ip|cidr>[:port], lan:<ip|cidr>[:port], host, or provider:<tile-glob>", rule)
	}
	switch {
	case r == "internet" || r == "host":
		return r, nil
	case strings.HasPrefix(r, "provider:"):
		if strings.ContainsAny(r, " \t") {
			return "", fmt.Errorf("%q — provider rules name a tile path or glob", rule)
		}
		return r, nil
	case strings.HasPrefix(r, "internet:"):
		spec := strings.TrimPrefix(r, "internet:")
		if strings.Contains(spec, ",") {
			return "", fmt.Errorf("%q — one destination per rule", rule)
		}
		host, _ := cutNetPort(spec)
		if strings.Contains(host, "*") {
			if strings.Count(host, "*") > 1 {
				return "", fmt.Errorf("%q — hostname globs take a single *", rule)
			}
			if bare := strings.Trim(strings.ReplaceAll(host, "*", ""), "."); bare == "" || !strings.Contains(bare, ".") {
				return "", fmt.Errorf("%q — a hostname glob needs a dotted remainder (*.example.com)", rule)
			}
			if _, isAddr := parsePrefixish(strings.ReplaceAll(host, "*", "1")); isAddr {
				return "", fmt.Errorf("%q — addresses can't glob; use a CIDR", rule)
			}
			if _, err := sandbox.ParseRule("net:" + strings.ReplaceAll(spec, "*", "x")); err != nil {
				return "", fmt.Errorf("%q — %v", rule, err)
			}
			return r, nil
		}
		pr, err := sandbox.ParseRule("net:" + spec)
		if err != nil {
			return "", fmt.Errorf("%q — %v", rule, err)
		}
		if pr.Internet {
			return "", fmt.Errorf("%q — use plain internet for unfiltered public egress", rule)
		}
		if pr.Net.IsValid() && !publicPrefix(pr.Net) {
			return "", fmt.Errorf("%q — private, loopback or link-local reach is a lan: rule", rule)
		}
		return r, nil
	case strings.HasPrefix(r, "lan:"):
		spec := strings.TrimPrefix(r, "lan:")
		if strings.Contains(spec, "*") {
			return "", fmt.Errorf("%q — lan rules are concrete addresses or CIDRs (no globs)", rule)
		}
		pr, err := sandbox.ParseRule("net:" + spec)
		if err != nil {
			return "", fmt.Errorf("%q — %v", rule, err)
		}
		if !pr.Net.IsValid() {
			return "", fmt.Errorf("%q — lan rules are addresses or CIDRs, not hostnames", rule)
		}
		return r, nil
	}
	return "", fmt.Errorf("%q — unknown rule", rule)
}

// publicPrefix mirrors the binding plane's D35 test: an internet: address must
// be routable public space (LAN reach is spelled lan:).
func publicPrefix(p netip.Prefix) bool {
	a := p.Addr()
	return !(a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified())
}

// --- store ------------------------------------------------------------------

func (s *Store) NetSets() map[string]NetSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]NetSet, len(s.netSets))
	for n, ns := range s.netSets {
		out[n] = NetSet{Rules: append([]string(nil), ns.Rules...), Created: ns.Created}
	}
	return out
}

func (s *Store) NetSet(name string) (NetSet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ns := s.netSets[normalizeID(name)]
	if ns == nil {
		return NetSet{}, false
	}
	return NetSet{Rules: append([]string(nil), ns.Rules...), Created: ns.Created}, true
}

// UpsertNetSet creates or replaces a set (ws-admin only at the API). Rules
// validate and normalize; Created survives a replace.
func (s *Store) UpsertNetSet(name string, ns NetSet) error {
	name = normalizeID(name)
	if err := validID(name); err != nil {
		return fmt.Errorf("network set name: %w", err)
	}
	if err := ValidateNetRules(ns.Rules); err != nil {
		return err
	}
	rules := NormalizeNetRules(ns.Rules)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.netSets == nil {
		s.netSets = map[string]*NetSet{}
	}
	created := time.Now().Unix()
	if old := s.netSets[name]; old != nil {
		created = old.Created
	}
	s.netSets[name] = &NetSet{Rules: rules, Created: created}
	return s.persistLocked()
}

// DeleteNetSet removes a set — refused while any org references it (detach
// first; explicit beats silently changing an org's reach).
func (s *Store) DeleteNetSet(name string) error {
	name = normalizeID(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.netSets[name]; !ok {
		return fmt.Errorf("no such network set %q", name)
	}
	for _, o := range s.orgs {
		if contains(o.NetSets, name) {
			return fmt.Errorf("network set %q is attached to org %q — detach it first", name, o.ID)
		}
	}
	delete(s.netSets, name)
	return s.persistLocked()
}

// SetOrgNetSets attaches network sets to an org by name (ws-admin, D54).
func (s *Store) SetOrgNetSets(orgID string, names []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	org := s.orgs[normalizeID(orgID)]
	if org == nil {
		return fmt.Errorf("no such org %q", orgID)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = normalizeID(n)
		if n == "" || seen[n] {
			continue
		}
		if _, ok := s.netSets[n]; !ok {
			return fmt.Errorf("no such network set %q", n)
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	no := *org
	no.NetSets = out
	s.orgs[no.ID] = &no
	return s.persistLocked()
}

// OrgNetSets lists the sets attached to an org.
func (s *Store) OrgNetSets(orgID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if org := s.orgs[normalizeID(orgID)]; org != nil {
		return append([]string(nil), org.NetSets...)
	}
	return nil
}

// OrgNetRules is an org's reach: the union of its attached sets' rules
// (sorted, deduped) and whether it includes host networking.
func (s *Store) OrgNetRules(orgID string) (rules []string, host bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	org := s.orgs[normalizeID(orgID)]
	if org == nil {
		return nil, false
	}
	rules = s.orgNetRulesLocked(org)
	return rules, contains(rules, "host")
}

func (s *Store) orgNetRulesLocked(org *Org) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range org.NetSets {
		ns := s.netSets[n]
		if ns == nil {
			continue
		}
		for _, r := range ns.Rules {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	sort.Strings(out)
	return out
}

// NetSetAttachedTo lists the orgs referencing a set (API attachedTo; restart
// fan-out after an edit).
func (s *Store) NetSetAttachedTo(name string) []string {
	name = normalizeID(name)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for _, o := range s.orgs {
		if contains(o.NetSets, name) {
			out = append(out, o.ID)
		}
	}
	sort.Strings(out)
	return out
}
