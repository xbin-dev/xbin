package main

// wire_test.go — what the gateway reads off the wire, end to end: each call
// goes through the real proxy to an upstream answering one recorded shape,
// and the counters (GET /stats, GET /metrics) say what was counted. The
// shapes are the two wires', streamed and not, as providers send them.

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// answers are upstream response bodies by the model that asks for them.
var answers = map[string]struct {
	ctype, body string
	in, out     int64
}{
	"chat-body": {"application/json",
		`{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1234,"completion_tokens":567,"total_tokens":1801,"prompt_tokens_details":{"cached_tokens":1024},"completion_tokens_details":{"reasoning_tokens":192}}}`,
		1234, 567},
	"chat-stream": {"text/event-stream",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"The \\\"usage\\\" field\"},\"finish_reason\":null}],\"usage\":null}\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":null}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2048,\"completion_tokens\":310,\"total_tokens\":2358,\"prompt_tokens_details\":{\"cached_tokens\":1024}}}\n\n" +
			"data: [DONE]\n\n",
		2048, 310},
	// spaces around the colons, and no total
	"chat-spaced": {"application/json", "{\"choices\":[],\"usage\":{\n  \"prompt_tokens\" : 5,\n  \"completion_tokens\" : 6\n}}", 5, 6},
	"responses-stream": {"text/event-stream",
		"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"output\":[],\"usage\":null}}\n\n" +
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"Counted once.\"}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[]," +
			"\"usage\":{\"input_tokens\":4096,\"input_tokens_details\":{\"cached_tokens\":3072},\"output_tokens\":640,\"output_tokens_details\":{\"reasoning_tokens\":512},\"total_tokens\":4736}}}\n\n",
		4096, 640},
	"responses-body": {"application/json", `{"id":"r2","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":8,"total_tokens":15}}`, 7, 8},
	// usage is read from the answer's last 8 KiB: a long answer before it
	// is no obstacle, a long one after it hides it
	"long-then-usage": {"application/json", `{"choices":[{"message":{"content":"` + strings.Repeat("word ", 4000) + `"}}],"usage":{"prompt_tokens":900,"completion_tokens":4000,"total_tokens":4900}}`, 900, 4000},
	"usage-then-long": {"application/json", `{"usage":{"prompt_tokens":900,"completion_tokens":4000},"choices":[{"message":{"content":"` + strings.Repeat("word ", 4000) + `"}}]}`, 0, 0},
	// two usage blocks in a stream: the last one counts
	"two-usages": {"text/event-stream",
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":25}}\n\ndata: [DONE]\n\n",
		10, 25},
	"no-usage": {"application/json", `{"object":"list","data":[{"id":"gpt-5"}]}`, 0, 0},
	"an-error": {"application/json", `{"error":{"message":"no such model","type":"invalid_request_error","code":"model_not_found"}}`, 0, 0},
}

// wireUpstream answers by model and records the request URIs it got.
func wireUpstream(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var uris []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		uris = append(uris, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		var body struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		a, ok := answers[body.Model]
		if !ok {
			http.Error(w, "no such answer", 404)
			return
		}
		w.Header().Set("Content-Type", a.ctype)
		if body.Model == "an-error" {
			w.WriteHeader(404)
		}
		_, _ = io.WriteString(w, a.body)
	}))
	t.Cleanup(up.Close)
	return up, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), uris...) }
}

func setConfig(t *testing.T, cfg string) {
	t.Helper()
	fx.mu.Lock()
	fx.kv["config"] = []byte(cfg)
	fx.mu.Unlock()
}

func backendStats(t *testing.T) (reqs, in, out int64, cost float64) {
	t.Helper()
	all, _ := statsAs(t, owner)
	var by map[string]struct {
		Reqs, TokIn, TokOut int64
		Cost                float64
	}
	if err := json.Unmarshal(all["backends"], &by); err != nil {
		t.Fatal(err)
	}
	s := by["fake"]
	return s.Reqs, s.TokIn, s.TokOut, s.Cost
}

// Every shape's tokens are counted as its provider reported them, the
// answer reaches the caller byte for byte, and the cost is the tokens at
// the model's configured price (USD per million, input and output).
func TestCountsEachWireShape(t *testing.T) {
	fresh(t, 0)
	up, _ := wireUpstream(t)
	const pin, pout = 0.15, 0.6
	prices := map[string]any{}
	for m := range answers {
		prices[m] = map[string]float64{"in": pin, "out": pout}
	}
	delete(prices, "responses-body") // one model without a price: counted, free
	cfg, _ := json.Marshal(map[string]any{"backends": map[string]any{"fake": map[string]string{"baseURL": up.URL}}, "pricing": prices})
	setConfig(t, string(cfg))

	var wantReqs, wantIn, wantOut int64
	var wantCost float64
	for _, m := range []string{"chat-body", "chat-stream", "chat-spaced", "responses-stream", "responses-body",
		"long-then-usage", "usage-then-long", "two-usages", "no-usage", "an-error"} {
		a := answers[m]
		w := httptest.NewRecorder()
		handleProxy(w, request(context.Background(), "POST", "/v1/chat/completions", `{"model":"`+m+`","messages":[]}`, chat))
		if w.Body.String() != a.body || w.Header().Get("Content-Type") != a.ctype {
			t.Fatalf("%s: the answer was not relayed as it came (%d, %q, %d bytes)", m, w.Code, w.Header().Get("Content-Type"), w.Body.Len())
		}
		wantReqs, wantIn, wantOut = wantReqs+1, wantIn+a.in, wantOut+a.out
		if m != "responses-body" {
			wantCost += float64(a.in)/1e6*pin + float64(a.out)/1e6*pout
		}
		reqs, in, out, cost := backendStats(t)
		if reqs != wantReqs || in != wantIn || out != wantOut || math.Abs(cost-wantCost) > 1e-12 {
			t.Fatalf("after %s: %d calls, %d in, %d out, $%v; want %d, %d, %d, $%v", m, reqs, in, out, cost, wantReqs, wantIn, wantOut, wantCost)
		}
	}

	w := httptest.NewRecorder()
	handleMetrics(w, request(context.Background(), "GET", "/metrics", "", owner))
	for _, line := range []string{
		`llmgw_requests_total{backend="fake"} 10`,
		`llmgw_tokens_in_total{backend="fake"} 8300`,
		`llmgw_tokens_out_total{backend="fake"} 5556`,
		`llmgw_cost_usd_total{backend="fake"} 0.004573`,
	} {
		if !strings.Contains(w.Body.String(), line+"\n") {
			t.Errorf("GET /metrics lacks %q:\n%s", line, w.Body)
		}
	}
}

// A price is looked up by the upstream's model id exactly: an alias is
// priced as its target, a backend prefix is not part of the id, and no
// other spelling matches.
func TestPriceIsByExactModelID(t *testing.T) {
	fresh(t, 0)
	up, _ := wireUpstream(t)
	cfg, _ := json.Marshal(map[string]any{
		"backends": map[string]any{"fake": map[string]string{"baseURL": up.URL}},
		"aliases":  map[string]string{"fast": "fake/chat-body"},
		"pricing":  map[string]any{"chat-body": map[string]float64{"in": 2.5, "out": 10}, "chat-*": map[string]float64{"in": 100, "out": 100}, "stream": map[string]float64{"in": 100, "out": 100}},
	})
	setConfig(t, string(cfg))
	one := float64(1234)/1e6*2.5 + float64(567)/1e6*10
	for i, m := range []string{"chat-body", "fast", "fake/chat-body", "chat-stream"} {
		w := httptest.NewRecorder()
		handleProxy(w, request(context.Background(), "POST", "/v1/chat/completions", `{"model":"`+m+`"}`, chat))
		want := one * float64(min(i+1, 3)) // chat-stream has no price of its own: "chat-*" and "stream" are other ids
		if _, _, _, cost := backendStats(t); w.Code != 200 || math.Abs(cost-want) > 1e-12 {
			t.Fatalf("after %s: %d, $%v, want $%v", m, w.Code, cost, want)
		}
	}
	// the settings page reads the prices back as they are stored
	w := httptest.NewRecorder()
	handleGetConfig(w, request(context.Background(), "GET", "/config", "", owner))
	var got struct {
		Pricing json.RawMessage `json:"pricing"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || string(got.Pricing) != `{"chat-*":{"in":100,"out":100},"chat-body":{"in":2.5,"out":10},"stream":{"in":100,"out":100}}` {
		t.Fatalf("GET /config pricing: %s (%v)", got.Pricing, err)
	}
}

// A backend's base URL is written with or without the /v1 the request path
// starts with: the upstream is asked the same path either way, the query
// kept.
func TestUpstreamURLJoin(t *testing.T) {
	for _, suffix := range []string{"", "/", "/v1", "/v1/", "/api/v1"} {
		fresh(t, 0)
		up, uris := wireUpstream(t)
		cfg, _ := json.Marshal(map[string]any{"backends": map[string]any{"fake": map[string]string{"baseURL": up.URL + suffix}}})
		setConfig(t, string(cfg))
		handleProxy(httptest.NewRecorder(), request(context.Background(), "POST", "/v1/chat/completions?api-version=7&x=a%20b", `{"model":"chat-body"}`, chat))
		handleProxy(httptest.NewRecorder(), request(context.Background(), "POST", "/v1/responses", `{"model":"responses-body"}`, chat))
		w := httptest.NewRecorder()
		handleModels(w, request(context.Background(), "GET", "/v1/models", "", chat))
		pre := strings.TrimSuffix(strings.TrimSuffix(suffix, "/"), "/v1")
		want := "POST " + pre + "/v1/chat/completions?api-version=7&x=a%20b, POST " + pre + "/v1/responses, GET " + pre + "/v1/models"
		if got := strings.Join(uris(), ", "); got != want {
			t.Errorf("base URL …%q: the upstream was asked %s; want %s", suffix, got, want)
		}
	}
}
