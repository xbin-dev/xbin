package runner

// gen.go — the generation a request is sent to (Ensure, EnsureGen, Gen,
// ensurePrimary): moved out of runner.go (its size budget); D173 is why the
// proxy needs the generation itself, not just its socket.

import (
	"context"
	"fmt"

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

// ensurePrimary is Ensure for deployment dep, c's primary, whose code the
// registry's component describes (07-runtime §5.1).
func (r *Runner) ensurePrimary(ctx context.Context, c *registry.Component, dep string) (*instance, error) {
	if err := registry.ValidateRuntime(c.Manifest); err != nil {
		return nil, fmt.Errorf("component %s: %w", c.Path, err) // runtime "cgi" (D117): never runs
	}
	if c.Manifest.Runtime == "" || c.Manifest.Runtime == "static" {
		return nil, fmt.Errorf("component %s has no long-running backend", c.Path)
	}
	// Lifecycle gate (plans/lifecycle.md): a disabled/offloaded component never
	// spawns — enforced here so no path (proxy, watcher rebuild, grant change)
	// can start it. The proxy still 409s earlier for a nicer message.
	if r.ShouldRun != nil && !r.ShouldRun(c.Path) {
		why := "is not enabled"
		if r.HoldReason != nil {
			if w := r.HoldReason(c.Path); w != "" {
				why = w
			}
		}
		return nil, fmt.Errorf("component %s %s", c.Path, why)
	}
	if r.noGlobal(c.Path, dep) { // only people's partitions run (partitions.go)
		return nil, globalRefusal(c.Path)
	}
	return r.ensureState(ctx, c, r.stateOf(c.Path, dep))
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
