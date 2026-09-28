// access.go — is the person a key names still one of this tile's users?
//
// A key outlives the page call that registered it: its person may since
// have been removed from the workspace, disabled, or taken off this tile.
// xbind says what they may do now (GET /api/xbin/access/<user>,
// xbin.AccessOf — the level X-XBin-User-Level would carry). It is asked at
// every SSH login, at every session a connection opens, on key
// registration, and every half minute while a connection lives; answers are
// kept 30 s (the level on a request from the tile's page is xbind's answer
// too, and kept the same way — tile.go seen). It fails closed: no answer,
// no login (a live connection isn't cut over a failed check — only over an
// answer that says no).
//
// A person without read access is told "access revoked" and their keys are
// marked inactive — kept, not deleted: access may come back, and the next
// check that finds it marks them active again.
package main

import (
	"context"
	"log"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const (
	accessTTL    = 30 * time.Second // an answer is kept this long
	accessErrTTL = 2 * time.Second  // xbind didn't answer: ask again after this
)

// accessEntry is one person's cached answer (or failure).
type accessEntry struct {
	a   xbin.UserAccess
	err error
	at  time.Time
}

// access is what person may do on this tile now: cached, and it marks their
// keys inactive (or active again) as the answer says.
func (t *Tile) access(ctx context.Context, person string) (xbin.UserAccess, error) {
	now := time.Now()
	t.accMu.Lock()
	if e, ok := t.accCache[person]; ok {
		ttl := accessTTL
		if e.err != nil {
			ttl = accessErrTTL
		}
		if now.Sub(e.at) < ttl {
			t.accMu.Unlock()
			return e.a, e.err
		}
	}
	t.accMu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	a, err := t.accessOf(cctx, person)
	cancel()
	if err != nil && ctx.Err() != nil {
		return a, err // the caller gave up: nothing learned
	}
	t.accMu.Lock()
	if len(t.accCache) >= 4096 { // forget the stale ones
		for k, e := range t.accCache {
			if now.Sub(e.at) >= accessTTL {
				delete(t.accCache, k)
			}
		}
	}
	t.accCache[person] = accessEntry{a: a, err: err, at: now}
	t.accMu.Unlock()
	if err != nil {
		log.Printf("ssh: xbind didn't say what %s may do on this tile: %v", person, err)
		return a, err
	}
	t.noteAccess(person, a.CanRead())
	return a, nil
}

// noteAccess marks person's keys inactive (no read access) or active again —
// one write, and only when a mark changes.
func (t *Tile) noteAccess(person string, ok bool) {
	if person == "" {
		return
	}
	now := time.Now().UnixMilli()
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.keysLoaded {
		return
	}
	var next []keyRec
	for i, k := range t.keys {
		if k.User != person || (k.Inactive != 0) == !ok {
			continue
		}
		if next == nil {
			next = append([]keyRec(nil), t.keys...)
		}
		if ok {
			next[i].Inactive = 0
		} else {
			next[i].Inactive = now
		}
	}
	if next != nil {
		if err := t.saveKeysLocked(next); err != nil {
			log.Printf("ssh: marking %s's keys %s: %v", person, map[bool]string{true: "active", false: "inactive"}[ok], err)
		}
	}
}

// recheck asks xbind about every person keys names (eight at a time, five
// seconds in all; answers cached), so everyone's keys show whose access is
// gone. A person it couldn't ask keeps the mark they had.
func (t *Tile) recheck(ctx context.Context, keys []keyRec) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k.User] {
			continue
		}
		seen[k.User] = true
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func(person string) {
			defer func() { <-sem; wg.Done() }()
			_, _ = t.access(ctx, person)
		}(k.User)
	}
	wg.Wait()
}

// refusedWhy is what a person without access (or whose access xbind
// didn't confirm) is told; "" = they may go on.
func refusedWhy(a xbin.UserAccess, err error) string {
	switch {
	case err != nil:
		return "your access to this tile can't be checked right now (xbind didn't answer) — try again in a moment"
	case !a.Active:
		return "access revoked: your account is disabled or was removed from this workspace. Your keys here are kept, inactive."
	case !a.CanRead():
		return "access revoked: you no longer have access to this terminal tile. Your keys here are kept, inactive — ask the tile's owner for access again."
	}
	return ""
}

// watchAccess cuts a live connection off once xbind says its person may no
// longer use this tile (checked every accessTTL while it lives; a failed
// check leaves it be).
func (t *Tile) watchAccess(ctx context.Context, lc *liveConn) {
	tick := time.NewTicker(t.accessEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		a, err := t.access(ctx, lc.user)
		if err != nil {
			continue
		}
		if why := refusedWhy(a, nil); why != "" {
			log.Printf("ssh: %s lost access to this tile: ending their connection from %s", lc.user, lc.remote)
			lc.mu.Lock()
			ss := make([]*session, 0, len(lc.sess))
			for s := range lc.sess {
				ss = append(ss, s)
			}
			lc.mu.Unlock()
			told := make(chan struct{})
			go func() { // a client that stopped reading doesn't hold the cut up
				defer close(told)
				for _, s := range ss {
					s.say("%s", why)
				}
			}()
			select {
			case <-told:
			case <-time.After(time.Second):
			}
			_ = lc.sc.Close()
			return
		}
	}
}
