package main

import (
	"fmt"
	"strings"

	"github.com/xbin-dev/xbin/internal/scaffold"
)

// cmdNew scaffolds a component (shared engine with POST /api/xbin/create;
// see internal/scaffold). Plain paths are purely local file writes (xbind
// picks the component up via watch, and it works with no daemon running).
// Org paths (an o/u marker in the path) and --team route through the create
// API instead: the daemon validates the org exists and applies the
// create-in-team auto-grant (plans/orgs.md).
func cmdNew(args []string) error {
	o := scaffold.Options{}
	team := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--runtime":
			i++
			if i >= len(args) {
				return fmt.Errorf("--runtime needs a value")
			}
			o.Runtime = args[i]
		case "--expose":
			o.Expose = true
		case "--title":
			i++
			if i >= len(args) {
				return fmt.Errorf("--title needs a value")
			}
			o.Title = args[i]
		case "--team":
			i++
			if i >= len(args) {
				return fmt.Errorf("--team needs <org>/<team>")
			}
			team = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("unknown flag %s", args[i])
			}
			o.Path = args[i]
		}
	}
	if o.Path == "" {
		return fmt.Errorf("usage: bx new <path> [--runtime go|node|python|cgi|static] [--expose] [--title \"Pretty Name\"] [--team <org>/<team>]")
	}
	if team != "" || hasReservedSeg(o.Path) {
		body := map[string]any{"path": o.Path, "runtime": o.Runtime, "title": o.Title, "expose": o.Expose}
		if team != "" {
			body["team"] = team
		}
		var out struct {
			Path      string `json:"path"`
			TeamLevel string `json:"teamLevel"`
		}
		if err := apiJSON("POST", "/api/xbin/create", body, &out); err != nil {
			return err
		}
		fmt.Printf("scaffolded %s", o.Path)
		if team != "" {
			fmt.Printf(" in team %s (team level: %s)", team, out.TeamLevel)
		}
		fmt.Printf("\nframe it:  <bx-frame src=%q></bx-frame>\n", o.Path)
		return nil
	}
	ws := workspaceRoot()
	if ws == "" {
		return fmt.Errorf("not inside a xbin workspace")
	}
	if _, err := scaffold.Create(ws, o); err != nil {
		return err
	}
	fmt.Printf("scaffolded %s (runtime %s)\nframe it:  <bx-frame src=%q></bx-frame>\n",
		o.Path, orStatic(o.Runtime), o.Path)
	return nil
}

// hasReservedSeg reports an o/u org marker anywhere in the path — those
// creations go through xbind so it can validate the org and apply grants.
func hasReservedSeg(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if seg == "o" || seg == "u" {
			return true
		}
	}
	return false
}
