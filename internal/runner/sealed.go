package runner

// sealed.go — how a sealed component's backend comes to be (D227): the
// release's own build of it, never a compile on a production host
// (build.go asks these).

import (
	"errors"

	"github.com/xbin-dev/xbin/internal/registry"
)

// errRetryStart marks a start that failed for a reason the next start may
// not meet (SealedVerify): the state keeps no sticky error for it.
var errRetryStart = errors.New("this start failed")

// SealedHooks is what boot tells the runner about sealed components.
type SealedHooks struct {
	// SealedPrebuilt answers the release's prebuilt backend of a sealed
	// component, when the install has one (bin/sealed/<name>-<digest>
	// beside xbind); nil or false = none.
	SealedPrebuilt func(c *registry.Component) (bin string, ok bool)
	// SealedCompile lets a sealed component without one be compiled from
	// its materialized source: a development xbind's alone.
	SealedCompile bool
	// SealedVerify checks what is about to run of a sealed component — its
	// tree, its build — where a backend of it is spawned (startFor), never
	// on a request an instance already serves. An error fails that start
	// and is not kept: the next request starts, and checks, again.
	SealedVerify func(c *registry.Component) error
}
