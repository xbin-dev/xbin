package registry

// sealed.go — sealed components in the registry (D227). A sealed
// component's code is the running xbind's own:
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
	"slices"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/jsonc"
	"github.com/xbin-dev/xbin/internal/util"
)

// SealedCode is one sealed component as the registry registers it. Beyond
// its code, everything a workspace manifest would decide for a tile is the
// table's here: its partition mode, the interface slots it has, the grants
// it holds.
type SealedCode struct {
	Path string // its reserved path, under util.SealedPrefix
	// Dir is the read-only tree xbind materialized from the embedded source
	// (xbind's own data, under .xbin/): what backends run in and files are
	// served from.
	Dir string
	// Title is what people see it called; "" = its manifest's.
	Title string
	// Manifest and Scope are its xbin.json and scope.json (nil: none), from
	// the embedded source.
	Manifest, Scope []byte
	// Partition is its partition request (nil: one instance), in place of
	// the manifest's.
	Partition []string
	// Slots are the interface slots it has: the manifest's other slots are
	// dropped.
	Slots []string
	// Grants are the grants the table gives it, read as rows of the grant
	// table (Workspace) that no workspace file holds.
	Grants []Use
	// NoData, when set, says why its data must not be touched: its scope
	// then declares no resources — nothing is provisioned, mounted, backed
	// up or removed for it — and the reason is its manifest error.
	NoData string
}

// addSealed registers the sealed components over whatever the walk found:
// the walk never yields a path under xbin/, and a table entry wins anyway.
// Each roots a scope of its own, declared or not: a component without one
// would fall into the workspace scope and hold its resources.
func (r *Registry) addSealed(comps map[string]*Component, scopes map[string]*ScopeManifest) []SealedCode {
	if r.Sealed == nil {
		return nil
	}
	codes := r.Sealed()
	for _, sc := range codes {
		if !util.SealedPath(sc.Path) {
			continue // the table is checked where it is loaded; never register outside the namespace
		}
		c := &Component{Path: sc.Path, Dir: sc.Dir, Sealed: true}
		if sc.Manifest != nil {
			c.readManifest(sc.Manifest)
		}
		// the table's, never the manifest's
		c.Manifest.Partition = nil
		if sc.Partition != nil {
			c.Manifest.Partition = PartitionList(sc.Partition)
		}
		c.Manifest.Template, c.Manifest.Chrome, c.Manifest.Deps = nil, false, nil
		// never a target: no roles to grant, nothing a slot binds to, no
		// public endpoint — no tile calls into it or reaches a person's
		// partition of it; people use it at their own access level
		c.Manifest.Expose, c.Manifest.Provides, c.Manifest.Exposes = nil, nil, nil
		c.Manifest.VM, c.Manifest.Setup = nil, "" // the release's sandbox, as shipped
		for slot := range c.Manifest.Interfaces {
			if !slices.Contains(sc.Slots, slot) {
				delete(c.Manifest.Interfaces, slot)
			}
		}
		if sc.Title != "" {
			c.Manifest.Title = sc.Title
		}
		c.HasIndex = regularFile(filepath.Join(sc.Dir, "index.html"))
		c.Native, c.NativeErr = c.resolveNative()
		comps[sc.Path] = c
		sm := &ScopeManifest{}
		if sc.Scope != nil {
			if err := jsonc.Unmarshal(sc.Scope, sm); err == nil {
				sm.dropInvalidResources(sc.Path)
			} else {
				sm = &ScopeManifest{}
			}
		}
		if sc.NoData != "" {
			sm.Resources = nil
			sm.Err = sc.NoData
		}
		scopes[sc.Path] = sm
	}
	return codes
}

// sealedView is the workspace manifest as everything but its own file
// reads it (Workspace): no row of the file speaks for, or reaches into, a
// sealed component — a grant from one, a grant on one or on its resources,
// a binding of a slot the table doesn't give it — and the table's grants
// are there as rows. The file itself is never rewritten for this.
func sealedView(ws WorkspaceManifest, codes []SealedCode) WorkspaceManifest {
	sealedTarget := func(t string) bool {
		return util.SealedPath(t) || strings.HasPrefix(t, "res:"+util.SealedPrefix)
	}
	if slices.ContainsFunc(ws.Grants, func(g Grant) bool { return util.SealedPath(g.From) || sealedTarget(g.Target) }) || len(codes) > 0 {
		kept := make([]Grant, 0, len(ws.Grants))
		for _, g := range ws.Grants {
			if !util.SealedPath(g.From) && !sealedTarget(g.Target) {
				kept = append(kept, g)
			}
		}
		for _, sc := range codes {
			for _, u := range sc.Grants {
				kept = append(kept, Grant{From: sc.Path, Target: u.Target, Role: u.Role, ApprovedBy: "xbin"})
			}
		}
		ws.Grants = kept
	}
	ws.Bindings = withoutSealedProviders(ws.Bindings)
	// a sealed component is switched off, never offloaded (there is no
	// source or archive to bring back): a file that says so reads disabled
	for comp, state := range ws.Lifecycle {
		if util.SealedPath(comp) && IsOffloaded(state) {
			lc := make(map[string]string, len(ws.Lifecycle))
			for k, v := range ws.Lifecycle {
				if util.SealedPath(k) && IsOffloaded(v) {
					v = StateDisabled
				}
				lc[k] = v
			}
			ws.Lifecycle = lc
			break
		}
	}
	for comp, slots := range ws.Bindings {
		if !util.SealedPath(comp) {
			continue
		}
		var has []string
		for _, sc := range codes {
			if sc.Path == comp {
				has = sc.Slots
			}
		}
		kept := map[string]Binding{}
		for slot, b := range slots {
			if slices.Contains(has, slot) {
				kept[slot] = b
			}
		}
		if len(kept) != len(slots) {
			out := make(map[string]map[string]Binding, len(ws.Bindings))
			for k, v := range ws.Bindings {
				out[k] = v
			}
			if len(kept) == 0 {
				delete(out, comp)
			} else {
				out[comp] = kept
			}
			ws.Bindings = out
		}
	}
	return ws
}

// withoutSealedProviders drops every binding ref that names a sealed
// component as its provider (a sealed component is never a target),
// copying only when the file holds one.
func withoutSealedProviders(all map[string]map[string]Binding) map[string]map[string]Binding {
	sealedRef := func(r BindRef) bool { return util.SealedPath(r.Ref) }
	dirty := false
	for _, slots := range all {
		for _, b := range slots {
			dirty = dirty || slices.ContainsFunc(b, sealedRef)
		}
	}
	if !dirty {
		return all
	}
	out := make(map[string]map[string]Binding, len(all))
	for comp, slots := range all {
		kept := make(map[string]Binding, len(slots))
		for slot, b := range slots {
			if b = slices.DeleteFunc(slices.Clone(b), sealedRef); len(b) > 0 {
				kept[slot] = b
			}
		}
		if len(kept) > 0 {
			out[comp] = kept
		}
	}
	return out
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
