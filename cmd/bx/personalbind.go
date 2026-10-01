package main

// bx bind --personal — personal binds on partitioned tiles
// (docs/partitions.md §Bind types, docs/bx.md): a tile you own personally,
// wired into your own partition of a partitioned tile. A person's own act —
// run it with your sign-in, not from a tile's terminal (the route refuses
// every tile credential). Alone it lists yours (an admin's: everyone's).

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const personalBindUsage = "usage: bx bind --personal [--unset] <tile> <slot>=<provider> [--json]  |  bx bind --personal [--json]"

// cmdBindAny is `bx bind`: --personal goes to the personal binds, anything
// else to cmdBind (global binds, unchanged).
func cmdBindAny(args []string) error {
	if len(args) > 0 && args[0] == "--personal" {
		return cmdBindPersonal(args[1:])
	}
	return cmdBind(args)
}

type personalBindRow struct {
	ID        string `json:"id"`
	User      string `json:"user"`
	Requester string `json:"requester"`
	Slot      string `json:"slot"`
	Provider  string `json:"provider"`
	Live      bool   `json:"live"`
	Why       string `json:"why,omitempty"`
}

func cmdBindPersonal(args []string) error {
	unset, asJSON := false, false
	var rest []string
	for _, a := range args {
		switch a {
		case "--unset":
			unset = true
		case "--json":
			asJSON = true
		default:
			if strings.HasPrefix(a, "--") {
				return errors.New(personalBindUsage)
			}
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 && !unset {
		var out struct {
			Binds []personalBindRow `json:"binds"`
		}
		if err := apiJSON("GET", "/api/xbin/partitions/binds", nil, &out); err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(os.Stdout).Encode(out)
		}
		if len(out.Binds) == 0 {
			fmt.Println("no personal binds")
		}
		for _, b := range out.Binds {
			state := "live"
			if !b.Live {
				state = "inert: " + b.Why
			}
			fmt.Printf("%-12s %s.%s → %s  (%s, %s)\n", b.User, b.Requester, b.Slot, b.Provider, b.ID, state)
		}
		return nil
	}
	if len(rest) != 2 {
		return errors.New(personalBindUsage)
	}
	slot, provider, ok := strings.Cut(rest[1], "=")
	if !ok || slot == "" || provider == "" {
		return fmt.Errorf("expected <slot>=<provider>, got %q", rest[1])
	}
	body := map[string]string{"requester": rest[0], "slot": slot, "provider": provider}
	method := "POST"
	if unset {
		method = "DELETE"
	}
	var out map[string]any
	if err := apiJSON(method, "/api/xbin/partitions/binds", body, &out); err != nil {
		if os.Getenv("XBIN_COMPONENT") != "" && strings.Contains(err.Error(), "person's own act") {
			return fmt.Errorf("%w\nhint: a personal bind is made with your own sign-in, not from a tile's terminal", err)
		}
		return err
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	fmt.Println("ok")
	return nil
}
