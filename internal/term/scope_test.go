package term

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/magik6k/xbin/internal/sandbox"
)

func rwRO(binds []sandbox.Bind) (rw, ro map[string]bool) {
	rw, ro = map[string]bool{}, map[string]bool{}
	for _, b := range binds {
		if b.RO {
			ro[b.Src] = true
		} else {
			rw[b.Src] = true
		}
	}
	return
}

func TestScopedBinds(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "home"), 0o755); err != nil {
		t.Fatal(err)
	}
	extra := []sandbox.Bind{{Src: "/sdk", Dst: "/sdk", RO: true}}

	// Root terminal (owner plane): the whole workspace rw, no ro root bind.
	rw, ro := rwRO(scopedBinds(root, "", extra))
	if !rw[root] || ro[root] {
		t.Fatalf("root terminal must bind the workspace rw")
	}

	// Component terminal: workspace read-only, $HOME + the component read-write.
	rw, ro = rwRO(scopedBinds(root, "apps/welcome", extra))
	if !ro[root] || rw[root] {
		t.Errorf("component terminal: workspace root must be READ-ONLY")
	}
	if !rw[filepath.Join(root, "home")] {
		t.Errorf("$HOME must be read-write")
	}
	if !rw[filepath.Join(root, "apps/welcome")] {
		t.Errorf("the component's own dir must be read-write (code + its .git)")
	}
	// A sibling is not separately bound — it's covered by the read-only root.
	if rw[filepath.Join(root, "apps/other")] {
		t.Errorf("a sibling component must not be writable")
	}
	if !ro["/sdk"] {
		t.Errorf("SDK extra bind lost")
	}
}

// $HOME is only rw-bound if it exists (fresh/odd workspaces without one still work).
func TestScopedBindsNoHome(t *testing.T) {
	root := t.TempDir()
	rw, _ := rwRO(scopedBinds(root, "apps/x", nil))
	if rw[filepath.Join(root, "home")] {
		t.Errorf("no home dir → no home bind")
	}
	if !rw[filepath.Join(root, "apps/x")] {
		t.Errorf("component still rw")
	}
}
