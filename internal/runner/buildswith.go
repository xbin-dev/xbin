package runner

// buildswith.go — which other tiles' code a tile's backend build takes, for
// the power rule of an access line (D246; the broker's accessreach.go): a
// tile's build workspace (D166, deps.BuildWork) holds the workspace modules
// its go.mod, manifest and code choose, transitively — and what a build
// takes runs as the tile that built it. So whoever writes such a module
// changes code that runs with that tile's grants.

import (
	"slices"

	"github.com/xbin-dev/xbin/internal/registry"
)

// BuildsWith lists the other tiles whose modules c's next work-tree build of
// its backend uses (its build workspace's modules, each tile once). nil for
// a tile not built from Go — what another runtime loads of other tiles is
// its manifest's deps, which the broker reads itself — and for xbind's own
// (sealed) code, which builds against no workspace module.
func (r *Runner) BuildsWith(c *registry.Component) []string {
	if c == nil || c.Sealed || c.Manifest.Runtime != "go" {
		return nil
	}
	w, ok := r.buildWork(c, goEntry(c.Manifest), "", nil)
	if !ok {
		return nil
	}
	var out []string
	for _, m := range w.Uses {
		if m.Tile != "" && m.Tile != c.Path && !slices.Contains(out, m.Tile) {
			out = append(out, m.Tile)
		}
	}
	slices.Sort(out)
	return out
}
