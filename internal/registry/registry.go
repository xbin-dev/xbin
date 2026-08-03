// Package registry models the workspace: components (directories with a view
// and/or backend), scopes (app-level groupings), and the workspace manifest
// including the grant table. See docs/elements.md for the builder-facing
// description of everything parsed here.
package registry

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/xbin-dev/xbin/internal/jsonc"
	"github.com/xbin-dev/xbin/internal/util"
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
	Runtime string   `json:"runtime,omitempty"` // static|go|node|python|cgi
	Entry   string   `json:"entry,omitempty"`   // runtime-specific; defaults in runner
	Deps    []string `json:"deps,omitempty"`    // source visibility (deps/ symlinks)
	Uses    []Use    `json:"uses,omitempty"`    // runtime call grant requests
	Expose  *Expose  `json:"expose,omitempty"`
	Inject  *bool    `json:"inject,omitempty"` // false disables D4 HTML injection
	// Chrome marks a component as trusted workspace chrome (plans/auth.md §6):
	// its frames are NOT sandboxed, so its frontend keeps the ambient session
	// cookie and acts as the signed-in human (like the shell itself). This is
	// the highest-trust manifest flag — settable only by editing xbin.json on
	// the host (the create APIs never write it), never grantable to elements.
	// root and shell are chrome implicitly.
	Chrome   bool          `json:"chrome,omitempty"`
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
	// Exposes are endpoints offered to the OUTSIDE world (plans/ingress.md):
	// http endpoints routed by hostname through an ingress terminator, and
	// stream (tcp/udp) ports relayed from a host port. Keyed by slot; bound by
	// the owner like interfaces (unbound = unreachable, today's default-deny).
	Exposes map[string]ExposeDef `json:"exposes,omitempty"`
}

// Iface declares one interface slot (requested or provided).
type Iface struct {
	Kind    string `json:"kind"`              // net | http | gpu | resource | stream | lan-ingress | ingress (provide)
	Service string `json:"service,omitempty"` // for kind=http: the service contract (e.g. "openai")
	// Role is which exposed role a kind=http PROVIDER grants bound requesters
	// (so the binding is also the call grant). Defaults to "reader".
	Role string `json:"role,omitempty"`
	// Multi (REQUEST side, kind=http only) declares the slot accepts a SET of
	// bindings — the requester explicitly supports multiple inputs and gets a
	// JSON list injected instead of a single URL (plans/interfaces.md
	// §Multiplicity).
	Multi bool `json:"multi,omitempty"`
	// Instances (PROVIDE side) marks the provide as a template: the provider
	// registers concrete instances at runtime (PUT /api/xbin/iface-instances),
	// each addressable as "<provider>#<instance>" in bindings.
	Instances bool `json:"instances,omitempty"`
}

// ExposeDef is one entry of a manifest "exposes" map: an endpoint the
// component offers to the OUTSIDE world (plans/ingress.md). Declaring it is
// inert — the endpoint becomes reachable only when the owner binds the slot
// to an ingress source (the `runtime` builtin or a terminator tile).
type ExposeDef struct {
	Kind string `json:"kind"` // http | stream
	// Paths (kind=http) is the public path allowlist: exact paths, or
	// subtree patterns ending in "/*" ("/*" publishes everything). Requests
	// outside it are refused at the terminator — default-deny.
	Paths []string `json:"paths,omitempty"`
	// Proto/Port (kind=stream): the L4 protocol (tcp default, or udp) and the
	// in-netns port the backend listens on (ordinary net.Listen).
	Proto string `json:"proto,omitempty"`
	Port  int    `json:"port,omitempty"`
}

// ValidateExposes checks a manifest's exposes section; the error names the
// offending slot (surfaced by bx doctor and refused at bind time).
func ValidateExposes(m Manifest) error {
	for slot, def := range m.Exposes {
		if _, dup := m.Interfaces[slot]; dup {
			return fmt.Errorf("exposes slot %q collides with an interfaces slot of the same name", slot)
		}
		switch def.Kind {
		case "http":
			if len(def.Paths) == 0 {
				return fmt.Errorf("exposes.%s: kind=http needs a non-empty \"paths\" allowlist (use [\"/*\"] to publish everything)", slot)
			}
			for _, p := range def.Paths {
				if !strings.HasPrefix(p, "/") {
					return fmt.Errorf("exposes.%s: path %q must start with /", slot, p)
				}
			}
			if def.Port != 0 || def.Proto != "" {
				return fmt.Errorf("exposes.%s: proto/port are for kind=stream", slot)
			}
		case "stream":
			if def.Port < 1 || def.Port > 65535 {
				return fmt.Errorf("exposes.%s: kind=stream needs \"port\" (1-65535), the in-sandbox port the backend listens on", slot)
			}
			switch def.Proto {
			case "", "tcp", "udp":
			default:
				return fmt.Errorf("exposes.%s: proto must be tcp or udp", slot)
			}
			if len(def.Paths) > 0 {
				return fmt.Errorf("exposes.%s: paths are for kind=http", slot)
			}
		default:
			return fmt.Errorf("exposes.%s: kind must be http or stream", slot)
		}
	}
	return nil
}

// StreamProto returns a stream expose's protocol (tcp default).
func (d ExposeDef) StreamProto() string {
	if d.Proto == "" {
		return "tcp"
	}
	return d.Proto
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

// Grant is one row of the workspace grant table: caller may call target at
// role. ApprovedBy/ApprovedAt record who approved it and when (D33 audit —
// "user:<id>", "owner", or the approving element's path); they are
// provenance only and never part of row identity.
type Grant struct {
	From       string `json:"from"`
	Target     string `json:"target"`
	Role       string `json:"role"`
	ApprovedBy string `json:"approvedBy,omitempty"`
	ApprovedAt int64  `json:"approvedAt,omitempty"`
}

// WorkspaceManifest is the workspace-root xbin.json. It is machine-managed
// (rewritten as formatted JSON when grants change); comments do not survive.
type WorkspaceManifest struct {
	Schema    int               `json:"schema"`
	ImportMap map[string]string `json:"importMap,omitempty"`
	Grants    []Grant           `json:"grants,omitempty"`
	// Resources declared at workspace level ("res:workspace/<name>" targets).
	Resources map[string]Resource `json:"resources,omitempty"`
	// Bindings wire each component's requested interface slots to providers
	// (plans/interfaces.md): bindings[component][slot] = provider ref(s).
	// A ref is "<provider>[#<instance>]". Single bindings marshal as a plain
	// string (the pre-multi format); multi-bind slots as an array. Owner-managed.
	Bindings map[string]map[string]Binding `json:"bindings,omitempty"`
	// IfaceInstances holds the runtime-registered instances of provides
	// declared {instances:true}: ifaceInstances[provider][instance] = the
	// provider's API path prefix for that instance ("" = the provider root).
	// Registered by the provider itself (PUT /api/xbin/iface-instances);
	// addressed in bindings as "<provider>#<instance>".
	IfaceInstances map[string]map[string]string `json:"ifaceInstances,omitempty"`
	// IngressHosts holds the concrete hostnames a component self-registered
	// inside a delegated zone binding (plans/ingress.md ING-2), keyed by
	// component. Registered via PUT /api/xbin/ingress-hosts, bounded to the
	// zones the owner bound — the runtime half of the hostname authority.
	IngressHosts map[string][]string `json:"ingressHosts,omitempty"`
	// Lifecycle holds each component's non-default state (plans/lifecycle.md):
	// "disabled" | "offloaded" | "offloaded-full". Absent = enabled. Owner-managed.
	Lifecycle map[string]string `json:"lifecycle,omitempty"`
	// LifecycleAt records when each component's state last changed (RFC3339), so
	// the UI can tell a "post-disable" backup (a consistent, stopped-state
	// snapshot) from a stale one when gating offload.
	LifecycleAt map[string]string `json:"lifecycleAt,omitempty"`
}

// BindRef is one bound provider/source: the ref plus optional route config
// (plans/ingress.md — bindings CARRY CONFIG for exposed endpoints; interface
// bindings never set the extra fields). A config-free ref marshals as the
// plain string it always was.
type BindRef struct {
	Ref string `json:"ref"` // "<provider>[#<instance>]", or an ingress source ("runtime" / a tile path)
	// Host/Zone (http exposes): the hostname authority this binding grants —
	// an exact public host, or a delegated wildcard zone ("*.sites.example.com")
	// the tile registers concrete hosts within (PUT /api/xbin/ingress-hosts).
	Host string `json:"host,omitempty"`
	Zone string `json:"zone,omitempty"`
	// Listen (stream exposes bound to runtime): the HOST listen address
	// (":2456", "0.0.0.0:443"); defaults to ":<expose port>".
	Listen string `json:"listen,omitempty"`
}

// bare reports whether the ref carries no config (marshals as a string).
func (r BindRef) bare() bool { return r.Host == "" && r.Zone == "" && r.Listen == "" }

func (r BindRef) MarshalJSON() ([]byte, error) {
	if r.bare() {
		return json.Marshal(r.Ref)
	}
	type raw BindRef // shed the method set
	return json.Marshal(raw(r))
}

func (r *BindRef) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*r = BindRef{Ref: s}
		return nil
	}
	type raw BindRef
	var v raw
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*r = BindRef(v)
	return nil
}

// Binding is the ordered provider set bound to one slot. The common
// single-provider case marshals as a plain string — every pre-multi workspace
// manifest parses unchanged and stays in its original format; refs carrying
// ingress route config marshal as objects.
type Binding []BindRef

// First returns the sole/first provider ref ("" when unbound) — the accessor
// for slots that are single-valued by construction (net, @archive, …).
func (b Binding) First() string {
	if len(b) == 0 {
		return ""
	}
	return b[0].Ref
}

// FirstRef returns the sole/first entry with its route config (zero value
// when unbound) — the accessor for exposed-endpoint slots.
func (b Binding) FirstRef() BindRef {
	if len(b) == 0 {
		return BindRef{}
	}
	return b[0]
}

// Refs returns just the ref strings (display, set comparisons).
func (b Binding) Refs() []string {
	out := make([]string, len(b))
	for i, r := range b {
		out[i] = r.Ref
	}
	return out
}

// BindTo builds a config-free Binding from ref strings.
func BindTo(refs ...string) Binding {
	out := make(Binding, len(refs))
	for i, r := range refs {
		out[i] = BindRef{Ref: r}
	}
	return out
}

func (b Binding) MarshalJSON() ([]byte, error) {
	if len(b) == 1 {
		return json.Marshal(b[0])
	}
	return json.Marshal([]BindRef(b))
}

func (b *Binding) UnmarshalJSON(data []byte) error {
	var one BindRef
	if err := json.Unmarshal(data, &one); err == nil {
		if one == (BindRef{}) {
			*b = nil
		} else {
			*b = Binding{one}
		}
		return nil
	}
	var list []BindRef
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	*b = Binding(list)
	return nil
}

// Lifecycle states (plans/lifecycle.md). Enabled is the implicit default (a
// component with no entry in WorkspaceManifest.Lifecycle).
const (
	StateEnabled       = "enabled"
	StateDisabled      = "disabled"
	StateHidden        = "hidden"         // disabled + filtered out of sidebars/listings (D42)
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
			} else if err := ValidateExposes(c.Manifest); err != nil {
				// Surfaced like a parse error (bx ls/doctor, status API); the
				// component keeps serving — publishing just refuses at bind.
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
