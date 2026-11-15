package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
)

// A sealed component is verified where its backend is spawned and only
// there, and a failed check fails that start alone: the next request
// starts — and checks — again, with no change needed in between (a build
// error stays until one).
func TestSealedVerifyAtSpawnOnlyAndNotSticky(t *testing.T) {
	c := &registry.Component{Path: "xbin/x", Sealed: true, Manifest: registry.Manifest{Runtime: "go"}}
	tile := &registry.Component{Path: "apps/t", Manifest: registry.Manifest{Runtime: "go"}}
	r, f, _ := newSeamRunner(t, c, tile)
	checks, fail := 0, errors.New("its build can't be read just now")
	r.SealedVerify = func(got *registry.Component) error {
		if !got.Sealed {
			t.Errorf("SealedVerify asked about %s, which is no sealed component", got.Path)
		}
		checks++
		return fail
	}
	if _, err := r.Ensure(context.Background(), c); err == nil || !errors.Is(err, errRetryStart) {
		t.Fatalf("a start whose check fails: %v", err)
	}
	if _, err := r.Ensure(context.Background(), c); err == nil || checks != 2 {
		t.Fatalf("the next request did not start (and check) again: %v after %d check(s)", err, checks)
	}
	fail = nil
	if _, err := r.Ensure(context.Background(), c); err != nil || checks != 3 {
		t.Fatalf("the start after the check passes: %v after %d check(s)", err, checks)
	}
	for i := 0; i < 5; i++ { // requests an instance already serves: no check
		if _, err := r.Ensure(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	if checks != 3 {
		t.Errorf("%d checks: a request an instance serves was checked", checks)
	}
	if _, err := r.Ensure(context.Background(), tile); err != nil || checks != 3 {
		t.Errorf("a tile's start asked the sealed check: %v, %d", err, checks)
	}
	_ = f
}
