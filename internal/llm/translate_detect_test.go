package llm

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// respondsNative makes up answer as a provider that HAS a Responses API
// and refuses a model it doesn't know with body (status 404) on both
// routes — the model named "nope".
func respondsNative(up *fakeUpstream, notFound string) {
	up.mu.Lock()
	defer up.mu.Unlock()
	up.fail = func(w http.ResponseWriter, r *http.Request) bool {
		up.mu.Lock()
		body := up.bodies[len(up.bodies)-1]
		up.mu.Unlock()
		if m, _ := body["model"].(string); strings.Contains(m, "nope") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, notFound)
			return true
		}
		return false
	}
}

// covers D244 — nobody can talk an upstream into being "chat only". The
// detection read the upstream's refusal after it had been rewritten for
// the caller, so any 404 that was not OpenAI-shaped counted as "no such
// route": a caller naming a model the upstream doesn't have (Ollama and
// vLLM answer that in shapes of their own) switched EVERYONE's Responses
// calls for that upstream to translation for ten minutes. It is decided
// on the upstream's own status and body, a refusal that speaks of the
// model is the model's, and nothing is remembered until a translated call
// has itself succeeded.
func TestResponsesDetectionCantBeForced(t *testing.T) {
	for name, notFound := range map[string]string{
		"Ollama":                    `{"error":"model 'nope' not found, try pulling it first"}`,
		"vLLM":                      `{"object":"error","message":"The model ` + "`nope`" + ` does not exist.","type":"NotFoundError","param":null,"code":404}`,
		"a bare 404 on both routes": `{"detail":"Not Found"}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, "")
			up := newUpstream(t)
			respondsNative(up, notFound)
			r.addUpstream("main", up, nil) // no model list: any name passes through
			r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"people": "everyone"})
			// the attack: any person names a model the upstream doesn't have, a few times
			for range 3 {
				if a := r.do(pUser("mallory"), "POST", "/llm/v1/responses", map[string]any{"model": "main/nope", "input": "x"}); a.code != 404 {
					t.Fatalf("a Responses call for a model the upstream doesn't have: %d %s, want its 404", a.code, a.body)
				}
			}
			// everyone else's Responses calls still go to the upstream's own Responses API
			n := up.calls()
			a := r.do(pUser("bob"), "POST", "/llm/v1/responses", map[string]any{"model": "main/gpt-5", "input": "x", "previous_response_id": "resp_1"})
			if a.code != 200 || !strings.Contains(string(a.body), `"resp_1"`) || up.calls() != n+1 {
				t.Fatalf("after the attack, a Responses call with previous_response_id: %d %s (%d upstream calls) — want the upstream's own answer", a.code, a.body, up.calls()-n)
			}
			if sent := up.lastBody(); sent["messages"] != nil || sent["previous_response_id"] != "resp_1" {
				t.Errorf("the call was translated: %v", sent)
			}
			cfg := r.must(200, pOwner, "GET", "/llm/config", nil).json(t)
			if uv := cfg["upstreams"].([]any)[0].(map[string]any); uv["translating"] == true {
				t.Errorf("the upstream reads as chat only: %v", uv)
			}
		})
	}
}

// covers D244 — detection works whatever the upstream is called (the
// rewritten refusal named the upstream, and one called "models-eu" was
// never detected: its name holds "model"); it is remembered only once a
// translated call succeeded, per model, shown to the admin with when it
// was found, and setting responsesVia again forgets it.
func TestResponsesDetectionRemembersWhatWorked(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	chatFails := true
	chatOnly(up, func(w http.ResponseWriter, body map[string]any) {
		if chatFails {
			writeJSON(w, 500, map[string]any{"error": map[string]any{"message": "boom", "type": "server_error"}})
			return
		}
		writeJSON(w, 200, map[string]any{"id": "chatcmpl-1", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4}})
	})
	r.addUpstream("models-eu", up, nil)
	call := func() answer {
		return r.do(pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "models-eu/gpt-5", "input": "x"})
	}
	view := func() map[string]any {
		return r.must(200, pOwner, "GET", "/llm/config", nil).json(t)["upstreams"].([]any)[0].(map[string]any)
	}
	// the route is missing and the chat call fails: the caller gets the chat call's failure, and nothing is remembered
	if a := call(); a.code != 500 || up.calls() != 2 {
		t.Fatalf("no Responses route and a failing chat call: %d %s after %d upstream calls", a.code, a.body, up.calls())
	}
	if view()["translating"] == true {
		t.Fatal("remembered as chat only though no translated call worked")
	}
	if a := call(); a.code != 500 || up.calls() != 4 {
		t.Fatalf("the next call didn't ask the upstream for the route again: %d upstream calls", up.calls())
	}
	// a translated call that works: remembered, and said to the admin with when
	chatFails = false
	if a := call(); a.code != 200 || up.calls() != 6 {
		t.Fatalf("no Responses route and a working chat call: %d %s after %d upstream calls", a.code, a.body, up.calls())
	}
	v := view()
	since, _ := v["translatingSince"].(string)
	if _, err := time.Parse(time.RFC3339, since); v["translating"] != true || err != nil {
		t.Fatalf("the upstream's view after detection: translating %v since %q", v["translating"], since)
	}
	if a := call(); a.code != 200 || up.calls() != 7 {
		t.Fatalf("once detected, a call goes straight to chat: %d upstream calls", up.calls())
	}
	// another model of the same upstream is found for itself
	if a := r.do(pOwner, "POST", "/llm/v1/responses", map[string]any{"model": "models-eu/gpt-5-mini", "input": "x"}); a.code != 200 || up.calls() != 9 {
		t.Fatalf("another model: %d after %d upstream calls, want its own probe and then chat", a.code, up.calls())
	}
	// the admin sets responsesVia again: what was detected is forgotten
	r.must(200, pOwner, "PUT", "/llm/upstreams/models-eu", map[string]any{"responsesVia": "auto"})
	if view()["translating"] == true {
		t.Fatal("setting responsesVia didn't forget the detection")
	}
	if a := call(); a.code != 200 || up.calls() != 11 {
		t.Fatalf("after the reset the route is asked for again: %d upstream calls", up.calls())
	}
	// an explicit setting wins over what was detected
	r.must(200, pOwner, "PUT", "/llm/upstreams/models-eu", map[string]any{"responsesVia": "upstream"})
	if a := call(); a.code != 404 || view()["translating"] == true {
		t.Fatalf("responsesVia upstream after a detection: %d, view %v", a.code, view())
	}
}
