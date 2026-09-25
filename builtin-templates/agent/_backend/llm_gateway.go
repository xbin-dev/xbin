// llm_gateway.go — gatewayLLM: the LLM interface over llm-gw. It picks a wire
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
)

// Per-call bounds. Vars so tests can shorten them. The idle bound is a
// watchdog re-armed on every read of the stream, not a ticker: a stream that
// keeps delivering bytes runs as long as llmStreamTimeout allows.
var (
	llmCallTimeout   = 5 * time.Minute  // one non-streaming call
	llmStreamTimeout = 10 * time.Minute // one streaming call, overall
	llmIdleTimeout   = 90 * time.Second // a stream with no bytes for this long is dead
	wireBackoff      = llmBackoff       // between retries (tests make it instant)
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
	base   string // e.g. http://xbin/api/apps/llm-gw (no trailing slash)
}

func newGatewayLLM() *gatewayLLM {
	return &gatewayLLM{client: xbin.Client(), base: "http://xbin/api/apps/" + gwPath()}
}

// bareModel strips a llm-gw "backend/" prefix: "openai/gpt-5" → "gpt-5".
func bareModel(model string) string {
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

func (g *gatewayLLM) Chat(ctx context.Context, req LLMRequest, onEvent func(LLMEvent)) (LLMReply, error) {
	if pickWire(req.Wire, req.Model) == "responses" {
		rep, err := g.call(ctx, req, onEvent, responsesCodec{})
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
	return g.call(ctx, req, onEvent, chatCodec{})
}

// call runs one request with retries. Retrying is allowed only while nothing
// has been produced: once an event went out the partial is surfaced as-is.
func (g *gatewayLLM) call(ctx context.Context, req LLMRequest, onEvent func(LLMEvent), w wireCodec) (LLMReply, error) {
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
		rep, retry, err := g.attempt(ctx, req, w, body, emit)
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
		lastErr = err
		if !retry || emitted || ctx.Err() != nil {
			rep.Wire, rep.Model = w.name(), req.Model
			return rep, err
		}
	}
	if lastErr == nil {
		lastErr = ctx.Err()
	}
	return LLMReply{Wire: w.name(), Model: req.Model}, lastErr
}

// attempt is one HTTP round trip. retry reports whether the failure is worth
// another attempt (transport trouble, 429/5xx, a stalled stream).
func (g *gatewayLLM) attempt(ctx context.Context, req LLMRequest, w wireCodec, body []byte, emit func(LLMEvent)) (rep LLMReply, retry bool, err error) {
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

	hreq, err := http.NewRequestWithContext(cctx, http.MethodPost, g.base+w.path(), bytes.NewReader(body))
	if err != nil {
		return LLMReply{}, false, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if req.Stream {
		hreq.Header.Set("Accept", "text/event-stream")
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
		msg := strings.TrimSpace(string(raw))
		if w.unsupported(resp.StatusCode, msg) {
			return LLMReply{}, false, fmt.Errorf("%w (llm-gw %s: %s)", errWireUnsupported, resp.Status, msg)
		}
		return LLMReply{}, retryableLLM(resp.StatusCode), fmt.Errorf("llm-gw %s: %s", resp.Status, msg)
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
		return rep, false, err
	}
	idle.Reset(llmIdleTimeout)
	rep, err = w.parseStream(&watchedReader{r: resp.Body, idle: idle}, emit)
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

// watchedReader re-arms the idle watchdog on every read that delivered bytes.
type watchedReader struct {
	r    io.Reader
	idle *time.Timer
}

func (w *watchedReader) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if n > 0 {
		w.idle.Reset(llmIdleTimeout)
	}
	return n, err
}

// streamError is an error the upstream reported inside a stream (an error
// chunk, response.failed): final, not a transport hiccup.
type streamError struct{ msg string }

func (e *streamError) Error() string { return e.msg }

// sseData calls fn with each `data:` payload of an SSE stream until [DONE] or
// EOF. Scanner errors are returned.
func sseData(r io.Reader, fn func(data []byte) error) error {
	br := newLineReader(r)
	for {
		line, err := br.next()
		if len(line) > 0 {
			l := bytes.TrimSpace(line)
			if d, ok := bytes.CutPrefix(l, []byte("data:")); ok {
				d = bytes.TrimSpace(d)
				if string(d) == "[DONE]" {
					return nil
				}
				if len(d) > 0 {
					if ferr := fn(d); ferr != nil {
						return ferr
					}
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// lineReader splits on '\n' with an 8 MiB line cap (a huge tool-call argument
// arrives as one data line on some providers).
type lineReader struct {
	r   io.Reader
	buf []byte
	eof bool
}

func newLineReader(r io.Reader) *lineReader { return &lineReader{r: r} }

func (l *lineReader) next() ([]byte, error) {
	for {
		if i := bytes.IndexByte(l.buf, '\n'); i >= 0 {
			line := l.buf[:i]
			l.buf = l.buf[i+1:]
			return line, nil
		}
		if l.eof {
			line := l.buf
			l.buf = nil
			return line, io.EOF
		}
		if len(l.buf) > 8<<20 {
			return nil, errors.New("sse line too long")
		}
		chunk := make([]byte, 32<<10)
		n, err := l.r.Read(chunk)
		l.buf = append(l.buf, chunk[:n]...)
		if err == io.EOF {
			l.eof = true
		} else if err != nil {
			return nil, err
		}
	}
}
