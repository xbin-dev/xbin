package broker

import (
	"net/http"
	"os"
	"sort"

	"github.com/magik6k/buxon/internal/auth"
	"github.com/magik6k/buxon/internal/registry"
	"github.com/magik6k/buxon/internal/server"
	"github.com/magik6k/buxon/internal/vault"
)

// Admin-console aggregate endpoints (plans/admin-tile.md). All gated by
// IsAdmin; they only summarize state the admin could already fetch piecemeal.

func (b *Broker) registerAdmin(srv *server.Server) {
	srv.RegisterAPI("GET /vaults", b.apiVaults)
	srv.RegisterAPI("GET /resources", b.apiResources)
	srv.RegisterAPI("GET /auth-overview", b.apiAuthOverview)
	b.registerCode(srv)
}

func (b *Broker) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if b.IsAdmin(auth.PrincipalOf(r)) {
		return true
	}
	server.WriteJSON(w, http.StatusForbidden, map[string]string{
		"error": "admin only — needs the buxon:admin capability", "docs": "/docs/auth.md",
	})
	return false
}

// GET /vaults — every vault and its key names (values via /vault/<c>/<k>).
func (b *Broker) apiVaults(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	// Listing key names requires decryption, so a sealed barrier can't serve
	// this — unseal first.
	if b.vaultSealed() {
		b.vaultError(w, vault.ErrSealed)
		return
	}
	type entry struct {
		Component string   `json:"component"`
		Keys      []string `json:"keys"`
	}
	// Any component may have a vault; the file is named by CompKey. Walk the
	// registry so we report by component path, not opaque key.
	out := []entry{}
	for _, c := range b.Reg.Components() {
		m, err := b.vaultRead(c.Path)
		if err != nil || len(m) == 0 {
			continue
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out = append(out, entry{Component: c.Path, Keys: keys})
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// GET /resources — declared resources across the workspace and all scopes.
func (b *Broker) apiResources(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	type res struct {
		ID    string `json:"id"`    // res:<scope>/<name>
		Scope string `json:"scope"` // "" = workspace
		Name  string `json:"name"`
		Type  string `json:"type"`
	}
	out := []res{}
	add := func(scope string, m map[string]registry.Resource) {
		for name, rr := range m {
			rt := resTarget{Scope: scope, Name: name}
			out = append(out, res{ID: rt.String(), Scope: scope, Name: name, Type: rr.Type})
		}
	}
	add("", b.Reg.Workspace().Resources)
	for scope, sm := range b.Reg.Scopes() {
		add(scope, sm.Resources)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	server.WriteJSON(w, http.StatusOK, out)
}

// GET /auth-overview — one call powering the admin overview tab.
func (b *Broker) apiAuthOverview(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	ws := b.Reg.Workspace()

	type comp struct {
		Path     string            `json:"path"`
		Runtime  string            `json:"runtime,omitempty"`
		Scope    string            `json:"scope,omitempty"`
		Roles    map[string]string `json:"roles,omitempty"`
		Uses     []registry.Use    `json:"uses,omitempty"`
		HasVault bool              `json:"hasVault"`
		State    string            `json:"state,omitempty"` // lifecycle; "" = enabled
		Manifest string            `json:"manifestError,omitempty"`
	}
	comps := []comp{}
	exposed := 0
	for _, c := range b.Reg.Components() {
		ci := comp{Path: c.Path, Runtime: c.Manifest.Runtime, Scope: c.Scope,
			Uses: c.Manifest.Uses, Manifest: c.ManifestErr}
		if s := b.Reg.LifecycleState(c.Path); s != registry.StateEnabled {
			ci.State = s
		}
		if c.Manifest.Expose != nil && len(c.Manifest.Expose.Roles) > 0 {
			ci.Roles = c.Manifest.Expose.Roles
			exposed++
		}
		// File existence, not decryption — works while the barrier is sealed.
		if _, err := os.Stat(b.vaultPath(c.Path)); err == nil {
			ci.HasVault = true
		}
		comps = append(comps, ci)
	}

	pending := b.Pending()
	grants := ws.Grants
	if grants == nil {
		grants = []registry.Grant{} // [] not null; the tile does .length on it
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"components": comps,
		"grants":     grants,
		"pending":    pending,
		"counts": map[string]int{
			"components": len(comps),
			"exposed":    exposed,
			"grants":     len(grants),
			"pending":    len(pending),
		},
	})
}
