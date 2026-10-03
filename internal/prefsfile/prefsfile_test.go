package prefsfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/internal/util"
)

// The path rule is the on-disk layout every workspace already has: a move
// of it would lose people's layouts and themes (docs/compat.md).
func TestPath(t *testing.T) {
	want := filepath.Join("/ws", "data", "prefs", util.CompKey("ana"), util.CompKey("root")+".json")
	if got := Path("/ws", "ana", Root); got != want {
		t.Fatalf("Path = %s, want %s", got, want)
	}
	if got := Dir("/ws", Root); got != filepath.Join("/ws", "data", "prefs", util.CompKey("root")) {
		t.Fatalf("Dir = %s", got)
	}
}

func TestReadWrite(t *testing.T) {
	p := Path(t.TempDir(), "ana", "apps/x")
	m, err := Read(p)
	if err != nil || len(m) != 0 {
		t.Fatalf("a missing bucket: %v %v", m, err)
	}
	if err := Write(p, map[string]json.RawMessage{"theme": json.RawMessage(`"light"`)}); err != nil {
		t.Fatal(err)
	}
	m, err = Read(p)
	if err != nil || string(m["theme"]) != `"light"` {
		t.Fatalf("round trip: %v %v", m, err)
	}
	if err := os.WriteFile(p, []byte(`{"theme"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(p); err == nil {
		t.Fatal("a broken bucket read without an error")
	}
}
