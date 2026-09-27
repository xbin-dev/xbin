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
