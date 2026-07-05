// Package registry models the workspace: components (directories with a view
// and/or backend), scopes (app-level groupings), and the workspace manifest
// including the grant table. See docs/elements.md for the builder-facing
// description of everything parsed here.
package registry

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/magik6k/xbin/internal/jsonc"
	"github.com/magik6k/xbin/internal/util"
)

// Use is one entry of a manifest "uses" list: a runtime call grant request.
// Target is a component path ("apps/calendar") or resource ("res:scope/name").
type Use struct {
	Target string `json:"target"`
	Role   string `json:"role"`
}

// Expose declares the callable surface of a component.
type Expose struct {
	// Roles maps role name → human description (mandatory; shown in the
	// grants UI and `bx api`).
	Roles map[string]string `json:"roles,omitempty"`
	// Implies maps role → roles it includes, for custom role names. The
	// conventional admin ⊃ writer ⊃ reader ordering is built in.
	Implies map[string][]string `json:"implies,omitempty"`
}

// TemplateMeta, when present in a manifest, marks the component as a
// **template** (plans/templates.md): a blueprint that is not plugged in
// (no backend, not a live tile) until instantiated into a named copy.
type TemplateMeta struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	DefaultName string `json:"defaultName,omitempty"` // suggested instance basename
}

// Manifest is a component's xbin.json. All fields optional; a bare directory
// with index.html is a valid static component.
type Manifest struct {
	Runtime  string        `json:"runtime,omitempty"` // static|go|node|python|cgi
	Entry    string        `json:"entry,omitempty"`   // runtime-specific; defaults in runner
	Deps     []string      `json:"deps,omitempty"`    // source visibility (deps/ symlinks)
	Uses     []Use         `json:"uses,omitempty"`    // runtime call grant requests
	Expose   *Expose       `json:"expose,omitempty"`
	Inject   *bool         `json:"inject,omitempty"` // false disables D4 HTML injection
	Template *TemplateMeta `json:"template,omitempty"`
	// Setup is a freeform shell script run once at build time to populate the
	// component's environment layer — extra system/runtime deps beyond the base
	// rootfs (e.g. install Ruby). Built in a sandbox with net:internet, cached,
	// rebuilt only when this changes. See plans/component-env.md.
	Setup string `json:"setup,omitempty"`

	// Interfaces are typed capability slots this component REQUESTS; the owner
	// binds each to a provider (plans/interfaces.md). Provides are slots it offers
	// to others (e.g. a firewall tile provides a "net" interface). Keyed by slot.
	Interfaces map[string]Iface `json:"interfaces,omitempty"`
	Provides   map[string]Iface `json:"provides,omitempty"`
}

// Iface declares one interface slot (requested or provided).
type Iface struct {
	Kind    string `json:"kind"`              // net | http | gpu | resource
	Service string `json:"service,omitempty"` // for kind=http: the service contract (e.g. "openai")
	// Role is which exposed role a kind=http PROVIDER grants bound requesters
	// (so the binding is also the call grant). Defaults to "reader".
	Role string `json:"role,omitempty"`
}

// Resource is a broker-provisioned resource declared in scope.json.
type Resource struct {
	Type string `json:"type"` // filesystem|sqlite|kv|blob|bus|cron
}

// ScopeManifest is a scope.json: marks a directory as a scope root.
type ScopeManifest struct {
	Resources map[string]Resource `json:"resources,omitempty"`
	ImportMap map[string]string   `json:"importMap,omitempty"`
}

// Grant is one row of the workspace grant table: caller may call target at role.
type Grant struct {
	From   string `json:"from"`
	Target string `json:"target"`
	Role   string `json:"role"`
}

// WorkspaceManifest is the workspace-root xbin.json. It is machine-managed
// (rewritten as formatted JSON when grants change); comments do not survive.
type WorkspaceManifest struct {
	Schema    int               `json:"schema"`
	ImportMap map[string]string `json:"importMap,omitempty"`
	Grants    []Grant           `json:"grants,omitempty"`
	// Resources declared at workspace level ("res:workspace/<name>" targets).
	Resources map[string]Resource `json:"resources,omitempty"`
	// Bindings wire each component's requested interface slots to a provider
	// (plans/interfaces.md): bindings[component][slot] = provider. Owner-managed.
	Bindings map[string]map[string]string `json:"bindings,omitempty"`
	// Lifecycle holds each component's non-default state (plans/lifecycle.md):
	// "disabled" | "offloaded" | "offloaded-full". Absent = enabled. Owner-managed.
	Lifecycle map[string]string `json:"lifecycle,omitempty"`
	// LifecycleAt records when each component's state last changed (RFC3339), so
	// the UI can tell a "post-disable" backup (a consistent, stopped-state
	// snapshot) from a stale one when gating offload.
	LifecycleAt map[string]string `json:"lifecycleAt,omitempty"`
}

// Lifecycle states (plans/lifecycle.md). Enabled is the implicit default (a
// component with no entry in WorkspaceManifest.Lifecycle).
const (
	StateEnabled       = "enabled"
	StateDisabled      = "disabled"
	StateOffloaded     = "offloaded"      // resource data archived + removed
	StateOffloadedFull = "offloaded-full" // + source/term-env archived + removed
)

// LifecycleState returns a component's lifecycle state, defaulting to enabled.
func (r *Registry) LifecycleState(comp string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if s := r.workspace.Lifecycle[comp]; s != "" {
		return s
	}
	return StateEnabled
}

// IsOffloaded reports whether a state means the component's data is not local.
func IsOffloaded(state string) bool {
	return state == StateOffloaded || state == StateOffloadedFull
}

// Component is a scanned workspace component.
type Component struct {
	Path        string // workspace-relative, slash-separated; also its identity
	Dir         string // absolute
	Manifest    Manifest
	HasIndex    bool   // has index.html (renderable in bx-frame)
	ManifestErr string // parse error text, surfaced by bx doctor / status API
	Scope       string // nearest ancestor scope path; "" = workspace scope
}

// IsTemplate reports whether the component is a template blueprint (not
// plugged in until instantiated).
func (c *Component) IsTemplate() bool { return c.Manifest.Template != nil }

// HasBackend reports whether the component declares a runnable backend. A
// template never runs (it's instantiated first).
func (c *Component) HasBackend() bool {
	if c.IsTemplate() {
		return false
	}
	switch c.Manifest.Runtime {
	case "go", "node", "python", "cgi":
		return true
	}
	return false
}

type Registry struct {
	Root string

	mu         sync.RWMutex
	components map[string]*Component
	scopes     map[string]*ScopeManifest // scope path → manifest
	workspace  WorkspaceManifest
}

func Open(root string) (*Registry, error) {
	r := &Registry{Root: root}
	if err := r.Rescan(); err != nil {
		return nil, err
	}
	return r, nil
}

// Rescan walks the workspace and rebuilds the component/scope tables. It is
// cheap (directory metadata only) and called on debounced file changes.
func (r *Registry) Rescan() error {
	comps := map[string]*Component{}
	scopes := map[string]*ScopeManifest{}

	err := filepath.WalkDir(r.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(r.Root, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		base := d.Name()
		if util.IgnoredDirs[base] || strings.HasPrefix(base, ".") {
			return filepath.SkipDir
		}
		if util.ReservedTop[strings.Split(rel, "/")[0]] {
			return filepath.SkipDir
		}

		if b, err := os.ReadFile(filepath.Join(p, "scope.json")); err == nil {
			sm := &ScopeManifest{}
			if err := jsonc.Unmarshal(b, sm); err == nil {
				scopes[rel] = sm
			} else {
				scopes[rel] = &ScopeManifest{} // still a scope; error surfaces on component
			}
		}

		c := &Component{Path: rel, Dir: p}
		hasManifest := false
		if b, err := os.ReadFile(filepath.Join(p, "xbin.json")); err == nil {
			hasManifest = true
			if err := jsonc.Unmarshal(b, &c.Manifest); err != nil {
				c.ManifestErr = err.Error()
			}
		}
		if _, err := os.Stat(filepath.Join(p, "index.html")); err == nil {
			c.HasIndex = true
		}
		if hasManifest || c.HasIndex {
			comps[rel] = c
		}
		return nil
	})
	if err != nil {
		return err
	}

	ws := WorkspaceManifest{Schema: 1}
	if b, err := os.ReadFile(filepath.Join(r.Root, "xbin.json")); err == nil {
		if err := jsonc.Unmarshal(b, &ws); err != nil {
			return fmt.Errorf("workspace xbin.json: %w", err)
		}
	}

	// Assign nearest-ancestor scope to each component.
	for _, c := range comps {
		c.Scope = nearestScope(scopes, c.Path)
	}

	r.mu.Lock()
	r.components, r.scopes, r.workspace = comps, scopes, ws
	r.mu.Unlock()
	return nil
}

func nearestScope(scopes map[string]*ScopeManifest, compPath string) string {
	p := compPath
	for p != "." && p != "" {
		if _, ok := scopes[p]; ok {
			return p
		}
		p = path.Dir(p)
	}
	return ""
}

func (r *Registry) Component(p string) (*Component, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.components[p]
	return c, ok
}

// Resolve finds the longest component prefix of urlPath ("a/b/c/x.js" →
// component "a/b/c" if registered, else "a/b", …) and returns the remainder.
func (r *Registry) Resolve(urlPath string) (*Component, string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := strings.Trim(urlPath, "/")
	rest := ""
	for p != "" {
		if c, ok := r.components[p]; ok {
			return c, rest, true
		}
		i := strings.LastIndex(p, "/")
		if i < 0 {
			break
		}
		rest = path.Join(p[i+1:], rest)
		p = p[:i]
	}
	return nil, "", false
}

func (r *Registry) Components() []*Component {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Component, 0, len(r.components))
	for _, c := range r.components {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (r *Registry) Scopes() map[string]*ScopeManifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]*ScopeManifest{}
	for k, v := range r.scopes {
		out[k] = v
	}
	return out
}

func (r *Registry) Workspace() WorkspaceManifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.workspace
}

// ImportMapFor returns the merged import map for a component: workspace map
// overlaid with its scope's map (scope wins).
func (r *Registry) ImportMapFor(c *Component) map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]string{}
	for k, v := range r.workspace.ImportMap {
		out[k] = v
	}
	if c != nil && c.Scope != "" {
		if sm, ok := r.scopes[c.Scope]; ok {
			for k, v := range sm.ImportMap {
				out[k] = v
			}
		}
	}
	return out
}

// MutateWorkspace applies fn to the workspace manifest and persists it as
// formatted JSON. Used for grant changes; the file is machine-managed.
func (r *Registry) MutateWorkspace(fn func(*WorkspaceManifest)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(&r.workspace)
	b, err := jsonMarshalIndent(r.workspace)
	if err != nil {
		return err
	}
	tmp := filepath.Join(r.Root, ".xbin.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(r.Root, "xbin.json"))
}
