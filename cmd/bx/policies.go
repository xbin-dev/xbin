package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// bx policies — the workspace policies for partitioned tiles (PD-55,
// docs/bx.md): GET /api/xbin/workspace-policies for a person or admin, PUT
// (admin) to turn one switch on or off.

const policiesUsage = `  bx policies [ls] [--json] | set partition-consent|credential-reset-confirm on|off
                                        workspace policies for partitioned tiles
`

func init() { moreCmds["policies"] = cmdPolicies }

// policySwitches maps each switch's CLI name to its wire key and line.
var policySwitches = []struct{ name, key, line string }{
	{"partition-consent", "partitionConsent", "ask each person before another partitioned tile uses their data"},
	{"credential-reset-confirm", "credentialResetConfirm", "credential resets wait for the person"},
}

func cmdPolicies(args []string) error {
	if len(args) > 0 && args[0] == "ls" {
		args = args[1:]
	}
	if len(args) == 0 || (len(args) == 1 && args[0] == "--json") {
		var view map[string]any
		if err := apiJSON("GET", "/api/xbin/workspace-policies", nil, &view); err != nil {
			return err
		}
		if len(args) == 1 {
			return json.NewEncoder(os.Stdout).Encode(view)
		}
		printPolicies(view)
		return nil
	}
	if args[0] != "set" {
		return fmt.Errorf("bx policies: unknown subcommand %q (set)", args[0])
	}
	if len(args) != 3 || (args[2] != "on" && args[2] != "off") {
		return errors.New("usage: bx policies set partition-consent|credential-reset-confirm on|off")
	}
	for _, s := range policySwitches {
		if s.name != args[1] {
			continue
		}
		var view map[string]any
		if err := apiJSON("PUT", "/api/xbin/workspace-policies", map[string]bool{s.key: args[2] == "on"}, &view); err != nil {
			return err
		}
		printPolicies(view)
		return nil
	}
	return fmt.Errorf("bx policies: no switch %q (partition-consent, credential-reset-confirm)", args[1])
}

func printPolicies(view map[string]any) {
	for _, s := range policySwitches {
		state := "off"
		if view[s.key] == true {
			state = "on"
		}
		fmt.Printf("%-26s %-3s  %s\n", s.name, state, s.line)
	}
	fmt.Println("(these apply to partitioned tiles)")
}
