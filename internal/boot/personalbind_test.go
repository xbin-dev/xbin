package boot

import "testing"

// covers PD-54 — the merge guard of the personal binds' restart (05 §3):
// boot installs the runner's StopPartition as the broker's restart of one
// person's partition instance (wirePartitionRunner). The broker's tests use
// a fake; a merge that drops the line would leave a person's running
// partition with a stale XBIN_IFACE_<SLOT> (a new bind missing, a removed
// one still listed) until it next starts.
func TestPersonalBindRestartWired(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	d := zsBoot(t, zsWorkspace(t))
	if !d.st.Broker.PartitionRestartWired() {
		t.Error("the broker has no partition restart: wirePartitionRunner must call SetPartitionRestart(run.StopPartition) (plans/partitions/records/F15.md)")
	}
}

// covers D246 — an access line's power rule asks the runner which tiles a
// tile's build takes: boot wires it, or a power tile that builds another
// tile's code would go unseen.
func TestBuildsWithWired(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	d := zsBoot(t, zsWorkspace(t))
	if d.st.Broker.BuildsWith == nil {
		t.Error("the broker has no BuildsWith: boot must set brk.BuildsWith = run.BuildsWith (accessreach.go)")
	}
}
