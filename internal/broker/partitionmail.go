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
//	    meta        {store: {schema, tile, dep}, seq, box:<bucket>: {user, uid, expired, undeliverable}}
//	    global      the global instance's inbox
//	    u-<32 hex>  a person's inbox, by their partition id (pkey)
//
// An item is keyed by its id (12 bytes: 8 of a per-store monotonic time, 4
// random; hex on the wire, so ids sort in arrival order) and stored as
//
//	expiry (8 bytes) | len(sender) (1 byte) | sender | sealed item
//
// — the expiry and the sender's inbox name ("global" or their pkey) in
// the clear, so expiry, counts, the sweep and each sender's share of the
// global inbox need no vault; the item — {from, topic, data, at} — sealed
// with the vault barrier under a label naming the tile, deployment and
// inbox, like kv values (encodeKV): a value moved to another inbox fails to
// open. data/ is never in a sandbox (D118), and mail isn't backed up
// (transient by design, AR-14).
//
// Who sends, reads and acks is partitionmail_api.go's; put, list, ack and
// counts partitionmail_inbox.go's; the doorbell, the boot load and the
// sweep partitionmail_bell.go's; the drops (a person's inbox on a reset,
// purge, orphan sweep or deletion; the whole store on a switch) and "holds
// data" (01 §2.2) partitionmail_drop.go's. This file keeps the store's
// shape: its path, lock, value layout, metadata and ids.

import (
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
// features list of GET /api/xbin/partitions (06 §6; the integrator
// registers it with that route's list).
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
	senderItems int   // items one sender may have waiting in the global inbox (its many senders share it)
	senderBytes int64 // stored bytes one sender may have waiting there
	backoff     []time.Duration
	startDelay  time.Duration // a partition's start rings its doorbell this much later (the ring joins the start)
	bootDelay   time.Duration // boot's first sweep waits for the rest of boot
	ringTimeout time.Duration
	sweepEvery  time.Duration
	now         func() time.Time
}

var mailDefaults = mailKnobs{
	inboxItems: 1000, inboxBytes: 64 << 20,
	senderItems: 100, senderBytes: 8 << 20,
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
// how many items expired unread, and how many were dropped undeliverable
// (their value can't be opened: sealed under another key, or damaged).
type mailBoxMeta struct {
	User          string `json:"user,omitempty"`
	UID           string `json:"uid,omitempty"`
	Expired       int64  `json:"expired,omitempty"`
	Undeliverable int64  `json:"undeliverable,omitempty"`
}

func boxMetaKey(bucket string) []byte { return []byte("box:" + bucket) }

// mailState is a broker's mail: a lock per tile for every open, read,
// write and removal of its stores (each operation opens its store and
// closes it, so a switch's wipe or a reset never meets an open file, and
// one tile's mail never waits on another's), and the doorbells.
type mailState struct {
	locksMu  sync.Mutex
	locks    map[string]*sync.Mutex // by TileKey: all of a tile's deployments' stores
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
	v, _ := mailStates.LoadOrStore(b, &mailState{locks: map[string]*sync.Mutex{}, bells: map[mailBellKey]*mailBell{}})
	return v.(*mailState)
}

// lockKey takes the lock of the tile whose TileKey is key — every store of
// the tile, each deployment's — and answers its unlock.
func (ms *mailState) lockKey(key string) func() {
	ms.locksMu.Lock()
	m := ms.locks[key]
	if m == nil {
		m = &sync.Mutex{}
		ms.locks[key] = m
	}
	ms.locksMu.Unlock()
	m.Lock()
	return m.Unlock
}

// lockTile takes tile's mail lock.
func (b *Broker) lockTile(tile string) func() { return b.mail().lockKey(util.TileKey(tile)) }

// mailPathKey is the TileKey a store's path names.
func mailPathKey(path string) string { return filepath.Base(filepath.Dir(filepath.Dir(path))) }

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
// caller holds the tile's mail lock. create makes a missing store (and
// stamps its tile); otherwise a missing one is errNoMailStore.
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
		err := mailUpdate(db, func(tx *bolt.Tx) (bool, error) {
			meta, err := tx.CreateBucketIfNotExists(mailMetaBucket)
			if err != nil || meta.Get(mailStoreKey) != nil {
				return false, err
			}
			raw, _ := json.Marshal(mailStoreMeta{Schema: mailSchema, Tile: tile, Dep: dep})
			return true, meta.Put(mailStoreKey, raw)
		})
		if err != nil {
			return err
		}
	}
	return fn(db)
}

// errMailUnchanged rolls back a write transaction that changed nothing.
var errMailUnchanged = errors.New("partition mail: nothing changed")

// mailUpdate runs fn in a write transaction and commits it only when fn
// says it changed something: a read, a count or a refused send that found
// nothing to drop costs no commit (and no fsync).
func mailUpdate(db *bolt.DB, fn func(tx *bolt.Tx) (changed bool, err error)) error {
	err := db.Update(func(tx *bolt.Tx) error {
		changed, err := fn(tx)
		if err == nil && !changed {
			return errMailUnchanged
		}
		return err
	})
	if errors.Is(err, errMailUnchanged) {
		return nil
	}
	return err
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

// mailValue lays an item out for the store: its expiry, its sender's
// inbox name, and the sealed item.
func mailValue(expires time.Time, sender string, sealed []byte) []byte {
	v := make([]byte, mailExpiresLen+1, mailExpiresLen+1+len(sender)+len(sealed))
	binary.BigEndian.PutUint64(v, uint64(expires.UnixNano()))
	v[mailExpiresLen] = byte(len(sender))
	v = append(v, sender...)
	return append(v, sealed...)
}

// splitMailValue reads a stored value: expiry, sender, sealed item; ok
// false for a value this layout doesn't hold.
func splitMailValue(v []byte) (expires time.Time, sender string, sealed []byte, ok bool) {
	if len(v) < mailExpiresLen+1 {
		return time.Time{}, "", nil, false
	}
	n := int(v[mailExpiresLen])
	if len(v) < mailExpiresLen+1+n {
		return time.Time{}, "", nil, false
	}
	expires = time.Unix(0, int64(binary.BigEndian.Uint64(v[:mailExpiresLen])))
	return expires, string(v[mailExpiresLen+1 : mailExpiresLen+1+n]), v[mailExpiresLen+1+n:], true
}

// boxUsage is what an inbox holds once its expired items are dropped:
// items and stored bytes, and — when asked — each sender's.
type boxUsage struct {
	items   int
	bytes   int64
	senders map[string]senderUsage
}

type senderUsage struct {
	items int
	bytes int64
}

// purgeExpired drops bucket's expired items (counted as expired) and values
// this layout can't read (counted as undeliverable) into the inbox's
// metadata, and answers what stays — each sender's too when bySender.
// changed: it dropped something.
func purgeExpired(tx *bolt.Tx, name string, now time.Time, bySender bool) (u boxUsage, changed bool, err error) {
	bk := tx.Bucket([]byte(name))
	if bk == nil {
		return u, false, nil
	}
	if bySender {
		u.senders = map[string]senderUsage{}
	}
	var expired, bad [][]byte
	err = bk.ForEach(func(k, v []byte) error {
		exp, sender, _, ok := splitMailValue(v)
		switch {
		case !ok:
			bad = append(bad, append([]byte(nil), k...))
		case !now.Before(exp):
			expired = append(expired, append([]byte(nil), k...))
		default:
			u.items++
			u.bytes += int64(len(v))
			if bySender {
				s := u.senders[sender]
				s.items++
				s.bytes += int64(len(v))
				u.senders[sender] = s
			}
		}
		return nil
	})
	if err != nil || len(expired)+len(bad) == 0 {
		return u, false, err
	}
	return u, true, dropMailItems(tx, bk, name, expired, bad)
}

// dropMailItems deletes items of inbox name, counting expired and
// undeliverable ones in its metadata.
func dropMailItems(tx *bolt.Tx, bk *bolt.Bucket, name string, expired, undeliverable [][]byte) error {
	for _, keys := range [][][]byte{expired, undeliverable} {
		for _, k := range keys {
			if err := bk.Delete(k); err != nil {
				return err
			}
		}
	}
	m := readBoxMeta(tx, name)
	m.Expired += int64(len(expired))
	m.Undeliverable += int64(len(undeliverable))
	return writeBoxMeta(tx, name, m)
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
// tile's mail lock, which the switch's wipe takes, so nothing mailed after
// the wipe survives it.
func (b *Broker) mailPaused(tile string) error {
	if why := b.PartitionHoldReason(tile); why != "" {
		return statusErr{http.StatusConflict, tile + " " + why + ": its partition mail waits meanwhile"}
	}
	return nil
}
