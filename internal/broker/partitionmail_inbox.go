package broker

// partitionmail_inbox.go — an inbox's acts (plans/partitions/04 §3;
// PD-15; partitionmail.go keeps the store's shape): put, list, ack, what
// the doorbell counts, and the counts admins see. Each act holds its
// tile's mail lock and opens the store for the call; one that changes
// nothing commits nothing.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/vault"
)

// mailPut stores an item from sender from in box, answering its id: 409
// while the tile is paused, 507 when the inbox is full — or, in the global
// inbox, which every person of the tile mails, when the sender's own share
// of it is (a person can't fill it for everyone else).
func (b *Broker) mailPut(box, from mailBox, topic string, data json.RawMessage, ttl time.Duration) (string, error) {
	path, err := b.mailStorePath(box.tile, box.dep)
	if err != nil {
		return "", err
	}
	now := mailNow()
	rec, err := json.Marshal(mailRecord{From: string(from.part), Topic: topic, Data: data, At: now.UTC()})
	if err != nil {
		return "", err
	}
	sealed, err := b.encodeKV(mailSealLabel(box.tile, box.dep, box.bucket), rec)
	if err != nil {
		return "", err
	}
	val := mailValue(now.Add(ttl), from.bucket, sealed)
	shared := box.bucket == mailGlobalBox
	unlock := b.lockTile(box.tile)
	defer unlock()
	if err := b.mailPaused(box.tile); err != nil {
		return "", err
	}
	var id []byte
	full := ""
	err = withMailStore(path, box.tile, box.dep, true, func(db *bolt.DB) error {
		return mailUpdate(db, func(tx *bolt.Tx) (bool, error) {
			bk, err := tx.CreateBucketIfNotExists([]byte(box.bucket))
			if err != nil {
				return false, err
			}
			u, purged, err := purgeExpired(tx, box.bucket, now, shared)
			k := knobs()
			mine := u.senders[from.bucket]
			switch {
			case err != nil:
				return false, err
			case u.items >= k.inboxItems:
				full = fmt.Sprintf("%s's inbox is full: it holds %d items, its limit", box.part, u.items)
			case u.bytes+int64(len(val)) > k.inboxBytes:
				full = fmt.Sprintf("%s's inbox is full: it holds %d MiB, and its limit is %d", box.part, u.bytes>>20, k.inboxBytes>>20)
			case shared && mine.items >= k.senderItems:
				full = fmt.Sprintf("your share of %s's inbox is full: %d of your items wait there, the most one sender may have", box.part, mine.items)
			case shared && mine.bytes+int64(len(val)) > k.senderBytes:
				full = fmt.Sprintf("your share of %s's inbox is full: %d MiB of your items wait there, and one sender may have %d", box.part, mine.bytes>>20, k.senderBytes>>20)
			}
			if full != "" {
				return purged, nil
			}
			if id, err = nextMailID(tx, now); err != nil {
				return false, err
			}
			if err := bk.Put(id, val); err != nil {
				return false, err
			}
			if box.user != "" {
				m := readBoxMeta(tx, box.bucket)
				if m.User != box.user || m.UID != box.uid {
					m.User, m.UID = box.user, box.uid
					return true, writeBoxMeta(tx, box.bucket, m)
				}
			}
			return true, nil
		})
	})
	if err != nil {
		return "", err
	}
	if full != "" {
		return "", statusErr{http.StatusInsufficientStorage, full + "; it takes more once its addressee reads and acknowledges some"}
	}
	return hex.EncodeToString(id), nil
}

// openMailItem opens a stored item of inbox label: vault.ErrSealed while
// the vault is sealed; any other error means the item can never open
// (sealed under another key or label, or damaged).
func (b *Broker) openMailItem(label string, k, v []byte) (mailItem, error) {
	exp, _, sealed, ok := splitMailValue(v)
	if !ok {
		return mailItem{}, errors.New("a value this xbind can't read")
	}
	raw, err := b.decodeKV(label, sealed)
	if err != nil {
		return mailItem{}, err
	}
	var rec mailRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return mailItem{}, fmt.Errorf("a mail item can't be read: %w", err)
	}
	data := rec.Data
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	return mailItem{ID: hex.EncodeToString(k), From: rec.From, Topic: rec.Topic, Data: data,
		At: rec.At.UTC().Format(time.RFC3339Nano), Expires: exp.UTC().Format(time.RFC3339Nano)}, nil
}

// mailList reads box's items after after (an id; "" from the first), at
// most limit and about mailPageBytes; more: others follow. Expired items
// are dropped first; an item that can never open (anything but a sealed
// vault: 503 for the whole read) is dropped and counted undeliverable, so
// one bad item never stops the ones behind it.
func (b *Broker) mailList(box mailBox, after string, limit int) ([]mailItem, bool, error) {
	path, err := b.mailStorePath(box.tile, box.dep)
	if err != nil {
		return nil, false, err
	}
	var from []byte
	if after != "" {
		var ok bool
		if from, ok = parseMailID(after); !ok {
			return nil, false, statusErr{http.StatusBadRequest, "after: not a mail id"}
		}
	}
	label := mailSealLabel(box.tile, box.dep, box.bucket)
	now := mailNow()
	unlock := b.lockTile(box.tile)
	defer unlock()
	if err := b.mailPaused(box.tile); err != nil {
		return nil, false, err
	}
	items := []mailItem{}
	more := false
	err = withMailStore(path, box.tile, box.dep, false, func(db *bolt.DB) error {
		return mailUpdate(db, func(tx *bolt.Tx) (bool, error) {
			_, changed, err := purgeExpired(tx, box.bucket, now, false)
			if err != nil {
				return false, err
			}
			bk := tx.Bucket([]byte(box.bucket))
			if bk == nil {
				return changed, nil
			}
			c := bk.Cursor()
			k, v := c.First()
			if from != nil {
				if k, v = c.Seek(from); k != nil && bytes.Equal(k, from) {
					k, v = c.Next()
				}
			}
			var bad [][]byte
			size := 0
			for ; k != nil; k, v = c.Next() {
				if len(items) >= limit || len(items) > 0 && size >= mailPageBytes {
					more = true
					break
				}
				it, err := b.openMailItem(label, k, v)
				if errors.Is(err, vault.ErrSealed) {
					return false, err
				}
				if err != nil {
					slog.Warn("partition mail: an item that can't be opened is dropped as undeliverable", "tile", box.tile,
						"partition", box.part, "id", hex.EncodeToString(k), "err", err)
					bad = append(bad, append([]byte(nil), k...))
					continue
				}
				size += len(it.Data)
				items = append(items, it)
			}
			if len(bad) == 0 {
				return changed, nil
			}
			return true, dropMailItems(tx, bk, box.bucket, nil, bad)
		})
	})
	if err != nil && !errors.Is(err, errNoMailStore) {
		return nil, false, err
	}
	return items, more, nil
}

// mailAck removes ids from box, answering how many items remain. Unknown
// ids (acked or expired already) are nothing to do. It never opens an
// item, so it works while the vault is sealed.
func (b *Broker) mailAck(box mailBox, ids [][]byte) (left int, err error) {
	path, err := b.mailStorePath(box.tile, box.dep)
	if err != nil {
		return 0, err
	}
	unlock := b.lockTile(box.tile)
	defer unlock()
	if err := b.mailPaused(box.tile); err != nil {
		return 0, err
	}
	err = withMailStore(path, box.tile, box.dep, false, func(db *bolt.DB) error {
		return mailUpdate(db, func(tx *bolt.Tx) (bool, error) {
			acked := false
			if bk := tx.Bucket([]byte(box.bucket)); bk != nil {
				for _, k := range ids {
					if bk.Get(k) == nil {
						continue
					}
					if err := bk.Delete(k); err != nil {
						return false, err
					}
					acked = true
				}
			}
			u, purged, err := purgeExpired(tx, box.bucket, mailNow(), false)
			left = u.items
			return acked || purged, err
		})
	})
	if errors.Is(err, errNoMailStore) {
		return 0, nil
	}
	return left, err
}

// mailPending is how many unexpired items bucket of tile's deployment dep
// holds, and whose inbox it is (a person's: user and uid).
func (b *Broker) mailPending(tile, dep, bucket string) (n int, meta mailBoxMeta, err error) {
	path, err := b.mailStorePath(tile, dep)
	if err != nil {
		return 0, meta, err
	}
	unlock := b.lockTile(tile)
	defer unlock()
	err = withMailStore(path, tile, dep, false, func(db *bolt.DB) error {
		return mailUpdate(db, func(tx *bolt.Tx) (bool, error) {
			meta = readBoxMeta(tx, bucket)
			u, purged, err := purgeExpired(tx, bucket, mailNow(), false)
			n = u.items
			return purged, err
		})
	})
	if errors.Is(err, errNoMailStore) {
		err = nil
	}
	return n, meta, err
}

// MailCount is one inbox's metadata (the partitions API, GET /partitions):
// counts, never contents.
type MailCount struct {
	Pending       int   `json:"pending"`
	Bytes         int64 `json:"bytes"`
	Expired       int64 `json:"expired"`
	Undeliverable int64 `json:"undeliverable,omitempty"`
}

// PartitionMailCounts are tile's inboxes' counts on deployment dep ("" is
// the primary), keyed "global" or a person's partition id (pkey): what
// GET /partitions shows a person of their own inbox and admins of each
// (06 §6, 04 §3). Never a content.
func (b *Broker) PartitionMailCounts(tile, dep string) (map[string]MailCount, error) {
	if dep == "" {
		dep = b.primaryOf(tile)
	}
	path, err := b.mailStorePath(tile, dep)
	if err != nil {
		return nil, err
	}
	out := map[string]MailCount{}
	unlock := b.lockTile(tile)
	defer unlock()
	now := mailNow()
	err = withMailStore(path, tile, dep, false, func(db *bolt.DB) error {
		return db.View(func(tx *bolt.Tx) error {
			return tx.ForEach(func(name []byte, bk *bolt.Bucket) error {
				if bytes.Equal(name, mailMetaBucket) {
					return nil
				}
				m := readBoxMeta(tx, string(name))
				c := MailCount{Expired: m.Expired, Undeliverable: m.Undeliverable}
				_ = bk.ForEach(func(_, v []byte) error {
					if exp, _, _, ok := splitMailValue(v); ok && now.Before(exp) {
						c.Pending++
						c.Bytes += int64(len(v))
					}
					return nil
				})
				out[string(name)] = c
				return nil
			})
		})
	})
	if errors.Is(err, errNoMailStore) {
		err = nil
	}
	return out, err
}
