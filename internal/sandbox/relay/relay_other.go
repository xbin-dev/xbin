//go:build !linux

package relay

import "errors"

// Relay is a no-op off Linux.
type Relay struct{}

// Start is unsupported off Linux.
func Start(int, Allow, string) (*Relay, error) { return nil, errors.New("relay: linux only") }

func (r *Relay) Close()       {}
func (r *Relay) Stats() Stats { return Stats{} }
