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
	// ID names the code: the short digest of the tree at Dir. It changes
	// exactly when a release changes the component's code.
	ID string
	// Dir is the read-only tree xbind materialized from the embedded source
	// (xbind's own data, under .xbin/): what backends run in and files are
	// served from.
	Dir string
	// Files is the tree itself, as the release holds it in memory: slash
	// path → bytes (read-only). What is served of a sealed component is
	// served from here, never from the copy at Dir (Registry.SealedFile).
	Files map[string][]byte
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
	// Defaults binds a slot (of Slots) to a builtin provider while the
	// workspace file has no entry for the slot and the provider is
	// available (Registry.SealedProvider): slot → ref.
	Defaults map[string]string
	// Providers names, per slot, the providers the slot may be bound to
	// beside its default: slot → refs. A slot binds to nothing else,
	// whoever asks (BindingAllowed).
	Providers map[string][]string
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
		c := &Component{Path: sc.Path, Dir: sc.Dir, Sealed: true, SealedID: sc.ID}
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

// BindingAllowed reports whether the sealed component sc may have slot
// bound to the provider ref ("<provider>[#<instance>]"): a slot the table
// gives it, and its default provider or one the table lists for the slot.
// Who a sealed component talks to is the release's decision — its model
// provider reads what people say to it — never a workspace file's alone.
func (sc SealedCode) BindingAllowed(slot, ref string) bool {
	if !slices.Contains(sc.Slots, slot) || ref == "" {
		return false
	}
	bare, _, _ := strings.Cut(ref, "#")
	return bare == sc.Defaults[slot] || slices.Contains(sc.Providers[slot], bare)
}

// SealedRowError is MutateWorkspace's refusal of a change that would write
// a row about a sealed component the file may not hold.
type SealedRowError struct{ Row string }

func (e *SealedRowError) Error() string {
	return "the workspace file takes no such row about a system component (" + e.Row + "): it is managed by xbin"
}

// sealedRows names what a workspace file must not say about sealed
// components: the rows sealedView would not honour. MutateWorkspace refuses
// a change that adds one, so whatever a request's handler read, no such row
// is written; a row already in a hand-edited file is left (and ignored).
func sealedRows(ws WorkspaceManifest, codes []SealedCode) map[string]bool {
	rows := map[string]bool{}
	for _, g := range ws.Grants {
		if util.SealedPath(g.From) || util.SealedTarget(g.Target) {
			rows["grant "+g.From+" → "+g.Target+" ("+g.Role+")"] = true
		}
	}
	code := func(path string) (SealedCode, bool) {
		i := slices.IndexFunc(codes, func(sc SealedCode) bool { return sc.Path == path })
		if i < 0 {
			return SealedCode{}, false
		}
		return codes[i], true
	}
	for comp, slots := range ws.Bindings {
		for slot, b := range slots {
			sc, known := code(comp)
			for _, ref := range b {
				switch {
				case util.SealedPath(ref.Ref):
					rows["binding "+comp+" "+slot+" → "+ref.Ref] = true
				case util.SealedPath(comp) && (!known || !sc.BindingAllowed(slot, ref.Ref)):
					rows["binding "+comp+" "+slot+" → "+ref.Ref] = true
				}
			}
			if util.SealedPath(comp) && len(b) == 0 && (!known || !slices.Contains(sc.Slots, slot)) {
				rows["binding "+comp+" "+slot] = true
			}
		}
	}
	for comp := range ws.IfaceInstances {
		if util.SealedPath(comp) {
			rows["ifaceInstances "+comp] = true
		}
	}
	for comp := range ws.IngressHosts {
		if util.SealedPath(comp) {
			rows["ingressHosts "+comp] = true
		}
	}
	for comp, state := range ws.Lifecycle {
		if util.SealedPath(comp) && IsOffloaded(state) {
			rows["lifecycle "+comp+" "+state] = true
		}
	}
	return rows
}

// sealedView is the workspace manifest as everything but its own file
// reads it (Workspace): no row of the file speaks for, or reaches into, a
// sealed component — a grant from one, a grant on one, on its resources or
// on its source, a binding of a slot the table doesn't give it or to a
// provider the table doesn't allow, an instance or ingress host of one —
// and the table's grants are there as rows. The file itself is never
// rewritten for this.
func sealedView(ws WorkspaceManifest, codes []SealedCode, available func(ref string) bool) WorkspaceManifest {
	sealedTarget := util.SealedTarget
	ws.IfaceInstances = withoutSealedKeys(ws.IfaceInstances)
	ws.IngressHosts = withoutSealedKeys(ws.IngressHosts)
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
	ws.Bindings = withSealedDefaults(ws.Bindings, codes, available)
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
		var code SealedCode
		for _, sc := range codes {
			if sc.Path == comp {
				code = sc
			}
		}
		kept := map[string]Binding{}
		for slot, b := range slots {
			// a declared slot, bound (or unbound: the empty entry) to
			// providers the table allows; anything else reads as no entry
			if slices.Contains(code.Slots, slot) && !slices.ContainsFunc(b, func(r BindRef) bool { return !code.BindingAllowed(slot, r.Ref) }) {
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

// withSealedDefaults binds each sealed component's defaulted slots that the
// file says nothing about, while their provider is available. A slot the
// file has an entry for — a binding, or the empty one an unbind leaves
// (broker.apiBindingSet) — is the admin's and is left alone. Copies only
// when a default applies.
func withSealedDefaults(all map[string]map[string]Binding, codes []SealedCode, available func(ref string) bool) map[string]map[string]Binding {
	if available == nil {
		return all
	}
	out, copied := all, false
	for _, sc := range codes {
		for slot, ref := range sc.Defaults {
			if _, explicit := all[sc.Path][slot]; explicit || !slices.Contains(sc.Slots, slot) || !available(ref) {
				continue
			}
			if !copied {
				out, copied = make(map[string]map[string]Binding, len(all)+1), true
				for k, v := range all {
					out[k] = v
				}
			}
			slots := make(map[string]Binding, len(out[sc.Path])+1)
			for k, v := range out[sc.Path] {
				slots[k] = v
			}
			slots[slot] = BindTo(ref)
			out[sc.Path] = slots
		}
	}
	return out
}

// SealedDefault is the provider the sealed component at comp binds slot to
// by default ("" = none), and whether that default is what binds it now:
// the workspace file has no entry for the slot and the provider is
// available.
func (r *Registry) SealedDefault(comp, slot string) (ref string, applied bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, sc := range r.sealed {
		if sc.Path == comp {
			ref = sc.Defaults[slot]
		}
	}
	if ref == "" {
		return "", false
	}
	_, explicit := r.workspace.Bindings[comp][slot]
	return ref, !explicit && r.SealedProvider != nil && r.SealedProvider(ref)
}

// SealedFile answers file rel (a slash path) of the sealed component at
// path from the release's own bytes: the bytes; or dir when rel is a
// directory of its tree; ok false when it has no such file. The bytes are
// the release's and must not be changed.
func (r *Registry) SealedFile(path, rel string) (data []byte, dir, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rel = strings.Trim(rel, "/")
	for _, sc := range r.sealed {
		if sc.Path != path {
			continue
		}
		if b, has := sc.Files[rel]; has {
			return b, false, true
		}
		for name := range sc.Files {
			if rel == "" || strings.HasPrefix(name, rel+"/") {
				return nil, true, true
			}
		}
	}
	return nil, false, false
}

// SealedBindingAllowed reports whether the sealed component at comp may
// have slot bound to ref (SealedCode.BindingAllowed); false for a path
// that is no registered sealed component.
func (r *Registry) SealedBindingAllowed(comp, slot, ref string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, sc := range r.sealed {
		if sc.Path == comp {
			return sc.BindingAllowed(slot, ref)
		}
	}
	return false
}

// withoutSealedKeys is m without its entries for paths under xbin/,
// copying only when it has one.
func withoutSealedKeys[V any](m map[string]V) map[string]V {
	dirty := false
	for k := range m {
		dirty = dirty || util.SealedPath(k)
	}
	if !dirty {
		return m
	}
	out := make(map[string]V, len(m))
	for k, v := range m {
		if !util.SealedPath(k) {
			out[k] = v
		}
	}
	return out
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
