package boot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/internal/layers"
)

// The boot's GC passes keep the union of the layer stamps and the
// tile-sandbox definitions' bases; a pin that can't be read makes the set
// unknown (nil), which releases nothing.
func TestPinnedBases(t *testing.T) {
	ws := t.TempDir()
	sb := filepath.Join(ws, ".xbin", "sbx", "apps~m-1", "web")
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
	st.sandboxBasePins = func() []string { return []string{"b-def"} }
	if p := st.pinnedBases(); len(p) != 2 || !p["b-def"] {
		t.Fatalf("with definitions: %v", p)
	}
	os.Remove(filepath.Join(sb, layers.BaseFile))
	os.Symlink("/etc/hostname", filepath.Join(sb, layers.BaseFile))
	if p := st.pinnedBases(); p != nil {
		t.Fatalf("an unreadable pin must make the set unknown: %v", p)
	}
}
