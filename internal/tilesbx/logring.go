package tilesbx

// logring.go — a sandbox's log: what its first process (the init's
// `[sbx]` trace, then the agent) writes to stdout and stderr, kept in a
// ring per sandbox so the last of it can say why a start failed or a run
// ended (plans/tile-sandbox-runtime.md §2.4). Sessions never write here:
// their output goes to their own streams.

import (
	"bytes"
	"strings"
	"sync"
)

// logRingSize is how much of a sandbox's log is kept.
const logRingSize = 64 << 10

// logRing keeps the last logRingSize bytes written to it. Safe for
// concurrent use; the zero value is empty and ready.
type logRing struct {
	mu    sync.Mutex
	buf   []byte // at most logRingSize, oldest first
	lost  bool   // bytes were dropped off the front
	total int64  // bytes ever written: a mark (Mark) is an offset in it
}

func (l *logRing) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	l.total += int64(n)
	if len(p) >= logRingSize {
		p = p[len(p)-logRingSize:]
		l.buf, l.lost = l.buf[:0], true
	}
	if over := len(l.buf) + len(p) - logRingSize; over > 0 {
		l.buf, l.lost = append(l.buf[:0], l.buf[over:]...), true
	}
	l.buf = append(l.buf, p...)
	return n, nil
}

// Mark is where the log is now: a run's start, for TailSince.
func (l *logRing) Mark() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

// Tail is the last lines of the log (at most n), trimmed; "" when empty.
func (l *logRing) Tail(n int) string { return l.TailSince(0, n) }

// TailSince is Tail of what was written after mark (what the ring still
// holds of it): one run's own lines, never an earlier run's.
func (l *logRing) TailSince(mark int64, n int) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buf
	if since := l.total - mark; since < int64(len(b)) {
		b = b[int64(len(b))-max(since, 0):]
	}
	b = bytes.TrimRight(b, "\n\r\t ")
	lines := strings.Split(string(b), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// String is the whole ring.
func (l *logRing) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.buf)
}
