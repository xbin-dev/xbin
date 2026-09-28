// Package sandbox runs component backends in per-component OS sandboxes
// (user/mount/pid/ipc/uts/net namespaces + an overlay rootfs), the Tier-3
// isolation of plans/isolation.md. This file is the OS-independent egress
// policy — the vocabulary of the net:* grants — so it builds and tests
// everywhere; the namespace machinery is in the linux-only files.
package sandbox

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// Rule is one parsed egress grant.
type Rule struct {
	Internet bool         // net:internet — any *public* address
	Net      netip.Prefix // net:<cidr|ip> — a subnet/host by address
	Host     string       // net:<hostname> — matched at DNS-resolution time
	Port     int          // 0 = any port
}

// EgressPolicy is a component's set of egress rules; default-deny.
type EgressPolicy struct {
	Rules []Rule
	// strict narrows what "internet" means (Strict): a tile sandbox's
	// policy has it, a backend's or terminal's doesn't.
	strict bool
}

// Strict returns the policy with the strict "internet" test, the one a tile
// sandbox's class runs under (plans/tile-sandbox-runtime.md §4, the D120
// addendum): the sandbox-manager contract promises that `internet` reaches
// no private or local network, so besides what net:internet always refuses
// it refuses CGNAT (100.64.0.0/10, which Tailscale uses), benchmarking
// (198.18.0.0/15), reserved (240.0.0.0/4) and NAT64 (64:ff9b::/96 and the
// local-use 64:ff9b:1::/48) addresses. Allow, Reach and Covers honour it; a
// relay running it sets Config.StrictPublic so its DNS pins do too. A
// backend's net:internet is unchanged: narrowing it would change existing
// tiles' egress.
func (p EgressPolicy) Strict() EgressPolicy {
	p.strict = true
	return p
}

// IsStrict reports whether the policy has the strict "internet" test.
func (p EgressPolicy) IsStrict() bool { return p.strict }

// ParseRule parses a grant target like "net:internet:443",
// "net:10.0.0.0/24:5432", "net:192.168.1.5", "net:db.internal", or bracketed
// IPv6 "net:[2001:db8::]/32" / "net:[::1]:80".
func ParseRule(target string) (Rule, error) {
	rest, ok := strings.CutPrefix(target, "net:")
	if !ok {
		return Rule{}, fmt.Errorf("egress target must start with %q", "net:")
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return Rule{}, fmt.Errorf("empty egress target")
	}

	var host string
	port := 0

	switch {
	case rest[0] == '[': // bracketed IPv6: [addr] optionally + /prefix and/or :port
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return Rule{}, fmt.Errorf("unterminated [ in %q", target)
		}
		host = rest[1:end]
		tail := rest[end+1:]
		if i := strings.LastIndexByte(tail, ':'); i >= 0 && allDigits(tail[i+1:]) {
			p, err := parsePort(tail[i+1:])
			if err != nil {
				return Rule{}, err
			}
			port = p
			tail = tail[:i]
		}
		host += tail // may carry a "/prefix" for a CIDR
	default:
		// A trailing ":<digits>" is a port. IPv4 CIDRs/addrs and "internet" have
		// no other colon, so LastIndexByte is unambiguous for them.
		if i := strings.LastIndexByte(rest, ':'); i >= 0 && allDigits(rest[i+1:]) {
			p, err := parsePort(rest[i+1:])
			if err != nil {
				return Rule{}, err
			}
			port = p
			rest = rest[:i]
		}
		host = rest
	}

	if host == "internet" {
		return Rule{Internet: true, Port: port}, nil
	}
	if strings.Contains(host, "/") {
		pfx, err := netip.ParsePrefix(host)
		if err != nil {
			return Rule{}, fmt.Errorf("bad CIDR %q: %w", host, err)
		}
		return Rule{Net: pfx.Masked(), Port: port}, nil
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return Rule{Net: netip.PrefixFrom(addr, addr.BitLen()), Port: port}, nil
	}
	// Otherwise a hostname — matched when the relay's DNS resolves it.
	return Rule{Host: strings.ToLower(host), Port: port}, nil
}

// Parse builds an EgressPolicy from a set of grant targets, ignoring non-net:
// targets (so it can be handed the component's full uses list).
func Parse(targets []string) (EgressPolicy, error) {
	var p EgressPolicy
	for _, t := range targets {
		if !strings.HasPrefix(t, "net:") {
			continue
		}
		r, err := ParseRule(t)
		if err != nil {
			return p, err
		}
		p.Rules = append(p.Rules, r)
	}
	return p, nil
}

// Allow reports whether a connection to (ip, port) is permitted. Host rules are
// resolved to addresses by the relay before this is called, so only Internet
// and Net rules match here.
func (p EgressPolicy) Allow(ip netip.Addr, port int) bool {
	for _, r := range p.Rules {
		if r.Port != 0 && r.Port != port {
			continue
		}
		if r.Internet {
			if p.public(ip) {
				return true
			}
			continue
		}
		if r.Host != "" {
			continue // matched at DNS layer, not by raw IP
		}
		if r.Net.IsValid() && r.Net.Contains(ip) {
			return true
		}
	}
	return false
}

// AllowsHost reports whether a hostname is covered by a host rule (the relay
// pairs this with an Allow on the resolved address). A rule may carry one
// '*' glob (org network sets, D54 — per-tile bindings name concrete hosts):
// `*.example.com` matches any depth below the apex and, like the allowance
// grammar, the apex itself.
func (p EgressPolicy) AllowsHost(name string, port int) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	for _, r := range p.Rules {
		if r.Host == "" || (r.Port != 0 && r.Port != port) {
			continue
		}
		if hostMatch(r.Host, name) {
			return true
		}
	}
	return false
}

// hostMatch: exact, or a single-'*' glob with the `*.x.y` ⇒ `x.y` carve-out.
func hostMatch(pat, name string) bool {
	i := strings.IndexByte(pat, '*')
	if i < 0 {
		return pat == name
	}
	pre, suf := pat[:i], pat[i+1:]
	if len(name) >= len(pre)+len(suf) && strings.HasPrefix(name, pre) && strings.HasSuffix(name, suf) {
		return true
	}
	rest, ok := strings.CutPrefix(pat, "*.")
	return ok && rest == name
}

// Empty reports whether the policy grants no egress (default-deny → empty netns).
func (p EgressPolicy) Empty() bool { return len(p.Rules) == 0 }

// The reach vocabulary: what a sandbox may reach, in the sandbox-manager
// contract's words (docs/sandbox-manager.md, `egress`), ordered
// none < internet < open.
const (
	ReachNone     = "none"     // nothing: no network at all
	ReachInternet = "internet" // the public internet only
	ReachOpen     = "open"     // more than that: a LAN, a private range
)

// Reach summarises the policy in the contract's vocabulary: ReachNone when it
// grants nothing; ReachInternet when every rule stays on the public internet
// (an internet rule; a host rule, whose DNS pins are public-only; a prefix
// that is wholly public); ReachOpen otherwise. It never claims less than
// Allow admits: a prefix counts as public only when every address in it
// passes the same test net:internet uses — the strict one under Strict, so
// a strict net:100.64.0.0/10 is open.
func (p EgressPolicy) Reach() string {
	if p.Empty() {
		return ReachNone
	}
	for _, r := range p.Rules {
		if r.Internet || r.Host != "" || !r.Net.IsValid() {
			continue
		}
		if !publicPrefix(r.Net, p.strict) {
			return ReachOpen
		}
	}
	return ReachInternet
}

// Covers reports whether p admits every flow q admits — p is a superset of
// q, so a sandbox running under q may keep its flows when its class
// resolves to p (plans/tile-sandbox-runtime.md §4: a class change that
// narrows stops the sandbox; one that widens waits for the next start).
// It is conservative: each rule of q must sit inside a single rule of p,
// so a q rule that only a union of p's rules covers counts as narrowed. A
// strict p never covers an internet or host rule of a non-strict q (whose
// internet, and DNS pins, reach the ranges Strict refuses).
func (p EgressPolicy) Covers(q EgressPolicy) bool {
	wider := p.strict && !q.strict // q's internet reaches past p's
	for _, r := range q.Rules {
		if !slices.ContainsFunc(p.Rules, func(s Rule) bool { return s.covers(r, p.strict, wider) }) {
			return false
		}
	}
	return true
}

// covers reports whether rule s (of a policy strict or not) admits every
// flow rule r admits; wider says r's internet is wider than s's.
func (s Rule) covers(r Rule, strict, wider bool) bool {
	if s.Port != 0 && s.Port != r.Port {
		return false
	}
	switch {
	case r.Internet:
		return s.Internet && !wider
	case r.Host != "":
		// A host rule admits only public pins (the relay never pins a
		// private answer), so an internet rule covers it too.
		return !wider && (s.Internet || (s.Host != "" && hostMatch(s.Host, r.Host)))
	case r.Net.IsValid():
		if s.Internet {
			return publicPrefix(r.Net, strict)
		}
		return s.Net.IsValid() && s.Net.Bits() <= r.Net.Bits() && s.Net.Contains(r.Net.Masked().Addr())
	}
	return false
}

// nonPublic is every range isPublic refuses: unspecified, RFC1918 and ULA,
// loopback, link-local, multicast — and, since isPublic unmaps, the same
// IPv4 ranges IPv4-mapped. strictNonPublic adds what isPublicStrict also
// refuses.
var (
	nonPublic = prefixes([]string{"0.0.0.0/32", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4"},
		[]string{"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8"})
	strictOnly = prefixes([]string{"100.64.0.0/10", "198.18.0.0/15", "240.0.0.0/4"},
		[]string{"64:ff9b::/96", "64:ff9b:1::/48"})
	strictNonPublic = append(slices.Clip(nonPublic), strictOnly...)
)

// prefixes parses IPv4 ranges (each also IPv4-mapped) and IPv6 ones.
func prefixes(v4, v6 []string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range v4 {
		p := netip.MustParsePrefix(s)
		out = append(out, p, netip.PrefixFrom(netip.AddrFrom16(p.Addr().As16()), p.Bits()+96))
	}
	for _, s := range v6 {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// publicPrefix reports whether every address in pfx is public — under the
// strict test when strict.
func publicPrefix(pfx netip.Prefix, strict bool) bool {
	pfx = pfx.Masked()
	np := nonPublic
	if strict {
		np = strictNonPublic
	}
	for _, n := range np {
		if pfx.Overlaps(n) {
			return false
		}
	}
	return true
}

// HasHostRules reports whether any rule matches by hostname — the signal to
// enable the relay's DNS pinning (D35).
func (p EgressPolicy) HasHostRules() bool {
	for _, r := range p.Rules {
		if r.Host != "" {
			return true
		}
	}
	return false
}

// String renders a rule back to its net:… grant form (for display).
func (r Rule) String() string {
	var base string
	switch {
	case r.Internet:
		base = "net:internet"
	case r.Host != "":
		base = "net:" + r.Host
	case r.Net.IsValid():
		if r.Net.Bits() == r.Net.Addr().BitLen() {
			base = "net:" + r.Net.Addr().String()
		} else {
			base = "net:" + r.Net.String()
		}
	default:
		base = "net:?"
	}
	if r.Port != 0 {
		base += ":" + strconv.Itoa(r.Port)
	}
	return base
}

// Strings renders the policy's rules to their grant forms.
func (p EgressPolicy) Strings() []string {
	out := make([]string, len(p.Rules))
	for i, r := range p.Rules {
		out[i] = r.String()
	}
	return out
}

// isPublic is the "internet" test: a routable public address, explicitly NOT
// RFC1918/ULA/loopback/link-local — so net:internet never reaches the LAN.
// An IPv4-mapped address is judged as the IPv4 address it names (netip's
// IsUnspecified alone doesn't unmap, and ::ffff:0.0.0.0 dials the host).
func isPublic(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() && !ip.IsUnspecified()
}

// isPublicStrict is Strict's "internet" test: isPublic, minus CGNAT,
// benchmarking, reserved and NAT64 addresses (strictOnly).
func isPublicStrict(ip netip.Addr) bool {
	if !isPublic(ip) {
		return false
	}
	ip = ip.Unmap()
	for _, p := range strictOnly {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// public is the policy's "internet" test: isPublic, or isPublicStrict under
// Strict.
func (p EgressPolicy) public(ip netip.Addr) bool {
	if p.strict {
		return isPublicStrict(ip)
	}
	return isPublic(ip)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("bad port %q", s)
	}
	return n, nil
}
