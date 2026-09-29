package registry

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// covers PD-44 S13 — OnPartitionChange (plans/partitions/01 §6, 03 §A.10):
// every rescan tells the hook about each tile whose settled state or
// recorded mode moved — before the scan is published, so the runner stops
// what the new state doesn't cover first — and about a tile it dropped;
// an unchanged rescan, and a workspace where nothing asks, tell nothing.
func TestPartitionChangeHook(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"apps/a/xbin.json": `{"runtime":"go","partition":["user","global"]}`,
		"apps/z/xbin.json": `{"runtime":"go"}`,
	})
	mode := PartitionMode{State: PartitionPartitioned, Recorded: PartitionSpec{User: true, Global: true}}
	r := &Registry{Root: root, PartitionModes: func(a PartitionAsk) PartitionMode {
		if a.Requested == nil {
			return PartitionMode{}
		}
		return mode
	}}
	if err := r.Rescan(); err != nil {
		t.Fatal(err)
	}
	var seen []string
	var published []bool // the hook's component, as the registry answered it during the call
	r.OnPartitionChange(func(c *Component, old, new PartitionMode) {
		seen = append(seen, c.Path+" "+old.State.String()+"→"+new.State.String())
		pc, _ := r.Component(c.Path)
		published = append(published, pc == c)
	})
	rescan := func() []string {
		t.Helper()
		seen, published = nil, nil
		if err := r.Rescan(); err != nil {
			t.Fatal(err)
		}
		return seen
	}
	if got := rescan(); len(got) != 0 {
		t.Errorf("an unchanged rescan told %q", got)
	}
	mode = PartitionMode{State: PartitionPending, Recorded: mode.Recorded, Request: &PartitionRequest{}}
	if got, want := rescan(), []string{"apps/a partitioned→pending"}; !slices.Equal(got, want) {
		t.Errorf("pending: %q, want %q", got, want)
	}
	if slices.Contains(published, true) {
		t.Error("the hook ran after the scan was published")
	}
	mode = PartitionMode{State: PartitionPartitioned, Recorded: PartitionSpec{User: true}}
	if got, want := rescan(), []string{"apps/a pending→partitioned"}; !slices.Equal(got, want) {
		t.Errorf("keep/switch: %q, want %q", got, want)
	}
	mode = PartitionMode{State: PartitionPartitioned, Recorded: PartitionSpec{User: true, Global: true}}
	if got, want := rescan(), []string{"apps/a partitioned→partitioned"}; !slices.Equal(got, want) {
		t.Errorf("global added: %q, want %q", got, want)
	}
	if err := os.RemoveAll(filepath.Join(root, "apps/a")); err != nil {
		t.Fatal(err)
	}
	if got, want := rescan(), []string{"apps/a partitioned→unpartitioned"}; !slices.Equal(got, want) {
		t.Errorf("removed: %q, want %q", got, want)
	}
}
