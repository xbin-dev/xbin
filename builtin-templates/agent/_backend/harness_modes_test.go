package main

import (
	"encoding/json"
	"testing"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// TestHarnessModesFromOption: an adapter that speaks its modes only as a
// config option of category mode (opencode 1.18: no `modes` in session/new,
// a `mode` option with its agents — found live, WP-A11) still fills the
// summary's mode picker (§4.2.4: the picker covers that option; it is never
// in `options`). Session modes win when an adapter has both (claude), and
// the catalog's modes stand in before a session.
func TestHarnessModesFromOption(t *testing.T) {
	opencode, _ := acp.Lookup("opencode")
	claude, _ := acp.Lookup("claude")
	modeOpt := acp.ConfigOption{ID: "mode", Name: "Mode", Category: "mode", Type: "select", CurrentValue: "build",
		Options: []acp.ConfigValue{{Value: "build", Name: "Build"}, {Value: "plan", Name: "Plan", Description: "read-only"}}}
	model := acp.ConfigOption{ID: "model", Name: "Model", Category: "model", Type: "select", CurrentValue: "big-pickle",
		Options: []acp.ConfigValue{{Value: "big-pickle"}}}
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }

	st := acp.SessionState{Options: []acp.ConfigOption{model, modeOpt}}
	got := js(harnessModes(opencode, st, ""))
	want := `{"available":[{"id":"build","name":"Build"},{"description":"read-only","id":"plan","name":"Plan"}],"current":"build"}`
	if got != want {
		t.Errorf("opencode's modes from its mode option:\n got %s\nwant %s", got, want)
	}
	if opts := harnessOptionsView(st.Options); len(opts) != 1 || opts[0].ID != "model" {
		t.Errorf("the mode option is the picker's, not an option: %+v", opts)
	}

	// session modes win over the option; explicit from the catalog
	st = acp.SessionState{Modes: &acp.SessionModes{CurrentModeID: "default", AvailableModes: []acp.ModeEntry{{ID: "default", Name: "Default"},
		{ID: "bypassPermissions", Name: "Bypass"}}}, Options: []acp.ConfigOption{modeOpt}}
	got = js(harnessModes(claude, st, ""))
	want = `{"available":[{"id":"default","name":"Default"},{"explicit":true,"id":"bypassPermissions","name":"Bypass"}],"current":"default"}`
	if got != want {
		t.Errorf("claude's session modes:\n got %s\nwant %s", got, want)
	}

	// before a session: the catalog's (none for opencode)
	if got := js(harnessModes(opencode, acp.SessionState{}, "")); got != `{"available":[],"current":""}` {
		t.Errorf("opencode before a session: %s", got)
	}
	if m := harnessModes(claude, acp.SessionState{}, "acceptEdits"); m["current"] != "acceptEdits" || len(m["available"].([]map[string]any)) != len(claude.Modes) {
		t.Errorf("claude before a session: %v", m)
	}
}
