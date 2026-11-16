package vault

// sealedmap.go — a small map of secrets xbind itself holds (not a tile's),
// kept in one file sealed whole under a subkey of the barrier: the LLM
// connector's upstream keys (internal/llm/keys.go, label
// "llm/upstream-keys") and the workspace agent's outside MCP servers'
// secrets (internal/delegation, label "delegation/mcp-secrets"; D240). The
// envelope is a tile vault file's, under a key no tile's vault shares.
//
// In each vault state:
//
//   - open (unsealed): read and written; a map found in the clear (kept
//     before a barrier existed) is sealed at the first read.
//   - sealed: nothing can be read or written (ErrMapSealed) until an admin
//     unseals; nothing decrypted is kept across a seal.
//   - locked (production, no barrier yet): as sealed for a sealed file
//     (ErrMapLocked); a write is refused — a secret is never written in the
//     clear where a vault is meant to exist.
//   - plaintext (--insecure-vault, --no-auth): the map is kept in the
//     clear, mode 0600, like every vault file in that mode.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// Sealer is what a SealedMap needs of the barrier (*Barrier).
type Sealer interface {
	Initialized() bool
	Sealed() bool
	EncryptFor(label string, plaintext []byte) ([]byte, error)
	DecryptFor(label string, blob []byte) ([]byte, error)
}

// The states of a SealedMap's vault.
const (
	MapOpen      = "open"
	MapSealed    = "sealed"
	MapLocked    = "locked"
	MapPlaintext = "plaintext"
)

var (
	// ErrMapSealed: the vault is sealed — nothing in the map can be read or
	// written until an admin unseals it.
	ErrMapSealed = errors.New("the workspace vault is sealed")
	// ErrMapLocked: no vault is set up, and secrets are never kept in the clear.
	ErrMapLocked = errors.New("the workspace vault isn't set up yet")
)

// mapEnvelope is the file: sealed (enc 1) or, in the plaintext mode, the map.
type mapEnvelope struct {
	Enc  int               `json:"enc,omitempty"`
	Data []byte            `json:"data,omitempty"`
	Keys map[string]string `json:"keys,omitempty"`
}

// SealedMap is a map name → secret in one file, sealed under Label. The
// zero value with Path and Label set is ready; Vault nil is no barrier.
type SealedMap struct {
	Path  string // the file
	Label string // the barrier subkey it is sealed under
	// Name is how errors name the file ("data/llm/keys.json").
	Name       string
	Vault      Sealer
	AllowPlain bool // the plaintext vault mode

	mu     sync.Mutex
	cache  map[string]string
	cached bool
}

// State is the map's vault state now.
func (k *SealedMap) State() string {
	switch {
	case k.Vault != nil && k.Vault.Initialized() && k.Vault.Sealed():
		return MapSealed
	case k.Vault != nil && k.Vault.Initialized():
		return MapOpen
	case k.AllowPlain:
		return MapPlaintext
	}
	return MapLocked
}

func (k *SealedMap) name() string {
	if k.Name != "" {
		return k.Name
	}
	return filepath.Base(k.Path)
}

// load reads the map. k.mu held.
func (k *SealedMap) load() (map[string]string, error) {
	st := k.State()
	if st == MapSealed {
		k.cache, k.cached = nil, false // nothing decrypted outlives a seal
		return nil, ErrMapSealed
	}
	if k.cached {
		return k.cache, nil
	}
	b, err := os.ReadFile(k.Path)
	if os.IsNotExist(err) {
		k.cache, k.cached = map[string]string{}, true
		return k.cache, nil
	}
	if err != nil {
		return nil, err
	}
	var env mapEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, errors.New(k.name() + " is unreadable")
	}
	m := env.Keys
	if env.Enc > 0 {
		if st != MapOpen {
			return nil, ErrMapLocked // sealed, and no barrier to open it with
		}
		pt, err := k.Vault.DecryptFor(k.Label, env.Data)
		if err != nil {
			return nil, errors.New(k.name() + " can't be opened with this vault")
		}
		m = map[string]string{}
		if err := json.Unmarshal(pt, &m); err != nil {
			return nil, errors.New(k.name() + " is unreadable")
		}
	}
	if m == nil {
		m = map[string]string{}
	}
	k.cache, k.cached = m, true
	if env.Enc == 0 && st == MapOpen && len(m) > 0 {
		_ = k.save(m) // kept in the clear before the barrier existed: seal it now
	}
	return m, nil
}

// save writes the map. k.mu held.
func (k *SealedMap) save(m map[string]string) error {
	var env mapEnvelope
	switch k.State() {
	case MapSealed:
		return ErrMapSealed
	case MapLocked:
		return ErrMapLocked
	case MapPlaintext:
		env.Keys = m
	default:
		pt, err := json.Marshal(m)
		if err != nil {
			return err
		}
		ct, err := k.Vault.EncryptFor(k.Label, pt)
		if err != nil {
			return err
		}
		env.Enc, env.Data = 1, ct
	}
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(k.Path), 0o700); err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(k.Path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	k.cache, k.cached = m, true
	return nil
}

// Get is name's secret ("" when none is stored).
func (k *SealedMap) Get(name string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return "", err
	}
	return m[name], nil
}

// Names are the names that hold a secret, sorted.
func (k *SealedMap) Names() ([]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// Set stores (or, with secret "", removes) name's secret.
func (k *SealedMap) Set(name, secret string) error {
	return k.Update(func(m map[string]string) {
		if secret == "" {
			delete(m, name)
		} else {
			m[name] = secret
		}
	})
}

// Update changes the map in one write: change gets a copy to edit. A
// change that leaves the map as it was writes nothing.
func (k *SealedMap) Update(change func(map[string]string)) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	cur, err := k.load()
	if err != nil {
		return err
	}
	next := make(map[string]string, len(cur))
	for n, v := range cur {
		next[n] = v
	}
	change(next)
	if sameMap(cur, next) {
		return nil
	}
	return k.save(next)
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for n, v := range a {
		if w, ok := b[n]; !ok || w != v {
			return false
		}
	}
	return true
}
