package runner

import (
	"testing"
	"time"
)

// A tile's user partitions stop after the default idle time unless the
// tile has its own (PartitionIdleFor: a sealed component's row), and a
// tile that names none — or names zero — keeps the default.
func TestPartitionIdleIsTheTilesOrTheDefault(t *testing.T) {
	r := &Runner{}
	if got := r.partitionIdle("apps/x"); got != partitionIdleReap {
		t.Errorf("without a hook: %s", got)
	}
	r.PartitionIdleFor = func(tile string) time.Duration {
		if tile == "xbin/wsagent" {
			return 3 * time.Minute
		}
		return 0
	}
	if got := r.partitionIdle("xbin/wsagent"); got != 3*time.Minute {
		t.Errorf("a tile with its own idle stop: %s", got)
	}
	if got := r.partitionIdle("apps/x"); got != partitionIdleReap {
		t.Errorf("a tile without one: %s", got)
	}
}
