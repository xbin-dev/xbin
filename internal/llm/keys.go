package llm

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// keys.go — the upstream keys an admin set, in xbind's vault (docs/llm.md
// "Keys and the vault"). They live in data/llm/keys.json, one map upstream →
// key, sealed whole under a subkey of the vault barrier (label
// "llm/upstream-keys") — the same envelope a tile's vault file uses, under
// a key no tile's vault shares.
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
type Vault interface {
	Initialized() bool
	Sealed() bool
	EncryptFor(label string, plaintext []byte) ([]byte, error)
	DecryptFor(label string, blob []byte) ([]byte, error)
}

// keyLabel names the barrier subkey the map is sealed under.
const keyLabel = "llm/upstream-keys"

// The vault states (the admin's view's "vault").
const (
	VaultOpen      = "open"
	VaultSealed    = "sealed"
	VaultLocked    = "locked"
	VaultPlaintext = "plaintext"
)

var (
	// ErrVaultSealed: an admin-set key can't be read or written now.
	ErrVaultSealed = errors.New("the workspace vault is sealed: an admin unseals it (bx vault unseal, or the admin console) before upstream keys kept in it can be used")
	// ErrVaultLocked: no vault is set up, and keys are never kept in the clear.
	ErrVaultLocked = errors.New("the workspace vault isn't set up yet: an admin sets it up (bx vault unseal) before an upstream key can be stored")
)

// keyEnvelope is data/llm/keys.json: sealed (enc 1) or, in the plaintext
// vault mode, the map itself.
type keyEnvelope struct {
	Enc  int               `json:"enc,omitempty"`
	Data []byte            `json:"data,omitempty"`
	Keys map[string]string `json:"keys,omitempty"`
}

type keyStore struct {
	path       string
	vault      Vault // nil: no barrier (tests, the plaintext mode's shape)
	allowPlain bool

	mu     sync.Mutex
	cache  map[string]string
	cached bool
}

func (k *keyStore) state() string {
	switch {
	case k.vault != nil && k.vault.Initialized() && k.vault.Sealed():
		return VaultSealed
	case k.vault != nil && k.vault.Initialized():
		return VaultOpen
	case k.allowPlain:
		return VaultPlaintext
	}
	return VaultLocked
}

// load reads the map. k.mu held.
func (k *keyStore) load() (map[string]string, error) {
	st := k.state()
	if st == VaultSealed {
		k.cache, k.cached = nil, false // nothing decrypted outlives a seal
		return nil, ErrVaultSealed
	}
	if k.cached {
		return k.cache, nil
	}
	b, err := os.ReadFile(k.path)
	if os.IsNotExist(err) {
		k.cache, k.cached = map[string]string{}, true
		return k.cache, nil
	}
	if err != nil {
		return nil, err
	}
	var env keyEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, errors.New("data/llm/keys.json is unreadable")
	}
	m := env.Keys
	if env.Enc > 0 {
		if st != VaultOpen {
			return nil, ErrVaultLocked // sealed keys, and no barrier to open them with
		}
		pt, err := k.vault.DecryptFor(keyLabel, env.Data)
		if err != nil {
			return nil, errors.New("data/llm/keys.json can't be opened with this vault")
		}
		m = map[string]string{}
		if err := json.Unmarshal(pt, &m); err != nil {
			return nil, errors.New("data/llm/keys.json is unreadable")
		}
	}
	if m == nil {
		m = map[string]string{}
	}
	k.cache, k.cached = m, true
	if env.Enc == 0 && st == VaultOpen && len(m) > 0 {
		_ = k.save(m) // kept in the clear before the barrier existed: seal it now
	}
	return m, nil
}

// save writes the map. k.mu held.
func (k *keyStore) save(m map[string]string) error {
	var env keyEnvelope
	switch k.state() {
	case VaultSealed:
		return ErrVaultSealed
	case VaultLocked:
		return ErrVaultLocked
	case VaultPlaintext:
		env.Keys = m
	default:
		pt, err := json.Marshal(m)
		if err != nil {
			return err
		}
		ct, err := k.vault.EncryptFor(keyLabel, pt)
		if err != nil {
			return err
		}
		env.Enc, env.Data = 1, ct
	}
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(k.path), 0o700); err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(k.path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	k.cache, k.cached = m, true
	return nil
}

// get is upstream name's key ("" when it has none stored).
func (k *keyStore) get(name string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return "", err
	}
	return m[name], nil
}

// set stores (or, with key "", removes) name's key.
func (k *keyStore) set(name, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	cur, err := k.load()
	if err != nil {
		return err
	}
	next := cloneMap(cur)
	if key == "" {
		if _, has := next[name]; !has {
			return nil
		}
		delete(next, name)
	} else {
		next[name] = key
	}
	return k.save(next)
}

// hint shows a key as its last four characters.
func hint(key string) string {
	if len(key) < 12 {
		return ""
	}
	return "…" + key[len(key)-4:]
}
