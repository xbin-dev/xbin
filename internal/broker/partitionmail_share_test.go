package broker

// partitionmail_share_test.go — partition mail's review fixes
// (plans/partitions/04 §3; PD-15, PD-18, S20): a doorbell that starts its
// own partition keeps its backoff; each sender's share of the global
// inbox; an item that can't be opened never wedges the ones behind it; a
// sealed vault stops reads that return items, never acks; reads, counts
// and refused sends commit nothing.

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/util"
)

// mailTxID is the store's last committed transaction: it moves only when
// something was written.
func mailTxID(t *testing.T, path string) int {
	t.Helper()
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id := 0
	_ = db.View(func(tx *bolt.Tx) error { id = tx.ID(); return nil })
	return id
}

// covers PD-18 S20 04§3 — the doorbell that starts its own partition: a
// ring to a stopped partition is a mail start, and the start rings again
// (whatever started it) — but keeps the backoff, so an item left unacked
// cold-starts its partition less and less often, never every ~36 min for
// its whole ttl. A new item still rings at once and starts the backoff
// over.
func TestMailStartKeepsBackoff(t *testing.T) {
	w := mailWS(t)
	b := w.b
	global := instanceOf("apps/pg", "")
	uid, err := b.mintPartitionUID("alice")
	if err != nil {
		t.Fatal(err)
	}
	pk := util.PartitionKey("alice", uid)
	var mu sync.Mutex
	var at []time.Time
	calls := fakeMailDispatch(t, b, func(c bellCall) int {
		if c.p.Partition == util.UserPartition("alice") {
			mu.Lock()
			at = append(at, time.Now())
			mu.Unlock()
			// the ring cold-starts her partition; her handler skips the
			// item (an unknown topic), acking nothing
			b.notePartitionStart("apps/pg", "main", "user:alice", pk, uid)
		}
		return 200
	})
	backoff := []time.Duration{20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond, 160 * time.Millisecond, 320 * time.Millisecond}
	withMailKnobs(t, func(k *mailKnobs) {
		k.backoff, k.startDelay, k.bootDelay = backoff, 5*time.Millisecond, 10*time.Millisecond
	})
	go func() { // drain: the times are the answer's
		for range calls {
		}
	}()
	b.notePartitionStart("apps/pg", "main", "user:alice", pk, uid) // she ran before
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	mailSendOK(t, b, global, "user:alice", "unknown/topic", 1)
	time.Sleep(1600 * time.Millisecond)

	ms := b.mail()
	ms.bellMu.Lock()
	bl := ms.bells[mailBellKey{"apps/pg", "main", pk}]
	attempt := -1
	if bl != nil {
		attempt = bl.attempt
	}
	ms.bellMu.Unlock()
	mu.Lock()
	rings := append([]time.Time(nil), at...)
	mu.Unlock()
	var gaps []time.Duration
	maxGap := time.Duration(0)
	for i := 1; i < len(rings); i++ {
		g := rings[i].Sub(rings[i-1])
		gaps = append(gaps, g.Round(time.Millisecond))
		maxGap = max(maxGap, g)
	}
	// 20+40+80+160 ms, then every 320: about 12 rings in 1.6 s (a start's
	// ring beside some), where a start resetting the backoff rang ~60
	if len(rings) == 0 || len(rings) > 24 || attempt < len(backoff) || maxGap < 250*time.Millisecond {
		t.Errorf("a doorbell that starts its partition: %d rings in %v, attempt %d, gaps %v — the backoff must grow to its last step",
			len(rings), time.Since(start).Round(time.Millisecond), attempt, gaps)
	}

	// a start no ring reached (she opens the tile between two rings): at
	// once, the backoff kept
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(at) }
	for n := count(); count() == n; { // just past a ring
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	n0 := count()
	b.notePartitionStart("apps/pg", "main", "user:alice", pk, uid)
	time.Sleep(100 * time.Millisecond) // well before the next step (320 ms)
	if n := count(); n != n0+1 {
		t.Errorf("a start no ring reached: %d rings, want 1", n-n0)
	}
	ms.bellMu.Lock()
	if bl := ms.bells[mailBellKey{"apps/pg", "main", pk}]; bl == nil || bl.attempt < len(backoff) {
		t.Errorf("a start reset the backoff: %+v", bl)
	}
	ms.bellMu.Unlock()

	// a new item: at once, and the backoff starts over
	n0 = count()
	mailSendOK(t, b, global, "user:alice", "another", 2)
	time.Sleep(60 * time.Millisecond)
	mu.Lock()
	n1 := len(at)
	mu.Unlock()
	if n1 == n0 {
		t.Error("a new item didn't ring at once")
	}
	ms.bellMu.Lock()
	if bl := ms.bells[mailBellKey{"apps/pg", "main", pk}]; bl == nil || bl.attempt >= len(backoff) {
		t.Errorf("a new item kept the backoff at its last step: %+v", bl)
	}
	ms.bellMu.Unlock()
	b.forgetBellsOf("apps/pg")
}

// covers S20 04§3 — the global inbox is every person's to mail, so each
// sender has a share of it (100 items, 8 MiB): alice filling hers gets 507
// and bob still mails global; an ack frees her share; a person's inbox,
// which only global mails, has no share beyond the inbox's limits.
func TestMailSenderShare(t *testing.T) {
	w := mailWS(t)
	b := w.b
	global, aliceInst, bobInst := instanceOf("apps/pg", ""), partInst("apps/pg", "alice"), partInst("apps/pg", "bob")
	if k := knobs(); k.senderItems != 100 || k.senderBytes != 8<<20 {
		t.Errorf("a sender's share %d items, %d bytes; want 100 and 8 MiB", k.senderItems, k.senderBytes)
	}
	withMailKnobs(t, func(k *mailKnobs) { k.senderItems = 3 })
	for i := 0; i < 3; i++ {
		mailSendOK(t, b, aliceInst, "global", fmt.Sprint("a", i), i)
	}
	store := filepath.Join(w.root, "data", "partitions", util.TileKey("apps/pg"), "main", "mail.db")
	tx0 := mailTxID(t, store)
	code, body := mailSend(t, b, mailFrame("apps/pg", "alice"), `{"to":"global","topic":"flood"}`)
	if code != 507 || !strings.Contains(body, "your share") {
		t.Errorf("alice past her share: %d %s", code, body)
	}
	if tx := mailTxID(t, store); tx != tx0 {
		t.Errorf("a refused send committed (tx %d → %d)", tx0, tx)
	}
	mailSendOK(t, b, bobInst, "global", "b0", 0) // bob's share is his own
	mailSendOK(t, b, global, "global", "g0", 0)  // and global's own
	for i := 0; i < 4; i++ {                     // a person's inbox: one sender, no share
		mailSendOK(t, b, global, "user:alice", fmt.Sprint("dm", i), i)
	}
	_, pg, _ := mailRead(t, b, global, "")
	if len(pg.Items) != 5 {
		t.Fatalf("global's inbox: %+v", pg.Items)
	}
	if code, _ := mailAckCall(t, b, global, pg.Items[0].ID); code != 200 {
		t.Fatal(code)
	}
	mailSendOK(t, b, aliceInst, "global", "a3", 3) // acked: room again

	// the byte share
	withMailKnobs(t, func(k *mailKnobs) { k.senderBytes = 400 })
	if code, body := mailSend(t, b, bobInst, `{"to":"global","topic":"big","data":"`+strings.Repeat("z", 500)+`"}`); code != 507 || !strings.Contains(body, "your share") {
		t.Errorf("bob past his byte share: %d %s", code, body)
	}
}

// covers 04§3 — an item that can't be opened (sealed under another inbox's
// label, or damaged) is dropped and counted undeliverable, so the items
// behind it are read and acked; a sealed vault answers 503 to a read that
// returns items and to a send, while acks and an empty read work; a read
// with nothing to drop commits nothing.
func TestMailUndeliverable(t *testing.T) {
	w := mailWS(t)
	b := w.b
	global, aliceInst := instanceOf("apps/pg", ""), partInst("apps/pg", "alice")
	if err := b.barrier.Init("mail-pass"); err != nil {
		t.Fatal(err)
	}
	ids := []string{
		mailSendOK(t, b, global, "user:alice", "a", 1),
		mailSendOK(t, b, global, "user:alice", "b", 2),
		mailSendOK(t, b, global, "user:alice", "c", 3),
		mailSendOK(t, b, global, "user:alice", "d", 4),
	}
	mailSendOK(t, b, global, "user:bob", "for-bob", 5)
	store := filepath.Join(w.root, "data", "partitions", util.TileKey("apps/pg"), "main", "mail.db")
	// b: bob's item moved into alice's inbox (it can't open under her
	// label); c: a value this layout can't read
	akey, bkey := w.pkeyOf("alice"), w.pkeyOf("bob")
	db, err := bolt.Open(store, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		var bobs []byte
		_ = tx.Bucket([]byte(bkey)).ForEach(func(_, v []byte) error { bobs = append([]byte(nil), v...); return nil })
		k1, _ := parseMailID(ids[1])
		k2, _ := parseMailID(ids[2])
		if err := tx.Bucket([]byte(akey)).Put(k1, bobs); err != nil {
			return err
		}
		return tx.Bucket([]byte(akey)).Put(k2, []byte{1, 2, 3})
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := mailTopics(t, b, aliceInst); fmt.Sprint(got) != "[global a global d]" {
		t.Errorf("alice's inbox past two items that can't open: %q", got)
	}
	counts, _ := b.PartitionMailCounts("apps/pg", "")
	if c := counts[akey]; c.Undeliverable != 2 || c.Pending != 2 {
		t.Errorf("alice's counts %+v", c)
	}
	tx0 := mailTxID(t, store)
	mailTopics(t, b, aliceInst)
	if _, _, err := b.mailPending("apps/pg", "main", akey); err != nil {
		t.Fatal(err)
	}
	if code, _ := mailAckCall(t, b, aliceInst, ids[1]); code != 200 { // gone already: nothing to do
		t.Fatal(code)
	}
	if tx := mailTxID(t, store); tx != tx0 {
		t.Errorf("a read, a count and an ack of nothing committed (tx %d → %d)", tx0, tx)
	}

	// the vault sealed: a read that returns items and a send 503; an
	// empty read and acks work
	b.barrier.Seal()
	if code, _, body := mailRead(t, b, aliceInst, ""); code != 503 || !strings.Contains(body, "sealed") {
		t.Errorf("a read while sealed: %d %s", code, body)
	}
	if code, body := mailSend(t, b, global, `{"to":"user:alice","topic":"t"}`); code != 503 {
		t.Errorf("a send while sealed: %d %s", code, body)
	}
	if code, pg, body := mailRead(t, b, global, ""); code != 200 || len(pg.Items) != 0 {
		t.Errorf("an empty read while sealed: %d %s", code, body)
	}
	if code, body := mailAckCall(t, b, aliceInst, ids[0]); code != 200 {
		t.Errorf("an ack while sealed: %d %s", code, body)
	}
	if err := b.barrier.Unseal("mail-pass"); err != nil {
		t.Fatal(err)
	}
	if got := mailTopics(t, b, aliceInst); fmt.Sprint(got) != "[global d]" {
		t.Errorf("after the ack while sealed: %q", got)
	}
}
