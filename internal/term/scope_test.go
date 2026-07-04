package term

import (
	"testing"

	"github.com/magik6k/buxon/internal/sandbox"
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
	root := "/ws"
	comps := []string{"apps/welcome", "apps/welcome/sub", "apps/s3-archiver", "root", "shell", "apps"}
	extra := []sandbox.Bind{{Src: "/sdk", Dst: "/sdk", RO: true}}

	// Root terminal: whole workspace rw, no per-component ro binds (+ the extra).
	root0 := scopedBinds(root, "", comps, extra)
	if len(root0) != 2 || root0[0].Src != root || root0[0].RO {
		t.Fatalf("root terminal should bind only the rw workspace (+extra): %+v", root0)
	}

	// Component terminal on apps/welcome.
	rw, ro := rwRO(scopedBinds(root, "apps/welcome", comps, extra))
	if !rw[root] || ro[root] {
		t.Errorf("workspace must stay rw (so $HOME/.git/commits work)")
	}
	// Siblings + unrelated components are read-only.
	for _, s := range []string{"/ws/apps/s3-archiver", "/ws/root", "/ws/shell"} {
		if !ro[s] {
			t.Errorf("%s should be read-only", s)
		}
	}
	// The current component, its ancestor ("apps"), and its descendant stay writable.
	for _, w := range []string{"/ws/apps/welcome", "/ws/apps/welcome/sub", "/ws/apps"} {
		if ro[w] {
			t.Errorf("%s must NOT be read-only (current component's path)", w)
		}
	}
	// Read-only ExtraBinds (the SDK) are preserved.
	if !ro["/sdk"] {
		t.Errorf("SDK extra bind lost")
	}
}

func TestUnderPath(t *testing.T) {
	if underPath("apps/we", "apps/welcome") {
		t.Error("apps/we is not under apps/welcome (segment-aware)")
	}
	if !underPath("apps/welcome/sub", "apps/welcome") {
		t.Error("descendant should be under")
	}
	if !underPath("apps/welcome", "apps/welcome") {
		t.Error("equal path counts as under")
	}
	if underPath("apps/welcome", "apps/welcome/sub") {
		t.Error("parent is not under its child")
	}
}
