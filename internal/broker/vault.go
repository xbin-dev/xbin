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

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"

	"github.com/xbin-dev/xbin/internal/vault"
)

// Vault: per-element private secrets (plans/auth.md §4). One JSON file per
// component under data/vault/, owned by xbind, mode 0600. Elements reach
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
	"vault encryption not configured — set XBIN_VAULT_PASSPHRASE (or unseal the barrier) before storing secrets")

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
		return b.vaultOpen(comp, env.Data)
	}
	return out, json.Unmarshal(bts, &out)
}

// vaultOpen decrypts a sealed vault map; what names the vault in errors.
func (b *Broker) vaultOpen(what string, data []byte) (map[string]string, error) {
	if b.barrier == nil || b.barrier.Sealed() {
		return nil, vault.ErrSealed
	}
	pt, err := b.barrier.Decrypt(data)
	if err != nil {
		return nil, fmt.Errorf("vault decrypt %s: %w", what, err)
	}
	out := map[string]string{}
	return out, json.Unmarshal(pt, &out)
}

// vaultSeal is how a vault map is kept at rest: sealed with the barrier
// whenever one is initialized (the ciphertext), in plain (nil) only when
// explicitly allowed (dev / --insecure-vault); otherwise refused, so
// production can never persist secrets in the clear.
func (b *Broker) vaultSeal(m map[string]string) ([]byte, error) {
	switch {
	case b.barrier != nil && b.barrier.Initialized():
		if b.barrier.Sealed() {
			return nil, vault.ErrSealed
		}
		plain, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		return b.barrier.Encrypt(plain)
	case b.AllowInsecureVault:
		return nil, nil
	}
	return nil, errVaultUnconfigured
}

func (b *Broker) vaultWrite(comp string, m map[string]string) error {
	p := b.vaultPath(comp)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	ct, err := b.vaultSeal(m)
	if err != nil {
		return err
	}
	var bts []byte
	if ct != nil {
		bts, err = json.MarshalIndent(vaultEnvelope{Enc: 1, Data: ct}, "", "  ")
	} else {
		bts, err = json.MarshalIndent(m, "", "  ")
	}
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(p, bts, 0o600)
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
		if fsutil.WriteFileAtomic(p, out, 0o600) == nil {
			slog.Info("vault: migrated legacy plaintext to encrypted", "file", e.Name())
		}
	}
	b.migrateDeploymentVaults() // the vaults beyond main, below data/vault/.deployments (deployvault.go)
	b.migratePartitionVaults()  // people's partitions', below data/vault/.partitions (partitionvault.go)
}

// vaultAccess parses {rest...} into (component, key) using the component
// registry for the split, and authorizes: owner always, element only itself.
// The deployment whose vault the call reaches is the one the caller
// addresses (vaultDeployment, deployvault.go): for a tile without deployments,
// always main, today's file.
func (b *Broker) vaultAccess(w http.ResponseWriter, r *http.Request) (vaultCall, bool) {
	rest := strings.Trim(r.PathValue("rest"), "/")
	c, remainder, found := b.Reg.Resolve(rest)
	if !found {
		server.WriteError(w, http.StatusNotFound, "no such component", "/docs/auth.md")
		return vaultCall{}, false
	}
	p := auth.PrincipalOf(r)
	// Who reaches a vault at all (D30): the element's own BACKEND (instance
	// token) and its tile TERMINALS (management from the shell), plus
	// admin-capable principals (owner or a xbin:admin tile) who may MANAGE
	// any vault — list keys, set/rotate, delete (the admin console's
	// password-manager function). Value READS are backend-only, enforced in
	// apiVaultGet. A tile's FRONTEND (frame token) gets nothing: anyone who
	// can merely open a tile must not see or edit its secrets — route secret
	// use through the backend.
	if p.Via == "frame" {
		server.WriteError(w, http.StatusForbidden, "the vault API is not reachable from a tile frontend — secrets are handled by the tile's backend (D30)", "/docs/auth.md")
		return vaultCall{}, false
	}
	if b.partitionParamRefused(w, r, c.Path) {
		return vaultCall{}, false
	}
	dep, named, ok := b.vaultDeployment(w, r, p, c.Path)
	if !ok {
		return vaultCall{}, false
	}
	call := vaultCall{comp: c.Path, dep: dep, key: remainder, named: named}
	if !b.vaultPartition(w, p, &call) { // a person's partition: its own vault (partitionvault.go)
		return vaultCall{}, false
	}
	return call, true
}

func (b *Broker) apiVaultGet(w http.ResponseWriter, r *http.Request) {
	c, ok := b.vaultAccess(w, r)
	if !ok {
		return
	}
	// A secret's VALUE is readable ONLY by the element's BACKEND (instance
	// token) — not admin/owner, and not the tile's terminals either (D30):
	// everyone else can list keys and set/rotate secrets (write-only
	// management), but never exfiltrate values. This holds the line even
	// when org membership confers terminal on a credential-bearing tile.
	// Per deployment: only the backend of the deployment whose vault it is.
	if p := auth.PrincipalOf(r); c.key != "" && valueRefused(p, c) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{
			"error": "a secret's value is readable only by the tile's backend; admins and tile terminals can list and set secrets, not read them (D30)",
			"docs":  "/docs/auth.md",
		})
		return
	}
	m, err := b.vaultReadCall(c)
	if err != nil {
		b.vaultError(w, err)
		return
	}
	if c.key == "" { // list
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := map[string]any{"keys": keys}
		if c.part == nil && !b.isPrimary(c.comp, c.dep) { // the primary's key names with no value here (D127i)
			if out["placeholders"], err = b.vaultPlaceholders(c.comp, c.dep, m); err != nil {
				b.vaultError(w, err)
				return
			}
		}
		vaultAnswer(w, c, out)
		return
	}
	v, found := m[c.key]
	if !found {
		b.placeholderRead(w, c)
		return
	}
	vaultAnswer(w, c, map[string]any{"value": v})
}

func (b *Broker) apiVaultPut(w http.ResponseWriter, r *http.Request) {
	c, ok := b.vaultAccess(w, r)
	if !ok {
		return
	}
	if c.key == "" {
		server.WriteError(w, http.StatusBadRequest, "missing key")
		return
	}
	if !b.vaultWritable(w, auth.PrincipalOf(r), c) {
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := server.DecodeJSON(r, &body); err != nil {
		server.WriteError(w, http.StatusBadRequest, "need {\"value\": …}")
		return
	}
	m, err := b.vaultReadCall(c)
	if err == nil {
		m[c.key] = body.Value
		err = b.vaultWriteCall(c, m)
	}
	if err != nil {
		b.vaultError(w, err)
		return
	}
	vaultOK(w, c)
}

func (b *Broker) apiVaultDelete(w http.ResponseWriter, r *http.Request) {
	c, ok := b.vaultAccess(w, r)
	if !ok {
		return
	}
	if !b.vaultWritable(w, auth.PrincipalOf(r), c) {
		return
	}
	m, err := b.vaultReadCall(c)
	if err == nil {
		delete(m, c.key)
		err = b.vaultWriteCall(c, m)
	}
	if err != nil {
		b.vaultError(w, err)
		return
	}
	vaultOK(w, c)
}

// vaultError maps a sealed barrier to 503 (retry after unseal) and anything
// else to 500.
func (b *Broker) vaultError(w http.ResponseWriter, err error) {
	if errors.Is(err, vault.ErrSealed) {
		server.WriteError(w, http.StatusServiceUnavailable, "vault is sealed — unseal it first (bx vault unseal, or the admin console)", "/docs/auth.md")
		return
	}
	if errors.Is(err, errVaultUnconfigured) {
		server.WriteError(w, http.StatusServiceUnavailable, err.Error(), "/docs/auth.md")
		return
	}
	if se := (statusErr{}); errors.As(err, &se) { // a person's partition's vault while its tile is paused (partitionvault.go)
		server.WriteError(w, se.code, se.msg, "/docs/partitions.md")
		return
	}
	server.WriteError(w, http.StatusInternalServerError, err.Error())
}

// --- seal/unseal API (owner or xbin:admin) ---

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
	if err := server.DecodeJSON(r, &body); err != nil || body.Passphrase == "" {
		server.WriteError(w, http.StatusBadRequest, "need {\"passphrase\": …}")
		return
	}
	inited := b.barrier.Initialized()
	if err := b.UnsealOrInit(body.Passphrase); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, vault.ErrBadPassphrase) {
			code = http.StatusForbidden
		}
		server.WriteError(w, code, err.Error())
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

// apiVaultRekey changes the barrier passphrase (re-wraps the DEK; no data
// re-encryption). Requires the barrier unsealed AND the current passphrase —
// an admin session alone can't silently rotate the credential out from under
// whoever holds it.
func (b *Broker) apiVaultRekey(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := server.DecodeJSON(r, &body); err != nil || body.New == "" {
		server.WriteError(w, http.StatusBadRequest, "need {\"current\": …, \"new\": …}")
		return
	}
	if !b.barrier.Initialized() {
		server.WriteError(w, http.StatusConflict, "no barrier yet — set the first passphrase via unseal")
		return
	}
	if b.barrier.Sealed() {
		server.WriteError(w, http.StatusConflict, "vault is sealed — unseal before changing the passphrase")
		return
	}
	if err := b.barrier.CheckPassphrase(body.Current); err != nil {
		server.WriteError(w, http.StatusForbidden, "current passphrase is wrong")
		return
	}
	if err := b.barrier.Rekey(body.New); err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Backup key bundles exported so far wrap the same DEK under the old
	// passphrase: the nudges ask for a fresh one (backupkeys_status.go).
	if err := b.backupKeys().passphraseChanged(); err != nil {
		slog.Warn("vault rekey: marking the backup key exports stale", "err", err)
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"rekeyed": true})
}
