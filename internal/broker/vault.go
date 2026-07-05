package broker

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/magik6k/buxon/internal/auth"
	"github.com/magik6k/buxon/internal/server"
	"github.com/magik6k/buxon/internal/util"
	"github.com/magik6k/buxon/internal/vault"
)

// Vault: per-element private secrets (plans/auth.md §4). One JSON file per
// component under data/vault/, owned by buxond, mode 0600. Elements reach
// only their own vault; the owner reaches all (via API — that's the point:
// raw file perms close it to everyone else at tier 2). No cross-element
// sharing by design: wrap shared secrets behind a role-guarded API instead.
//
// At rest the file is one of two formats (self-describing):
//   - encrypted envelope {"enc":1,"data":"<b64 nonce||ciphertext>"} — the whole
//     key→value map (key names included) sealed with the vault barrier
//     (internal/vault). Written whenever the barrier is unsealed.
//   - legacy plaintext {"key":"value", …} — pre-encryption / no barrier
//     configured. Migrated to encrypted form on the next barrier init.

type vaultEnvelope struct {
	Enc  int    `json:"enc"`  // format version (1)
	Data []byte `json:"data"` // barrier ciphertext of the JSON map
}

// errVaultUnconfigured is returned when a write is attempted with no
// encryption barrier and plaintext is not allowed (production default).
var errVaultUnconfigured = errors.New(
	"vault encryption not configured — set BUXON_VAULT_PASSPHRASE (or unseal the barrier) before storing secrets")

func (b *Broker) vaultPath(comp string) string {
	return filepath.Join(b.Reg.Root, "data", "vault", util.CompKey(comp)+".json")
}

// vaultSealed reports whether reads/writes are currently blocked because an
// initialized barrier is sealed.
func (b *Broker) vaultSealed() bool {
	return b.barrier != nil && b.barrier.Initialized() && b.barrier.Sealed()
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
	// Sniff the format: an object with an "enc" field is an encrypted envelope.
	var env vaultEnvelope
	if json.Unmarshal(bts, &env) == nil && env.Enc > 0 {
		if b.barrier == nil || b.barrier.Sealed() {
			return nil, vault.ErrSealed
		}
		pt, err := b.barrier.Decrypt(env.Data)
		if err != nil {
			return nil, fmt.Errorf("vault decrypt %s: %w", comp, err)
		}
		return out, json.Unmarshal(pt, &out)
	}
	return out, json.Unmarshal(bts, &out)
}

func (b *Broker) vaultWrite(comp string, m map[string]string) error {
	p := b.vaultPath(comp)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	plain, err := json.Marshal(m)
	if err != nil {
		return err
	}
	var bts []byte
	// Encrypt whenever a barrier is available and unsealed. Without a barrier,
	// only write plaintext when explicitly allowed (dev / --insecure-vault);
	// otherwise refuse, so production can never persist secrets in the clear.
	if b.barrier != nil && b.barrier.Initialized() {
		if b.barrier.Sealed() {
			return vault.ErrSealed
		}
		ct, err := b.barrier.Encrypt(plain)
		if err != nil {
			return err
		}
		if bts, err = json.MarshalIndent(vaultEnvelope{Enc: 1, Data: ct}, "", "  "); err != nil {
			return err
		}
	} else if b.AllowInsecureVault {
		if bts, err = json.MarshalIndent(m, "", "  "); err != nil {
			return err
		}
	} else {
		return errVaultUnconfigured
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, bts, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// migrateVaults re-encrypts any legacy plaintext vault files now that the
// barrier is unsealed. Called after a first-time Init. Idempotent.
func (b *Broker) migrateVaults() {
	dir := filepath.Join(b.Reg.Root, "data", "vault")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		bts, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var env vaultEnvelope
		if json.Unmarshal(bts, &env) == nil && env.Enc > 0 {
			continue // already encrypted
		}
		var m map[string]string
		if json.Unmarshal(bts, &m) != nil {
			continue
		}
		ct, err := b.barrier.Encrypt(bts)
		if err != nil {
			continue
		}
		out, _ := json.MarshalIndent(vaultEnvelope{Enc: 1, Data: ct}, "", "  ")
		tmp := p + ".tmp"
		if os.WriteFile(tmp, out, 0o600) == nil {
			_ = os.Rename(tmp, p)
			slog.Info("vault: migrated legacy plaintext to encrypted", "file", e.Name())
		}
	}
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
		b.vaultError(w, err)
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
		b.vaultError(w, err)
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
		b.vaultError(w, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// vaultError maps a sealed barrier to 503 (retry after unseal) and anything
// else to 500.
func (b *Broker) vaultError(w http.ResponseWriter, err error) {
	if errors.Is(err, vault.ErrSealed) {
		server.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "vault is sealed — unseal it first (bx vault unseal, or the admin console)",
			"docs":  "/docs/auth.md",
		})
		return
	}
	if errors.Is(err, errVaultUnconfigured) {
		server.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": err.Error(), "docs": "/docs/auth.md",
		})
		return
	}
	server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

// --- seal/unseal API (owner or buxon:admin) ---

func (b *Broker) apiVaultStatus(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	st := b.barrier.Status()
	// mode: unsealed (encryption active) | sealed (encrypted, needs unseal) |
	// plaintext (no barrier, plaintext allowed) | unconfigured (no barrier,
	// locked — unseal to set it up, writes refused until then).
	mode := "unconfigured"
	switch {
	case st.Initialized && !st.Sealed:
		mode = "unsealed"
	case st.Initialized:
		mode = "sealed"
	case b.AllowInsecureVault:
		mode = "plaintext"
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"initialized": st.Initialized,
		"sealed":      st.Sealed,
		"mode":        mode,
		"insecure":    mode == "plaintext",
	})
}

func (b *Broker) apiVaultUnseal(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	var body struct {
		Passphrase string `json:"passphrase"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Passphrase == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {\"passphrase\": …}"})
		return
	}
	inited := b.barrier.Initialized()
	if err := b.UnsealOrInit(body.Passphrase); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, vault.ErrBadPassphrase) {
			code = http.StatusForbidden
		}
		server.WriteJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"sealed": false, "initialized": true, "created": !inited,
	})
}

func (b *Broker) apiVaultSeal(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	b.barrier.Seal()
	b.SealResources() // stop stateful components + unmount encrypted resources
	server.WriteJSON(w, http.StatusOK, map[string]any{"sealed": true})
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
