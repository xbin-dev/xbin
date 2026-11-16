package broker

// pendingbind.go — the bind-on-install prompt's rows (GET /bindings
// pending): every requested slot with no binding yet, with the providers
// that can satisfy it and whether it is required or, its tile's required
// slots all bound, optional (docs/overview/11-interfaces.md).

import (
	"sort"

	"github.com/xbin-dev/xbin/internal/registry"
)

// pendingBind is a requested interface slot that is not yet bound, with the
// providers that could satisfy it — the data behind the owner's bind-on-install
// prompt (mirrors a pending `uses` grant request).
type pendingBind struct {
	Component string `json:"component"`
	Slot      string `json:"slot"`
	Kind      string `json:"kind"`
	Service   string `json:"service,omitempty"`
	Multi     bool   `json:"multi,omitempty"`
	// Expose marks an unbound EXPOSED endpoint (plans/ingress.md): binding it
	// publishes the tile, and the bind call carries route config (host/zone/
	// listen), so UIs render it with the route editor, not a plain picker.
	Expose  bool         `json:"expose,omitempty"`
	Options []bindOption `json:"options"`
	// Default names the builtin the slot resolves to WITHOUT a binding — "org"
	// on an org-owned tile with network sets (D54). Such a slot is satisfied;
	// binding it only narrows or overrides.
	Default string `json:"default,omitempty"`
	// Approvable: the viewer may wire this slot (workspace admin, or an admin
	// of the org that owns the tile — D26). False on a tile the viewer merely
	// owns or writes: the prompt lists it for information, bind is not
	// offered (mirrors /grants pending[].approvable).
	Approvable bool `json:"approvable"`
	// Required: the manifest marks the slot "required" — the tile can't do
	// its work unbound. Optional: it isn't, and every slot the tile marks
	// required is bound, so the tile works as it is: UIs fold such rows
	// into one line rather than put them over the tile (a tile that marks
	// none is never optional — nothing says what it needs).
	Required bool `json:"required,omitempty"`
	Optional bool `json:"optional,omitempty"`
}

// requiredBound: the interfaces mark at least one slot required, and every
// one of them is bound.
func requiredBound(ifaces map[string]registry.Iface, bound map[string]registry.Binding) bool {
	has := false
	for slot, req := range ifaces {
		if !req.Required {
			continue
		}
		has = true
		if len(bound[slot]) == 0 {
			return false
		}
	}
	return has
}

// orEmpty keeps a slot nobody provides as "options": [] on the wire — a
// null there broke the shell's binding panel for every slot (an agent's
// unprovided mcp slot hid them all).
func orEmpty(o []bindOption) []bindOption {
	if o == nil {
		return []bindOption{}
	}
	return o
}

// pendingBindings lists every requested interface slot with no binding yet.
func (b *Broker) pendingBindings(wsAdmin bool) []pendingBind {
	var out []pendingBind
	for _, c := range b.Reg.Components() {
		if registry.IsOffloaded(b.Reg.LifecycleState(c.Path)) {
			continue // offloaded components make no live bind requests
		}
		ready := requiredBound(c.Manifest.Interfaces, b.Reg.Workspace().Bindings[c.Path])
		for slot, req := range c.Manifest.Interfaces {
			if len(b.Reg.Workspace().Bindings[c.Path][slot]) > 0 {
				continue // already bound (multi slots grow via the bindings UI)
			}
			pb := pendingBind{
				Component: c.Path, Slot: slot, Kind: req.Kind, Service: req.Service,
				Multi:    req.Multi,
				Options:  orEmpty(b.bindOptions(c.Path, req, wsAdmin)),
				Required: req.Required, Optional: !req.Required && ready,
			}
			if req.Kind == "net" && b.orgNetDefault(c.Path) {
				pb.Default = NetRefOrg
			} else if req.Kind == "net" && b.personalNetDefault(c.Path) {
				pb.Default = NetRefPersonal
			}
			out = append(out, pb)
		}
		for slot, def := range c.Manifest.Exposes {
			if len(b.Reg.Workspace().Bindings[c.Path][slot]) > 0 {
				continue
			}
			out = append(out, pendingBind{
				Component: c.Path, Slot: slot, Kind: def.Kind, Expose: true,
				Options: orEmpty(b.exposeBindOptions(c.Path, def)),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Component != out[j].Component {
			return out[i].Component < out[j].Component
		}
		return out[i].Slot < out[j].Slot
	})
	return out
}
