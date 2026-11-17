package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// plainChat answers every chat call with one plain completion.
func plainChat(up *fakeUpstream, msg map[string]any, finish string) {
	chatOnly(up, func(w http.ResponseWriter, body map[string]any) {
		writeJSON(w, 200, map[string]any{"id": "chatcmpl-1", "choices": []any{map[string]any{"message": msg, "finish_reason": finish}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4}})
	})
}

// covers D244 — nothing of a translated request vanishes silently. What
// would change the request's meaning is refused, naming the field; what
// is advisory is not sent and is named in X-XBin-LLM-Dropped.
func TestResponsesTranslationSaysWhatItDropped(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	plainChat(up, map[string]any{"role": "assistant", "content": "ok"}, "stop")
	r.addUpstream("main", up, map[string]any{"responsesVia": "chat"})
	fn := map[string]any{"type": "function", "name": "roll", "parameters": map[string]any{"type": "object"}}
	req := func(more map[string]any) answer {
		body := map[string]any{"model": "main/gpt-5", "input": "x"}
		for k, v := range more {
			body[k] = v
		}
		return r.do(pOwner, "POST", "/llm/v1/responses", body)
	}

	// advisory: dropped, and said
	a := req(map[string]any{
		"metadata": map[string]any{"a": "b"}, "include": []string{"reasoning.encrypted_content"}, "store": true,
		"reasoning": map[string]any{"effort": "low", "summary": "auto"}, "text": map[string]any{"verbosity": "low"},
		"prompt_cache_key": "k", "service_tier": "flex", "max_tool_calls": 3, "truncation": "auto", "some_new_field": 1,
		"tools": []any{fn, map[string]any{"type": "web_search"}, map[string]any{"type": "local_shell"}}, "top_logprobs": 2,
	})
	want := "include,max_tool_calls,metadata,prompt_cache_key,reasoning.summary,service_tier,some_new_field,store,text.verbosity,tools.local_shell,tools.web_search,truncation"
	if a.code != 200 || a.h.Get(DroppedHeader) != want {
		t.Fatalf("advisory fields: %d, %s: %q\nwant %q\n%s", a.code, DroppedHeader, a.h.Get(DroppedHeader), want, a.body)
	}
	sent := up.lastBody()
	if sent["logprobs"] != true || sent["top_logprobs"] != 2.0 || sent["reasoning_effort"] != "low" || len(sent["tools"].([]any)) != 1 {
		t.Errorf("the chat request: logprobs %v top_logprobs %v effort %v tools %v", sent["logprobs"], sent["top_logprobs"], sent["reasoning_effort"], sent["tools"])
	}
	for _, k := range []string{"metadata", "include", "store", "prompt_cache_key", "service_tier", "max_tool_calls", "truncation", "some_new_field", "text", "reasoning"} {
		if _, ok := sent[k]; ok {
			t.Errorf("%s was sent to the chat upstream", k)
		}
	}
	// the response object says what the request said
	resp := a.json(t)
	if tools, _ := resp["tools"].([]any); len(tools) != 3 || resp["parallel_tool_calls"] != true {
		t.Errorf("the response's tools %v, parallel_tool_calls %v — want the request's", resp["tools"], resp["parallel_tool_calls"])
	}
	if a := req(map[string]any{"tools": []any{fn}, "parallel_tool_calls": false}); a.json(t)["parallel_tool_calls"] != false || a.h.Get(DroppedHeader) != "" {
		t.Errorf("parallel_tool_calls: false is echoed %v; dropped %q", a.json(t)["parallel_tool_calls"], a.h.Get(DroppedHeader))
	}

	// tool_choice: allowed_tools narrows what is offered
	a = req(map[string]any{"tools": []any{fn, map[string]any{"type": "function", "name": "other"}, map[string]any{"type": "web_search"}},
		"tool_choice": map[string]any{"type": "allowed_tools", "mode": "required", "tools": []any{map[string]any{"type": "function", "name": "roll"}}}})
	sent = up.lastBody()
	if tools, _ := sent["tools"].([]any); a.code != 200 || len(tools) != 1 || sent["tool_choice"] != "required" {
		t.Errorf("allowed_tools: %d, sent tools %v, tool_choice %v", a.code, sent["tools"], sent["tool_choice"])
	}

	// a tool's output with an image: the text is the tool message, the image follows in a user message
	a = req(map[string]any{"input": []any{
		map[string]any{"role": "user", "content": "look"},
		map[string]any{"type": "function_call", "call_id": "c1", "name": "shot", "arguments": "{}"},
		map[string]any{"type": "function_call", "call_id": "c2", "name": "shot", "arguments": "{}"},
		map[string]any{"type": "function_call_output", "call_id": "c1", "output": []any{
			map[string]any{"type": "input_text", "text": "a screenshot"}, map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"}}},
		map[string]any{"type": "function_call_output", "call_id": "c2", "output": "plain"},
	}})
	msgs, _ := up.lastBody()["messages"].([]any)
	if a.code != 200 || len(msgs) != 5 {
		t.Fatalf("an image in a tool's output: %d, %d messages: %v", a.code, len(msgs), msgs)
	}
	roles := ""
	for _, m := range msgs {
		roles += m.(map[string]any)["role"].(string) + " "
	}
	if last := fmt.Sprint(msgs[4]); roles != "user assistant tool tool user " || !strings.Contains(last, "data:image/png;base64,AAAA") || !strings.Contains(last, "c1") {
		t.Errorf("messages %s: the last %v", roles, msgs[4])
	}

	// what would change the meaning is refused, naming the field
	n := up.calls()
	for name, c := range map[string]struct {
		more map[string]any
		want string
	}{
		"a hosted tool the caller requires": {map[string]any{"tools": []any{fn, map[string]any{"type": "web_search"}}, "tool_choice": map[string]any{"type": "web_search"}}, "tool_choice"},
		"required, with only hosted tools":  {map[string]any{"tools": []any{map[string]any{"type": "web_search"}}, "tool_choice": "required"}, "tool_choice"},
		"allowed_tools with none we have":   {map[string]any{"tools": []any{fn, map[string]any{"type": "mcp", "server_label": "x"}}, "tool_choice": map[string]any{"type": "allowed_tools", "mode": "required", "tools": []any{map[string]any{"type": "mcp", "server_label": "x"}}}}, "tool_choice"},
		"content that is no text":           {map[string]any{"input": []any{map[string]any{"role": "user", "content": map[string]any{"type": "input_text", "text": "x"}}}}, "content"},
		"an image outside a user message":   {map[string]any{"input": []any{map[string]any{"role": "user", "content": "x"}, map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"}}}}}, "image"},
		"a file in a tool's output":         {map[string]any{"input": []any{map[string]any{"type": "function_call", "call_id": "c1", "name": "f", "arguments": "{}"}, map[string]any{"type": "function_call_output", "call_id": "c1", "output": []any{map[string]any{"type": "input_file", "file_id": "file_1"}}}}}, "input_file"},
		"a stored prompt":                   {map[string]any{"prompt": map[string]any{"id": "pmpt_1"}}, "prompt"},
	} {
		a := req(c.more)
		if a.code != 400 || a.errCode(t) != "xbin_llm_translate" || !strings.Contains(string(a.body), c.want) || !strings.Contains(string(a.body), "chat completions only") {
			t.Errorf("%s: %d %s — want a 400 naming %q", name, a.code, a.body, c.want)
		}
	}
	if up.calls() != n {
		t.Errorf("%d refused requests reached the upstream", up.calls()-n)
	}
}

// covers D244 — truncation: "auto" isn't applied by a chat upstream: when
// the request then is over the model's context, the refusal says so.
func TestResponsesTranslationTruncationOverflow(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	chatOnly(up, func(w http.ResponseWriter, body map[string]any) {
		writeJSON(w, 400, map[string]any{"error": map[string]any{"message": "This model's maximum context length is 8192 tokens.", "type": "invalid_request_error", "code": "context_length_exceeded"}})
	})
	r.addUpstream("main", up, map[string]any{"responsesVia": "chat"})
	a := r.do(pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "main/gpt-5", "input": "x", "truncation": "auto"})
	if a.code != 400 || a.errCode(t) != "xbin_llm_translate" || !strings.Contains(string(a.body), "truncation") || !strings.Contains(string(a.body), "maximum context length is 8192") {
		t.Errorf("an overflow with truncation: auto: %d %s", a.code, a.body)
	}
	// without it, the upstream's refusal is the caller's
	a = r.do(pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "main/gpt-5", "input": "x"})
	if a.code != 400 || a.errCode(t) != "context_length_exceeded" {
		t.Errorf("an overflow without truncation: %d %s", a.code, a.body)
	}
}

// covers D244 — the answer's translation: an answer the provider filtered
// is incomplete, not completed; a refusal is a refusal part; and a
// provider that numbers every tool call 0 (telling them apart by id) gets
// its calls kept apart.
func TestResponsesTranslatedAnswerFidelity(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	r.addUpstream("main", up, map[string]any{"responsesVia": "chat"})
	call := func(stream bool) answer {
		return r.do(pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "main/gpt-5", "input": "x", "stream": stream,
			"tools": []any{map[string]any{"type": "function", "name": "roll"}}})
	}
	// content_filter
	plainChat(up, map[string]any{"role": "assistant", "content": "par"}, "content_filter")
	resp := call(false).json(t)
	if d, _ := resp["incomplete_details"].(map[string]any); resp["status"] != "incomplete" || d["reason"] != "content_filter" {
		t.Errorf("a filtered answer: status %v, details %v", resp["status"], resp["incomplete_details"])
	}
	chatOnly(up, func(w http.ResponseWriter, body map[string]any) {
		sse(w, delta(map[string]any{"content": "par"}, ""), delta(map[string]any{}, "content_filter"), "data: [DONE]\n\n")
	})
	evs := events(t, call(true).body)
	last := evs[len(evs)-1]
	if d, _ := last.Data["response"].(map[string]any)["incomplete_details"].(map[string]any); last.Type != "response.incomplete" || d["reason"] != "content_filter" {
		t.Errorf("a filtered stream ends %s %v", last.Type, last.Data["response"])
	}

	// a refusal
	plainChat(up, map[string]any{"role": "assistant", "content": nil, "refusal": "I can't help with that."}, "stop")
	resp = call(false).json(t)
	out, _ := resp["output"].([]any)
	if len(out) != 1 || !strings.Contains(fmt.Sprint(out[0]), "type:refusal") || !strings.Contains(fmt.Sprint(out[0]), "refusal:I can't help with that.") {
		t.Errorf("a refusal: output %v", resp["output"])
	}
	chatOnly(up, func(w http.ResponseWriter, body map[string]any) {
		sse(w, delta(map[string]any{"refusal": "I can't "}, ""), delta(map[string]any{"refusal": "help."}, "stop"), "data: [DONE]\n\n")
	})
	evs = events(t, call(true).body)
	if types := eventTypes(evs); !strings.Contains(types, "refusal.delta refusal.delta") || !strings.Contains(types, "refusal.done") {
		t.Errorf("a streamed refusal: %s", types)
	}
	done, _ := json.Marshal(evs[len(evs)-1].Data)
	if !strings.Contains(string(done), `"refusal":"I can't help."`) {
		t.Errorf("a streamed refusal's final response: %s", done)
	}

	// two calls, both "index 0", told apart by id
	tc := func(id, name, args string) map[string]any {
		f := map[string]any{"arguments": args}
		if name != "" {
			f["name"] = name
		}
		c := map[string]any{"index": 0, "function": f}
		if id != "" {
			c["id"] = id
		}
		return map[string]any{"tool_calls": []any{c}}
	}
	chatOnly(up, func(w http.ResponseWriter, body map[string]any) {
		sse(w, delta(tc("call_a", "roll", `{"n":`), ""), delta(tc("call_a", "", `1}`), ""), delta(tc("call_b", "roll", `{"n":2}`), "tool_calls"), "data: [DONE]\n\n")
	})
	evs = events(t, call(true).body)
	out, _ = evs[len(evs)-1].Data["response"].(map[string]any)["output"].([]any)
	if len(out) != 2 {
		t.Fatalf("two calls numbered 0: %d output items: %v", len(out), out)
	}
	for i, want := range []string{`{"n":1}`, `{"n":2}`} {
		if it := out[i].(map[string]any); it["arguments"] != want || it["call_id"] != []string{"call_a", "call_b"}[i] {
			t.Errorf("call %d: %v", i, it)
		}
	}
}
