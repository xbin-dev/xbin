package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// bx settings — the workspace settings an admin sets (docs/bx.md, D173):
// GET/PUT /api/xbin/workspace-settings. Today one: base auto-update, a
// tile's terminal layer built on an older base image moving to the current
// base at its next session start.

const settingsUsage = `  bx settings [ls] | set --base-auto-update[=true|false]
                                        workspace settings (set: admin)
`

// wsSettings mirrors GET /api/xbin/workspace-settings.
type wsSettings struct {
	BaseAutoUpdate bool   `json:"baseAutoUpdate"`
	Error          string `json:"error"`
}

func (s wsSettings) print() {
	state := "on — a terminal on an older base image moves to the current one at its next start (its apt installs reset)"
	if !s.BaseAutoUpdate {
		state = "off — terminals stay on their base image; the terminal window offers the update"
	}
	fmt.Printf("base auto-update:  %s\n", state)
	if s.Error != "" {
		fmt.Printf("                   (the settings file can't be read: %s)\n", s.Error)
	}
}

func cmdSettings(args []string) error {
	sub := "ls"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "ls":
		if len(args) > 0 {
			return errors.New("usage: bx settings [ls]")
		}
		var s wsSettings
		if err := apiJSON("GET", "/api/xbin/workspace-settings", nil, &s); err != nil {
			return err
		}
		s.print()
		return nil
	case "set":
		body := map[string]any{}
		for _, a := range args {
			flag, val, hasVal := strings.Cut(a, "=")
			switch flag {
			case "--base-auto-update":
				on := true
				if hasVal {
					v, err := strconv.ParseBool(val)
					if err != nil {
						return fmt.Errorf("--base-auto-update: %q isn't true or false", val)
					}
					on = v
				}
				body["baseAutoUpdate"] = on
			case "--no-base-auto-update":
				if hasVal {
					return errors.New("--no-base-auto-update takes no value")
				}
				body["baseAutoUpdate"] = false
			default:
				return fmt.Errorf("bx settings set: unknown flag %q (--base-auto-update[=true|false])", a)
			}
		}
		if len(body) == 0 {
			return errors.New("usage: bx settings set --base-auto-update[=true|false]")
		}
		var s wsSettings
		if err := apiJSON("PUT", "/api/xbin/workspace-settings", body, &s); err != nil {
			return err
		}
		s.print()
		return nil
	}
	return fmt.Errorf("bx settings: unknown subcommand %q (ls|set)", sub)
}
