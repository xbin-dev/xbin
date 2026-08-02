package builtins

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func backfillFixture(t *testing.T) (*Updater, string, string) {
	t.Helper()
	root := t.TempDir()
	scaffold := fstest.MapFS{
		"tiles/organisations/xbin.json":        {Data: []byte(`{"title":"Organisations"}`)},
		"tiles/organisations/index.html":       {Data: []byte(`<html>orgs</html>`)},
		"tiles/organisations/organisations.js": {Data: []byte(`// js`)},
	}
	u := NewUpdater(root, nil, scaffold)
	return u, root, filepath.Join(root, "data", "backfills.json")
}

// Ledger semantics (essential-builtin backfill): absent+no-ledger →
// installed; absent+ledger → left alone (a deliberate delete sticks);
// present → untouched but ledgered.
func TestBackfillEssentials(t *testing.T) {
	u, root, ledger := backfillFixture(t)
	tile := filepath.Join(root, "tiles", "organisations")

	// Absent, no ledger → installed + ledgered.
	installed, err := u.BackfillEssentials(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 1 || installed[0] != "tiles/organisations" {
		t.Fatalf("installed = %v", installed)
	}
	if _, err := os.Stat(filepath.Join(tile, "xbin.json")); err != nil {
		t.Fatal("tile files must exist after backfill")
	}
	if _, err := os.Stat(ledger); err != nil {
		t.Fatal("ledger must be written")
	}

	// Deliberate delete + restart → stays deleted.
	if err := os.RemoveAll(tile); err != nil {
		t.Fatal(err)
	}
	installed, err = u.BackfillEssentials(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 0 {
		t.Fatalf("ledgered unit must not reinstall: %v", installed)
	}
	if _, err := os.Stat(tile); !os.IsNotExist(err) {
		t.Fatal("deliberately deleted tile must stay gone")
	}

	// Present on first sight → untouched (local edits survive) but ledgered,
	// so a LATER delete also sticks.
	u2, root2, ledger2 := backfillFixture(t)
	tile2 := filepath.Join(root2, "tiles", "organisations")
	if err := os.MkdirAll(tile2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tile2, "xbin.json"), []byte(`{"title":"customized"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := u2.BackfillEssentials(ledger2); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(tile2, "xbin.json"))
	if string(b) != `{"title":"customized"}` {
		t.Fatal("present unit must not be touched")
	}
	if err := os.RemoveAll(tile2); err != nil {
		t.Fatal(err)
	}
	if _, err := u2.BackfillEssentials(ledger2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tile2); !os.IsNotExist(err) {
		t.Fatal("post-ledger delete must stick")
	}
}

// Updates() lists an absent essential as MISSING (installable), and stops
// once it's installed.
func TestUpdatesListsMissingEssential(t *testing.T) {
	u, _, ledger := backfillFixture(t)
	ups, err := u.Updates()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, up := range ups {
		if up.ID == "scaffold:tiles/organisations" {
			found = true
			if !up.Missing || !up.HasUpdate {
				t.Fatalf("must be Missing+HasUpdate: %+v", up)
			}
		}
	}
	if !found {
		t.Fatal("missing essential must be listed")
	}
	// Bare-name resolution installs it.
	if id := u.ResolveID("tiles/organisations"); id != "scaffold:tiles/organisations" {
		t.Fatalf("ResolveID = %q", id)
	}
	if _, err := u.BackfillEssentials(ledger); err != nil {
		t.Fatal(err)
	}
	ups, _ = u.Updates()
	for _, up := range ups {
		if up.ID == "scaffold:tiles/organisations" && up.Missing {
			t.Fatal("installed unit must not list as missing")
		}
	}
}
