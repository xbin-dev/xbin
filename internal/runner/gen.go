package runner

// gen.go — the generation a request is sent to (Ensure, EnsureGen, Gen):
// moved out of runner.go (its size budget); D173 is why the proxy needs
// the generation itself, not just its socket.

import (
	"context"

	"github.com/xbin-dev/xbin/internal/registry"
)

// Ensure returns the unix socket of a healthy backend for c's primary,
// (re)building first if needed. Blocks concurrent callers during builds
// (single-flight) so a save under load never surfaces connection-refused.
func (r *Runner) Ensure(ctx context.Context, c *registry.Component) (string, error) {
	return sockOf(r.ensurePrimary(ctx, c, r.primary(c.Path)))
}

// EnsureGen is Ensure answering the generation itself, for a caller that
// sends it a request and must tell, when the request fails, whether xbind
// retired the generation meanwhile (the proxy).
func (r *Runner) EnsureGen(ctx context.Context, c *registry.Component) (Gen, error) {
	inst, err := r.ensurePrimary(ctx, c, r.primary(c.Path))
	return Gen{inst}, err
}

// Gen is one backend generation a request is sent to: its socket, and
// whether xbind has retired it since. The zero Gen has no socket and is
// never retired.
type Gen struct{ inst *instance }

// Sock is the generation's unix socket.
func (g Gen) Sock() string {
	if g.inst == nil {
		return ""
	}
	return g.inst.sock
}

// Retired says whether xbind has stopped the generation: a swap replaced it
// (the deployment's newer generation already serves), or a stop, a reap or
// a shutdown ended it. A generation that exited by itself (a crash) is not
// retired. Once true it stays true: a retired generation is never handed
// out again.
func (g Gen) Retired() bool { return g.inst != nil && g.inst.retired.Load() }

// sockOf is a generation's socket, for the callers that only dial it.
func sockOf(inst *instance, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return inst.sock, nil
}
