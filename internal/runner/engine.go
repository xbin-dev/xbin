package runner

// engine.go — the runner's seam. A generation transition has five effects:
// build the code, start a process, wait for it to answer, stop a process, and
// read the clock; a failed start's retry waits on a timer (retry.go). buildAndStart, Ensure, Track, the crash watch, the reaper,
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
	healthy func(c *registry.Component, inst *instance) error                   // waitHealthy(inst.sock, inst.waitCh, r.healthFor(c), r.waitingOnXbind(inst))
	stop    func(inst *instance, deadline time.Duration)                        // r.stop
	now     func() time.Time                                                    // time.Now
	after   func(d time.Duration, f func()) (stop func() bool)                  // time.AfterFunc(d, f).Stop
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
	return waitHealthy(inst.sock, inst.waitCh, r.healthFor(c), r.waitingOnXbind(inst))
}

// stopGen terminates a generation: SIGTERM, then SIGKILL after deadline. It
// marks the generation retired before the signal, so a request that fails
// because of the stop already finds it retired (Gen.Retired).
func (r *Runner) stopGen(inst *instance, deadline time.Duration) {
	inst.retired.Store(true)
	if e := r.engine; e != nil && e.stop != nil {
		e.stop(inst, deadline)
		r.live.forget(inst) // an engine's generation has no process whose end says so (live.go)
		return
	}
	r.stop(inst, deadline)
}

// afterFunc runs f once d has passed (a failed start's retry, retry.go);
// stop cancels it.
func (r *Runner) afterFunc(d time.Duration, f func()) (stop func() bool) {
	if e := r.engine; e != nil && e.after != nil {
		return e.after(d, f)
	}
	return time.AfterFunc(d, f).Stop
}

// now is the runner's clock: request stamps, idle reaping, crash windows.
func (r *Runner) now() time.Time {
	if e := r.engine; e != nil && e.now != nil {
		return e.now()
	}
	return time.Now()
}
