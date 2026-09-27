//go:build !linux

package relay

import "net/netip"

// hostLocalPrefixes is the host's interface addresses (Linux reads the local
// routing table instead).
func hostLocalPrefixes() ([]netip.Prefix, error) { return interfacePrefixes() }
