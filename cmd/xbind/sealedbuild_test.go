package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `xbind sealed-build --check` is the release's gate (publish-release.sh):
// the bundle's bin/sealed must hold exactly the builds this source's table
// asks for — a missing one would ship a component that is held, a stray one
// is another source's.
func TestSealedBuildCheck(t *testing.T) {
	dir := t.TempDir()
	if err := sealedBuild([]string{"--check", "--out", dir}); err != nil {
		t.Fatalf("an empty bin/sealed for the compiled-in table: %v", err)
	}
	if err := sealedBuild([]string{"--check", "--out", filepath.Join(dir, "absent")}); err != nil {
		t.Fatalf("no bin/sealed and an empty table: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stale-0123456789abcdef01234567"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sealedBuild([]string{"--check", "--out", dir}); err == nil || !strings.Contains(err.Error(), "isn't a build of this source") {
		t.Fatalf("a stray build in bin/sealed: %v", err)
	}
	// building removes it
	if err := sealedBuild([]string{"--out", dir, "--sdk", "../../sdk"}); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("after a build bin/sealed holds %v", ents)
	}
}
