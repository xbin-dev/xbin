// store.go — where the tile keeps things: two kv resources (`state`, this
// instance's own; `conf`, written by global and read by people's
// partitions) and the vault (secrets only). Behind small interfaces so the
// tests run the whole tile in memory; production is the sdk's.
package main

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// errNotFound is a missing key in a kv or the vault.
var errNotFound = errors.New("not found")

// kvStore is a kv resource: JSON values by key.
type kvStore interface {
	Get(key string, v any) error // errNotFound when missing
	Put(key string, v any) error
	Delete(key string) error
	List(prefix string) ([]string, error)
}

// vaultStore is this instance's vault: secrets by name, never logged.
type vaultStore interface {
	Get(name string) (secretString, error) // errNotFound when missing
	Set(name string, v secretString) error
	Delete(name string) error
}

// sdkKV is a kv resource through xbind.
type sdkKV struct{ kv *xbin.KVStore }

func newSDKKV(name string) kvStore {
	res := xbin.Resource(name)
	if res == "" {
		return nil
	}
	return sdkKV{xbin.KV(res)}
}

func (k sdkKV) Get(key string, v any) error {
	err := k.kv.GetJSON(key, v)
	if errors.Is(err, xbin.ErrNotFound) {
		return errNotFound
	}
	return err
}
func (k sdkKV) Put(key string, v any) error { return k.kv.PutJSON(key, v) }
func (k sdkKV) Delete(key string) error {
	if err := k.kv.Delete(key); err != nil && !errors.Is(err, xbin.ErrNotFound) {
		return err
	}
	return nil
}
func (k sdkKV) List(prefix string) ([]string, error) { return k.kv.List(prefix) }

// sdkVault is this instance's vault through xbind (a partition's is its own).
type sdkVault struct{}

func (sdkVault) Get(name string) (secretString, error) {
	v, err := xbin.Secret(name)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return secretString{}, errNotFound
		}
		return secretString{}, err
	}
	if v == "" {
		return secretString{}, errNotFound
	}
	return newSecret(v), nil
}
func (sdkVault) Set(name string, v secretString) error { return xbin.SetSecret(name, v.Reveal()) }
func (sdkVault) Delete(name string) error              { return xbin.DeleteSecret(name) }

// memKV is a kv in memory: the tests' stores, and the stand-in for a
// resource this instance doesn't have (an unpartitioned copy's conf).
type memKV struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMemKV() *memKV { return &memKV{m: map[string][]byte{}} }

func (k *memKV) Get(key string, v any) error {
	k.mu.Lock()
	b, ok := k.m[key]
	k.mu.Unlock()
	if !ok {
		return errNotFound
	}
	return json.Unmarshal(b, v)
}

func (k *memKV) Put(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	k.mu.Lock()
	k.m[key] = b
	k.mu.Unlock()
	return nil
}

func (k *memKV) Delete(key string) error {
	k.mu.Lock()
	delete(k.m, key)
	k.mu.Unlock()
	return nil
}

func (k *memKV) List(prefix string) ([]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []string
	for key := range k.m {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			out = append(out, key)
		}
	}
	return out, nil
}

// memVault is a vault in memory (tests).
type memVault struct {
	mu sync.Mutex
	m  map[string]string
}

func newMemVault() *memVault { return &memVault{m: map[string]string{}} }

func (v *memVault) Get(name string) (secretString, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	s, ok := v.m[name]
	if !ok {
		return secretString{}, errNotFound
	}
	return newSecret(s), nil
}

func (v *memVault) Set(name string, s secretString) error {
	v.mu.Lock()
	v.m[name] = s.Reveal()
	v.mu.Unlock()
	return nil
}

func (v *memVault) Delete(name string) error {
	v.mu.Lock()
	delete(v.m, name)
	v.mu.Unlock()
	return nil
}

// vaultJSON reads a JSON value kept as one vault secret (a token with its
// expiry): the vault holds strings only.
func vaultJSON(v vaultStore, name string, out any) error {
	s, err := v.Get(name)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(s.Reveal()), out)
}

func setVaultJSON(v vaultStore, name string, val any) error {
	b, err := json.Marshal(val)
	if err != nil {
		return err
	}
	return v.Set(name, newSecret(string(b)))
}
