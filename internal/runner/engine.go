package runner

// engine.go — the runner's seam. A generation transition has five effects:
// build the code, start a process, wait for it to answer, stop a process, and
// read the clock. buildAndStart, Ensure, Track, the crash watch, the reaper,
// Stop and StopAll reach them only through the methods below. Every shipped
// runner has a nil engine, which means today's methods, so the seam adds no
// behaviour; the state-machine tests inject a fake engine and a fake clock
// (fake_engine_test.go).

import (
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
)

// engine replaces the runner's effects. A nil engine, or a nil function in
// one, means today's method (named beside each field).
type engine struct {
	build   func(c *registry.Component) (bin string, err error)                 // r.build
	start   func(c *registry.Component, bin string, gen int) (*instance, error) // r.start
	healthy func(c *registry.Component, inst *instance) error                   // waitHealthy(inst.sock, inst.waitCh, r.healthFor(c))
	stop    func(inst *instance, deadline time.Duration)                        // r.stop
	now     func() time.Time                                                    // time.Now
}

// buildGen produces the runnable entry for c's next generation.
func (r *Runner) buildGen(c *registry.Component) (string, error) {
	if e := r.engine; e != nil && e.build != nil {
		return e.build(c)
	}
	return r.build(c)
}

// startGen spawns generation gen of c from bin.
func (r *Runner) startGen(c *registry.Component, bin string, gen int) (*instance, error) {
	if e := r.engine; e != nil && e.start != nil {
		return e.start(c, bin, gen)
	}
	return r.start(c, bin, gen)
}

// awaitHealthy waits until a started generation answers on its socket.
func (r *Runner) awaitHealthy(c *registry.Component, inst *instance) error {
	if e := r.engine; e != nil && e.healthy != nil {
		return e.healthy(c, inst)
	}
	return waitHealthy(inst.sock, inst.waitCh, r.healthFor(c))
}

// stopGen terminates a generation: SIGTERM, then SIGKILL after deadline.
func (r *Runner) stopGen(inst *instance, deadline time.Duration) {
	if e := r.engine; e != nil && e.stop != nil {
		e.stop(inst, deadline)
		return
	}
	r.stop(inst, deadline)
}

// now is the runner's clock: request stamps, idle reaping, crash windows.
func (r *Runner) now() time.Time {
	if e := r.engine; e != nil && e.now != nil {
		return e.now()
	}
	return time.Now()
}
