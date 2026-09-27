package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
)

// Every path that starts a backend goes through Ensure (proxy, ingress,
// stream dials, alwaysOn); a tile still declaring the removed runtime
// "cgi" (D117) is refused there with the removal message, and a save never
// schedules a build for it. A zero Runner has no state map: getting past
// either check would panic.
func TestRemovedCGINeverStarts(t *testing.T) {
	c := &registry.Component{Path: "apps/old", Dir: t.TempDir(),
		Manifest: registry.Manifest{Runtime: "cgi", AlwaysOn: true}}
	r := &Runner{}
	_, err := r.Ensure(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), `runtime "cgi" was removed`) {
		t.Fatalf("Ensure = %v, want the removal error", err)
	}
	r.Changed(c) // must be a no-op
}
