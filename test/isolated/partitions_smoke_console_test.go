//go:build linux && integration

package isolated

// partitions_smoke_console_test.go — the partitions smoke's admin-console
// case (F12, plans/partitions 06 §12.3): the admin tile's view of
// partitioned tiles, on a real isolated xbind. Called from the smoke's ops
// case (partitions_smoke_test.go).

import (
	"testing"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// psConsole — a tile holding xbin:admin (the admin console) whose frame an
// admin's login minted reads GET /partitions as that admin: the overview's
// totals and isolation, the fixture's people's metadata rows and its mode
// history; a partitioned tile's own frame still reads the tile-level fields
// only.
func psConsole(t *testing.T, d *xbindtest.Daemon, e *psEnv) {
	t.Helper()
	const console = "apps/pconsole"
	d.WriteTile(t, console, map[string]string{
		"index.html": "<!doctype html><p>an admin console</p>\n",
		"xbin.json":  `{"uses":[{"target":"xbin","role":"admin"}]}`,
	})
	d.Must(t, "POST", "/api/xbin/grants", map[string]string{"from": console, "target": "xbin", "role": "admin"}, 200)
	frame := e.frame(t, console, "carol")

	var ov struct {
		Tiles []struct {
			Tile   string
			Totals map[string]int64
			Mine   any
		}
		Isolated *bool
		Orphans  []any
		Notices  any
	}
	d.Must(t, "GET", "/api/xbin/partitions", nil, 200, frame).Decode(t, &ov)
	found := false
	for _, r := range ov.Tiles {
		if r.Tile == psTile {
			found = r.Totals != nil && r.Totals["people"] >= 2 && r.Mine == nil
		}
	}
	if !found || ov.Isolated == nil || !*ov.Isolated || ov.Orphans == nil || ov.Notices != nil {
		t.Errorf("the admin console's overview: %+v", ov)
	}

	var one struct {
		History    []struct{ Op string }
		Partitions []struct{ User string }
		Orphans    []any
	}
	d.Must(t, "GET", "/api/xbin/partitions?tile="+psTile, nil, 200, frame).Decode(t, &one)
	if len(one.History) == 0 || len(one.Partitions) < 2 || one.Orphans == nil {
		t.Errorf("the admin console's view of %s: %+v", psTile, one)
	}

	var own struct {
		Tiles []map[string]any
	}
	d.Must(t, "GET", "/api/xbin/partitions", nil, 200, e.fr(t, psTile, "alice")).Decode(t, &own)
	for _, r := range own.Tiles {
		if r["totals"] != nil || r["mine"] != nil {
			t.Errorf("BUG: a partitioned tile's own frame reads the admins' overview: %v", r)
		}
	}

	// the same console driven by bob (no admin): the tile-level fields only,
	// and no admin act — the tile's grant opens the console, the person decides
	bobs := e.frame(t, console, "bob")
	var nov map[string]any
	d.Must(t, "GET", "/api/xbin/partitions", nil, 200, bobs).Decode(t, &nov)
	tiles, _ := nov["tiles"].([]any)
	for _, r := range tiles {
		if row, _ := r.(map[string]any); row["totals"] != nil || row["mine"] != nil {
			t.Errorf("BUG: the console driven by bob reads the admins' overview: %v", row)
		}
	}
	if nov["orphans"] != nil || nov["isolated"] != nil {
		t.Errorf("BUG: the console driven by bob reads the admins' fields: %v", nov)
	}
	var none map[string]any
	d.Must(t, "GET", "/api/xbin/partitions?tile="+psTile, nil, 200, bobs).Decode(t, &none)
	if none["partitions"] != nil || none["history"] != nil || none["orphans"] != nil {
		t.Errorf("BUG: the console driven by bob reads people's rows of %s: %v", psTile, none)
	}
	d.Must(t, "POST", "/api/xbin/partitions/limits", map[string]any{"tile": psTile, "maxRunning": 3}, 403, bobs)
	d.Must(t, "GET", "/api/xbin/partitions/binds", nil, 403, bobs)
}
