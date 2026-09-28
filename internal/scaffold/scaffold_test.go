package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `bx new --runtime cgi` and POST /api/xbin/create {runtime: "cgi"} are
// refused with the removal message (D117), before anything is written.
func TestCreateRefusesCGI(t *testing.T) {
	root := t.TempDir()
	files, err := Create(root, Options{Path: "apps/old", Runtime: "cgi"})
	if err == nil || !strings.Contains(err.Error(), `runtime "cgi" was removed`) {
		t.Fatalf("Create(cgi) = %v, want the removal error", err)
	}
	if len(files) > 0 {
		t.Errorf("wrote %v", files)
	}
	if _, err := os.Stat(filepath.Join(root, "apps", "old")); !os.IsNotExist(err) {
		t.Errorf("the component directory was created (stat: %v)", err)
	}
	if _, err := Create(root, Options{Path: "apps/py", Runtime: "python"}); err != nil {
		t.Fatalf("Create(python) = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "apps", "py", "backend", "server.py")); err != nil {
		t.Fatal(err)
	}
}

// covers D127j — `bx new` writes locally through Create, so Create refuses a
// '+' in any segment of a new tile's path, as /create does, before anything
// is written: "<tile>+<name>" is a tile deployment's URL.
func TestCreateRefusesPlus(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"apps/x+dev", "tools+x/lint"} {
		files, err := Create(root, Options{Path: p})
		if err == nil || !strings.Contains(err.Error(), "'+' isn't allowed in tile names") {
			t.Errorf("Create(%s) = %v, want the '+' refusal", p, err)
		}
		if len(files) > 0 {
			t.Errorf("Create(%s) wrote %v", p, files)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); !os.IsNotExist(err) {
			t.Errorf("Create(%s) made its directory (stat: %v)", p, err)
		}
	}
}
