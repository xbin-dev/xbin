package relay

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"
)

// relayNet is the relay's own virtual network: the gateway (10.0.2.2), the
// DNS address (10.0.2.3), the hairpin VIP (10.0.2.4) and the sandbox's own
// address. A flow into it that the relay doesn't serve itself would be dialed
// on the host's network, where 10.0.2.0/24 may be anything.
var relayNet = netip.MustParsePrefix("10.0.2.0/24")

// HostDeny returns the Deny of a relay that must give no route to the host
// itself (a tile sandbox's, plans/tile-sandbox-runtime.md §4). It denies
//   - loopback, link-local, unspecified and multicast addresses;
//   - 10.0.2.0/24, the relay's virtual network;
//   - the listen addresses given (the whole address: a sandbox reaches none
//     of its ports);
//   - every address the host would deliver locally. On Linux that is decided
//     per flow: a route lookup (RTM_GETROUTE) for the destination in the
//     network namespace HostDeny was called in — the lookup the dial itself
//     would make — denies a local, broadcast, multicast or anycast route; a
//     destination with no route passes on to the policy, and a failed lookup
//     denies. So an address that appears after the relay started (a VPN,
//     docker0, a rotated IPv6 temporary address) is denied on its first
//     flow, and an AnyIP range (`ip route add local <cidr> dev lo`) is too.
//     Elsewhere the host's interface addresses are read and re-read at most
//     every few seconds, and until a read succeeds everything is denied.
//
// An IPv4-mapped IPv6 destination is judged as the IPv4 address it names.
func HostDeny(listen ...netip.AddrPort) Deny {
	return newHostDeny(hostLocal(), listen...).denied
}

// hostDeny is HostDeny's state: the static checks, the listen set and the
// host-locality test (local reports whether the host delivers ip to itself;
// it answers true when it can't tell).
type hostDeny struct {
	listen map[netip.Addr]bool
	local  func(netip.Addr) bool
}

func newHostDeny(local func(netip.Addr) bool, listen ...netip.AddrPort) *hostDeny {
	d := &hostDeny{local: local, listen: map[netip.Addr]bool{}}
	for _, l := range listen {
		if a := l.Addr(); a.IsValid() {
			d.listen[norm(a)] = true
		}
	}
	return d
}

// norm is the form every check compares: IPv4-mapped addresses unmapped,
// zones dropped.
func norm(ip netip.Addr) netip.Addr { return ip.Unmap().WithZone("") }

func (d *hostDeny) denied(ip netip.Addr) bool {
	ip = norm(ip)
	if !ip.IsValid() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		relayNet.Contains(ip) || d.listen[ip] {
		return true
	}
	return d.local(ip)
}

const (
	// hostAddrsTTL bounds how stale an addrSet's view of the host's own
	// addresses gets (off Linux, where there is no per-flow route lookup):
	// an interface that comes up after the sandbox started (a VPN, a
	// bridge) is denied within this long.
	hostAddrsTTL = 5 * time.Second
	// hostAddrsRetry paces re-reads after a failed read.
	hostAddrsRetry = time.Second
)

// addrSet is the host's local addresses as last read, re-read when older
// than ttl: HostDeny's locality test off Linux.
type addrSet struct {
	read func() ([]netip.Prefix, error)
	ttl  time.Duration

	mu     sync.Mutex
	ok     bool                // a read has succeeded
	addrs  map[netip.Addr]bool // single host addresses
	ranges []netip.Prefix      // wider local ranges (AnyIP)
	next   time.Time           // when to read again
}

func newAddrSet(read func() ([]netip.Prefix, error), ttl time.Duration) *addrSet {
	s := &addrSet{read: read, ttl: ttl}
	s.mu.Lock()
	s.refreshLocked(time.Now())
	s.mu.Unlock()
	return s
}

// contains reports whether ip is one of the host's addresses — true for
// everything until a read has succeeded.
func (s *addrSet) contains(ip netip.Addr) bool {
	ip = norm(ip)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked(time.Now())
	if !s.ok || s.addrs[ip] {
		return true
	}
	for _, p := range s.ranges {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// refreshLocked re-reads the host's addresses when they are due. A failed
// read keeps the last good view (or, with none, denies everything).
func (s *addrSet) refreshLocked(now time.Time) {
	if !s.next.IsZero() && now.Before(s.next) {
		return
	}
	pfx, err := s.read()
	if err != nil {
		s.next = now.Add(hostAddrsRetry)
		return
	}
	addrs := map[netip.Addr]bool{}
	var ranges []netip.Prefix
	for _, p := range pfx {
		if !p.IsValid() {
			continue
		}
		a := norm(p.Addr())
		bits := p.Bits()
		if p.Addr().Is4In6() {
			bits -= 96
		}
		if bits < 0 {
			continue
		}
		if bits == a.BitLen() {
			addrs[a] = true
		} else {
			ranges = append(ranges, netip.PrefixFrom(a, bits).Masked())
		}
	}
	s.ok, s.addrs, s.ranges, s.next = true, addrs, ranges, now.Add(s.ttl)
}

// interfacePrefixes is every interface address, as a single-address prefix.
func interfacePrefixes() ([]netip.Prefix, error) {
	as, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, a := range as {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if addr, ok := netip.AddrFromSlice(ip); ok {
			addr = norm(addr)
			out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
		}
	}
	if len(out) == 0 {
		return nil, errors.New("relay: no host addresses")
	}
	return out, nil
}
