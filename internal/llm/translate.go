package llm

// translate.go — the Responses wire on an upstream that only has Chat
// Completions (D244). Many OpenAI-compatible providers and gateways serve
// POST /chat/completions and nothing else; a client that speaks Responses
// alone (Codex) could not run on them. For such an upstream the connector
// answers POST /v1/responses itself: it rewrites the request as a chat
// completion, sends that, and rewrites the answer — a stream event by
// event, as it arrives — into the Responses shape.
//
// What is translated is the stateless core both wires share: instructions
// and input items (messages with text and images, function calls and
// their outputs), function tools, tool choice, parallel tool calls,
// sampling and output limits, a JSON output format, streamed text,
// reasoning text and tool calls, usage. What only OpenAI's own service has
// is refused or left out, and said: previous_response_id and stored
// conversations (400 — the caller sends the conversation in input), an
// input item or content part of a kind chat can't carry (400, naming it),
// OpenAI's hosted tools (left out of the tool list: web search, file
// search, the code interpreter …). A "custom" (free-form) tool becomes a
// function tool with one string argument, input, and its call comes back
// as a custom_tool_call.
//
// Errors need no translation: an upstream's refusal of a chat completion
// — a context-length error among them — is an OpenAI-shaped error on
// either wire, and passes as it is.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sdkllm "github.com/xbin-dev/xbin/sdk/llm"
)

// The ways an upstream's Responses calls are served (Upstream.ResponsesVia).
const (
	ViaAuto     = "auto"     // the upstream's own Responses API when it has one, else translated ("" means this)
	ViaUpstream = "upstream" // always the upstream's own: its 404 is the caller's
	ViaChat     = "chat"     // always translated to chat completions
)

// chatOnlyTTL is how long an upstream that answered "no such route" to a
// Responses call is taken to have none before it is asked again.
const chatOnlyTTL = 10 * time.Minute

// wireMemo remembers which upstreams have no Responses API.
type wireMemo struct {
	mu       sync.Mutex
	chatOnly map[string]time.Time // upstream name + address → when it said so
}

func wireKey(t Target) string { return t.Name + "\x00" + t.BaseURL }

func (m *wireMemo) isChatOnly(t Target, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	at, ok := m.chatOnly[wireKey(t)]
	return ok && now.Sub(at) < chatOnlyTTL
}

func (m *wireMemo) setChatOnly(t Target, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.chatOnly == nil {
		m.chatOnly = map[string]time.Time{}
	}
	m.chatOnly[wireKey(t)] = now
}

// routeMissing reports whether an upstream's answer to a Responses call
// says it has no such API — rather than refusing this call: a 404, 405 or
// 501 that doesn't speak of the model.
func routeMissing(status int, body []byte) bool {
	if status != http.StatusNotFound && status != http.StatusMethodNotAllowed && status != http.StatusNotImplemented {
		return false
	}
	var e struct {
		Error struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return true
	}
	code := strings.ToLower(fmt.Sprint(e.Error.Code))
	msg := strings.ToLower(e.Error.Message)
	if strings.Contains(code, "model") || strings.Contains(msg, "model") && !strings.Contains(msg, "/responses") {
		return false // it knows the route and not the model
	}
	return true
}

// heldAnswer is a ResponseWriter that keeps a "no such route" answer back
// instead of writing it, so the call can be made again another way; any
// other answer passes straight through.
type heldAnswer struct {
	http.ResponseWriter
	held   bool
	status int
	header http.Header
	body   bytes.Buffer
	wrote  bool
}

func (h *heldAnswer) Unwrap() http.ResponseWriter { return h.ResponseWriter }

// FlushError flushes what was let through; a held answer has nothing out.
func (h *heldAnswer) FlushError() error {
	if !h.wrote || h.held {
		return nil
	}
	return http.NewResponseController(h.ResponseWriter).Flush()
}

func (h *heldAnswer) Header() http.Header {
	if h.header == nil {
		h.header = http.Header{}
	}
	return h.header // copied out when the answer is let through
}

func (h *heldAnswer) WriteHeader(code int) {
	if h.wrote {
		return
	}
	h.wrote, h.status = true, code
	if code == http.StatusNotFound || code == http.StatusMethodNotAllowed || code == http.StatusNotImplemented {
		h.held = true
		return
	}
	h.release()
}

func (h *heldAnswer) release() {
	dst := h.ResponseWriter.Header()
	for k, vs := range h.header {
		dst[k] = vs
	}
	h.ResponseWriter.WriteHeader(h.status)
}

func (h *heldAnswer) Write(p []byte) (int, error) {
	if !h.wrote {
		h.WriteHeader(http.StatusOK)
	}
	if h.held {
		if h.body.Len() < maxErrorBody {
			h.body.Write(p)
		}
		return len(p), nil
	}
	return h.ResponseWriter.Write(p)
}

// pass writes a held answer after all: it was the call's own refusal.
func (h *heldAnswer) pass() {
	h.release()
	_, _ = h.ResponseWriter.Write(h.body.Bytes())
}

// ResponsesViaChat answers w one Responses request (body) by a chat
// completion made through plane at t: the request rewritten for
// upstreamModel, the answer rewritten back as it arrives, asked echoed as
// its model. It is what the connector does for an upstream that has no
// Responses API. sent is the size of the request made; ok is false when
// the request could not be translated — w then holds the refusal and
// nothing was sent.
func ResponsesViaChat(ctx context.Context, plane Connector, w http.ResponseWriter, t Target, body []byte, asked, upstreamModel string, h, answer http.Header) (res Outcome, sent int, ok bool) {
	chat, x, ref := responsesToChat(body, asked, upstreamModel)
	if ref != nil {
		ref.write(w)
		return Outcome{Status: ref.status}, 0, false
	}
	tw := newRespWriter(w, x)
	res = plane.Relay(ctx, tw, Call{Target: t, Path: sdkllm.PathChat, Body: chat, Header: h, Sink: usageSink, Answer: answer})
	tw.done(res)
	return res, len(chat), true
}

// ---- the request ------------------------------------------------------------------------

// xlate is what the answer's translation needs of the request.
type xlate struct {
	stream bool
	model  string          // as the caller named it: echoed in the answer
	custom map[string]bool // tools that were "custom" (free-form): their calls go back as custom_tool_call
}

type chatMsg struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func xlateRefuse(format string, a ...any) *refusal {
	return refuse(http.StatusBadRequest, "xbin_llm_translate",
		fmt.Sprintf(format, a...)+" — this upstream serves chat completions only, and the connector translates Responses calls for it (docs: /docs/llm.md, \"Responses on a chat-only upstream\")")
}

// responsesToChat rewrites a Responses request as a chat completion for
// upstreamModel. asked is the model as the caller named it.
func responsesToChat(body []byte, asked, upstreamModel string) ([]byte, *xlate, *refusal) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, nil, refuse(http.StatusBadRequest, "xbin_llm_body", "the request body is not a JSON object")
	}
	isSet := func(k string) bool {
		v := strings.TrimSpace(string(in[k]))
		return v != "" && v != "null"
	}
	if isSet("previous_response_id") || isSet("conversation") {
		return nil, nil, xlateRefuse("previous_response_id and stored conversations are OpenAI's own service: send the conversation in input")
	}
	x := &xlate{model: asked, custom: map[string]bool{}}
	out := map[string]any{"model": upstreamModel}
	if isSet("stream") {
		_ = json.Unmarshal(in["stream"], &x.stream)
	}
	if x.stream {
		out["stream"] = true
		out["stream_options"] = map[string]any{"include_usage": true}
	}

	var msgs []chatMsg
	if isSet("instructions") {
		var s string
		if json.Unmarshal(in["instructions"], &s) != nil {
			return nil, nil, xlateRefuse("instructions must be a string")
		}
		if s != "" {
			msgs = append(msgs, chatMsg{Role: "system", Content: s})
		}
	}
	more, ref := xlateInput(in["input"])
	if ref != nil {
		return nil, nil, ref
	}
	msgs = append(msgs, more...)
	if len(msgs) == 0 {
		return nil, nil, xlateRefuse("the request has no input")
	}
	out["messages"] = msgs

	if isSet("tools") {
		var tools []map[string]json.RawMessage
		if json.Unmarshal(in["tools"], &tools) != nil {
			return nil, nil, xlateRefuse("tools must be a list")
		}
		var ct []any
		for _, t := range tools {
			var typ, name, desc string
			_ = json.Unmarshal(t["type"], &typ)
			_ = json.Unmarshal(t["name"], &name)
			_ = json.Unmarshal(t["description"], &desc)
			fn := map[string]any{"name": name}
			if desc != "" {
				fn["description"] = desc
			}
			switch typ {
			case "function":
				if p := t["parameters"]; len(p) > 0 && string(p) != "null" {
					fn["parameters"] = p
				}
				if s := t["strict"]; len(s) > 0 && string(s) != "null" {
					fn["strict"] = s
				}
			case "custom": // free-form text in: one string argument
				x.custom[name] = true
				fn["parameters"] = map[string]any{"type": "object", "required": []string{"input"},
					"properties": map[string]any{"input": map[string]any{"type": "string", "description": "the tool's input, as plain text"}}}
			default:
				continue // a hosted tool of OpenAI's own service: this upstream has none
			}
			if name == "" {
				return nil, nil, xlateRefuse("a %s tool has no name", typ)
			}
			ct = append(ct, map[string]any{"type": "function", "function": fn})
		}
		if len(ct) > 0 {
			out["tools"] = ct
		}
	}
	if isSet("tool_choice") && out["tools"] != nil {
		var s string
		if json.Unmarshal(in["tool_choice"], &s) == nil {
			out["tool_choice"] = s
		} else {
			var tc struct{ Type, Name string }
			if json.Unmarshal(in["tool_choice"], &tc) == nil && tc.Name != "" && (tc.Type == "function" || tc.Type == "custom") {
				out["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": tc.Name}}
			}
		}
	}
	if out["tools"] != nil && isSet("parallel_tool_calls") {
		out["parallel_tool_calls"] = in["parallel_tool_calls"]
	}
	for from, to := range map[string]string{"max_output_tokens": "max_tokens", "temperature": "temperature", "top_p": "top_p",
		"user": "user", "seed": "seed", "top_logprobs": "top_logprobs"} {
		if isSet(from) {
			out[to] = in[from]
		}
	}
	if isSet("reasoning") {
		var r struct {
			Effort string `json:"effort"`
		}
		if json.Unmarshal(in["reasoning"], &r) == nil && r.Effort != "" {
			out["reasoning_effort"] = r.Effort
		}
	}
	if isSet("text") {
		var t struct {
			Format map[string]json.RawMessage `json:"format"`
		}
		if json.Unmarshal(in["text"], &t) == nil && t.Format != nil {
			var typ string
			_ = json.Unmarshal(t.Format["type"], &typ)
			switch typ {
			case "json_object":
				out["response_format"] = map[string]any{"type": "json_object"}
			case "json_schema":
				js := map[string]any{}
				for _, k := range []string{"name", "schema", "strict", "description"} {
					if v := t.Format[k]; len(v) > 0 && string(v) != "null" {
						js[k] = v
					}
				}
				out["response_format"] = map[string]any{"type": "json_schema", "json_schema": js}
			}
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, nil, refuse(http.StatusBadRequest, "xbin_llm_body", "the request body could not be rewritten for the upstream")
	}
	return b, x, nil
}

// xlateInput turns Responses input — a string, or a list of items — into
// chat messages. Function calls that follow one another (and the assistant
// text before them) become one assistant message, as chat wants them.
func xlateInput(raw json.RawMessage) ([]chatMsg, *refusal) {
	if s := strings.TrimSpace(string(raw)); s == "" || s == "null" {
		return nil, nil
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return []chatMsg{{Role: "user", Content: str}}, nil
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil, xlateRefuse("input must be a string or a list of items")
	}
	var out []chatMsg
	var open *chatMsg // an assistant message still taking tool calls
	flush := func() {
		if open != nil {
			out = append(out, *open)
			open = nil
		}
	}
	str1 := func(m map[string]json.RawMessage, k string) string {
		var s string
		_ = json.Unmarshal(m[k], &s)
		return s
	}
	call := func(id, name, args string) {
		if open == nil {
			open = &chatMsg{Role: "assistant", Content: nil}
		}
		tc := chatToolCall{ID: id, Type: "function"}
		tc.Function.Name, tc.Function.Arguments = name, args
		open.ToolCalls = append(open.ToolCalls, tc)
	}
	for _, it := range items {
		typ := str1(it, "type")
		if typ == "" && len(it["role"]) > 0 {
			typ = "message"
		}
		switch typ {
		case "message":
			role := str1(it, "role")
			if role == "developer" {
				role = "system"
			}
			content, ref := xlateContent(it["content"], role)
			if ref != nil {
				return nil, ref
			}
			flush()
			if role == "assistant" {
				open = &chatMsg{Role: role, Content: content}
			} else {
				out = append(out, chatMsg{Role: role, Content: content})
			}
		case "function_call":
			call(str1(it, "call_id"), str1(it, "name"), str1(it, "arguments"))
		case "custom_tool_call":
			args, _ := json.Marshal(map[string]string{"input": str1(it, "input")})
			call(str1(it, "call_id"), str1(it, "name"), string(args))
		case "function_call_output", "custom_tool_call_output":
			flush()
			out = append(out, chatMsg{Role: "tool", ToolCallID: str1(it, "call_id"), Content: xlateOutput(it["output"])})
		case "reasoning":
			// the provider's own, opaque: not this upstream's to read
		default:
			return nil, xlateRefuse("an input item of type %q can't be sent as a chat completion", typ)
		}
	}
	flush()
	return out, nil
}

// xlateContent is a message's content for chat: a string where it is only
// text, parts where it has images.
func xlateContent(raw json.RawMessage, role string) (any, *refusal) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return "", nil
	}
	var text []string
	var out []any
	images := false
	for _, p := range parts {
		var typ, t string
		_ = json.Unmarshal(p["type"], &typ)
		switch typ {
		case "input_text", "output_text", "text", "refusal", "summary_text":
			if json.Unmarshal(p["text"], &t) != nil {
				_ = json.Unmarshal(p["refusal"], &t)
			}
			text = append(text, t)
			out = append(out, map[string]any{"type": "text", "text": t})
		case "input_image":
			var u string
			if json.Unmarshal(p["image_url"], &u) != nil || u == "" {
				return nil, xlateRefuse("an input_image without an image_url (a file id is OpenAI's own service)")
			}
			images = true
			img := map[string]any{"url": u}
			var detail string
			if json.Unmarshal(p["detail"], &detail) == nil && detail != "" {
				img["detail"] = detail
			}
			out = append(out, map[string]any{"type": "image_url", "image_url": img})
		default:
			return nil, xlateRefuse("a content part of type %q can't be sent as a chat completion", typ)
		}
	}
	if !images || role != "user" {
		return strings.Join(text, ""), nil
	}
	return out, nil
}

// xlateOutput is a tool's output as chat's tool message carries it: text.
func xlateOutput(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type, Text string
	}
	if json.Unmarshal(raw, &parts) == nil && len(parts) > 0 {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return string(raw)
}

// ---- the answer -------------------------------------------------------------------------

// chatChunk is a chat completion, whole or one chunk of its stream.
type chatChunk struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	Choices []struct {
		Delta        *chatDelta `json:"delta"`
		Message      *chatDelta `json:"message"`
		FinishReason *string    `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		TotalTokens         int64 `json:"total_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails *struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
		Type    string `json:"type"`
	} `json:"error"`
}

type chatDelta struct {
	Content          *string `json:"content"`
	Reasoning        *string `json:"reasoning"`
	ReasoningContent *string `json:"reasoning_content"`
	ToolCalls        []struct {
		Index    *int   `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// respItem is one output item of the answer being built.
type respItem struct {
	kind   string // reasoning | message | function_call | custom_tool_call
	id     string
	index  int
	text   strings.Builder // a message's or the reasoning's text; a call's arguments
	callID string
	name   string
}

func (it *respItem) object(done bool) map[string]any {
	status := "in_progress"
	if done {
		status = "completed"
	}
	switch it.kind {
	case "reasoning":
		sum := []any{}
		if done && it.text.Len() > 0 {
			sum = append(sum, map[string]any{"type": "summary_text", "text": it.text.String()})
		}
		return map[string]any{"id": it.id, "type": "reasoning", "summary": sum}
	case "message":
		content := []any{}
		if done {
			content = append(content, map[string]any{"type": "output_text", "text": it.text.String(), "annotations": []any{}})
		}
		return map[string]any{"id": it.id, "type": "message", "status": status, "role": "assistant", "content": content}
	case "custom_tool_call":
		input := ""
		if done {
			input = customInput(it.text.String())
		}
		return map[string]any{"id": it.id, "type": "custom_tool_call", "status": status, "call_id": it.callID, "name": it.name, "input": input}
	}
	args := ""
	if done {
		args = it.text.String()
	}
	return map[string]any{"id": it.id, "type": "function_call", "status": status, "call_id": it.callID, "name": it.name, "arguments": args}
}

// customInput is a free-form tool's input out of the one-argument call the
// model made for it.
func customInput(args string) string {
	var a struct {
		Input *string `json:"input"`
	}
	if json.Unmarshal([]byte(args), &a) == nil && a.Input != nil {
		return *a.Input
	}
	return args
}

// respWriter is the ResponseWriter a translated call's upstream answer is
// written to: it writes the Responses form of it to the client. An answer
// that is no success passes as it is.
type respWriter struct {
	w      http.ResponseWriter
	x      *xlate
	status int
	stream bool
	plain  bool // not a success: passed through
	head   bool
	buf    bytes.Buffer // a stream's unfinished line; a plain answer's body
	werr   error

	id      string
	created int64
	seq     int
	started bool
	ended   bool
	items   []*respItem
	msg     *respItem
	think   *respItem
	calls   map[int]*respItem
	finish  string
	usage   map[string]any
}

func newRespWriter(w http.ResponseWriter, x *xlate) *respWriter {
	return &respWriter{w: w, x: x, calls: map[int]*respItem{}}
}

func (t *respWriter) Unwrap() http.ResponseWriter { return t.w }

// FlushError flushes the client's side — except for a plain success, whose
// body (and status) are written whole at the end (done).
func (t *respWriter) FlushError() error {
	if !t.head || !t.plain && !t.stream {
		return nil
	}
	return http.NewResponseController(t.w).Flush()
}
func (t *respWriter) Header() http.Header { return t.w.Header() }

func (t *respWriter) WriteHeader(code int) {
	if t.head {
		return
	}
	t.head, t.status = true, code
	h := t.w.Header()
	t.stream = strings.HasPrefix(strings.ToLower(h.Get("Content-Type")), "text/event-stream")
	t.plain = code < 200 || code >= 300
	if !t.plain && !t.stream {
		return // the body is rewritten whole: its status goes out with it (done)
	}
	h.Del("Content-Length")
	t.w.WriteHeader(code)
}

func (t *respWriter) Write(p []byte) (int, error) {
	if !t.head {
		t.WriteHeader(http.StatusOK)
	}
	switch {
	case t.plain:
		return t.w.Write(p)
	case !t.stream:
		t.buf.Write(p)
		return len(p), nil
	}
	t.buf.Write(p)
	for {
		raw := t.buf.Bytes()
		i := bytes.IndexByte(raw, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(raw[:i]), "\r")
		t.buf.Next(i + 1)
		t.line(line)
	}
	return len(p), t.werr
}

func (t *respWriter) line(line string) {
	data, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return // event names, comments, blank lines
	}
	data = strings.TrimSpace(data)
	if data == "" {
		return
	}
	if data == "[DONE]" {
		t.complete()
		return
	}
	var c chatChunk
	if json.Unmarshal([]byte(data), &c) != nil {
		return
	}
	t.chunk(&c)
}

func (t *respWriter) emit(typ string, fields map[string]any) {
	if t.werr != nil {
		return
	}
	fields["type"] = typ
	fields["sequence_number"] = t.seq
	t.seq++
	b, err := json.Marshal(fields)
	if err != nil {
		return
	}
	_, t.werr = fmt.Fprintf(t.w, "event: %s\ndata: %s\n\n", typ, b)
}

func (t *respWriter) response(status string) map[string]any {
	out := []any{}
	for _, it := range t.items {
		out = append(out, it.object(status != "in_progress"))
	}
	r := map[string]any{"id": t.id, "object": "response", "created_at": t.created, "status": status, "model": t.x.model,
		"output": out, "error": nil, "incomplete_details": nil, "parallel_tool_calls": true, "tools": []any{}}
	if t.usage != nil {
		r["usage"] = t.usage
	}
	return r
}

func (t *respWriter) start(c *chatChunk) {
	if t.started {
		return
	}
	t.started = true
	t.id, t.created = "resp_"+strings.TrimPrefix(c.ID, "chatcmpl-"), c.Created
	if c.ID == "" {
		t.id = "resp_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	if t.created == 0 {
		t.created = time.Now().Unix()
	}
	if t.stream {
		t.emit("response.created", map[string]any{"response": t.response("in_progress")})
		t.emit("response.in_progress", map[string]any{"response": t.response("in_progress")})
	}
}

func (t *respWriter) add(kind, prefix string) *respItem {
	it := &respItem{kind: kind, index: len(t.items)}
	it.id = prefix + "_" + strings.TrimPrefix(t.id, "resp_") + "_" + strconv.Itoa(it.index)
	t.items = append(t.items, it)
	return it
}

func (t *respWriter) added(it *respItem) {
	if t.stream {
		t.emit("response.output_item.added", map[string]any{"output_index": it.index, "item": it.object(false)})
	}
}

func (t *respWriter) chunk(c *chatChunk) {
	if c.Error != nil && len(c.Choices) == 0 {
		t.start(c)
		t.fail(fmt.Sprint(c.Error.Code), c.Error.Message)
		return
	}
	t.start(c)
	if u := c.Usage; u != nil {
		in := map[string]any{"cached_tokens": 0}
		if u.PromptTokensDetails != nil {
			in["cached_tokens"] = u.PromptTokensDetails.CachedTokens
		}
		out := map[string]any{"reasoning_tokens": 0}
		if u.CompletionTokensDetails != nil {
			out["reasoning_tokens"] = u.CompletionTokensDetails.ReasoningTokens
		}
		total := u.TotalTokens
		if total == 0 {
			total = u.PromptTokens + u.CompletionTokens
		}
		t.usage = map[string]any{"input_tokens": u.PromptTokens, "input_tokens_details": in,
			"output_tokens": u.CompletionTokens, "output_tokens_details": out, "total_tokens": total}
	}
	for _, ch := range c.Choices {
		d := ch.Delta
		if d == nil {
			d = ch.Message
		}
		if d != nil {
			t.delta(d)
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			t.finish = *ch.FinishReason
		}
	}
}

func (t *respWriter) delta(d *chatDelta) {
	think := d.ReasoningContent
	if think == nil {
		think = d.Reasoning
	}
	if think != nil && *think != "" {
		if t.think == nil {
			t.think = t.add("reasoning", "rs")
			t.added(t.think)
			if t.stream {
				t.emit("response.reasoning_summary_part.added", map[string]any{"item_id": t.think.id, "output_index": t.think.index, "summary_index": 0,
					"part": map[string]any{"type": "summary_text", "text": ""}})
			}
		}
		t.think.text.WriteString(*think)
		if t.stream {
			t.emit("response.reasoning_summary_text.delta", map[string]any{"item_id": t.think.id, "output_index": t.think.index, "summary_index": 0, "delta": *think})
		}
	}
	if d.Content != nil && *d.Content != "" {
		if t.msg == nil {
			t.msg = t.add("message", "msg")
			t.added(t.msg)
			if t.stream {
				t.emit("response.content_part.added", map[string]any{"item_id": t.msg.id, "output_index": t.msg.index, "content_index": 0,
					"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
			}
		}
		t.msg.text.WriteString(*d.Content)
		if t.stream {
			t.emit("response.output_text.delta", map[string]any{"item_id": t.msg.id, "output_index": t.msg.index, "content_index": 0, "delta": *d.Content})
		}
	}
	for n, tc := range d.ToolCalls {
		idx := n
		if tc.Index != nil {
			idx = *tc.Index
		}
		it := t.calls[idx]
		if it == nil {
			kind, prefix := "function_call", "fc"
			if t.x.custom[tc.Function.Name] {
				kind, prefix = "custom_tool_call", "ctc"
			}
			it = t.add(kind, prefix)
			it.callID, it.name = tc.ID, tc.Function.Name
			if it.callID == "" {
				it.callID = "call_" + it.id
			}
			t.calls[idx] = it
			t.added(it)
		}
		if tc.Function.Arguments != "" {
			it.text.WriteString(tc.Function.Arguments)
			if t.stream && it.kind == "function_call" {
				t.emit("response.function_call_arguments.delta", map[string]any{"item_id": it.id, "output_index": it.index, "delta": tc.Function.Arguments})
			}
		}
	}
}

// closeItems ends every output item, in order.
func (t *respWriter) closeItems() {
	if !t.stream {
		return
	}
	for _, it := range t.items {
		switch it.kind {
		case "reasoning":
			t.emit("response.reasoning_summary_text.done", map[string]any{"item_id": it.id, "output_index": it.index, "summary_index": 0, "text": it.text.String()})
			t.emit("response.reasoning_summary_part.done", map[string]any{"item_id": it.id, "output_index": it.index, "summary_index": 0,
				"part": map[string]any{"type": "summary_text", "text": it.text.String()}})
		case "message":
			t.emit("response.output_text.done", map[string]any{"item_id": it.id, "output_index": it.index, "content_index": 0, "text": it.text.String()})
			t.emit("response.content_part.done", map[string]any{"item_id": it.id, "output_index": it.index, "content_index": 0,
				"part": map[string]any{"type": "output_text", "text": it.text.String(), "annotations": []any{}}})
		case "function_call":
			t.emit("response.function_call_arguments.done", map[string]any{"item_id": it.id, "output_index": it.index, "arguments": it.text.String()})
		case "custom_tool_call":
			t.emit("response.custom_tool_call_input.done", map[string]any{"item_id": it.id, "output_index": it.index, "input": customInput(it.text.String())})
		}
		t.emit("response.output_item.done", map[string]any{"output_index": it.index, "item": it.object(true)})
	}
}

// complete ends the answer as the upstream ended it.
func (t *respWriter) complete() {
	if t.ended || !t.started {
		return
	}
	t.ended = true
	t.closeItems()
	if t.finish == "length" {
		r := t.response("incomplete")
		r["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
		t.emit("response.incomplete", map[string]any{"response": r})
		return
	}
	t.emit("response.completed", map[string]any{"response": t.response("completed")})
}

func (t *respWriter) fail(code, msg string) {
	if t.ended {
		return
	}
	t.ended = true
	r := t.response("failed")
	r["error"] = map[string]any{"code": code, "message": msg}
	t.emit("response.failed", map[string]any{"response": r})
}

// done is called when the upstream's answer has ended, however: a plain
// answer is rewritten and written now; a stream that broke off before its
// end is ended as failed, so no client waits on it.
func (t *respWriter) done(o Outcome) {
	switch {
	case !t.head || t.plain:
		return
	case t.stream:
		if !t.ended {
			if !t.started {
				t.start(&chatChunk{})
			}
			if o.Err == "" && t.finish != "" { // it ended without [DONE]: some servers do
				t.complete()
				return
			}
			t.fail("upstream_error", "the upstream's answer broke off before it ended")
		}
		return
	}
	var c chatChunk
	if err := json.Unmarshal(t.buf.Bytes(), &c); err != nil || len(c.Choices) == 0 {
		t.w.Header().Set("Content-Type", "application/json")
		t.w.WriteHeader(http.StatusBadGateway)
		b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": "the upstream's chat completion could not be read as one", "type": "xbin_error", "code": "xbin_llm_translate"}})
		_, _ = t.w.Write(b)
		return
	}
	t.chunk(&c)
	status := "completed"
	r := t.response(status)
	if t.finish == "length" {
		r["status"], r["incomplete_details"] = "incomplete", map[string]any{"reason": "max_output_tokens"}
	}
	b, _ := json.Marshal(r)
	t.w.Header().Set("Content-Type", "application/json")
	t.w.WriteHeader(t.status)
	_, _ = t.w.Write(b)
}
