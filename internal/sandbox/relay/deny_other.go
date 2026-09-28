//go:build !linux

package relay

import "net/netip"

// hostLocal is HostDeny's locality test off Linux: the host's interface
// addresses, re-read at most every hostAddrsTTL (Linux asks the routing
// table per flow instead).
func hostLocal() func(netip.Addr) bool {
	return newAddrSet(interfacePrefixes, hostAddrsTTL).contains
}
