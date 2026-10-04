// scm.go — the scm client (API.md §scm providers and credentials): the
// providers bound to this agent's `scm` slot (multi, service "scm";
// docs/scm.md is the contract, protocol 1), each called through the xbin
// gateway as an scmAPI (scm_types.go).
//
// Who is asking is the gateway's to say, never ours: xbind sets X-XBin-From
// (this tile) on every call and, from a person's partition, the partition
// headers — so there the provider sees that person and answers from its own
// partition for them (their sign-in, their tokens), and from the global
// instance or an unpartitioned agent it sees a tile (the bot only). The
// client sends no identity header of its own and never asks `as: person`
// from a home without a person (scm_gate.go decides the identity).
//
// A provider's refusal comes back as an *scmError with its HTTP status and
// the contract's refusal (a 5xx with no body: unavailable). Hellos are
// cached 60 s (10 s after a failure), like the sandbox managers' (sandboxes.go).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const (
	scmCallTimeout  = 30 * time.Second
	scmHelloTimeout = 10 * time.Second
	scmJSONMax      = 16 << 20 // an answer (a job log is ≤ 1 MiB of text)
)

// scmEndpoint is one bound provider.
type scmEndpoint struct {
	Provider string // the provider tile ("apps/scm-github"; "apps/scm-github#eu" for an instance): Project.SCM
	URL      string // its base, no trailing slash (the contract's routes are under <URL>/scm/)
}

// scmClient reaches the providers (through the xbin gateway); tests point it
// at plain HTTP servers with setSCMClient.
var (
	scmClientMu sync.RWMutex
	scmClientFn = xbin.Client
)

func scmHTTP() *http.Client {
	scmClientMu.RLock()
	f := scmClientFn
	scmClientMu.RUnlock()
	return f()
}

// setSCMClient points the client at f and returns what it was (tests).
func setSCMClient(f func() *http.Client) (old func() *http.Client) {
	scmClientMu.Lock()
	defer scmClientMu.Unlock()
	old, scmClientFn = scmClientFn, f
	return old
}

// scmEndpoints reads the `scm` slot's bindings from the runner-injected env
// (a rebinding restarts the backend). Nothing bound: none.
func scmEndpoints() []scmEndpoint {
	var eps []struct {
		Provider string `json:"provider"`
		Instance string `json:"instance"`
		URL      string `json:"url"`
		Service  string `json:"service"`
	}
	if raw := os.Getenv("XBIN_IFACE_SCM"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &eps)
	}
	out := make([]scmEndpoint, 0, len(eps))
	seen := map[string]bool{}
	for _, e := range eps {
		if e.URL == "" || e.Provider == "" || (e.Service != "" && e.Service != "scm") {
			continue
		}
		name := e.Provider
		if e.Instance != "" {
			name += "#" + e.Instance
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, scmEndpoint{Provider: name, URL: strings.TrimRight(e.URL, "/")})
	}
	return out
}

func init() {
	scmBound = func() []string {
		var out []string
		for _, e := range scmEndpoints() {
			out = append(out, e.Provider)
		}
		return out
	}
	scmFor = func(provider string) (scmAPI, error) {
		for _, e := range scmEndpoints() {
			if e.Provider == provider {
				return &scmConn{E: e}, nil
			}
		}
		return nil, errScmUnbound
	}
}

// scmOnly is the provider a request means: the one named, or the only one
// bound when none is named.
func scmOnly(name string) (scmAPI, error) {
	if name != "" {
		return scmFor(name)
	}
	b := scmBound()
	switch len(b) {
	case 0:
		return nil, errScmUnbound
	case 1:
		return scmFor(b[0])
	}
	return nil, &scmError{Status: 400, Refusal: scmRefInvalid, Message: "several scm providers are bound: name one (scm=" + strings.Join(b, ", ") + ")"}
}

// scmConn is one bound provider, called as this home.
type scmConn struct{ E scmEndpoint }

func (c *scmConn) Provider() string { return c.E.Provider }

// --- errors ---------------------------------------------------------------------------

// scmStatusRefusal names the refusal a status means when the body didn't say
// (xbind's own answers: a missing grant, a stopped backend).
func scmStatusRefusal(status int) string {
	switch status {
	case http.StatusBadRequest:
		return scmRefInvalid
	case http.StatusUnauthorized, http.StatusForbidden:
		return scmRefNotAllowed
	case http.StatusNotFound:
		return scmRefNotFound
	case http.StatusConflict:
		return scmRefExists
	case http.StatusPreconditionFailed:
		return scmRefPrecondition
	case http.StatusTooManyRequests:
		return scmRefLimit
	case http.StatusNotImplemented:
		return scmRefUnsupported
	case http.StatusBadGateway:
		return scmRefUpstream
	}
	if status >= 500 {
		return scmRefUnavailable
	}
	return scmRefInvalid
}

// decodeSCMError reads a refusal: the contract's body, or the status alone
// (a 5xx without a body is unavailable).
func decodeSCMError(provider string, resp *http.Response) *scmError {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	e := &scmError{}
	if len(bytes.TrimSpace(raw)) > 0 && json.Unmarshal(raw, e) != nil {
		e = &scmError{} // not the contract's body
	}
	e.Status = resp.StatusCode
	if e.Refusal == "" {
		e.Refusal = scmStatusRefusal(resp.StatusCode)
	}
	if e.Message == "" {
		e.Message = fmt.Sprintf("%s answered %d", provider, resp.StatusCode)
	}
	if e.RetryAfterMs == 0 {
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			e.RetryAfterMs = int64(s) * 1000
		}
	}
	return e
}

// --- plumbing -------------------------------------------------------------------------

// do sends one request: in (nil: no body) as JSON, the answer decoded into
// out (nil: dropped). A 304 answers notModified true; anything but 2xx is an
// *scmError. The ETag header fills an answer's etag when its body has none.
func (c *scmConn) do(ctx context.Context, method, route string, q url.Values, in, out any, timeout time.Duration) (notModified bool, err error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	u := c.E.URL + "/scm" + route
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return false, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return false, &scmError{Refusal: scmRefInvalid, Message: err.Error()}
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := scmHTTP().Do(req)
	if err != nil {
		if ctx.Err() != nil && context.Cause(ctx) != context.DeadlineExceeded {
			return false, context.Cause(ctx)
		}
		return false, &scmError{Status: 0, Refusal: scmRefUnavailable, Message: fmt.Sprintf("%s didn't answer: %v", c.E.Provider, err)}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return true, nil
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return false, decodeSCMError(c.E.Provider, resp)
	case out == nil || resp.StatusCode == http.StatusNoContent:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return false, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, scmJSONMax)).Decode(out); err != nil {
		return false, &scmError{Status: resp.StatusCode, Refusal: scmRefUnavailable, Message: c.E.Provider + " sent a garbled answer"}
	}
	if et := resp.Header.Get("ETag"); et != "" {
		scmSetETag(out, et)
	}
	return false, nil
}

// setETag fills an answer's etag from the header when the body had none.
func scmSetETag(out any, et string) {
	switch v := out.(type) {
	case *scmChecks:
		v.ETag = orStr(v.ETag, et)
	case *scmPull:
		v.ETag = orStr(v.ETag, et)
	case *scmRepo:
		v.ETag = orStr(v.ETag, et)
	case *scmIssue:
		v.ETag = orStr(v.ETag, et)
	}
}

// vals builds a query, leaving out empty values.
func scmVals(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			v.Set(kv[i], kv[i+1])
		}
	}
	return v
}

func scmItoa(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func scmI64(n int64) string {
	if n == 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}

// queryOf is a list's query string.
func scmQueryOf(q scmQuery) url.Values {
	v := scmVals("repo", q.Repo, "q", q.Q, "as", q.As, "state", q.State, "head", q.Head, "base", q.Base,
		"since", scmI64(q.Since), "limit", scmItoa(q.Limit), "cursor", q.Cursor, "ifNoneMatch", q.IfNoneMatch)
	if len(q.Labels) > 0 {
		v.Set("labels", strings.Join(q.Labels, ","))
	}
	return v
}

// --- hello ----------------------------------------------------------------------------

type scmHelloEntry struct {
	h   *scmHello
	err error
	at  time.Time
}

var (
	scmHelloMu      sync.Mutex
	scmHelloCache   = map[string]scmHelloEntry{}
	scmHelloTTL     = 60 * time.Second
	scmHelloFailTTL = 10 * time.Second
)

// forgetSCMHellos drops every cached hello (tests; a rebinding restarts us).
func forgetSCMHellos() {
	scmHelloMu.Lock()
	scmHelloCache = map[string]scmHelloEntry{}
	scmHelloMu.Unlock()
}

// Hello negotiates protocol 1, cached. A provider that doesn't speak it, or
// doesn't offer credentials (the one capability the contract requires), is
// refused with the reason.
func (c *scmConn) Hello(ctx context.Context) (*scmHello, error) {
	key := c.E.Provider + "\x00" + c.E.URL
	scmHelloMu.Lock()
	if e, ok := scmHelloCache[key]; ok {
		ttl := scmHelloTTL
		if e.err != nil {
			ttl = scmHelloFailTTL
		}
		if time.Since(e.at) < ttl {
			scmHelloMu.Unlock()
			return e.h, e.err
		}
	}
	scmHelloMu.Unlock()
	h, err := c.fetchHello(ctx)
	if err != nil && ctx.Err() != nil {
		return nil, err // the caller gave up: nothing learned
	}
	scmHelloMu.Lock()
	scmHelloCache[key] = scmHelloEntry{h: h, err: err, at: time.Now()}
	scmHelloMu.Unlock()
	return h, err
}

func (c *scmConn) fetchHello(ctx context.Context) (*scmHello, error) {
	var h scmHello
	if _, err := c.do(ctx, "GET", "/hello", scmVals("protocol", strconv.Itoa(scmProtocol)), nil, &h, scmHelloTimeout); err != nil {
		return nil, err
	}
	switch {
	case h.Protocol != scmProtocol:
		return nil, &scmError{Status: 400, Refusal: scmRefProtocol, Protocols: h.Protocols,
			Message: fmt.Sprintf("%s speaks scm protocol %d; this agent speaks %d", c.E.Provider, h.Protocol, scmProtocol)}
	case !h.has(scmCapCredentials):
		return nil, &scmError{Status: 501, Refusal: scmRefUnsupported,
			Message: c.E.Provider + " doesn't offer credentials, which the scm contract requires"}
	}
	return &h, nil
}

// --- credentials ----------------------------------------------------------------------

func (c *scmConn) Token(ctx context.Context, req scmTokenReq) (*scmToken, error) {
	var out scmToken
	if _, err := c.do(ctx, "POST", "/token", nil, req, &out, scmCallTimeout); err != nil {
		return nil, err
	}
	if out.Token.Empty() {
		return nil, &scmError{Status: 502, Refusal: scmRefUnavailable, Message: c.E.Provider + " answered a token request without a token"}
	}
	return &out, nil
}

// Revoke sends {token} (revealed here only) or {purpose}.
func (c *scmConn) Revoke(ctx context.Context, req scmRevokeReq) error {
	body := map[string]string{}
	if !req.Token.Empty() {
		body["token"] = req.Token.Reveal()
	}
	if req.Purpose != "" {
		body["purpose"] = req.Purpose
	}
	_, err := c.do(ctx, "POST", "/token/revoke", nil, body, nil, scmCallTimeout)
	return err
}

func (c *scmConn) signin(ctx context.Context, method, route string) (*scmSigninState, error) {
	var out scmSigninState
	var in any
	if method == "POST" {
		in = struct{}{}
	}
	if _, err := c.do(ctx, method, route, nil, in, &out, scmCallTimeout); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *scmConn) Signin(ctx context.Context) (*scmSigninState, error) {
	return c.signin(ctx, "POST", "/signin")
}

func (c *scmConn) SigninState(ctx context.Context) (*scmSigninState, error) {
	return c.signin(ctx, "GET", "/signin")
}

func (c *scmConn) SigninPoll(ctx context.Context, pollID string) (*scmSigninState, error) {
	return c.signin(ctx, "GET", "/signin/"+url.PathEscape(pollID))
}

func (c *scmConn) Forget(ctx context.Context) error {
	_, err := c.do(ctx, "DELETE", "/signin", nil, nil, nil, scmCallTimeout)
	return err
}

// --- reads ----------------------------------------------------------------------------

func (c *scmConn) Repos(ctx context.Context, q scmQuery) (*scmPage[scmRepo], error) {
	var out scmPage[scmRepo]
	_, err := c.do(ctx, "GET", "/repos", scmQueryOf(q), nil, &out, scmCallTimeout)
	return &out, err
}

func (c *scmConn) Repo(ctx context.Context, repo, as string) (*scmRepo, error) {
	var out scmRepo
	_, err := c.do(ctx, "GET", "/repo", scmVals("repo", repo, "as", as), nil, &out, scmCallTimeout)
	return &out, err
}

func (c *scmConn) PullCreate(ctx context.Context, req scmPullReq) (*scmPull, error) {
	var out scmPull
	_, err := c.do(ctx, "POST", "/pulls", nil, req, &out, scmCallTimeout)
	return &out, err
}

func (c *scmConn) Pulls(ctx context.Context, q scmQuery) (*scmPage[scmPull], error) {
	var out scmPage[scmPull]
	_, err := c.do(ctx, "GET", "/pulls", scmQueryOf(q), nil, &out, scmCallTimeout)
	return &out, err
}

func (c *scmConn) Pull(ctx context.Context, repo string, n int, as string) (*scmPull, error) {
	var out scmPull
	_, err := c.do(ctx, "GET", "/pulls/"+strconv.Itoa(n), scmVals("repo", repo, "as", as), nil, &out, scmCallTimeout)
	return &out, err
}

func (c *scmConn) PullPatch(ctx context.Context, n int, p scmPullPatch) (*scmPull, error) {
	var out scmPull
	_, err := c.do(ctx, "PATCH", "/pulls/"+strconv.Itoa(n), nil, p, &out, scmCallTimeout)
	return &out, err
}

func (c *scmConn) Comments(ctx context.Context, repo string, n int, since int64, as string) (*scmPage[scmComment], error) {
	var out scmPage[scmComment]
	_, err := c.do(ctx, "GET", "/pulls/"+strconv.Itoa(n)+"/comments", scmVals("repo", repo, "since", scmI64(since), "as", as), nil, &out, scmCallTimeout)
	return &out, err
}

// Checks answers nil, nil when ifNoneMatch still holds (304).
func (c *scmConn) Checks(ctx context.Context, repo, ref, ifNoneMatch, as string) (*scmChecks, error) {
	var out scmChecks
	nm, err := c.do(ctx, "GET", "/checks", scmVals("repo", repo, "ref", ref, "ifNoneMatch", ifNoneMatch, "as", as), nil, &out, scmCallTimeout)
	if err != nil || nm {
		return nil, err
	}
	return &out, nil
}

func (c *scmConn) JobLog(ctx context.Context, repo, job string, tailBytes int, since, until int64, as string) (*scmJobLog, error) {
	var out scmJobLog
	_, err := c.do(ctx, "GET", "/checks/jobs/"+url.PathEscape(job)+"/log",
		scmVals("repo", repo, "tailBytes", scmItoa(tailBytes), "since", scmI64(since), "until", scmI64(until), "as", as), nil, &out, scmCallTimeout)
	return &out, err
}

func (c *scmConn) Annotations(ctx context.Context, repo, check, cursor, as string) (*scmPage[scmAnnotation], error) {
	var out scmPage[scmAnnotation]
	_, err := c.do(ctx, "GET", "/checks/runs/"+url.PathEscape(check)+"/annotations", scmVals("repo", repo, "cursor", cursor, "as", as), nil, &out, scmCallTimeout)
	return &out, err
}

func (c *scmConn) Rerun(ctx context.Context, req scmRerunReq) (*scmRerun, error) {
	var out scmRerun
	_, err := c.do(ctx, "POST", "/checks/rerun", nil, req, &out, scmCallTimeout)
	return &out, err
}

func (c *scmConn) Issues(ctx context.Context, q scmQuery) (*scmPage[scmIssue], error) {
	var out scmPage[scmIssue]
	_, err := c.do(ctx, "GET", "/issues", scmQueryOf(q), nil, &out, scmCallTimeout)
	return &out, err
}

func (c *scmConn) Issue(ctx context.Context, repo string, n int, comments bool, as string) (*scmIssue, error) {
	cm := ""
	if comments {
		cm = "1"
	}
	var out scmIssue
	_, err := c.do(ctx, "GET", "/issues/"+strconv.Itoa(n), scmVals("repo", repo, "comments", cm, "as", as), nil, &out, scmCallTimeout)
	return &out, err
}

// --- poll and events ------------------------------------------------------------------

func (c *scmConn) Poll(ctx context.Context, req scmPollReq) (*scmPollResp, error) {
	var out scmPollResp
	_, err := c.do(ctx, "POST", "/poll", nil, req, &out, scmCallTimeout)
	return &out, err
}

func (c *scmConn) Subscribe(ctx context.Context, s scmSubscription) (*scmSubscription, error) {
	var out scmSubscription
	_, err := c.do(ctx, "POST", "/subscriptions", nil, s, &out, scmCallTimeout)
	return &out, err
}

func (c *scmConn) Subscriptions(ctx context.Context) ([]scmSubscription, error) {
	var out struct {
		Items []scmSubscription `json:"items"`
	}
	_, err := c.do(ctx, "GET", "/subscriptions", nil, nil, &out, scmCallTimeout)
	return out.Items, err
}

func (c *scmConn) Unsubscribe(ctx context.Context, id string) error {
	_, err := c.do(ctx, "DELETE", "/subscriptions/"+url.PathEscape(id), nil, nil, nil, scmCallTimeout)
	return err
}
