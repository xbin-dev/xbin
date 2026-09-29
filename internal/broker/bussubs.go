package broker

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// Bus push subscriptions (D85). A backend can't hold a /ws/events socket
// through idle reaping, so it asks xbind to POST matching bus events to one of
// its own endpoints instead. Like cron (cron.go) a subscription is
// self-targeted — a component subscribes only itself; admins may name one —
// and delivery rides the proxy as From: xbin/bus with the role chosen at
// registration, starting an idle backend lazily. Unlike cron the subscriber
// must hold `reader` on the bus: checked when it subscribes and again at
// every delivery, so a revoked grant stops delivery at once.
//
// Delivery is at-most-once, like the bus itself: one FIFO per subscription
// (busSubQueue slots, the newest dropped when full), one POST in flight, a
// timeout, no retries, and a loop guard — past busSubPerSec events in one
// second the rest are dropped, so a handler publishing to the bus it
// consumes can't run away. Events come straight from publish (never through
// the hub's per-subscriber buffer). Stored in data/bus-subscriptions.json;
// a component's backup carries its own.

// BusPrincipal is the From identity of bus deliveries.
const BusPrincipal = "xbin/bus"

const (
	busSubQueue    = 256
	busSubTimeout  = 2 * time.Minute
	busSubPerSec   = 100
	busSubsPerComp = 64
)

var busSubNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type busSub struct {
	Name      string `json:"name"`
	Resource  string `json:"resource"`         // res:<scope>/<name>, type bus
	Prefix    string `json:"prefix,omitempty"` // topic prefix under the resource ("" = every topic)
	Component string `json:"component"`        // the subscriber, and the target
	Path      string `json:"path"`             // its endpoint, e.g. /bus
	Role      string `json:"role"`             // the role deliveries carry
}

// busDelivery is the body POSTed to a subscriber.
type busDelivery struct {
	ID           string `json:"id"` // the event's: the same for every subscription it reaches
	Subscription string `json:"subscription"`
	Resource     string `json:"resource"`
	Topic        string `json:"topic"` // under the resource
	Data         any    `json:"data,omitempty"`
	TS           int64  `json:"ts"` // unix ms, when it was published
}

// busSubStats are a subscription's counters since the daemon started.
type busSubStats struct {
	Delivered int64  `json:"delivered"`
	Dropped   int64  `json:"dropped"` // queue full, loop guard, component not enabled
	Failed    int64  `json:"failed"`  // the POST failed or answered ≥400, or the grant is gone
	LastError string `json:"lastError,omitempty"`
	LastAt    int64  `json:"lastAt,omitempty"` // unix ms of the last attempt
	// DormantEvents counts events that reached no delivery because the
	// subscription's deployment isn't in the active set: its deliveries are
	// switched off (dormant.go).
	DormantEvents int64 `json:"dormantEvents,omitempty"`
}

type busSubState struct {
	sub   busSub
	owner string // the deployment it belongs to: "" for main, whose live in today's store
	stats busSubStats
	queue []busDelivery
	busy  bool // a drain goroutine owns the queue
	gone  bool // unsubscribed: the drain stops
	win   time.Time
	winN  int
}

// BusDispatch delivers one body to comp's path as principal p (installed by
// main as a call into the proxy — DispatchBodyViaProxy).
type BusDispatch func(ctx context.Context, p auth.Principal, comp, path string, body []byte) (int, string)

type busSubs struct {
	b        *Broker
	mu       sync.Mutex
	subs     map[string]*busSubState // component \x00 name
	dispatch BusDispatch
	// The subscriptions of deployments beyond main, kept in their own files
	// (dormant.go), keyed by depKey, and the lock that serializes rewrites
	// of those files.
	dep    map[string]*busSubState
	fileMu sync.Mutex
}

func newBusSubs(b *Broker) *busSubs {
	bs := &busSubs{b: b, subs: map[string]*busSubState{}, dep: map[string]*busSubState{}}
	if bts, err := os.ReadFile(bs.storePath()); err == nil {
		var list []busSub
		if json.Unmarshal(bts, &list) == nil {
			for _, s := range list {
				bs.subs[subKey(s.Component, s.Name)] = &busSubState{sub: s}
			}
		}
	}
	return bs
}

// SetBusDispatch installs the delivery path (from main, backed by the proxy).
// Events published before it is set reach no subscription. It then loads the
// subscriptions of every deployment beyond main (dormant.go): boot calls it
// after installing the deployments plane's answers.
func (b *Broker) SetBusDispatch(fn BusDispatch) {
	b.bus.mu.Lock()
	b.bus.dispatch = fn
	b.bus.mu.Unlock()
	b.bus.loadDeps()
}

// DispatchBodyViaProxy adapts the proxy into BusDispatch: a POST with a JSON
// body, bounded by ctx. Only the first 4 KiB of the answer are kept.
func DispatchBodyViaProxy(h http.Handler) BusDispatch {
	return func(ctx context.Context, p auth.Principal, comp, path string, body []byte) (int, string) {
		req, _ := http.NewRequestWithContext(auth.WithPrincipal(ctx, p), "POST", "/api/"+comp+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := &capRecorder{h: http.Header{}, code: 200}
		h.ServeHTTP(rec, req)
		return rec.code, rec.body.String()
	}
}

type capRecorder struct {
	h     http.Header
	code  int
	wrote bool
	body  bytes.Buffer
}

func (c *capRecorder) Header() http.Header { return c.h }
func (c *capRecorder) WriteHeader(code int) {
	if !c.wrote {
		c.code, c.wrote = code, true
	}
}
func (c *capRecorder) Write(p []byte) (int, error) {
	c.WriteHeader(200)
	if room := 4096 - c.body.Len(); room > 0 {
		c.body.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func subKey(comp, name string) string { return comp + "\x00" + name }

func (bs *busSubs) storePath() string {
	return filepath.Join(bs.b.Reg.Root, "data", "bus-subscriptions.json")
}

// persist writes the set; callers hold no lock.
func (bs *busSubs) persist() {
	bs.mu.Lock()
	list := make([]busSub, 0, len(bs.subs))
	for _, s := range bs.subs {
		list = append(list, s.sub)
	}
	bs.mu.Unlock()
	sort.Slice(list, func(i, k int) bool {
		if list[i].Component != list[k].Component {
			return list[i].Component < list[k].Component
		}
		return list[i].Name < list[k].Name
	})
	bts, _ := json.MarshalIndent(list, "", "  ")
	if err := fsutil.WriteFileAtomicIn(bs.storePath(), bts, 0o644); err != nil {
		slog.Warn("bus subscriptions: persist", "err", err)
	}
}

// put adds or replaces a subscription (validated by the caller). Replacing
// keeps the counters and whatever is queued.
func (bs *busSubs) put(s busSub) error {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	key := subKey(s.Component, s.Name)
	if st, ok := bs.subs[key]; ok {
		st.sub = s
		return nil
	}
	n := 0
	for _, st := range bs.subs {
		if st.sub.Component == s.Component {
			n++
		}
	}
	if n >= busSubsPerComp {
		return fmt.Errorf("%s already has %d bus subscriptions (the limit) — delete one first", s.Component, n)
	}
	bs.subs[key] = &busSubState{sub: s}
	return nil
}

func (bs *busSubs) remove(comp, name string) bool {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	st, ok := bs.subs[subKey(comp, name)]
	if ok {
		st.gone, st.queue = true, nil
		delete(bs.subs, subKey(comp, name))
	}
	return ok
}

// forget drops every subscription of path (or a path under it) — the
// leftovers a new tile at a removed tile's path must not inherit.
func (bs *busSubs) forget(path string) int {
	bs.mu.Lock()
	n := 0
	for key, st := range bs.subs {
		if c := st.sub.Component; c == path || strings.HasPrefix(c, path+"/") {
			st.gone, st.queue = true, nil
			delete(bs.subs, key)
			n++
		}
	}
	bs.mu.Unlock()
	if n > 0 {
		bs.persist()
	}
	return n
}

func (bs *busSubs) forComponent(comp string) []busSub {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	var out []busSub
	for _, st := range bs.subs {
		if st.sub.Component == comp {
			out = append(out, st.sub)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Name < out[k].Name })
	return out
}

// countFor is how many subscriptions consume the bus resource id.
func (bs *busSubs) countFor(resource string) int {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	n := 0
	for _, st := range bs.subs {
		if st.sub.Resource == resource {
			n++
		}
	}
	return n
}

func newBusEventID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// publish queues one bus event for every matching subscription. It never
// blocks on delivery. The event is in the resource's primary namespace.
func (bs *busSubs) publish(resource, topic string, data any) { bs.publishIn(resource, "", topic, data) }

// publishIn is publish for an event in deployment ns's data namespace ("" is
// the resource's primary's): it reaches only the subscriptions whose
// addressed namespace that is, and queues nothing for those whose deployment
// isn't in the active set, counting the event as dormant (dormant.go).
func (bs *busSubs) publishIn(resource, ns, topic string, data any) {
	now := time.Now()
	var ev *busDelivery
	where := bs.b.busNamespace(resource, ns)
	bs.mu.Lock()
	defer bs.mu.Unlock()
	for st := range bs.all() {
		if st.sub.Resource != resource || !strings.HasPrefix(topic, st.sub.Prefix) || !where.reaches(st) {
			continue
		}
		if !bs.b.firing(st.sub.Component, st.owner) {
			st.stats.DormantEvents++
			continue
		}
		if ev == nil {
			ev = &busDelivery{ID: newBusEventID(), Resource: resource, Topic: topic, Data: data, TS: now.UnixMilli()}
		}
		if now.Sub(st.win) >= time.Second {
			st.win, st.winN = now, 0
		}
		st.winN++
		if st.winN > busSubPerSec || len(st.queue) >= busSubQueue {
			st.stats.Dropped++
			continue
		}
		d := *ev
		d.Subscription = st.sub.Name
		st.queue = append(st.queue, d)
		if !st.busy {
			st.busy = true
			go bs.drain(st)
		}
	}
}

// all ranges over main's subscriptions and every other deployment's; the
// caller holds bs.mu.
func (bs *busSubs) all() iter.Seq[*busSubState] {
	return func(yield func(*busSubState) bool) {
		for _, set := range [2]map[string]*busSubState{bs.subs, bs.dep} {
			for _, st := range set {
				if !yield(st) {
					return
				}
			}
		}
	}
}

// busNS is where one published event is (08-data §4.3; 09-fabric §5.10):
// the resource's scope, its primary namespace P(R) (the scope root's
// primary; main for a plain directory or the workspace level), and the
// namespace the event is in.
type busNS struct {
	b       *Broker
	scope   string
	primary string
	ns      string
}

// busNamespace places an event on resource in deployment ns's namespace;
// "" is the resource's primary namespace, what an unqualified publish is.
func (b *Broker) busNamespace(resource, ns string) busNS {
	n := busNS{b: b, primary: util.MainDeployment}
	if rt, _, ok := b.parseRes(resource); ok && rt.Scope != "" {
		n.scope, n.primary = rt.Scope, b.primaryOf(rt.Scope)
	}
	n.ns = cmp.Or(ns, n.primary)
	return n
}

// reaches reports whether the event is in st's addressed namespace for the
// resource: an own-scope subscription reads its owner's namespace, any other
// the scope's primary's. In the zero state both are main.
func (n busNS) reaches(st *busSubState) bool {
	want := n.primary
	if n.scope != "" {
		if c, ok := n.b.Reg.Component(st.sub.Component); ok && c.Scope == n.scope {
			want = cmp.Or(st.owner, util.MainDeployment)
		}
	}
	return want == n.ns
}

// drain delivers st's queue in order, one at a time, and exits when it is
// empty (the next publish starts another).
func (bs *busSubs) drain(st *busSubState) {
	for {
		bs.mu.Lock()
		if st.gone || len(st.queue) == 0 {
			st.busy = false
			bs.mu.Unlock()
			return
		}
		d := st.queue[0]
		st.queue = st.queue[1:]
		sub, dispatch := st.sub, bs.dispatch
		bs.mu.Unlock()

		outcome, errText := bs.deliver(dispatch, st.owner, sub, d)

		bs.mu.Lock()
		st.stats.LastAt = time.Now().UnixMilli()
		switch outcome {
		case "ok":
			st.stats.Delivered++
		case "dropped":
			st.stats.Dropped++
		case "failed":
			st.stats.Failed++
			st.stats.LastError = errText
		case "dormant":
			st.stats.DormantEvents++
		}
		bs.mu.Unlock()
		if outcome == "gone" && st.owner != "" {
			bs.b.pruneDepSub(sub.Component, st.owner, sub.Name)
		} else if outcome == "gone" && bs.remove(sub.Component, sub.Name) {
			slog.Info("bus subscription pruned: its component is gone", "component", sub.Component, "name", sub.Name)
			bs.persist()
		}
	}
}

// deliver POSTs one queued event to sub, which belongs to deployment owner
// ("" is main), re-checking at delivery that owner's registrations are
// active and that it can still read the bus (D127h).
func (bs *busSubs) deliver(dispatch BusDispatch, owner string, sub busSub, d busDelivery) (outcome, errText string) {
	b := bs.b
	if _, ok := b.Reg.Component(sub.Component); !ok {
		return "gone", ""
	}
	// A disabled tile, or a primary its partition mode pauses (pending or
	// invalid, plans/partitions/01 §2.3), drops the event quietly.
	if b.Reg.LifecycleState(sub.Component) != registry.StateEnabled || dispatch == nil || b.partitionPaused(sub.Component, owner) {
		return "dropped", ""
	}
	if !b.firing(sub.Component, owner) {
		return "dormant", ""
	}
	if err := b.busOwnerMayRead(sub.Component, owner, sub.Resource); err != nil {
		return "failed", err.Error()
	}
	body, err := json.Marshal(d)
	if err != nil {
		return "failed", err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), busSubTimeout)
	defer cancel()
	p := auth.Principal{Component: BusPrincipal, Via: "bus", Role: sub.Role, Deployment: owner}
	code, resp := dispatch(ctx, p, sub.Component, sub.Path, body)
	if code >= 400 {
		slog.Warn("bus delivery failed", "component", sub.Component, "subscription", sub.Name,
			"status", code, "body", firstLine(resp))
		return "failed", fmt.Sprintf("%d %s", code, firstLine(resp))
	}
	return "ok", ""
}

// --- API ---------------------------------------------------------------

// busSubView is a row of GET /bus/subscriptions: today's row, plus the
// deployment by the name rule and whether it is dormant (11-contract §8),
// both absent for a zero-state tile.
type busSubView struct {
	busSub
	busSubStats
	Deployment string `json:"deployment,omitempty"`
	Dormant    bool   `json:"dormant,omitempty"`
}

func (b *Broker) apiBusSubsList(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	admin := b.IsAdmin(p)
	// the deployment listed: a tile principal's own, else ?deployment= or main
	dep, code, err := b.listDeployment(r, p)
	if err != nil {
		server.WriteError(w, code, err.Error(), "/docs/resources.md")
		return
	}
	keep := func(comp string) bool { return admin || (p.Component != "" && p.Component == comp) }
	out := []busSubView{}
	if dep == util.MainDeployment {
		b.bus.mu.Lock()
		for _, st := range b.bus.subs {
			if keep(st.sub.Component) {
				out = append(out, busSubView{busSub: st.sub, busSubStats: st.stats})
			}
		}
		b.bus.mu.Unlock()
	} else {
		out = append(out, b.bus.depViews(dep, keep)...)
	}
	server.WriteJSON(w, http.StatusOK, b.busList(r, dep, out))
}

func (b *Broker) apiBusSubsPut(w http.ResponseWriter, r *http.Request) {
	const docs = "/docs/resources.md"
	var s busSub
	if err := server.DecodeJSON(r, &s); err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error(), docs)
		return
	}
	p := auth.PrincipalOf(r)
	if !b.IsAdmin(p) {
		s.Component = p.Component // components subscribe only themselves
	}
	if s.Component == "" {
		server.WriteError(w, http.StatusBadRequest, "a subscription belongs to a component — admins name it with \"component\"", docs)
		return
	}
	if !busSubNameRe.MatchString(s.Name) || !strings.HasPrefix(s.Path, "/") {
		server.WriteError(w, http.StatusBadRequest, "need {name: [A-Za-z0-9._-]{1,64}, resource, path: /…, prefix?, role?}", docs)
		return
	}
	if _, ok := b.Reg.Component(s.Component); !ok {
		server.WriteError(w, http.StatusNotFound, "no such component "+s.Component, docs)
		return
	}
	rt, res, ok := b.parseRes(s.Resource)
	if !ok || res == nil || res.Type != "bus" {
		server.WriteError(w, http.StatusNotFound, "no such bus resource (declare one in scope.json)", docs)
		return
	}
	s.Resource = rt.String()
	// the deployment it subscribes for: a tile's own, else ?deployment= or main
	dep, code, err := b.regDeployment(r, p, s.Component)
	if err != nil {
		server.WriteError(w, code, err.Error(), docs)
		return
	}
	if dep != util.MainDeployment {
		b.putDepSub(w, r, dep, s, rt) // its own file (dormant.go)
		return
	}
	// the SUBSCRIBER must be able to read the bus — an admin registering for
	// a component doesn't lend it their access
	if err := b.allowRes(auth.Principal{Component: s.Component}, s.Resource, "reader"); err != nil {
		server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
		return
	}
	if err := b.depEdge(s.Component, dep, s.Resource); err != nil { // main while it isn't the primary
		server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
		return
	}
	if s.Role == "" {
		s.Role = "writer"
	}
	if err := b.bus.put(s); err != nil {
		server.WriteError(w, http.StatusConflict, err.Error(), docs)
		return
	}
	b.bus.persist()
	writeRegOK(w, r, !b.firing(s.Component, dep))
}

func (b *Broker) apiBusSubsDelete(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	comp := r.URL.Query().Get("component")
	if !b.IsAdmin(p) {
		comp = p.Component // only your own
	}
	dep, code, err := b.regDeployment(r, p, comp)
	if err != nil {
		server.WriteError(w, code, err.Error(), "/docs/resources.md")
		return
	}
	if dep != util.MainDeployment {
		b.deleteDepSub(w, r, comp, dep, r.PathValue("name")) // its own file (dormant.go)
		return
	}
	if !b.bus.remove(comp, r.PathValue("name")) {
		server.WriteError(w, http.StatusNotFound, "no such subscription")
		return
	}
	b.bus.persist()
	writeRegOK(w, r, false)
}
