package runner

import (
	"testing"

	"github.com/xbin-dev/xbin/internal/sbx"
)

// covers D119c PO-11 — the sandbox registry lists a backend generation in the
// leaf its start placed it in (WP-34's chooseLeaf, WP-S4's rows): main's
// zero-state generation in the flat leaf, and once the tile has its
// parent, main's next generation in its nested leaf; without cgroup
// accounting no leaf is listed.
func TestRegistryListsPlacedLeaf(t *testing.T) {
	h := newHostWorld(t, 0)
	cg := newFakeCgroup(tileCaps)
	h.r.cgOps = cg
	reg := sbx.New()
	h.r.Sandboxes = reg
	if got := h.ensure(); got != "g1" {
		t.Fatalf("ensure: %s", got)
	}
	listed := func(gen string) string {
		t.Helper()
		e, ok := reg.Get("backend:" + ckX + ":" + gen)
		if !ok {
			t.Fatalf("%s isn't listed", gen)
		}
		return e.Leaf
	}
	if l, placed := listed("g1"), curOf(t, h.r, "apps/x", "main").leaf; l != placed || l != ckX {
		t.Errorf("g1 listed in %q, placed in %q; want the flat %q", l, placed, ckX)
	}
	h.r.joinLeaf("apps/x", "dev", h.r.chooseLeaf("apps/x", "dev"), "no-vm.sock", fakePid) // the tile gains its parent
	h.r.Changed(h.c)
	waitUntil(t, "g2 healthy", func() bool { return statusOf(h.r, "apps/x") == "healthy g2" })
	if l, placed := listed("g2"), curOf(t, h.r, "apps/x", "main").leaf; l != placed || l != leafXmn {
		t.Errorf("g2 listed in %q, placed in %q; want %q", l, placed, leafXmn)
	}
	// A runner of its own: h.r's cgroup model is set once, before it runs —
	// g1's drain still reads it (leaveLeaf) while this asks, so clearing it
	// here raced (-race, under load).
	if l := (&Runner{}).listedLeaf(leafXmn); l != "" {
		t.Errorf("without cgroup accounting the listed leaf is %q", l)
	}
	if l := (&Runner{cgOps: disabledCgroup{cg}}).listedLeaf(leafXmn); l != "" {
		t.Errorf("with cgroup accounting off the listed leaf is %q", l)
	}
}

// disabledCgroup is a cgroup model whose accounting is off.
type disabledCgroup struct{ cgroupOps }

func (disabledCgroup) Enabled() bool { return false }
