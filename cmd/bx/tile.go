package main

import (
	"fmt"
	"strings"
)

// cmdTile manages the builtin tile catalog (plans/tile-sharing.md):
//
//	bx tile ls                        list builtin tiles
//	bx tile import <name> [as <path>] install one into the workspace
func cmdTile(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx tile ls | import <name> [as <path>]")
	}
	switch args[0] {
	case "ls":
		var tiles []struct {
			Name        string `json:"name"`
			Title       string `json:"title"`
			Description string `json:"description"`
			DefaultPath string `json:"defaultPath"`
			Installed   bool   `json:"installed"`
		}
		if err := apiJSON("GET", "/api/buxon/builtins", nil, &tiles); err != nil {
			return err
		}
		if len(tiles) == 0 {
			fmt.Println("(no builtin tiles)")
			return nil
		}
		for _, t := range tiles {
			mark := " "
			if t.Installed {
				mark = "*"
			}
			fmt.Printf("%s %-12s %-24s %s\n", mark, t.Name, t.DefaultPath, t.Title)
		}
		fmt.Println("\n* = already installed at its default path. Import: bx tile import <name> [as <path>]")
		return nil

	case "import":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx tile import <name> [as <path>]")
		}
		name := args[1]
		path := ""
		// optional: `as <path>`
		if len(args) >= 4 && args[2] == "as" {
			path = args[3]
		} else if len(args) == 3 {
			path = args[2]
		}
		var out struct {
			Path          string `json:"path"`
			PendingGrants []struct {
				From, Target, Role string
			} `json:"pendingGrants"`
		}
		body := map[string]string{"name": name}
		if path != "" {
			body["path"] = path
		}
		if err := apiJSON("POST", "/api/buxon/builtins/import", body, &out); err != nil {
			return err
		}
		fmt.Printf("imported %s\nframe it:  <bx-frame src=%q></bx-frame>\n", out.Path, out.Path)
		if len(out.PendingGrants) > 0 {
			fmt.Println("\nthis tile needs grants (approve them, or the tile 403s):")
			for _, g := range out.PendingGrants {
				fmt.Printf("  bx grant %s %s:%s\n", g.From, g.Target, g.Role)
			}
		}
		return nil
	}
	return fmt.Errorf("unknown: bx tile %s", strings.Join(args, " "))
}
