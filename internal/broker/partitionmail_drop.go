package broker

// partitionmail_drop.go — partition mail's stores going away
// (plans/partitions/04 §3, 01 §2.2, §2.6; partitionmail.go keeps the
// store): a person's inbox on a reset, purge, orphan sweep or deletion, the
// global inbox on removing "global" (H1), the whole store on a switch; the
// walk over every store; and "holds data".

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// ---- drops: a person's inbox, the global inbox, a whole store ----

// dropMailBucket deletes bucket (and its metadata) of tile's deployment
// dep: a person's reset, purge or orphan sweep (dropPartition), their
// deletion, or global's data on removing "global". The doorbell forgets it.
func (b *Broker) dropMailBucket(tile, dep, bucket string) (removed int, user string, err error) {
	path, err := b.mailStorePath(tile, dep)
	if err != nil {
		return 0, "", err
	}
	unlock := b.lockTile(tile)
	err = withMailStore(path, tile, dep, false, func(db *bolt.DB) error {
		return mailUpdate(db, func(tx *bolt.Tx) (bool, error) {
			bk := tx.Bucket([]byte(bucket))
			if bk == nil {
				return false, nil
			}
			removed = bk.Stats().KeyN
			user = readBoxMeta(tx, bucket).User
			if err := tx.DeleteBucket([]byte(bucket)); err != nil {
				return false, err
			}
			if meta := tx.Bucket(mailMetaBucket); meta != nil {
				return true, meta.Delete(boxMetaKey(bucket))
			}
			return true, nil
		})
	})
	unlock()
	b.forgetBell(mailBellKey{tile, dep, bucket})
	if errors.Is(err, errNoMailStore) {
		err = nil
	}
	return removed, user, err
}

// mailStoreOf is one store on disk: where, and whose (its own record).
type mailStoreOf struct {
	path      string
	tile, dep string
}

// eachMailStore lists the stores of tile ("" for all) whose record reads.
// For one tile the caller holds its mail lock; for all, each store is read
// under its own tile's lock, one at a time. One that can't be read is an
// error, and listed with the tile its directory names only when asked for
// one tile.
func (b *Broker) eachMailStore(tile string) ([]mailStoreOf, error) {
	key := "*"
	if tile != "" {
		key = util.TileKey(tile)
	}
	paths, err := filepath.Glob(filepath.Join(b.Reg.Root, "data", partitionsDir, key, "*", mailFile)) // walk-ok: data/partitions is xbind's own
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []mailStoreOf
	var errs []error
	for _, path := range paths {
		dep := filepath.Base(filepath.Dir(path))
		if !util.DeploymentNameOK(dep) || !tileKeyOK(mailPathKey(path)) {
			continue
		}
		s := mailStoreOf{path: path, dep: dep}
		read := func() error {
			return withMailStore(path, "", dep, false, func(db *bolt.DB) error {
				return db.View(func(tx *bolt.Tx) error {
					m, ok := readStoreMeta(tx)
					if !ok {
						return fmt.Errorf("%s names no tile this xbind can read", path)
					}
					s.tile = m.Tile
					return nil
				})
			})
		}
		var err error
		if tile == "" {
			unlock := b.mail().lockKey(mailPathKey(path))
			err = read()
			unlock()
		} else {
			err = read()
		}
		switch {
		case errors.Is(err, errNoMailStore):
		case err != nil:
			errs = append(errs, err)
			if tile != "" {
				out = append(out, mailStoreOf{path: path, tile: tile, dep: dep})
			}
		case tile == "" || s.tile == tile:
			out = append(out, s)
		}
	}
	return out, errors.Join(errs...)
}

// ---- holds data and the switch's wipe (01 §2.2, §2.6) ----

func init() {
	registerPartitionStore(partitionStore{"partition-mail", holdsPartitionMail})
	registerWipeHook(wipeHook{name: "partition-mail", wipe: wipePartitionMail})
}

// holdsPartitionMail: an inbox of the tile holds an item — expired ones
// included, until a read or the sweep drops them. A store that can't be
// read holds data.
func holdsPartitionMail(b *Broker, ask registry.PartitionAsk) (bool, error) {
	unlock := b.lockTile(ask.Tile)
	defer unlock()
	stores, err := b.eachMailStore(ask.Tile)
	if err != nil {
		return true, err
	}
	held := false
	for _, s := range stores {
		err := withMailStore(s.path, s.tile, s.dep, false, func(db *bolt.DB) error {
			return db.View(func(tx *bolt.Tx) error {
				return tx.ForEach(func(name []byte, bk *bolt.Bucket) error {
					if !bytes.Equal(name, mailMetaBucket) {
						if k, _ := bk.Cursor().First(); k != nil {
							held = true
						}
					}
					return nil
				})
			})
		})
		if err != nil && !errors.Is(err, errNoMailStore) {
			return true, err
		}
	}
	return held, nil
}

// wipePartitionMail is the "partition-mail" store's wipe: a switch between
// user partitions and unpartitioned removes the tile's whole mail store,
// every deployment's; removing "global" deletes the global instance's
// inbox only — people's inboxes stay with their partitions (owner ruling
// H1); adding it deletes nothing. A dry run counts. Its bytes are the
// summary's (mail has no count of its own there); a person is added to
// its People only when their partition exists — someone the global
// instance mailed who never opened the tile had no partition to lose.
func wipePartitionMail(b *Broker, t wipeTarget, sum *wipeSummary) error {
	if t.Kind == wipeNone {
		return nil
	}
	unlock := b.lockTile(t.Tile)
	// A store this xbind can't read is listed with the tile anyway: a switch
	// that deletes everything removes it (the manager confirmed deleting all
	// the tile's data), and only a removal that fails stops the switch.
	stores, err := b.eachMailStore(t.Tile)
	var errs []error
	if err != nil && t.Kind != wipeEverything {
		errs = append(errs, err)
	}
	var dropGlobal []mailStoreOf
	for _, s := range stores {
		var people []string
		err := withMailStore(s.path, s.tile, s.dep, false, func(db *bolt.DB) error {
			return db.View(func(tx *bolt.Tx) error {
				return tx.ForEach(func(name []byte, bk *bolt.Bucket) error {
					switch {
					case bytes.Equal(name, mailMetaBucket):
					case t.Kind == wipeEverything:
						if k, _ := bk.Cursor().First(); k != nil && b.mailPartitionExists(s, string(name)) {
							people = append(people, readBoxMeta(tx, string(name)).User)
						}
					case string(name) == mailGlobalBox:
						_ = bk.ForEach(func(_, v []byte) error { sum.Bytes += int64(len(v)); return nil })
					}
					return nil
				})
			})
		})
		switch {
		case err == nil, errors.Is(err, errNoMailStore):
		case t.Kind == wipeEverything:
			slog.Warn("partition mail: a store the switch removes can't be read; its people aren't told of its mail", "tile", t.Tile, "err", err)
		default:
			errs = append(errs, err)
		}
		switch t.Kind {
		case wipeEverything:
			for _, u := range people {
				sum.addPerson(u)
			}
			if fi, err := os.Stat(s.path); err == nil { // walk-ok: as above
				sum.Bytes += fi.Size()
			}
			if !t.DryRun {
				if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
					errs = append(errs, err)
				}
				removeEmptyDirs(filepath.Dir(s.path)) // the deployment's level; the TileKey's keeps mode.json
			}
		case wipeGlobal:
			dropGlobal = append(dropGlobal, s)
		}
	}
	unlock()
	if t.DryRun {
		return errors.Join(errs...)
	}
	for _, s := range dropGlobal {
		if _, _, err := b.dropMailBucket(s.tile, s.dep, mailGlobalBox); err != nil {
			errs = append(errs, err)
		}
	}
	if t.Kind == wipeEverything {
		b.forgetBellsOf(t.Tile)
	}
	return errors.Join(errs...)
}

// mailPartitionExists: inbox bucket of store s is a person's whose
// partition of the tile has a record (it ran, or was opened).
func (b *Broker) mailPartitionExists(s mailStoreOf, bucket string) bool {
	if !util.PartitionKeyOK(bucket) {
		return false
	}
	dir, err := b.partitionRecordDir(s.tile, s.dep, bucket)
	if err != nil {
		return false
	}
	_, ok, err := readPartitionRecordAt(dir)
	return ok || err != nil // a record that can't be read is a partition's
}

// mailUserDeleted removes person userID's inboxes (of incarnation uid; ""
// every one this store names) in every tile: the users store's delete hook,
// through PartitionUserDeleted. Mail is transient: it doesn't wait for the
// orphan retention.
func (b *Broker) mailUserDeleted(userID, uid string) {
	stores, _ := b.eachMailStore("")
	type drop struct{ tile, dep, bucket string }
	var drops []drop
	for _, s := range stores {
		unlock := b.mail().lockKey(mailPathKey(s.path))
		_ = withMailStore(s.path, s.tile, s.dep, false, func(db *bolt.DB) error {
			return db.View(func(tx *bolt.Tx) error {
				return tx.ForEach(func(name []byte, _ *bolt.Bucket) error {
					if m := readBoxMeta(tx, string(name)); m.User == userID && (uid == "" || m.UID == uid) {
						drops = append(drops, drop{s.tile, s.dep, string(name)})
					}
					return nil
				})
			})
		})
		unlock()
	}
	for _, d := range drops {
		if _, _, err := b.dropMailBucket(d.tile, d.dep, d.bucket); err != nil {
			slog.Warn("partition mail: a deleted person's inbox can't be removed", "tile", d.tile, "err", err)
		}
	}
}
