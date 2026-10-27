package boot

import (
	"testing"

	"github.com/xbin-dev/xbin/internal/limits"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/vm"
)

// covers D203 — stepTileLimits on a booted workspace: the hooks are in
// (a tile's ceiling, VM grants, OOM hits); with no decision the ceilings
// are today's; each kind's default and whether it is enforced there
// (memMiB on a backend with cgroup delegation, not a static tile; the VM
// and sandbox kinds where those runtimes run; diskGiB never: the
// broker's); the maxima and the room; an OOM hit's shape (a primary's is
// the tile's, another deployment's names it, a partition's says so); and
// applying a decision to a tile with nothing running is a no-op.
func TestTileLimitsWiring(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	d := zsBoot(t, zsWorkspace(t))
	st := d.st
	if st.Deployments.TileCeiling == nil || st.Run.OnOOM == nil || st.Run.VMGrant == nil {
		t.Fatal("stepTileLimits installed no hooks")
	}
	if got := st.Deployments.TileCeiling("apps/zsnode"); got != st.Deployments.TileLimits {
		t.Errorf("the ceiling without a decision: %+v, want the workspace's %+v", got, st.Deployments.TileLimits)
	}
	if m, c := st.Run.VMGrant("apps/zsnode"); m != 0 || c != 0 {
		t.Errorf("a VM grant without a decision: %d %d", m, c)
	}

	cg := st.Run.Cgroup.Enabled()
	// the default is XBIN_LIMIT_MEM's 2 GiB with or without cgroups: only
	// whether it is enforced differs (it read 0 without them)
	if def, on := st.limitDefault("apps/zsnode", limits.MemMiB); on != cg || def != 2048 {
		t.Errorf("memMiB on a backend: %d %v (cgroups %v)", def, on, cg)
	}
	for _, c := range []struct {
		tile string
		k    limits.Kind
		def  int64
	}{
		{"apps/zs", limits.MemMiB, 2048},                   // static: nothing to cap
		{"apps/zsnode", limits.VMMemMiB, vm.DefaultMemMiB}, // no VM manager here
		{"apps/zsnode", limits.VMVCPUs, vm.DefaultVCPUs},
		{"apps/zsnode", limits.SbxMemMiB, 8192}, // the sandboxes policy's perTile; no isolation
		{"apps/zsnode", limits.SbxVCPUs, 8},
		{"apps/zsnode", limits.SbxDiskGiB, 100},
		{"apps/zsnode", limits.DiskGiB, 0}, // the broker's own
	} {
		if def, on := st.limitDefault(c.tile, c.k); on || def != c.def {
			t.Errorf("%s %s: %d enforced %v, want %d not enforced", c.tile, c.k, def, on, c.def)
		}
	}
	// the workspace's defaults (tile ""): enforced where the runtime runs
	if def, on := st.limitDefault("", limits.MemMiB); on != cg || def != 2048 {
		t.Errorf("the workspace's memMiB: %d %v", def, on)
	}
	if def, on := st.limitDefault("", limits.SbxMemMiB); on || def != 8192 {
		t.Errorf("the workspace's sbxMemMiB: %d %v (no isolation)", def, on)
	}
	if def, on := st.limitDefault("", limits.VMMemMiB); on || def != vm.DefaultMemMiB {
		t.Errorf("the workspace's vmMemMiB: %d %v (no VM runtime)", def, on)
	}
	if a := st.limitApplied("apps/zsnode", limits.MemMiB); a != 0 {
		t.Errorf("applied with nothing running: %d", a)
	}
	if m := st.limitMax(limits.MemMiB); m != runner.HostMemory()>>20 || m <= 0 {
		t.Errorf("memMiB's maximum: %d", m)
	}
	if m := st.limitMax(limits.VMVCPUs); m < 1 || m > 32 {
		t.Errorf("vmVCPUs' maximum: %d", m)
	}
	if m := st.limitMax(limits.SbxVCPUs); m != 256 {
		t.Errorf("sbxVCPUs' maximum: %d", m)
	}
	src := st.limitSources()
	if c := src.Capacity(limits.MemMiB); c.Kind != limits.MemMiB || c.Total <= 0 || c.Free < 0 || c.Free > c.Total || c.Max != c.Total {
		t.Errorf("memory's room: %+v", c)
	}
	if c := src.Capacity(limits.SbxDiskGiB); c.Total <= 0 || c.Max <= 0 || c.Max >= c.Total {
		t.Errorf("sandbox disk's room: %+v", c)
	}
	if u := src.Usage("apps/zsnode", limits.SbxMemMiB); u != 0 {
		t.Errorf("a tile without sandboxes uses %d", u)
	}

	h := st.oomHit("apps/zsnode", "main", false, 2, 2048, "")
	if h.Deployment != "" || h.Partition || h.Count != 1 || h.Value != 2048 || h.Detail != "OOM-killed (2 processes)" || h.At.IsZero() || h.Since != h.At {
		t.Errorf("the primary's hit: %+v", h)
	}
	h = st.oomHit("apps/zsnode", "dev", false, 1, 0, "while the backend kept running")
	if h.Deployment != "dev" || h.Detail != "OOM-killed (1 process) while the backend kept running" || h.Value != st.Deployments.TileLimits.MemMax>>20 {
		t.Errorf("a non-primary deployment's hit: %+v", h)
	}
	if h = st.oomHit("apps/zsnode", "", true, 1, 512, ""); !h.Partition || h.Deployment != "" {
		t.Errorf("a person's partition's hit: %+v", h)
	}
	src.Apply("apps/zsnode", limits.MemMiB, false)
	src.Apply("apps/zsnode", limits.SbxMemMiB, true)
	src.Apply("apps/nope", limits.VMMemMiB, true)
}
