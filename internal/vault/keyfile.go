package vault

import (
	"encoding/json"
	"errors"
	"fmt"
)

// The largest key-derivation parameters FromKeyfile takes.
const (
	maxKeyfileMemory  = 1 << 20 // KiB: 1 GiB
	maxKeyfileThreads = 16
)

// Keyfile answers the barrier's descriptor, as .barrier.json holds it: the
// Argon2id parameters and the DEK wrapped under the passphrase's KEK —
// nothing usable without the passphrase. A backup key bundle carries it
// (plans/partitions/11-backup-encryption.md §5).
func (b *Barrier) Keyfile() ([]byte, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.kf == nil {
		return nil, ErrNotInited
	}
	return json.MarshalIndent(b.kf, "", "  ")
}

// FromKeyfile is a barrier over another workspace's descriptor, held in
// memory only: Unseal it with that workspace's passphrase to open what its
// DEK sealed. It is never persisted — no path, so Init and Rekey fail —
// and Seal drops its DEK like any barrier's.
func FromKeyfile(data []byte) (*Barrier, error) {
	var kf keyfile
	if err := json.Unmarshal(data, &kf); err != nil {
		return nil, fmt.Errorf("vault keyfile corrupt: %w", err)
	}
	if kf.KDF != "argon2id" || len(kf.Salt) == 0 || len(kf.WrappedDEK) == 0 || kf.Time == 0 || kf.Memory == 0 || kf.Threads == 0 {
		return nil, errors.New("vault keyfile corrupt: not an argon2id barrier descriptor")
	}
	// A hostile or damaged descriptor mustn't take the host's memory, its
	// CPUs or hours: at most 1 GiB (16× this barrier's own), 16 threads and
	// 64 passes.
	if kf.Memory > maxKeyfileMemory || kf.Threads > maxKeyfileThreads || kf.Time > 64 {
		return nil, errors.New("vault keyfile: its key-derivation parameters are out of range")
	}
	return &Barrier{kf: &kf}, nil
}
