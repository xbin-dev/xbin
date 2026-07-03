//go:build !linux

package relay

import (
	"errors"
	"net/netip"
)

// Allow decides whether a flow to (ip, port) may leave.
type Allow func(ip netip.Addr, port int) bool

// Relay is a no-op off Linux.
type Relay struct{}

// Start is unsupported off Linux.
func Start(int, Allow, string) (*Relay, error) { return nil, errors.New("relay: linux only") }

func (r *Relay) Close() {}
