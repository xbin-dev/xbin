//go:build linux && integration

package isolated

// partitions_page_test.go — the partitions page on a real isolated xbind
// (PD-47 06§12.1; docs/partitions.md §Your partitions page): served as it
// ships, top-level only, and every read and act it makes answers a person
// with their own running partition.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// covers PD-47 06§12.1 — alice's partition of apps/pt runs (isolated); the
// page is web/partitions.html byte for byte with its framing headers; the
// reads it makes (whoami, the overview, components, consents, binds, the
// ledger, the tile's detail) answer alice 200 with her own running row and
// the trust panel; the owner token's consents are refused (a person's own:
// the page says so); the Stop it sends stops her instance, and the next
// request starts it again.
func TestPartitionsPage(t *testing.T) {
	t.Parallel()
	e := psSetup(t)
	d := e.d
	psWrite(t, d, psTile, `["user", "global"]`)
	e.waitState(t, psTile, "partitioned")
	boot := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice"))
	if boot.Partition != "user:alice" {
		t.Fatalf("alice's frame reaches %q, want her partition", boot.Partition)
	}

	want, err := os.ReadFile(filepath.Join("..", "..", "web", "partitions.html"))
	if err != nil {
		t.Fatal(err)
	}
	accept := xbindtest.H("Accept", "text/html")
	page := d.Call(t, "GET", "/xbin/partitions", nil, append(e.as("alice"), accept)...)
	if page.Status != 200 || string(page.Body) != string(want) {
		t.Fatalf("GET /xbin/partitions: %d, the shipped page byte for byte: %v", page.Status, string(page.Body) == string(want))
	}
	if h := page.Header; h.Get("X-Frame-Options") != "DENY" || !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Errorf("the page can be framed: %v", h)
	}
	if r := d.Call(t, "GET", "/vendor/partitions.html", nil); r.Status != 404 {
		t.Errorf("/vendor/partitions.html: %d, want 404", r.Status)
	}

	// the page's reads, as alice
	for _, p := range []string{"/api/xbin/whoami", "/api/xbin/components", "/api/xbin/partitions/consents",
		"/api/xbin/partitions/binds", "/api/xbin/partitions/ledger?days=30"} {
		if r := d.Call(t, "GET", p, nil, e.as("alice")...); r.Status != 200 {
			t.Errorf("alice: GET %s: %d %s", p, r.Status, r.Body)
		}
	}
	type ovTile struct {
		Tile string `json:"tile"`
		Mine *struct {
			Partition string `json:"partition"`
		} `json:"mine"`
	}
	var ov struct {
		Features []string `json:"features"`
		Tiles    []ovTile `json:"tiles"`
	}
	d.Must(t, "GET", "/api/xbin/partitions", nil, 200, e.as("alice")...).Decode(t, &ov)
	if !slices.Contains(ov.Features, "partitions-page/1") {
		t.Errorf("features %q without partitions-page/1", ov.Features)
	}
	if i := slices.IndexFunc(ov.Tiles, func(x ovTile) bool { return x.Tile == psTile }); i < 0 || ov.Tiles[i].Mine == nil || ov.Tiles[i].Mine.Partition != "user:alice" {
		t.Errorf("the overview lacks alice's partition of %s: %+v", psTile, ov.Tiles)
	}
	type detail struct {
		Partitions []struct {
			User    string `json:"user"`
			Running bool   `json:"running"`
		} `json:"partitions"`
		Trust json.RawMessage `json:"trust"`
	}
	read := func() detail {
		var out detail
		d.Must(t, "GET", "/api/xbin/partitions?tile="+psTile, nil, 200, e.as("alice")...).Decode(t, &out)
		return out
	}
	if got := read(); len(got.Partitions) != 1 || got.Partitions[0].User != "alice" || !got.Partitions[0].Running || len(got.Trust) == 0 {
		t.Errorf("alice's detail of %s: %+v", psTile, got)
	}
	// the root token is no person: the page shows the consents' refusal
	if r := d.Call(t, "GET", "/api/xbin/partitions/consents", nil); r.Status != 403 {
		t.Errorf("the owner token's consents: %d, want 403", r.Status)
	}

	// Stop, as the page sends it
	d.Must(t, "POST", "/api/xbin/partitions/stop", map[string]string{"tile": psTile, "partition": "user:alice"}, 200, e.as("alice")...)
	xbindtest.Eventually(t, 30*time.Second, "alice's instance stopped", func() (bool, string) {
		got := read()
		return len(got.Partitions) == 1 && !got.Partitions[0].Running, fmt.Sprintf("%+v", got.Partitions)
	})
	if again := e.who(t, "/api/"+psTile+"/who", e.fr(t, psTile, "alice")); again.Partition != "user:alice" || again.Boot == boot.Boot {
		t.Errorf("after the stop, alice's next request: %+v (boot %s before)", again, boot.Boot)
	}
}
