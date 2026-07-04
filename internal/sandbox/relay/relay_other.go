//go:build !linux

package relay

import "errors"

// Relay is a no-op off Linux.
type Relay struct{}

// Start is unsupported off Linux.
func Start(Config) (*Relay, error) { return nil, errors.New("relay: linux only") }

func (r *Relay) Close()       {}
func (r *Relay) Stats() Stats { return Stats{} }

// Splicer is a no-op off Linux.
type Splicer struct{}

// Splice is unsupported off Linux.
func Splice(int, int) *Splicer { return &Splicer{} }
func (s *Splicer) Close()      {}
