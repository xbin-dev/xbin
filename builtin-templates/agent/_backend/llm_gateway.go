// llm_gateway.go — gatewayLLM: the LLM interface over the agent's bound LLM
// providers (llm_providers.go — llm-gw, or any OpenAI-compatible tile). It picks a wire
// (Chat Completions or OpenAI's Responses API), bounds every call with
// deadlines, and retries transient failures only while nothing has streamed
// yet — a retry after tokens flowed would show the human a restarted draft.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/llm"
)

// Per-call bounds. Vars so tests can shorten them. The idle bound is a
// watchdog re-armed on every read of the stream, not a ticker: a stream that
// keeps delivering bytes runs as long as llmStreamTimeout allows. It is short
// until the model starts answering, and longer once it has: some upstreams
// send a tool call whole when it is done (kimi-k3, deepseek-v4.1-flash via
// llm-gw), so a large one — a file written in one call — is minutes of
// silence mid-stream (~4 s per KB of arguments) that isn't a dead stream.
var (
	llmCallTimeout        = 5 * time.Minute  // one non-streaming call
	llmStreamTimeout      = 10 * time.Minute // one streaming call, overall
	llmIdleTimeout        = 90 * time.Second // no bytes yet (headers, the first token) for this long: dead
	llmStartedIdleTimeout = 5 * time.Minute  // once bytes came: silence this long is dead
	wireBackoff           = llmBackoff       // between retries (tests make it instant)
)

var (
	errLLMIdle          = errors.New("llm stream stalled: no data from the model for too long")
	errLLMDeadline      = errors.New("llm call took too long")
	errWireUnsupported  = errors.New("the Responses API is not available for this model")
	reasoningModelRE    = regexp.MustCompile(`^(gpt-5|o[0-9])`)
	responsesFallbackMu sync.Mutex
	// responsesFallback remembers models whose gateway/upstream refused the
	// Responses API, so later calls go straight to Chat Completions.
	responsesFallback = map[string]bool{}
)

type gatewayLLM struct {
	client *http.Client
	// base pins one endpoint (tests); empty ⇒ each request goes where its
	// model reference resolves (resolveModel).
	base string
}

func newGatewayLLM() *gatewayLLM {
	return &gatewayLLM{client: xbin.Client()}
}

// bareModel strips a provider ("apps/llm-gw|…") and a llm-gw "backend/"
// prefix: "openai/gpt-5" → "gpt-5".
func bareModel(model string) string {
	_, model = splitModelRef(model)
	if i := strings.LastIndex(model, "/"); i >= 0 {
		return model[i+1:]
	}
	return model
}

// isReasoningModel is a name heuristic for OpenAI's reasoning families — the
// models that only show their thinking through the Responses API.
func isReasoningModel(model string) bool {
	return reasoningModelRE.MatchString(strings.ToLower(bareModel(model)))
}

// pickWire resolves "auto" and honours an explicit choice.
func pickWire(wire, model string) string {
	switch wire {
	case "chat", "responses":
		return wire
	}
	if !isReasoningModel(model) {
		return "chat"
	}
	responsesFallbackMu.Lock()
	no := responsesFallback[model]
	responsesFallbackMu.Unlock()
	if no {
		return "chat"
	}
	return "responses"
}

// wireCodec is one model API: how a request is encoded and a reply decoded.
type wireCodec interface {
	name() string
	path() string
	encode(req LLMRequest) ([]byte, error)
	parseStream(r io.Reader, emit func(LLMEvent)) (LLMReply, error)
	parseBody(raw []byte) (LLMReply, error)
	unsupported(code int, body string) bool
}

// llmHeaderHooks add headers to a model call by the run that makes it
// (LLMRequest.Run); registered at init, none in a normal instance's calls.
var llmHeaderHooks []func(req LLMRequest, h http.Header)

// llmRefuseHooks may refuse a model call before it is made, whatever the
// LLM behind the engine (Engine.chat): a workspace conversation that can't
// go on on its model. Registered at init; none in a normal instance.
var llmRefuseHooks []func(req LLMRequest) error

// llmStatusHooks hear how a model call was answered: its status, and the
// body of a refusal (nil for a 200). Registered at init.
var llmStatusHooks []func(req LLMRequest, status int, body []byte)

func (g *gatewayLLM) Chat(ctx context.Context, req LLMRequest, onEvent func(LLMEvent)) (LLMReply, error) {
	// the provider a reference names; requests (and replies, and the
	// transcript) carry the bare model id
	base := g.base
	if base == "" {
		var p llmProvider
		p, req.Model = resolveModel(ctx, req.Model)
		base = p.URL
		served := servedFor(ctx, p.Path, req)
		req.Via, req.Expect = p.Path+"|"+served, served
	} else {
		_, req.Model = splitModelRef(req.Model)
		req.Via = "|" + req.Model
	}
	if pickWire(req.Wire, req.Model) == "responses" {
		rep, err := g.send(ctx, base, req, onEvent, responsesCodec{})
		if !errors.Is(err, errWireUnsupported) {
			return rep, err
		}
		// Only an automatic choice falls back; a forced "responses" reports it.
		if req.Wire == "responses" {
			return rep, err
		}
		responsesFallbackMu.Lock()
		responsesFallback[req.Model] = true
		responsesFallbackMu.Unlock()
	}
	rep, err := g.send(ctx, base, req, onEvent, chatCodec{})
	if err != nil && replayRefused(err) {
		// a provider that takes no plain reasoning back (D249): the call once more without it — only a 400
		// that isn't about the context's size, only once. The run remembers it (Engine.chat) only when the
		// refusal named the field and that call worked; opaque blocks (reasoning_details) go either way.
		if sent := plainFields(req); req.PlainOff == "" && len(sent) > 0 {
			again := req
			again.PlainOff = "*"
			if rep2, err2 := g.send(ctx, base, again, onEvent, chatCodec{}); err2 == nil {
				rep2.ReplayRefused = namedField(err, sent)
				return rep2, nil
			}
		} else if req.PlainOff != "" && req.Reenable {
			// refused with the run's plain reasoning off: perhaps it was never the trouble — once per run, the
			// call with it
			again := req
			again.PlainOff = ""
			if len(plainFields(again)) > 0 {
				if rep2, err2 := g.send(ctx, base, again, onEvent, chatCodec{}); err2 == nil {
					rep2.ReplayBack = true
					return rep2, nil
				}
			}
		}
	}
	return rep, err
}

// send is call, its reply's Via the model that answered: the LLM
// connector's word for it (X-XBin-LLM-Model) when it said, else the one
// expected. A request that sends reasoning back says whose it is
// (X-XBin-LLM-Expect-Model); the connector refuses it when the model's name
// now leads to another (an alias's target changed, or this agent's list of
// models is out of date: 409 xbin_llm_model_moved) — then it goes once more
// without any reasoning sent back, the list read anew.
func (g *gatewayLLM) send(ctx context.Context, base string, req LLMRequest, onEvent func(LLMEvent), w wireCodec) (LLMReply, error) {
	if !carriesReplay(req, w.name()) {
		req.Expect = ""
	}
	rep, err := g.call(ctx, base, req, onEvent, w)
	if err != nil && req.Expect != "" && modelMoved(err) {
		dropCatalog()
		req.NoReplay, req.Expect = true, ""
		rep, err = g.call(ctx, base, req, onEvent, w)
	}
	rep.Via = req.Via
	if prov, _, ok := strings.Cut(req.Via, "|"); ok && rep.Served != "" && prov != "" {
		rep.Via = prov + "|" + rep.Served
	}
	return rep, err
}

// servedFor is the model a provider serves for the request's model: an
// alias's target (the LLM connector's xbin/default, an admin's alias) when
// its list says, the model itself when its list names it; else — a name
// its list doesn't know (one a catch-all alias routes) — the model that
// answered the conversation's latest call to it, as the connector named
// it; else the name. Reasoning goes back only to the model that produced it
// (and the connector checks it: send).
func servedFor(ctx context.Context, provider string, req LLMRequest) string {
	if s, known := servedAs(ctx, provider, req.Model); known {
		return s
	}
	for i := len(req.Msgs) - 1; i >= 0; i-- {
		if r := req.Msgs[i].Replay; req.Msgs[i].Role == "assistant" && r != nil && r.Model == req.Model {
			if p, s, ok := strings.Cut(r.Via, "|"); ok && p == provider && s != "" {
				return s
			}
		}
	}
	return req.Model
}

// servedAs is what provider's model list says id is served by: an alias's
// target, or id itself; known is false when the list doesn't name it.
func servedAs(ctx context.Context, provider, id string) (string, bool) {
	for _, m := range modelCatalog(ctx).Models {
		if m.ID == id && (m.Provider == provider || m.Provider == "") {
			if m.AliasOf != "" {
				return m.AliasOf, true
			}
			return id, true
		}
	}
	return id, false
}

// modelMoved: the connector refused a request that sent reasoning back
// because the model's name now leads to another model.
func modelMoved(err error) bool {
	var f *llmFailed
	return errors.As(err, &f) && f.Status == http.StatusConflict && strings.Contains(err.Error(), "xbin_llm_model_moved")
}

// carriesReplay reports whether req, encoded for wire, sends reasoning back.
func carriesReplay(req LLMRequest, wire string) bool {
	for _, m := range req.Msgs {
		r := m.Replay
		if m.Role != "assistant" || !sameSource(r, wire, req) {
			continue
		}
		if len(r.ReasoningRaw) > 0 || (wire == "chat" && r.Via != "" && r.Reasoning != "" && r.ReplayField != "" && !plainOff(req, r.ReplayField)) {
			return true
		}
	}
	return false
}

// call runs one request with retries. Retrying is allowed only while nothing
// has been produced: once an event went out the partial is surfaced as-is.
func (g *gatewayLLM) call(ctx context.Context, base string, req LLMRequest, onEvent func(LLMEvent), w wireCodec) (LLMReply, error) {
	body, err := w.encode(req)
	if err != nil {
		return LLMReply{}, err
	}
	var thinkStart, thinkEnd time.Time
	emitted := false
	emit := func(ev LLMEvent) {
		emitted = true
		if ev.Kind == "thinking" {
			if thinkStart.IsZero() {
				thinkStart = time.Now()
			}
		} else if !thinkStart.IsZero() && thinkEnd.IsZero() {
			thinkEnd = time.Now()
		}
		if onEvent != nil {
			onEvent(ev)
		}
	}
	var lastErr error
	for attempt := 0; attempt <= llmRetries; attempt++ {
		if attempt > 0 && !wireBackoff(ctx, attempt) {
			break
		}
		rep, retry, err := g.attempt(ctx, base, req, w, body, emit)
		if err == nil {
			rep.Wire, rep.Model = w.name(), req.Model
			if !thinkStart.IsZero() {
				if thinkEnd.IsZero() {
					thinkEnd = time.Now()
				}
				rep.ReasoningMs = thinkEnd.Sub(thinkStart).Milliseconds()
			}
			return rep, nil
		}
		lastErr = failedLLM(err, retry)
		if !retry || emitted || ctx.Err() != nil {
			rep.Wire, rep.Model = w.name(), req.Model
			return rep, lastErr
		}
	}
	if lastErr == nil {
		lastErr = ctx.Err()
	}
	return LLMReply{Wire: w.name(), Model: req.Model}, lastErr
}

// failedLLM is an attempt's error as Chat returns it: typed (llmFailed),
// with what the attempt judged of it — unless it is already, or says the
// wire isn't the upstream's (the caller's to switch).
func failedLLM(err error, retry bool) error {
	var f *llmFailed
	if errors.As(err, &f) || errors.Is(err, errWireUnsupported) || errors.Is(err, context.Canceled) {
		return err
	}
	return &llmFailed{err: err, Transient: retry}
}

// attempt is one HTTP round trip. retry reports whether the failure is worth
// another attempt (transport trouble, 429/5xx, a stalled stream).
func (g *gatewayLLM) attempt(ctx context.Context, base string, req LLMRequest, w wireCodec, body []byte, emit func(LLMEvent)) (rep LLMReply, retry bool, err error) {
	var cctx context.Context
	var cancel context.CancelCauseFunc
	var idle *time.Timer
	if req.Stream {
		tctx, tcancel := context.WithTimeoutCause(ctx, llmStreamTimeout, errLLMDeadline)
		defer tcancel()
		cctx, cancel = context.WithCancelCause(tctx)
		// Armed now so it also covers the wait for response headers.
		idle = time.AfterFunc(llmIdleTimeout, func() { cancel(errLLMIdle) })
		defer idle.Stop()
	} else {
		tctx, tcancel := context.WithTimeoutCause(ctx, llmCallTimeout, errLLMDeadline)
		defer tcancel()
		cctx, cancel = context.WithCancelCause(tctx)
	}
	defer cancel(nil)

	hreq, err := http.NewRequestWithContext(cctx, http.MethodPost, base+w.path(), bytes.NewReader(body))
	if err != nil {
		return LLMReply{}, false, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if req.Stream {
		hreq.Header.Set("Accept", "text/event-stream")
	}
	for _, f := range llmHeaderHooks { // what a kind of conversation adds to its model calls (a workspace agent: its handle)
		f(req, hreq.Header)
	}
	if req.Expect != "" {
		hreq.Header.Set("X-XBin-LLM-Expect-Model", req.Expect) // whose reasoning it sends back (send)
	}
	resp, err := g.client.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return LLMReply{}, false, ctx.Err()
		}
		return LLMReply{}, true, fmt.Errorf("llm-gw call: %w", causeOf(cctx, err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		for _, f := range llmStatusHooks {
			f(req, resp.StatusCode, raw)
		}
		msg := strings.TrimSpace(string(raw))
		if w.unsupported(resp.StatusCode, msg) {
			return LLMReply{}, false, fmt.Errorf("%w (llm-gw %s: %s)", errWireUnsupported, resp.Status, msg)
		}
		transient := llmTransient(resp.StatusCode, msg)
		return LLMReply{}, transient, &llmFailed{err: fmt.Errorf("llm-gw %s: %s", resp.Status, msg), Status: resp.StatusCode, Transient: transient}
	}
	for _, f := range llmStatusHooks {
		f(req, resp.StatusCode, nil)
	}
	said := resp.Header.Get("X-XBin-LLM-Effort") // what the LLM connector sent the model (D249)
	served := resp.Header.Get("X-XBin-LLM-Model")
	if len(served) > 200 {
		served = ""
	}
	if !req.Stream {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			if ctx.Err() != nil {
				return LLMReply{}, false, ctx.Err()
			}
			return LLMReply{}, true, fmt.Errorf("llm-gw read: %w", causeOf(cctx, err))
		}
		rep, err := w.parseBody(raw)
		rep.Effort, rep.Served = said, served
		return rep, false, err
	}
	idle.Reset(llmIdleTimeout)
	rep, err = w.parseStream(&watchedReader{r: resp.Body, idle: idle}, emit)
	rep.Effort, rep.Served = said, served
	if err != nil {
		if ctx.Err() != nil {
			return rep, false, ctx.Err()
		}
		var se *streamError
		if errors.As(err, &se) {
			return rep, false, err // the upstream said so; retrying repeats it
		}
		return rep, true, fmt.Errorf("llm stream: %w", causeOf(cctx, err))
	}
	return rep, false, nil
}

// causeOf prefers our own cancellation cause (idle watchdog, deadline) over the
// bare "context canceled" a read returns.
func causeOf(ctx context.Context, err error) error {
	if c := context.Cause(ctx); c != nil && (errors.Is(c, errLLMIdle) || errors.Is(c, errLLMDeadline)) {
		return c
	}
	return err
}

// watchedReader re-arms the idle watchdog on every read that delivered bytes
// — with the started bound: the model has begun answering.
type watchedReader struct {
	r    io.Reader
	idle *time.Timer
}

func (w *watchedReader) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if n > 0 {
		w.idle.Reset(llmStartedIdleTimeout)
	}
	return n, err
}

// streamError is an error the upstream reported inside a stream (an error
// chunk, response.failed): final, not a transport hiccup.
type streamError struct{ msg string }

func (e *streamError) Error() string { return e.msg }

// errSSELine is a stream line past the reader's cap (8 MiB: a huge
// tool-call argument arrives as one data line on some providers).
var errSSELine = errors.New("sse line too long")

// sseData calls fn with each `data:` payload of an SSE stream until [DONE] or
// EOF — a line at a time (llm.ReadData), so a server that leaves out the
// blank line between events is read too. Read errors are returned.
func sseData(r io.Reader, fn func(data []byte) error) error {
	err := llm.ReadData(r, fn)
	if err == llm.ErrSSELineTooLong {
		return errSSELine
	}
	return err
}
