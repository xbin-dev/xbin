package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fastWires makes retries instant and returns a restore func.
func fastWires(t *testing.T) {
	t.Helper()
	old := wireBackoff
	wireBackoff = func(ctx context.Context, attempt int) bool { return ctx.Err() == nil }
	t.Cleanup(func() { wireBackoff = old })
}

func testGW(srv *httptest.Server) *gatewayLLM {
	return &gatewayLLM{client: srv.Client(), base: srv.URL}
}

// sse writes data lines (and flushes after each) as a streaming response.
func sse(w http.ResponseWriter, lines ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	fl, _ := w.(http.Flusher)
	for _, l := range lines {
		fmt.Fprintf(w, "data: %s\n\n", l)
		if fl != nil {
			fl.Flush()
		}
	}
}

type evLog struct{ evs []LLMEvent }

func (l *evLog) on(ev LLMEvent) { l.evs = append(l.evs, ev) }

func (l *evLog) last(kind string) LLMEvent {
	for i := len(l.evs) - 1; i >= 0; i-- {
		if l.evs[i].Kind == kind {
			return l.evs[i]
		}
	}
	return LLMEvent{}
}

func chatStream(t *testing.T, lines ...string) (LLMReply, *evLog, error) {
	t.Helper()
	fastWires(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		sse(w, append(lines, "[DONE]")...)
	}))
	defer srv.Close()
	var log evLog
	rep, err := testGW(srv).Chat(context.Background(), LLMRequest{Model: "m", Stream: true, Wire: "chat"}, log.on)
	return rep, &log, err
}

func TestChatStreamReasoningContent(t *testing.T) {
	rep, log, err := chatStream(t,
		`{"choices":[{"delta":{"reasoning_content":"Let me"}}]}`,
		`{"choices":[{"delta":{"reasoning_content":" think"}}]}`,
		`{"choices":[{"delta":{"content":"Hello"}}]}`,
		`{"choices":[{"delta":{"content":" world"},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30,"completion_tokens_details":{"reasoning_tokens":7}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reasoning != "Let me think" || asString(rep.Msg.Content) != "Hello world" {
		t.Fatalf("reasoning %q text %q", rep.Reasoning, asString(rep.Msg.Content))
	}
	if rep.Usage.ReasoningTokens != 7 || rep.Usage.PromptTokens != 10 || rep.Finish != "stop" || rep.Wire != "chat" || rep.Model != "m" {
		t.Fatalf("reply meta %+v", rep)
	}
	if got := log.last("thinking").Text; got != "Let me think" {
		t.Fatalf("thinking events carry accumulated text: %q", got)
	}
	if got := log.last("text").Text; got != "Hello world" {
		t.Fatalf("text events carry accumulated text: %q", got)
	}
	if log.evs[0].Kind != "thinking" || log.evs[0].Text != "Let me" {
		t.Fatalf("first event %+v", log.evs[0])
	}
}

func TestChatStreamReasoningAndDetails(t *testing.T) {
	// OpenRouter sends both: plain reasoning for display, details for replay.
	rep, _, err := chatStream(t,
		`{"choices":[{"delta":{"reasoning":"Hmm","reasoning_details":[{"type":"reasoning.text","text":"Hmm","index":0}]}}]}`,
		`{"choices":[{"delta":{"reasoning":" ok","reasoning_details":[{"type":"reasoning.text","text":" ok","index":0}]}}]}`,
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","index":0,"signature":"sig"}]}}]}`,
		`{"choices":[{"delta":{"content":"done"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reasoning != "Hmm ok" {
		t.Fatalf("plain reasoning wins for display, once each: %q", rep.Reasoning)
	}
	var items []map[string]any
	if json.Unmarshal(rep.ReasoningRaw, &items) != nil || len(items) != 1 {
		t.Fatalf("fragments merge into one block: %s", rep.ReasoningRaw)
	}
	if items[0]["text"] != "Hmm ok" || items[0]["signature"] != "sig" {
		t.Fatalf("merged block %v", items[0])
	}
}

func TestChatStreamReasoningDetailsOnly(t *testing.T) {
	rep, log, err := chatStream(t,
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.summary","summary":"First","index":0}]}}]}`,
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.summary","summary":", then","index":0}]}}]}`,
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.encrypted","data":"XYZ","index":1}]}}]}`,
		`{"choices":[{"delta":{"content":"ans"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reasoning != "First, then" || log.last("thinking").Text != "First, then" {
		t.Fatalf("details text is the display reasoning when nothing plain came: %q", rep.Reasoning)
	}
	var items []map[string]any
	_ = json.Unmarshal(rep.ReasoningRaw, &items)
	if len(items) != 2 || items[1]["data"] != "XYZ" {
		t.Fatalf("raw blocks %s", rep.ReasoningRaw)
	}
}

func TestChatThinkTagSplitAcrossChunks(t *testing.T) {
	rep, log, err := chatStream(t,
		`{"choices":[{"delta":{"content":"\n<thi"}}]}`,
		`{"choices":[{"delta":{"content":"nk>pondering"}}]}`,
		`{"choices":[{"delta":{"content":" deeply</th"}}]}`,
		`{"choices":[{"delta":{"content":"ink>\n\nAnswer"}}]}`,
		`{"choices":[{"delta":{"content":" here"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reasoning != "pondering deeply" || asString(rep.Msg.Content) != "Answer here" {
		t.Fatalf("reasoning %q text %q", rep.Reasoning, asString(rep.Msg.Content))
	}
	for _, ev := range log.evs {
		if ev.Kind == "text" && strings.Contains(ev.Text, "think") {
			t.Fatalf("a tag leaked into text: %q", ev.Text)
		}
	}
	// Content that merely starts with '<' is text.
	rep, _, _ = chatStream(t, `{"choices":[{"delta":{"content":"<b"}}]}`, `{"choices":[{"delta":{"content":">bold"}}]}`)
	if asString(rep.Msg.Content) != "<b>bold" || rep.Reasoning != "" {
		t.Fatalf("plain '<' content: %q / %q", asString(rep.Msg.Content), rep.Reasoning)
	}
	// An unterminated think block ends as thought, not text.
	rep, _, _ = chatStream(t, `{"choices":[{"delta":{"content":"<think>still going </thi"}}]}`)
	if asString(rep.Msg.Content) != "" || rep.Reasoning != "still going </thi" {
		t.Fatalf("unterminated: text %q reasoning %q", asString(rep.Msg.Content), rep.Reasoning)
	}
}

func TestChatToolCallDeltas(t *testing.T) {
	rep, log, err := chatStream(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"file_read","arguments":"{\"pa"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","function":{"name":"note","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Msg.ToolCalls) != 2 || rep.Msg.ToolCalls[0].Function.Arguments != `{"path":"a"}` || rep.Msg.ToolCalls[1].ID != "c2" {
		t.Fatalf("calls %+v", rep.Msg.ToolCalls)
	}
	if rep.Finish != "tool_calls" {
		t.Fatalf("finish %q", rep.Finish)
	}
	var partial []string
	for _, ev := range log.evs {
		if ev.Kind == "tool" && ev.Index == 0 {
			partial = append(partial, ev.Args)
		}
	}
	if len(partial) != 2 || partial[0] != `{"pa` || partial[1] != `{"path":"a"}` {
		t.Fatalf("partial args events %q", partial)
	}
	if ev := log.last("tool"); ev.Index != 1 || ev.Name != "note" || ev.ID != "c2" {
		t.Fatalf("second call event %+v", ev)
	}
}

func TestChatErrorChunkIsNotRetried(t *testing.T) {
	fastWires(t)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		sse(w, `{"error":{"message":"model overloaded","code":529}}`)
	}))
	defer srv.Close()
	_, err := testGW(srv).Chat(context.Background(), LLMRequest{Model: "m", Stream: true, Wire: "chat"}, nil)
	if err == nil || !strings.Contains(err.Error(), "model overloaded") {
		t.Fatalf("err %v", err)
	}
	if n.Load() != 1 {
		t.Fatalf("an upstream error chunk is final: %d attempts", n.Load())
	}
}

// truncated answers 200 with a Content-Length it never fills, then drops the
// connection: a transport error mid-body.
func truncated(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Length", "100000")
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	_, _ = io.WriteString(w, body)
	w.(http.Flusher).Flush()
}

func TestChatRetriesOnlyBeforeTheFirstToken(t *testing.T) {
	fastWires(t)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch n.Add(1) {
		case 1:
			http.Error(w, "busy", http.StatusServiceUnavailable)
		case 2:
			truncated(w, "") // dropped before anything
		default:
			sse(w, `{"choices":[{"delta":{"content":"ok"}}]}`, "[DONE]")
		}
	}))
	defer srv.Close()
	rep, err := testGW(srv).Chat(context.Background(), LLMRequest{Model: "m", Stream: true, Wire: "chat"}, nil)
	if err != nil || asString(rep.Msg.Content) != "ok" || n.Load() != 3 {
		t.Fatalf("rep %q err %v attempts %d", asString(rep.Msg.Content), err, n.Load())
	}

	n.Store(0)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		truncated(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	}))
	defer srv2.Close()
	var log evLog
	rep, err = testGW(srv2).Chat(context.Background(), LLMRequest{Model: "m", Stream: true, Wire: "chat"}, log.on)
	if err == nil {
		t.Fatal("a stream cut after tokens is an error")
	}
	if n.Load() != 1 || asString(rep.Msg.Content) != "partial" || log.last("text").Text != "partial" {
		t.Fatalf("no retry after tokens flowed: attempts %d partial %q", n.Load(), asString(rep.Msg.Content))
	}
}

func TestChatNonStreaming(t *testing.T) {
	fastWires(t)
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"<think>why</think>because","tool_calls":[{"id":"t1","type":"function","function":{"name":"note","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	}))
	defer srv.Close()
	rep, err := testGW(srv).Chat(context.Background(), LLMRequest{Model: "m", Wire: "chat", ReasoningEffort: "low"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reasoning != "why" || asString(rep.Msg.Content) != "because" || len(rep.Msg.ToolCalls) != 1 || rep.Usage.TotalTokens != 7 || rep.Finish != "tool_calls" {
		t.Fatalf("rep %+v", rep)
	}
	if _, ok := body["stream"]; ok || body["reasoning_effort"] != "low" {
		t.Fatalf("request %v", body)
	}
}

func TestChatReplaysDetailsOnlyToTheSameModelAndWire(t *testing.T) {
	raw := json.RawMessage(`[{"type":"reasoning.text","text":"r"}]`)
	msgs := []wireMsg{
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "a", Replay: &msgMeta{Wire: "chat", Model: "A", ReasoningRaw: raw}},
		{Role: "assistant", Content: "b", Replay: &msgMeta{Wire: "responses", Model: "A", ReasoningRaw: raw}},
	}
	enc := func(model string) []map[string]any {
		b, err := chatCodec{}.encode(LLMRequest{Model: model, Msgs: msgs})
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.Unmarshal(b, &out)
		return out.Messages
	}
	m := enc("A")
	if m[1]["reasoning_details"] == nil || m[2]["reasoning_details"] != nil || m[0]["reasoning_details"] != nil {
		t.Fatalf("same model/wire only: %v", m)
	}
	if enc("B")[1]["reasoning_details"] != nil {
		t.Fatal("another model never gets it")
	}
	for _, k := range []string{"Replay", "replay"} {
		if _, ok := m[1][k]; ok {
			t.Fatal("the meta itself never goes on the wire")
		}
	}
}

func TestResponsesStream(t *testing.T) {
	fastWires(t)
	var req map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		for _, ev := range []string{
			`{"type":"response.created","response":{"status":"in_progress"}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"Plan"}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":" steps"}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":1,"delta":"Check"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"ENC","summary":[{"type":"summary_text","text":"Plan steps"},{"type":"summary_text","text":"Check"}]}}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"Hi"}`,
			`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":" there"}`,
			`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"note","arguments":""}}`,
			`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"text\":"}`,
			`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"\"x\"}"}`,
			`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"note","arguments":"{\"text\":\"x\"}"}}`,
			`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":100,"output_tokens":40,"output_tokens_details":{"reasoning_tokens":30},"total_tokens":140}}}`,
		} {
			var t struct{ Type string }
			_ = json.Unmarshal([]byte(ev), &t)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", t.Type, ev)
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()
	var log evLog
	rep, err := testGW(srv).Chat(context.Background(), LLMRequest{Model: "gpt-5", Stream: true, Wire: "auto",
		Msgs: []wireMsg{{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}}}, log.on)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/responses" || rep.Wire != "responses" {
		t.Fatalf("path %s wire %s", path, rep.Wire)
	}
	if rep.Reasoning != "Plan steps\n\nCheck" || log.last("thinking").Text != "Plan steps\n\nCheck" {
		t.Fatalf("reasoning %q", rep.Reasoning)
	}
	if asString(rep.Msg.Content) != "Hi there" || len(rep.Msg.ToolCalls) != 1 {
		t.Fatalf("text %q calls %+v", asString(rep.Msg.Content), rep.Msg.ToolCalls)
	}
	tc := rep.Msg.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "note" || tc.Function.Arguments != `{"text":"x"}` || rep.Finish != "tool_calls" {
		t.Fatalf("call %+v finish %s", tc, rep.Finish)
	}
	if ev := log.last("tool"); ev.Index != 0 || ev.Args != `{"text":"x"}` || ev.ID != "call_1" {
		t.Fatalf("tool event %+v", ev)
	}
	if rep.Usage.PromptTokens != 100 || rep.Usage.CompletionTokens != 40 || rep.Usage.ReasoningTokens != 30 || rep.Usage.TotalTokens != 140 {
		t.Fatalf("usage %+v", rep.Usage)
	}
	var items []map[string]any
	if json.Unmarshal(rep.ReasoningRaw, &items) != nil || len(items) != 1 || items[0]["encrypted_content"] != "ENC" {
		t.Fatalf("reasoning items kept raw for replay: %s", rep.ReasoningRaw)
	}
	if req["store"] != false || req["stream"] != true || req["instructions"] != "sys" {
		t.Fatalf("request %v", req)
	}
	if r, _ := req["reasoning"].(map[string]any); r["summary"] != "auto" {
		t.Fatalf("summaries requested: %v", req["reasoning"])
	}
	if inc, _ := req["include"].([]any); len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Fatalf("include %v", req["include"])
	}
}

func TestResponsesEncodeInput(t *testing.T) {
	parts := json.RawMessage(`[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,xx"}}]`)
	tc := toolCall{ID: "call_1", Type: "function"}
	tc.Function.Name, tc.Function.Arguments = "note", `{"text":"x"}`
	msgs := []wireMsg{
		{Role: "system", Content: "S"},
		{Role: "user", Content: "u1"},
		{Role: "system", Content: "S2"},
		{Role: "user", Content: parts},
		{Role: "assistant", Content: "ok", ToolCalls: []toolCall{tc},
			Replay: &msgMeta{Wire: "responses", Model: "gpt-5", ReasoningRaw: json.RawMessage(`[{"type":"reasoning","id":"rs_1","encrypted_content":"E","summary":[]}]`)}},
		{Role: "tool", ToolCallID: "call_1", Name: "note", Content: "noted"},
	}
	b, err := responsesCodec{}.encode(LLMRequest{Model: "gpt-5", Msgs: msgs,
		Tools: []toolSpec{{Type: "function", Function: funcDef{Name: "note", Description: "d", Parameters: obj(nil, map[string]any{})}}}})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Instructions string           `json:"instructions"`
		Input        []map[string]any `json:"input"`
		Tools        []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Instructions != "S" || len(out.Input) != 7 {
		t.Fatalf("instructions %q input %v", out.Instructions, out.Input)
	}
	in := out.Input
	if in[0]["role"] != "user" || in[0]["content"] != "u1" || in[1]["role"] != "developer" || in[1]["content"] != "S2" {
		t.Fatalf("messages %v", in[:2])
	}
	p, _ := in[2]["content"].([]any)
	if len(p) != 2 || p[0].(map[string]any)["type"] != "input_text" || p[1].(map[string]any)["image_url"] != "data:image/png;base64,xx" {
		t.Fatalf("parts %v", in[2])
	}
	if in[3]["type"] != "reasoning" || in[3]["encrypted_content"] != "E" {
		t.Fatalf("reasoning replayed before the assistant turn: %v", in[3])
	}
	if in[4]["role"] != "assistant" || in[4]["content"] != "ok" {
		t.Fatalf("assistant %v", in[4])
	}
	if in[5]["type"] != "function_call" || in[5]["call_id"] != "call_1" || in[5]["arguments"] != `{"text":"x"}` {
		t.Fatalf("call %v", in[5])
	}
	if in[6]["type"] != "function_call_output" || in[6]["call_id"] != "call_1" || in[6]["output"] != "noted" {
		t.Fatalf("output %v", in[6])
	}
	if len(out.Tools) != 1 || out.Tools[0]["name"] != "note" || out.Tools[0]["type"] != "function" {
		t.Fatalf("flat tools %v", out.Tools)
	}
	// Another model: no replay. A non-reasoning model: no reasoning params.
	b, _ = responsesCodec{}.encode(LLMRequest{Model: "gpt-4.1", Msgs: msgs})
	var other map[string]any
	_ = json.Unmarshal(b, &other)
	if strings.Contains(string(b), `"encrypted_content"`) || other["reasoning"] != nil || other["include"] != nil {
		t.Fatalf("gpt-4.1 request %s", b)
	}
}

func TestAutoWireAndResponsesFallback(t *testing.T) {
	fastWires(t)
	var respHits, chatHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			respHits.Add(1)
			http.Error(w, `{"error":"no such route"}`, http.StatusNotFound)
		case "/v1/chat/completions":
			chatHits.Add(1)
			sse(w, `{"choices":[{"delta":{"content":"via chat"}}]}`, "[DONE]")
		}
	}))
	defer srv.Close()
	gw := testGW(srv)
	model := "gpt-5-fallback-test"
	t.Cleanup(func() {
		responsesFallbackMu.Lock()
		delete(responsesFallback, model)
		responsesFallbackMu.Unlock()
	})
	for i := 0; i < 2; i++ {
		rep, err := gw.Chat(context.Background(), LLMRequest{Model: model, Stream: true}, nil)
		if err != nil || asString(rep.Msg.Content) != "via chat" || rep.Wire != "chat" {
			t.Fatalf("call %d: %q %v", i, asString(rep.Msg.Content), err)
		}
	}
	if respHits.Load() != 1 || chatHits.Load() != 2 {
		t.Fatalf("the refusal is remembered: responses %d chat %d", respHits.Load(), chatHits.Load())
	}
	// A forced Responses wire reports the refusal instead of falling back.
	_, err := gw.Chat(context.Background(), LLMRequest{Model: "gpt-5-forced-test", Stream: true, Wire: "responses"}, nil)
	if !errors.Is(err, errWireUnsupported) {
		t.Fatalf("forced: %v", err)
	}
	for model, want := range map[string]string{"llama-3.3-70b": "chat", "openai/o3-mini": "responses", "gpt-5.1": "responses", "gpt-4o": "chat"} {
		if got := pickWire("auto", model); got != want {
			t.Errorf("pickWire(%s) = %s, want %s", model, got, want)
		}
	}
	if pickWire("chat", "gpt-5") != "chat" || pickWire("responses", "llama") != "responses" {
		t.Error("an explicit wire is honoured")
	}
}

func TestIdleWatchdogAbortsAStalledStream(t *testing.T) {
	fastWires(t)
	old := llmIdleTimeout
	llmIdleTimeout = 150 * time.Millisecond
	t.Cleanup(func() { llmIdleTimeout = old })
	var n atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		sse(w, `{"choices":[{"delta":{"content":"so far"}}]}`)
		select { // then go quiet
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	start := time.Now()
	rep, err := testGW(srv).Chat(context.Background(), LLMRequest{Model: "m", Stream: true, Wire: "chat"}, nil)
	if !errors.Is(err, errLLMIdle) {
		t.Fatalf("err %v", err)
	}
	if n.Load() != 1 || asString(rep.Msg.Content) != "so far" {
		t.Fatalf("stalled after tokens: attempts %d partial %q", n.Load(), asString(rep.Msg.Content))
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("the watchdog took %v", time.Since(start))
	}

	// Stalled before any byte (no headers either): retried, then reported.
	n.Store(0)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv2.Close()
	defer close(release) // before the Close calls above: they wait for the handlers
	_, err = testGW(srv2).Chat(context.Background(), LLMRequest{Model: "m", Stream: true, Wire: "chat"}, nil)
	if !errors.Is(err, errLLMIdle) || n.Load() != llmRetries+1 {
		t.Fatalf("err %v attempts %d", err, n.Load())
	}

	// A caller cancel is not retried and not an idle error.
	n.Store(0)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err = testGW(srv2).Chat(ctx, LLMRequest{Model: "m", Stream: true, Wire: "chat"}, nil)
	if !errors.Is(err, context.Canceled) || n.Load() != 1 {
		t.Fatalf("cancel: %v attempts %d", err, n.Load())
	}
}

func TestResponsesFailedEvent(t *testing.T) {
	fastWires(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"boom"}}}`)
	}))
	defer srv.Close()
	_, err := testGW(srv).Chat(context.Background(), LLMRequest{Model: "o3", Stream: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v", err)
	}
}

func TestResponsesNonStreaming(t *testing.T) {
	out := `{"status":"completed","output":[
	 {"type":"reasoning","id":"rs_9","encrypted_content":"E9","summary":[{"type":"summary_text","text":"A"},{"type":"summary_text","text":"B"}]},
	 {"type":"message","content":[{"type":"output_text","text":"answer"}]},
	 {"type":"function_call","call_id":"c9","name":"note","arguments":"{}"}],
	 "usage":{"input_tokens":5,"output_tokens":6}}`
	rep, err := responsesCodec{}.parseBody([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reasoning != "A\n\nB" || asString(rep.Msg.Content) != "answer" || len(rep.Msg.ToolCalls) != 1 || rep.Msg.ToolCalls[0].ID != "c9" {
		t.Fatalf("rep %+v", rep)
	}
	if rep.Usage.TotalTokens != 11 || !strings.Contains(string(rep.ReasoningRaw), "E9") {
		t.Fatalf("usage %+v raw %s", rep.Usage, rep.ReasoningRaw)
	}
}
