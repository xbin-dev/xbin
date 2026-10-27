package main

import (
	"fmt"
	"strconv"
)

// cmdApp is the xbin app's workspace settings (D204): how many devices one
// person may pair, and the app tunnel.
//
//	bx app limit [<n>]    the workspace device limit (admin): show, or set 1–32

func init() { moreCmds["app"] = cmdApp }

const appUsage = `  bx app limit [<n>]                    app devices one person may pair: show,
                                        or set the workspace default (1–32);
                                        per person: bx user set <id> --max-devices
`

func cmdApp(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage:\n%s", appUsage)
	}
	switch args[0] {
	case "limit":
		return cmdAppLimit(args[1:])
	}
	return fmt.Errorf("usage:\n%s", appUsage)
}

type limitRow struct {
	User     string `json:"user"`
	Name     string `json:"name"`
	Override *int   `json:"override"`
	Limit    int    `json:"limit"`
	Count    int    `json:"count"`
}

func cmdAppLimit(args []string) error {
	var out struct {
		Default   int        `json:"default"`
		Max       int        `json:"max"`
		AtLimit   []limitRow `json:"atLimit"`
		Overrides []limitRow `json:"overrides"`
	}
	switch len(args) {
	case 0:
		if err := apiJSON("GET", "/api/xbin/devices/limits", nil, &out); err != nil {
			return err
		}
	case 1:
		n, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("usage: bx app limit [<n>] (1–32)")
		}
		if err := apiJSON("PUT", "/api/xbin/devices/limits", map[string]int{"default": n}, &out); err != nil {
			return err
		}
	default:
		return fmt.Errorf("usage: bx app limit [<n>] (1–32)")
	}
	fmt.Printf("app devices per person: %d (at most %d)\n", out.Default, out.Max)
	for _, r := range out.Overrides {
		fmt.Printf("  %s: %d (own limit), %d paired\n", r.User, *r.Override, r.Count)
	}
	for _, r := range out.AtLimit {
		if r.Override == nil {
			fmt.Printf("  %s: at the limit, %d paired\n", r.User, r.Count)
		}
	}
	return nil
}
