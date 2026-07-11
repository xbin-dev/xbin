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
		if b.Mask {
			continue // masks carry no Src; checked separately via masked()
		}
		if b.RO {
			ro[b.Src] = true
		} else {
			rw[b.Src] = true
		}
	}
	return
}

// masked collects the Dst of every mask bind.
func masked(binds []sandbox.Bind) map[string]bool {
	m := map[string]bool{}
	for _, b := range binds {
		if b.Mask {
			m[b.Dst] = true
		}
	}
	return m
}

func TestScopedBinds(t *testing.T) {
	root := t.TempDir()
	home := HomeDir(root, "alice")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	extra := []sandbox.Bind{{Src: "/sdk", Dst: "/sdk", RO: true}}

	// Root terminal (owner plane): the whole workspace rw, no ro root bind.
	rw, ro := rwRO(scopedBinds(root, "", home, extra))
	if !rw[root] || ro[root] {
		t.Fatalf("root terminal must bind the workspace rw")
	}

	// Component terminal: workspace read-only, the user's $HOME + the component read-write.
	rw, ro = rwRO(scopedBinds(root, "apps/welcome", home, extra))
	if !ro[root] || rw[root] {
		t.Errorf("component terminal: workspace root must be READ-ONLY")
	}
	if !rw[home] {
		t.Errorf("the user's $HOME must be read-write")
	}
	if !rw[filepath.Join(root, "apps/welcome")] {
		t.Errorf("the component's own dir must be read-write (code + its .git)")
	}
	// A sibling is not separately bound — it's covered by the read-only root
	// (sibling CODE stays visible RO for API integration; only its dir isn't rw).
	if rw[filepath.Join(root, "apps/other")] {
		t.Errorf("a sibling component must not be writable")
	}
	if !ro["/sdk"] {
		t.Errorf("SDK extra bind lost")
	}

	// The platform's secrets + other users' data are masked out of the tile
	// terminal, even though the root is bound read-only (Gap 0): .xbin (owner
	// token, frame secret), data (vault, resource state, password hashes), and
	// homes (other users' $HOME).
	mk := masked(scopedBinds(root, "apps/welcome", home, extra))
	for _, secret := range []string{".xbin", "data", "homes"} {
		if !mk[filepath.Join(root, secret)] {
			t.Errorf("%s must be masked from a tile terminal", secret)
		}
	}
	// The own $HOME (under homes/) is still read-write — it nests over the mask.
	if !rw[home] {
		t.Errorf("own $HOME must remain read-write under the homes mask")
	}

	// The workspace root is bound NON-recursively, so the resource (resenc)
	// gocryptfs submounts the workspace fs carries aren't cloned into the terminal
	// — the only way to keep other tiles' resource names out of `mount`, since a
	// rootless userns locks inherited mounts against unmounting from inside.
	sb := scopedBinds(root, "apps/welcome", home, extra)
	var wsBind *sandbox.Bind
	for i := range sb {
		if sb[i].Dst == root && sb[i].Src == root {
			wsBind = &sb[i]
			break
		}
	}
	if wsBind == nil {
		t.Fatal("no workspace root bind")
	}
	if !wsBind.NoRec {
		t.Errorf("workspace root bind must be NoRec (else resenc submounts leak into the terminal's mount table)")
	}
	if !wsBind.RO {
		t.Errorf("workspace root bind must be read-only")
	}
}

// $HOME is only rw-bound if it exists (fresh/odd workspaces without one still work).
func TestScopedBindsNoHome(t *testing.T) {
	root := t.TempDir()
	rw, _ := rwRO(scopedBinds(root, "apps/x", HomeDir(root, "ghost"), nil))
	if rw[HomeDir(root, "ghost")] {
		t.Errorf("no home dir → no home bind")
	}
	if !rw[filepath.Join(root, "apps/x")] {
		t.Errorf("component still rw")
	}
}
