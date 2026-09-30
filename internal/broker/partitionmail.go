package broker

// partitionmail.go — partition mail (plans/partitions/04 §3; PD-15, S4,
// C21): the xbind-owned, durable drop box through which a partitioned
// tile's global instance hands something to ONE person's partition, and a
// person's partition hands something to global — without another
// partition, an admin or global's own storage keeping it.
//
// One store per tile and deployment (people's partitions run on the primary
// only, PD-17):
//
//	data/partitions/<TileKey>/<dep>/mail.db     (bbolt, xbind's own, 0600)
//	    meta        {store: {schema, tile, dep}, seq, box:<bucket>: {user, uid, expired}}
//	    global      the global instance's inbox
//	    u-<32 hex>  a person's inbox, by their partition id (pkey)
//
// An item is keyed by its id (12 bytes: 8 of a per-store monotonic time, 4
// random; hex on the wire, so ids sort in arrival order) and stored as its
// expiry (8 bytes, unsealed, so a sweep needs no vault) followed by the
// item — {from, topic, data, at} — sealed with the vault barrier under a
// label naming the tile, deployment and inbox, like kv values (encodeKV): a
// value moved to another inbox fails to open. data/ is never in a sandbox
// (D118), and mail isn't backed up (transient by design, AR-14).
//
// Who sends, reads and acks is partitionmail_api.go's; the doorbell,
// the boot load and the sweep are partitionmail_bell.go's; the drops (a
// person's inbox on a reset, purge, orphan sweep or deletion; the whole
// store on a switch) and "holds data" (01 §2.2) partitionmail_drop.go's.
// This file keeps the store: put, list, ack and counts.

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/util"
)

// PartitionMailFeature is the feature word of partition mail, for the
// features list of GET /api/xbin/partitions (06 §6; F7b's route lists it).
const PartitionMailFeature = "partition-mail/1"

// The limits of 04 §3 (an inbox's are knobs, below).
const (
	mailItemMax     = 1 << 20 // topic + data of one item
	mailTTLDefault  = 7 * 24 * time.Hour
	mailTTLMax      = 30 * 24 * time.Hour
	mailPageDefault = 100
	mailPageMax     = 1000
	mailPageBytes   = 8 << 20 // a GET page stops past this many bytes of data (one item at least)
	mailAckMax      = 1000
	mailTopicMax    = 256
	mailSourceMax   = 256
)

const (
	mailFile       = "mail.db"
	mailSchema     = 1
	mailGlobalBox  = "global"
	mailDocs       = "/docs/partitions.md"
	mailIDLen      = 12
	mailExpiresLen = 8
)

var (
	mailMetaBucket = []byte("meta")
	mailStoreKey   = []byte("store")
	mailSeqKey     = []byte("seq")
)

// mailKnobs are partition mail's inbox limits, doorbell timings and clock:
// one set, swapped whole (tests stand theirs in), so a timer that fires
// meanwhile reads a consistent set.
type mailKnobs struct {
	inboxItems  int   // items an inbox holds
	inboxBytes  int64 // stored bytes an inbox holds
	backoff     []time.Duration
	startDelay  time.Duration // a partition's start rings its doorbell this much later (the ring joins the start)
	bootDelay   time.Duration // boot's first sweep waits for the rest of boot
	ringTimeout time.Duration
	sweepEvery  time.Duration
	now         func() time.Time
}

var mailDefaults = mailKnobs{
	inboxItems: 1000, inboxBytes: 64 << 20,
	// the re-ring delays after each ring; the last repeats
	backoff:    []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour},
	startDelay: 2 * time.Second, bootDelay: 5 * time.Second, ringTimeout: 2 * time.Minute, sweepEvery: time.Hour,
	now: time.Now,
}

var mailKnobsNow atomic.Pointer[mailKnobs]

func init() { mailKnobsNow.Store(&mailDefaults) }

// knobs is the current set.
func knobs() *mailKnobs { return mailKnobsNow.Load() }

// mailNow is the store's clock.
func mailNow() time.Time { return knobs().now() }

// mailBox is one inbox: a tile's primary deployment's, the global
// instance's (bucket "global") or a person's (bucket = their pkey).
type mailBox struct {
	tile, dep string
	bucket    string
	part      util.Partition // global, or user:<id>
	user, uid string         // a person's inbox
}

// mailRecord is an item as sealed in the store.
type mailRecord struct {
	From  string          `json:"from"`
	Topic string          `json:"topic"`
	Data  json.RawMessage `json:"data,omitempty"`
	At    time.Time       `json:"at"`
}

// mailItem is an item on the wire (GET /partitions/mail).
type mailItem struct {
	ID      string          `json:"id"`
	From    string          `json:"from"`
	Topic   string          `json:"topic"`
	Data    json.RawMessage `json:"data"`
	At      string          `json:"at"`
	Expires string          `json:"expires"`
}

// mailStoreMeta names a store's tile: a TileKey is a hash.
type mailStoreMeta struct {
	Schema int    `json:"schema"`
	Tile   string `json:"tile"`
	Dep    string `json:"dep"`
}

// mailBoxMeta is one inbox's metadata: whose (a person's: user and uid),
// and how many items expired unread.
type mailBoxMeta struct {
	User    string `json:"user,omitempty"`
	UID     string `json:"uid,omitempty"`
	Expired int64  `json:"expired,omitempty"`
}

func boxMetaKey(bucket string) []byte { return []byte("box:" + bucket) }

// mailState is a broker's mail: one lock for every store's open, read,
// write and removal (each operation opens its store and closes it, so a
// switch's wipe or a reset never meets an open file), and the doorbells.
type mailState struct {
	mu       sync.Mutex
	bellMu   sync.Mutex
	bells    map[mailBellKey]*mailBell
	dispatch BusDispatch // tests; nil: the bus's delivery path (the proxy)
	started  bool        // the boot load ran (loadMailBells)
	closed   bool        // the broker closed: nothing rings or sweeps again
	sweeper  *time.Timer
}

var mailStates sync.Map // *Broker → *mailState

func (b *Broker) mail() *mailState {
	if v, ok := mailStates.Load(b); ok {
		return v.(*mailState)
	}
	v, _ := mailStates.LoadOrStore(b, &mailState{bells: map[mailBellKey]*mailBell{}})
	return v.(*mailState)
}

// mailStorePath is data/partitions/<TileKey>/<dep>/mail.db.
func (b *Broker) mailStorePath(tile, dep string) (string, error) {
	if tile == "" || !util.DeploymentNameOK(dep) {
		return "", fmt.Errorf("%s: no mail store for deployment %q", tile, dep)
	}
	return filepath.Join(b.Reg.Root, "data", partitionsDir, util.TileKey(tile), dep, mailFile), nil
}

// mailSealLabel binds a sealed item to its inbox.
func mailSealLabel(tile, dep, bucket string) string {
	return "partition-mail:" + util.TileKey(tile) + "/" + dep + "/" + bucket
}

// errNoMailStore: the store doesn't exist (nothing was ever mailed there,
// or it was removed).
var errNoMailStore = errors.New("no mail store")

// withMailStore runs fn on the store at path, opened for the call; the
// caller holds the mail lock. create makes a missing store (and stamps its
// tile); otherwise a missing one is errNoMailStore.
func withMailStore(path, tile, dep string, create bool, fn func(db *bolt.DB) error) error {
	if _, err := os.Stat(path); err != nil { // walk-ok: data/partitions is xbind's own
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if !create {
			return errNoMailStore
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return err
	}
	defer db.Close()
	if create {
		err := db.Update(func(tx *bolt.Tx) error {
			meta, err := tx.CreateBucketIfNotExists(mailMetaBucket)
			if err != nil || meta.Get(mailStoreKey) != nil {
				return err
			}
			raw, _ := json.Marshal(mailStoreMeta{Schema: mailSchema, Tile: tile, Dep: dep})
			return meta.Put(mailStoreKey, raw)
		})
		if err != nil {
			return err
		}
	}
	return fn(db)
}

// readStoreMeta reads a store's own record.
func readStoreMeta(tx *bolt.Tx) (mailStoreMeta, bool) {
	var m mailStoreMeta
	meta := tx.Bucket(mailMetaBucket)
	if meta == nil {
		return m, false
	}
	raw := meta.Get(mailStoreKey)
	if raw == nil || json.Unmarshal(raw, &m) != nil || m.Schema != mailSchema || m.Tile == "" {
		return m, false
	}
	return m, true
}

func readBoxMeta(tx *bolt.Tx, bucket string) mailBoxMeta {
	var m mailBoxMeta
	if meta := tx.Bucket(mailMetaBucket); meta != nil {
		if raw := meta.Get(boxMetaKey(bucket)); raw != nil {
			_ = json.Unmarshal(raw, &m)
		}
	}
	return m
}

func writeBoxMeta(tx *bolt.Tx, bucket string, m mailBoxMeta) error {
	meta, err := tx.CreateBucketIfNotExists(mailMetaBucket)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(m)
	return meta.Put(boxMetaKey(bucket), raw)
}

// mailExpires reads an item's expiry (its value's prefix).
func mailExpires(v []byte) (time.Time, bool) {
	if len(v) < mailExpiresLen {
		return time.Time{}, false
	}
	return time.Unix(0, int64(binary.BigEndian.Uint64(v[:mailExpiresLen]))), true
}

// purgeExpired drops bucket's expired items (and malformed values), counts
// them in the inbox's metadata, and answers what stays: items and bytes.
func purgeExpired(tx *bolt.Tx, name string, now time.Time) (n int, size int64, err error) {
	bk := tx.Bucket([]byte(name))
	if bk == nil {
		return 0, 0, nil
	}
	var gone [][]byte
	err = bk.ForEach(func(k, v []byte) error {
		if exp, ok := mailExpires(v); !ok || !now.Before(exp) {
			gone = append(gone, append([]byte(nil), k...))
			return nil
		}
		n++
		size += int64(len(v))
		return nil
	})
	if err != nil || len(gone) == 0 {
		return n, size, err
	}
	for _, k := range gone {
		if err := bk.Delete(k); err != nil {
			return n, size, err
		}
	}
	m := readBoxMeta(tx, name)
	m.Expired += int64(len(gone))
	return n, size, writeBoxMeta(tx, name, m)
}

// nextMailID is a fresh id: a time after every earlier id of the store, and
// 4 random bytes.
func nextMailID(tx *bolt.Tx, now time.Time) ([]byte, error) {
	meta, err := tx.CreateBucketIfNotExists(mailMetaBucket)
	if err != nil {
		return nil, err
	}
	t := uint64(now.UnixNano())
	if last := meta.Get(mailSeqKey); len(last) == 8 {
		if l := binary.BigEndian.Uint64(last); t <= l {
			t = l + 1
		}
	}
	id := make([]byte, mailIDLen)
	binary.BigEndian.PutUint64(id, t)
	if _, err := rand.Read(id[8:]); err != nil {
		return nil, err
	}
	if err := meta.Put(mailSeqKey, id[:8]); err != nil {
		return nil, err
	}
	return id, nil
}

// parseMailID is a wire id's key.
func parseMailID(s string) ([]byte, bool) {
	k, err := hex.DecodeString(s)
	return k, err == nil && len(k) == mailIDLen && hex.EncodeToString(k) == s
}

// mailPaused refuses a mail act on tile while its partition mode holds it —
// pending or invalid, or a switch deleting its data: 409. Asked under the
// mail lock, which the switch's wipe takes, so nothing mailed after the
// wipe survives it.
func (b *Broker) mailPaused(tile string) error {
	if why := b.PartitionHoldReason(tile); why != "" {
		return statusErr{http.StatusConflict, tile + " " + why + ": its partition mail waits meanwhile"}
	}
	return nil
}

// mailPut stores an item from from in box, answering its id: 409 while the
// tile is paused, 507 when the inbox is full.
func (b *Broker) mailPut(box mailBox, from util.Partition, topic string, data json.RawMessage, ttl time.Duration) (string, error) {
	path, err := b.mailStorePath(box.tile, box.dep)
	if err != nil {
		return "", err
	}
	now := mailNow()
	rec, err := json.Marshal(mailRecord{From: string(from), Topic: topic, Data: data, At: now.UTC()})
	if err != nil {
		return "", err
	}
	sealed, err := b.encodeKV(mailSealLabel(box.tile, box.dep, box.bucket), rec)
	if err != nil {
		return "", err
	}
	val := make([]byte, mailExpiresLen, mailExpiresLen+len(sealed))
	binary.BigEndian.PutUint64(val, uint64(now.Add(ttl).UnixNano()))
	val = append(val, sealed...)
	ms := b.mail()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if err := b.mailPaused(box.tile); err != nil {
		return "", err
	}
	var id []byte
	full := ""
	err = withMailStore(path, box.tile, box.dep, true, func(db *bolt.DB) error {
		return db.Update(func(tx *bolt.Tx) error {
			bk, err := tx.CreateBucketIfNotExists([]byte(box.bucket))
			if err != nil {
				return err
			}
			n, size, err := purgeExpired(tx, box.bucket, now)
			switch {
			case err != nil:
				return err
			case n >= knobs().inboxItems:
				full = fmt.Sprintf("the inbox holds %d items, its limit", n)
				return nil
			case size+int64(len(val)) > knobs().inboxBytes:
				full = fmt.Sprintf("the inbox holds %d MiB, and its limit is %d", size>>20, knobs().inboxBytes>>20)
				return nil
			}
			if id, err = nextMailID(tx, now); err != nil {
				return err
			}
			if err := bk.Put(id, val); err != nil {
				return err
			}
			if box.user != "" {
				m := readBoxMeta(tx, box.bucket)
				if m.User != box.user || m.UID != box.uid {
					m.User, m.UID = box.user, box.uid
					return writeBoxMeta(tx, box.bucket, m)
				}
			}
			return nil
		})
	})
	if err != nil {
		return "", err
	}
	if full != "" {
		return "", statusErr{http.StatusInsufficientStorage, fmt.Sprintf("%s's inbox is full: %s; it takes more once its partition reads and acknowledges some", box.part, full)}
	}
	return hex.EncodeToString(id), nil
}

// mailList reads box's items after after (an id; "" from the first), at
// most limit and about mailPageBytes; more: others follow. Expired items
// are dropped first.
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
	ms := b.mail()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if err := b.mailPaused(box.tile); err != nil {
		return nil, false, err
	}
	items := []mailItem{}
	more := false
	err = withMailStore(path, box.tile, box.dep, false, func(db *bolt.DB) error {
		return db.Update(func(tx *bolt.Tx) error {
			if _, _, err := purgeExpired(tx, box.bucket, now); err != nil {
				return err
			}
			bk := tx.Bucket([]byte(box.bucket))
			if bk == nil {
				return nil
			}
			c := bk.Cursor()
			k, v := c.First()
			if from != nil {
				if k, v = c.Seek(from); k != nil && bytes.Equal(k, from) {
					k, v = c.Next()
				}
			}
			size := 0
			for ; k != nil; k, v = c.Next() {
				if len(items) >= limit || len(items) > 0 && size >= mailPageBytes {
					more = true
					return nil
				}
				exp, _ := mailExpires(v)
				raw, err := b.decodeKV(label, v[mailExpiresLen:])
				if err != nil {
					return err
				}
				var rec mailRecord
				if err := json.Unmarshal(raw, &rec); err != nil {
					return fmt.Errorf("a mail item can't be read: %w", err)
				}
				data := rec.Data
				if len(data) == 0 {
					data = json.RawMessage("null")
				}
				size += len(data)
				items = append(items, mailItem{ID: hex.EncodeToString(k), From: rec.From, Topic: rec.Topic, Data: data,
					At: rec.At.UTC().Format(time.RFC3339Nano), Expires: exp.UTC().Format(time.RFC3339Nano)})
			}
			return nil
		})
	})
	if err != nil && !errors.Is(err, errNoMailStore) {
		return nil, false, err
	}
	return items, more, nil
}

// mailAck removes ids from box, answering how many items remain. Unknown
// ids (acked or expired already) are nothing to do.
func (b *Broker) mailAck(box mailBox, ids [][]byte) (left int, err error) {
	path, err := b.mailStorePath(box.tile, box.dep)
	if err != nil {
		return 0, err
	}
	ms := b.mail()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if err := b.mailPaused(box.tile); err != nil {
		return 0, err
	}
	err = withMailStore(path, box.tile, box.dep, false, func(db *bolt.DB) error {
		return db.Update(func(tx *bolt.Tx) error {
			if bk := tx.Bucket([]byte(box.bucket)); bk != nil {
				for _, k := range ids {
					if err := bk.Delete(k); err != nil {
						return err
					}
				}
			}
			left, _, err = purgeExpired(tx, box.bucket, mailNow())
			return err
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
	ms := b.mail()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	err = withMailStore(path, tile, dep, false, func(db *bolt.DB) error {
		return db.Update(func(tx *bolt.Tx) error {
			meta = readBoxMeta(tx, bucket)
			n, _, err = purgeExpired(tx, bucket, mailNow())
			return err
		})
	})
	if errors.Is(err, errNoMailStore) {
		err = nil
	}
	return n, meta, err
}

// MailCount is one inbox's metadata for admins (the partitions API, F7b):
// counts, never contents.
type MailCount struct {
	Pending int   `json:"pending"`
	Bytes   int64 `json:"bytes"`
	Expired int64 `json:"expired"`
}

// PartitionMailCounts are tile's inboxes' counts on deployment dep ("" is
// the primary), keyed "global" or a person's partition id (pkey): what
// GET /partitions shows admins (06 §6, 04 §3). Never a content.
func (b *Broker) PartitionMailCounts(tile, dep string) (map[string]MailCount, error) {
	if dep == "" {
		dep = b.primaryOf(tile)
	}
	path, err := b.mailStorePath(tile, dep)
	if err != nil {
		return nil, err
	}
	out := map[string]MailCount{}
	ms := b.mail()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	now := mailNow()
	err = withMailStore(path, tile, dep, false, func(db *bolt.DB) error {
		return db.View(func(tx *bolt.Tx) error {
			return tx.ForEach(func(name []byte, bk *bolt.Bucket) error {
				if bytes.Equal(name, mailMetaBucket) {
					return nil
				}
				c := MailCount{Expired: readBoxMeta(tx, string(name)).Expired}
				_ = bk.ForEach(func(_, v []byte) error {
					if exp, ok := mailExpires(v); ok && now.Before(exp) {
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
