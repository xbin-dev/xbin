package runner

import (
	"reflect"
	"testing"

	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers D119c D127q — a tile's at-limit leaves: none without cgroup accounting;
// with it, the flat leaf alone, exactly as Cgroup.AtLimit answers for
// util.CompKey(tile), which the limit alerts asked before and keep keying
// their counters by.
func TestAtLimitTileZeroState(t *testing.T) {
	const tile = "apps/crm"
	r := &Runner{}
	if hits := r.AtLimitTile(tile); hits != nil {
		t.Errorf("without cgroup: %+v", hits)
	}
	r.Cgroup = cgroup.New()
	leaf := util.CompKey(tile)
	mem, pids, ok := r.Cgroup.AtLimit(leaf)
	var want []LimitHit
	if ok {
		want = []LimitHit{{Leaf: leaf, Mem: mem, Pids: pids}}
	}
	if hits := r.AtLimitTile(tile); !reflect.DeepEqual(hits, want) {
		t.Errorf("AtLimitTile = %+v; the flat leaf answers %+v", hits, want)
	}
}
