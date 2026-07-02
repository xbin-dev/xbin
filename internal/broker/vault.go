package broker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/magik6k/buxon/internal/auth"
	"github.com/magik6k/buxon/internal/server"
	"github.com/magik6k/buxon/internal/util"
)

// Vault: per-element private secrets (plans/auth.md §4). One JSON file per
// component under data/vault/, owned by buxond, mode 0600. Elements reach
// only their own vault; the owner reaches all (via API — that's the point:
// raw file perms close it to everyone else at tier 2). No cross-element
// sharing by design: wrap shared secrets behind a role-guarded API instead.

func (b *Broker) vaultPath(comp string) string {
	return filepath.Join(b.Reg.Root, "data", "vault", util.CompKey(comp)+".json")
}

func (b *Broker) vaultRead(comp string) (map[string]string, error) {
	out := map[string]string{}
	bts, err := os.ReadFile(b.vaultPath(comp))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	return out, json.Unmarshal(bts, &out)
}

func (b *Broker) vaultWrite(comp string, m map[string]string) error {
	p := b.vaultPath(comp)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	bts, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, bts, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// vaultAccess parses {rest...} into (component, key) using the component
// registry for the split, and authorizes: owner always, element only itself.
func (b *Broker) vaultAccess(w http.ResponseWriter, r *http.Request) (comp, key string, ok bool) {
	rest := strings.Trim(r.PathValue("rest"), "/")
	c, remainder, found := b.Reg.Resolve(rest)
	if !found {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such component", "docs": "/docs/auth.md"})
		return "", "", false
	}
	p := auth.PrincipalOf(r)
	// Own vault always; otherwise an admin-capable principal (owner or a
	// buxon:admin tile) may manage any vault — that's the admin console's
	// password-manager function. No *unprivileged* cross-element access.
	if p.Component != c.Path && !b.IsAdmin(p) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{
			"error": "vaults are private to their element; cross-vault access needs buxon:admin",
			"docs":  "/docs/auth.md",
		})
		return "", "", false
	}
	return c.Path, remainder, true
}

func (b *Broker) apiVaultGet(w http.ResponseWriter, r *http.Request) {
	comp, key, ok := b.vaultAccess(w, r)
	if !ok {
		return
	}
	m, err := b.vaultRead(comp)
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if key == "" { // list
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		server.WriteJSON(w, http.StatusOK, map[string]any{"keys": keys})
		return
	}
	v, found := m[key]
	if !found {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such key"})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"value": v})
}

func (b *Broker) apiVaultPut(w http.ResponseWriter, r *http.Request) {
	comp, key, ok := b.vaultAccess(w, r)
	if !ok {
		return
	}
	if key == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "missing key"})
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := decodeJSON(r, &body); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {\"value\": …}"})
		return
	}
	m, err := b.vaultRead(comp)
	if err == nil {
		m[key] = body.Value
		err = b.vaultWrite(comp, m)
	}
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (b *Broker) apiVaultDelete(w http.ResponseWriter, r *http.Request) {
	comp, key, ok := b.vaultAccess(w, r)
	if !ok {
		return
	}
	m, err := b.vaultRead(comp)
	if err == nil {
		delete(m, key)
		err = b.vaultWrite(comp, m)
	}
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
