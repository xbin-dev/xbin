package broker

// partitionmail_bell.go — the partition mail doorbell (plans/partitions/04
// §3; PD-15, PD-18, PD-20, S1, S20). While an inbox holds unacked items,
// xbind POSTs {"partition": <addressee>, "pending": <n>} to the tile's
// declared partitionMail path on the addressee's instance, as
// Principal{Component: xbin/mail, Via: mail, Role: writer, Partition:
// <addressee>} through the proxy — Route's delivery rule, a background start
// of class StartMail for a person's partition — and the handler pulls with
// GET /partitions/mail and acks. At-least-once until acked or expired:
// handlers dedupe by id.
//
//   - A new item rings at once; so does the addressee's start, whatever
//     starts it. While items remain it rings again after 1 min, 5 min,
//     30 min, 2 h, then every 6 h.
//   - A person's stopped partition is started only when it has run before
//     (its partition.json has lastStarted: mail never makes a person's first
//     instance, S1, S20), its person is live (PD-20), and the runner's
//     background admission and mail start rate (6 a minute per tile) allow;
//     a deferred start waits for the next ring. Otherwise the items wait for
//     the partition's next start.
//   - Nothing rings while the tile is paused, disabled, not the primary, or
//     declares no partitionMail (its code polls GET); the items wait.
//
// Boot rings every inbox holding items (loadMailBells, from SetBusDispatch),
// and an hourly sweep drops expired items, rings inboxes that lost their
// bell, and removes a removed tile's store once it holds nothing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// mailBellKey names one inbox's doorbell.
type mailBellKey struct{ tile, dep, bucket string }

// mailBell is one inbox's doorbell state.
type mailBell struct {
	timer      *time.Timer // the next re-ring
	attempt    int         // the next re-ring's step in the backoff; a new item starts it over
	ringing    bool        // a ring is in flight
	againNew   bool        // a new item came during it: ring once more, the backoff started over
	againStart time.Time   // the addressee started during it: ring once more, unless a ring reached it
	rang       time.Time   // when the last ring that reached the delivery path ended
}

// ringWhy is why an inbox rings now.
type ringWhy int

const (
	// ringNew: a new item — at once, and its backoff starts over (a sender
	// resetting it for a person's stopped partition meets the runner's mail
	// start rate).
	ringNew ringWhy = iota
	// ringStart: the addressee started — at once, the backoff kept, unless
	// a ring was in flight at the start or came after it: often the
	// doorbell's own ring started the partition, and it was delivered to
	// the new instance. Were such a start to ring again (or to reset the
	// backoff), an item left unacked would cold-start its partition over
	// and over for its whole ttl (S20); a handler that crashes on an item
	// would loop.
	ringStart
	// ringTimer: the backoff's next step.
	ringTimer
)

// mailDispatch is the delivery path: the bus's (the proxy, installed at boot
// by SetBusDispatch), or a test's.
func (b *Broker) mailDispatch() BusDispatch {
	ms := b.mail()
	ms.bellMu.Lock()
	d := ms.dispatch
	ms.bellMu.Unlock()
	if d != nil || b.bus == nil {
		return d
	}
	b.bus.mu.Lock()
	defer b.bus.mu.Unlock()
	return b.bus.dispatch
}

// mailPrincipal is a doorbell's identity (02 §5).
func mailPrincipal(dep string, part util.Partition) auth.Principal {
	if dep == util.MainDeployment {
		dep = ""
	}
	return auth.Principal{Component: partitionMailPrincipal, Via: "mail", Role: "writer", Deployment: dep, Partition: part}
}

// ringMail rings inbox k now, for why (at: a start's time, for ringStart).
// A ring in flight takes a new item or a start as one more ring after it.
func (b *Broker) ringMail(k mailBellKey, why ringWhy, at time.Time) {
	ms := b.mail()
	ms.bellMu.Lock()
	if ms.closed {
		ms.bellMu.Unlock()
		return
	}
	bl := ms.bells[k]
	if bl == nil {
		bl = &mailBell{}
		ms.bells[k] = bl
	}
	switch {
	case why == ringNew:
		bl.attempt = 0
	case why == ringStart && !bl.rang.Before(at): // a ring reached the start: it has had its ring
		ms.bellMu.Unlock()
		return
	}
	if bl.ringing {
		switch why {
		case ringNew:
			bl.againNew = true
		case ringStart:
			if at.After(bl.againStart) {
				bl.againStart = at
			}
		}
		ms.bellMu.Unlock()
		return
	}
	if bl.timer != nil {
		bl.timer.Stop()
		bl.timer = nil
	}
	bl.ringing = true
	ms.bellMu.Unlock()
	go b.rungMail(k, bl)
}

// rungMail rings once, then schedules what follows.
func (b *Broker) rungMail(k mailBellKey, bl *mailBell) {
	pending, dispatched := b.ringOnce(k)
	ms := b.mail()
	ms.bellMu.Lock()
	if ms.bells[k] != bl { // forgotten meanwhile (acked, dropped, wiped)
		ms.bellMu.Unlock()
		return
	}
	bl.ringing = false
	if dispatched {
		bl.rang = time.Now()
	}
	// a new item during the ring (after its last count, maybe), or a start
	// this ring didn't reach (it was held, say): ring again
	againNew, startAt := bl.againNew, bl.againStart
	again := againNew || !startAt.IsZero() && bl.rang.Before(startAt)
	bl.againNew, bl.againStart = false, time.Time{}
	switch {
	case again:
		why := ringStart
		if againNew {
			why = ringNew
		}
		ms.bellMu.Unlock()
		b.ringMail(k, why, startAt)
		return
	case pending == 0:
		delete(ms.bells, k)
	default:
		backoff := knobs().backoff
		d := backoff[min(bl.attempt, len(backoff)-1)]
		bl.attempt++
		bl.timer = time.AfterFunc(d, func() { b.ringMail(k, ringTimer, time.Time{}) })
	}
	ms.bellMu.Unlock()
}

// forgetBell drops inbox k's doorbell: nothing left to ring.
func (b *Broker) forgetBell(k mailBellKey) {
	ms := b.mail()
	ms.bellMu.Lock()
	defer ms.bellMu.Unlock()
	if bl := ms.bells[k]; bl != nil {
		if bl.timer != nil {
			bl.timer.Stop()
		}
		delete(ms.bells, k)
	}
}

// forgetBellsOf drops every doorbell of tile.
func (b *Broker) forgetBellsOf(tile string) {
	ms := b.mail()
	ms.bellMu.Lock()
	defer ms.bellMu.Unlock()
	for k, bl := range ms.bells {
		if k.tile == tile {
			if bl.timer != nil {
				bl.timer.Stop()
			}
			delete(ms.bells, k)
		}
	}
}

// ringOnce rings inbox k if it may ring now, answering how many items it
// holds after (0: nothing left to ring for), and whether the ring went to
// the delivery path.
func (b *Broker) ringOnce(k mailBellKey) (pending int, dispatched bool) {
	n, meta, err := b.mailPending(k.tile, k.dep, k.bucket)
	if err != nil {
		slog.Warn("partition mail: an inbox can't be read for its doorbell", "tile", k.tile, "err", err)
		return 1, false // try again later
	}
	if n == 0 {
		return 0, false
	}
	path, part, why := b.mailBellHeld(k, meta)
	dispatch := b.mailDispatch()
	if why != "" || dispatch == nil {
		return n, false // the items wait: a later ring, a start or a new item
	}
	body, _ := json.Marshal(map[string]any{"partition": part, "pending": n})
	ctx, cancel := context.WithTimeout(context.Background(), knobs().ringTimeout)
	code, _ := dispatch(ctx, mailPrincipal(k.dep, part), k.tile, path, body)
	cancel()
	if code/100 != 2 { // the status only: the body is the addressee's (PD-46)
		slog.Info("partition mail: a doorbell wasn't answered", "tile", k.tile, "partition", part, "status", code)
	}
	if n, _, err = b.mailPending(k.tile, k.dep, k.bucket); err != nil {
		return 1, true
	}
	return n, true
}

// mailBellHeld is where inbox k's doorbell rings and whose it is, or why it
// may not ring now ("": it may).
func (b *Broker) mailBellHeld(k mailBellKey, meta mailBoxMeta) (path string, part util.Partition, why string) {
	c, ok := b.Reg.Component(k.tile)
	switch {
	case !ok:
		return "", "", "no such tile"
	case b.Reg.LifecycleState(k.tile) != registry.StateEnabled:
		return "", "", "the tile isn't enabled"
	}
	spec, on := c.Partitioned()
	switch {
	case !on || !spec.User || !spec.Global:
		return "", "", "the tile isn't partitioned with a global instance"
	case c.Manifest.PartitionMail == "":
		return "", "", "the tile declares no partitionMail: its code polls"
	case !b.isPrimary(k.tile, k.dep) || b.partitionPaused(k.tile, k.dep):
		return "", "", "the tile's primary is elsewhere, or paused"
	case k.bucket == mailGlobalBox:
		return c.Manifest.PartitionMail, util.PartitionGlobal, ""
	case meta.User == "" || b.personLive(meta.User, k.tile) != nil:
		return "", "", "the person isn't live on the tile"
	}
	if uid := b.storedPartitionUID(meta.User); uid == "" || util.PartitionKey(meta.User, uid) != k.bucket {
		return "", "", "an earlier incarnation's inbox"
	}
	dir, err := b.partitionRecordDir(k.tile, k.dep, k.bucket)
	if err != nil {
		return "", "", err.Error()
	}
	if rec, ok, err := readPartitionRecordAt(dir); err != nil || !ok || rec.LastStarted == "" {
		return "", "", "the partition never ran: mail never starts a person's first instance"
	}
	return c.Manifest.PartitionMail, util.UserPartition(meta.User), ""
}

// mailPartitionStarted is a person's partition starting (the partitions'
// records see every spawn, notePartitionStart): its doorbell rings if its
// inbox holds items — the items that waited for it — keeping its backoff,
// and not at all when a ring reached the start (ringStart: the start may be
// the doorbell's own).
func (b *Broker) mailPartitionStarted(tile, dep, pkey string) {
	k, at := mailBellKey{tile, cmp0(dep), pkey}, time.Now()
	time.AfterFunc(knobs().startDelay, func() {
		if n, _, err := b.mailPending(k.tile, k.dep, k.bucket); err == nil && n > 0 {
			b.ringMail(k, ringStart, at)
		}
	})
}

func cmp0(dep string) string {
	if dep == "" {
		return util.MainDeployment
	}
	return dep
}

// ---- boot, the sweep, removed tiles ----

// loadMailBells starts the doorbells once the delivery path is installed
// (SetBusDispatch): a sweep a moment after boot rings every inbox holding
// items, and the sweep repeats hourly.
func (b *Broker) loadMailBells() {
	ms := b.mail()
	ms.bellMu.Lock()
	defer ms.bellMu.Unlock()
	if ms.started {
		return
	}
	ms.started = true
	var sweep func()
	sweep = func() {
		b.sweepMail()
		ms.bellMu.Lock()
		if !ms.closed {
			ms.sweeper = time.AfterFunc(knobs().sweepEvery, sweep)
		}
		ms.bellMu.Unlock()
	}
	ms.sweeper = time.AfterFunc(knobs().bootDelay, sweep)
}

// closeMail stops the sweep and every doorbell (Broker.Close).
func (b *Broker) closeMail() {
	v, ok := mailStates.Load(b) // kept, closed: a late timer finds it and does nothing
	if !ok {
		return
	}
	ms := v.(*mailState)
	ms.bellMu.Lock()
	defer ms.bellMu.Unlock()
	ms.closed = true
	if ms.sweeper != nil {
		ms.sweeper.Stop()
	}
	for k, bl := range ms.bells {
		if bl.timer != nil {
			bl.timer.Stop()
		}
		delete(ms.bells, k)
	}
}

// sweepMail drops every store's expired items, rings each inbox that holds
// items and has no doorbell, and removes the store of a tile no longer
// registered once it holds nothing (mail is transient: a removed tile's
// items wait out their ttl, then go). It holds one tile's lock at a time.
func (b *Broker) sweepMail() {
	stores, err := b.eachMailStore("")
	if err != nil {
		slog.Warn("partition mail: a store this xbind can't read; kept", "err", err)
	}
	now := mailNow()
	var ring []mailBellKey
	for _, s := range stores {
		ring = append(ring, b.sweepMailStore(s, now)...)
	}
	if len(ring) > 0 {
		slog.Info("partition mail: the sweep rings the inboxes holding items", "inboxes", len(ring))
	}
	ms := b.mail()
	for _, k := range ring {
		ms.bellMu.Lock()
		_, has := ms.bells[k]
		ms.bellMu.Unlock()
		if !has {
			b.ringMail(k, ringTimer, time.Time{})
		}
	}
}

// sweepMailStore sweeps one store under its tile's lock, answering the
// inboxes that hold items.
func (b *Broker) sweepMailStore(s mailStoreOf, now time.Time) (ring []mailBellKey) {
	unlock := b.mail().lockKey(mailPathKey(s.path))
	defer unlock()
	err := withMailStore(s.path, s.tile, s.dep, false, func(db *bolt.DB) error {
		return mailUpdate(db, func(tx *bolt.Tx) (bool, error) {
			var names []string
			_ = tx.ForEach(func(name []byte, _ *bolt.Bucket) error {
				if string(name) != string(mailMetaBucket) {
					names = append(names, string(name))
				}
				return nil
			})
			changed := false
			for _, name := range names {
				u, purged, err := purgeExpired(tx, name, now, false)
				if err != nil {
					return false, err
				}
				changed = changed || purged
				if u.items > 0 {
					ring = append(ring, mailBellKey{s.tile, s.dep, name})
				}
			}
			return changed, nil
		})
	})
	switch {
	case errors.Is(err, errNoMailStore):
		return nil
	case err != nil:
		slog.Warn("partition mail: sweeping a store", "tile", s.tile, "err", err)
		return nil
	}
	if _, registered := b.Reg.Component(s.tile); len(ring) == 0 && !registered {
		if err := os.Remove(s.path); err == nil {
			removeEmptyDirs(filepath.Dir(s.path))
			slog.Info("partition mail: a removed tile's empty store removed", "tile", s.tile)
		}
	}
	return ring
}

// mailLeftovers are the mail stores tiles no longer registered at or under
// a path left (pathLeftovers, through partitionLeftovers): a new tile there
// would read them.
func (b *Broker) mailLeftovers(gone func(string) bool) []string {
	stores, _ := b.eachMailStore("")
	var out []string
	for _, s := range stores {
		if gone(s.tile) {
			out = append(out, fmt.Sprintf("%s's partition mail (deployment %s)", s.tile, s.dep))
		}
	}
	return out
}

// mailTileCreated removes the mail stores of a removed tile at path, or
// under it, when a tile is created there (assignOwner): the new tile never
// reads a removed one's mail.
func (b *Broker) mailTileCreated(path string) {
	stores, _ := b.eachMailStore("")
	var dropped []string
	for _, s := range stores {
		_, registered := b.Reg.Component(s.tile)
		if s.tile != path && (!strings.HasPrefix(s.tile, path+"/") || registered) {
			continue
		}
		unlock := b.mail().lockKey(mailPathKey(s.path))
		err := os.Remove(s.path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			removeEmptyDirs(filepath.Dir(s.path))
		}
		unlock()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("partition mail: a removed tile's store can't be removed", "tile", s.tile, "err", err)
			continue
		}
		dropped = append(dropped, s.tile)
	}
	for _, t := range dropped {
		b.forgetBellsOf(t)
	}
}
