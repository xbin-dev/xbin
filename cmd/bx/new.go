package main

import (
	"fmt"
	"strings"

	"github.com/magik6k/buxon/internal/scaffold"
)

// cmdNew scaffolds a component (shared engine with POST /api/buxon/create;
// see internal/scaffold). Purely local file writes; buxond picks the new
// component up via watch.
func cmdNew(args []string) error {
	o := scaffold.Options{}
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
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("unknown flag %s", args[i])
			}
			o.Path = args[i]
		}
	}
	if o.Path == "" {
		return fmt.Errorf("usage: bx new <path> [--runtime go|node|python|cgi|static] [--expose] [--title \"Pretty Name\"]")
	}
	ws := workspaceRoot()
	if ws == "" {
		return fmt.Errorf("not inside a buxon workspace")
	}
	if _, err := scaffold.Create(ws, o); err != nil {
		return err
	}
	fmt.Printf("scaffolded %s (runtime %s)\nframe it:  <bx-frame src=%q></bx-frame>\n",
		o.Path, orStatic(o.Runtime), o.Path)
	return nil
}
