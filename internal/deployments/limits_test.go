package deployments

import (
	"net/http"
	"testing"

	"github.com/xbin-dev/xbin/internal/cgroup"
)

// covers D203 — a tile's own ceiling (TileCeiling: the workspace's caps
// raised by its decisions) is every deployment's default and ceiling, for
// that tile alone: LimitsFor follows it, a manager's override below it
// holds and above it is refused naming it; diskGiB's ceiling is the
// tile's scope's raised quota (DiskCeilingFor), the default's without it
// (DiskCeiling), and 501 with neither.
func TestTileCeilingRaisesDeploymentLimits(t *testing.T) {
	f := newGovFx(t, false)
	f.p.TileLimits = cgroup.Limits{MemMax: 2 << 30, PidsMax: 512}
	raised := cgroup.Limits{MemMax: 4 << 30, PidsMax: 512}
	f.p.TileCeiling = func(tile string) cgroup.Limits {
		if tile == opSite {
			return raised
		}
		return f.p.TileLimits
	}
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"})
	for _, dep := range []string{"main", "dev"} {
		if l := f.p.LimitsFor(opSite, dep); l != raised {
			t.Errorf("%s's limits under the raised ceiling: %+v", dep, l)
		}
	}
	if l := f.p.LimitsFor("apps/other", "main"); l != f.p.TileLimits {
		t.Errorf("another tile's limits: %+v, want the workspace's", l)
	}
	mem := func(v int64) error {
		_, err := f.do(ownerP, OpLimits, &LimitsRequest{Tile: opSite, Deployment: "dev", Limits: LimitsPatch{MemMiB: Override{Set: true, Value: &v}}})
		return err
	}
	if err := mem(3072); err != nil {
		t.Fatalf("3 GiB under the raised 4 GiB: %v", err)
	}
	if l := f.p.LimitsFor(opSite, "dev"); l.MemMax != 3<<30 {
		t.Errorf("dev's override: %+v", l)
	}
	if l := f.p.LimitsFor(opSite, "main"); l != raised {
		t.Errorf("main beside dev's override: %+v", l)
	}
	wantErr(t, "above the raised ceiling", mem(4097), http.StatusBadRequest, "memMiB can't exceed the tile's ceiling (4096)")

	f.write("apps/shop/scope.json", "{}")
	f.write("apps/shop/xbin.json", "{}")
	if err := f.reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	f.add(ownerP, &AddRequest{Tile: "apps/shop", Deployment: "dev"})
	disk := func(v int64) error {
		_, err := f.do(ownerP, OpLimits, &LimitsRequest{Tile: "apps/shop", Deployment: "dev", Limits: LimitsPatch{DiskGiB: Override{Set: true, Value: &v}}})
		return err
	}
	f.p.SetGovHooks(func(h *GovHooks) {
		h.DiskCeiling = func() int64 { return 50 << 30 }
		h.DiskCeilingFor = func(tile string) int64 { return map[string]int64{"apps/shop": 80 << 30}[tile] }
	})
	if err := disk(70); err != nil {
		t.Errorf("70 GiB under the scope's raised 80: %v", err)
	}
	wantErr(t, "above the scope's raised quota", disk(81), http.StatusBadRequest, "diskGiB can't exceed the tile's ceiling (80)")
	f.p.SetGovHooks(func(h *GovHooks) { h.DiskCeilingFor = nil })
	wantErr(t, "the default's ceiling", disk(51), http.StatusBadRequest, "diskGiB can't exceed the tile's ceiling (50)")
	f.p.SetGovHooks(func(h *GovHooks) { h.DiskCeiling = nil })
	wantErr(t, "neither", disk(5), http.StatusNotImplemented, "a deployment's disk limit isn't built")
}
