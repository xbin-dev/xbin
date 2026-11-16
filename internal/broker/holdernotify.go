package broker

// holdernotify.go — xbind's notices to a delegation holder's backend (D228
// part C0; docs/protocol/delegation.md §Notices). The workspace agent's
// backend must learn, without polling, that something it waits on changed:
// a request was decided, expired or withdrawn; a standing approval came or
// went; a change set was decided or applied; a session it started changed
// status; its person paused or resumed; delegation was switched off.
//
// A notice is a POST xbind makes to the person's partition of the holder,
// on the path the holder's table entry names (auth.DelegateNotifyPath),
// over the delivery path partition mail's doorbell uses (the same
// principal: xbin/mail, role writer, the person's partition — Route and
// the proxy already know it; no new kind of principal). Its body is
//
//	{"partition": "user:<id>", "events": [{"kind": "...", "id": "..."}]}
//
// and NOTHING else: no state, no text, no result. The backend re-reads
// what an event names through the API, as the person's delegate. So a
// notice is a hint and its content untrusted-input-shaped for the holder:
// it must treat an event as "look again", never as a fact.
//
// Coalesced: events for one person wait in one queue, a kind+id once, and
// leave together. At least once: an event leaves the queue only when a
// delivery that carried it was answered 2xx; otherwise it is sent again
// with backoff for up to holderNotifyKeep, then dropped (the backend's own
// re-read on start covers a holder that was down that long). Bounded:
// holderNotifyMax events a person.

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

const (
	holderNotifyMax  = 256
	holderNotifyKeep = time.Hour
)

// holderNotifyBackoff is the wait before each retry (the last repeats);
// a var for tests.
var holderNotifyBackoff = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute}

// HolderEvent is one line of a notice.
type HolderEvent struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

type holderQueue struct {
	events  []HolderEvent
	since   map[HolderEvent]time.Time
	sending bool
	attempt int
	timer   *time.Timer
}

type holderNotifier struct {
	mu     sync.Mutex
	queues map[[2]string]*holderQueue // holder, user
	closed bool
	wg     sync.WaitGroup
	ctx    context.Context
	stop   context.CancelFunc
}

func (b *Broker) holderNotes() *holderNotifier {
	b.holderOnce.Do(func() {
		b.holderN = &holderNotifier{queues: map[[2]string]*holderQueue{}}
		b.holderN.ctx, b.holderN.stop = context.WithCancel(context.Background())
	})
	return b.holderN
}

// NotifyHolder queues a notice for user's partition of holder: kind (and
// id) changed. A holder with no notice path is told nothing.
func (b *Broker) NotifyHolder(holder, user, kind, id string) {
	if holder == "" || user == "" || kind == "" || auth.DelegateNotifyPath(holder) == "" {
		return
	}
	n := b.holderNotes()
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return
	}
	key := [2]string{holder, user}
	q := n.queues[key]
	if q == nil {
		q = &holderQueue{since: map[HolderEvent]time.Time{}}
		n.queues[key] = q
	}
	ev := HolderEvent{Kind: kind, ID: id}
	if _, dup := q.since[ev]; !dup {
		if len(q.events) >= holderNotifyMax { // the oldest goes: the backend re-reads everything on a "resync"
			old := q.events[0]
			q.events = q.events[1:]
			delete(q.since, old)
			resync := HolderEvent{Kind: "resync"}
			if _, has := q.since[resync]; !has {
				q.events, q.since[resync] = append(q.events, resync), time.Now()
			}
		}
		q.events, q.since[ev] = append(q.events, ev), time.Now()
	}
	q.attempt = 0
	if q.timer != nil {
		q.timer.Stop()
		q.timer = nil
	}
	b.sendHolderLocked(n, key, q)
}

// sendHolderLocked starts a delivery of q unless one is in flight (it
// sends again when it ends, if events came meanwhile). n.mu is held.
func (b *Broker) sendHolderLocked(n *holderNotifier, key [2]string, q *holderQueue) {
	if q.sending || len(q.events) == 0 || n.closed {
		return
	}
	q.sending = true
	batch := append([]HolderEvent(nil), q.events...)
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		ok := b.deliverHolder(n.ctx, key[0], key[1], batch)
		n.mu.Lock()
		defer n.mu.Unlock()
		q.sending = false
		now := time.Now()
		if ok {
			for _, ev := range batch {
				delete(q.since, ev)
			}
			q.attempt = 0
		} else {
			q.attempt++
		}
		kept := q.events[:0]
		for _, ev := range q.events {
			if at, waiting := q.since[ev]; waiting && now.Sub(at) < holderNotifyKeep {
				kept = append(kept, ev)
			} else {
				delete(q.since, ev)
			}
		}
		q.events = kept
		switch {
		case len(q.events) == 0:
			delete(n.queues, key)
		case n.closed:
		case ok: // more came while it was in flight
			b.sendHolderLocked(n, key, q)
		default:
			d := holderNotifyBackoff[min(q.attempt-1, len(holderNotifyBackoff)-1)]
			q.timer = time.AfterFunc(d, func() {
				n.mu.Lock()
				defer n.mu.Unlock()
				q.timer = nil
				b.sendHolderLocked(n, key, q)
			})
		}
	}()
}

// deliverHolder makes one delivery; true when the holder answered 2xx.
func (b *Broker) deliverHolder(ctx context.Context, holder, user string, events []HolderEvent) bool {
	path, dispatch := auth.DelegateNotifyPath(holder), b.mailDispatch()
	if path == "" || dispatch == nil {
		return false
	}
	part := util.UserPartition(user)
	body, _ := json.Marshal(map[string]any{"partition": string(part), "events": events})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	code, _ := dispatch(ctx, mailPrincipal(b.primaryOf(holder), part), holder, path, body)
	return code/100 == 2
}

// closeHolderNotes ends the deliveries in flight and waits for them.
func (b *Broker) closeHolderNotes() {
	if b.holderN == nil {
		return
	}
	n := b.holderN
	n.mu.Lock()
	n.closed = true
	for _, q := range n.queues {
		if q.timer != nil {
			q.timer.Stop()
		}
	}
	n.mu.Unlock()
	n.stop()
	n.wg.Wait()
}
