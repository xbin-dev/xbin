package deployments

import (
	"reflect"
	"testing"
)

// covers P5 PO-8 — AlwaysOnSwitched, the runner's alwaysOn switch hook
// (07-runtime §11), names the deployments beyond the primary whose switch is
// on, sorted; the primary's own switch never counts (its code alone keeps it
// up), and a tile without a record, or a literal that never booted, names
// none.
func TestAlwaysOnSwitched(t *testing.T) {
	const tile = "apps/crm"
	if got := (&Plane{}).AlwaysOnSwitched(tile); got != nil {
		t.Errorf("a literal plane names %v", got)
	}
	root := t.TempDir()
	if got := bootPlane(t, root, newOwners(map[string]string{})).AlwaysOnSwitched(tile); got != nil {
		t.Errorf("the zero state names %v", got)
	}
	doc := recordDoc(tile, "")
	doc["liveReload"], doc["lastLiveReload"] = "dev", "dev"
	doc["deployments"] = map[string]any{
		"main": map[string]any{"checkpoint": treeA, "alwaysOn": true},
		"qa":   map[string]any{"checkpoint": treeB, "alwaysOn": true},
		"dev":  map[string]any{"checkpoint": nil, "alwaysOn": true},
		"load": map[string]any{"checkpoint": treeB},
	}
	writeRecordDoc(t, root, tile, doc)
	p := bootPlane(t, root, newOwners(map[string]string{}))
	if f := p.Lookup(tile); f.State != RecordActive {
		t.Fatalf("the fixture record doesn't govern the tile: %+v", f)
	}
	if got, want := p.AlwaysOnSwitched(tile), []string{"dev", "qa"}; !reflect.DeepEqual(got, want) {
		t.Errorf("AlwaysOnSwitched = %v; want %v", got, want)
	}
	if got := p.AlwaysOnSwitched("apps/other"); got != nil {
		t.Errorf("another tile names %v", got)
	}
}
