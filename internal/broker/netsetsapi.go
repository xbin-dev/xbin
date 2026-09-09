package broker

import (
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// Organisation network sets (plans/DECISIONS.md D54): the ws-admin API,
// the restart fan-out after an edit, and the net labels the console and
// `bx status` render. Gated like permission sets (admin or xbin:users).

// GET /net-sets → {sets:{name:{rules,created}}, attachedTo:{name:[orgs]}}
func (b *Broker) apiNetSetsList(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	sets := st.NetSets()
	attached := map[string][]string{}
	for name := range sets {
		attached[name] = st.NetSetAttachedTo(name)
		if attached[name] == nil {
			attached[name] = []string{}
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"sets": sets, "attachedTo": attached})
}

// PUT /net-sets/{name} {rules:[…]} — create or replace; attached orgs' tiles
// restart so the new reach applies.
func (b *Broker) apiNetSetPut(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	name := r.PathValue("name")
	var body struct {
		Rules []string `json:"rules"`
	}
	if err := server.DecodeJSON(r, &body); err != nil || body.Rules == nil {
		server.WriteError(w, http.StatusBadRequest, "need {rules: [\"internet\" | \"internet:<host|host-glob|ip|cidr>[:port]\" | \"lan:<cidr>\" | \"host\" | \"provider:<tile-glob>\", …]}", "/docs/auth.md")
		return
	}
	if err := st.UpsertNetSet(name, users.NetSet{Rules: body.Rules}); err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	ns, _ := st.NetSet(name)
	slog.Info("net set updated", "name", name, "rules", ns.Rules, "by", humanID(auth.PrincipalOf(r)))
	b.netSetsChanged(st.NetSetAttachedTo(name))
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, map[string]any{"name": name, "rules": ns.Rules, "created": ns.Created,
		"attachedTo": st.NetSetAttachedTo(name)})
}

// DELETE /net-sets/{name} — 409 while any org references it.
func (b *Broker) apiNetSetDelete(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	name := r.PathValue("name")
	if err := st.DeleteNetSet(name); err != nil {
		code := http.StatusBadRequest
		if strings.Contains(err.Error(), "attached") {
			code = http.StatusConflict
		} else if strings.Contains(err.Error(), "no such") {
			code = http.StatusNotFound
		}
		server.WriteError(w, code, err.Error())
		return
	}
	slog.Info("net set deleted", "name", name, "by", humanID(auth.PrincipalOf(r)))
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// netSetsChanged restarts what the orgs' reach materializes at spawn: every
// org-owned tile that declares a net interface (its relay policy / host
// netns follows the set union), plus the provider tiles those tiles are
// STORED as bound to (their client roster may change — the transfer.go
// trick: resolution may already say "" after the change). Terminals apply
// at their next spawn.
func (b *Broker) netSetsChanged(orgs []string) {
	if b.Users == nil || len(orgs) == 0 {
		return
	}
	tiles := map[string]bool{}
	providers := map[string]bool{}
	for _, org := range orgs {
		for _, tile := range b.Users.OwnedBy(users.OwnerKindOrg + ":" + org) {
			c, ok := b.Reg.Component(tile)
			if !ok {
				continue
			}
			for slot, req := range c.Manifest.Interfaces {
				if req.Kind != "net" {
					continue
				}
				tiles[tile] = true
				for _, ref := range b.Reg.Workspace().Bindings[tile][slot] {
					prov := providerPath(ref.Ref)
					if p, ok := b.Reg.Component(prov); ok && providesNet(p) {
						providers[prov] = true
					}
				}
			}
		}
	}
	all := make([]string, 0, len(tiles)+len(providers))
	for t := range tiles {
		all = append(all, t)
	}
	for p := range providers {
		if !tiles[p] {
			all = append(all, p)
		}
	}
	sort.Strings(all)
	for _, t := range all {
		if b.OnGrantChange != nil {
			b.OnGrantChange(t) // respawns only running backends
		}
		b.Hub.Publish(events.Event{Type: "grants", Component: t})
	}
}

// NetLabel describes a component's effective network for the console and
// `bx status`: the stored/defaulted ref, the effective mode, the relay rules
// (when any), where the reach comes from, and why it is inert (if it is).
type NetLabel struct {
	Ref       string   `json:"netRef,omitempty"`    // "org", "internet", "lan:…", a provider path, "" (unbound/none)
	Effective string   `json:"net,omitempty"`       // host | relay | splice | none
	Rules     []string `json:"netRules,omitempty"`  // relay rules in force
	Source    string   `json:"netSource,omitempty"` // "org:sales (devs-net, infra-net)" for org reach
	Note      string   `json:"netNote,omitempty"`   // inert reason
}

// netIfaceSlot returns the component's net interface slot name, if any.
func netIfaceSlot(c *registry.Component) (string, bool) {
	for slot, req := range c.Manifest.Interfaces {
		if req.Kind == "net" {
			return slot, true
		}
	}
	return "", false
}

func (b *Broker) NetLabel(comp string) NetLabel {
	c, ok := b.Reg.Component(comp)
	if !ok {
		return NetLabel{}
	}
	if _, has := netIfaceSlot(c); !has {
		return NetLabel{}
	}
	nb := b.netBinding(comp)
	out := NetLabel{Ref: nb, Effective: "none"}
	if nb == "" {
		// Distinguish an explicit/unbound slot from an inert one.
		if slot, has := netIfaceSlot(c); has {
			if ref := b.Reg.Workspace().Bindings[comp][slot].First(); ref != "" {
				out.Ref = ref
			}
		}
		if reason, ok := b.inertNet.Load(comp); ok {
			out.Note = reason.(string)
		}
		return out
	}
	switch {
	case b.NetHostShare(c):
		out.Effective = "host"
	case b.netProvider(comp) != "":
		out.Effective = "splice"
	default:
		pol := b.EgressFor(c)
		if !pol.Empty() {
			out.Effective = "relay"
			for _, r := range pol.Rules {
				out.Rules = append(out.Rules, r.String())
			}
		}
	}
	if nb == NetRefOrg && b.Users != nil {
		ceil := b.Users.Ceiling(comp)
		out.Source = "org:" + ceil.OwnerOrg() + " (" + strings.Join(ceil.NetSets(), ", ") + ")"
	}
	return out
}
