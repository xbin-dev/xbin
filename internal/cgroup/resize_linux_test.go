//go:build linux

package cgroup

import (
	"errors"
	"io/fs"
	"reflect"
	"testing"
)

// covers D203 — Resize on a directory that keeps cgroupfs's rules: a raise
// writes memory.max before memory.high, a cut memory.high first, so the soft
// ceiling never passes the hard one; memory.high is ⅞ of the cap as a leaf
// is made with, or l's own, or none; a zero cap lifts it; nothing else is
// written; a nested leaf (a deployment's) resizes like a flat one; an absent
// leaf, a node and a malformed name are errors; disabled, a no-op.
func TestResizeWriteOrder(t *testing.T) {
	m, f := newFakeCgroupfs(t, Limits{MemMax: 2 << 30, PidsMax: 512})
	m.Add("apps~x-1", 4242)
	nested := DeploymentLeaf("apps~x-1", "dev")
	m.AddLimited(nested, 4343, Limits{MemMax: 2 << 30, PidsMax: 64, NodeWeight: NonPrimaryWeight})
	if f.file("apps~x-1", "memory.max") != "2147483648" || f.file(nested, "memory.max") != "2147483648" {
		t.Fatalf("the leaves' caps: %q, %q", f.file("apps~x-1", "memory.max"), f.file(nested, "memory.max"))
	}

	cases := []struct {
		name string
		leaf string
		l    Limits
		want []string
	}{
		{"a raise: max, then high", "apps~x-1", Limits{MemMax: 3 << 30, PidsMax: 9},
			[]string{"comp-apps~x-1/memory.max=3221225472", "comp-apps~x-1/memory.high=2818572288"}},
		{"a cut: high, then max", "apps~x-1", Limits{MemMax: 1 << 30},
			[]string{"comp-apps~x-1/memory.high=939524096", "comp-apps~x-1/memory.max=1073741824"}},
		{"the same cap: as a raise", "apps~x-1", Limits{MemMax: 1 << 30},
			[]string{"comp-apps~x-1/memory.max=1073741824", "comp-apps~x-1/memory.high=939524096"}},
		{"l's own soft ceiling", "apps~x-1", Limits{MemMax: 2 << 30, MemHigh: 1 << 30},
			[]string{"comp-apps~x-1/memory.max=2147483648", "comp-apps~x-1/memory.high=1073741824"}},
		{"no soft ceiling", "apps~x-1", Limits{MemMax: 4 << 30, MemHigh: -1},
			[]string{"comp-apps~x-1/memory.max=4294967296", "comp-apps~x-1/memory.high=max"}},
		{"a zero cap lifts it", "apps~x-1", Limits{},
			[]string{"comp-apps~x-1/memory.max=max", "comp-apps~x-1/memory.high=max"}},
		{"from no cap to one: a cut", "apps~x-1", Limits{MemMax: 2 << 30},
			[]string{"comp-apps~x-1/memory.high=1879048192", "comp-apps~x-1/memory.max=2147483648"}},
		{"a deployment's nested leaf", nested, Limits{MemMax: 3 << 30, PidsMax: 64, NodeWeight: 50},
			[]string{nested + "/memory.max=3221225472", nested + "/memory.high=2818572288"}},
	}
	for _, c := range cases {
		f.writes = nil
		if err := m.Resize(c.leaf, c.l); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(f.writes, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, f.writes, c.want)
		}
	}
	if got := f.file(nested, "pids.max"); got != "64" {
		t.Errorf("Resize touched pids.max: %q", got)
	}

	f.writes = nil
	for _, bad := range []string{"apps~gone", DeploymentLeaf("apps~x-1", "gone"), TileNode("apps~x-1"), "", "..", "tile-/x"} {
		if err := m.Resize(bad, Limits{MemMax: 1 << 30}); err == nil {
			t.Errorf("Resize(%q) = nil", bad)
		}
	}
	if err := m.Resize("apps~gone", Limits{MemMax: 1 << 30}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an absent leaf: %v, want ErrNotExist", err)
	}
	if len(f.writes) != 0 {
		t.Errorf("refused resizes wrote %q", f.writes)
	}
	if err := (&Manager{}).Resize("apps~x-1", Limits{MemMax: 1}); err != nil {
		t.Errorf("disabled: %v", err)
	}
}

// covers D203 — OOMKills reads a nested leaf (a deployment's, a person's
// partition's), not only a flat one: memory.events oom_kill by the name rule
// every reader shares; a node, a malformed or an absent name reads 0.
func TestOOMKillsNestedLeaves(t *testing.T) {
	m, f := newFakeCgroupfs(t, Limits{MemMax: 2 << 30})
	m.Add("apps~x-1", 1)
	dep := DeploymentLeaf("apps~x-1", "dev")
	part := TileNode("apps~x-1") + "p-main-u0123/backend"
	m.AddLimited(dep, 2, Limits{MemMax: 1 << 30})
	m.AddLimited(part, 3, Limits{MemMax: 1 << 30})
	f.set(t, "apps~x-1", "memory.events", "low 0\nhigh 4\nmax 2\noom 1\noom_kill 1\n")
	f.set(t, dep, "memory.events", "low 0\nhigh 9\nmax 7\noom 3\noom_kill 3\noom_group_kill 0\n")
	f.set(t, part, "memory.events", "max 1\noom 1\noom_kill 2\n")
	for name, want := range map[string]int64{
		"apps~x-1": 1, dep: 3, part: 2,
		TileNode("apps~x-1") + "d-gone/backend": 0, "apps~gone": 0, "": 0, "tile-/x": 0, "../x": 0,
	} {
		if got := m.OOMKills(name); got != want {
			t.Errorf("OOMKills(%q) = %d, want %d", name, got, want)
		}
	}
	if (&Manager{}).OOMKills(dep) != 0 {
		t.Error("a disabled manager read a count")
	}
}
