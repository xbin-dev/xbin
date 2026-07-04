package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLifecycleState(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "buxon.json"), []byte(`{"schema":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	if got := r.LifecycleState("apps/x"); got != StateEnabled {
		t.Fatalf("default state = %q, want %q", got, StateEnabled)
	}
	set := func(state string) {
		t.Helper()
		if err := r.MutateWorkspace(func(ws *WorkspaceManifest) {
			if ws.Lifecycle == nil {
				ws.Lifecycle = map[string]string{}
			}
			ws.Lifecycle["apps/x"] = state
		}); err != nil {
			t.Fatal(err)
		}
	}
	set(StateDisabled)
	if got := r.LifecycleState("apps/x"); got != StateDisabled {
		t.Fatalf("after disable = %q, want %q", got, StateDisabled)
	}
	// An untouched component is still enabled.
	if got := r.LifecycleState("apps/y"); got != StateEnabled {
		t.Fatalf("sibling = %q, want %q", got, StateEnabled)
	}
	// Clearing the entry returns to the enabled default.
	if err := r.MutateWorkspace(func(ws *WorkspaceManifest) { delete(ws.Lifecycle, "apps/x") }); err != nil {
		t.Fatal(err)
	}
	if got := r.LifecycleState("apps/x"); got != StateEnabled {
		t.Fatalf("after clear = %q, want %q", got, StateEnabled)
	}
}

func TestIsOffloaded(t *testing.T) {
	for _, s := range []string{StateOffloaded, StateOffloadedFull} {
		if !IsOffloaded(s) {
			t.Errorf("%q should be offloaded", s)
		}
	}
	for _, s := range []string{StateEnabled, StateDisabled, ""} {
		if IsOffloaded(s) {
			t.Errorf("%q should not be offloaded", s)
		}
	}
}
