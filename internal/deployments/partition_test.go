package deployments

import (
	"net/http"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
)

// covers PD-17 — POST /deployments/primary is refused (409) for a tile
// whose recorded partition mode has user partitions, dry run included: the
// people's data stays keyed by the old primary. A tile without partitions
// reassigns as today.
func TestPrimaryRefusedWhenPartitioned(t *testing.T) {
	f := newGovFx(t, false)
	f.write("xbin.json", `{"schema":1}`)
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"})
	r0 := f.rec(opSite)
	recorded := registry.PartitionMode{State: registry.PartitionPartitioned, Recorded: registry.PartitionSpec{User: true}}
	f.p.Reg.PartitionModes = func(a registry.PartitionAsk) registry.PartitionMode {
		if a.Tile == opSite {
			return recorded
		}
		return registry.PartitionMode{}
	}
	if err := f.p.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	want := opSite + " is partitioned: switching the primary would leave every person's data with main: promote instead"
	for _, dry := range []bool{true, false} {
		_, err := f.do(ownerP, OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, DryRun: dry, Seq: ptr(r0.Seq)})
		wantErr(t, "reassigning a partitioned tile's primary", err, http.StatusConflict, want)
	}
	// Pending (or declined) is still partitioned data.
	recorded.State = registry.PartitionPending
	if err := f.p.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	_, err := f.do(ownerP, OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, Seq: ptr(r0.Seq)})
	wantErr(t, "reassigning a pending partitioned tile's primary", err, http.StatusConflict, want)
	if f.rec(opSite).Primary != "main" {
		t.Fatal("a refused reassignment moved the primary")
	}
	// Unpartitioned: today's reassignment.
	f.p.Reg.PartitionModes = nil
	if err := f.p.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.do(ownerP, OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, Seq: ptr(r0.Seq)}); err != nil {
		t.Fatalf("an unpartitioned tile's reassignment: %v", err)
	}
	if f.rec(opSite).Primary != "dev" {
		t.Error("the unpartitioned reassignment didn't move the primary")
	}
}
