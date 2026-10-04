// outbox.go — the events half's state at global (API.md §7): the hub that
// holds subscriptions and the outbox in memory over state's `sub/` and
// `outbox/` keys, one outbox item per (event, consumer, for), the `tick`
// cron registered while any item waits, and GET /scm/events — the events
// delivered (or due) to a consumer in the last seven days.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const (
	keepEvents   = 7 * 24 * time.Hour // GET /scm/events looks back this far
	retryFor     = 24 * time.Hour     // an item undelivered this long is dropped
	retryFirst   = 10 * time.Second
	retryMax     = time.Hour
	outboxMax    = 10000 // items kept (delivered ones go first)
	checksMerge  = 5 * time.Second
	cronName     = "outbox"
	eventsMaxAge = int64(keepEvents / time.Millisecond)
)

// outItem is state "outbox/<id>": one event for one consumer and for.
type outItem struct {
	ID       string          `json:"id"` // <queued ms, 13 digits><random>: sorts by time
	Consumer string          `json:"consumer"`
	For      string          `json:"for"`
	Person   string          `json:"person,omitempty"` // for user:<id>: the xbin person
	PID      string          `json:"pid,omitempty"`    // their partition id, as the subscription was made
	Repo     string          `json:"repo"`
	Private  bool            `json:"private"`
	Event    json.RawMessage `json:"event"` // the event v1 as it is POSTed
	State    string          `json:"state"` // pending | delivered
	Attempts int             `json:"attempts"`
	QueuedAt int64           `json:"queuedAt"`
	NextAt   int64           `json:"nextAt"`
	DoneAt   int64           `json:"doneAt,omitempty"`
	Last     string          `json:"last,omitempty"`   // the last attempt's outcome (a status, "unbound", …)
	Enrich   string          `json:"enrich,omitempty"` // "suite:<id>": the suite's runs are fetched before the first attempt
}

// agentEndpoint is one binding of the `agents` slot (XBIN_IFACE_AGENTS).
type agentEndpoint struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
	Personal bool   `json:"personal,omitempty"`
}

// evCounts are what the events half has counted since it started.
type evCounts struct {
	Received   int64 `json:"received"`
	Duplicates int64 `json:"duplicates"`
	Foreign    int64 `json:"foreign"`    // dropped: an account outside allowedAccounts
	BadSig     int64 `json:"badSig"`     // refused: a signature that didn't match
	Queued     int64 `json:"queued"`     // outbox items written
	Delivered  int64 `json:"delivered"`  // a consumer answered 200
	NotFound   int64 `json:"notFound"`   // a consumer answered 404: dropped
	Expired    int64 `json:"expired"`    // undelivered for 24 h: dropped
	AccessLost int64 `json:"accessLost"` // a person who can no longer read a private repo: dropped
	PersonGone int64 `json:"personGone"` // a person signed out (or re-created under the same id): dropped
	Overflow   int64 `json:"overflow"`   // the outbox was full: dropped
}

// hub is one instance's events state (global and legacy).
type hub struct {
	s  *srv
	mu sync.Mutex

	loaded bool
	subs   map[string]*subscription
	out    map[string]*outItem
	counts evCounts
	health healthRec
	saved  healthRec                // as last written
	window map[string]*checksWindow // repo|sha → a checks.completed still held to merge

	// What the tests swap: the agents binding, the POST to a consumer, the
	// cron registration. bg runs the delivery loop in the background.
	agents func() []agentEndpoint
	post   func(ctx context.Context, url string, body []byte) (int, error)
	cron   func(on bool) error
	bg     bool

	cronMu    sync.Mutex
	cronState int // 0 unknown, 1 on, 2 off
	wake      chan struct{}
	deliverMu sync.Mutex
	seenAdds  map[string]int  // installation → seen keys written since the last prune
	inflight  map[string]bool // deliveries being taken now (seen keys)
	lastPrune int64
}

var hubs sync.Map // *srv → *hub

// What a new hub reaches: the `agents` binding, a consumer, xbind's cron
// (the tests swap them).
var (
	defaultAgents = boundAgents
	defaultPost   = postEvent
	defaultCron   = cronSet
)

// ev is this instance's hub (made on first use).
func (s *srv) ev() *hub {
	if h, ok := hubs.Load(s); ok {
		return h.(*hub)
	}
	h := &hub{s: s, subs: map[string]*subscription{}, out: map[string]*outItem{}, window: map[string]*checksWindow{},
		agents: defaultAgents, post: defaultPost, cron: defaultCron, wake: make(chan struct{}, 1), seenAdds: map[string]int{}, inflight: map[string]bool{}}
	got, _ := hubs.LoadOrStore(s, h)
	return got.(*hub)
}

// load reads the subscriptions and the outbox from state once. h.mu held.
func (h *hub) load() {
	if h.loaded {
		return
	}
	h.loaded = true
	s := h.s
	if keys, err := s.state.List("sub/"); err == nil {
		for _, k := range keys {
			var sub subscription
			if s.state.Get(k, &sub) == nil && sub.ID != "" {
				h.subs[sub.ID] = &sub
			}
		}
	}
	if keys, err := s.state.List("outbox/"); err == nil {
		for _, k := range keys {
			var it outItem
			if s.state.Get(k, &it) == nil && it.ID != "" {
				h.out[it.ID] = &it
			}
		}
	}
	_ = s.state.Get("events-health", &h.health)
	h.saved = h.health
}

// startEvents resumes the events half at start (global and legacy): the
// outbox's cron as the outbox needs it, the delivery loop, and a catch-up
// of deliveries GitHub couldn't make while this tile was down.
func (s *srv) startEvents(bg bool) {
	if s.mode == modeUser {
		return
	}
	h := s.ev()
	h.mu.Lock()
	h.bg = bg
	h.load()
	h.mu.Unlock()
	h.syncCron()
	if bg {
		go h.loop()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if n, err := s.catchUp(ctx); err != nil {
				log.Printf("events: catch-up: %v", err)
			} else if n > 0 {
				log.Printf("events: asked GitHub to redeliver %d deliveries", n)
			}
		}()
	}
}

// itemSeq orders the items queued within one millisecond.
var itemSeq atomic.Uint64

// newItemID sorts by the time it was queued.
func newItemID(now int64) string {
	return fmt.Sprintf("%013d%08d%s", now, itemSeq.Add(1)%1e8, randomID(4))
}

// putItem keeps an item (memory and state). h.mu held.
func (h *hub) putItem(it *outItem) error {
	if err := h.s.state.Put("outbox/"+it.ID, it); err != nil {
		return err
	}
	h.out[it.ID] = it
	return nil
}

// dropItem forgets an item. h.mu held.
func (h *hub) dropItem(id string) {
	delete(h.out, id)
	_ = h.s.state.Delete("outbox/" + id)
}

// pending says whether any item waits for delivery. h.mu held.
func (h *hub) pending() bool {
	for _, it := range h.out {
		if it.State == "pending" {
			return true
		}
	}
	return false
}

// room makes room for n more items: delivered ones go first, oldest first;
// false when only pending ones are left. h.mu held.
func (h *hub) room(n int) bool {
	if len(h.out)+n <= outboxMax {
		return true
	}
	ids := make([]string, 0, len(h.out))
	for id, it := range h.out {
		if it.State != "pending" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if len(h.out)+n <= outboxMax {
			break
		}
		h.dropItem(id)
	}
	return len(h.out)+n <= outboxMax
}

// prune drops what is past keeping: delivered items after seven days,
// pending ones after a day of retries, lapsed subscriptions. h.mu held.
func (h *hub) prune(now int64) {
	for id, it := range h.out {
		switch {
		case it.State == "pending" && now-it.QueuedAt > retryFor.Milliseconds():
			h.counts.Expired++
			h.dropItem(id)
		case it.State != "pending" && now-it.QueuedAt > eventsMaxAge:
			h.dropItem(id)
		}
	}
	for id, sub := range h.subs {
		if sub.Expires < now {
			h.dropSub(id)
		}
	}
	for k, w := range h.window {
		if now-w.at > checksMerge.Milliseconds() {
			delete(h.window, k)
		}
	}
}

// kick wakes the delivery loop.
func (h *hub) kick() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// loop delivers what is due as it falls due (production; the cron's tick
// is the fallback after a restart).
func (h *hub) loop() {
	for {
		h.deliverDue(context.Background())
		h.mu.Lock()
		next := int64(0)
		for _, it := range h.out {
			if it.State == "pending" && (next == 0 || it.NextAt < next) {
				next = it.NextAt
			}
		}
		h.mu.Unlock()
		wait := time.Minute
		if next > 0 {
			wait = max(time.Duration(next-h.s.now().UnixMilli())*time.Millisecond, 200*time.Millisecond)
		}
		select {
		case <-h.wake:
		case <-time.After(min(wait, time.Minute)):
		}
	}
}

// syncCron registers the `tick` cron while the outbox holds pending items
// and removes it when it doesn't (each change once).
func (h *hub) syncCron() {
	h.mu.Lock()
	want := 2
	if h.pending() {
		want = 1
	}
	h.mu.Unlock()
	h.cronMu.Lock()
	defer h.cronMu.Unlock()
	if h.cronState == want || h.cron == nil {
		return
	}
	if err := h.cron(want == 1); err != nil {
		log.Printf("events: cron %s: %v", map[int]string{1: "register", 2: "remove"}[want], err)
		return
	}
	h.cronState = want
}

// eventsTick is the `tick` cron's work: prune, deliver what is due, and
// the cron itself as the outbox needs it.
func eventsTick(ctx context.Context, s *srv) {
	if s.mode == modeUser {
		return
	}
	h := s.ev()
	h.mu.Lock()
	h.load()
	h.prune(s.now().UnixMilli())
	h.mu.Unlock()
	h.deliverDue(ctx)
	s.pruneSeen(false)
	h.syncCron()
}

// --- production wiring -------------------------------------------------------------

// boundAgents is the `agents` slot's bindings (a multi slot: JSON).
func boundAgents() []agentEndpoint {
	var out []agentEndpoint
	_ = json.Unmarshal([]byte(os.Getenv("XBIN_IFACE_AGENTS")), &out)
	return out
}

// postEvent POSTs an event to a consumer through xbind.
func postEvent(ctx context.Context, url string, body []byte) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	return resp.StatusCode, nil
}

// cronSet registers (or removes) the `tick` cron job: every minute, POST
// /tick as this tile.
func cronSet(on bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	method, u, body := http.MethodDelete, "http://xbin/api/xbin/cron/jobs/"+cronName, []byte(nil)
	if on {
		res := xbin.Resource("tick")
		if res == "" {
			return fmt.Errorf("no tick resource (grant res:<self>/tick writer)")
		}
		method, u = http.MethodPut, "http://xbin/api/xbin/cron/jobs"
		body, _ = json.Marshal(map[string]string{"name": cronName, "resource": res, "schedule": "@every 1m", "path": "/tick", "role": "admin"})
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 300 && !(resp.StatusCode == http.StatusNotFound && !on) {
		return fmt.Errorf("xbind answered %d", resp.StatusCode)
	}
	return nil
}

// --- GET /scm/events ------------------------------------------------------------------

// handleEvents is a consumer's catch-up: at global (or legacy) a tile's
// own; in a person's partition, relayed to global for the person.
func (s *srv) handleEvents(w http.ResponseWriter, r *http.Request, c who) {
	q := r.URL.Query()
	switch {
	case s.mode == modeUser:
		var out page[json.RawMessage]
		v := url.Values{"consumer": {c.c.From}}
		for _, k := range []string{"since", "repo", "limit", "cursor"} {
			if x := q.Get(k); x != "" {
				v.Set(k, x)
			}
		}
		if err := s.relay(r.Context(), http.MethodGet, "/partition/events?"+v.Encode(), nil, &out); err != nil {
			fail(w, err)
			return
		}
		writeGET(w, r, out)
	case c.cls == clsTile:
		s.answerEvents(w, r, c.c.From, "global", "")
	default:
		fail(w, personAtUnpartitioned())
	}
}

// handleRelayEvents is GET /scm/events for a person's partition (relay):
// the person's own events for the consumer it names.
func (s *srv) handleRelayEvents(w http.ResponseWriter, r *http.Request, c who) {
	s.relayStart(c)
	s.answerEvents(w, r, r.URL.Query().Get("consumer"), "user:"+c.person, c.pid)
}

// answerEvents lists one consumer's events for one for: oldest first,
// from since (unix ms), a repo's only when asked.
func (s *srv) answerEvents(w http.ResponseWriter, r *http.Request, consumer, forWhom, pid string) {
	q := r.URL.Query()
	limit, err := strconv.Atoi(or(q.Get("limit"), strconv.Itoa(pageDefault)))
	if err != nil || limit < 1 || limit > pageMax {
		fail(w, refuse(refInvalid, "limit is 1 to %d", pageMax))
		return
	}
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	repo := q.Get("repo")
	after := ""
	if cur := q.Get("cursor"); cur != "" {
		b, err := base64.RawURLEncoding.DecodeString(cur)
		if err != nil {
			fail(w, refuse(refInvalid, "cursor isn't one this provider gave"))
			return
		}
		after = string(b)
	}
	now := s.now().UnixMilli()
	h := s.ev()
	h.mu.Lock()
	h.load()
	var its []*outItem
	for _, it := range h.out {
		if it.Consumer != consumer || it.For != forWhom || (pid != "" && it.PID != pid) || now-it.QueuedAt > eventsMaxAge ||
			it.QueuedAt < since || it.ID <= after || (repo != "" && !strings.EqualFold(repo, it.Repo)) {
			continue
		}
		its = append(its, it)
	}
	sort.Slice(its, func(i, k int) bool { return its[i].ID < its[k].ID })
	out := page[json.RawMessage]{Items: []json.RawMessage{}}
	for i, it := range its {
		if i == limit {
			out.Next = base64.RawURLEncoding.EncodeToString([]byte(its[i-1].ID))
			break
		}
		out.Items = append(out.Items, it.Event)
	}
	h.mu.Unlock()
	writeGET(w, r, out)
}
