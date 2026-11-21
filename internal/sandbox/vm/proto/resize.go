package proto

import (
	"context"
	"sync"
	"time"
)

// Resizes keeps a host's side of resize ordering for one tty session (see
// Msg): the resizes it numbered and the agent hasn't answered yet, so that
// the session's input can wait for the ones sent before it. xbind's tile
// sandbox sessions and the VM shim's terminal session use it. The zero
// value numbers nothing until Enable.
type Resizes struct {
	mu      sync.Mutex
	on      bool
	seq     uint64
	pending map[uint64]struct{}
	changed chan struct{} // closed (and replaced) at every End
}

// Enable: the agent answers this session's numbered resizes (its "started"
// said ResizeAcks).
func (r *Resizes) Enable() {
	r.mu.Lock()
	r.on = true
	r.mu.Unlock()
}

// Begin numbers a resize about to be sent: 0 when the agent won't answer
// it (send it unnumbered; nothing will wait for it).
func (r *Resizes) Begin() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.on {
		return 0
	}
	r.seq++
	if r.pending == nil {
		r.pending, r.changed = map[uint64]struct{}{}, make(chan struct{})
	}
	r.pending[r.seq] = struct{}{}
	return r.seq
}

// End: resize seq was answered ("resized"), or won't be (its send failed).
func (r *Resizes) End(seq uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.pending[seq]; !ok {
		return // never sent, or ended already
	}
	delete(r.pending, seq)
	close(r.changed)
	r.changed = make(chan struct{})
}

// Await waits until every resize begun before the call has ended, so input
// written next reaches the terminal after them. With none outstanding it
// returns at once: a keystroke costs no round trip, only the first after a
// resize waits for one. It ends early when end closes (the session is
// over: nil) or ctx ends (ctx's error). Past d the agent isn't answering:
// those resizes are given up — nothing waits for them again — and expired
// is true, for the caller to log; the input goes on.
func (r *Resizes) Await(ctx context.Context, end <-chan struct{}, d time.Duration) (expired bool, err error) {
	r.mu.Lock()
	upto := r.seq
	r.mu.Unlock()
	var t *time.Timer
	for {
		r.mu.Lock()
		waiting := false
		for seq := range r.pending {
			if seq <= upto {
				waiting = true
				break
			}
		}
		ch := r.changed
		r.mu.Unlock()
		if !waiting {
			return false, nil
		}
		if t == nil {
			t = time.NewTimer(d)
			defer t.Stop()
		}
		select {
		case <-ch:
		case <-end:
			return false, nil
		case <-ctx.Done():
			return false, ctx.Err()
		case <-t.C:
			r.mu.Lock()
			for seq := range r.pending {
				if seq <= upto {
					delete(r.pending, seq)
				}
			}
			r.mu.Unlock()
			return true, nil
		}
	}
}
