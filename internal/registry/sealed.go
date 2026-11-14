package registry

// sealed.go — sealed components in the registry (spike S1 of the workspace
// agent, issue #51). A sealed component's code is the running xbind's own:
// internal/sealed renders it from the embedded tree and materializes it
// read-only under .xbin/sealed/. The registry learns of it here, from a
// compiled-in table handed in at boot, and from nowhere else: nothing a
// workspace holds — a directory, an xbin.json, the workspace manifest —
// declares one.
//
// Why no workspace can hold, or later create, a component at such a path:
// sealed paths lie under "xbin/", and "xbin" is a util.ReservedTop name
// (as "buxon" was from the first commit, before any release). Rescan skips
// the reserved top-level directories whole, util.ComponentPathOK refuses
// them for every creation path (clone, import, template instance, git
// import), and the /c/ plane refuses them (pathAllowed). So a directory
// <workspace>/xbin/agent is never scanned, served or run: the sealed
// component of that path is the table's, with or without it.

import (
	"path/filepath"
	"sort"

	"github.com/xbin-dev/xbin/internal/jsonc"
	"github.com/xbin-dev/xbin/internal/util"
)

// SealedCode is one sealed component as the registry registers it.
type SealedCode struct {
	Path string // its reserved path, under util.SealedPrefix
	// Dir is the read-only tree xbind materialized from the embedded source
	// (xbind's own data, under .xbin/): what backends run in and files are
	// served from.
	Dir string
	// Manifest and Scope are its xbin.json and scope.json (nil: none), from
	// the embedded source, already carrying the table's partition mode.
	Manifest, Scope []byte
}

// addSealed registers the sealed components over whatever the walk found:
// the walk never yields a path under xbin/, and a table entry wins anyway.
func (r *Registry) addSealed(comps map[string]*Component, scopes map[string]*ScopeManifest) {
	if r.Sealed == nil {
		return
	}
	for _, sc := range r.Sealed() {
		if !util.SealedPath(sc.Path) {
			continue // the table is checked where it is loaded; never register outside the namespace
		}
		c := &Component{Path: sc.Path, Dir: sc.Dir, Sealed: true}
		if sc.Manifest != nil {
			c.readManifest(sc.Manifest)
		}
		c.HasIndex = regularFile(filepath.Join(sc.Dir, "index.html"))
		c.Native, c.NativeErr = c.resolveNative()
		comps[sc.Path] = c
		if sc.Scope != nil {
			sm := &ScopeManifest{}
			if err := jsonc.Unmarshal(sc.Scope, sm); err == nil {
				sm.dropInvalidResources(sc.Path)
			} else {
				sm = &ScopeManifest{}
			}
			scopes[sc.Path] = sm
		}
	}
}

// sealedScopes is the set of scope paths rooted by sealed components.
func sealedScopes(comps map[string]*Component) map[string]bool {
	var out map[string]bool
	for p, c := range comps {
		if c.Sealed {
			if out == nil {
				out = map[string]bool{}
			}
			out[p] = true
		}
	}
	return out
}

// sealedHolder is the sealed scope among ps, the claimants of one data key
// ("" when none is sealed).
func sealedHolder(ps []string, sealed map[string]bool) string {
	for _, p := range ps {
		if sealed[p] {
			return p
		}
	}
	return ""
}

// SealedComponents lists the registered sealed components, sorted by path.
func (r *Registry) SealedComponents() []*Component {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Component
	for _, c := range r.components {
		if c.Sealed {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
