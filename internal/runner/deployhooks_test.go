package runner

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers P5 P7 SC-ZERO — with no deployment hook installed, the runner's
// deployment helpers answer as today: every tile's one deployment is main,
// its primary, following the work tree; its view is the registry's own
// component; no checkpoint materializes; its env is EnvForComponent's with no
// remap; its socket dir is today's CompKey.
func TestDeploymentHooksZeroState(t *testing.T) {
	r := &Runner{}
	c := &registry.Component{Path: "apps/x", Dir: "/ws/apps/x", Manifest: registry.Manifest{Runtime: "go"}}

	if got := r.primary(c.Path); got != util.MainDeployment {
		t.Errorf("primary = %q, want main", got)
	}
	if code, err := r.codeFor(c.Path, util.MainDeployment); err != nil || code != (Code{WorkTree: true}) {
		t.Errorf("codeFor(main) = %+v, %v; want the work tree", code, err)
	}
	for _, dep := range []string{"dev", "", "Main"} {
		if _, err := r.codeFor(c.Path, dep); !errors.Is(err, util.ErrNoDeployment) {
			t.Errorf("codeFor(%q) error = %v, want ErrNoDeployment", dep, err)
		}
	}
	if v, err := r.view(c, Code{WorkTree: true}); err != nil || v != c {
		t.Errorf("view(work tree) = %p, %v; want the registry's own pointer %p", v, err, c)
	}
	if _, err := r.view(c, Code{Tree: strings.Repeat("a", 40)}); err == nil {
		t.Error("view of a checkpoint without the plane: no error")
	}
	if _, err := r.materialize(c.Path, strings.Repeat("a", 40)); err == nil {
		t.Error("materialize without the plane: no error")
	}

	if env, remap := r.envFor(c, util.MainDeployment); env != nil || remap != nil {
		t.Errorf("envFor without EnvForComponent = %q, %v; want nothing", env, remap)
	}
	r.EnvForComponent = func(*registry.Component) []string { return []string{"XBIN_RES_DB=/ws/data/x"} }
	if env, remap := r.envFor(c, util.MainDeployment); !reflect.DeepEqual(env, []string{"XBIN_RES_DB=/ws/data/x"}) || remap != nil {
		t.Errorf("envFor = %q, %v; want EnvForComponent's env and no remap", env, remap)
	}

	if !r.shouldRun(c.Path, util.MainDeployment) {
		t.Error("shouldRun without ShouldRun = false")
	}
	r.ShouldRun = func(comp string) bool { return comp != "apps/x" }
	if r.shouldRun(c.Path, util.MainDeployment) {
		t.Error("shouldRun ignores ShouldRun")
	}

	// main's socket dir is today's (TestZeroStateKeys' literal); another
	// deployment's is "d-" + 16 hex of SHA-256("apps/x\x00dev"), computed
	// once with sha256sum, never a CompKey shape.
	if got := sockDir("apps/x", util.MainDeployment); got != "apps~x-ebdae547" {
		t.Errorf("sockDir(main) = %q, want apps~x-ebdae547", got)
	}
	if got := sockDir("apps/x", "dev"); got != "d-2a102cf9fefcda61" {
		t.Errorf("sockDir(dev) = %q, want d-2a102cf9fefcda61", got)
	}
	if d := sockDir("apps/x", "dev"); len(d) >= 9 && d[len(d)-9] == '-' {
		t.Errorf("sockDir(dev) = %q has a CompKey's shape", d)
	}
}

// covers P7 PO-5 Z3 — emit: the primary's runner events keep today's types,
// bare component and bytes; a non-primary deployment's never ride an old
// type, only a "deployments" event naming it in data (rule C2).
func TestEmitRuleC2(t *testing.T) {
	hub := events.NewHub()
	ch, cancel := hub.Subscribe(nil)
	defer cancel()
	r := &Runner{Hub: hub}
	take := func() []string {
		var out []string
		for {
			select {
			case e := <-ch:
				b, _ := json.Marshal(e)
				out = append(out, string(b))
			default:
				return out
			}
		}
	}

	r.emit("apps/x", "main", "reload", "")
	r.emit("apps/x", "main", "build-start", "")
	r.emit("apps/x", "main", "build-error", "x.go:1: nope")
	r.emit("apps/x", "main", "build-ok", "")
	want := []string{
		`{"type":"reload","component":"apps/x"}`,
		`{"type":"build-start","component":"apps/x"}`,
		`{"type":"build-error","component":"apps/x","text":"x.go:1: nope"}`,
		`{"type":"build-ok","component":"apps/x"}`,
	}
	if got := take(); !reflect.DeepEqual(got, want) {
		t.Errorf("the primary's events:\n got %q\nwant %q", got, want)
	}

	r.emit("apps/x", "dev", "reload", "")
	r.emit("apps/x", "dev", "build-start", "")
	r.emit("apps/x", "dev", "build-error", "x.go:1: nope")
	r.emit("apps/x", "dev", "build-ok", "")
	r.emit("apps/x", "dev", "status", "hidden") // no other runner event names a deployment
	want = []string{
		`{"type":"deployments","component":"apps/x","data":{"op":"reload","deployment":"dev"}}`,
		`{"type":"deployments","component":"apps/x","data":{"op":"build","deployment":"dev","phase":"start"}}`,
		`{"type":"deployments","component":"apps/x","data":{"op":"build","deployment":"dev","phase":"error","text":"x.go:1: nope"}}`,
		`{"type":"deployments","component":"apps/x","data":{"op":"build","deployment":"dev","phase":"ok"}}`,
	}
	if got := take(); !reflect.DeepEqual(got, want) {
		t.Errorf("a non-primary deployment's events:\n got %q\nwant %q", got, want)
	}

	// The primary is whoever the Primary hook names: main, once not the
	// primary, is a deployment like any other.
	r.Primary = func(string) string { return "dev" }
	r.emit("apps/x", "dev", "reload", "")
	r.emit("apps/x", "main", "reload", "")
	want = []string{
		`{"type":"reload","component":"apps/x"}`,
		`{"type":"deployments","component":"apps/x","data":{"op":"reload","deployment":"main"}}`,
	}
	if got := take(); !reflect.DeepEqual(got, want) {
		t.Errorf("with dev primary:\n got %q\nwant %q", got, want)
	}
}

// covers P7 SC-ZERO PO-12 — the per-deployment names beside Ensure, Track,
// Changed and Stop mean the primary exactly as the old names do, and refuse
// or ignore every other name without creating state; Deploy is refused
// without touching the running generation; no checkpoint tree is in use.
func TestDeploymentNamesMeanThePrimary(t *testing.T) {
	x := &registry.Component{Path: "apps/x", Manifest: registry.Manifest{Runtime: "go"}}
	r, f, tp := newSeamRunner(t, x)
	ctx := context.Background()
	states := func() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.states) }

	// Every other name: refused or ignored, and no state appears.
	if _, err := r.EnsureDeployment(ctx, x, "dev"); !errors.Is(err, util.ErrNoDeployment) {
		t.Errorf("EnsureDeployment(dev) error = %v, want ErrNoDeployment", err)
	}
	r.TrackDeployment(x.Path, "dev")()
	r.ChangedDeployment(x, "dev")
	r.StopDeployment(x.Path, "dev")
	if n := states(); n != 0 {
		t.Errorf("names other than the primary's created %d runner states", n)
	}

	// main: Ensure's single-flight start.
	sock, err := r.EnsureDeployment(ctx, x, util.MainDeployment)
	if err != nil || sock != "fake-run/apps/x/g1.sock" {
		t.Fatalf("EnsureDeployment(main) = %q, %v", sock, err)
	}
	if again, err := r.Ensure(ctx, x); err != nil || again != sock {
		t.Errorf("Ensure after EnsureDeployment(main) = %q, %v; want the same generation %q", again, err, sock)
	}
	if got, want := f.takeLog(), []string{"build apps/x main@worktree", "start apps/x main g1"}; !equalStrings(got, want) {
		t.Errorf("effects %q, want %q", got, want)
	}
	release := r.TrackDeployment(x.Path, util.MainDeployment)
	s := r.state(x.Path)
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active != 1 {
		t.Errorf("TrackDeployment(main): %d active, want 1", active)
	}
	release()

	// Deploy: refused, commit and progress never called, g1 keeps serving.
	called := false
	err = r.Deploy(ctx, x, util.MainDeployment, Code{Tree: strings.Repeat("b", 40)},
		func() error { called = true; return nil }, func(string, string, error) { called = true })
	if err == nil || called {
		t.Errorf("Deploy = %v (callbacks called: %v), want a refusal without callbacks", err, called)
	}
	if again, _ := r.Ensure(ctx, x); again != sock {
		t.Errorf("after Deploy the backend is %q, want %q", again, sock)
	}
	if got := r.RootsInUse(); got != nil {
		t.Errorf("RootsInUse = %q, want nil", got)
	}

	// ChangedTile is today's Changed: a rebuild of the running backend.
	tp.take()
	r.ChangedTile(x)
	settle(t, r, f)
	if got, want := f.takeLog(), []string{"build apps/x main@worktree", "start apps/x main g2", "stop apps/x main g1"}; !equalStrings(got, want) {
		t.Errorf("ChangedTile effects %q, want %q", got, want)
	}
	if got, want := tp.take(), []string{"build-start apps/x", "build-ok apps/x"}; !equalStrings(got, want) {
		t.Errorf("ChangedTile events %q, want %q", got, want)
	}

	// StopDeployment(main) is Stop.
	r.StopDeployment(x.Path, util.MainDeployment)
	if got, want := f.takeLog(), []string{"stop apps/x main g2"}; !equalStrings(got, want) {
		t.Errorf("StopDeployment(main) effects %q, want %q", got, want)
	}

	// Once the Primary hook names another deployment, main is the one refused.
	r.Primary = func(string) string { return "dev" }
	if _, err := r.EnsureDeployment(ctx, x, util.MainDeployment); !errors.Is(err, util.ErrNoDeployment) {
		t.Errorf("EnsureDeployment(main) with dev primary: %v, want ErrNoDeployment", err)
	}
	if _, err := r.EnsureDeployment(ctx, x, "dev"); err != nil {
		t.Errorf("EnsureDeployment(dev) with dev primary: %v", err)
	}
}
