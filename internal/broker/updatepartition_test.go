package broker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/builtins"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-52 — the builtin updater's stand-in for an installed manifest
// it can't read is the "partition" xbind last read from the tile's code:
// an open or declined request's mode, else the recorded one, none for a
// tile with no record; unknown when the record can't be read.
func TestRecordedPartition(t *testing.T) {
	b := testBroker(t)
	user := &registry.PartitionSpec{User: true}
	both := &registry.PartitionSpec{User: true, Global: true}
	b.parts.mu.Lock()
	b.parts.recs["apps/mode"] = &modeRecord{Tile: "apps/mode", Mode: user}
	b.parts.recs["apps/req"] = &modeRecord{Tile: "apps/req", Mode: user, Request: &modeRequest{Spec: nil}}
	b.parts.recs["apps/dec"] = &modeRecord{Tile: "apps/dec", Declined: &modeDeclined{Spec: both}}
	b.parts.unread[util.TileKey("apps/bad")] = "it doesn't parse"
	b.parts.mu.Unlock()
	for tile, want := range map[string]string{
		"apps/none": "none", "apps/mode": `["user"]`, "apps/req": "none",
		"apps/dec": `["user","global"]`, "apps/bad": "unknown",
	} {
		raw, has, ok := b.recordedPartition(tile)
		got := "unknown"
		switch {
		case ok && has:
			got = string(raw)
		case ok:
			got = "none"
		}
		if got != want {
			t.Errorf("%s: %s, want %s", tile, got, want)
		}
	}
	pm := b.parts
	b.parts = nil
	if _, _, ok := b.recordedPartition("apps/mode"); ok {
		t.Error("no mode store: a mode was told")
	}
	b.parts = pm
}

// SetUpdater wires the store in: a replace over an installed manifest that
// doesn't parse writes the recorded mode, never upstream's.
func TestUpdaterReadsRecordedPartition(t *testing.T) {
	b := testBroker(t)
	root := b.Reg.Root
	unit := func(partition string) fstest.MapFS {
		p := ""
		if partition != "" {
			p = `"partition": ` + partition + `, `
		}
		return fstest.MapFS{
			"tiles/x/xbin.json":  {Data: []byte(`{` + p + `"title": "x"}` + "\n")},
			"tiles/x/index.html": {Data: []byte("<html></html>\n")},
		}
	}
	if _, err := builtins.NewUpdater(root, nil, unit("")).ApplyReplace("scaffold:tiles/x"); err != nil {
		t.Fatal(err)
	}
	mp := filepath.Join(root, "tiles", "x", "xbin.json")
	if err := os.WriteFile(mp, []byte(`{"title": "x",,`), 0o644); err != nil {
		t.Fatal(err)
	}
	b.parts.mu.Lock()
	b.parts.recs["tiles/x"] = &modeRecord{Tile: "tiles/x", Mode: &registry.PartitionSpec{User: true}}
	b.parts.mu.Unlock()
	b.SetUpdater(builtins.NewUpdater(root, nil, unit(`["user", "global"]`)))
	a, err := b.updater.ApplyReplace("scaffold:tiles/x")
	if err != nil {
		t.Fatal(err)
	}
	if got := instancePartitionOf(t, root, "tiles/x"); got != `["user"]` {
		t.Errorf("partition = %q", got)
	}
	if len(a.Notes) != 1 || !strings.Contains(a.Notes[0], "as xbind last read it") {
		t.Errorf("notes = %q", a.Notes)
	}
}

// Two requests instantiating at one path: the one whose copy is written is
// the note the auto record reads, and neither removes the other's.
func TestInstantiatorNoteConcurrent(t *testing.T) {
	b := testBroker(t)
	key := b.Reg.Root + "\x00apps/t"
	by := func() string {
		if n, ok := instantiators.Load(key); ok {
			return n.(*instantiator).by
		}
		return ""
	}
	alice := b.noteInstantiator("apps/t", auth.Principal{UserID: "alice"})
	bob := b.noteInstantiator("apps/t", auth.Principal{UserID: "bob"})
	if by() != "alice" {
		t.Fatalf("the second note replaced the first: %q", by())
	}
	bob.created() // bob's copy was written; alice's fails (it exists)
	alice.done()
	if by() != "bob" {
		t.Fatalf("after alice's failed request: %q", by())
	}
	bob.done()
	if by() != "" {
		t.Fatalf("the note outlived both requests: %q", by())
	}
}

// Mode "pr" when upstream changed nothing but a manifest's partition: no PR
// (the series would be empty), the version recorded, and a note.
func TestBuiltinUpdatePROnlyPartition(t *testing.T) {
	b := testBroker(t)
	root := b.Reg.Root
	unit := func(p string) fstest.MapFS {
		return fstest.MapFS{
			"tiles/x/xbin.json":  {Data: []byte("{\n  \"runtime\": \"static\",\n" + p + "  \"title\": \"x\"\n}\n")},
			"tiles/x/index.html": {Data: []byte("<html></html>\n")},
		}
	}
	if _, err := builtins.NewUpdater(root, nil, unit("  \"partition\": [\"user\"],\n")).ApplyReplace("scaffold:tiles/x"); err != nil {
		t.Fatal(err)
	}
	b.SetUpdater(builtins.NewUpdater(root, nil, unit("")))
	w := call(t, b.apiBuiltinsUpdate, auth.Principal{Owner: true}, "POST", "/builtins/update", `{"id":"scaffold:tiles/x","mode":"pr"}`, nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out struct {
		PR    *prMeta  `json:"pr"`
		Files []string `json:"files"`
		Notes []string `json:"notes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.PR != nil || out.Files == nil || len(out.Notes) != 1 || !strings.Contains(out.Notes[0], `partition kept as installed (["user"]; upstream asks none)`) {
		t.Errorf("answer: %s", w.Body.String())
	}
	if got := instancePartitionOf(t, root, "tiles/x"); got != `["user"]` {
		t.Errorf("partition = %q", got)
	}
	ups, err := b.updater.Updates()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range ups {
		if u.ID == "scaffold:tiles/x" && u.HasUpdate {
			t.Errorf("still offered: %+v", u)
		}
	}
}
