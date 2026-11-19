package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	sdkllm "github.com/xbin-dev/xbin/sdk/llm"
)

// effort_connector_test.go — D249 through the connector: the model list,
// the wire, the header, the translation's replay, the admin's entries.

func (f *fakeUpstream) setModels(models ...map[string]any) {
	f.mu.Lock()
	f.models = models
	f.mu.Unlock()
}

func (f *fakeUpstream) lastRaw() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.raws) == 0 {
		return nil
	}
	return f.raws[len(f.raws)-1]
}

func modelByID(t *testing.T, a answer, id string) map[string]any {
	t.Helper()
	for _, m := range a.json(t)["data"].([]any) {
		if mm := m.(map[string]any); mm["id"] == id {
			return mm
		}
	}
	return nil
}

// covers D249 — GET /v1/models carries effort: the upstream's own member
// made into the record (its values kept, xbind's table filling the rest),
// an admin's entry over both, nothing for a model nobody describes (and an
// upstream's member that is no record passes as it came), aliases carry
// their target's.
func TestEffortInTheModelList(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	up.setModels(
		map[string]any{"id": "deepseek-v4.1-flash", "context_window": 1000000, "effort": map[string]any{"supported_levels": []any{"low", "high", "max", "turbo"}, "default_level": "high", "send": map[string]any{"chat": map[string]any{"model": "x"}}}},
		map[string]any{"id": "gpt-5", "effort": "whatever"},
		map[string]any{"id": "local-llama"},
		map[string]any{"id": "kimi-k3"},
	)
	r.addUpstream("house", up, nil)
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"defaults": map[string]any{"model": "house/deepseek-v4.1-flash"}})
	list := r.must(200, pOwner, "GET", "/llm/v1/models", nil)
	ds := modelByID(t, list, "house/deepseek-v4.1-flash")
	e, _ := ds["effort"].(map[string]any)
	if e == nil || fmt.Sprint(e["supported_levels"]) != "[low high max]" || e["default_level"] != "high" || e["source"] != "upstream" ||
		e["can_disable"] != true || e["aliases"].(map[string]any)["medium"] != "high" || e["send"] != nil || ds["context_window"] != 1000000.0 {
		t.Errorf("DeepSeek's own effort, merged: %v", ds)
	}
	if g := modelByID(t, list, "house/gpt-5"); g["effort"] != "whatever" {
		t.Errorf("an upstream member that is no record passes as it came: %v", g)
	}
	if l := modelByID(t, list, "house/local-llama"); l["effort"] != nil {
		t.Errorf("an undescribed model got an effort: %v", l)
	}
	if k := modelByID(t, list, "house/kimi-k3"); fmt.Sprint(k["effort"].(map[string]any)["source"]) != "xbin" {
		t.Errorf("the table: %v", k)
	}
	if d := modelByID(t, list, "xbin/default"); d["alias_of"] != "house/deepseek-v4.1-flash" || fmt.Sprint(d["effort"]) != fmt.Sprint(e) {
		t.Errorf("an alias carries its target's effort: %v", d)
	}
	// an admin's entry: the whole truth, source admin; null removes it
	v := r.must(200, pOwner, "PUT", "/llm/upstreams/house", map[string]any{"reasoning": map[string]any{"deepseek*": map[string]any{"preset": "none"}, "local-llama": map[string]any{"preset": "toggle"}}}).json(t)
	ups := v["upstreams"].([]any)[0].(map[string]any)
	if rs, _ := ups["reasoning"].(map[string]any); rs == nil || rs["local-llama"].(map[string]any)["source"] != "admin" {
		t.Errorf("the view's reasoning table: %v", ups["reasoning"])
	}
	if ps, _ := v["reasoningPresets"].([]any); len(ps) != 4 {
		t.Errorf("the presets: %v", v["reasoningPresets"])
	}
	list = r.must(200, pOwner, "GET", "/llm/v1/models", nil)
	if e := modelByID(t, list, "house/deepseek-v4.1-flash")["effort"].(map[string]any); fmt.Sprint(e["supported_levels"]) != "[]" || e["source"] != "admin" {
		t.Errorf("the admin's no-reasoning entry: %v", e)
	}
	if e := modelByID(t, list, "house/local-llama")["effort"].(map[string]any); fmt.Sprint(e["supported_levels"]) != "[high]" || e["can_disable"] != true {
		t.Errorf("the admin's toggle: %v", e)
	}
	r.must(200, pOwner, "PUT", "/llm/upstreams/house", map[string]any{"reasoning": map[string]any{"deepseek*": nil}})
	list = r.must(200, pOwner, "GET", "/llm/v1/models", nil)
	if e := modelByID(t, list, "house/deepseek-v4.1-flash")["effort"].(map[string]any); e["source"] != "upstream" {
		t.Errorf("after the entry's removal: %v", e)
	}
	// what an admin may not write
	for name, body := range map[string]any{
		"an unknown preset":         map[string]any{"m": map[string]any{"preset": "fast"}},
		"a send patch":              map[string]any{"m": map[string]any{"supported_levels": []string{"low"}, "send": map[string]any{"chat": map[string]any{}}}},
		"a word off the ladder":     map[string]any{"m": map[string]any{"supported_levels": []string{"turbo"}}},
		"a model name with a space": map[string]any{"a b": map[string]any{"preset": "openai"}},
	} {
		if a := r.do(pOwner, "PUT", "/llm/upstreams/house", map[string]any{"reasoning": body}); a.code != 400 {
			t.Errorf("%s: %d %s", name, a.code, a.body)
		}
	}
	// a host's upstream is locked, reasoning and all
	host := newRig(t, writeHost(t, t.TempDir(), `{"upstreams":{"h":{"baseURL":"`+up.URL+`/v1","reasoning":{"local-llama":{"preset":"openai"}}}}}`, ""))
	if a := host.do(pOwner, "PUT", "/llm/upstreams/h", map[string]any{"reasoning": map[string]any{"x": map[string]any{"preset": "none"}}}); a.code != 409 {
		t.Errorf("the host's upstream's reasoning changed: %d %s", a.code, a.body)
	}
	hl := host.must(200, pOwner, "GET", "/llm/v1/models", nil)
	if e := modelByID(t, hl, "h/local-llama")["effort"].(map[string]any); e["source"] != "host" || fmt.Sprint(e["supported_levels"]) != "[low medium high]" {
		t.Errorf("the host's entry: %v", e)
	}
}

// covers D249 — on the wire: a described model's effort is mapped and the
// answer says so (X-XBin-LLM-Effort), streamed or not, on either wire and
// through the translation; a model without reasoning gets none, and the
// drop is named.
func TestEffortOnTheWire(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	up.setModels(map[string]any{"id": "deepseek-v4.1-flash"}, map[string]any{"id": "plain"})
	r.addUpstream("house", up, map[string]any{"reasoning": map[string]any{"plain": map[string]any{"preset": "none"}}})
	for _, stream := range []bool{false, true} {
		body := chatBody("house/deepseek-v4.1-flash", stream)
		body["reasoning_effort"] = "medium"
		a := r.must(200, pOwner, "POST", "/llm/v1/chat/completions", body)
		if up.lastBody()["reasoning_effort"] != "high" || a.h.Get(EffortHeader) != "high; asked=medium" {
			t.Errorf("chat (stream %v): sent %v, header %q", stream, up.lastBody()["reasoning_effort"], a.h.Get(EffortHeader))
		}
		body["reasoning_effort"] = "max"
		if a := r.must(200, pOwner, "POST", "/llm/v1/chat/completions", body); a.h.Get(EffortHeader) != "max" || up.lastBody()["reasoning_effort"] != "max" {
			t.Errorf("chat as asked (stream %v): %q", stream, a.h.Get(EffortHeader))
		}
		// the native Responses wire
		rb := map[string]any{"model": "house/deepseek-v4.1-flash", "input": "x", "stream": stream, "reasoning": map[string]any{"effort": "xhigh", "summary": "auto"}}
		a = r.must(200, pOwner, "POST", "/llm/v1/responses", rb)
		if reas, _ := up.lastBody()["reasoning"].(map[string]any); reas["effort"] != "high" || reas["summary"] != "auto" || a.h.Get(EffortHeader) != "high; asked=xhigh" {
			t.Errorf("responses (stream %v): %v %q", stream, up.lastBody()["reasoning"], a.h.Get(EffortHeader))
		}
		// no reasoning: dropped and named
		pb := chatBody("house/plain", stream)
		pb["reasoning_effort"] = "high"
		a = r.must(200, pOwner, "POST", "/llm/v1/chat/completions", pb)
		if _, sent := up.lastBody()["reasoning_effort"]; sent || a.h.Get(DroppedHeader) != "reasoning_effort" || a.h.Get(EffortHeader) != "none; asked=high; can't" {
			t.Errorf("no reasoning (stream %v): sent %v, %q, %q", stream, sent, a.h.Get(DroppedHeader), a.h.Get(EffortHeader))
		}
	}
	// a call that sends no effort says nothing
	if a := r.must(200, pOwner, "POST", "/llm/v1/chat/completions", chatBody("house/deepseek-v4.1-flash", false)); a.h.Get(EffortHeader) != "" {
		t.Errorf("no effort asked: %q", a.h.Get(EffortHeader))
	}
	// through the translation (a chat-only upstream)
	r2 := newRig(t, "")
	up2 := newUpstream(t)
	plainChat(up2, map[string]any{"role": "assistant", "content": "ok"}, "stop")
	r2.addUpstream("house", up2, map[string]any{"responsesVia": "chat", "reasoning": map[string]any{"plain": map[string]any{"preset": "none"}}})
	for _, stream := range []bool{false, true} {
		a := r2.must(200, pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "house/deepseek-v4.1-flash", "input": "x", "stream": stream, "reasoning": map[string]any{"effort": "medium"}})
		if up2.lastBody()["reasoning_effort"] != "high" || a.h.Get(EffortHeader) != "high; asked=medium" {
			t.Errorf("translated (stream %v): %v %q", stream, up2.lastBody()["reasoning_effort"], a.h.Get(EffortHeader))
		}
		a = r2.must(200, pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "house/plain", "input": "x", "stream": stream, "reasoning": map[string]any{"effort": "low", "summary": "auto"}})
		if _, sent := up2.lastBody()["reasoning_effort"]; sent || a.h.Get(DroppedHeader) != "reasoning.effort,reasoning.summary" || a.h.Get(EffortHeader) != "none; asked=low; can't" {
			t.Errorf("translated, no reasoning (stream %v): %q %q", stream, a.h.Get(DroppedHeader), a.h.Get(EffortHeader))
		}
		// an undescribed model gets the word as it came (D244's verbatim copy)
		a = r2.must(200, pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "house/local", "input": "x", "stream": stream, "reasoning": map[string]any{"effort": "medium"}})
		if up2.lastBody()["reasoning_effort"] != "medium" || a.h.Get(EffortHeader) != "" {
			t.Errorf("translated, undescribed (stream %v): %v %q", stream, up2.lastBody()["reasoning_effort"], a.h.Get(EffortHeader))
		}
	}
}

// covers D249 (compat) — a model nothing describes: what reaches the
// upstream is byte for byte what the relay sent before effort existed
// (sdkllm.RewriteRequest, the D226 relay), on both wires, with every kind
// of reasoning member a caller sends.
func TestEffortUndescribedIsByteIdentical(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	up.setModels(map[string]any{"id": "local-llama"}, map[string]any{"id": "odd", "effort": map[string]any{"supported_levels": []any{"turbo"}}})
	r.addUpstream("house", up, nil)
	bodies := []string{
		`{"model":"house/local-llama","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`,
		`{"model":"house/local-llama","stream":true,"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none","stream_options":{"x":1}}`,
		`{"model":"house/odd","messages":[{"role":"user","content":"hi"}],"reasoning":{"effort":"max","exclude":true},"thinking":{"type":"enabled"},"chat_template_kwargs":{"reasoning_effort":62}}`,
		`{"z":1,"model":"house/local-llama","messages":[],"reasoning_effort":"ultra","a":[1,  2]}`,
	}
	for _, b := range bodies {
		a := r.do(pOwner, "POST", "/llm/v1/chat/completions", json.RawMessage(b))
		if a.code != 200 {
			t.Fatalf("%s: %d %s", b, a.code, a.body)
		}
		want, _, err := sdkllm.RewriteRequest([]byte(b), strings.TrimPrefix(modelOf(b), "house/"), true)
		if err != nil {
			t.Fatal(err)
		}
		if got := up.lastRaw(); !bytes.Equal(got, want) {
			t.Errorf("chat %s:\n got %s\nwant %s", b, got, want)
		}
		if a.h.Get(EffortHeader) != "" || a.h.Get(DroppedHeader) != "" {
			t.Errorf("chat %s: headers %v", b, a.h)
		}
	}
	for _, b := range []string{
		`{"model":"house/local-llama","input":"x","reasoning":{"effort":"medium","summary":"auto"},"include":["reasoning.encrypted_content"]}`,
		`{"model":"house/odd","input":"x","stream":true,"reasoning":{"effort":"none"}}`,
	} {
		if a := r.do(pOwner, "POST", "/llm/v1/responses", json.RawMessage(b)); a.code != 200 {
			t.Fatalf("%s: %d %s", b, a.code, a.body)
		}
		want, _, _ := sdkllm.RewriteRequest([]byte(b), strings.TrimPrefix(modelOf(b), "house/"), false)
		if got := up.lastRaw(); !bytes.Equal(got, want) {
			t.Errorf("responses %s:\n got %s\nwant %s", b, got, want)
		}
	}
}

func modelOf(body string) string {
	var m struct{ Model string }
	_ = json.Unmarshal([]byte(body), &m)
	return m.Model
}

// a chat-only upstream that answers with reasoning, then text or a call
func reasoningChat(up *fakeUpstream, think string, call bool) {
	chatOnly(up, func(w http.ResponseWriter, body map[string]any) {
		msg := map[string]any{"role": "assistant", "reasoning_content": think, "content": "answer"}
		finish := "stop"
		if call {
			msg["content"] = nil
			msg["tool_calls"] = []any{map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "roll", "arguments": "{}"}}}
			finish = "tool_calls"
		}
		writeJSON(w, 200, map[string]any{"id": "chatcmpl-r1", "choices": []any{map[string]any{"message": msg, "finish_reason": finish}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4}})
	})
}

// covers D249 (stage 0) — the translation's own reasoning goes back: the
// reasoning item it made of a model's answer, sent again in a later
// request's input, becomes reasoning_content on the assistant message it
// came before — for that upstream's model only. Any other reasoning item (a
// provider's own, another model's, another upstream's with the same model
// id) is not sent, and is named.
func TestTranslationReplaysItsOwnReasoning(t *testing.T) {
	r := newRig(t, "")
	up, other := newUpstream(t), newUpstream(t)
	reasoningChat(up, "I should roll.", true)
	reasoningChat(other, "elsewhere", false)
	r.addUpstream("house", up, map[string]any{"responsesVia": "chat"})
	r.addUpstream("twin", other, map[string]any{"responsesVia": "chat"})
	fn := map[string]any{"type": "function", "name": "roll", "parameters": map[string]any{"type": "object"}}
	first := r.must(200, pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "house/m", "input": "roll", "tools": []any{fn}}).json(t)
	out := first["output"].([]any)
	item := out[0].(map[string]any)
	id, _ := item["id"].(string)
	if item["type"] != "reasoning" || !strings.HasPrefix(id, "rs_x") || item["encrypted_content"] != nil {
		t.Fatalf("the answer's reasoning item: %v", item)
	}
	twinItem := r.must(200, pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "twin/m", "input": "x"}).json(t)["output"].([]any)[0].(map[string]any)
	call := out[1].(map[string]any)
	history := func(reasoning ...any) []any {
		in := []any{map[string]any{"role": "user", "content": "roll"}}
		in = append(in, reasoning...)
		return append(in, call, map[string]any{"type": "function_call_output", "call_id": call["call_id"], "output": "4"})
	}
	next := func(model string, input []any) (answer, []any) {
		a := r.must(200, pOwner, "POST", "/llm/v1/responses", map[string]any{"model": model, "input": input, "tools": []any{fn}})
		target := up
		if strings.HasPrefix(model, "twin/") {
			target = other
		}
		msgs, _ := target.lastBody()["messages"].([]any)
		return a, msgs
	}
	// back to the model that produced it: on its assistant message
	a, msgs := next("house/m", history(item))
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" || asst["reasoning_content"] != "I should roll." || a.h.Get(DroppedHeader) != "" {
		t.Errorf("replayed: %v, dropped %q", msgs, a.h.Get(DroppedHeader))
	}
	// to another model of the same upstream, or the same model id on another upstream: never
	for _, model := range []string{"house/other-model", "twin/m"} {
		a, msgs := next(model, history(item))
		if _, has := msgs[1].(map[string]any)["reasoning_content"]; has || a.h.Get(DroppedHeader) != "input.reasoning" {
			t.Errorf("%s: %v, dropped %q", model, msgs[1], a.h.Get(DroppedHeader))
		}
	}
	// a provider's own item, another's tagged one, one with encrypted content or other content: named, not sent
	for name, it := range map[string]any{
		"OpenAI's encrypted":  map[string]any{"type": "reasoning", "id": "rs_68ab", "encrypted_content": "gAAA", "summary": []any{}},
		"a plain rs_ id":      map[string]any{"type": "reasoning", "id": "rs_chatcmpl-1_0", "summary": []any{map[string]any{"type": "summary_text", "text": "t"}}},
		"another upstream's":  twinItem,
		"forged content":      map[string]any{"type": "reasoning", "id": id, "summary": []any{}, "content": []any{map[string]any{"type": "reasoning_text", "text": "x"}}},
		"encrypted, our tag":  map[string]any{"type": "reasoning", "id": id, "encrypted_content": "e", "summary": []any{}},
		"a summary of images": map[string]any{"type": "reasoning", "id": id, "summary": []any{map[string]any{"type": "input_image", "image_url": "x"}}},
	} {
		a, msgs := next("house/m", history(it))
		if _, has := msgs[1].(map[string]any)["reasoning_content"]; has || a.h.Get(DroppedHeader) != "input.reasoning" {
			t.Errorf("%s: %v, dropped %q", name, msgs[1], a.h.Get(DroppedHeader))
		}
	}
	// reasoning with no answer after it (it ends the input): named
	a = r.must(200, pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "house/m", "input": []any{map[string]any{"role": "user", "content": "x"}, item}})
	if a.h.Get(DroppedHeader) != "input.reasoning" {
		t.Errorf("reasoning followed by nothing: dropped %q", a.h.Get(DroppedHeader))
	}
	// nothing of a reasoning block is logged
	if strings.Contains(r.logs.String(), "I should roll") || strings.Contains(r.logs.String(), "elsewhere") {
		t.Errorf("reasoning text in the log: %s", r.logs)
	}
	// the escape hatch: a record that says never
	r.must(200, pOwner, "PUT", "/llm/upstreams/house", map[string]any{"reasoning": map[string]any{"m": map[string]any{"supported_levels": []string{"high"}, "replay": "never"}}})
	a, msgs = next("house/m", history(item))
	if _, has := msgs[1].(map[string]any)["reasoning_content"]; has || a.h.Get(DroppedHeader) != "input.reasoning" {
		t.Errorf("replay never: %v, dropped %q", msgs[1], a.h.Get(DroppedHeader))
	}
}

// covers D249 (compat, robustness) — an upstream that refuses the field
// (400) gets the request once more without it: the answer is the second
// call's, and says what was left out. A context overflow and any other
// error pass as they are; nothing loops; nothing is remembered (a caller
// can provoke a refusal).
func TestTranslationReplayRefusedOnce(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	mode := "strict"
	chatOnly(up, func(w http.ResponseWriter, body map[string]any) {
		replayed := strings.Contains(fmt.Sprint(body["messages"]), "reasoning_content")
		switch {
		case mode == "strict" && replayed:
			writeJSON(w, 400, map[string]any{"error": map[string]any{"message": "messages.1: unknown field reasoning_content", "type": "invalid_request_error"}})
		case mode == "overflow":
			writeJSON(w, 400, map[string]any{"error": map[string]any{"message": "maximum context length exceeded", "code": "context_length_exceeded"}})
		case mode == "always400":
			writeJSON(w, 400, map[string]any{"error": map[string]any{"message": "bad tools", "type": "invalid_request_error"}})
		case mode == "500":
			writeJSON(w, 500, map[string]any{"error": map[string]any{"message": "boom"}})
		default:
			writeJSON(w, 200, map[string]any{"id": "chatcmpl-z", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "fine", "reasoning_content": "hm"}, "finish_reason": "stop"}},
				"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4}})
		}
	})
	r.addUpstream("house", up, map[string]any{"responsesVia": "chat"})
	mode = "ok"
	item := r.must(200, pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "house/m", "input": "x"}).json(t)["output"].([]any)[0]
	input := []any{map[string]any{"role": "user", "content": "x"}, item, map[string]any{"type": "function_call", "call_id": "c9", "name": "roll", "arguments": "{}"},
		map[string]any{"type": "function_call_output", "call_id": "c9", "output": "4"}, map[string]any{"role": "user", "content": "again"}}
	for _, stream := range []bool{false, true} {
		mode = "strict"
		n := up.calls()
		a := r.must(200, pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "house/m", "input": input, "stream": stream})
		if up.calls()-n != 2 || a.h.Get(DroppedHeader) != "input.reasoning" || strings.Contains(fmt.Sprint(up.lastBody()["messages"]), "reasoning_content") || !strings.Contains(string(a.body), "fine") {
			t.Errorf("refused, sent again (stream %v): %d calls, dropped %q, last %v", stream, up.calls()-n, a.h.Get(DroppedHeader), up.lastBody())
		}
		// the second request is the first without the reasoning sent back, and nothing else
		up.mu.Lock()
		firstReq, again := up.bodies[len(up.bodies)-2], up.bodies[len(up.bodies)-1]
		up.mu.Unlock()
		for _, m := range firstReq["messages"].([]any) {
			delete(m.(map[string]any), "reasoning_content")
		}
		if fmt.Sprint(firstReq) != fmt.Sprint(again) {
			t.Errorf("the request sent again differs by more than the reasoning:\n%v\n%v", firstReq, again)
		}
		// nothing remembered: the next request carries it again (and is refused, and sent again)
		n = up.calls()
		mode = "ok"
		r.must(200, pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "house/m", "input": input, "stream": stream})
		if !strings.Contains(fmt.Sprint(up.lastBody()["messages"]), "reasoning_content") || up.calls()-n != 1 {
			t.Errorf("a refusal was remembered (stream %v)", stream)
		}
	}
	for m, want := range map[string]int{"overflow": 400, "500": 500, "always400": 400} {
		mode = m
		n := up.calls()
		a := r.do(pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "house/m", "input": input})
		wantCalls := 1
		if m == "always400" {
			wantCalls = 2 // sent again once, refused again: that refusal is the caller's — no third
		}
		if a.code != want || up.calls()-n != wantCalls {
			t.Errorf("%s: %d after %d calls — want %d after %d", m, a.code, up.calls()-n, want, wantCalls)
		}
	}
	// a request without reasoning sent back is never sent twice
	mode = "always400"
	n := up.calls()
	r.do(pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "house/m", "input": "x"})
	if up.calls()-n != 1 {
		t.Errorf("a refusal of a request without replay was retried: %d calls", up.calls()-n)
	}
}

// covers D249 (compat) — an older agent template on this xbind: its own
// recorded request (the template's golden file, request/chat: kimi-k3, a
// reasoning_effort, OpenRouter's reasoning_details sent back) goes to a
// model nobody describes as it came, and to a described one with only its
// effort mapped.
func TestAnOlderTemplatesRequest(t *testing.T) {
	raw, err := os.ReadFile("../../builtin-templates/agent/_backend/testdata/llm_wire_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]json.RawMessage
	_ = json.Unmarshal(raw, &golden)
	var body string
	if err := json.Unmarshal(golden["request/chat"], &body); err != nil || !strings.Contains(body, `"reasoning_effort":"high"`) {
		t.Fatalf("request/chat: %v %s", err, body)
	}
	r := newRig(t, "")
	up := newUpstream(t)
	up.setModels(map[string]any{"id": "kimi-k3"}, map[string]any{"id": "llama"})
	r.addUpstream("house", up, nil)
	for model, want := range map[string]string{"house/llama": "", "house/kimi-k3": "high"} {
		b := strings.Replace(body, `"model":"kimi-k3"`, `"model":"`+model+`"`, 1)
		a := r.must(200, pOwner, "POST", "/llm/v1/chat/completions", json.RawMessage(b))
		wantBody, _, _ := sdkllm.RewriteRequest([]byte(b), strings.TrimPrefix(model, "house/"), true)
		if got := up.lastRaw(); !bytes.Equal(got, wantBody) || a.h.Get(EffortHeader) != want {
			t.Errorf("%s: header %q (want %q)\n got %s\nwant %s", model, a.h.Get(EffortHeader), want, got, wantBody)
		}
	}
}
