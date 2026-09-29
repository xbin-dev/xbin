package broker

// backupkeys_status.go — how the backup keys stand against the key bundles
// exported (plans/partitions/11-backup-encryption.md §5; owner ruling H3):
// GET /backup-keys, the admin /alerts nudge, bx doctor and the admin
// console's Backup tab all read backupKeysStatus. exports.json records the
// exports; a passphrase change (POST /vault/rekey) marks them stale — a
// bundle opens with the passphrase in force when it was exported, so the
// nudge asks for a fresh one — and the counts are cached until the store
// next writes, so an /alerts poll doesn't parse every key file.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// backupKeyCounts are the export status' counts: the key files and
// tombstones read once per store write.
type backupKeyCounts struct {
	Keys, Unexported, Erased, ErasedSinceExport int
	ex                                          backupExports
}

func (s *backupKeyStore) exports() backupExports {
	var e backupExports
	if data, err := os.ReadFile(filepath.Join(s.dir, backupExportsFile)); err == nil {
		_ = json.Unmarshal(data, &e)
	}
	return e
}

func (s *backupKeyStore) writeExports(e backupExports) error {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return s.writeFile(backupExportsFile, data)
}

// passphraseChanged marks every export stale after the vault's passphrase
// changed: the bundles hold the data key wrapped under the old one, and
// still open with it. The nudges ask for a fresh export until one is made.
// Nothing when no bundle was ever exported.
func (s *backupKeyStore) passphraseChanged() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ex := s.exports()
	if ex.Last == "" {
		return nil
	}
	ex.Rekeyed, ex.IDs = nowStamp(time.Now()), nil
	return s.writeExports(ex)
}

// countsNow answers the cached counts, reading them when a write dropped
// them.
func (s *backupKeyStore) countsNow() (backupKeyCounts, error) {
	if c := s.counts.Load(); c != nil {
		return *c, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.list()
	if err != nil {
		return backupKeyCounts{}, err
	}
	tombs, err := s.tombstones()
	if err != nil {
		return backupKeyCounts{}, err
	}
	c := backupKeyCounts{Keys: len(keys), Erased: len(tombs), ex: s.exports()}
	for _, k := range keys {
		if !slices.Contains(c.ex.IDs, k.ID) {
			c.Unexported++
		}
	}
	for _, t := range tombs {
		if c.ex.Last == "" || t.ErasedAt > c.ex.Last {
			c.ErasedSinceExport++
		}
	}
	s.counts.Store(&c)
	return c, nil
}

// backupKeysStatus is GET /backup-keys: how archives are sealed, and how
// the key bundle exports stand.
type backupKeysStatus struct {
	// Mode: sealed | plaintext (the plaintext-vault mode: plain tars) |
	// vault-sealed | vault-locked (no vault set up yet: no backup runs).
	Mode              string `json:"mode"`
	Keys              int    `json:"keys"`
	Unexported        int    `json:"unexported"`
	LastExport        string `json:"lastExport,omitempty"`
	Exports           int    `json:"exports"`
	Erased            int    `json:"erased"`
	ErasedSinceExport int    `json:"erasedSinceExport"`
	// PassphraseChanged is when the vault passphrase changed after the
	// last export: every bundle exported before it is stale.
	PassphraseChanged string `json:"passphraseChanged,omitempty"`
}

func (b *Broker) backupKeysStatus() (backupKeysStatus, error) {
	st := backupKeysStatus{Mode: "sealed"}
	switch sealed, err := b.sealing(); {
	case errors.Is(err, errVaultLocked):
		st.Mode = "vault-locked"
	case err != nil:
		st.Mode = "vault-sealed"
	case !sealed:
		st.Mode = "plaintext"
	}
	c, err := b.backupKeys().countsNow()
	if err != nil {
		return st, err
	}
	st.Keys, st.Unexported, st.Erased, st.ErasedSinceExport = c.Keys, c.Unexported, c.Erased, c.ErasedSinceExport
	st.LastExport, st.Exports, st.PassphraseChanged = c.ex.Last, c.ex.Count, c.ex.Rekeyed
	return st, nil
}

// backupKeyAlerts is the admin nudge (§5; H3): keys no export holds yet,
// or exports a passphrase change made stale.
func (b *Broker) backupKeyAlerts() []Alert {
	st, err := b.backupKeysStatus()
	switch {
	case err != nil || st.Keys == 0:
		return nil
	case st.PassphraseChanged != "":
		return []Alert{{Level: "warn", Kind: "backup-keys", Message: fmt.Sprintf(
			"the vault passphrase changed on %s, after the last backup key export: export a fresh key bundle (bx backup keys export, or the admin console's Backup tab) and destroy the older ones — they still open with the old passphrase",
			dateOf(st.PassphraseChanged))}}
	case st.Unexported == 0:
		return nil
	}
	what := "backup key isn't"
	if st.Unexported > 1 {
		what = "backup keys aren't"
	}
	return []Alert{{Level: "warn", Kind: "backup-keys", Message: fmt.Sprintf(
		"%d %s in any export yet: restoring sealed backups on a new machine needs them — export a key bundle (bx backup keys export, or the admin console's Backup tab)",
		st.Unexported, what)}}
}

// dateOf is an RFC 3339 stamp's day, or the stamp as it is.
func dateOf(stamp string) string {
	if ts, err := time.Parse(time.RFC3339, stamp); err == nil {
		return ts.UTC().Format("2006-01-02")
	}
	return stamp
}
