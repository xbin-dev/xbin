package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/sealed"
)

// `xbind sealed-build --check` is the release's gate (publish-release.sh):
// the bundle's bin/sealed must hold exactly the builds this source's table
// asks for, each the file its manifest lists — a missing one would ship a
// component that is held, a stray one is another source's, a changed one is
// not what was built. The table is the compiled-in one: empty in a release
// build, the fixture's under -tags sealedfixture, and the test holds for
// both.
func TestSealedBuildCheck(t *testing.T) {
	set, err := sealed.Load(sealed.Table())
	if err != nil {
		t.Fatal(err)
	}
	want := set.BuildNames()
	dir := t.TempDir()
	check := func(out string) error { return sealedBuild([]string{"--check", "--out", out, "--sdk", "../../sdk"}) }
	switch err := check(dir); {
	case len(want) == 0 && err != nil:
		t.Fatalf("an empty bin/sealed for an empty table: %v", err)
	case len(want) > 0 && (err == nil || !strings.Contains(err.Error(), "has no ")):
		t.Fatalf("an empty bin/sealed for a table with %v: %v", want, err)
	}
	if err := check(filepath.Join(dir, "absent")); (err == nil) != (len(want) == 0) {
		t.Fatalf("no bin/sealed, a table with %d backend(s): %v", len(want), err)
	}
	// building makes it what the check wants, and removes a stray file
	stray := filepath.Join(dir, "stale-0123456789abcdef01234567")
	if err := os.WriteFile(stray, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		if err := check(dir); err == nil || !strings.Contains(err.Error(), "isn't a build of this source") {
			t.Fatalf("a stray build in bin/sealed: %v", err)
		}
	}
	if testing.Short() && len(want) > 0 {
		t.Skip("builds the fixture's backends")
	}
	if err := sealedBuild([]string{"--out", dir, "--sdk", "../../sdk"}); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	holds := len(want)
	if holds > 0 {
		holds++ // and sealed.json, which lists them
	}
	if len(ents) != holds {
		t.Errorf("after a build bin/sealed holds %v, want this table's %d build(s)", ents, len(want))
	}
	if err := check(dir); err != nil {
		t.Fatalf("a fresh build: %v", err)
	}
	if len(want) == 0 {
		return
	}
	// a stray file, and a build that is no longer the file the manifest lists
	if err := os.WriteFile(stray, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := check(dir); err == nil || !strings.Contains(err.Error(), "isn't a build of this source") {
		t.Fatalf("a stray build in bin/sealed: %v", err)
	}
	_ = os.Remove(stray)
	bin := filepath.Join(dir, want[0])
	b, _ := os.ReadFile(bin)
	if err := os.WriteFile(bin, append(b, 0), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := check(dir); err == nil || !strings.Contains(err.Error(), "SHA-256 differs") {
		t.Fatalf("a changed build: %v", err)
	}
}
