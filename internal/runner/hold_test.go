package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
)

// Ensure's refusal of a component ShouldRun holds says HoldReason's why
// (an encryption hold, a lifecycle state), else "is not enabled".
func TestEnsureSaysWhyHeld(t *testing.T) {
	c := &registry.Component{Path: "apps/x", Manifest: registry.Manifest{Runtime: "go"}}
	r := &Runner{ShouldRun: func(string) bool { return false }}
	if _, err := r.Ensure(context.Background(), c); err == nil || err.Error() != "component apps/x is not enabled" {
		t.Fatalf("no reason: %v", err)
	}
	r.HoldReason = func(string) string {
		return "is held: it uses the encrypted resource res:apps/x/db, and the vault is sealed"
	}
	if _, err := r.Ensure(context.Background(), c); err == nil || !strings.Contains(err.Error(), "apps/x is held: it uses the encrypted resource res:apps/x/db") {
		t.Fatalf("with a reason: %v", err)
	}
}
