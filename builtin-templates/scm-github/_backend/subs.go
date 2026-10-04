// subs.go — subscriptions (docs/scm.md §Subscriptions; API.md §7): POST,
// GET and DELETE /scm/subscriptions at global for a tile (`for: global`,
// the bot must see the repo) and, relayed from a person's partition
// (/partition/subscriptions), for that person (`for: user:<id>`, their
// verified login must be able to read the repo); which subscriptions an
// event matches; and each person's read access, cached an hour at
// `access/<login>/<repo>` and dropped by GitHub's access events.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	subLife        = 30 * 24 * time.Hour
	subsPerHolder  = 2000  // one consumer's for one `for`
	subsMax        = 20000 // in all
	accessCacheFor = time.Hour
)

// subscription is state "sub/<id>". The first fields are the contract's
// answer; the rest is kept, never answered.
type subscription struct {
	ID       string   `json:"id"`
	Repo     string   `json:"repo"`
	Branches []string `json:"branches"`
	PRs      []int    `json:"prs"`
	Issues   bool     `json:"issues"`
	Kinds    []string `json:"kinds"`
	Key      string   `json:"key,omitempty"`
	For      string   `json:"for"`
	Expires  int64    `json:"expires"`

	Consumer string `json:"consumer"`         // the consumer's tile path: delivery goes to its `agents` binding
	Person   string `json:"person,omitempty"` // for user:<id>: the xbin person
	PID      string `json:"pid,omitempty"`    // their partition id when it was made
	Created  int64  `json:"created"`
}

// subView is a subscription as the contract answers it.
type subView struct {
	ID       string   `json:"id"`
	Repo     string   `json:"repo"`
	Branches []string `json:"branches"`
	PRs      []int    `json:"prs"`
	Issues   bool     `json:"issues"`
	Kinds    []string `json:"kinds"`
	Key      string   `json:"key,omitempty"`
	For      string   `json:"for"`
	Expires  int64    `json:"expires"`
}

func (sub *subscription) view() subView {
	return subView{sub.ID, sub.Repo, sub.Branches, sub.PRs, sub.Issues, sub.Kinds, sub.Key, sub.For, sub.Expires}
}

// subReq is POST /scm/subscriptions's body.
type subReq struct {
	Repo     string   `json:"repo"`
	Branches []string `json:"branches"`
	PRs      []int    `json:"prs"`
	Issues   bool     `json:"issues"`
	Kinds    []string `json:"kinds"`
	Key      string   `json:"key"`
}

// check validates a subscription request (a relay's too: the person's own
// input).
func (q *subReq) check() error {
	if !validRepo(q.Repo) {
		return refuse(refInvalid, "repo is owner/name")
	}
	if len(q.Branches) > 50 || len(q.PRs) > 100 || len(q.Kinds) > 30 || len(q.Key) > 200 {
		return refuse(refInvalid, "at most 50 branches, 100 prs and 30 kinds; a key of at most 200 bytes")
	}
	for _, b := range q.Branches {
		if cleanBranch(b) == "" {
			return refuse(refInvalid, "%q isn't a branch name", clip(b, 80))
		}
	}
	for _, n := range q.PRs {
		if n <= 0 {
			return refuse(refInvalid, "prs are pull request numbers")
		}
	}
	for _, k := range q.Kinds {
		kind, action, dotted := strings.Cut(k, ".")
		if _, ok := kindActions[kind]; !ok || (dotted && !hasAction(kind, action)) {
			return refuse(refInvalid, "%q isn't a kind (pull, checks, comment, review, push, issue, workflow, job, check — or kind.action)", clip(k, 80))
		}
	}
	for _, r := range q.Key {
		if r < 0x20 || r == 0x7f {
			return refuse(refInvalid, "key is text without control characters")
		}
	}
	if q.Branches == nil {
		q.Branches = []string{}
	}
	if q.PRs == nil {
		q.PRs = []int{}
	}
	if q.Kinds == nil {
		q.Kinds = []string{}
	}
	return nil
}

// matches says whether an event is one this subscription asked for: its
// repo; its kinds (none: every kind but the progress ones); and, when it
// names branches, pull requests or issues, one of those.
func (sub *subscription) matches(e *event) bool {
	if !strings.EqualFold(sub.Repo, e.Repo) {
		return false
	}
	if len(sub.Kinds) == 0 {
		if progressKind(e.Kind) {
			return false
		}
	} else if !slices.Contains(sub.Kinds, e.Kind) && !slices.Contains(sub.Kinds, e.Kind+"."+e.Action) {
		return false
	}
	if len(sub.Branches) == 0 && len(sub.PRs) == 0 && !sub.Issues {
		return true
	}
	return sub.branchOf(e) != "" || (e.Ref.PR > 0 && slices.Contains(sub.PRs, e.Ref.PR)) || (sub.Issues && e.Ref.Issue > 0)
}

// branchOf is the first of the event's branches this subscription names.
func (sub *subscription) branchOf(e *event) string {
	for _, b := range e.branches {
		if slices.Contains(sub.Branches, b) {
			return b
		}
	}
	return ""
}

// --- the routes ----------------------------------------------------------------------

// personAtUnpartitioned refuses a person's consumer at an unpartitioned
// copy: it keeps no one's sign-in, so it can't check what they may read.
func personAtUnpartitioned() error {
	e := refuse(refIdentity, "this scm-github isn't partitioned: it keeps no one's sign-in, so it delivers no one's events (a tile's are for: global)")
	e.Identities = []string{}
	return e
}

func (s *srv) handleSubPost(w http.ResponseWriter, r *http.Request, c who) {
	var q subReq
	if err := readBody(r, &q); err != nil {
		fail(w, err)
		return
	}
	if err := q.check(); err != nil {
		fail(w, err)
		return
	}
	switch {
	case s.mode == modeUser:
		if s.signedIn() == nil {
			fail(w, s.signinRefusal(r.Context()))
			return
		}
		st, raw, err := s.relayRaw(r.Context(), http.MethodPost, "/partition/subscriptions", map[string]any{"consumer": c.c.From, "sub": q})
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		_, _ = w.Write(raw)
	case c.cls == clsTile:
		err := s.withAuth(r.Context(), c, asBot, q.Repo, false, func(a ghAuth) error {
			_, err := s.gh.call(r.Context(), a, http.MethodGet, s.repoBase(q.Repo), nil, nil)
			return err
		})
		if err != nil {
			fail(w, err)
			return
		}
		s.answerSub(w, &subscription{Consumer: c.c.From, For: "global"}, q)
	default:
		fail(w, personAtUnpartitioned())
	}
}

// answerSub stores a subscription (a repeat of its key replaces it: 200)
// and answers it.
func (s *srv) answerSub(w http.ResponseWriter, base *subscription, q subReq) {
	sub, replaced, err := s.ev().storeSub(base, q, s.now())
	if err != nil {
		fail(w, err)
		return
	}
	st := http.StatusCreated
	if replaced {
		st = http.StatusOK
	}
	writeJSON(w, st, sub.view())
}

func (h *hub) storeSub(base *subscription, q subReq, now time.Time) (*subscription, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.load()
	var old *subscription
	n := 0
	for _, sub := range h.subs {
		if sub.Consumer != base.Consumer || sub.For != base.For || sub.PID != base.PID {
			continue
		}
		n++
		if q.Key != "" && sub.Key == q.Key {
			old = sub
		}
	}
	if old == nil && (n >= subsPerHolder || len(h.subs) >= subsMax) {
		e := refuse(refLimit, "too many subscriptions (at most %d per consumer): delete some", subsPerHolder)
		e.RetryAfterMs = 60_000
		return nil, false, e
	}
	sub := *base
	sub.ID, sub.Created = "s_"+randomID(12), now.UnixMilli()
	if old != nil {
		sub.ID, sub.Created = old.ID, old.Created
	}
	sub.Repo, sub.Branches, sub.PRs, sub.Issues, sub.Kinds, sub.Key = q.Repo, q.Branches, q.PRs, q.Issues, q.Kinds, q.Key
	sub.Expires = now.Add(subLife).UnixMilli()
	if err := h.s.state.Put("sub/"+sub.ID, &sub); err != nil {
		return nil, false, err
	}
	h.subs[sub.ID] = &sub
	return &sub, old != nil, nil
}

func (s *srv) handleSubList(w http.ResponseWriter, r *http.Request, c who) {
	switch {
	case s.mode == modeUser:
		var out struct {
			Items []subView `json:"items"`
		}
		if err := s.relay(r.Context(), http.MethodGet, "/partition/subscriptions?consumer="+url.QueryEscape(c.c.From), nil, &out); err != nil {
			fail(w, err)
			return
		}
		writeGET(w, r, out)
	case c.cls == clsTile:
		writeGET(w, r, s.listSubs(c.c.From, "global", ""))
	default:
		fail(w, personAtUnpartitioned())
	}
}

func (s *srv) listSubs(consumer, forWhom, pid string) map[string][]subView {
	h := s.ev()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.load()
	items := []subView{}
	now := s.now().UnixMilli()
	for _, sub := range h.subs {
		if sub.Consumer == consumer && sub.For == forWhom && sub.PID == pid && sub.Expires >= now {
			items = append(items, sub.view())
		}
	}
	slices.SortFunc(items, func(a, b subView) int { return strings.Compare(a.ID, b.ID) })
	return map[string][]subView{"items": items}
}

func (s *srv) handleSubDelete(w http.ResponseWriter, r *http.Request, c who) {
	id := r.PathValue("id")
	switch {
	case s.mode == modeUser:
		if err := s.relay(r.Context(), http.MethodDelete, "/partition/subscriptions/"+url.PathEscape(id), nil, nil); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case c.cls == clsTile:
		s.deleteSub(w, id, func(sub *subscription) bool { return sub.Consumer == c.c.From && sub.For == "global" })
	default:
		fail(w, personAtUnpartitioned())
	}
}

// deleteSub deletes one subscription its caller holds (404 otherwise).
func (s *srv) deleteSub(w http.ResponseWriter, id string, mine func(*subscription) bool) {
	h := s.ev()
	h.mu.Lock()
	h.load()
	sub := h.subs[id]
	if sub == nil || !mine(sub) {
		h.mu.Unlock()
		fail(w, refuse(refNotFound, "no such subscription"))
		return
	}
	h.dropSub(id)
	h.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// dropSub forgets a subscription. h.mu held.
func (h *hub) dropSub(id string) {
	delete(h.subs, id)
	_ = h.s.state.Delete("sub/" + id)
}

// --- the relay (global) ----------------------------------------------------------------

// handleRelaySubPost is a person's subscription, relayed from their
// partition. The body is theirs: consumer must be one of this instance's
// `agents` bindings, and the person's own verified login must be able to
// read the repo.
func (s *srv) handleRelaySubPost(w http.ResponseWriter, r *http.Request, c who) {
	s.relayStart(c)
	var body struct {
		Consumer string `json:"consumer"`
		Sub      subReq `json:"sub"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	q := body.Sub
	if err := q.check(); err != nil {
		fail(w, err)
		return
	}
	if !s.ev().bound(body.Consumer) {
		fail(w, refuse(refInvalid, "consumer isn't a tile this scm-github delivers events to (bx bind %s agents+=<it>)", s.self))
		return
	}
	id := s.ident(c.person)
	if id == nil {
		fail(w, refuse(refSignin, "sign in to GitHub first: a person's events are checked against their own GitHub access"))
		return
	}
	if err := s.policy().checkAccount(ownerOf(q.Repo)); err != nil {
		fail(w, err)
		return
	}
	perm, err := s.personRead(r.Context(), id.Login, q.Repo, true)
	if err != nil {
		fail(w, err)
		return
	}
	if perm == "none" {
		fail(w, refuse(refNotAllowed, "%s can't read %s on GitHub", id.Login, q.Repo))
		return
	}
	s.answerSub(w, &subscription{Consumer: body.Consumer, For: "user:" + c.person, Person: c.person, PID: c.pid}, q)
}

func (s *srv) handleRelaySubList(w http.ResponseWriter, r *http.Request, c who) {
	s.relayStart(c)
	writeGET(w, r, s.listSubs(r.URL.Query().Get("consumer"), "user:"+c.person, c.pid))
}

func (s *srv) handleRelaySubDelete(w http.ResponseWriter, r *http.Request, c who) {
	s.relayStart(c)
	s.deleteSub(w, r.PathValue("id"), func(sub *subscription) bool { return sub.Person == c.person && sub.PID == c.pid })
}

// wipeEvents forgets everything kept for a person at global (their
// identity went: Forget, or a new partition under the same id).
func wipeEvents(s *srv, person string) {
	h := s.ev()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.load()
	for id, sub := range h.subs {
		if sub.Person == person {
			h.dropSub(id)
		}
	}
	for id, it := range h.out {
		if it.Person == person {
			h.dropItem(id)
		}
	}
}

// bound says whether consumer is one of this instance's `agents` bindings.
func (h *hub) bound(consumer string) bool {
	return h.endpoint(consumer) != ""
}

// endpoint is a consumer's `agents` binding's URL ("" when unbound; a
// person's own binding never counts at global).
func (h *hub) endpoint(consumer string) string {
	if consumer == "" {
		return ""
	}
	for _, a := range h.agents() {
		if a.Provider == consumer && !a.Personal && a.URL != "" {
			return a.URL
		}
	}
	return ""
}

// relayRaw is relay keeping the global instance's status and body (a
// subscription's 201 or 200).
func (s *srv) relayRaw(ctx context.Context, method, path string, body any) (int, []byte, error) {
	if s.relayCall == nil {
		return 0, nil, refuse(refUnavailable, "this partition can't reach its global instance")
	}
	b, _ := json.Marshal(body)
	resp, err := s.relayCall(ctx, method, path, b)
	if err != nil {
		e := refuse(refUnavailable, "this tile's global instance didn't answer")
		e.RetryAfterMs = 5000
		return 0, nil, e
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, refuse(refUnavailable, "this tile's global instance's answer broke off")
	}
	if resp.StatusCode >= 300 {
		var e scmErr
		if json.Unmarshal(raw, &e) != nil || e.Refusal == "" {
			return 0, nil, refuse(refUnavailable, "this tile's global instance answered %d", resp.StatusCode)
		}
		e.Status = resp.StatusCode
		return 0, nil, &e
	}
	return resp.StatusCode, bytes.TrimSpace(raw), nil
}

// --- a person's read access ---------------------------------------------------------------

type accessRec struct {
	Perm string `json:"perm"` // admin | maintain | write | triage | read | none
	At   int64  `json:"at"`
}

func accessKey(login, repo string) string {
	return "access/" + strings.ToLower(login) + "/" + strings.ToLower(repo)
}

// personRead is what a GitHub login may do on a repo, as the bot sees it
// (the collaborator permission API with the tile's installation token):
// cached an hour unless fresh. A repo the installation can't see is 404
// not-found.
func (s *srv) personRead(ctx context.Context, login, repo string, fresh bool) (string, error) {
	key := accessKey(login, repo)
	var rec accessRec
	if !fresh && s.state.Get(key, &rec) == nil && rec.Perm != "" && s.now().UnixMilli()-rec.At < accessCacheFor.Milliseconds() {
		return rec.Perm, nil
	}
	auth, err := s.instAuth(ctx, ownerOf(repo), nameOf(repo), "read")
	if err != nil {
		return "", err
	}
	var out struct {
		Permission string `json:"permission"`
	}
	_, err = s.gh.call(ctx, auth, http.MethodGet, s.repoBase(repo)+"/collaborators/"+pathEsc(login)+"/permission", nil, &out)
	if err != nil {
		return "", err
	}
	perm := cleanWord(out.Permission)
	if perm == "" {
		perm = "none"
	}
	_ = s.state.Put(key, accessRec{Perm: perm, At: s.now().UnixMilli()})
	return perm, nil
}

// dropAccess forgets cached access: a login's (on an account, or
// everywhere), or everyone's on a repo.
func (s *srv) dropAccess(login, account, repo string) {
	keys, err := s.state.List("access/")
	if err != nil {
		return
	}
	login, account, repo = strings.ToLower(login), strings.ToLower(account), strings.ToLower(repo)
	for _, k := range keys {
		rest := strings.TrimPrefix(k, "access/")
		l, rp, _ := strings.Cut(rest, "/")
		if (login == "" || l == login) && (account == "" || ownerOf(rp) == account) && (repo == "" || rp == repo) {
			_ = s.state.Delete(k)
		}
	}
}

// dropPersonSubs deletes a person's subscriptions on a repo (they lost
// read access to it). h.mu held.
func (h *hub) dropPersonSubs(person, repo string) {
	for id, sub := range h.subs {
		if sub.Person == person && strings.EqualFold(sub.Repo, repo) {
			h.dropSub(id)
		}
	}
}
