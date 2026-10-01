package runner

import (
	"context"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
)

// covers D173 — the proxy resends a request whose generation failed it only
// when xbind retired that generation (internal/proxy rerouting). A
// generation is retired exactly when xbind stops it: a swap's old one, a
// reap, a stop and a shutdown; its own exit (a crash) is no retirement; and
// a retired generation is never handed out again. Driven through the
// engine seam, so every stop is the runner's own.
func TestGenRetired(t *testing.T) {
	c := &registry.Component{Path: "apps/x", Manifest: registry.Manifest{Runtime: "go"}}
	r, f, _ := newSeamRunner(t, c)
	ensure := func() Gen {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		g, err := r.EnsureGen(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if g.Retired() {
			t.Fatalf("Ensure handed out a retired generation %s", g.Sock())
		}
		settle(t, r, f)
		return g
	}

	g1 := ensure()
	r.Changed(c) // a save: g2 swaps in, g1 drains
	settle(t, r, f)
	if !g1.Retired() {
		t.Errorf("the generation a swap replaced (%s) is not retired", g1.Sock())
	}
	g2 := ensure()
	if g2.Sock() == g1.Sock() {
		t.Fatalf("after the swap Ensure still answers %s", g1.Sock())
	}
	if sock, err := r.EnsureDeployment(context.Background(), c, "main"); err != nil || sock != g2.Sock() {
		t.Errorf("EnsureDeployment(main) = %q, %v; EnsureGen answered %q", sock, err, g2.Sock())
	}

	f.crash(c.Path, "main")
	settle(t, r, f)
	if g2.Retired() {
		t.Errorf("a generation that crashed (%s) counts as retired", g2.Sock())
	}

	g3 := ensure()
	f.advance(idleReap + time.Minute)
	r.reapOnce()
	settle(t, r, f)
	if !g3.Retired() {
		t.Errorf("a reaped generation (%s) is not retired", g3.Sock())
	}

	g4 := ensure()
	r.Stop(c.Path)
	if !g4.Retired() {
		t.Errorf("a stopped generation (%s) is not retired", g4.Sock())
	}

	g5 := ensure()
	r.StopAll()
	if !g5.Retired() {
		t.Errorf("a generation StopAll ended (%s) is not retired", g5.Sock())
	}
	if (Gen{}).Retired() || (Gen{}).Sock() != "" {
		t.Error("the zero Gen has a socket or is retired")
	}
}
