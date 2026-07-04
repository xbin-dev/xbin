package main

import "net"

// parseIPv4 pulls the protocol, source and destination out of a raw IPv4 packet
// (the L3 payload we get off the point-to-point client TUNs, which have no
// link-layer header). ok is false for anything that isn't a well-formed IPv4
// packet — IPv6, runts, junk — which we simply ignore.
func parseIPv4(b []byte) (proto byte, src, dst net.IP, ok bool) {
	if len(b) < 20 || b[0]>>4 != 4 {
		return 0, nil, nil, false
	}
	ihl := int(b[0]&0x0f) * 4
	if ihl < 20 || len(b) < ihl {
		return 0, nil, nil, false
	}
	// Copy out of the shared read buffer — the caller reuses it.
	src = net.IP(append([]byte(nil), b[12:16]...))
	dst = net.IP(append([]byte(nil), b[16:20]...))
	return b[9], src, dst, true
}

// trackable reports whether a remote address is one worth surfacing to the owner
// and metering: a real, globally-routable unicast host. We drop our own machinery
// (the /30 client links, loopback, link-local) and anything not globally routable
// (RFC1918/CGNAT/multicast) — the internet builtin never carries those anyway, so
// they only appear as noise.
func trackable(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil || ip4.IsUnspecified() {
		return false
	}
	if ip4.IsLoopback() || ip4.IsLinkLocalUnicast() || ip4.IsMulticast() || ip4.IsInterfaceLocalMulticast() {
		return false
	}
	if ip4[0] == 255 { // broadcast
		return false
	}
	return ip4.IsGlobalUnicast() && !isPrivate(ip4)
}

// isPrivate covers the ranges the internet builtin already excludes: RFC1918,
// CGNAT (100.64/10), and the 10.42/16 link space this tile uses internally.
func isPrivate(ip4 net.IP) bool {
	switch {
	case ip4[0] == 10:
		return true
	case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
		return true
	case ip4[0] == 192 && ip4[1] == 168:
		return true
	case ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127:
		return true
	}
	return false
}
