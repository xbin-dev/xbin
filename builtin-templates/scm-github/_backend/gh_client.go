// gh_client.go — GitHub's REST and GraphQL API as this tile calls it: the
// API version header, conditional GETs from an ETag LRU (a 304 doesn't
// count against GitHub's rate limit), the rate limit tracked per identity,
// Link pagination, and GitHub's errors mapped to the contract's refusals.
package main

import (
	"bytes"
	"container/list"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ghAuth is how one call authenticates; key names the identity for the
// rate limit and the ETag cache (GitHub's ETags vary by Authorization, and
// tokens rotate: the key is the identity, not the token).
type ghAuth struct {
	key    string       // "app", "inst:<id>", "user:<login>", "basic", "" (anonymous)
	bearer secretString // a JWT or a token
	user   string       // basic auth: the client id
	pass   secretString // basic auth: the client secret
}

func bearerAuth(key string, tok secretString) ghAuth { return ghAuth{key: key, bearer: tok} }

type ghResp struct {
	Status int
	Header http.Header
	Body   []byte
}

type etagEntry struct {
	key  string
	etag string
	body []byte
}

type ghClient struct {
	hc  *http.Client
	now func() time.Time

	mu      sync.Mutex
	etags   map[string]*list.Element
	order   *list.List // front = most recent
	maxTags int
	blocked map[string]time.Time // rate limit: identity key|resource → when it resets
}

func newGHClient(hc *http.Client, now func() time.Time) *ghClient {
	return &ghClient{hc: hc, now: now, etags: map[string]*list.Element{}, order: list.New(), maxTags: 2000, blocked: map[string]time.Time{}}
}

const maxJSONBody = 16 << 20

// do makes one call and answers GitHub's response, whatever its status
// (a 304 to a conditional GET answers the cached body with status 200).
// The error is a refusal: the rate limit already hit, or no answer.
func (g *ghClient) do(ctx context.Context, a ghAuth, method, url string, body any) (*ghResp, error) {
	res := rateResource(url)
	if until, ok := g.blockedUntil(a.key, res); ok {
		e := refuse(refLimit, "GitHub's rate limit for this identity is spent until %s", until.UTC().Format(time.RFC3339))
		e.RetryAfterMs = until.Sub(g.now()).Milliseconds()
		return nil, e
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, refuse(refInvalid, "a bad upstream address")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "xbin-scm-github")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch {
	case !a.bearer.Empty():
		req.Header.Set("Authorization", "Bearer "+a.bearer.Reveal())
	case a.user != "":
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(a.user+":"+a.pass.Reveal())))
	}
	tagKey := a.key + " " + url
	var cached *etagEntry
	if method == http.MethodGet && a.key != "" {
		if cached = g.etag(tagKey); cached != nil {
			req.Header.Set("If-None-Match", cached.etag)
		}
	}
	release := upstreamSlot(ctx)
	resp, err := g.hc.Do(req)
	release()
	if err != nil {
		e := refuse(refUnavailable, "GitHub didn't answer (%s): %s", req.URL.Host, clip(scrubURLs(err.Error()), 200))
		e.RetryAfterMs = 5000
		return nil, e
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody))
	if err != nil {
		e := refuse(refUnavailable, "GitHub's answer broke off")
		e.RetryAfterMs = 5000
		return nil, e
	}
	g.trackRate(a.key, res, resp, b)
	if resp.StatusCode == http.StatusNotModified && cached != nil {
		g.touch(tagKey)
		h := resp.Header.Clone()
		h.Set("ETag", cached.etag)
		return &ghResp{Status: http.StatusOK, Header: h, Body: cached.body}, nil
	}
	if method == http.MethodGet && a.key != "" && resp.StatusCode == http.StatusOK {
		if et := resp.Header.Get("ETag"); et != "" {
			g.putETag(tagKey, et, b)
		}
	}
	return &ghResp{Status: resp.StatusCode, Header: resp.Header, Body: b}, nil
}

// form posts a form to GitHub's web endpoints (the device flow, OAuth),
// asking for a JSON answer. Never conditional, never rate-tracked.
func (g *ghClient) form(ctx context.Context, u string, form url.Values) (*ghResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, refuse(refInvalid, "a bad upstream address")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "xbin-scm-github")
	resp, err := g.hc.Do(req)
	if err != nil {
		e := refuse(refUnavailable, "GitHub didn't answer (%s)", req.URL.Host)
		e.RetryAfterMs = 5000
		return nil, e
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, refuse(refUnavailable, "GitHub's answer broke off")
	}
	return &ghResp{Status: resp.StatusCode, Header: resp.Header, Body: b}, nil
}

// call is do with GitHub's errors mapped to refusals and a 2xx answer
// decoded into out (nil: ignored).
func (g *ghClient) call(ctx context.Context, a ghAuth, method, url string, body, out any) (*ghResp, error) {
	r, err := g.do(ctx, a, method, url, body)
	if err != nil {
		return nil, err
	}
	if r.Status >= 300 {
		return r, ghError(r, g.now())
	}
	if out != nil && len(r.Body) > 0 {
		if err := json.Unmarshal(r.Body, out); err != nil {
			return r, refuse(refUpstream, "GitHub's answer wasn't the JSON expected")
		}
	}
	return r, nil
}

// ghError maps a GitHub error answer to a refusal. The message is
// GitHub's own (never a user's text), clipped.
func ghError(r *ghResp, now time.Time) *scmErr {
	var m struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(r.Body, &m)
	msg := clip(m.Message, 300)
	if msg == "" {
		msg = http.StatusText(r.Status)
	}
	retry := retryAfter(r.Header, now)
	switch {
	case r.Status == http.StatusForbidden && r.Header.Get("X-GitHub-SSO") != "":
		e := refuse(refNotAllowed, "the organization requires SAML single sign-on for this identity: authorize it at GitHub")
		e.SSO = &ssoInfo{URL: ssoURL(r.Header.Get("X-GitHub-SSO"))}
		return e
	case r.Status == http.StatusTooManyRequests ||
		(r.Status == http.StatusForbidden && (r.Header.Get("X-RateLimit-Remaining") == "0" || r.Header.Get("Retry-After") != "" || strings.Contains(strings.ToLower(msg), "rate limit"))):
		e := refuse(refLimit, "GitHub's rate limit: %s", msg)
		e.RetryAfterMs = max(retry, 1000)
		return e
	case r.Status == http.StatusForbidden:
		return refuse(refNotAllowed, "GitHub refused: %s", msg)
	case r.Status == http.StatusNotFound || r.Status == http.StatusGone:
		return refuse(refNotFound, "GitHub has no such thing for this identity (%s)", msg)
	case r.Status == http.StatusConflict:
		return refuse(refExists, "GitHub: %s", msg)
	case r.Status == http.StatusPreconditionFailed:
		return refuse(refPrecondition, "GitHub: %s", msg)
	case r.Status == http.StatusUnprocessableEntity:
		return refuse(refInvalid, "GitHub refused the request: %s", msg)
	case r.Status >= 500:
		e := refuse(refUnavailable, "GitHub is having trouble (%d): %s", r.Status, msg)
		e.RetryAfterMs = max(retry, 5000)
		return e
	}
	e := refuse(refUpstream, "GitHub answered %d: %s", r.Status, msg)
	e.Upstream = &upstreamErr{Status: r.Status, Message: msg}
	return e
}

// retryAfter reads Retry-After (seconds) or X-RateLimit-Reset (unix s).
func retryAfter(h http.Header, now time.Time) int64 {
	if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil && s > 0 {
		return int64(s) * 1000
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			if d := time.Unix(reset, 0).Sub(now); d > 0 {
				return d.Milliseconds()
			}
		}
	}
	return 0
}

// ssoURL takes the url out of "required; url=https://…".
func ssoURL(h string) string {
	for _, part := range strings.Split(h, ";") {
		if u, ok := strings.CutPrefix(strings.TrimSpace(part), "url="); ok {
			return u
		}
	}
	return ""
}

// rateResource is the rate limit a call spends: GitHub keeps search's and
// GraphQL's apart from the core REST one (X-RateLimit-Resource), so one
// spent doesn't block the others. It reads the path from the API's root
// (GitHub.com's, or a GHES's /api/v3 and /api/graphql), never anywhere in
// it: a repo or owner named "search" is core.
func rateResource(u string) string {
	p := u
	if i := strings.Index(p, "://"); i >= 0 {
		p = p[i+3:]
	}
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if i := strings.IndexByte(p, '/'); i >= 0 {
		p = p[i:] // past the host
	} else {
		p = "/"
	}
	if p == "/api/graphql" {
		return "graphql"
	}
	p = strings.TrimPrefix(p, "/api/v3")
	switch {
	case p == "/graphql":
		return "graphql"
	case strings.HasPrefix(p, "/search/"):
		return "search"
	}
	return "core"
}

// secondaryWait is how long a secondary rate limit blocks an identity
// when GitHub doesn't say (Retry-After): GitHub asks for at least a minute.
const secondaryWait = time.Minute

// trackRate keeps an identity's spent limit: the primary one
// (X-RateLimit-Remaining 0, until its reset), or a secondary one (429, or
// a 403 with Retry-After or naming it) for its Retry-After — every caller
// of that identity waits, as GitHub asks, not only the one refused.
func (g *ghClient) trackRate(key, res string, r *http.Response, body []byte) {
	if key == "" {
		return
	}
	if h := r.Header.Get("X-RateLimit-Resource"); h != "" {
		res = h
	}
	var until time.Time
	switch {
	case r.Header.Get("X-RateLimit-Remaining") == "0":
		reset, err := strconv.ParseInt(r.Header.Get("X-RateLimit-Reset"), 10, 64)
		if err != nil {
			return
		}
		until = time.Unix(reset, 0)
	case r.StatusCode == http.StatusTooManyRequests ||
		(r.StatusCode == http.StatusForbidden && (r.Header.Get("Retry-After") != "" || strings.Contains(strings.ToLower(string(body)), "secondary rate limit"))):
		wait := secondaryWait
		if s, err := strconv.Atoi(r.Header.Get("Retry-After")); err == nil && s > 0 {
			wait = time.Duration(min(s, 3600)) * time.Second
		}
		until = g.now().Add(wait)
	default:
		return
	}
	g.mu.Lock()
	g.blocked[key+"|"+res] = until
	g.mu.Unlock()
}

func (g *ghClient) blockedUntil(key, res string) (time.Time, bool) {
	if key == "" {
		return time.Time{}, false
	}
	key += "|" + res
	g.mu.Lock()
	defer g.mu.Unlock()
	until, ok := g.blocked[key]
	if !ok {
		return time.Time{}, false
	}
	if !g.now().Before(until) {
		delete(g.blocked, key)
		return time.Time{}, false
	}
	return until, true
}

func (g *ghClient) etag(key string) *etagEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	if el, ok := g.etags[key]; ok {
		return el.Value.(*etagEntry)
	}
	return nil
}

func (g *ghClient) touch(key string) {
	g.mu.Lock()
	if el, ok := g.etags[key]; ok {
		g.order.MoveToFront(el)
	}
	g.mu.Unlock()
}

func (g *ghClient) putETag(key, etag string, body []byte) {
	if len(body) > 1<<20 {
		return // a big answer isn't worth keeping twice
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if el, ok := g.etags[key]; ok {
		el.Value = &etagEntry{key, etag, body}
		g.order.MoveToFront(el)
		return
	}
	g.etags[key] = g.order.PushFront(&etagEntry{key, etag, body})
	for g.order.Len() > g.maxTags {
		last := g.order.Back()
		g.order.Remove(last)
		delete(g.etags, last.Value.(*etagEntry).key)
	}
}

// forget drops every cached answer of an identity (a token revoked).
func (g *ghClient) forget(identityKey string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, el := range g.etags {
		if strings.HasPrefix(k, identityKey+" ") {
			g.order.Remove(el)
			delete(g.etags, k)
		}
	}
}

var linkNext = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// nextLink is the Link header's rel="next" URL, "" on the last page.
func nextLink(h http.Header) string {
	if m := linkNext.FindStringSubmatch(h.Get("Link")); m != nil {
		return m[1]
	}
	return ""
}

// getAll follows Link pagination, decoding each page's array (or, with
// field set, the array under that key) into out, up to max items.
func getAll[T any](ctx context.Context, g *ghClient, a ghAuth, url, field string, maxItems int) ([]T, error) {
	var all []T
	for url != "" && len(all) < maxItems {
		r, err := g.call(ctx, a, http.MethodGet, url, nil, nil)
		if err != nil {
			return nil, err
		}
		var items []T
		if field == "" {
			err = json.Unmarshal(r.Body, &items)
		} else {
			var wrap map[string]json.RawMessage
			if err = json.Unmarshal(r.Body, &wrap); err == nil {
				err = json.Unmarshal(wrap[field], &items)
			}
		}
		if err != nil {
			return nil, refuse(refUpstream, "GitHub's list wasn't the JSON expected")
		}
		all = append(all, items...)
		url = nextLink(r.Header)
	}
	if len(all) > maxItems {
		all = all[:maxItems]
	}
	return all, nil
}

// graphqlURL is GitHub's GraphQL endpoint: api.github.com/graphql, and on
// a GitHub Enterprise Server https://<host>/api/graphql, beside (not under)
// the REST API's /api/v3.
func graphqlURL(apiBase string) string {
	if b, ok := strings.CutSuffix(apiBase, "/api/v3"); ok {
		return b + "/api/graphql"
	}
	return apiBase + "/graphql"
}

// graphql runs one GraphQL operation; GitHub's errors become refusals.
func (g *ghClient) graphql(ctx context.Context, a ghAuth, apiBase, query string, vars map[string]any, out any) error {
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, err := g.call(ctx, a, http.MethodPost, graphqlURL(apiBase), map[string]any{"query": query, "variables": vars}, &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		e := resp.Errors[0]
		switch e.Type {
		case "NOT_FOUND":
			return refuse(refNotFound, "GitHub: %s", clip(e.Message, 300))
		case "FORBIDDEN":
			return refuse(refNotAllowed, "GitHub: %s", clip(e.Message, 300))
		}
		return refuse(refUpstream, "GitHub's GraphQL API: %s", clip(e.Message, 300))
	}
	if out != nil && len(resp.Data) > 0 {
		return json.Unmarshal(resp.Data, out)
	}
	return nil
}

var urlQuery = regexp.MustCompile(`\?[^\s"]*`)

// scrubURLs drops query strings from a transport error (a log URL's
// signature, say) before it reaches a message.
func scrubURLs(s string) string { return urlQuery.ReplaceAllString(s, "?…") }

// pathEsc escapes one path segment of a GitHub URL (a branch may hold "/").
func pathEsc(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
