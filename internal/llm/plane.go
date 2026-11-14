package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	sdkllm "github.com/xbin-dev/xbin/sdk/llm"
)

// Connector is the connector's data plane: everything that touches an
// upstream. The rest of the package — who may call, which upstream and
// model, quotas, metering, the API — reaches upstreams only through it, and
// hands it a target resolved from configuration for every call, so the
// plane holds no settings and no keys of its own. Proxy is the one
// implementation, in the daemon; one that runs elsewhere (a sealed
// component, another process) implements the same three methods.
//
// A call is wire-agnostic here: a path under the upstream's base URL, a
// body, the request headers to send, and a sink that reads usage from the
// answer as it passes. Another wire (Anthropic's /v1/messages) is a route
// and a sink, not another plane.
type Connector interface {
	// Relay sends c to its target and streams the answer to w as it
	// arrives. It always answers w — the upstream's answer, or an
	// OpenAI-shaped error of its own — and reports what happened.
	Relay(ctx context.Context, w http.ResponseWriter, c Call) Outcome
	// Models asks an upstream for its model list.
	Models(ctx context.Context, t Target) ([]sdkllm.Model, error)
}

// Target is an upstream as one call reaches it. Only configuration (the
// admin's or the host's) makes one: no part comes from a request.
type Target struct {
	Name    string
	Kind    string
	BaseURL string
	Key     string // "" for an upstream that takes none
}

// Call is one request for an upstream.
type Call struct {
	Target Target
	Path   string      // under the base URL: sdkllm.PathChat, PathResponses
	Body   []byte      // sent as it is
	Header http.Header // sent beside Content-Type and the key (Accept)
	// Sink reads the usage of a successful answer (stream: it is an SSE
	// stream); nil reads none.
	Sink func(stream bool) sdkllm.UsageSink
	// Answer is set on the answer's headers before its status is written,
	// whoever writes it (a quota warning).
	Answer http.Header
}

// Outcome is how a call went.
type Outcome struct {
	Status   int          // what w was answered
	Upstream bool         // the upstream answered (Status is its own)
	Usage    sdkllm.Usage // what the answer reported
	HasUsage bool
	// Sent: the request reached the upstream (it may have worked on it,
	// whatever came back). Stream: the answer was an SSE stream. Bytes and
	// Events: how much of the answer was read from the upstream — what an
	// estimate is made from when the answer ended without its usage.
	Sent   bool
	Stream bool
	Bytes  int64
	Events int64
	// Canceled: the client went away before the answer ended.
	Canceled bool
	Err      string // why it failed, for the log: never a body or a key
}

// OK reports a call the upstream answered with success.
func (o Outcome) OK() bool { return o.Upstream && o.Status >= 200 && o.Status < 300 }

// Proxy is the in-daemon plane: a streaming reverse proxy. Nothing of an
// answer is held back — every read is written and flushed — and no deadline
// bounds a whole call: an upstream is given up on only after HeaderTimeout
// without an answer or IdleTimeout without a byte, so a long stream runs as
// long as it keeps talking.
//
// A client that goes away ends the upstream call, after Grace: providers
// report usage at the end of an answer and bill what they generated, so a
// call dropped at once would be free to whoever drops it. For Grace the
// upstream's answer is still read (and thrown away) to catch that usage;
// then the call is cut, and what was relayed is what the caller is charged
// an estimate for. A client that stays but stops reading is treated the
// same once a write has waited WriteTimeout.
type Proxy struct {
	Client        *http.Client
	HeaderTimeout time.Duration // no response headers for this long (a non-streaming call thinks before it answers)
	IdleTimeout   time.Duration // no byte of the body for this long
	Grace         time.Duration // how long an upstream call outlives its client, to report its usage
	WriteTimeout  time.Duration // one write to the client waits this long at most
	Log           *slog.Logger
}

// DefaultGrace is Proxy.Grace unless set (XBIN_LLM_CANCEL_GRACE).
const DefaultGrace = 5 * time.Second

// NewProxy is a Proxy with the default transport and timeouts.
func NewProxy(log *slog.Logger) *Proxy {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &Proxy{
		// an upstream's redirect is never followed: the key goes to the
		// configured address and nowhere else
		Client:        &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		HeaderTimeout: 10 * time.Minute, IdleTimeout: 10 * time.Minute, Grace: DefaultGrace, WriteTimeout: 2 * time.Minute, Log: log,
	}
}

var (
	errUpstreamSilent = errors.New("the upstream went silent")
	errClientGone     = errors.New("the client went away")
)

// answerHeaders are the upstream's response headers a client gets. Not
// Set-Cookie (it would land on xbind's origin), not the provider's account
// headers.
var answerHeaders = []string{"Content-Type", "Retry-After", "X-Request-Id", "Request-Id",
	"X-Ratelimit-Limit-Requests", "X-Ratelimit-Limit-Tokens", "X-Ratelimit-Remaining-Requests",
	"X-Ratelimit-Remaining-Tokens", "X-Ratelimit-Reset-Requests", "X-Ratelimit-Reset-Tokens"}

// maxErrorBody bounds an upstream's error answer, which is read whole so
// the key can never leave in it.
const maxErrorBody = 1 << 20

func (p *Proxy) request(ctx context.Context, method string, t Target, path string, body []byte, h http.Header) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, sdkllm.JoinURL(t.BaseURL, path), rd)
	if err != nil {
		return nil, err
	}
	for k, vs := range h {
		req.Header[http.CanonicalHeaderKey(k)] = vs
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if t.Key != "" {
		req.Header.Set("Authorization", "Bearer "+t.Key)
	}
	return req, nil
}

// Relay implements Connector.
func (p *Proxy) Relay(ctx context.Context, w http.ResponseWriter, c Call) Outcome {
	fail := func(status int, code, msg, logged string) Outcome {
		copyHeader(w.Header(), c.Answer)
		sdkllm.WriteError(w, status, sdkllm.Error{Message: msg, Code: code})
		return Outcome{Status: status, Err: logged}
	}
	rc := http.NewResponseController(w)
	defer func() { _ = rc.SetWriteDeadline(time.Time{}) }() // never leave a deadline on a connection that is kept alive

	// the upstream call is the client's, but outlives it by Grace
	up, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancel(nil)
	silent := time.AfterFunc(p.HeaderTimeout, func() { cancel(errUpstreamSilent) })
	defer silent.Stop()
	var (
		graceMu sync.Mutex
		grace   *time.Timer
		ended   bool
	)
	gone := func() { // the client went away, or stopped reading: once
		graceMu.Lock()
		defer graceMu.Unlock()
		if grace == nil && !ended {
			grace = time.AfterFunc(p.Grace, func() { cancel(errClientGone) })
		}
	}
	unwatch := context.AfterFunc(ctx, gone)
	defer func() {
		unwatch()
		graceMu.Lock()
		ended = true
		if grace != nil {
			grace.Stop()
		}
		graceMu.Unlock()
	}()

	req, err := p.request(up, http.MethodPost, c.Target, c.Path, c.Body, c.Header)
	if err != nil {
		return fail(http.StatusBadGateway, "xbin_llm_upstream", "the upstream "+c.Target.Name+" is misconfigured (its base URL)", err.Error())
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		switch cause := context.Cause(up); {
		case errors.Is(cause, errClientGone): // nobody reads this answer; the upstream had the request for Grace
			out := fail(499, "xbin_llm_canceled", "the call was cancelled", "the client went away before the upstream answered")
			out.Canceled, out.Sent = true, true
			return out
		case errors.Is(cause, errUpstreamSilent):
			out := fail(http.StatusGatewayTimeout, "xbin_llm_upstream_timeout",
				fmt.Sprintf("the upstream %s did not answer within %s", c.Target.Name, p.HeaderTimeout), "no response headers in time")
			out.Sent = true
			return out
		}
		return fail(http.StatusBadGateway, "xbin_llm_upstream_unreachable",
			"the upstream "+c.Target.Name+" could not be reached (an admin checks it: bx llm upstream test "+c.Target.Name+")", transportErr(err, c.Target))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return p.relayError(w, resp, c)
	}

	h := w.Header()
	for _, k := range answerHeaders {
		if vs := resp.Header.Values(k); len(vs) > 0 {
			h[k] = vs
		}
	}
	stream := strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	h.Set("Cache-Control", "no-store")
	if stream {
		h.Set("X-Accel-Buffering", "no") // a reverse proxy in front of xbind must not hold the stream back either
	}
	copyHeader(h, c.Answer)
	w.WriteHeader(resp.StatusCode)
	_ = rc.Flush()

	var sink sdkllm.UsageSink
	if c.Sink != nil {
		sink = c.Sink(stream)
	}
	out := Outcome{Status: resp.StatusCode, Upstream: true, Sent: true, Stream: stream}
	buf := make([]byte, 32<<10)
	reading := true // the client still takes what is written
	for {
		silent.Reset(p.IdleTimeout)
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			out.Bytes += int64(n)
			if sink != nil {
				_, _ = sink.Write(buf[:n])
			}
			if reading {
				_ = rc.SetWriteDeadline(time.Now().Add(p.WriteTimeout))
				_, werr := w.Write(buf[:n])
				if werr == nil {
					werr = rc.Flush()
				}
				if werr != nil && !errors.Is(werr, http.ErrNotSupported) {
					// gone, or not reading: the rest is read for its usage only, for Grace
					reading, out.Canceled, out.Err = false, true, "the client stopped reading"
					gone()
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			switch cause := context.Cause(up); {
			case errors.Is(cause, errClientGone):
				out.Canceled, out.Err = true, "the client went away mid-answer"
			case errors.Is(cause, errUpstreamSilent):
				out.Err = "the upstream went silent mid-answer"
			default:
				out.Err = "the upstream's answer broke off: " + transportErr(rerr, c.Target)
			}
			break
		}
	}
	if ctx.Err() != nil {
		out.Canceled = true
	}
	if sink != nil {
		out.Usage, out.HasUsage = sink.Usage()
		switch e := sink.(type) {
		case interface{ Events() int64 }:
			out.Events = e.Events()
		}
	}
	return out
}

// relayError answers an upstream's refusal. Its body is read whole (it is
// small) and passed on with the key cut out of it, should the upstream have
// echoed it. A refusal of the key itself (401, 403) is the workspace's
// problem, not the caller's credential: it is answered as a 502 of the
// connector's own, so no client mistakes it for its own sign-in failing.
// A redirect is refused the same way.
func (p *Proxy) relayError(w http.ResponseWriter, resp *http.Response, c Call) Outcome {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	out := Outcome{Status: resp.StatusCode, Upstream: true, Sent: true, Err: "upstream status " + resp.Status}
	own := func(code, msg string) Outcome {
		copyHeader(w.Header(), c.Answer)
		sdkllm.WriteError(w, http.StatusBadGateway, sdkllm.Error{Message: msg, Code: code})
		out.Status, out.Upstream = http.StatusBadGateway, false
		return out
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return own("xbin_llm_upstream_auth", "the upstream "+c.Target.Name+" refused the workspace's key (an admin checks it: bx llm upstream test "+c.Target.Name+")")
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return own("xbin_llm_upstream", "the upstream "+c.Target.Name+" answered with a redirect, which is never followed (an admin sets its base URL to the final address)")
	}
	if c.Target.Key != "" {
		body = bytes.ReplaceAll(body, []byte(c.Target.Key), []byte("[key]"))
	}
	h := w.Header()
	for _, k := range answerHeaders {
		if vs := resp.Header.Values(k); len(vs) > 0 {
			h[k] = vs
		}
	}
	if _, ok := sdkllm.ParseError(body); !ok { // not an OpenAI-shaped error (a gateway's HTML page): say so in one
		copyHeader(h, c.Answer)
		sdkllm.WriteError(w, resp.StatusCode, sdkllm.Error{Message: "the upstream " + c.Target.Name + " answered " + resp.Status, Code: "xbin_llm_upstream"})
		return out
	}
	h.Set("Content-Type", "application/json")
	copyHeader(h, c.Answer)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
	return out
}

func copyHeader(dst, src http.Header) {
	for k, vs := range src {
		dst[k] = vs
	}
}

// transportErr is a transport failure for the log, with the key (which no
// transport error carries, but a URL in one might) cut out.
func transportErr(err error, t Target) string {
	s := err.Error()
	if t.Key != "" {
		s = strings.ReplaceAll(s, t.Key, "[key]")
	}
	return s
}

// maxModelsBody bounds an upstream's model list.
const maxModelsBody = 8 << 20

// Models implements Connector.
func (p *Proxy) Models(ctx context.Context, t Target) ([]sdkllm.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := p.request(ctx, http.MethodGet, t, sdkllm.PathModels, nil, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, errors.New(transportErr(err, t))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxModelsBody))
	if resp.StatusCode != http.StatusOK {
		msg := resp.Status
		if e, ok := sdkllm.ParseError(body); ok {
			msg += ": " + e.Message
		}
		if t.Key != "" {
			msg = strings.ReplaceAll(msg, t.Key, "[key]")
		}
		return nil, errors.New(msg)
	}
	var list sdkllm.ModelList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, errors.New("its /models answer is not a model list")
	}
	return list.Data, nil
}
