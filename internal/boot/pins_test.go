package boot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/internal/layers"
)

// The boot's GC passes keep the union of the layer stamps (a tile
// sandbox's in its cur/) and the tile-sandbox definitions' bases; a pin that
// can't be read — a stamp, or the definitions — makes the set unknown
// (nil), which releases nothing.
func TestPinnedBases(t *testing.T) {
	ws := t.TempDir()
	sb := filepath.Join(ws, ".xbin", "sbx", "apps~m-1", "web.0123456789ab", layers.CurDir)
	if err := os.MkdirAll(sb, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := layers.Stamp(sb, layers.Stamps{Base: "b-sbx"}); err != nil {
		t.Fatal(err)
	}
	st := &State{WS: ws}
	if p := st.pinnedBases(); len(p) != 1 || !p["b-sbx"] {
		t.Fatalf("stamps only: %v", p)
	}
	st.sandboxBasePins = func() ([]string, error) { return []string{"b-def"}, nil }
	if p := st.pinnedBases(); len(p) != 2 || !p["b-def"] {
		t.Fatalf("with definitions: %v", p)
	}
	st.sandboxBasePins = func() ([]string, error) { return nil, errors.New("data/sandboxes.json: unexpected EOF") }
	if p := st.pinnedBases(); p != nil {
		t.Fatalf("unreadable definitions must make the set unknown: %v", p)
	}
	st.sandboxBasePins = nil
	os.Remove(filepath.Join(sb, layers.BaseFile))
	os.Symlink("/etc/hostname", filepath.Join(sb, layers.BaseFile))
	if p := st.pinnedBases(); p != nil {
		t.Fatalf("an unreadable pin must make the set unknown: %v", p)
	}
}

// A boot keeps a base only a tile-sandbox definition pins through
// stepIsolation's GC: stepWorkspace sets the pin source, which reads
// data/sandboxes.json itself (the runtime isn't built yet).
func TestDefinitionPinsSurviveTheBootGC(t *testing.T) {
	dir := t.TempDir()
	ws, rootfs := filepath.Join(dir, "ws"), filepath.Join(dir, "rootfs")
	old := rootfs + "-b-old"
	for _, d := range []struct{ dir, ver string }{{rootfs, "b-new"}, {old, "b-old"}, {rootfs + "-b-stale", "b-stale"}} {
		if err := os.MkdirAll(filepath.Join(d.dir, "etc"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d.dir, layers.VersionFile), []byte(d.ver+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(ws, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "data", "sandboxes.json"), []byte(`{"version":1,"tiles":{"apps/mgr":{"sandboxes":{
		"img-base":{"name":"img-base","uid":"0123456789ab","mode":"namespace","base":"b-old"}}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st := &State{WS: ws}
	if err := st.stepWorkspace(); err != nil {
		t.Fatal(err)
	}
	layers.GC(rootfs, st.pinnedBases()) // what stepIsolation runs
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("the base a definition pins was released: %v", err)
	}
	if _, err := os.Stat(rootfs + "-b-stale"); !os.IsNotExist(err) {
		t.Fatalf("an unpinned base was kept: %v", err)
	}
}
