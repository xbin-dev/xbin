// Package webfetch is how xbind reads a web page for a person's workspace
// agent (D239; docs/protocol/delegation.md §Web and MCP): one GET, judged
// before it is made and again at every redirect, dialled only to an
// address checked when the socket connects, bounded in size and time, and
// turned into text. It holds no policy of its own beyond what makes a
// request safe to make from xbind's host — whether a person's agent may
// fetch at all, and which hosts, is the caller's (Fetcher.Check).
//
// What "safe from xbind's host" means: xbind runs inside networks — a LAN,
// a cloud with a metadata endpoint, its own loopback with the daemon's
// listeners on it. A URL is the agent's text, so nothing it names may
// reach any of that: only public unicast addresses are dialled, and the
// address judged is the address connected to (the dialer's Control sees
// the socket's own peer), so a DNS answer that changes between a check and
// the connect reaches nothing.
package webfetch

import (
	"net/netip"
	"strings"
)

// notPublic are the ranges no fetch reaches beyond what netip's own
// predicates say (loopback, private, link-local, multicast, unspecified):
// this network, CGNAT, IETF protocol assignments, documentation,
// benchmarking, the 6to4 relay, reserved and broadcast; the IPv4-compatible
// and IPv4-translated forms, NAT64 (both), the IETF protocol assignments
// (Teredo, benchmarking, ORCHID…), 6to4, discard-only, documentation (both),
// SRv6 SIDs and the old site-local range — the IPv6 forms that carry, or
// route to, an IPv4 address of the above, and what the IANA special-purpose
// registries (checked 2026-11) don't call globally reachable.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),           // IPv4-compatible (deprecated): ::a.b.c.d
	netip.MustParsePrefix("::ffff:0:0:0/96"), // IPv4-translated (SIIT)
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"), // IETF protocol assignments: Teredo, benchmarking, ORCHID, AMT…
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"), // documentation (RFC 9637)
	netip.MustParsePrefix("5f00::/16"), // SRv6 SIDs (RFC 9602)
	netip.MustParsePrefix("fec0::/10"),
}

// PublicAddr reports a public unicast address: one a fetch may dial. An
// IPv4-mapped IPv6 address is judged as the IPv4 address it names.
func PublicAddr(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// numericHost reports a host that a resolver could read as an IPv4
// address in one of inet_aton's forms — a decimal integer (2130706433), a
// hex one (0x7f000001), octal parts (0177.0.0.1), fewer than four parts
// (127.1) — rather than as a name: its last label is all digits or starts
// with 0x. No public DNS name has such a last label (a top-level domain is
// never numeric), so a host like this is either a canonical dotted quad,
// which netip parses, or refused.
func numericHost(host string) bool {
	last := host
	if i := strings.LastIndexByte(host, '.'); i >= 0 {
		last = host[i+1:]
	}
	if last == "" {
		return false
	}
	if strings.HasPrefix(last, "0x") || strings.HasPrefix(last, "0X") {
		return true
	}
	for _, c := range last {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
