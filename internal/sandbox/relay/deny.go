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

const (
	// hostAddrsTTL bounds how stale HostDeny's view of the host's own
	// addresses gets: an interface that comes up after the sandbox started (a
	// VPN, a bridge) is denied within this long.
	hostAddrsTTL = 5 * time.Second
	// hostAddrsRetry paces re-reads after a failed read.
	hostAddrsRetry = time.Second
)

// HostDeny returns the Deny of a relay that must give no route to the host
// itself (a tile sandbox's, plans/tile-sandbox-runtime.md §4). It denies
//   - loopback, link-local, unspecified and multicast addresses;
//   - 10.0.2.0/24, the relay's virtual network;
//   - every address the host delivers locally: its interfaces' addresses (on
//     Linux, the local routing table, which also holds AnyIP ranges). They
//     are read now and re-read at most every few seconds, so an address that
//     appears later is denied too. Until a read succeeds, everything is denied;
//   - the listen addresses given (the whole address: a sandbox reaches none
//     of its ports).
//
// An IPv4-mapped IPv6 destination is judged as the IPv4 address it names.
func HostDeny(listen ...netip.AddrPort) Deny {
	d := newHostDeny(hostLocalPrefixes, hostAddrsTTL, listen...)
	return d.denied
}

type hostDeny struct {
	read   func() ([]netip.Prefix, error)
	ttl    time.Duration
	listen map[netip.Addr]bool

	mu     sync.Mutex
	ok     bool                // a read has succeeded
	addrs  map[netip.Addr]bool // single host addresses
	ranges []netip.Prefix      // wider local ranges (AnyIP)
	next   time.Time           // when to read again
}

func newHostDeny(read func() ([]netip.Prefix, error), ttl time.Duration, listen ...netip.AddrPort) *hostDeny {
	d := &hostDeny{read: read, ttl: ttl, listen: map[netip.Addr]bool{}}
	for _, l := range listen {
		if a := l.Addr(); a.IsValid() {
			d.listen[norm(a)] = true
		}
	}
	d.mu.Lock()
	d.refreshLocked(time.Now())
	d.mu.Unlock()
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
	d.mu.Lock()
	defer d.mu.Unlock()
	d.refreshLocked(time.Now())
	if !d.ok || d.addrs[ip] {
		return true
	}
	for _, p := range d.ranges {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// refreshLocked re-reads the host's addresses when they are due. A failed
// read keeps the last good view (or, with none, denies everything).
func (d *hostDeny) refreshLocked(now time.Time) {
	if !d.next.IsZero() && now.Before(d.next) {
		return
	}
	pfx, err := d.read()
	if err != nil {
		d.next = now.Add(hostAddrsRetry)
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
	d.ok, d.addrs, d.ranges, d.next = true, addrs, ranges, now.Add(d.ttl)
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
