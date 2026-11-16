package acp

import "testing"

// "Asks before acting" is the harness's ask or plan mode and no other:
// the modes that edit or run on their own are Safe (no bypass) and do not
// ask before every edit and command.
func TestAsksIsTheAskAndPlanModesOnly(t *testing.T) {
	asks := map[string][]string{"claude": {"default", "plan"}, "codex": {"read-only"}, "gemini": {"default", "plan"}, "opencode": {"plan"}}
	acts := map[string][]string{"claude": {"acceptEdits", "auto", "bypassPermissions"}, "codex": {"agent", "agent-full-access"},
		"gemini": {"autoEdit", "yolo"}, "opencode": {"build"}}
	for _, p := range Providers() {
		if len(asks[p.ID]) == 0 {
			t.Errorf("%s: a shipped harness this test doesn't know — say which of its modes ask before every edit and command", p.ID)
		}
		for _, m := range asks[p.ID] {
			if !p.Asks(m) {
				t.Errorf("%s %s: doesn't count as asking", p.ID, m)
			}
		}
		for _, m := range append(acts[p.ID], "", "nosuch") {
			if p.Asks(m) {
				t.Errorf("%s %q counts as asking before every edit and command", p.ID, m)
			}
		}
		for _, m := range p.Modes { // every mode is decided here, one way or the other
			known := false
			for _, k := range append(asks[p.ID], acts[p.ID]...) {
				known = known || k == m.ID
			}
			if !known {
				t.Errorf("%s has a mode %q this test doesn't place", p.ID, m.ID)
			}
			if m.Explicit && p.Asks(m.ID) {
				t.Errorf("%s %s is a bypass mode and counts as asking", p.ID, m.ID)
			}
		}
	}
	if (Provider{}).Asks("default") {
		t.Error("a harness the catalog lacks asks")
	}
}
