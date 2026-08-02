package main

import (
	"fmt"
	"strings"
)

// cmdBuiltin surfaces builtin-component updates (plans/builtin-updates.md):
//
//	bx builtin updates                      list builtins with an update available
//	bx builtin update <id> [--replace|--merge]  apply one (default: replace)
//	bx builtin pin <id> | unpin <id>        stop/resume offering updates
func cmdBuiltin(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx builtin updates | update <id> [--replace|--merge] | pin <id> | unpin <id>")
	}
	switch args[0] {
	case "updates", "ls":
		var ups []struct {
			ID          string `json:"id"`
			InstallPath string `json:"installPath"`
			FromVersion int    `json:"fromVersion"`
			ToVersion   int    `json:"toVersion"`
			Adopted     bool   `json:"adopted"`
			Clean       int    `json:"clean"`
			Conflicts   int    `json:"conflicts"`
			Missing     bool   `json:"missing"`
		}
		if err := apiJSON("GET", "/api/xbin/builtins/updates", nil, &ups); err != nil {
			return err
		}
		if len(ups) == 0 {
			fmt.Println("everything up to date")
			return nil
		}
		for _, u := range ups {
			if u.Missing {
				fmt.Printf("%-22s MISSING — this workspace predates it; install: bx builtin update %s\n", u.ID, u.ID)
				continue
			}
			note := fmt.Sprintf("%d clean", u.Clean)
			if u.Conflicts > 0 {
				note += fmt.Sprintf(", %d conflict", u.Conflicts)
			}
			if u.Adopted {
				note += ", adopted (no base — replace or hand-merge)"
			}
			fmt.Printf("%-22s v%d→v%d  %s\n", u.ID, u.FromVersion, u.ToVersion, note)
		}
		fmt.Println("\napply: bx builtin update <id> [--replace|--merge]")
		return nil

	case "update":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx builtin update <id> [--replace|--merge]")
		}
		id := args[1]
		mode := "replace"
		for _, a := range args[2:] {
			switch a {
			case "--merge":
				mode = "merge"
			case "--replace":
				mode = "replace"
			}
		}
		var out struct {
			Files []string `json:"files"`
		}
		if err := apiJSON("POST", "/api/xbin/builtins/update", map[string]string{"id": id, "mode": mode}, &out); err != nil {
			return err
		}
		fmt.Printf("%sd %s (%d file(s))\n", mode, id, len(out.Files))
		if mode == "merge" {
			fmt.Println("check for conflict markers (<<<<<<<) in the changed files and resolve them.")
		}
		return nil

	case "pin", "unpin":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx builtin %s <id>", args[0])
		}
		if err := apiJSON("POST", "/api/xbin/builtins/update", map[string]string{"id": args[1], "mode": args[0]}, nil); err != nil {
			return err
		}
		fmt.Printf("%sned %s\n", args[0], args[1])
		return nil
	}
	return fmt.Errorf("unknown: bx builtin %s", strings.Join(args, " "))
}
