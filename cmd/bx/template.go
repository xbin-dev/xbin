package main

import (
	"fmt"
	"strings"
)

// cmdTemplate manages template components (plans/templates.md):
//
//	bx template ls                          list templates (builtin + workspace)
//	bx template new <source> [as <path>]    instantiate one into a named copy
func cmdTemplate(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx template ls | new <source> [as <path>]")
	}
	switch args[0] {
	case "ls":
		var tpls []struct {
			ID          string `json:"id"`
			Source      string `json:"source"`
			Title       string `json:"title"`
			Description string `json:"description"`
			DefaultName string `json:"defaultName"`
		}
		if err := apiJSON("GET", "/api/xbin/templates", nil, &tpls); err != nil {
			return err
		}
		if len(tpls) == 0 {
			fmt.Println("(no templates)")
			return nil
		}
		for _, t := range tpls {
			fmt.Printf("%-10s %-20s %s\n", t.Source, t.ID, t.Title)
		}
		fmt.Println("\ninstantiate: bx template new <source> [as <path>]")
		return nil

	case "new":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx template new <source> [as <path>]")
		}
		source := args[1]
		path := ""
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
		body := map[string]string{"source": source}
		if path != "" {
			body["path"] = path
		}
		if err := apiJSON("POST", "/api/xbin/templates/new", body, &out); err != nil {
			return err
		}
		fmt.Printf("created %s\nframe it:  <bx-frame src=%q></bx-frame>\n", out.Path, out.Path)
		if len(out.PendingGrants) > 0 {
			fmt.Println("\nthis component needs grants (approve them, or it 403s):")
			for _, g := range out.PendingGrants {
				fmt.Printf("  bx grant %s %s:%s\n", g.From, g.Target, g.Role)
			}
		}
		return nil
	}
	return fmt.Errorf("unknown: bx template %s", strings.Join(args, " "))
}
