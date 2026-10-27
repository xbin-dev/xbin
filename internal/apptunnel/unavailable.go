package apptunnel

import (
	"context"
	"errors"

	"github.com/xbin-dev/xbin/internal/apptunnel/sidecar"
	"github.com/xbin-dev/xbin/internal/apptunnel/wire"
)

// errNoSidecar is every call of a build without the iroh sidecar.
var errNoSidecar = errors.New("this xbind has no iroh sidecar (built without it, and no XBIN_IROH)")

// Unavailable is the Runner of a build without the iroh sidecar: the
// tunnel's settings and routes work, its state reads "unavailable", and
// nothing listens. Boot uses it until the sidecar supervisor is wired in.
func Unavailable() sidecar.Runner { return unavailable{} }

type unavailable struct{}

func (unavailable) Start(context.Context, sidecar.Config) error { return errNoSidecar }
func (unavailable) SetAllow(context.Context, []string) error    { return errNoSidecar }
func (unavailable) SetPairing(bool)                             {}
func (unavailable) Drop(string)                                 {}
func (unavailable) SelfTest(context.Context) (wire.Event, error) {
	return wire.Event{}, errNoSidecar
}
func (unavailable) Streams() <-chan sidecar.Stream { return nil }
func (unavailable) Status() sidecar.Status {
	return sidecar.Status{State: sidecar.StateUnavailable, Error: errNoSidecar.Error()}
}
func (unavailable) Stop() {}
