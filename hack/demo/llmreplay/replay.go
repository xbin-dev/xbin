package main

// Replay: which recorded exchange answers a live request, and playing it
// back on the recorded clock.
//
// A take is matched lane by lane. Within a lane, a model call ("sequenced":
// chat/completions, responses, messages) takes an unused recorded exchange
// of the same kind (wire, model, stream flag):
//
//  1. exact — the earliest one whose body hashes the same (volatile
//     fields aside); this holds whenever nothing in the request changed;
//  2. thread — else the next one of its conversation: requests that share a
//     thread key (the system prompt's start and the first user message,
//     normalized) are one conversation, and the Nth request of a live
//     conversation gets its recorded conversation's Nth response. With a
//     single conversation this is "the Nth request of the lane gets the Nth
//     response"; with an agent's side calls and parallel subagents it keeps
//     each conversation in step however their requests interleave;
//  3. nearest — else the most similar of the unused ones up to -window past
//     the furthest exchange this take reached; the live conversation then
//     follows the recorded one it matched (a retyped first prompt doesn't
//     lose the thread).
//
// Every match short of exact is compared with the live request (normalized:
// ids, dates, durations masked): below -drift similarity — of the whole
// request or of what is new in it since the model last spoke — or with a
// different number of messages, it is logged as DRIFT with where the two
// part. The recorded response is served anyway: a retake carries on, and
// the log says which shot no longer matches its recording.
//
// Everything else (GET /v1/models, embeddings, count_tokens) is a lookup:
// answered by the same request recorded (exact hash), else the most similar
// one on the same path, any number of times, without touching the sequence.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type entry struct {
	ex           *Exchange
	d            *digest
	pos          int // its place among its lane's model calls, from 0 (Seq counts lookups too)
	thread, step int // the thread's number in its lane and this call's place in it, from 1
	used         bool
	whole, tail  shingles // lazily (sets)
}

func (e *entry) sets() (shingles, shingles) {
	if e.whole == nil {
		w, t := e.d.texts()
		e.whole, e.tail = shingleSet(w...), shingleSet(t...)
	}
	return e.whole, e.tail
}

type laneState struct {
	name     string
	entries  []*entry            // model calls, by Seq
	threads  map[string][]*entry // by thread key, by Seq
	binds    map[string]string   // a live thread key → the recorded one it follows
	live     int                 // requests seen this take
	frontier int                 // the furthest entry served this take, by pos (-1: none)
	misses   int
	drifts   []string
}

type player struct {
	opt     options
	created time.Time

	mu      sync.Mutex
	lanes   map[string]*laneState
	lookups []*entry // everything else, in file order
	skipped int      // transient failures left out (-keep-errors keeps them)
}

func newPlayer(c *cassette, opt options) *player {
	p := &player{opt: opt, created: c.Header.Created, lanes: map[string]*laneState{}}
	// a long coding session resends its whole context on every call: digest
	// the calls on every core
	digests := make([]*digest, len(c.Exchanges))
	next := make(chan int)
	var wg sync.WaitGroup
	for range max(1, min(runtime.GOMAXPROCS(0), len(c.Exchanges))) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				ex := c.Exchanges[i]
				digests[i] = describe(ex.Method, ex.Path, ex.Query, ex.reqBody())
			}
		}()
	}
	for i := range c.Exchanges {
		next <- i
	}
	close(next)
	wg.Wait()
	for i, ex := range c.Exchanges {
		e := &entry{ex: ex, d: digests[i]}
		if !e.d.sequenced() {
			p.lookups = append(p.lookups, e)
			continue
		}
		if ex.transient() && !opt.keepErrors {
			p.skipped++
			continue
		}
		l := p.lanes[ex.Lane]
		if l == nil {
			l = &laneState{name: ex.Lane, threads: map[string][]*entry{}, binds: map[string]string{}, frontier: -1}
			p.lanes[ex.Lane] = l
		}
		if len(l.threads[e.d.root]) == 0 {
			e.thread = len(l.threads) + 1
		} else {
			e.thread = l.threads[e.d.root][0].thread
		}
		l.threads[e.d.root] = append(l.threads[e.d.root], e)
		e.step = len(l.threads[e.d.root])
		e.pos = len(l.entries)
		l.entries = append(l.entries, e)
	}
	return p
}

// match is the exchange chosen for a live request, and how well it fits.
type match struct {
	e           *entry
	how         string // exact | thread | nearest
	n           int    // the live request's number in its lane this take, from 1
	whole, tail float64
	drift       string // why it isn't a clean match ("" = it is)
}

// next picks the recorded exchange for a live model call (the package
// comment has the rules), marking it used; nil and why when nothing fits.
func (p *player) next(lane string, q *digest) (*match, string) {
	var qWhole, qTail shingles // the live request's sets, made only when a match needs them
	qSets := func() (shingles, shingles) {
		if qWhole == nil {
			w, t := q.texts()
			qWhole, qTail = shingleSet(w...), shingleSet(t...)
		}
		return qWhole, qTail
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.lanes[lane]
	if l == nil {
		return nil, fmt.Sprintf("lane %q has no recorded model calls (recorded lanes: %s)", lane, strings.Join(sortedKeys(p.lanes), ", "))
	}
	l.live++
	m := &match{n: l.live}
	kind := q.kind()
	for _, e := range l.entries {
		if !e.used && e.d.kind() == kind && e.d.hash == q.hash {
			m.e, m.how = e, "exact"
			break
		}
	}
	if m.e == nil {
		root := q.root
		if b, ok := l.binds[root]; ok {
			root = b
		}
		for _, e := range l.threads[root] {
			if !e.used && e.d.kind() == kind {
				m.e, m.how = e, "thread"
				break
			}
		}
	}
	if m.e == nil {
		best := -2.0
		for _, e := range l.entries {
			if e.pos > l.frontier+p.opt.window {
				break
			}
			if e.used || e.d.kind() != kind {
				continue
			}
			w, t := qSets()
			if s, _, _ := similarity(w, t, len(q.msgs), e); s > best {
				best, m.e, m.how = s, e, "nearest"
			}
		}
	}
	if m.e == nil {
		l.misses++
		left := 0
		for _, e := range l.entries {
			if !e.used {
				left++
			}
		}
		return nil, fmt.Sprintf("no unused %s call within reach in lane %s (%d recorded, %d unused)", kind, lane, len(l.entries), left)
	}
	e := m.e
	e.used = true
	l.frontier = max(l.frontier, e.pos)
	if _, recorded := l.threads[q.root]; !recorded && q.root != e.d.root {
		if _, bound := l.binds[q.root]; !bound {
			l.binds[q.root] = e.d.root
		}
	}
	if m.how == "exact" {
		m.whole, m.tail = 1, 1
		e.whole, e.tail = nil, nil
		return m, ""
	}
	w, t := qSets()
	_, m.whole, m.tail = similarity(w, t, len(q.msgs), e)
	e.whole, e.tail = nil, nil // served: its sets are no candidate's any more (a put-back remakes them)
	if min(m.whole, m.tail) < p.opt.drift || len(q.msgs) != len(e.d.msgs) {
		m.drift = diffNote(e.d, q)
		l.drifts = append(l.drifts, fmt.Sprintf("request %d ← seq %d (%s): similarity %.2f: %s", m.n, e.ex.Seq, m.how, min(m.whole, m.tail), m.drift))
	}
	return m, ""
}

// similarity scores a recorded call against a live one: the mean of the
// whole request's and its new input's set similarity, less a little per
// message the counts differ by.
func similarity(qWhole, qTail shingles, qMsgs int, e *entry) (score, whole, tail float64) {
	ew, et := e.sets()
	whole, tail = jaccard(qWhole, ew), jaccard(qTail, et)
	score = (whole + tail) / 2
	if dn := qMsgs - len(e.d.msgs); dn != 0 {
		score -= 0.05 * float64(min(abs(dn), 4))
	}
	return score, whole, tail
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// putBack returns an exchange whose replay its caller cut short (a timeout,
// a retry), so the retry gets it again.
func (p *player) putBack(e *entry) {
	p.mu.Lock()
	e.used = false
	p.mu.Unlock()
}

// lookup finds the recorded answer to a request outside the sequence: the
// same request (the latest such), else the most similar on the same method
// and path; this lane's first, then any lane's.
func (p *player) lookup(lane string, q *digest) *entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	var qWhole, qTail shingles
	for _, sameLane := range []bool{true, false} {
		var best *entry
		bestScore := -2.0
		for i := len(p.lookups) - 1; i >= 0; i-- {
			e := p.lookups[i]
			if (e.ex.Lane == lane) != sameLane || e.ex.Method != q.method || e.ex.Path != q.path {
				continue
			}
			if e.d.hash == q.hash {
				return e
			}
			if qWhole == nil {
				w, t := q.texts()
				qWhole, qTail = shingleSet(w...), shingleSet(t...)
			}
			if s, _, _ := similarity(qWhole, qTail, len(q.msgs), e); s > bestScore {
				best, bestScore = e, s
			}
		}
		if best != nil {
			return best
		}
	}
	return nil
}

// modelList is a model listing made up from the models a lane's recorded
// calls used (every lane's when it has none) — for a GET /v1/models the
// recording never saw.
func (p *player) modelList(lane string, anthropic bool) []byte {
	p.mu.Lock()
	seen := map[string]bool{}
	for _, l := range p.lanes {
		if l.name != lane && p.lanes[lane] != nil {
			continue
		}
		for _, e := range l.entries {
			if e.d.model != "" {
				seen[e.d.model] = true
			}
		}
	}
	p.mu.Unlock()
	models := sortedKeys(seen)
	created := p.created
	if created.IsZero() {
		created = time.Now()
	}
	var b []byte
	if anthropic {
		data := []map[string]any{}
		for _, m := range models {
			data = append(data, map[string]any{"type": "model", "id": m, "display_name": m, "created_at": created.UTC().Format(time.RFC3339)})
		}
		out := map[string]any{"data": data, "has_more": false, "first_id": nil, "last_id": nil}
		if len(models) > 0 {
			out["first_id"], out["last_id"] = models[0], models[len(models)-1]
		}
		b, _ = json.Marshal(out)
	} else {
		data := []map[string]any{}
		for _, m := range models {
			data = append(data, map[string]any{"id": m, "object": "model", "created": created.Unix(), "owned_by": "system"})
		}
		b, _ = json.Marshal(map[string]any{"object": "list", "data": data})
	}
	return b
}

// reset starts a take over: every exchange unused, no thread bound, no
// drift noted — one lane, or all of them ("").
func (p *player) reset(lane string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for name, l := range p.lanes {
		if lane != "" && name != lane {
			continue
		}
		for _, e := range l.entries {
			e.used = false
		}
		l.binds, l.live, l.frontier, l.misses, l.drifts = map[string]string{}, 0, -1, 0, nil
	}
}

func (p *player) status() map[string]laneStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]laneStatus{}
	for name, l := range p.lanes {
		st := laneStatus{Calls: len(l.entries), Misses: l.misses, Drifts: append([]string(nil), l.drifts...)}
		for _, e := range l.entries {
			if e.used {
				st.Served++
			} else {
				st.Left++
				if len(st.Next) < 3 {
					st.Next = append(st.Next, e.ex.Seq)
				}
			}
		}
		out[name] = st
	}
	return out
}

// summary is the startup line's account of what a cassette holds.
func (p *player) summary() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var parts []string
	for _, name := range sortedKeys(p.lanes) {
		l := p.lanes[name]
		parts = append(parts, fmt.Sprintf("%s (%d calls in %d threads)", name, len(l.entries), len(l.threads)))
	}
	s := "lanes " + strings.Join(parts, ", ")
	if len(parts) == 0 {
		s = "no model calls"
	}
	if len(p.lookups) > 0 {
		s += fmt.Sprintf(", %d lookups", len(p.lookups))
	}
	if p.skipped > 0 {
		s += fmt.Sprintf(", %d transient failures left out", p.skipped)
	}
	return s
}

// --- serving -----------------------------------------------------------------

func (s *server) replay(w http.ResponseWriter, r *http.Request, lane, path string, body []byte) {
	arrived := time.Now()
	q := describe(r.Method, path, r.URL.RawQuery, body)
	if !q.sequenced() {
		if e := s.play.lookup(lane, q); e != nil {
			s.logf("look %s: %s ← %s", lane, q.describeCall(), laneRef(e.ex.Lane, e.ex.Seq))
			s.playback(w, r, e.ex, arrived)
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(path, "/models") {
			b := s.play.modelList(lane, anthropicStyle(r, path))
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", strconv.Itoa(len(b)))
			_, _ = w.Write(b)
			s.logf("look %s: %s ← a list of the cassette's models (none recorded)", lane, q.describeCall())
			return
		}
		s.miss(w, r, lane, path, body, q, "nothing recorded for "+r.Method+" "+path)
		return
	}
	m, why := s.play.next(lane, q)
	if m == nil {
		s.miss(w, r, lane, path, body, q, why)
		return
	}
	e := m.e
	where := fmt.Sprintf("seq %d (thread %d step %d, %s)", e.ex.Seq, e.thread, e.step, m.how)
	if m.drift != "" {
		s.logf("DRIFT %s request %d ← %s: similarity %.2f (new input %.2f, whole %.2f): %s",
			lane, m.n, where, min(m.whole, m.tail), m.tail, m.whole, m.drift)
	}
	s.logf("play %s request %d: %s ← %s → %s", lane, m.n, q.describeCall(), where, describeAnswer(e.ex))
	if !s.playback(w, r, e.ex, arrived) && !e.ex.Aborted {
		s.play.putBack(e)
		s.logf("play %s request %d: the caller hung up mid-replay — seq %d is unused again (for its retry)", lane, m.n, e.ex.Seq)
	}
}

func (s *server) miss(w http.ResponseWriter, r *http.Request, lane, path string, body []byte, q *digest, why string) {
	s.logf("MISS %s: %s: %s", lane, q.describeCall(), why)
	if s.opt.onMiss == "passthrough" {
		s.forward(w, r, lane, path, body, false)
		return
	}
	writeAPIError(w, r, path, http.StatusBadRequest, "llmreplay: no recorded response fits this request: "+why)
}

// holdMax bounds how long a replay holds a stream its recording's caller
// hung up on.
const holdMax = 10 * time.Minute

// playback answers with a recorded exchange on the recorded clock (scaled
// by -timing, each pause capped at -max-wait): the headers when they came
// (counted from the request's arrival, so matching time is part of it),
// then each event after its recorded pause, flushed. A call whose caller
// hung up in the recording is held open after its last recorded event, as
// the model was still going then. It reports whether the answer went out
// whole.
func (s *server) playback(w http.ResponseWriter, r *http.Request, ex *Exchange, arrived time.Time) bool {
	ctx := r.Context()
	if !s.sleep(ctx, s.scaled(ex.HeadMs)-time.Since(arrived)) {
		return false
	}
	if ex.Status == 0 { // no response was recorded
		if ex.Aborted {
			s.hold(ctx)
			return true
		}
		writeAPIError(w, r, ex.Path, http.StatusBadGateway, "llmreplay: (recorded) the upstream failed: "+ex.Error)
		return true
	}
	h := w.Header()
	for k, vs := range ex.Headers {
		h[k] = append([]string(nil), vs...)
	}
	if !ex.stream() {
		b := ex.body()
		h.Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(ex.Status)
		if !s.sleep(ctx, s.scaled(ex.BodyMs)) {
			return false
		}
		_, err := w.Write(b)
		return err == nil
	}
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "text/event-stream")
	}
	w.WriteHeader(ex.Status)
	flush(w)
	for _, c := range ex.Chunks {
		if !s.sleep(ctx, s.scaled(c.Ms)) {
			return false
		}
		if _, err := w.Write(c.bytes()); err != nil {
			return false
		}
		flush(w)
	}
	if ex.Aborted {
		s.hold(ctx)
		return true
	}
	return s.sleep(ctx, s.scaled(ex.BodyMs))
}

func (s *server) hold(ctx context.Context) { s.sleep(ctx, holdMax) }

// scaled is a recorded pause on the replay clock.
func (s *server) scaled(msv float64) time.Duration {
	d := time.Duration(msv * s.opt.timing * float64(time.Millisecond))
	if s.opt.maxWait > 0 && d > s.opt.maxWait {
		d = s.opt.maxWait
	}
	return d
}

// laneThreads lists a cassette's lanes for inspect: each lane's entries in
// order, numbered as replay numbers them.
func laneThreads(c *cassette) (map[string][]*entry, []*entry) {
	p := newPlayer(c, options{keepErrors: true})
	out := map[string][]*entry{}
	for name, l := range p.lanes {
		out[name] = l.entries
	}
	sort.SliceStable(p.lookups, func(i, j int) bool { return p.lookups[i].ex.Started.Before(p.lookups[j].ex.Started) })
	return out, p.lookups
}
