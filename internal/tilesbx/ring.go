package tilesbx

// ring.go — an exec's output ring (plans/tile-sandbox-runtime.md §3.6, §6.4):
// the last bytes of its one output stream, addressed by their offset in the
// stream since it began, and read with a long-poll. A ring grows lazily up
// to the policy's outputRingMiB. A tile's rings share its outputBudgetMiB:
// when they hold more, the oldest finished exec's bytes go first (its
// ringStart moves up to its total, which the contract allows), then the
// oldest bytes of the running ring that holds the most.

import (
	"context"
	"sync"
	"time"
	"unicode/utf8"
)

// ringMinCap is a ring's first allocation; it doubles up to its cap.
const ringMinCap = 16 << 10

// ringBudget is one tile's rings and the bytes they may hold together. Its
// mutex guards every field of every ring in it: an append may drop another
// ring's bytes.
type ringBudget struct {
	mu    sync.Mutex
	max   int64 // bytes all its rings may hold (0: no bound)
	used  int64 // bytes they hold
	rings map[*ring]struct{}
	fin   []*ring // finished rings still holding bytes, oldest end first
}

func newRingBudget(max int64) *ringBudget {
	return &ringBudget{max: max, rings: map[*ring]struct{}{}}
}

// setMax changes the bound (the policy changed) and applies it now.
func (b *ringBudget) setMax(max int64) {
	b.mu.Lock()
	b.max = max
	b.evictLocked()
	b.mu.Unlock()
}

// held is the bytes its rings hold.
func (b *ringBudget) held() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// evictLocked drops bytes until the rings fit the budget: every byte of
// the oldest finished ring first, then the oldest bytes of the running ring
// holding the most.
func (b *ringBudget) evictLocked() {
	for b.max > 0 && b.used > b.max {
		if len(b.fin) > 0 {
			f := b.fin[0]
			b.fin = b.fin[1:]
			b.used -= int64(f.n)
			f.release()
			continue
		}
		var big *ring
		for r := range b.rings {
			if !r.ended && (big == nil || r.n > big.n) {
				big = r
			}
		}
		if big == nil || big.n == 0 {
			return
		}
		k := int(min(b.used-b.max, int64(big.n)))
		big.trim(k)
		b.used -= int64(k)
	}
}

// ring is one exec's output: bytes [total-n, total) of its stream, kept in
// a circular buffer. Every field is guarded by b.mu.
type ring struct {
	b       *ringBudget
	max     int    // its own cap
	buf     []byte // circular; len(buf) is its capacity
	head    int    // where the oldest byte held is
	n       int    // bytes held
	total   int64  // bytes the stream has had
	ended   bool   // the exec ended: no more bytes come
	dropped bool   // forgotten: it holds nothing and takes nothing
	wake    chan struct{}
}

// newRing is an empty ring in b, holding at most max bytes.
func newRing(b *ringBudget, max int) *ring {
	r := &ring{b: b, max: max, wake: make(chan struct{})}
	b.mu.Lock()
	b.rings[r] = struct{}{}
	b.mu.Unlock()
	return r
}

// notifyLocked wakes every long-poll on the ring.
func (r *ring) notifyLocked() {
	close(r.wake)
	r.wake = make(chan struct{})
}

// Write appends the stream's next bytes. What the ring can't hold (past its
// cap, or the tile's budget) drops the oldest bytes; total counts them all.
func (r *ring) Write(p []byte) {
	if len(p) == 0 {
		return
	}
	b := r.b
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.dropped || r.ended {
		return
	}
	r.total += int64(len(p))
	if len(p) > r.max {
		p = p[len(p)-r.max:]
	}
	before := r.n
	r.put(p)
	b.used += int64(r.n - before)
	b.evictLocked()
	r.notifyLocked()
}

// put stores p (≤ max bytes) after the bytes held, growing the buffer up
// to max and dropping the oldest bytes past it.
func (r *ring) put(p []byte) {
	if need := r.n + len(p); need > len(r.buf) && len(r.buf) < r.max {
		r.grow(min(r.max, max(need, 2*len(r.buf), ringMinCap)))
	}
	c := len(r.buf)
	if over := r.n + len(p) - c; over > 0 {
		r.trim(over)
	}
	w := (r.head + r.n) % c
	k := copy(r.buf[w:], p)
	copy(r.buf, p[k:])
	r.n += len(p)
}

// grow moves the bytes held into a buffer of c bytes, oldest first.
func (r *ring) grow(c int) {
	nb := make([]byte, c)
	r.copyOut(nb, 0, r.n)
	r.buf, r.head = nb, 0
}

// copyOut copies cnt bytes held, from the off-th oldest, into dst.
func (r *ring) copyOut(dst []byte, off, cnt int) {
	if cnt == 0 {
		return
	}
	c := len(r.buf)
	pos := (r.head + off) % c
	k := copy(dst[:cnt], r.buf[pos:min(pos+cnt, c)])
	copy(dst[k:cnt], r.buf[:cnt-k])
}

// trim drops the k oldest bytes held.
func (r *ring) trim(k int) {
	if k <= 0 {
		return
	}
	r.n -= k
	if r.n == 0 {
		r.head = 0
		return
	}
	r.head = (r.head + k) % len(r.buf)
}

// release drops every byte held, and the buffer.
func (r *ring) release() {
	r.buf, r.head, r.n = nil, 0, 0
}

// End marks the stream ended: a long-poll answers at once, and its bytes
// are the first the budget drops.
func (r *ring) End() {
	b := r.b
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.ended || r.dropped {
		return
	}
	r.ended = true
	if r.n > 0 {
		b.fin = append(b.fin, r)
	}
	r.notifyLocked()
}

// Drop forgets the ring: its bytes go back to the budget (the exec was
// deleted or pruned).
func (r *ring) Drop() {
	b := r.b
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.dropped {
		return
	}
	r.dropped = true
	b.used -= int64(r.n)
	r.release()
	delete(b.rings, r)
	for i, f := range b.fin {
		if f == r {
			b.fin = append(b.fin[:i], b.fin[i+1:]...)
			break
		}
	}
	r.notifyLocked()
}

// chunk is a read of a ring.
type chunk struct {
	start, end, total, ringStart int64
	data                         []byte
	ended                        bool
}

// Read is the bytes from offset since (or the oldest held, when since was
// dropped), at most limit of them. since past the stream's end reads
// nothing at its end.
func (r *ring) Read(since, limit int64) chunk {
	b := r.b
	b.mu.Lock()
	defer b.mu.Unlock()
	c := chunk{total: r.total, ringStart: r.total - int64(r.n), ended: r.ended || r.dropped}
	c.start = min(max(since, c.ringStart), r.total)
	c.end = min(r.total, c.start+limit)
	if n := int(c.end - c.start); n > 0 {
		c.data = make([]byte, n)
		r.copyOut(c.data, int(c.start-c.ringStart), n)
	}
	return c
}

// Tail is the last n bytes held (a TTY's replay).
func (r *ring) Tail(n int) []byte {
	b := r.b
	b.mu.Lock()
	defer b.mu.Unlock()
	n = min(n, r.n)
	out := make([]byte, n)
	r.copyOut(out, r.n-n, n)
	return out
}

// Total is the bytes the stream has had.
func (r *ring) Total() int64 {
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	return r.total
}

// Wait waits until the stream has bytes past since, or has ended — at most
// d, and not past ctx.
func (r *ring) Wait(ctx context.Context, since int64, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		b := r.b
		b.mu.Lock()
		if r.total > since || r.ended || r.dropped {
			b.mu.Unlock()
			return
		}
		wake := r.wake
		b.mu.Unlock()
		select {
		case <-wake:
		case <-t.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// textCut is where a text read ends so that it never splits a character: a
// UTF-8 sequence cut short at the end of data is left for the next read —
// unless it is all the read has, or the stream ends there (then the bytes
// are replaced like any invalid ones).
func textCut(data []byte, final bool) int {
	if final {
		return len(data)
	}
	for i := len(data) - 1; i >= 0 && i >= len(data)-utf8.UTFMax; i-- {
		if utf8.RuneStart(data[i]) {
			if !utf8.FullRune(data[i:]) && i > 0 {
				return i
			}
			break
		}
	}
	return len(data)
}
