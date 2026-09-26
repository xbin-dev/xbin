package relay

import (
	"sync"
	"time"
)

// Rate is a token-bucket rate: Burst tokens, refilled at PerHour tokens an
// hour. A zero Rate never limits.
type Rate struct {
	PerHour float64
	Burst   float64
}

// limiter is a set of token buckets keyed by string (a workspace id, a
// handle, a client IP). Idle buckets that have refilled completely are
// forgotten, so the map stays bounded by the keys active within the refill
// time.
type limiter struct {
	mu      sync.Mutex
	rate    Rate
	buckets map[string]*bucket
	now     func() time.Time
	sweep   time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter(r Rate, now func() time.Time) *limiter {
	return &limiter{rate: r, buckets: map[string]*bucket{}, now: now}
}

// allow takes a token for key; when none is left it reports how long until
// one is.
func (l *limiter) allow(key string) (bool, time.Duration) {
	if l == nil || l.rate.PerHour <= 0 || l.rate.Burst <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	perSec := l.rate.PerHour / 3600
	if now.Sub(l.sweep) > 10*time.Minute {
		l.sweep = now
		for k, b := range l.buckets {
			if b.tokens+now.Sub(b.at).Seconds()*perSec >= l.rate.Burst {
				delete(l.buckets, k)
			}
		}
	}
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.rate.Burst, at: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.rate.Burst, b.tokens+now.Sub(b.at).Seconds()*perSec)
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / perSec * float64(time.Second))
	return false, wait
}
