package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// chatOnly makes up answer like a provider that has Chat Completions and
// nothing else: POST /v1/responses is its 404 page; a chat completion is
// answered by script (which gets the request's body).
func chatOnly(up *fakeUpstream, script func(w http.ResponseWriter, body map[string]any)) {
	up.mu.Lock()
	defer up.mu.Unlock()
	up.fail = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/responses") {
			http.Error(w, "404 page not found", http.StatusNotFound)
			return true
		}
		if script == nil {
			return false
		}
		up.mu.Lock()
		body := up.bodies[len(up.bodies)-1]
		up.mu.Unlock()
		script(w, body)
		return true
	}
}

// sse writes chunks as a chat-completions stream, ended as the script says.
func sse(w http.ResponseWriter, chunks ...any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	// what a real gateway sends first: an event line and a comment
	fmt.Fprint(w, "event: x-keepalive\n: true\n\n")
	for _, c := range chunks {
		if s, ok := c.(string); ok {
			fmt.Fprint(w, s)
			continue
		}
		b, _ := json.Marshal(c)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func delta(d map[string]any, finish string) map[string]any {
	ch := map[string]any{"index": 0, "delta": d}
	if finish != "" {
		ch["finish_reason"] = finish
	}
	return map[string]any{"id": "chatcmpl-abc", "created": 1700000000, "choices": []any{ch}}
}

type respEvent struct {
	Type string
	Data map[string]any
}

// events parses a Responses stream.
func events(t *testing.T, body []byte) []respEvent {
	t.Helper()
	var out []respEvent
	seq := -1.0
	for _, block := range strings.Split(string(body), "\n\n") {
		var ev, data string
		for _, l := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(l, "event: "); ok {
				ev = v
			}
			if v, ok := strings.CutPrefix(l, "data: "); ok {
				data = v
			}
		}
		if data == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			t.Fatalf("a stream event that is no JSON: %q", data)
		}
		if m["type"] != ev {
			t.Errorf("event %q carries type %v", ev, m["type"])
		}
		if n, _ := m["sequence_number"].(float64); n != seq+1 {
			t.Errorf("event %s: sequence_number %v after %v", ev, m["sequence_number"], seq)
		}
		seq++
		out = append(out, respEvent{ev, m})
	}
	return out
}

func eventTypes(evs []respEvent) string {
	var s []string
	for _, e := range evs {
		s = append(s, strings.TrimPrefix(e.Type, "response."))
	}
	return strings.Join(s, " ")
}

// covers D244 — a Responses call for an upstream that has only Chat
// Completions is translated: found by the upstream's 404 (once: the next
// call goes straight to chat), streamed text and parallel tool calls come
// back as Responses events, and the call is metered once, by the chat
// usage.
func TestResponsesOnAChatOnlyUpstream(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	chatOnly(up, func(w http.ResponseWriter, body map[string]any) {
		sse(w,
			delta(map[string]any{"role": "assistant"}, ""),
			delta(map[string]any{"reasoning_content": "thinking. "}, ""),
			delta(map[string]any{"content": "I'll check "}, ""),
			delta(map[string]any{"content": "both."}, ""),
			// two calls: the first whole in one chunk, the second in pieces
			delta(map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "id": "call_a", "type": "function", "function": map[string]any{"name": "get_weather", "arguments": `{"city":"Paris"}`}},
				map[string]any{"index": 1, "id": "call_b", "type": "function", "function": map[string]any{"name": "get_weather", "arguments": `{"ci`}}}}, ""),
			delta(map[string]any{"tool_calls": []any{map[string]any{"index": 1, "function": map[string]any{"arguments": `ty":"Rome"}`}}}}, "tool_calls"),
			map[string]any{"id": "chatcmpl-abc", "choices": []any{}, "usage": map[string]any{"prompt_tokens": 300, "completion_tokens": 80, "total_tokens": 380,
				"prompt_tokens_details": map[string]any{"cached_tokens": 192}, "completion_tokens_details": map[string]any{"reasoning_tokens": 7}}},
			"data: [DONE]\n\n")
	})
	r.addUpstream("main", up, map[string]any{"models": []string{"vendor/model-x"}})

	req := map[string]any{"model": "main/vendor/model-x", "stream": true, "instructions": "Be brief.", "parallel_tool_calls": true, "max_output_tokens": 500,
		"reasoning": map[string]any{"effort": "low"}, "store": false,
		"input": []any{
			map[string]any{"type": "message", "role": "developer", "content": "dev note"},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Weather in Paris and Rome?"}}},
			map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}},
			map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Looking."}}},
			map[string]any{"type": "function_call", "call_id": "call_0", "name": "lookup", "arguments": `{"q":"x"}`},
			map[string]any{"type": "function_call_output", "call_id": "call_0", "output": "found"},
		},
		"tools": []any{
			map[string]any{"type": "function", "name": "get_weather", "description": "weather", "parameters": map[string]any{"type": "object"}, "strict": false},
			map[string]any{"type": "web_search"},
		}}
	a := r.must(200, pOwner, "POST", "/llm/v1/responses", req)
	if ct := a.h.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type %q: %s", ct, a.body)
	}

	// what the upstream was sent: the 404'd Responses call, then one chat completion
	if up.calls() != 2 {
		t.Fatalf("the upstream was called %d times, want 2 (the Responses call it has no route for, then chat)", up.calls())
	}
	sent := up.lastBody()
	msgs, _ := sent["messages"].([]any)
	var shape []string
	for _, m := range msgs {
		mm := m.(map[string]any)
		s := fmt.Sprint(mm["role"])
		if tc, ok := mm["tool_calls"].([]any); ok {
			s += fmt.Sprintf("+%dcalls", len(tc))
		}
		if id, ok := mm["tool_call_id"]; ok {
			s += "(" + fmt.Sprint(id) + ")"
		}
		shape = append(shape, s)
	}
	if got := strings.Join(shape, " "); got != "system system user assistant+1calls tool(call_0)" {
		t.Errorf("the chat messages: %s\n%v", got, msgs)
	}
	if msgs[0].(map[string]any)["content"] != "Be brief." || msgs[2].(map[string]any)["content"] != "Weather in Paris and Rome?" || msgs[3].(map[string]any)["content"] != "Looking." {
		t.Errorf("message contents: %v", msgs)
	}
	tools, _ := sent["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["function"].(map[string]any)["name"] != "get_weather" {
		t.Errorf("the chat tools (a hosted tool is left out): %v", tools)
	}
	if sent["model"] != "vendor/model-x" || sent["stream"] != true || sent["max_tokens"] != 500.0 || sent["reasoning_effort"] != "low" ||
		sent["parallel_tool_calls"] != true || sent["stream_options"].(map[string]any)["include_usage"] != true || sent["instructions"] != nil || sent["input"] != nil || sent["store"] != nil {
		t.Errorf("the chat request: %v", sent)
	}

	evs := events(t, a.body)
	want := "created in_progress output_item.added reasoning_summary_part.added reasoning_summary_text.delta " +
		"output_item.added content_part.added output_text.delta output_text.delta " +
		"output_item.added function_call_arguments.delta output_item.added function_call_arguments.delta function_call_arguments.delta " +
		"reasoning_summary_text.done reasoning_summary_part.done output_item.done " +
		"output_text.done content_part.done output_item.done " +
		"function_call_arguments.done output_item.done function_call_arguments.done output_item.done completed"
	if got := eventTypes(evs); got != want {
		t.Fatalf("the events:\n got %s\nwant %s", got, want)
	}
	final := evs[len(evs)-1].Data["response"].(map[string]any)
	out := final["output"].([]any)
	if len(out) != 4 {
		t.Fatalf("output items: %v", out)
	}
	msg, c1, c2 := out[1].(map[string]any), out[2].(map[string]any), out[3].(map[string]any)
	if msg["type"] != "message" || msg["content"].([]any)[0].(map[string]any)["text"] != "I'll check both." {
		t.Errorf("the message item: %v", msg)
	}
	if c1["type"] != "function_call" || c1["call_id"] != "call_a" || c1["name"] != "get_weather" || c1["arguments"] != `{"city":"Paris"}` ||
		c2["call_id"] != "call_b" || c2["arguments"] != `{"city":"Rome"}` {
		t.Errorf("the function calls: %v %v", c1, c2)
	}
	u := final["usage"].(map[string]any)
	if final["status"] != "completed" || final["model"] != "main/vendor/model-x" || u["input_tokens"] != 300.0 || u["output_tokens"] != 80.0 || u["total_tokens"] != 380.0 ||
		u["input_tokens_details"].(map[string]any)["cached_tokens"] != 192.0 || u["output_tokens_details"].(map[string]any)["reasoning_tokens"] != 7.0 {
		t.Errorf("the final response: status %v model %v usage %v", final["status"], final["model"], u)
	}

	// metered once, as the chat usage said, under the model's own name (its slash and all)
	rows := r.usageRows("")
	if len(rows) != 1 || rows[0].Model != "main/vendor/model-x" || rows[0].Requests != 1 || rows[0].InputTokens != 300 || rows[0].OutputTokens != 80 || rows[0].CachedTokens != 192 {
		t.Errorf("metered: %+v", rows)
	}

	// the next call doesn't ask for the route again, and an admin reads that it translates
	r.must(200, pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "main/vendor/model-x", "stream": true, "input": "again"})
	if up.calls() != 3 {
		t.Errorf("the second call reached the upstream %d times in all, want 3", up.calls())
	}
	if in := up.lastBody()["messages"].([]any); len(in) != 1 || in[0].(map[string]any)["content"] != "again" {
		t.Errorf("a string input: %v", in)
	}
	cfg := r.must(200, pOwner, "GET", "/llm/config", nil).json(t)
	uv := cfg["upstreams"].([]any)[0].(map[string]any)
	if uv["responsesVia"] != "auto" || uv["translating"] != true {
		t.Errorf("the upstream's view: responsesVia %v translating %v", uv["responsesVia"], uv["translating"])
	}
}

// covers D244 — the plain (non-streaming) answer, a truncated one, a custom
// tool, and the setting: "chat" never asks the upstream for the route,
// "upstream" never translates.
func TestResponsesTranslatedPlainAndSettings(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	finish := "tool_calls"
	chatOnly(up, func(w http.ResponseWriter, body map[string]any) {
		writeJSON(w, 200, map[string]any{"id": "chatcmpl-p1", "created": 1700000001,
			"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": map[string]any{"role": "assistant", "content": "patching",
				"tool_calls": []any{map[string]any{"id": "call_p", "type": "function", "function": map[string]any{"name": "apply_patch", "arguments": `{"input":"*** Begin Patch"}`}}}}}},
			"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 5, "total_tokens": 16}})
	})
	r.addUpstream("main", up, map[string]any{"responsesVia": "chat"})

	req := map[string]any{"model": "main/gpt-5", "input": []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "look"}, map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"}}},
		map[string]any{"type": "custom_tool_call", "call_id": "call_o", "name": "apply_patch", "input": "old patch"},
		map[string]any{"type": "custom_tool_call_output", "call_id": "call_o", "output": []any{map[string]any{"type": "input_text", "text": "applied"}}},
	}, "tools": []any{map[string]any{"type": "custom", "name": "apply_patch", "description": "patch files"}},
		"tool_choice": map[string]any{"type": "custom", "name": "apply_patch"},
		"text":        map[string]any{"format": map[string]any{"type": "json_schema", "name": "out", "schema": map[string]any{"type": "object"}}}}
	a := r.must(200, pOwner, "POST", "/llm/v1/responses", req)
	if up.calls() != 1 {
		t.Fatalf("responsesVia chat: the upstream was called %d times, want 1 (never asked for the route)", up.calls())
	}
	resp := a.json(t)
	out, _ := resp["output"].([]any)
	if resp["object"] != "response" || resp["status"] != "completed" || resp["id"] != "resp_p1" || len(out) != 2 {
		t.Fatalf("the response: %s", a.body)
	}
	if c := out[1].(map[string]any); c["type"] != "custom_tool_call" || c["name"] != "apply_patch" || c["input"] != "*** Begin Patch" || c["call_id"] != "call_p" {
		t.Errorf("a custom tool's call comes back as one: %v", c)
	}
	if u := resp["usage"].(map[string]any); u["input_tokens"] != 11.0 || u["output_tokens"] != 5.0 {
		t.Errorf("usage: %v", u)
	}
	sent := up.lastBody()
	msgs := sent["messages"].([]any)
	parts, _ := msgs[0].(map[string]any)["content"].([]any)
	if len(parts) != 2 || parts[1].(map[string]any)["type"] != "image_url" || parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,AAAA" {
		t.Errorf("a user message with an image: %v", msgs[0])
	}
	tc := msgs[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if tc["name"] != "apply_patch" || tc["arguments"] != `{"input":"old patch"}` || msgs[2].(map[string]any)["content"] != "applied" {
		t.Errorf("a custom tool's earlier call and output: %v", msgs[1:])
	}
	fn := sent["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "apply_patch" || fn["parameters"].(map[string]any)["required"].([]any)[0] != "input" {
		t.Errorf("a custom tool as a function: %v", fn)
	}
	if sent["tool_choice"].(map[string]any)["function"].(map[string]any)["name"] != "apply_patch" ||
		sent["response_format"].(map[string]any)["json_schema"].(map[string]any)["name"] != "out" || sent["stream"] != nil {
		t.Errorf("tool choice and output format: %v", sent)
	}

	// cut off by the output limit: incomplete, with the reason
	finish = "length"
	if got := r.must(200, pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "main/gpt-5", "input": "x"}).json(t); got["status"] != "incomplete" ||
		got["incomplete_details"].(map[string]any)["reason"] != "max_output_tokens" {
		t.Errorf("a truncated answer: %v", got)
	}

	// what chat can't carry is refused before anything is sent
	n := up.calls()
	for name, bad := range map[string]map[string]any{
		"previous_response_id": {"model": "main/gpt-5", "input": "x", "previous_response_id": "resp_1"},
		"an item reference":    {"model": "main/gpt-5", "input": []any{map[string]any{"type": "item_reference", "id": "x"}}},
		"a file part":          {"model": "main/gpt-5", "input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_file", "file_id": "f"}}}}},
	} {
		if code := r.must(400, pOwner, "POST", "/llm/v1/responses", bad).errCode(t); code != "xbin_llm_translate" {
			t.Errorf("%s: code %q", name, code)
		}
	}
	if up.calls() != n {
		t.Errorf("a refused translation reached the upstream")
	}

	// "upstream": its 404 is the caller's
	r.must(200, pOwner, "PUT", "/llm/upstreams/main", map[string]any{"responsesVia": "upstream"})
	if a := r.do(pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "main/gpt-5", "input": "x"}); a.code != 404 {
		t.Errorf("responsesVia upstream on an upstream without the route: %d %s, want its 404", a.code, a.body)
	}
	if a := r.do(pOwner, "PUT", "/llm/upstreams/main", map[string]any{"responsesVia": "sideways"}); a.code != 400 {
		t.Errorf("an unknown responsesVia: %d", a.code)
	}
}

// covers D244 — errors pass as they are (a context-length refusal is the
// same shape on both wires); an error inside a stream and a stream that
// breaks off end the answer as failed; a 404 that is about the model is
// not taken for a missing route.
func TestResponsesTranslatedErrors(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	mode := "context"
	chatOnly(up, func(w http.ResponseWriter, body map[string]any) {
		switch mode {
		case "context":
			writeJSON(w, 400, map[string]any{"error": map[string]any{"message": "This model's maximum context length is 1000 tokens.", "type": "invalid_request_error", "code": "context_length_exceeded"}})
		case "stream-error":
			sse(w, delta(map[string]any{"content": "par"}, ""), map[string]any{"error": map[string]any{"message": "overloaded", "code": "server_error"}})
		case "broken":
			sse(w, delta(map[string]any{"content": "half"}, ""))
		case "no-done":
			sse(w, delta(map[string]any{"content": "whole"}, "stop"))
		}
	})
	r.addUpstream("main", up, nil)
	call := func(stream bool) answer {
		return r.do(pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "main/gpt-5", "stream": stream, "input": "x"})
	}
	if a := call(true); a.code != 400 || a.errCode(t) != "context_length_exceeded" {
		t.Errorf("a context-length refusal: %d %s", a.code, a.body)
	}
	mode = "stream-error"
	evs := events(t, call(true).body)
	if last := evs[len(evs)-1]; last.Type != "response.failed" || last.Data["response"].(map[string]any)["error"].(map[string]any)["message"] != "overloaded" {
		t.Errorf("an error inside the stream: %s", eventTypes(evs))
	}
	mode = "broken"
	evs = events(t, call(true).body)
	if last := evs[len(evs)-1]; last.Type != "response.failed" {
		t.Errorf("a stream that broke off: %s", eventTypes(evs))
	}
	mode = "no-done"
	evs = events(t, call(true).body)
	if last := evs[len(evs)-1]; last.Type != "response.completed" {
		t.Errorf("a stream that ended without [DONE]: %s", eventTypes(evs))
	}

	// an upstream that HAS the route and doesn't know the model: its answer is the caller's, and nothing is remembered
	r2 := newRig(t, "")
	up2 := newUpstream(t)
	up2.mu.Lock()
	up2.fail = func(w http.ResponseWriter, r *http.Request) bool {
		_, _ = io.Copy(io.Discard, r.Body)
		writeJSON(w, 404, map[string]any{"error": map[string]any{"message": "The model `nope` does not exist", "type": "invalid_request_error", "code": "model_not_found"}})
		return true
	}
	up2.mu.Unlock()
	r2.addUpstream("main", up2, nil)
	if a := r2.do(pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "main/nope", "input": "x"}); a.code != 404 || a.errCode(t) != "model_not_found" || up2.calls() != 1 {
		t.Errorf("a model the upstream doesn't have: %d %s after %d calls", a.code, a.body, up2.calls())
	}
}

// A provider's model ids often hold a slash ("vendor/model", as on
// OpenRouter-style gateways): the connector's name for one is
// <upstream>/<vendor>/<model>, and every place a model is named takes it —
// the list, a call by its full name, by the provider's own id where one
// upstream offers it, the default and a role, an alias, the row it is
// metered under, and the provider's Test. (The first real endpoint the
// workspace agent ran on, 2026-11-17.)
func TestModelIDsWithASlash(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	up.mu.Lock()
	up.models = []map[string]any{{"id": "vendor/model-x", "object": "model"}, {"id": "vendor/model-y", "object": "model"}}
	up.mu.Unlock()
	r.addUpstream("gw", up, nil) // no list of its own: the provider's passes through
	const full = "gw/vendor/model-x"
	list := string(r.must(200, pOwner, "GET", "/llm/v1/models", nil).body)
	if !strings.Contains(list, `"id":"`+full+`"`) || !strings.Contains(list, `"id":"gw/vendor/model-y"`) {
		t.Fatalf("the model list: %s", list)
	}
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"defaults": map[string]any{"model": full, "roles": map[string]string{"agent": "gw/vendor/model-y"}},
		"aliases": map[string]string{"fast": full}})
	for asked, want := range map[string]string{full: "vendor/model-x", "vendor/model-x": "vendor/model-x", "": "vendor/model-x", "xbin/default": "vendor/model-x",
		"xbin/agent": "vendor/model-y", "fast": "vendor/model-x"} {
		r.must(200, pOwner, "POST", "/llm/v1/chat/completions", chatBody(asked, false))
		if got := up.lastBody()["model"]; got != want {
			t.Errorf("model %q reached the upstream as %v, want %s", asked, got, want)
		}
	}
	names := map[string]int64{}
	for _, row := range r.usageRows("") {
		names[row.Model] += row.Requests
	}
	if names[full] != 5 || names["gw/vendor/model-y"] != 1 || len(names) != 2 {
		t.Errorf("metered under: %v, want %s ×5 and gw/vendor/model-y ×1", names, full)
	}
	if a := r.must(200, pOwner, "POST", "/llm/upstreams/gw/test", map[string]any{"model": "vendor/model-x"}).json(t); a["ok"] != true || a["model"] != "vendor/model-x" {
		t.Errorf("Test with a model id that holds a slash: %v", a)
	}
	if st := r.must(200, pOwner, "GET", "/llm", nil).json(t); strings.Contains(fmt.Sprint(st["problems"]), "vendor") {
		t.Errorf("the status takes the default for a model that doesn't exist: %v", st["problems"])
	}
}
