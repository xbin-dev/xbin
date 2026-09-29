package vault

import (
	"encoding/json"
	"errors"
	"fmt"
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
	if kf.Memory > 4<<20 || kf.Time > 64 { // a hostile descriptor mustn't take the host's memory or hours
		return nil, errors.New("vault keyfile: its key-derivation parameters are out of range")
	}
	return &Barrier{kf: &kf}, nil
}
