package broker

import (
	"testing"
	"time"
)

// A res: target whose path is rooted ("res:/x/y") names no scope. The walk
// up its directories once never ended there — path.Dir("/") is "/" — so one
// authenticated GET /api/xbin/kv/res:/x/y held a goroutine and a core for
// good.
func TestRootedResourceTargetEndsTheScopeWalk(t *testing.T) {
	b := testBroker(t)
	for _, target := range []string{"res:/x/y", "res:/", "res://", "res:/x", "res:///a/b"} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			if rt, ok := b.resScope(target); ok {
				t.Errorf("resScope(%q) = %+v, want no scope", target, rt)
			}
			if rt, res, ok := b.parseRes(target); ok || res != nil {
				t.Errorf("parseRes(%q) = %+v %v, want nothing", target, rt, res)
			}
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("resolving %q never returned", target)
		}
	}
}
