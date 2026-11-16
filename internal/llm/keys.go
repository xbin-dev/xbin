package llm

import (
	"errors"

	"github.com/xbin-dev/xbin/internal/vault"
)

// keys.go — the upstream keys an admin set, in xbind's vault (docs/llm.md
// "Keys and the vault"). They live in data/llm/keys.json, one map upstream →
// key, sealed whole under a subkey of the vault barrier (label
// "llm/upstream-keys") — the envelope of internal/vault's SealedMap, the one
// store of xbind-held secrets (the workspace agent's outside MCP servers'
// keep theirs in one too, D240), under a key no tile's vault shares.
//
// What the connector does in each vault state:
//
//   - open (unsealed): keys are read and written.
//   - sealed: no admin-set key can be read, so a call to an upstream whose
//     key is in the vault answers 503 naming the sealed vault, and setting
//     a key is refused, until an admin unseals. Nothing is cached across a
//     seal. Upstreams of the host's file keep working: their keys come from
//     files, not from the vault.
//   - locked (production, no barrier set up yet): as sealed; setting a key
//     is refused with how to set the vault up. A key is never written in
//     the clear where a vault is meant to exist.
//   - plaintext (--insecure-vault, --no-auth): the map is kept in the
//     clear, mode 0600, like every vault file in that mode; the first read
//     after a barrier is set up seals it.

// Vault is what the connector needs of xbind's vault barrier
// (*vault.Barrier).
type Vault = vault.Sealer

// keyLabel names the barrier subkey the map is sealed under.
const keyLabel = "llm/upstream-keys"

// The vault states (the admin's view's "vault").
const (
	VaultOpen      = vault.MapOpen
	VaultSealed    = vault.MapSealed
	VaultLocked    = vault.MapLocked
	VaultPlaintext = vault.MapPlaintext
)

var (
	// ErrVaultSealed: an admin-set key can't be read or written now.
	ErrVaultSealed = errors.New("the workspace vault is sealed: an admin unseals it (bx vault unseal, or the admin console) before upstream keys kept in it can be used")
	// ErrVaultLocked: no vault is set up, and keys are never kept in the clear.
	ErrVaultLocked = errors.New("the workspace vault isn't set up yet: an admin sets it up (bx vault unseal) before an upstream key can be stored")
)

// keyStore is the upstream keys' SealedMap, in the connector's words.
type keyStore struct{ m *vault.SealedMap }

func newKeyStore(path string, v Vault, allowPlain bool) *keyStore {
	return &keyStore{m: &vault.SealedMap{Path: path, Label: keyLabel, Name: "data/llm/keys.json", Vault: v, AllowPlain: allowPlain}}
}

func (k *keyStore) state() string { return k.m.State() }

// words is err in the connector's words.
func words(err error) error {
	switch {
	case errors.Is(err, vault.ErrMapSealed):
		return ErrVaultSealed
	case errors.Is(err, vault.ErrMapLocked):
		return ErrVaultLocked
	}
	return err
}

// get is upstream name's key ("" when it has none stored).
func (k *keyStore) get(name string) (string, error) {
	v, err := k.m.Get(name)
	return v, words(err)
}

// set stores (or, with key "", removes) name's key.
func (k *keyStore) set(name, key string) error { return words(k.m.Set(name, key)) }

// hint shows a key as its last four characters.
func hint(key string) string {
	if len(key) < 12 {
		return ""
	}
	return "…" + key[len(key)-4:]
}
