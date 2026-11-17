package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	xllm "github.com/xbin-dev/xbin/internal/llm"
	"github.com/xbin-dev/xbin/sdk/llm"
)

// ask posts one request to a handler and returns the answer's body.
func ask(t *testing.T, h http.HandlerFunc, body string) []byte {
	t.Helper()
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", body, w.Code, w.Body)
	}
	return w.Body.Bytes()
}

// payloads are a stream's data payloads, decoded.
func payloads(t *testing.T, stream []byte) []any {
	t.Helper()
	var out []any
	err := llm.ReadData(bytes.NewReader(stream), func(d []byte) error {
		var v any
		if err := json.Unmarshal(d, &v); err != nil {
			t.Fatalf("payload %s: %v", d, err)
		}
		out = append(out, v)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../sdk/llm/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The answers are built from sdk/llm's types and say what the fake said
// when it wrote them as maps: sdk/llm's *-fake fixtures are those answers
// to "hello", recorded then. Since: a non-streaming chat answer numbers its
// choice (index), and the Responses wire reports total_tokens (Codex drops a
// stream whose response.completed lacks it).
func TestAnswersMatchTheRecordedOnes(t *testing.T) {
	const chatReq = `{"model":"fake-chat","messages":[{"role":"user","content":"hello"}]`
	const respReq = `{"model":"gpt-5-fake","input":[{"role":"user","content":"hello"}]`
	same := func(what string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			g, _ := json.Marshal(got)
			w, _ := json.Marshal(want)
			t.Errorf("%s:\n got %s\nwant %s", what, g, w)
		}
	}
	same("chat stream", payloads(t, ask(t, chatCompletions, chatReq+`,"stream":true}`)), payloads(t, fixture(t, "chat-stream-fake.sse")))

	var got, want map[string]any
	_ = json.Unmarshal(ask(t, chatCompletions, chatReq+`}`), &got)
	_ = json.Unmarshal(fixture(t, "chat-body-fake.json"), &want)
	want["choices"].([]any)[0].(map[string]any)["index"] = 0.0
	same("chat body", got, want)

	withTotal := func(usage any) { usage.(map[string]any)["total_tokens"] = 120.0 }
	wantEvents := payloads(t, fixture(t, "responses-stream-fake.sse"))
	withTotal(wantEvents[len(wantEvents)-1].(map[string]any)["response"].(map[string]any)["usage"])
	stream := ask(t, responses, respReq+`,"stream":true}`)
	same("responses stream", payloads(t, stream), wantEvents)

	got, want = nil, nil
	_ = json.Unmarshal(ask(t, responses, respReq+`}`), &got)
	_ = json.Unmarshal(fixture(t, "responses-body-fake.json"), &want)
	withTotal(want["usage"])
	same("responses body", got, want)

	// the last event of a Responses stream is response.completed, its usage whole
	var last llm.ResponsesEvent
	_ = llm.ReadData(bytes.NewReader(stream), func(d []byte) error { return json.Unmarshal(d, &last) })
	if u := last.Response; last.Type != llm.EventCompleted || u == nil || u.Usage == nil || *u.Usage != (llm.ResponsesUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120, OutputTokensDetails: u.Usage.OutputTokensDetails}) ||
		u.Usage.OutputTokensDetails == nil || u.Usage.OutputTokensDetails.ReasoningTokens != 7 {
		t.Errorf("response.completed: %+v", last)
	}
}

// A tool call reads back through the wire types on both wires, streamed
// (its arguments in two fragments) and whole.
func TestToolCallsReadBack(t *testing.T) {
	const wantArgs = `{"summary":"Jot down a quick note","text":"checked"}`
	// chat, streamed
	var name, args, finish string
	var usage *llm.ChatUsage
	err := llm.ReadData(bytes.NewReader(ask(t, chatCompletions, `{"model":"fake-chat","stream":true,"messages":[{"role":"user","content":"use a tool"}]}`)), func(d []byte) error {
		var c llm.ChatChunk
		if err := json.Unmarshal(d, &c); err != nil {
			return err
		}
		if c.Usage != nil {
			usage = c.Usage
		}
		for _, ch := range c.Choices {
			for _, tc := range ch.Delta.ToolCalls {
				name, args = name+tc.Function.Name, args+tc.Function.Arguments
			}
			if ch.FinishReason != nil {
				finish = *ch.FinishReason
			}
		}
		return nil
	})
	if err != nil || name != "note" || args != wantArgs || finish != "tool_calls" || usage == nil || usage.Usage().TotalTokens != 120 {
		t.Errorf("chat stream: %v, %s(%s), finish %q, usage %+v", err, name, args, finish, usage)
	}
	// chat, whole
	var cr llm.ChatResponse
	if err := json.Unmarshal(ask(t, chatCompletions, `{"model":"fake-chat","messages":[{"role":"user","content":"use a tool"}]}`), &cr); err != nil ||
		len(cr.Choices) != 1 || len(cr.Choices[0].Message.ToolCalls) != 1 || cr.Choices[0].Message.ToolCalls[0].Function.Arguments != wantArgs ||
		cr.Choices[0].Message.ToolCalls[0].Type != "function" || cr.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("chat body: %v, %+v", err, cr)
	}
	// responses, whole
	var rr llm.Response
	if err := json.Unmarshal(ask(t, responses, `{"model":"gpt-5-fake","input":[{"role":"user","content":"use a tool"}]}`), &rr); err != nil || len(rr.Output) != 1 {
		t.Fatalf("responses body: %v, %+v", err, rr)
	}
	var item llm.OutputItem
	if err := json.Unmarshal(rr.Output[0], &item); err != nil || item.Type != "function_call" || item.Name != "note" || item.Arguments != wantArgs || item.CallID == "" {
		t.Errorf("responses body's call: %v, %+v", err, item)
	}
	// a request the fake can't read is refused in OpenAI's error shape
	w := httptest.NewRecorder()
	chatCompletions(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"messages":[]}`)))
	if e, ok := llm.ParseError(w.Body.Bytes()); w.Code != 400 || !ok || e.Message != "bad request" || e.Type != llm.ErrTypeInvalidRequest {
		t.Errorf("a bad request: %d %s", w.Code, w.Body)
	}
}

// -chat-only, and the LLM connector's translation over it (D244): what a
// client of the Responses wire reads through the connector from a server
// that has only chat completions is what this fake answers on its own
// Responses wire for the same request — the text, a tool call, the usage —
// streamed and whole.
func TestChatOnlyTranslatedMatchesTheResponsesWire(t *testing.T) {
	srv := httptest.NewServer(newMux(true))
	defer srv.Close()
	if r, err := http.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"fake-chat","input":"hi"}`)); err != nil || r.StatusCode != 404 {
		t.Fatalf("-chat-only: POST /v1/responses: %v %v, want 404", r, err)
	}
	plane := xllm.NewProxy(slog.New(slog.NewTextHandler(io.Discard, nil)))
	target := xllm.Target{Name: "fake", BaseURL: srv.URL + "/v1"}
	via := func(body string) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		res, _, ok := xllm.ResponsesViaChat(context.Background(), plane, w, target, []byte(body), "fake/fake-chat", "fake-chat", nil, nil)
		if !ok || w.Code != 200 || !res.OK() || !res.HasUsage || res.Usage.TotalTokens != 120 {
			t.Fatalf("%s through the translation: ok %v, %d, %+v: %s", body, ok, w.Code, res, w.Body)
		}
		return w.Body.Bytes()
	}
	// what a Responses answer holds that a client acts on: its text, its calls, its usage
	type got struct {
		Text, Call, Args, Status string
		Total                    int64
	}
	read := func(resp llm.Response) got {
		g := got{Status: resp.Status}
		if resp.Usage != nil {
			g.Total = resp.Usage.Usage().TotalTokens
		}
		for _, raw := range resp.Output {
			var it llm.OutputItem
			_ = json.Unmarshal(raw, &it)
			switch it.Type {
			case "function_call":
				g.Call, g.Args = it.Name, it.Arguments
			case "message":
				for _, c := range it.Content {
					g.Text += c.Text
				}
			}
		}
		return g
	}
	whole := func(b []byte) got {
		t.Helper()
		var resp llm.Response
		if err := json.Unmarshal(b, &resp); err != nil {
			t.Fatalf("a response: %v: %s", err, b)
		}
		return read(resp)
	}
	streamed := func(b []byte) got {
		t.Helper()
		var last llm.Response
		var deltas string
		done := false
		if err := llm.ReadSSE(bytes.NewReader(b), func(event string, data []byte) error {
			var ev llm.ResponsesEvent
			if err := json.Unmarshal(data, &ev); err != nil {
				return err
			}
			if ev.Type == "response.output_text.delta" {
				deltas += ev.Delta
			}
			if ev.Type == "response.completed" && ev.Response != nil {
				last, done = *ev.Response, true
			}
			return nil
		}); err != nil || !done {
			t.Fatalf("a Responses stream: %v, completed %v: %s", err, done, b)
		}
		g := read(last)
		if g.Text != deltas {
			t.Errorf("the stream's text deltas %q, its final message %q", deltas, g.Text)
		}
		return g
	}
	for _, say := range []string{"hello there", "use a tool"} {
		in := `"input":[{"role":"user","content":"` + say + `"}]`
		native := whole(ask(t, responses, `{"model":"fake-chat",`+in+`}`))
		if g := whole(via(`{"model":"fake/fake-chat",` + in + `}`)); g != native {
			t.Errorf("%q, whole: translated %+v, the Responses wire %+v", say, g, native)
		}
		if g := streamed(via(`{"model":"fake/fake-chat","stream":true,` + in + `}`)); g != native {
			t.Errorf("%q, streamed: translated %+v, the Responses wire %+v", say, g, native)
		}
		if native.Status != "completed" || native.Total != 120 || (say == "use a tool") != (native.Call == "note") {
			t.Errorf("%q on the Responses wire: %+v", say, native)
		}
	}
}
