package runner

import (
	"reflect"
	"strings"
	"testing"
)

// covers D166 G1 PD-17 (LAND) — the Go build versions alert counts tiles,
// never instances: a partitioned tile (its people's partitions and its
// global instance all run its one build) is one baseline entry, one line
// of the report and one line of the alert, and builds of its partitions —
// views of the tile, keyed by its path — re-check that one entry.
func TestGoVersionsPartitionedTile(t *testing.T) {
	g, w, root := goVersionsFixture(t, map[string][]modVer{
		"shared:apps/a": {{Path: "a"}, {Path: "example.com/dep", Version: "v1.2.0"}},
		"own:apps/a":    {{Path: "a"}, {Path: "example.com/dep", Version: "v1.0.0"}},
		"shared:apps/b": {{Path: "b"}, {Path: "example.com/dep", Version: "v1.2.0"}},
		"own:apps/b":    {{Path: "b"}, {Path: "example.com/dep", Version: "v1.2.0"}},
	})
	writeWS(t, root, "apps/a/xbin.json", `{"runtime":"go","partition":["user","global"]}`)
	if err := g.Run.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	c, ok := g.Run.Reg.Component("apps/a")
	if !ok || c.Manifest.Partition == nil {
		t.Fatalf("apps/a isn't a partitioned tile here: %+v", c)
	}
	upgraded(t, g, root)
	if st := readGoVersionsState(t, g); !reflect.DeepEqual(st.Baseline, map[string]*goVersionsBase{"apps/a": nil, "apps/b": nil}) {
		t.Fatalf("the baseline set: %+v", st.Baseline)
	}
	runPass(g, true)
	rep := g.Report()
	if len(rep.Tiles) != 1 || rep.Tiles[0].Tile != "apps/a" {
		t.Fatalf("the report: %+v", rep)
	}
	if msg, ok := g.Alert(); !ok || !strings.HasPrefix(msg, "apps/a builds") || strings.Count(msg, "builds with older") != 1 {
		t.Errorf("the alert: %q", msg)
	}
	// what a partition's build reports: its view's path, the tile's
	pv := partitionView(c, "user:alice", fakePkey("user:alice"))
	if pv.Path != "apps/a" {
		t.Fatalf("a partition's view is %q", pv.Path)
	}
	_, n := w.took(0)
	w.set("own:apps/a", []modVer{{Path: "a"}, {Path: "example.com/dep", Version: "v1.2.0"}})
	writeWS(t, root, "apps/a/go.mod", "module a\n\ngo 1.22\n\nrequire example.com/dep v1.2.0\n")
	g.Built(pv.Path)
	waitIdle(t, g, "apps/a")
	if calls, _ := w.took(n); len(calls) != 1 || calls[0] != "own:apps/a" {
		t.Errorf("a partition's build re-checked %q, want the tile's own list once", calls)
	}
	if rep := g.Report(); len(rep.Tiles) != 0 {
		t.Errorf("after the tile caught up: %+v", rep)
	}
}
