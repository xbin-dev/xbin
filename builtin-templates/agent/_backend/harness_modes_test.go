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
	got := js(harnessModes(opencode, st, "", ""))
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
	got = js(harnessModes(claude, st, "", ""))
	want = `{"available":[{"id":"default","name":"Default"},{"explicit":true,"id":"bypassPermissions","name":"Bypass"}],"current":"default"}`
	if got != want {
		t.Errorf("claude's session modes:\n got %s\nwant %s", got, want)
	}

	// before a session: the catalog's (none for opencode)
	if got := js(harnessModes(opencode, acp.SessionState{}, "", "")); got != `{"available":[],"current":""}` {
		t.Errorf("opencode before a session: %s", got)
	}
	if m := harnessModes(claude, acp.SessionState{}, "acceptEdits", ""); m["current"] != "acceptEdits" || len(m["available"].([]map[string]any)) != len(claude.Modes) {
		t.Errorf("claude before a session: %v", m)
	}
}

// Default-deny (§4.3.12): `explicit` (owner-only) is every mode the catalog
// doesn't know to be safe — a mode a newer adapter reports, every mode of a
// harness the catalog lacks but the one it opened its first session in by
// itself — and a permission option raises the session only when it is an
// allow naming such a mode.
func TestHarnessModesDefaultDeny(t *testing.T) {
	claude, _ := acp.Lookup("claude")
	house := harnessProvider("house-agent", &sbxHarness{ID: "house-agent", Title: "House agent", Argv: []string{"house", "acp"}})
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	newer := acp.SessionState{Modes: &acp.SessionModes{CurrentModeID: "default", AvailableModes: []acp.ModeEntry{{ID: "default", Name: "Default"},
		{ID: "acceptEdits", Name: "Accept"}, {ID: "dontAsk", Name: "Don't ask"}, {ID: "bypassPermissions", Name: "Bypass"}}}}
	want := `{"available":[{"id":"default","name":"Default"},{"id":"acceptEdits","name":"Accept"},{"explicit":true,"id":"dontAsk","name":"Don't ask"},{"explicit":true,"id":"bypassPermissions","name":"Bypass"}],"current":"default"}`
	if got := js(harnessModes(claude, newer, "", "")); got != want {
		t.Errorf("a mode the catalog doesn't list:\n got %s\nwant %s", got, want)
	}
	st := acp.SessionState{Modes: &acp.SessionModes{CurrentModeID: "ask", AvailableModes: []acp.ModeEntry{{ID: "ask", Name: "Ask"}, {ID: "yolo", Name: "Yolo"}}}}
	want = `{"available":[{"id":"ask","name":"Ask"},{"explicit":true,"id":"yolo","name":"Yolo"}],"current":"ask"}`
	if got := js(harnessModes(house, st, "", "ask")); got != want {
		t.Errorf("a harness the catalog lacks:\n got %s\nwant %s", got, want)
	}
	want = `{"available":[{"explicit":true,"id":"ask","name":"Ask"},{"explicit":true,"id":"yolo","name":"Yolo"}],"current":"ask"}`
	if got := js(harnessModes(house, st, "", "")); got != want {
		t.Errorf("…whose first mode isn't known:\n got %s\nwant %s", got, want)
	}
	opencode, _ := acp.Lookup("opencode")
	oc := acp.SessionState{Options: []acp.ConfigOption{{ID: "agent", Category: "mode", CurrentValue: "build",
		Options: []acp.ConfigValue{{Value: "build"}, {Value: "plan"}, {Value: "yolo-agent"}}}}}
	want = `{"available":[{"id":"build","name":"build"},{"id":"plan","name":"plan"},{"explicit":true,"id":"yolo-agent","name":"yolo-agent"}],"current":"build"}`
	if got := js(harnessModes(opencode, oc, "", "")); got != want {
		t.Errorf("opencode's agents:\n got %s\nwant %s", got, want)
	}
	if currentMode(oc) != "build" || currentMode(st) != "ask" || currentMode(acp.SessionState{}) != "" {
		t.Error("currentMode")
	}
	var hs harnessSession
	noteStartMode(&hs, "", st)
	noteStartMode(&hs, "", oc)
	if hs.StartMode != "ask" {
		t.Errorf("the start mode is the first: %q", hs.StartMode)
	}
	hs = harnessSession{}
	if noteStartMode(&hs, "yolo", st); hs.StartMode != "" {
		t.Errorf("a session opened in an asked mode says nothing of the adapter's own: %q", hs.StartMode)
	}
}

// A permission's option raises the session (explicit: the owner's) by the
// mode it switches to — claude-agent-acp's plan approval names none in its
// ids ("Yes, and bypass permissions" is exit-plan-bypass), so the catalog
// maps them; an allow_always of a mode switch the catalog can't place
// raises (default-deny), an allow_once or a reject never does.
func TestHarnessOptionRaises(t *testing.T) {
	claude, _ := acp.Lookup("claude")
	house := harnessProvider("house-agent", &sbxHarness{ID: "house-agent", Title: "House agent", Argv: []string{"house", "acp"}})
	st := acp.SessionState{Modes: &acp.SessionModes{CurrentModeID: "plan", AvailableModes: []acp.ModeEntry{{ID: "default"},
		{ID: "acceptEdits"}, {ID: "plan"}, {ID: "auto"}, {ID: "bypassPermissions"}}}}
	opt := func(id, kind string) acp.PermissionOption {
		return acp.PermissionOption{OptionID: id, Name: id, Kind: kind}
	}
	for _, x := range []struct {
		prov  acp.Provider
		start string
		o     acp.PermissionOption
		kind  string
		want  bool
	}{
		// claude 0.81's ExitPlanMode options
		{claude, "", opt("exit-plan-bypass", acp.AllowAlways), acp.KindSwitchMode, true},
		{claude, "", opt("exit-plan-clear-bypass", acp.AllowAlways), acp.KindSwitchMode, true},
		{claude, "", opt("exit-plan-auto", acp.AllowAlways), acp.KindSwitchMode, false},
		{claude, "", opt("exit-plan-clear-auto", acp.AllowAlways), acp.KindSwitchMode, false},
		{claude, "", opt("exit-plan-accept-edits", acp.AllowAlways), acp.KindSwitchMode, false},
		{claude, "", opt("exit-plan-default", acp.AllowOnce), acp.KindSwitchMode, false},
		{claude, "", opt("reject", acp.RejectOnce), acp.KindSwitchMode, false},
		// an option that is a mode id
		{claude, "", opt("bypassPermissions", acp.AllowAlways), acp.KindSwitchMode, true},
		{claude, "", opt("auto", acp.AllowAlways), acp.KindSwitchMode, false},
		// a newer adapter's plan option the catalog can't place
		{claude, "", opt("exit-plan-dont-ask", acp.AllowAlways), acp.KindSwitchMode, true},
		// an ordinary call's options switch nothing
		{claude, "", opt("allow-with-updates", acp.AllowAlways), "execute", false},
		{claude, "", opt("allow-once", acp.AllowOnce), "edit", false},
		// a harness the catalog lacks: only the mode it started in is open
		{house, "default", opt("default", acp.AllowOnce), acp.KindSwitchMode, false},
		{house, "default", opt("auto", acp.AllowAlways), acp.KindSwitchMode, true},
		{house, "default", opt("implement_plan", acp.AllowOnce), acp.KindSwitchMode, false},
		{house, "default", opt("exit-plan-clear-auto", acp.AllowAlways), acp.KindSwitchMode, true},
	} {
		if got := optionRaises(x.prov, st, x.start, x.o, x.kind); got != x.want {
			t.Errorf("%s %s (%s, %s): raises %v, want %v", x.prov.ID, x.o.OptionID, x.o.Kind, x.kind, got, x.want)
		}
	}
}
