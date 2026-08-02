package main

import (
	"fmt"
	"strings"

	"github.com/xbin-dev/xbin/internal/scaffold"
)

// cmdNew scaffolds a component (shared engine with POST /api/xbin/create;
// see internal/scaffold). Plain paths are purely local file writes (xbind
// picks the component up via watch, and it works with no daemon running).
// With --owner the creation routes through the create API instead: the
// daemon validates the owner and gates org-owned creation on the caller's
// Create knob (plans/ownership.md, D24/D25).
func cmdNew(args []string) error {
	o := scaffold.Options{}
	owner := ""
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
		case "--owner":
			i++
			if i >= len(args) {
				return fmt.Errorf("--owner needs user:<id> or org:<id>")
			}
			owner = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("unknown flag %s", args[i])
			}
			o.Path = args[i]
		}
	}
	if o.Path == "" {
		return fmt.Errorf("usage: bx new <path> [--runtime go|node|python|cgi|static] [--expose] [--title \"Pretty Name\"] [--owner user:<id>|org:<id>]")
	}
	if owner != "" {
		body := map[string]any{"path": o.Path, "runtime": o.Runtime, "title": o.Title, "expose": o.Expose, "owner": owner}
		var out struct {
			Path  string `json:"path"`
			Owner string `json:"owner"`
		}
		if err := apiJSON("POST", "/api/xbin/create", body, &out); err != nil {
			return err
		}
		fmt.Printf("scaffolded %s (owner %s)\nframe it:  <bx-frame src=%q></bx-frame>\n", o.Path, out.Owner, o.Path)
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
