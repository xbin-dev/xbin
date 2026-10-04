// bot.go — the App's bot tokens: installation tokens narrowed to a
// consumer's repos and permissions, reused per consumer and purpose,
// recorded until they expire and revoked by value, by purpose or all at
// once; plus the small caches the routes share.
package main

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// cacheKey is one handed-out token's identity: who asked, for what, which
// installation, which repos, which permissions.
type cacheKey struct {
	consumer string
	purpose  string
	inst     int64
	repos    string
	perms    string
	kind     string // bot | person
}

type cachedToken struct {
	token     secretString
	expiresAt time.Time
	perms     map[string]string
	repos     []string
	resp      *tokenResp // what was answered (person tokens: identity, author)
}

// tokenCache keeps two things apart. live is every token handed out, by
// its hash, until it expires or is revoked: the record revocation (by
// value, by purpose, all) walks, so nothing still alive at GitHub is ever
// dropped from it. reuse is an LRU index (≤ max) of the tokens that may be
// handed out again; a policy change, a new App or a person's new epoch
// empties it (never reuse) without touching live (never forget).
type tokenCache struct {
	mu    sync.Mutex
	max   int
	live  map[string]*cacheEntry // token hash → entry
	reuse map[cacheKey]*list.Element
	order *list.List // of *reuseEntry, most recent first
}

type cacheEntry struct {
	key cacheKey
	tok *cachedToken
}

type reuseEntry struct {
	key  cacheKey
	hash string
}

// liveCap bounds the live record: past it (every entry unexpired) a new
// token is refused rather than an old one forgotten.
func (c *tokenCache) liveCap() int { return 4 * c.max }

func newTokenCache(max int) *tokenCache {
	return &tokenCache{max: max, live: map[string]*cacheEntry{}, reuse: map[cacheKey]*list.Element{}, order: list.New()}
}

func tokenHash(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// get answers a reusable token with at least margin left to run.
func (c *tokenCache) get(k cacheKey, now time.Time, margin time.Duration) *cachedToken {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.reuse[k]
	if !ok {
		return nil
	}
	e, ok := c.live[el.Value.(*reuseEntry).hash]
	if !ok || e.tok.expiresAt.Sub(now) < margin {
		return nil
	}
	c.order.MoveToFront(el)
	return e.tok
}

// room says whether another token may be handed out: the live record has
// room once expired entries are pruned. Asked before minting.
func (c *tokenCache) room(now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.live) < c.liveCap() {
		return nil
	}
	c.pruneLocked(now)
	if len(c.live) < c.liveCap() {
		return nil
	}
	e := refuse(refLimit, "too many live tokens: revoke some, or wait for them to expire")
	e.RetryAfterMs = 60_000
	return e
}

func (c *tokenCache) pruneLocked(now time.Time) {
	for h, e := range c.live {
		if !e.tok.expiresAt.After(now) {
			delete(c.live, h)
			if el, ok := c.reuse[e.key]; ok && el.Value.(*reuseEntry).hash == h {
				c.order.Remove(el)
				delete(c.reuse, e.key)
			}
		}
	}
}

// put records a token handed out (live) and makes it reusable under k.
// The LRU's eviction only ends reuse: the token stays live.
func (c *tokenCache) put(k cacheKey, t *cachedToken) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := tokenHash(t.token.Reveal())
	c.live[h] = &cacheEntry{k, t}
	if el, ok := c.reuse[k]; ok {
		el.Value = &reuseEntry{k, h}
		c.order.MoveToFront(el)
	} else {
		c.reuse[k] = c.order.PushFront(&reuseEntry{k, h})
	}
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.reuse, last.Value.(*reuseEntry).key)
	}
	if len(c.live) > c.liveCap() {
		c.pruneLocked(time.Now())
	}
}

// byValue finds a token handed out to consumer by its value: its key, and
// whether it was.
func (c *tokenCache) byValue(consumer, token string) (cacheKey, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.live[tokenHash(token)]
	if !ok || e.key.consumer != consumer {
		return cacheKey{}, false
	}
	return e.key, true
}

// forgetValue drops a token by its value (revoked).
func (c *tokenCache) forgetValue(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := tokenHash(token)
	if e, ok := c.live[h]; ok {
		if el, ok := c.reuse[e.key]; ok && el.Value.(*reuseEntry).hash == h {
			c.order.Remove(el)
			delete(c.reuse, e.key)
		}
		delete(c.live, h)
	}
}

// take removes and answers the live tokens matching f (to be revoked).
func (c *tokenCache) take(now time.Time, f func(cacheKey) bool) []*cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*cacheEntry
	for h, e := range c.live {
		if !f(e.key) {
			continue
		}
		delete(c.live, h)
		if el, ok := c.reuse[e.key]; ok && el.Value.(*reuseEntry).hash == h {
			c.order.Remove(el)
			delete(c.reuse, e.key)
		}
		if e.tok.expiresAt.After(now) {
			out = append(out, e)
		}
	}
	return out
}

// retire ends reuse of the tokens matching f; they stay live, so a revoke
// still reaches them.
func (c *tokenCache) retire(f func(cacheKey) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, el := range c.reuse {
		if f(k) {
			c.order.Remove(el)
			delete(c.reuse, k)
		}
	}
}

// clear ends reuse of every token (a policy change, a new App); none is
// forgotten.
func (c *tokenCache) clear() { c.retire(func(cacheKey) bool { return true }) }

// len answers how many tokens may be reused.
func (c *tokenCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// tokenMargin is how long a cached token must still run to be handed out
// again: the consumer's minimum, and never under 15 minutes.
func tokenMargin(minTTL int) time.Duration {
	return max(time.Duration(minTTL)*time.Second, 15*time.Minute)
}

// botToken mints (or reuses) a bot token for consumer. The whole policy
// applies whoever asks: allowedAccounts, botRepos, the preset and
// allowWorkflows.
func (s *srv) botToken(ctx context.Context, consumer string, req *normReq) (*tokenResp, error) {
	return s.botTokenReuse(ctx, consumer, req, true)
}

// botTokenReuse is botToken; reuse false mints a fresh token every time
// (still cached, so revoke-all reaches it): a person's partition caches
// relayed bot tokens itself and revokes them there, so global must never
// hand one of them out again.
func (s *srv) botTokenReuse(ctx context.Context, consumer string, req *normReq, reuse bool) (*tokenResp, error) {
	a, err := s.mustApp()
	if err != nil {
		return nil, err
	}
	pol := s.policy()
	if err := pol.checkBotRepos(req.repos); err != nil {
		return nil, err
	}
	perms, err := narrow(req.access, req.perms, pol.AllowWorkflows)
	if err != nil {
		return nil, err
	}
	owner := ownerOf(req.repos[0])
	inst, err := s.installation(ctx, owner, nameOf(req.repos[0]))
	if err != nil {
		return nil, err
	}
	key := cacheKey{consumer: consumer, purpose: req.purpose, inst: inst, repos: strings.Join(req.repos, ","), perms: permsKey(perms), kind: asBot}
	if !reuse {
		key.purpose += "#" + randomID(9)
	}
	now := s.now()
	if t := s.bot.get(key, now, tokenMargin(req.minTTL)); reuse && t != nil {
		return s.botResp(a, t), nil
	}
	if err := s.bot.room(now); err != nil {
		return nil, err
	}
	names := make([]string, len(req.repos))
	for i, r := range req.repos {
		names[i] = nameOf(r)
	}
	m, err := s.mint(ctx, inst, names, perms)
	if err != nil {
		return nil, err
	}
	if m.ExpiresAt.Sub(now) < tokenMargin(req.minTTL) {
		return nil, refuse(refInvalid, "GitHub's installation tokens live an hour: minTtlSec %d can't be met", req.minTTL)
	}
	got := m.Permissions
	if len(got) == 0 {
		got = perms
	}
	t := &cachedToken{token: m.Token, expiresAt: m.ExpiresAt, perms: got, repos: req.repos}
	s.bot.put(key, t)
	return s.botResp(a, t), nil
}

func (s *srv) botResp(a *appState, t *cachedToken) *tokenResp {
	host, _, _ := s.hosts()
	login := a.Slug + "[bot]"
	r := &tokenResp{
		Host: host, Username: "x-access-token", Token: t.token,
		ExpiresAt: t.expiresAt.UnixMilli(), RefreshAfter: t.expiresAt.Add(-10 * time.Minute).UnixMilli(),
		Identity: identity{Kind: asBot, Login: login, ID: a.BotID},
		Author:   author{Name: login, Email: fmt.Sprintf("%d+%s@users.noreply.%s", a.BotID, login, host)},
		Repos:    append([]string(nil), t.repos...), Permissions: t.perms,
	}
	if a.BotID == 0 {
		r.Author.Email = login + "@users.noreply." + host
	}
	return r
}

// revokeBotToken asks GitHub to revoke an installation token (best effort:
// a token GitHub can't revoke still dies within its hour).
func (s *srv) revokeBotToken(ctx context.Context, tok secretString) {
	_, _ = s.gh.do(ctx, bearerAuth("", tok), http.MethodDelete, s.apiBase()+"/installation/token", nil)
}

// handleRevokeAll revokes every live bot token handed out — reusable or
// not (a policy change only ends reuse) — and the tile's own (a manager's
// "Revoke all bot tokens"): consumers ask again and get fresh ones.
func (s *srv) handleRevokeAll(w http.ResponseWriter, r *http.Request, _ who) {
	gone := s.bot.take(s.now(), func(k cacheKey) bool { return k.kind == asBot })
	for _, e := range gone {
		s.revokeBotToken(r.Context(), e.tok.token)
	}
	for _, e := range s.intl.take(s.now(), func(cacheKey) bool { return true }) {
		s.revokeBotToken(r.Context(), e.tok.token)
	}
	writeJSON(w, http.StatusOK, map[string]int{"revoked": len(gone)})
}

// clientIDs remembers POST /scm/pulls clientIds: a repeat answers the same
// pull; the same id for a different request is 409 exists.
type clientIDs struct {
	mu sync.Mutex
	m  map[string]clientIDEntry
}

type clientIDEntry struct {
	req    string
	repo   string
	number int
	at     time.Time
}

func newClientIDs() *clientIDs { return &clientIDs{m: map[string]clientIDEntry{}} }

func (c *clientIDs) lookup(key, req string) (clientIDEntry, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return e, false, nil
	}
	if e.req != req {
		return e, true, refuse(refExists, "that clientId was used for a different pull request")
	}
	return e, true, nil
}

func (c *clientIDs) remember(key, req, repo string, n int, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 5000 {
		for k, e := range c.m {
			if now.Sub(e.at) > 24*time.Hour {
				delete(c.m, k)
			}
		}
	}
	c.m[key] = clientIDEntry{req: req, repo: repo, number: n, at: now}
}

// shortCache keeps answers briefly (checks 5 s, repo lists 5 min).
type shortCache struct {
	mu  sync.Mutex
	max int
	m   map[string]shortEntry
}

type shortEntry struct {
	v  any
	at time.Time
}

func newShortCache(max int) *shortCache { return &shortCache{max: max, m: map[string]shortEntry{}} }

func (c *shortCache) get(k string, now time.Time, ttl time.Duration) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || now.Sub(e.at) >= ttl || now.Before(e.at) {
		return nil, false
	}
	return e.v, true
}

func (c *shortCache) put(k string, v any, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.max {
		var oldK string
		var old time.Time
		for kk, e := range c.m {
			if oldK == "" || e.at.Before(old) {
				oldK, old = kk, e.at
			}
		}
		delete(c.m, oldK)
	}
	c.m[k] = shortEntry{v, now}
}

func (c *shortCache) clear() {
	c.mu.Lock()
	c.m = map[string]shortEntry{}
	c.mu.Unlock()
}

// sortedRepos lowercases nothing (GitHub's names are case-insensitive but
// kept as asked) and sorts for a stable cache key.
func sortedRepos(r []string) []string {
	out := append([]string(nil), r...)
	sort.Strings(out)
	return out
}
