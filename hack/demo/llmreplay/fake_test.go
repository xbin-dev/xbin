package main

// Test fixtures: a scripted model API speaking the three wires (the
// upstream a recording runs against), proxies in each mode, and a client.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The upstream keys the proxy holds in these tests; a cassette must never
// carry them, nor the callers' own credentials.
const (
	openaiKey    = "sk-proj-UPSTREAM-OPENAI-KEY"
	anthropicKey = "sk-ant-api03-UPSTREAM-ANTHROPIC-KEY"
	callerSecret = "sk-ant-oat01-CALLER-OWN-TOKEN"
)

type seenReq struct {
	method, path, query string
	header              http.Header
	body                []byte
}

// fakeAPI answers every model call with "Answer <n> to <the last input>",
// n counting its calls — so each recorded response is unique — streamed
// word by word with gap between events when asked to stream.
type fakeAPI struct {
	*httptest.Server
	gap time.Duration

	mu      sync.Mutex
	n       int
	reqs    []seenReq
	fail429 int // answer this many next model calls with 429
}

func newFakeAPI(t *testing.T, gap time.Duration) *fakeAPI {
	f := &fakeAPI{gap: gap}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAPI) seen() []seenReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seenReq(nil), f.reqs...)
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, seenReq{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body})
	f.n++
	n := f.n
	fail := f.fail429 > 0 && r.Method == http.MethodPost
	if fail {
		f.fail429--
	}
	f.mu.Unlock()
	// what a real API sends besides the answer, which a cassette must not keep
	w.Header().Set("Set-Cookie", "__cf_bm=COOKIE-SECRET; path=/")
	w.Header().Set("Openai-Organization", "acme-org-SECRET")
	w.Header().Set("X-Request-Id", fmt.Sprintf("req_%d", n))
	if fail {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"Rate limit reached","type":"rate_limit_error"}}`)
		return
	}
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-5.1","object":"model","owned_by":"openai"},{"id":"gpt-5-mini","object":"model","owned_by":"openai"}]}`)
		return
	}
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)
	ctype, parts := fakeParts(apiOf(r.URL.Path), req.Model, req.Stream, fmt.Sprintf("Answer %d to %q", n, lastText(body)))
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(http.StatusOK)
	for i, p := range parts {
		if i > 0 && req.Stream {
			time.Sleep(f.gap)
		}
		_, _ = w.Write(p)
		_ = http.NewResponseController(w).Flush()
	}
}

// fakeParts is an answer in a wire's shape: one JSON body, or the events of
// a stream.
func fakeParts(api, model string, stream bool, text string) (string, [][]byte) {
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	words := strings.SplitAfter(text, " ")
	switch api {
	case apiMessages:
		if !stream {
			return "application/json", [][]byte{[]byte(js(map[string]any{"id": "msg_01FAKE", "type": "message", "role": "assistant", "model": model,
				"content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn",
				"usage": map[string]any{"input_tokens": 12, "output_tokens": 7}}))}
		}
		ev := func(name string, v any) []byte { return []byte("event: " + name + "\ndata: " + js(v) + "\n\n") }
		parts := [][]byte{
			ev("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_01FAKE", "type": "message", "role": "assistant",
				"model": model, "content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": 12, "output_tokens": 1}}}),
			ev("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}),
			ev("ping", map[string]any{"type": "ping"}),
		}
		for _, w := range words {
			parts = append(parts, ev("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": w}}))
		}
		return "text/event-stream; charset=utf-8", append(parts,
			ev("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}),
			ev("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 7}}),
			ev("message_stop", map[string]any{"type": "message_stop"}))
	case apiResponses:
		final := map[string]any{"id": "resp_FAKE", "object": "response", "status": "completed", "model": model,
			"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}},
			"usage":  map[string]any{"input_tokens": 12, "output_tokens": 7}}
		if !stream {
			return "application/json", [][]byte{[]byte(js(final) + "\n")}
		}
		ev := func(v map[string]any) []byte {
			return []byte("event: " + v["type"].(string) + "\ndata: " + js(v) + "\n\n")
		}
		parts := [][]byte{ev(map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_FAKE", "status": "in_progress"}})}
		for _, w := range words {
			parts = append(parts, ev(map[string]any{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "delta": w}))
		}
		return "text/event-stream", append(parts, ev(map[string]any{"type": "response.completed", "response": final}))
	default:
		if !stream {
			return "application/json", [][]byte{[]byte(js(map[string]any{"id": "chatcmpl-FAKE", "object": "chat.completion", "model": model,
				"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}},
				"usage":   map[string]any{"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19}}) + "\n")}
		}
		data := func(v any) []byte { return []byte("data: " + js(v) + "\n\n") }
		chunk := func(delta map[string]any, fin any) []byte {
			return data(map[string]any{"id": "chatcmpl-FAKE", "object": "chat.completion.chunk", "model": model,
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": fin}}})
		}
		parts := [][]byte{chunk(map[string]any{"role": "assistant", "content": ""}, nil)}
		for _, w := range words {
			parts = append(parts, chunk(map[string]any{"content": w}, nil))
		}
		return "text/event-stream", append(parts, chunk(map[string]any{}, "stop"),
			data(map[string]any{"id": "chatcmpl-FAKE", "choices": []any{}, "usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 7}}),
			[]byte("data: [DONE]\n\n"))
	}
}

// lastText is a request's last input, verbatim (a message, a tool result).
func lastText(body []byte) string {
	var v struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Input json.RawMessage `json:"input"`
	}
	_ = json.Unmarshal(body, &v)
	if n := len(v.Messages); n > 0 {
		return rawText(v.Messages[n-1].Content)
	}
	var items []struct {
		Content json.RawMessage `json:"content"`
		Output  json.RawMessage `json:"output"`
	}
	if json.Unmarshal(v.Input, &items) == nil && len(items) > 0 {
		if it := items[len(items)-1]; len(it.Output) > 0 {
			return rawText(it.Output)
		} else {
			return rawText(it.Content)
		}
	}
	var s string
	_ = json.Unmarshal(v.Input, &s)
	return s
}

func rawText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(raw, &parts)
	var out []string
	for _, p := range parts {
		if p.Text != "" {
			out = append(out, p.Text)
		} else if len(p.Content) > 0 {
			out = append(out, rawText(p.Content))
		}
	}
	return strings.Join(out, " ")
}

// --- proxies ---------------------------------------------------------------------

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type testProxy struct {
	*httptest.Server
	s   *server
	log *syncBuffer
}

func startProxy(t *testing.T, opt options, ups upstreams, setup func(*server)) *testProxy {
	t.Helper()
	lb := &syncBuffer{}
	s := newServer(opt, ups, log.New(lb, "", 0))
	if setup != nil {
		setup(s)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(func() {
		ts.Close()
		if t.Failed() {
			t.Logf("proxy log (%s):\n%s", opt.mode, lb.String())
		}
	})
	return &testProxy{Server: ts, s: s, log: lb}
}

// fakeUps are upstreams on f, with keys.
func fakeUps(f *fakeAPI) upstreams {
	return upstreams{
		openai:    upstream{name: "openai", base: f.URL + "/v1", key: openaiKey, keyEnv: "OPENAI_API_KEY", header: "Authorization"},
		anthropic: upstream{name: "anthropic", base: f.URL, key: anthropicKey, keyEnv: "ANTHROPIC_API_KEY", header: "X-Api-Key"},
	}
}

func cassettePath(t *testing.T) string {
	return filepath.Join(t.TempDir(), "replay", "take.jsonl")
}

func recordProxy(t *testing.T, ups upstreams, cassette string) *testProxy {
	t.Helper()
	rec, next, err := openRecorder(cassette, false, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rec.close() })
	return startProxy(t, options{mode: "record", cassette: cassette}, ups, func(s *server) { s.rec, s.seq = rec, next })
}

func replayOpts() options {
	return options{mode: "replay", timing: 1, drift: 0.8, window: 8, onMiss: "error"}
}

// replayProxy replays cassette; its clock doesn't wait (sleepLog collects
// what it was asked to wait, when given).
func replayProxy(t *testing.T, cassette string, opt options, ups upstreams, sleeps *sleepLog) *testProxy {
	t.Helper()
	c, err := loadCassette(cassette)
	if err != nil {
		t.Fatal(err)
	}
	return startProxy(t, opt, ups, func(s *server) {
		s.play = newPlayer(c, opt)
		s.sleep = sleeps.sleep
	})
}

type sleepLog struct {
	mu sync.Mutex
	d  []time.Duration
}

// sleep records d and returns at once (a nil log just returns).
func (l *sleepLog) sleep(ctx context.Context, d time.Duration) bool {
	if l != nil {
		l.mu.Lock()
		l.d = append(l.d, d)
		l.mu.Unlock()
	}
	return ctx.Err() == nil
}

func (l *sleepLog) all() []time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]time.Duration(nil), l.d...)
}

// finish stops a recording proxy (waiting for its handlers: an exchange is
// written once its handler is done) and returns the cassette's contents.
func finish(t *testing.T, p *testProxy, cassette string) string {
	t.Helper()
	p.Close()
	b, err := os.ReadFile(cassette)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// --- a client ----------------------------------------------------------------------

type resp struct {
	status int
	header http.Header
	body   string
}

func send(t *testing.T, url string, hdr map[string]string, body any) resp {
	t.Helper()
	method, rd := http.MethodGet, io.Reader(nil)
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		method, rd = http.MethodPost, bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp{r.StatusCode, r.Header, string(b)}
}

// --- request builders ------------------------------------------------------------

type obj = map[string]any

func chat(model string, stream bool, system string, msgs ...obj) obj {
	all := []any{obj{"role": "system", "content": system}}
	for _, m := range msgs {
		all = append(all, m)
	}
	return obj{"model": model, "stream": stream, "messages": all}
}

func user(text string) obj { return obj{"role": "user", "content": text} }

func said(text string) obj { return obj{"role": "assistant", "content": text} }

func calls(id, name, args string) obj {
	return obj{"role": "assistant", "content": nil, "tool_calls": []any{obj{"id": id, "type": "function", "function": obj{"name": name, "arguments": args}}}}
}

func result(id, text string) obj { return obj{"role": "tool", "tool_call_id": id, "content": text} }

// claude is a Messages API request the way Claude Code sends one: system
// blocks (the billing line first), metadata with its session, blocks.
func claude(model, session string, msgs ...obj) obj {
	all := []any{}
	for _, m := range msgs {
		all = append(all, m)
	}
	return obj{"model": model, "max_tokens": 32000, "stream": true,
		"system": []any{
			obj{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.280.a1b; cc_entrypoint=cli; cch=" + session[:5] + ";"},
			obj{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude.", "cache_control": obj{"type": "ephemeral"}},
			obj{"type": "text", "text": "You are an interactive CLI tool that helps users with software engineering tasks.\nToday's date is 2026-10-02."}},
		"messages": all,
		"tools":    []any{obj{"name": "Bash", "input_schema": obj{"type": "object"}}, obj{"name": "Read", "input_schema": obj{"type": "object"}}},
		"metadata": obj{"user_id": "user_4f1d_account__session_" + session}}
}

func userBlocks(texts ...string) obj {
	var c []any
	for _, t := range texts {
		c = append(c, obj{"type": "text", "text": t})
	}
	return obj{"role": "user", "content": c}
}

func toolUse(id, name string, input obj) obj {
	return obj{"role": "assistant", "content": []any{obj{"type": "tool_use", "id": id, "name": name, "input": input}}}
}

func toolResult(id, text string) obj {
	return obj{"role": "user", "content": []any{obj{"type": "tool_result", "tool_use_id": id, "content": text}}}
}
