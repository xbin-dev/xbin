package runner

// sealed.go — how a sealed component's backend comes to be (D227): the
// release's own build of it, never a compile on a production host
// (build.go asks these).

import "github.com/xbin-dev/xbin/internal/registry"

// SealedHooks is what boot tells the runner about sealed components.
type SealedHooks struct {
	// SealedPrebuilt answers the release's prebuilt backend of a sealed
	// component, when the install has one (bin/sealed/<name>-<digest>
	// beside xbind); nil or false = none.
	SealedPrebuilt func(c *registry.Component) (bin string, ok bool)
	// SealedCompile lets a sealed component without one be compiled from
	// its materialized source: a development xbind's alone.
	SealedCompile bool
}
