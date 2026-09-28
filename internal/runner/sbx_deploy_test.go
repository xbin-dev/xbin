package runner

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sbx"
)

// The storage names below are hand-maintained literals (sha256sum), never
// computed by the code under test: CompKey("apps/crm") and
// TileKey("apps/crm").
const (
	sbxTile    = "apps/crm"
	sbxKey     = "apps~crm-f7c24b7f"
	sbxTileKey = "5d01689965d746c344a7f12d46b8a671"
)

// sbxJSON is an entry or failure as the wire carries it, its clock zeroed.
func sbxJSON(t *testing.T, v any) string {
	t.Helper()
	switch x := v.(type) {
	case sbx.Entry:
		x.Started = time.Time{}
		v = x
	case sbx.Failure:
		x.Time = time.Time{}
		v = x
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// covers P13 P17 PO-11 T18 — TestSandboxEntriesPerDeployment (=
// TestRegistryRowsPerDeployment, 15-test-plan): main's registry rows keep
// backend:<CompKey>:g<gen> with no deployment field, byte for byte, even on
// a tile with a record whose main runs a checkpoint; another deployment's
// entry is backend+<name>:<CompKey>:g<gen>, under the tile's path, naming
// the deployment, so the two never collide at one generation number; each
// generation is listed in its own leaf (the flat leaf while main runs alone,
// the deployment's leaf under the tile's parent otherwise); Filter narrows
// by deployment; failures name the deployment, never coalesce with main's,
// and point at its own log; and the name rule holds after a primary
// reassignment: IDs follow the deployment's name, not its role.
func TestSandboxEntriesPerDeployment(t *testing.T) {
	reg := sbx.New()
	primary := "main"
	r := &Runner{Sandboxes: reg, Isolate: true}
	r.Primary = func(string) string { return primary }
	goRT := registry.Manifest{Runtime: "go"}
	// the tile has a record: main runs a checkpoint (the primary's view), dev another
	mainView := &registry.Component{Path: sbxTile, Manifest: goRT, CodeRoot: "/ws/.xbin/deploy/" + sbxTileKey + "/aaaa"}
	devView := &registry.Component{Path: sbxTile, Manifest: goRT, Deployment: "dev", CodeRoot: "/ws/.xbin/deploy/" + sbxTileKey + "/bbbb"}

	rmMain := r.sbxAdd(mainView, 3, "/run/"+sbxKey+"/g3.sock", 101)
	rmDev := r.sbxAdd(devView, 3, "/run/d-0123456789abcdef/g3.sock", 202)
	all := reg.List(sbx.Filter{Tile: sbxTile})
	if len(all) != 2 {
		t.Fatalf("main and dev at g3 are two entries: %+v", all)
	}
	byID := map[string]sbx.Entry{}
	for _, e := range all {
		byID[e.ID] = e
	}
	// main: exactly the row the runner listed before deployments (no leaf:
	// no cgroup accounting here)
	if got, want := sbxJSON(t, byID["backend:"+sbxKey+":g3"]),
		`{"id":"backend:apps~crm-f7c24b7f:g3","kind":"backend","tile":"apps/crm","mode":"namespace","pid":101,"gen":3,"started":"0001-01-01T00:00:00Z"}`; got != want {
		t.Errorf("main's entry:\n%s\nwant\n%s", got, want)
	}
	if got, want := sbxJSON(t, byID["backend+dev:"+sbxKey+":g3"]),
		`{"id":"backend+dev:apps~crm-f7c24b7f:g3","kind":"backend","tile":"apps/crm","deployment":"dev","mode":"namespace","pid":202,"gen":3,"started":"0001-01-01T00:00:00Z"}`; got != want {
		t.Errorf("dev's entry:\n%s\nwant\n%s", got, want)
	}
	if l := reg.List(sbx.Filter{Deployment: "main"}); len(l) != 1 || l[0].Deployment != "" {
		t.Errorf("Filter main: %+v", l)
	}
	if l := reg.List(sbx.Filter{Tile: sbxTile, Deployment: "dev"}); len(l) != 1 || l[0].ID != "backend+dev:"+sbxKey+":g3" {
		t.Errorf("Filter dev: %+v", l)
	}
	rmDev()
	if l := reg.List(sbx.Filter{}); len(l) != 1 || l[0].Deployment != "" {
		t.Errorf("dev's remove took main's entry: %+v", l)
	}
	rmMain()

	// the leaf is the generation's own
	for _, c := range []struct {
		dep    string
		nested bool
		want   string
	}{
		{"main", false, sbxKey}, // zero-state and main-only tiles: the flat leaf
		{"main", true, "tile-" + sbxKey + "/d-main/backend"},
		{"dev", false, "tile-" + sbxKey + "/d-dev/backend"},
		{"dev", true, "tile-" + sbxKey + "/d-dev/backend"},
	} {
		if got := leafFor(sbxKey, c.dep, c.nested); got != c.want {
			t.Errorf("leafFor(%s, nested %v) = %q, want %q", c.dep, c.nested, got, c.want)
		}
	}
	if l := r.genLeaf(devView, "dev"); l != "" {
		t.Errorf("without cgroup accounting a generation has no leaf, got %q", l)
	}
	defer r.sbxAddLeaf(mainView, 4, "/run/"+sbxKey+"/g4.sock", 103, sbxKey)()
	defer r.sbxAddLeaf(devView, 8, "/run/d-0123456789abcdef/g8.sock", 204, "tile-"+sbxKey+"/d-dev/backend")()
	if e, _ := reg.Get("backend:" + sbxKey + ":g4"); e.Leaf != sbxKey || e.Deployment != "" {
		t.Errorf("main's generation in the flat leaf: %+v", e)
	}
	if e, _ := reg.Get("backend+dev:" + sbxKey + ":g8"); e.Leaf != "tile-"+sbxKey+"/d-dev/backend" || e.Deployment != "dev" {
		t.Errorf("dev's generation in its own leaf: %+v", e)
	}

	// failures carry the deployment and coalesce per deployment
	err := errors.New("the rootfs is missing")
	r.sbxFail(devView, sbx.Start, err)
	r.sbxFail(mainView, sbx.Start, err)
	r.sbxFail(devView, sbx.Start, sbx.Refuse(errors.New("12 non-primary backends run")))
	f := reg.Failures(sbx.Filter{Tile: sbxTile})
	if len(f) != 3 || f[0].Deployment != "dev" || f[0].Stage != sbx.Refused || f[1].Deployment != "" || f[2].Deployment != "dev" {
		t.Fatalf("failures: %+v", f)
	}
	if got, want := sbxJSON(t, f[1]),
		`{"time":"0001-01-01T00:00:00Z","kind":"backend","tile":"apps/crm","mode":"namespace","stage":"start","error":"the rootfs is missing","count":1}`; got != want {
		t.Errorf("main's failure:\n%s\nwant\n%s", got, want)
	}
	if f := reg.Failures(sbx.Filter{Deployment: "dev"}); len(f) != 2 {
		t.Errorf("dev's failures: %+v", f)
	}
	exit127 := func() *os.ProcessState {
		cmd := exec.Command("sh", "-c", "exit 127") // exec-ok: test
		_ = cmd.Run()
		return cmd.ProcessState
	}
	r.sbxExited(devView, sbx.Namespace, exit127(), time.Now())
	r.sbxExited(mainView, sbx.Namespace, exit127(), time.Now())
	f = reg.Failures(sbx.Filter{Tile: sbxTile})
	if !strings.HasSuffix(f[1].Error, "see .xbin/deploy/"+sbxTileKey+"/d/dev/backend.log") || f[1].Deployment != "dev" {
		t.Errorf("dev's exit names its own log: %+v", f[1])
	}
	if want := "the sandbox couldn't start the backend (127) — see .xbin/log/" + sbxKey + ".log"; f[0].Error != want || f[0].Deployment != "" {
		t.Errorf("main's exit: %q, want today's %q", f[0].Error, want)
	}

	// the name rule after a reassignment: dev is the primary (its view is the
	// registry's own component), main a non-primary deployment
	primary = "dev"
	tileComp := &registry.Component{Path: sbxTile, Manifest: goRT}
	mainAside := &registry.Component{Path: sbxTile, Manifest: goRT, Deployment: "main"}
	defer r.sbxAdd(tileComp, 9, "/run/d-0123456789abcdef/g9.sock", 205)()
	defer r.sbxAdd(mainAside, 5, "/run/"+sbxKey+"/g5.sock", 106)()
	if e, ok := reg.Get("backend+dev:" + sbxKey + ":g9"); !ok || e.Deployment != "dev" {
		t.Errorf("the primary dev keeps dev's ID: %+v", e)
	}
	if e, ok := reg.Get("backend:" + sbxKey + ":g5"); !ok || e.Deployment != "" {
		t.Errorf("main, no longer the primary, keeps main's ID: %+v", e)
	}
}

// covers P13 T18 — GenUsage reads a deployment's generations as one group:
// the leaf they share once where it has accounting, else the sum of each
// generation's process tree; a group nothing answers is absent.
func TestGenUsageGroups(t *testing.T) {
	r := &Runner{}
	pid := os.Getpid()
	es := []sbx.Entry{
		{ID: "backend:" + sbxKey + ":g1", Tile: sbxTile, PID: pid},
		{ID: "backend+dev:" + sbxKey + ":g7", Tile: sbxTile, Deployment: "dev", PID: pid, Leaf: "tile-" + sbxKey + "/d-dev/backend"},
		{ID: "backend+dev:" + sbxKey + ":g8", Tile: sbxTile, Deployment: "dev", PID: pid, Leaf: "tile-" + sbxKey + "/d-dev/backend"},
		{ID: "backend+qa:" + sbxKey + ":g1", Tile: sbxTile, Deployment: "qa"},
	}
	u := r.GenUsage(es, func(e sbx.Entry) string { return e.Deployment })
	if _, ok := u["qa"]; ok || len(u) != 2 {
		t.Fatalf("groups: %+v", u)
	}
	one, two := u[""], u["dev"]
	if one.MemCurrent < 1<<20 || one.PidsCurrent < 1 || one.MemMax != -1 {
		t.Errorf("main's process tree: %+v", one)
	}
	// no cgroup here: dev's leaf can't be read, so both generations' trees add
	if two.PidsCurrent < 2*one.PidsCurrent-2 || two.MemCurrent < one.MemCurrent {
		t.Errorf("dev's two generations: %+v (one tree %+v)", two, one)
	}
}
