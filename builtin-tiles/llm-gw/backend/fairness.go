// fairness.go — the optional fairness limit (partitionLimit, off by
// default; API.md "Partitioned callers"): how many calls one person's
// partition of a calling tile may have in flight. More wait their turn, in
// order, for at most limitWait — then they are answered 429 with a
// Retry-After, which callers retry (the agent does), rather than held on.
// The global instance and tiles that aren't partitioned are never held.
package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// maxPartitionLimit bounds partitionLimit.
const maxPartitionLimit = 64

// limitWait bounds a call's wait for a slot. Well under the agent's stream
// watchdog (90 s, armed before the response headers:
// builtin-templates/agent/_backend/llm_gateway.go llmIdleTimeout), which
// would otherwise cancel a held call as a stalled stream and send its retry
// to the back of the queue; a 429 is retried by it with a backoff.
var limitWait = 20 * time.Second

// limitRetryAfter is the Retry-After (seconds) of a call the limit turned
// away.
const limitRetryAfter = "2"

// gate is a caller's calls in flight and those waiting for a slot.
type gate struct {
	part    string // the caller's display name, for GET /stats
	n       int
	waiters []chan struct{}
}

var (
	gates    = map[callerKey]*gate{} // under callersMu
	limitNow int                     // the partitionLimit the last call saw (for releases)
)

// limited: the fairness limit applies to the caller — a person's partition.
func (c *callerRef) limited() bool { return c != nil && c.key.PartitionID != "" }

// admission is admitCaller's answer.
type admission int

const (
	admitted   admission = iota
	callerGone           // the caller hung up while waiting: nothing to answer
	limitBusy            // no slot within limitWait: answer 429
)

// admitCaller counts a call of c in flight, first waiting for a slot when
// the fairness limit holds c's partition. With admitted it answers the
// release to call when the call ends (more than once is harmless).
func admitCaller(r *http.Request, c *callerRef, limit int) (func(), admission) {
	if c == nil {
		return func() {}, admitted
	}
	release := func() func() {
		var once sync.Once
		return func() { once.Do(func() { releaseCaller(c.key) }) }
	}
	callersMu.Lock()
	if limit != limitNow { // the config changed under the calls waiting
		limitNow = limit
		for _, g := range gates {
			pumpLocked(g)
		}
	}
	g := gates[c.key]
	if g == nil {
		g = &gate{part: c.part}
		gates[c.key] = g
	}
	if !c.limited() || limit <= 0 || g.n < limit {
		g.n++
		callersMu.Unlock()
		return release(), admitted
	}
	ch := make(chan struct{})
	g.waiters = append(g.waiters, ch)
	callersMu.Unlock()
	timer := time.NewTimer(limitWait)
	defer timer.Stop()
	var why admission
	select {
	case <-ch: // a release handed its slot on (g.n already counts it)
		return release(), admitted
	case <-r.Context().Done():
		why = callerGone
	case <-timer.C:
		why = limitBusy
	}
	callersMu.Lock()
	for i, w := range g.waiters {
		if w == ch {
			g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
			if g.n <= 0 && len(g.waiters) == 0 {
				delete(gates, c.key)
			}
			callersMu.Unlock()
			return nil, why
		}
	}
	callersMu.Unlock()
	// handed a slot as it gave up
	if why == limitBusy {
		return release(), admitted // the slot is its: use it
	}
	releaseCaller(c.key) // gone: pass the slot on
	return nil, why
}

// releaseCaller ends a call in flight and hands free slots to the waiters.
func releaseCaller(k callerKey) {
	callersMu.Lock()
	defer callersMu.Unlock()
	g := gates[k]
	if g == nil {
		return
	}
	g.n--
	pumpLocked(g)
	if g.n <= 0 && len(g.waiters) == 0 {
		delete(gates, k)
	}
}

// pumpLocked admits waiters while the limit (as last seen) allows.
func pumpLocked(g *gate) {
	for len(g.waiters) > 0 && (limitNow <= 0 || g.n < limitNow) {
		g.n++
		close(g.waiters[0])
		g.waiters = g.waiters[1:]
	}
}

// setPartitionLimit applies a changed limit to calls already waiting.
func setPartitionLimit(limit int) {
	callersMu.Lock()
	defer callersMu.Unlock()
	limitNow = limit
	for _, g := range gates {
		pumpLocked(g)
	}
}

// writeLimitBusy answers a call the limit turned away.
func writeLimitBusy(w http.ResponseWriter, limit int) {
	w.Header().Set("Retry-After", limitRetryAfter)
	xbin.WriteError(w, http.StatusTooManyRequests, "this partition has as many calls in flight as the gateway's fairness limit allows ("+
		strconv.Itoa(limit)+"): try again shortly")
}

// partitionLimitIn is a PUT /config body's partitionLimit, checked: nil
// when absent, else the limit or why it can't be set.
func partitionLimitIn(r *http.Request, raw json.RawMessage) (*int, string, int) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil, "", 0
	}
	if !xbin.Caller(r).UserCanWrite() {
		return nil, "the fairness limit needs write access to this tile", http.StatusForbidden
	}
	var n int
	if json.Unmarshal(raw, &n) != nil || n < 0 || n > maxPartitionLimit {
		return nil, "partitionLimit is a whole number from 0 (off) to 64", http.StatusBadRequest
	}
	return &n, "", 0
}
