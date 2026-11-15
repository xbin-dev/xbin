package acp

import "testing"

// A permission's option raises the session by the mode it switches to —
// claude-agent-acp's plan approval names none in its ids ("Yes, and bypass
// permissions" is exit-plan-bypass), so the catalog maps them; an
// allow_always of a mode switch the catalog can't place raises
// (default-deny), an allow_once or a reject never does.
func TestOptionRaises(t *testing.T) {
	claude, _ := Lookup("claude")
	opencode, _ := Lookup("opencode")
	house := Provider{ID: "house-agent", Name: "House agent", Driver: "acp", Argv: []string{"house", "acp"}} // not in the catalog
	st := SessionState{Modes: &SessionModes{CurrentModeID: "plan", AvailableModes: []ModeEntry{{ID: "default"},
		{ID: "acceptEdits"}, {ID: "plan"}, {ID: "auto"}, {ID: "bypassPermissions"}}}}
	asOption := SessionState{Options: []ConfigOption{{ID: "agent", Category: "mode", CurrentValue: "build",
		Options: []ConfigValue{{Value: "build"}, {Value: "plan"}, {Value: "danger"}}}}}
	opt := func(id, kind string) PermissionOption { return PermissionOption{OptionID: id, Name: id, Kind: kind} }
	for _, x := range []struct {
		prov  Provider
		st    SessionState
		start string
		o     PermissionOption
		kind  string
		want  bool
	}{
		// claude 0.81's ExitPlanMode options
		{claude, st, "", opt("exit-plan-bypass", AllowAlways), KindSwitchMode, true},
		{claude, st, "", opt("exit-plan-clear-bypass", AllowAlways), KindSwitchMode, true},
		{claude, st, "", opt("exit-plan-auto", AllowAlways), KindSwitchMode, false},
		{claude, st, "", opt("exit-plan-clear-auto", AllowAlways), KindSwitchMode, false},
		{claude, st, "", opt("exit-plan-accept-edits", AllowAlways), KindSwitchMode, false},
		{claude, st, "", opt("exit-plan-default", AllowOnce), KindSwitchMode, false},
		{claude, st, "", opt("reject", RejectOnce), KindSwitchMode, false},
		{claude, st, "", opt("exit-plan-bypass", RejectAlways), KindSwitchMode, false},
		// an option that is a mode id
		{claude, st, "", opt("bypassPermissions", AllowAlways), KindSwitchMode, true},
		{claude, st, "", opt("bypassPermissions", AllowOnce), "execute", true},
		{claude, st, "", opt("auto", AllowAlways), KindSwitchMode, false},
		// … known without a session (the catalog's own modes)
		{claude, SessionState{}, "", opt("bypassPermissions", AllowAlways), KindSwitchMode, true},
		{claude, SessionState{}, "", opt("acceptEdits", AllowAlways), KindSwitchMode, false},
		// the mode the session began in is open, a bypass too
		{claude, st, "bypassPermissions", opt("exit-plan-bypass", AllowAlways), KindSwitchMode, false},
		{claude, st, "bypassPermissions", opt("bypassPermissions", AllowAlways), KindSwitchMode, false},
		// a newer adapter's plan option the catalog can't place
		{claude, st, "", opt("exit-plan-dont-ask", AllowAlways), KindSwitchMode, true},
		{claude, st, "", opt("exit-plan-dont-ask", AllowOnce), KindSwitchMode, false},
		// an ordinary call's options switch nothing
		{claude, st, "", opt("allow-with-updates", AllowAlways), "execute", false},
		{claude, st, "", opt("allow-once", AllowOnce), "edit", false},
		// modes spoken as a config option: the catalog's SafeModes are open, another value is not
		{opencode, asOption, "", opt("plan", AllowAlways), KindSwitchMode, false},
		{opencode, asOption, "", opt("danger", AllowAlways), KindSwitchMode, true},
		// a provider the catalog lacks: only the mode it started in is open
		{house, st, "default", opt("default", AllowOnce), KindSwitchMode, false},
		{house, st, "default", opt("auto", AllowAlways), KindSwitchMode, true},
		{house, st, "default", opt("implement_plan", AllowOnce), KindSwitchMode, false},
		{house, st, "default", opt("exit-plan-clear-auto", AllowAlways), KindSwitchMode, true},
		{house, st, "", opt("default", AllowOnce), KindSwitchMode, true},
	} {
		if got := x.prov.OptionRaises(x.o, x.kind, x.st, x.start); got != x.want {
			t.Errorf("%s %s (%s, %s, start %q): raises %v, want %v", x.prov.ID, x.o.OptionID, x.o.Kind, x.kind, x.start, got, x.want)
		}
	}
}

func TestModeOpenAndSessionMode(t *testing.T) {
	claude, _ := Lookup("claude")
	for _, x := range []struct {
		mode, start string
		want        bool
	}{
		{"default", "", true}, {"plan", "", true}, {"bypassPermissions", "", false}, {"bypassPermissions", "bypassPermissions", true},
		{"newer-mode", "", false}, {"newer-mode", "newer-mode", true}, {"", "", false},
	} {
		if got := claude.ModeOpen(x.mode, x.start); got != x.want {
			t.Errorf("claude: mode %q (start %q) open %v, want %v", x.mode, x.start, got, x.want)
		}
	}
	if (Provider{}).ModeOpen("anything", "") {
		t.Error("a provider the catalog lacks opens no mode but its start")
	}
	modes := SessionState{Modes: &SessionModes{CurrentModeID: "plan"}, Options: []ConfigOption{{Category: "mode", CurrentValue: "build", Options: []ConfigValue{{Value: "build"}}}}}
	if got := modes.Mode(); got != "plan" {
		t.Errorf("a session's own mode comes first: %q", got)
	}
	modes.Modes = nil
	if got := modes.Mode(); got != "build" {
		t.Errorf("else its option of category mode: %q", got)
	}
	if got := (SessionState{Options: []ConfigOption{{Category: "mode", CurrentValue: "x"}, {Category: "model", CurrentValue: "m", Options: []ConfigValue{{Value: "m"}}}}}).Mode(); got != "" {
		t.Errorf("a mode option with no values is none: %q", got)
	}
}
