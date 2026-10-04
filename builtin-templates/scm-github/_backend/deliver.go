// deliver.go — the outbox's delivery (docs/scm.md §Delivery; API.md §7):
// each due item POSTed to its consumer's /adapter/scm/event through the
// `agents` binding whose tile is the subscription's consumer. A person's
// item is checked first — their identity still the one the subscription
// was made with and, for a private repo, their read access again (lost:
// the item dropped, their subscriptions on the repo deleted). 200 is
// delivered, 404 dropped, anything else retried from 10 s doubling to an
// hour, for a day.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// outcome is what one attempt came to.
type outcome struct {
	done   bool   // delivered
	drop   string // dropped, and why: "404", "access", "person"
	retry  string // retried, and why: a status, "unbound", "error", "check"
	event  json.RawMessage
	enrich bool // the item's suite runs were filled (or given up on)
}

// deliverDue makes one pass over the due items: each consumer's in order,
// up to four consumers at once; a consumer that fails is left for this
// pass, its other due items put back to the same time.
func (h *hub) deliverDue(ctx context.Context) {
	if !h.deliverMu.TryLock() {
		return
	}
	defer h.deliverMu.Unlock()
	now := h.s.now().UnixMilli()
	h.mu.Lock()
	h.load()
	h.prune(now)
	per := map[string][]outItem{}
	var taken []string
	for _, it := range h.out {
		if it.State == "pending" && it.NextAt <= now {
			per[it.Consumer] = append(per[it.Consumer], *it)
			h.sending[it.ID] = true // from here, a checks merge makes a new item instead
			taken = append(taken, it.ID)
		}
	}
	h.mu.Unlock()
	if len(per) == 0 {
		return
	}
	defer func() {
		h.mu.Lock()
		for _, id := range taken {
			delete(h.sending, id)
		}
		h.mu.Unlock()
	}()
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, its := range per {
		sort.Slice(its, func(i, k int) bool { return its[i].ID < its[k].ID })
		wg.Add(1)
		go func(its []outItem) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			for i := range its {
				o := h.attempt(ctx, &its[i])
				next := h.settle(&its[i], o)
				if o.retry != "" && o.retry != "check" {
					h.holdRest(its[i+1:], next)
					return
				}
			}
		}(its)
	}
	wg.Wait()
	h.syncCron()
}

// attempt tries to deliver one item.
func (h *hub) attempt(ctx context.Context, it *outItem) outcome {
	s := h.s
	var o outcome
	if strings.HasPrefix(it.For, "user:") {
		id := s.ident(it.Person)
		if id == nil || id.PID != it.PID {
			return outcome{drop: "person"} // signed out, or another person under the same id
		}
		if it.Private {
			perm, err := s.personRead(ctx, id.Login, it.Repo, false)
			switch {
			case isRefusal(err, refNotFound) || isRefusal(err, refNotInstalled) || (err == nil && perm == "none"):
				return outcome{drop: "access"}
			case err != nil:
				return outcome{retry: "check"}
			}
		}
	}
	if strings.HasPrefix(it.Enrich, "suite:") {
		o.enrich = true
		if ev := s.suiteRuns(ctx, it); ev != nil {
			it.Event, o.event = ev, ev
		}
	}
	u := h.endpoint(it.Consumer)
	if u == "" {
		o.retry = "unbound"
		return o
	}
	st, err := h.post(ctx, strings.TrimRight(u, "/")+"/adapter/scm/event", it.Event)
	switch {
	case err != nil:
		o.retry = "error"
	case st == http.StatusOK:
		o.done = true
	case st == http.StatusNotFound:
		o.drop = "404"
	default:
		o.retry = http.StatusText(st)
		if o.retry == "" {
			o.retry = "status"
		}
	}
	return o
}

// retryDelay is the wait after the n-th failed attempt: 10 s doubling to
// an hour.
func retryDelay(n int) time.Duration {
	d := retryFirst
	for i := 1; i < n && d < retryMax; i++ {
		d *= 2
	}
	return min(d, retryMax)
}

// settle records an attempt's outcome on the item (unless it went
// meanwhile) and answers when it is next due (0: not).
func (h *hub) settle(it *outItem, o outcome) int64 {
	now := h.s.now().UnixMilli()
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.out[it.ID]
	if cur == nil {
		return 0
	}
	if o.enrich {
		cur.Enrich = ""
		if o.event != nil {
			cur.Event = o.event
		}
	}
	switch {
	case o.done:
		cur.State, cur.DoneAt, cur.Last = "delivered", now, "200"
		h.counts.Delivered++
	case o.drop != "":
		switch o.drop {
		case "404":
			h.counts.NotFound++
		case "access":
			h.counts.AccessLost++
			h.dropPersonSubs(cur.Person, cur.Repo)
			for id, x := range h.out { // the person's other items there go too
				if x.Person == cur.Person && x.State == "pending" && strings.EqualFold(x.Repo, cur.Repo) && x.Private {
					h.dropItem(id)
				}
			}
		default:
			h.counts.PersonGone++
		}
		h.dropItem(cur.ID)
		return 0
	default:
		cur.Attempts++
		cur.Last = o.retry
		cur.NextAt = now + retryDelay(cur.Attempts).Milliseconds()
	}
	_ = h.putItem(cur)
	return cur.NextAt
}

// holdRest puts a failing consumer's other due items back to next.
func (h *hub) holdRest(rest []outItem, next int64) {
	if next == 0 || len(rest) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range rest {
		if cur := h.out[rest[i].ID]; cur != nil && cur.State == "pending" && cur.NextAt < next {
			cur.NextAt = next
			_ = h.putItem(cur)
		}
	}
}

// suiteRuns fills a checks.completed's runs from the suite's check runs
// on its commit (GitHub's suite event doesn't carry them): the event with
// them, or nil (it goes without).
func (s *srv) suiteRuns(ctx context.Context, it *outItem) json.RawMessage {
	var ev map[string]json.RawMessage
	if json.Unmarshal(it.Event, &ev) != nil {
		return nil
	}
	var data map[string]json.RawMessage
	var cd checksData
	if json.Unmarshal(ev["data"], &data) != nil || json.Unmarshal(data[kindChecks], &cd) != nil || cd.HeadSHA == "" {
		return nil
	}
	suite := strings.TrimPrefix(it.Enrich, "suite:")
	auth, err := s.instAuth(ctx, ownerOf(it.Repo), nameOf(it.Repo), "read")
	if err != nil {
		return nil
	}
	var out struct {
		CheckRuns []ghCheckRun `json:"check_runs"`
	}
	if _, err := s.gh.call(ctx, auth, http.MethodGet, s.repoBase(it.Repo)+"/commits/"+cd.HeadSHA+"/check-runs?per_page=100", nil, &out); err != nil {
		return nil
	}
	have := map[string]bool{}
	for _, r := range cd.Runs {
		have[r.ID] = true
	}
	for _, g := range out.CheckRuns {
		if g.CheckSuite == nil || idStr(g.CheckSuite.ID) != suite || have[idStr(g.ID)] || len(cd.Runs) >= 100 {
			continue
		}
		c := checkOf(&g)
		cd.Runs = append(cd.Runs, checksRun{ID: c.ID, Name: c.Name, Conclusion: c.Conclusion, URL: c.URL})
	}
	b, _ := json.Marshal(cd)
	data[kindChecks] = b
	ev["data"], _ = json.Marshal(data)
	raw, err := json.Marshal(ev)
	if err != nil {
		return nil
	}
	return raw
}
