// Package sandbox runs component backends in per-component OS sandboxes
// (user/mount/pid/ipc/uts/net namespaces + an overlay rootfs), the Tier-3
// isolation of plans/isolation.md. This file is the OS-independent egress
// policy — the vocabulary of the net:* grants — so it builds and tests
// everywhere; the namespace machinery is in the linux-only files.
package sandbox

import (
	"fmt"
	"net/netip"
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
type EgressPolicy struct{ Rules []Rule }

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
			if isPublic(ip) {
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
// pairs this with an Allow on the resolved address).
func (p EgressPolicy) AllowsHost(name string, port int) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	for _, r := range p.Rules {
		if r.Host == "" || (r.Port != 0 && r.Port != port) {
			continue
		}
		if r.Host == name {
			return true
		}
	}
	return false
}

// Empty reports whether the policy grants no egress (default-deny → empty netns).
func (p EgressPolicy) Empty() bool { return len(p.Rules) == 0 }

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
func isPublic(ip netip.Addr) bool {
	return ip.IsValid() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() && !ip.IsUnspecified()
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
