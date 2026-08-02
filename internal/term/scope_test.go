package term

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/sandbox"
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
	rw, ro := rwRO(scopedBinds(root, "", home, extra, nil))
	if !rw[root] || ro[root] {
		t.Fatalf("root terminal must bind the workspace rw")
	}

	// Component terminal: workspace read-only, the user's $HOME + the component read-write.
	rw, ro = rwRO(scopedBinds(root, "apps/welcome", home, extra, nil))
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
	mk := masked(scopedBinds(root, "apps/welcome", home, extra, nil))
	for _, secret := range []string{".xbin", "data", "homes"} {
		if !mk[filepath.Join(root, secret)] {
			t.Errorf("%s must be masked from a tile terminal", secret)
		}
	}
	// The own $HOME (under homes/) is still read-write — it nests over the mask.
	if !rw[home] {
		t.Errorf("own $HOME must remain read-write under the homes mask")
	}

	// The workspace root is bound read-only (a tile terminal reads all source,
	// writes only its own dir + $HOME). It's recursive — the only option in a
	// rootless userns (a non-recursive bind of a tree with locked resenc children
	// is EINVAL; see sandbox.Bind) — so the bind must not be sealed against the
	// deeper rw $HOME/component binds nesting on top.
	sb := scopedBinds(root, "apps/welcome", home, extra, nil)
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
	if !wsBind.RO {
		t.Errorf("workspace root bind must be read-only")
	}
}

// D17a: hide masks cover unreadable tiles' dirs, sealed; anything overlapping
// the session's own component is skipped (a mask must never shadow the cwd).
func TestScopedBindsHiddenTiles(t *testing.T) {
	root := t.TempDir()
	home := HomeDir(root, "alice")
	hide := []string{"apps/secret", "apps/welcome", "apps/welcome/nested", "sales/x"}
	sb := scopedBinds(root, "apps/welcome", home, nil, hide)
	mk := masked(sb)
	for _, h := range []string{"apps/secret", "sales/x"} {
		if !mk[filepath.Join(root, h)] {
			t.Errorf("%s must be masked for a hidden tile", h)
		}
	}
	for _, h := range []string{"apps/welcome", "apps/welcome/nested"} {
		if mk[filepath.Join(root, h)] {
			t.Errorf("%s overlaps the session's component and must not be masked", h)
		}
	}
	// A hidden-tile mask is sealed (RO) — nothing may nest back over it.
	for _, b := range sb {
		if b.Mask && b.Dst == filepath.Join(root, "apps/secret") && !b.RO {
			t.Error("hidden-tile mask must be read-only (sealed)")
		}
	}
	// No hide list (admins) adds nothing beyond the three secret masks.
	if n := len(masked(scopedBinds(root, "apps/welcome", home, nil, nil))); n != 3 {
		t.Errorf("admin terminal: %d masks, want 3 (.xbin, data, homes)", n)
	}
}

// $HOME is only rw-bound if it exists (fresh/odd workspaces without one still work).
func TestScopedBindsNoHome(t *testing.T) {
	root := t.TempDir()
	rw, _ := rwRO(scopedBinds(root, "apps/x", HomeDir(root, "ghost"), nil, nil))
	if rw[HomeDir(root, "ghost")] {
		t.Errorf("no home dir → no home bind")
	}
	if !rw[filepath.Join(root, "apps/x")] {
		t.Errorf("component still rw")
	}
}

// D40: the allow-list view plan — no masks at all; the staged view RO at the
// workspace root; only readable components bound (RO), own component + $HOME
// rw; extras appended.
func TestScopedBindsView(t *testing.T) {
	root := t.TempDir()
	home := HomeDir(root, "alice")
	for _, d := range []string{"apps/mine", "apps/friend", "apps/secret", filepath.Join("homes", "alice")} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	view := t.TempDir()
	extra := []sandbox.Bind{{Src: "/sdk", Dst: "/sdk", RO: true}}
	sb := scopedBindsView(root, "apps/mine", home, view, []string{"apps/mine", "apps/friend"}, extra)

	if n := len(masked(sb)); n != 0 {
		t.Fatalf("allow-list plan must carry no masks, got %d", n)
	}
	rw, ro := rwRO(sb)
	// The view covers the root read-only; the REAL root is never bound.
	viewAtRoot := false
	for _, b := range sb {
		if b.Dst == root && b.Src == view && b.RO {
			viewAtRoot = true
		}
		if b.Src == root {
			t.Fatalf("the real workspace root must not be bound: %+v", b)
		}
	}
	if !viewAtRoot {
		t.Fatal("staged view must be bound RO at the workspace root")
	}
	if !ro[filepath.Join(root, "apps/friend")] {
		t.Error("readable sibling must be bound read-only")
	}
	if !rw[filepath.Join(root, "apps/mine")] {
		t.Error("own component must be read-write")
	}
	if !rw[home] {
		t.Error("own $HOME must be read-write")
	}
	if !ro["/sdk"] {
		t.Error("SDK extra bind lost")
	}
	// The unreadable tile appears in NO bind — not even as a mask.
	for _, b := range sb {
		if strings.Contains(b.Src, "apps/secret") || strings.Contains(b.Dst, "apps/secret") {
			t.Errorf("unreadable tile must not appear in the plan: %+v", b)
		}
	}
}

// stageView writes the redacted root files, the CLAUDE.md symlink, and every
// nested-bind mountpoint (the view mounts read-only — nothing can mkdir later).
func TestStageView(t *testing.T) {
	root := t.TempDir()
	m := &Manager{Root: root}
	if err := os.MkdirAll(filepath.Join(root, ".xbin", "term"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"xbin.json":  []byte(`{"schema":1}`),
		"go.work":    []byte("go 1.24\n"),
		"AGENTS.md":  []byte("# agents"),
		".gitignore": []byte("data/\n"),
		"../evil":    []byte("nope"), // path tricks are dropped
	}
	dir, err := m.stageView("apps/mine", "alice", []string{"apps/mine", "apps/friend"}, files)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	for _, f := range []string{"xbin.json", "go.work", "AGENTS.md", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("staged file %s missing: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "evil")); err == nil {
		t.Error("path-trick file must be dropped")
	}
	if target, err := os.Readlink(filepath.Join(dir, "CLAUDE.md")); err != nil || target != "AGENTS.md" {
		t.Errorf("CLAUDE.md symlink: %q %v", target, err)
	}
	for _, d := range []string{".xbin", "homes/alice", "apps/mine", "apps/friend"} {
		if fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(d))); err != nil || !fi.IsDir() {
			t.Errorf("mountpoint %s missing", d)
		}
	}
}
