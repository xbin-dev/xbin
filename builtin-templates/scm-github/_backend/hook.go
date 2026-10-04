// hook.go — GitHub's webhooks at global (API.md §7): POST /hook/github
// checks the signature against the current and the previous secret, drops a
// delivery for an account outside allowedAccounts before anything else,
// dedupes per installation, keeps the installation and access caches
// fresh, and queues one outbox item per (event, consumer, for) — answering
// 202 at once. Also the events half's mounting (its routes, its cap, the
// events health hello reports, its tick) and the catch-up of deliveries
// GitHub couldn't make while this tile was down.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func init() {
	extraRoutes = append(extraRoutes, func(s *srv, mux *http.ServeMux) {
		mux.HandleFunc("POST /hook/github", s.handleHook)
		mux.HandleFunc("POST /scm/subscriptions", s.scmGuard(false, s.handleSubPost))
		mux.HandleFunc("GET /scm/subscriptions", s.scmGuard(false, s.handleSubList))
		mux.HandleFunc("DELETE /scm/subscriptions/{id}", s.scmGuard(false, s.handleSubDelete))
		mux.HandleFunc("GET /scm/events", s.scmGuard(false, s.handleEvents))
		mux.HandleFunc("POST /partition/subscriptions", s.relayGuard(s.handleRelaySubPost))
		mux.HandleFunc("GET /partition/subscriptions", s.relayGuard(s.handleRelaySubList))
		mux.HandleFunc("DELETE /partition/subscriptions/{id}", s.relayGuard(s.handleRelaySubDelete))
		mux.HandleFunc("GET /partition/events", s.relayGuard(s.handleRelayEvents))
		mux.HandleFunc("GET /api/events", s.managerGuard(s.handleEventsStats))
	})
	extraCaps = append(extraCaps, func(*srv) []string { return []string{capEvents} })
	eventsHealth = evHealth
	tickHooks = append(tickHooks, eventsTick)
	wipePerson = append(wipePerson, wipeEvents)
}

const (
	hookBodyMax  = 8 << 20
	seenFor      = 7 * 24 * time.Hour
	seenPerInst  = 10000
	prevSecretOK = 24 * time.Hour
)

var (
	deliveryRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	ghEventRE  = regexp.MustCompile(`^[a-z_]{1,64}$`)
)

// handleHook is a GitHub delivery.
func (s *srv) handleHook(w http.ResponseWriter, r *http.Request) {
	if s.mode == modeUser {
		fail(w, refuse(refNotFound, "webhooks arrive at this tile's global instance"))
		return
	}
	c := s.classify(r)
	if c.cls != clsIngress && !s.manager(c) {
		fail(w, refuse(refNotAllowed, "webhooks come from GitHub, through the hooks exposure"))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, hookBodyMax))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "the body is over 8 MiB"})
		return
	}
	now := s.now()
	ok, err := s.verifyHook(r.Header.Get("X-Hub-Signature-256"), body)
	if err != nil {
		fail(w, err)
		return
	}
	h := s.ev()
	if !ok {
		h.mu.Lock()
		h.load()
		h.health.Hook = s.hookState()
		h.counts.BadSig++
		h.health.LastSigFailAt = now.UnixMilli()
		h.saveHealth()
		h.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad signature"})
		return
	}
	ghEvent, delivery := r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery")
	if !ghEventRE.MatchString(ghEvent) || !deliveryRE.MatchString(delivery) {
		fail(w, refuse(refInvalid, "X-GitHub-Event and X-GitHub-Delivery are required"))
		return
	}
	h.mu.Lock()
	h.load()
	h.counts.Received++
	h.health.Hook = s.hookState()
	h.health.LastDeliveryAt = now.UnixMilli()
	h.saveHealth()
	h.mu.Unlock()
	if ghEvent == "ping" {
		writeJSON(w, http.StatusOK, map[string]string{"result": "pong"})
		return
	}
	var hk ghHook
	if err := json.Unmarshal(body, &hk); err != nil {
		fail(w, refuse(refInvalid, "the body isn't GitHub's JSON"))
		return
	}
	// A public App can be installed by anyone: an account the policy
	// doesn't serve is dropped before anything else is done with it.
	if acct := hk.account(); !validLogin(acct) || !s.policy().accountAllowed(acct) {
		h.mu.Lock()
		h.counts.Foreign++
		h.mu.Unlock()
		writeJSON(w, http.StatusAccepted, map[string]string{"result": "dropped: an account this scm-github doesn't serve"})
		return
	}
	inst := hk.installationID()
	key := seenKey(inst, delivery)
	h.mu.Lock()
	busy := h.inflight[key]
	h.inflight[key] = true
	h.mu.Unlock()
	if !busy {
		defer func() {
			h.mu.Lock()
			delete(h.inflight, key)
			h.mu.Unlock()
		}()
	}
	if busy || s.seen(inst, delivery) {
		h.mu.Lock()
		h.counts.Duplicates++
		h.mu.Unlock()
		writeJSON(w, http.StatusAccepted, map[string]string{"result": "duplicate"})
		return
	}
	n := 0
	switch ghEvent {
	case "installation", "installation_repositories", "member", "membership", "organization":
		s.accessEvent(ghEvent, &hk, now)
	default:
		nc := normCtx{provider: s.self, delivery: delivery, now: now.UnixMilli()}
		nc.host, _, nc.web = s.hosts()
		if a, _ := s.app(); a != nil && a.Slug != "" {
			nc.botLogin = a.Slug + "[bot]"
		}
		evs := normalize(ghEvent, &hk, nc)
		s.proveBranches(r.Context(), evs)
		if n, err = s.enqueue(evs); err != nil {
			fail(w, err) // not marked seen: a redelivery is taken
			return
		}
	}
	s.markSeen(inst, delivery, now.UnixMilli())
	if n > 0 {
		h.kick()
		h.syncCron()
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"result": "accepted", "queued": n})
}

// verifyHook checks X-Hub-Signature-256 in constant time against the
// webhook secret, and against the previous one for a day after a rotation.
// No secret at all is 503 setup.
func (s *srv) verifyHook(sig string, body []byte) (bool, error) {
	cur, err := s.vault.Get(vaultHookSecret)
	if err != nil || cur.Empty() {
		return false, refuse(refSetup, "no webhook secret yet: a manager sets the App up")
	}
	got, okp := strings.CutPrefix(strings.ToLower(strings.TrimSpace(sig)), "sha256=")
	if !okp {
		return false, nil
	}
	secrets := []secretString{cur}
	var rotated int64
	if s.state.Get("hook-secret-rotated", &rotated) == nil && s.now().UnixMilli()-rotated < prevSecretOK.Milliseconds() {
		if prev, err := s.vault.Get(vaultHookSecretP); err == nil && !prev.Empty() {
			secrets = append(secrets, prev)
		}
	}
	match := false
	for _, sec := range secrets {
		mac := hmac.New(sha256.New, []byte(sec.Reveal()))
		mac.Write(body)
		if hmac.Equal([]byte(got), []byte(hex.EncodeToString(mac.Sum(nil)))) {
			match = true
		}
	}
	return match, nil
}

// accessEvent keeps the caches GitHub's access events touch: installation
// events the installation cache; member, membership, organization and
// installation_repositories the read-access cache. None is forwarded.
func (s *srv) accessEvent(ghEvent string, hk *ghHook, now time.Time) {
	switch ghEvent {
	case "installation":
		if hk.Installation == nil || hk.Installation.Account == nil || !validLogin(hk.Installation.Account.Login) {
			return
		}
		acct := hk.Installation.Account.Login
		key := "inst/" + strings.ToLower(acct)
		switch hk.Action {
		case "deleted", "suspend":
			_ = s.state.Delete(key)
			_ = s.dropInstallation(hk.Installation.ID)
			s.dropAccess("", acct, "")
		default: // created, unsuspend, new_permissions_accepted
			_ = s.state.Put(key, instCache{ID: hk.Installation.ID, Account: acct, At: now.UnixMilli()})
		}
		s.reposC.clear()
	case "installation_repositories":
		for _, rp := range append(append([]ghHookRepo{}, hk.RepositoriesAdded...), hk.RepositoriesRemoved...) {
			if validRepo(rp.FullName) {
				s.dropAccess("", "", rp.FullName)
			}
		}
		s.reposC.clear()
	case "member":
		if hk.Member != nil && validLogin(hk.Member.Login) && hk.Repository != nil && validRepo(hk.Repository.FullName) {
			s.dropAccess(hk.Member.Login, "", hk.Repository.FullName)
		}
	case "membership", "organization":
		var u *ghUser
		if hk.Member != nil {
			u = hk.Member
		} else if hk.Membership != nil {
			u = hk.Membership.User
		}
		org := ""
		if hk.Organization != nil {
			org = hk.Organization.Login
		}
		if !validLogin(org) {
			return
		}
		if u != nil && validLogin(u.Login) {
			s.dropAccess(u.Login, org, "")
		} else if ghEvent == "organization" {
			s.dropAccess("", org, "") // a rename, a deletion: everyone's there
		}
	}
}

// --- matching and queueing ---------------------------------------------------------------

// group is one (consumer, for) an event reaches, with the subscriptions
// of theirs it matched.
type group struct {
	consumer, forWhom, person, pid string
	subs                           []*subscription
}

func (g *group) key() string { return g.consumer + "|" + g.forWhom + "|" + g.pid }

// checksWindow is a checks.completed held a few seconds, so a commit
// status and a check suite of one commit make one event.
type checksWindow struct {
	at     int64
	ev     *event
	groups map[string]*group
	items  map[string]string // group key → item id
}

// match is the (consumer, for)s an event reaches. A tile's subscription
// also keeps to botRepos. h.mu held.
func (h *hub) match(e *event, pol policy, now int64) map[string]*group {
	out := map[string]*group{}
	for _, sub := range h.subs {
		if sub.Expires < now || !sub.matches(e) || (sub.Person == "" && !pol.botRepoAllowed(e.Repo)) {
			continue
		}
		g := &group{consumer: sub.Consumer, forWhom: sub.For, person: sub.Person, pid: sub.PID}
		if old := out[g.key()]; old != nil {
			g = old
		} else {
			out[g.key()] = g
		}
		g.subs = append(g.subs, sub)
	}
	return out
}

// render is the event as one (consumer, for) gets it: its for, forPid and
// the subscriptions it matched; a branch it matched by when the event's
// own isn't one it names.
func (e *event) render(g *group) json.RawMessage {
	out := *e
	out.For, out.ForPid, out.Subs = g.forWhom, g.pid, nil
	if g.person == "" {
		out.ForPid = ""
	}
	byBranch := ""
	for _, sub := range g.subs {
		k := sub.Key
		if k == "" {
			k = sub.ID
		}
		if !containsFold(out.Subs, k) {
			out.Subs = append(out.Subs, k)
		}
		if b := sub.branchOf(e); b != "" && byBranch == "" {
			byBranch = b
		}
	}
	sort.Strings(out.Subs)
	if byBranch != "" && !containsFold(groupBranches(g), out.Ref.Branch) {
		out.Ref.Branch = byBranch
		out.Topic, out.Summary = topicOf(out.SCM.Host, &out), summaryOf(&out)
	}
	b, _ := json.Marshal(&out)
	return b
}

func groupBranches(g *group) []string {
	var bs []string
	for _, sub := range g.subs {
		bs = append(bs, sub.Branches...)
	}
	return bs
}

// enqueue writes one outbox item per (event, consumer, for); a
// checks.completed waits five seconds for its commit's other half. The
// time is read here, under the lock the delivery pass takes too.
func (s *srv) enqueue(evs []*event) (int, error) {
	h := s.ev()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.load()
	now := s.now().UnixMilli()
	pol := s.policy()
	n := 0
	var pend map[string]int
	for _, e := range evs {
		groups := h.match(e, pol, now)
		if len(groups) == 0 {
			continue
		}
		if pend == nil {
			pend = h.pendingPer()
		}
		if e.Kind == kindChecks {
			if m, err := h.mergeChecks(e, groups, now, pend); err != nil || m >= 0 {
				n += max(m, 0)
				if err != nil {
					return n, err
				}
				continue
			}
		}
		keys := make([]string, 0, len(groups))
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var w *checksWindow
		if e.Kind == kindChecks && e.Ref.SHA != "" {
			w = &checksWindow{at: now, ev: e, groups: map[string]*group{}, items: map[string]string{}}
		}
		for _, k := range keys {
			if !h.admit(groups[k], pend) {
				continue
			}
			it, err := h.newItem(e, groups[k], now, w != nil)
			if err != nil {
				return n, err
			}
			n++
			if w != nil {
				w.groups[k], w.items[k] = groups[k], it.ID
			}
		}
		if w != nil && len(w.items) > 0 {
			h.window[strings.ToLower(e.Repo)+"|"+e.Ref.SHA] = w
		}
	}
	return n, nil
}

// pendingPer is each consumer's pending items. h.mu held.
func (h *hub) pendingPer() map[string]int {
	pend := map[string]int{}
	for _, it := range h.out {
		if it.State == "pending" {
			pend[it.Consumer]++
		}
	}
	return pend
}

// admit says whether one more item for g fits: the outbox's room, and the
// consumer's own share of it (a consumer failing for a day fills its share,
// never the others'); one that doesn't is counted as overflow. h.mu held.
func (h *hub) admit(g *group, pend map[string]int) bool {
	if pend[g.consumer] >= outboxPerConsumer || !h.room(1) {
		h.counts.Overflow++
		return false
	}
	pend[g.consumer]++
	return true
}

// newItem queues an event for a group. h.mu held.
func (h *hub) newItem(e *event, g *group, now int64, hold bool) (*outItem, error) {
	it := &outItem{ID: newItemID(now), Consumer: g.consumer, For: g.forWhom, Person: g.person, PID: g.pid, Repo: e.Repo,
		Private: e.Private, Event: e.render(g), State: "pending", QueuedAt: now, NextAt: now}
	if hold {
		it.NextAt = now + checksMerge.Milliseconds()
	}
	if d, ok := e.Data[kindChecks].(checksData); ok && d.Suite != "" {
		it.Enrich = "suite:" + d.Suite
	}
	if err := h.putItem(it); err != nil {
		return nil, err
	}
	h.counts.Queued++
	return it, nil
}

// mergeChecks folds a checks.completed into one of the same commit still
// held (a status and a suite within five seconds): the worse conclusion,
// both halves' runs. -1: nothing to fold into — the window closed, or a
// delivery pass already took one of its items. h.mu held.
func (h *hub) mergeChecks(e *event, groups map[string]*group, now int64, pend map[string]int) (int, error) {
	key := strings.ToLower(e.Repo) + "|" + e.Ref.SHA
	w := h.window[key]
	if w == nil || now-w.at >= checksMerge.Milliseconds() {
		return -1, nil
	}
	for _, id := range w.items {
		if it := h.out[id]; it == nil || it.State != "pending" || it.Attempts > 0 || h.sending[id] {
			return -1, nil
		}
	}
	m := w.ev
	if conclusionRank(e.Conclusion) > conclusionRank(m.Conclusion) {
		m.Conclusion = e.Conclusion
	}
	md, _ := m.Data[kindChecks].(checksData)
	ed, _ := e.Data[kindChecks].(checksData)
	md.Runs = append(md.Runs, ed.Runs...)
	if md.Suite == "" {
		md.Suite = ed.Suite
	}
	m.Data[kindChecks] = md
	for _, b := range e.branches {
		if !containsFold(m.branches, b) {
			m.branches = append(m.branches, b)
		}
	}
	if m.Ref.PR == 0 && e.Ref.PR > 0 {
		m.Ref.PR, m.URL = e.Ref.PR, e.URL // the pull request's checks page
	}
	if m.Ref.Branch == "" {
		m.Ref.Branch = e.Ref.Branch
	}
	m.Topic, m.Summary = topicOf(m.SCM.Host, m), summaryOf(m)
	n := 0
	for k, g := range groups {
		if old := w.groups[k]; old != nil {
			for _, sub := range g.subs {
				if !subIn(old.subs, sub) {
					old.subs = append(old.subs, sub)
				}
			}
			continue
		}
		if !h.admit(g, pend) {
			continue
		}
		w.groups[k] = g
		it, err := h.newItem(m, g, w.at, true)
		if err != nil {
			return n, err
		}
		w.items[k] = it.ID
		n++
	}
	for k, id := range w.items {
		it := h.out[id]
		it.Event = m.render(w.groups[k])
		if md.Suite != "" {
			it.Enrich = "suite:" + md.Suite
		}
		if err := h.putItem(it); err != nil {
			return n, err
		}
	}
	return n, nil
}

func subIn(l []*subscription, s *subscription) bool {
	for _, x := range l {
		if x.ID == s.ID {
			return true
		}
	}
	return false
}

// conclusionRank orders conclusions from best to worst.
func conclusionRank(c string) int {
	switch c {
	case "success", "":
		return 0
	case "neutral", "skipped":
		return 1
	case "stale", "cancelled":
		return 2
	}
	return 3 // failure, timed_out, action_required, startup_failure
}

// --- dedupe ---------------------------------------------------------------------------------

func seenKey(inst int64, delivery string) string { return "seen/" + idStr(inst) + "/" + delivery }

func (s *srv) seen(inst int64, delivery string) bool {
	var at int64
	return s.state.Get(seenKey(inst, delivery), &at) == nil
}

func (s *srv) markSeen(inst int64, delivery string, now int64) {
	_ = s.state.Put(seenKey(inst, delivery), now)
	h := s.ev()
	h.mu.Lock()
	k := idStr(inst)
	h.seenAdds[k]++
	force, bg := h.seenAdds[k] > seenPerInst/10, h.bg
	h.mu.Unlock()
	switch {
	case force && bg:
		go s.pruneSeen(true)
	case force:
		s.pruneSeen(true)
	}
}

// pruneSeen keeps the dedupe set to seven days and 10 000 deliveries per
// installation, so one installation's traffic never evicts another's. At
// most daily unless forced (an installation's set grew by a tenth).
func (s *srv) pruneSeen(force bool) {
	h := s.ev()
	now := s.now().UnixMilli()
	h.mu.Lock()
	if !force && now-h.lastPrune < (24*time.Hour).Milliseconds() {
		h.mu.Unlock()
		return
	}
	h.lastPrune = now
	h.seenAdds = map[string]int{}
	h.mu.Unlock()
	keys, err := s.state.List("seen/")
	if err != nil {
		return
	}
	type ent struct {
		key string
		at  int64
	}
	per := map[string][]ent{}
	for _, k := range keys {
		inst, _, _ := strings.Cut(strings.TrimPrefix(k, "seen/"), "/")
		var at int64
		if s.state.Get(k, &at) != nil || now-at > seenFor.Milliseconds() {
			_ = s.state.Delete(k)
			continue
		}
		per[inst] = append(per[inst], ent{k, at})
	}
	for _, l := range per {
		if len(l) <= seenPerInst {
			continue
		}
		sort.Slice(l, func(i, k int) bool { return l[i].at < l[k].at })
		for _, e := range l[:len(l)-seenPerInst] {
			_ = s.state.Delete(e.key)
		}
	}
}

// --- health ----------------------------------------------------------------------------------

// healthRec is state "events-health" at global and conf "events" for
// people's partitions: what hello's events say.
type healthRec struct {
	Hook           string `json:"hook"` // set | unset | "" (no App)
	LastDeliveryAt int64  `json:"lastDeliveryAt"`
	LastSigFailAt  int64  `json:"lastSigFailAt"`
}

// saveHealth keeps the health record when it changes — a new delivery's
// or a bad signature's time at most once a minute, so a stranger's bad
// signatures cost a write a minute at most. h.mu held.
func (h *hub) saveHealth() {
	p, minute := h.saved, time.Minute.Milliseconds()
	if p.Hook == h.health.Hook && h.health.LastSigFailAt-p.LastSigFailAt < minute && h.health.LastDeliveryAt-p.LastDeliveryAt < minute {
		return
	}
	_ = h.s.state.Put("events-health", h.health)
	_ = h.s.conf.Put("events", h.health)
	h.saved = h.health
}

// hookState is whether GitHub's hook points somewhere: set, unset, or ""
// with no App.
func (s *srv) hookState() string {
	a, _ := s.app()
	switch {
	case a == nil:
		return ""
	case a.HookURL != "" && !strings.Contains(a.HookURL, "example.invalid"):
		return "set"
	}
	return "unset"
}

// evHealth is hello's events: webhooks active once GitHub's hook points
// here and a delivery (or a ping) came in the last day, inactive while it
// points nowhere; healthy when active and no signature failed in the last
// hour.
func evHealth(s *srv) eventsHealthInfo {
	now := s.now().UnixMilli()
	var rec healthRec
	if s.mode == modeUser {
		_ = s.conf.Get("events", &rec)
	} else {
		h := s.ev()
		h.mu.Lock()
		h.load()
		if hook := s.hookState(); hook != h.health.Hook {
			h.health.Hook = hook
			h.saveHealth()
		}
		rec = h.health
		h.mu.Unlock()
	}
	out := eventsHealthInfo{Webhooks: "unknown", PollMinMs: pollMinMs, LastDeliveryAt: rec.LastDeliveryAt}
	switch {
	case rec.Hook == "unset":
		out.Webhooks = "inactive"
	case rec.Hook == "set" && rec.LastDeliveryAt > 0 && now-rec.LastDeliveryAt < (24*time.Hour).Milliseconds():
		out.Webhooks = "active"
	}
	out.Healthy = out.Webhooks == "active" && now-rec.LastSigFailAt >= time.Hour.Milliseconds()
	return out
}

// handleEventsStats is the page's (managers') view of the events half.
func (s *srv) handleEventsStats(w http.ResponseWriter, r *http.Request, _ who) {
	h := s.ev()
	health := evHealth(s)
	h.mu.Lock()
	h.load()
	pend, done := 0, 0
	for _, it := range h.out {
		if it.State == "pending" {
			pend++
		} else {
			done++
		}
	}
	out := map[string]any{"health": health, "counts": h.counts, "outbox": map[string]int{"pending": pend, "delivered": done},
		"subscriptions": len(h.subs)}
	h.mu.Unlock()
	writeGET(w, r, out)
}

// --- catch-up -------------------------------------------------------------------------------

// catchUp asks GitHub to redeliver what it failed to deliver since the
// last delivery that arrived (at most 50, within GitHub's three days).
func (s *srv) catchUp(ctx context.Context) (int, error) {
	a, err := s.app()
	if err != nil || a == nil {
		return 0, err
	}
	auth, err := s.appAuth()
	if err != nil {
		return 0, err
	}
	h := s.ev()
	h.mu.Lock()
	h.load()
	since := h.health.LastDeliveryAt - time.Minute.Milliseconds()
	h.mu.Unlock()
	if floor := s.now().Add(-72 * time.Hour).UnixMilli(); since < floor {
		since = floor
	}
	var list []struct {
		ID           int64  `json:"id"`
		GUID         string `json:"guid"`
		DeliveredAt  string `json:"delivered_at"`
		Redelivery   bool   `json:"redelivery"`
		StatusCode   int    `json:"status_code"`
		Installation int64  `json:"installation_id"`
	}
	if _, err := s.gh.call(ctx, auth, http.MethodGet, s.apiBase()+"/app/hook/deliveries?per_page=100", nil, &list); err != nil {
		return 0, err
	}
	n := 0
	for _, d := range list {
		if n >= 50 {
			break
		}
		if ghTime(d.DeliveredAt) < since || (d.StatusCode >= 200 && d.StatusCode < 300) || d.Redelivery ||
			!deliveryRE.MatchString(d.GUID) || s.seen(d.Installation, d.GUID) {
			continue
		}
		if _, err := s.gh.call(ctx, auth, http.MethodPost, s.apiBase()+"/app/hook/deliveries/"+strconv.FormatInt(d.ID, 10)+"/attempts", nil, nil); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
