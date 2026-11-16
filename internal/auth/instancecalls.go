package auth

// instancecalls.go — the calls to xbind's own API (/api/xbin/*) that a
// backend generation has in flight, by its instance token. The runner's
// health check reads them: a generation still waiting on xbind's answer
// before it listens isn't charged for xbind's time, and one that never
// listens says which call it was waiting on (docs/sdk.md → Startup).

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// InstanceCall is one call a generation has in flight: its route ("PUT
// /api/xbin/cron/jobs" — the pattern, never the path's data) and when it
// arrived.
type InstanceCall struct {
	Route string
	Since time.Time
}

type instanceCalls struct {
	mu    sync.Mutex
	next  uint64
	calls map[string]map[uint64]InstanceCall // instance token → its calls in flight
}

// TrackInstanceCall records r, a call to xbind's own API on route, as in
// flight for the instance token it carries, until release runs. A request
// that carries no instance token records nothing.
func (a *Auth) TrackInstanceCall(r *http.Request, route string) (release func()) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return func() {}
	}
	tok := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	if _, ok := a.lookupInstance(tok); !ok {
		return func() {}
	}
	c := &a.calls
	c.mu.Lock()
	if c.calls == nil {
		c.calls = map[string]map[uint64]InstanceCall{}
	}
	if c.calls[tok] == nil {
		c.calls[tok] = map[uint64]InstanceCall{}
	}
	c.next++
	id := c.next
	c.calls[tok][id] = InstanceCall{Route: route, Since: time.Now()}
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.calls[tok], id)
			if len(c.calls[tok]) == 0 {
				delete(c.calls, tok)
			}
			c.mu.Unlock()
		})
	}
}

// InstanceCalls lists the calls the generation holding token has in flight,
// oldest first.
func (a *Auth) InstanceCalls(token string) []InstanceCall {
	c := &a.calls
	c.mu.Lock()
	out := make([]InstanceCall, 0, len(c.calls[token]))
	for _, call := range c.calls[token] {
		out = append(out, call)
	}
	c.mu.Unlock()
	sort.Slice(out, func(i, k int) bool { return out[i].Since.Before(out[k].Since) })
	return out
}
