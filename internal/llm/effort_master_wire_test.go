package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
)

// effort_master_wire_test.go — D249 (compat): what reaches an upstream that
// is no model's maker for a model nothing describes there is, byte for
// byte, what xbind sent before reasoning effort existed. The golden file
// (testdata/effort_master_wire.json) was recorded by this same file run on
// master 6ab5f7b6 (v0.3.82), with EFFORT_MASTER_WIRE_OUT set: chat,
// streamed chat, the direct Responses wire and the translated one; with and
// without an effort; duplicate keys, large numbers and unicode escapes;
// router-style ids and ids xbind's table matches by pattern; a router's
// listing that says the models reason. This file must keep compiling on
// master: it uses only the rig master has.

type wireRecorder struct {
	*httptest.Server
	mu  sync.Mutex
	got []string // "<path> <raw body>" of every call but the model list
}

func newWireRecorder(t *testing.T) *wireRecorder {
	w := &wireRecorder{}
	w.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/models") {
			rw.Header().Set("Content-Type", "application/json")
			var data []string
			for _, id := range masterWireModels {
				// a router's listing as it comes (OpenRouter-shaped): reasoning among its features, no levels
				data = append(data, fmt.Sprintf(`{"id":%q,"object":"model","context_length":131072,"supported_features":["reasoning","tools"],"supported_sampling_parameters":["temperature","top_p"]}`, id))
			}
			fmt.Fprintf(rw, `{"object":"list","data":[%s]}`, strings.Join(data, ","))
			return
		}
		w.mu.Lock()
		w.got = append(w.got, r.URL.Path+" "+string(raw))
		w.mu.Unlock()
		stream := strings.Contains(string(raw), `"stream":true`)
		switch {
		case strings.HasSuffix(r.URL.Path, "/responses") && stream:
			rw.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(rw, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
		case strings.HasSuffix(r.URL.Path, "/responses"):
			rw.Header().Set("Content-Type", "application/json")
			fmt.Fprint(rw, `{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
		case stream:
			rw.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(rw, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\ndata: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"id\":\"c1\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n")
		default:
			rw.Header().Set("Content-Type", "application/json")
			fmt.Fprint(rw, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		}
	}))
	t.Cleanup(w.Close)
	return w
}

func (w *wireRecorder) take() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.got
	w.got = nil
	return out
}

// masterWireModels: router-style ids, ids the table matches exactly, after
// the last "/", and by prefix — on a host that is no maker's.
var masterWireModels = []string{"deepseek/deepseek-v4.1-flash", "moonshotai/kimi-k3", "my-org/deepseek-v4-finetune", "glm-5.2", "z-ai/glm-5.3", "x-ai/grok-4.7", "local-llama"}

var masterWireChat = map[string]string{
	"effort":         `{"model":%q,"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`,
	"stream-none":    `{"model":%q,"stream":true,"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none","stream_options":{"include_usage":true}}`,
	"no-effort":      `{"model":%q,"messages":[{"role":"user","content":"hi"}],"temperature":0.2}`,
	"duplicate-keys": `{"model":%q,"reasoning_effort":"low","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"max"}`,
	"numbers-unicode": `{"model":%q,"max_tokens":12345678901234567890,"temperature":0.30000000000000004,"seed":1e400,` +
		`"messages":[{"role":"user","content":"caf\u00e9 \ud83d\ude00 \u003cb\u003e \"q\""}],"reasoning":{"effort":"xhigh","exclude":true},"chat_template_kwargs":{"thinking":true,"reasoning_effort":62}}`,
	"stream-xhigh": `{"stream":true,"model":%q,"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`,
}

var masterWireResponses = map[string]string{
	"effort":          `{"model":%q,"input":"x","reasoning":{"effort":"medium","summary":"auto"},"include":["reasoning.encrypted_content"]}`,
	"stream-none":     `{"model":%q,"input":"x","stream":true,"reasoning":{"effort":"none"}}`,
	"no-effort":       `{"model":%q,"input":[{"role":"user","content":"hi"}],"temperature":0.2}`,
	"duplicate-keys":  `{"model":%q,"input":"x","reasoning":{"effort":"low","effort":"max"}}`,
	"numbers-unicode": `{"model":%q,"input":"caf\u00e9 \ud83d\ude00 \u003cb\u003e","max_output_tokens":99999999999999999999,"top_p":1e-7,"reasoning":{"effort":"xhigh"}}`,
}

func TestEffortWireAsMaster(t *testing.T) {
	r := newRig(t, "")
	up := newWireRecorder(t)
	r.must(200, pOwner, "PUT", "/llm/upstreams/gw", map[string]any{"baseURL": up.URL + "/v1", "key": upstreamKey})
	r.must(200, pOwner, "PUT", "/llm/upstreams/gwc", map[string]any{"baseURL": up.URL + "/v1", "key": upstreamKey, "responsesVia": "chat"})
	got := map[string]any{}
	one := func(name, path, body string) {
		a := r.do(pOwner, "POST", path, []byte(body))
		got[name] = map[string]any{"code": a.code, "dropped": a.h.Get("X-XBin-LLM-Dropped"), "effort": a.h.Get("X-XBin-LLM-Effort"), "upstream": up.take()}
	}
	for _, m := range masterWireModels {
		for _, k := range sortedKeys(masterWireChat) {
			one("chat/"+k+"/"+m, "/llm/v1/chat/completions", fmt.Sprintf(masterWireChat[k], "gw/"+m))
		}
		for _, k := range sortedKeys(masterWireResponses) {
			one("responses/"+k+"/"+m, "/llm/v1/responses", fmt.Sprintf(masterWireResponses[k], "gw/"+m))
			one("translated/"+k+"/"+m, "/llm/v1/responses", fmt.Sprintf(masterWireResponses[k], "gwc/"+m))
		}
	}
	out, err := json.MarshalIndent(got, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, '\n')
	if path := os.Getenv("EFFORT_MASTER_WIRE_OUT"); path != "" {
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile("testdata/effort_master_wire.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) == string(want) {
		return
	}
	var w map[string]json.RawMessage
	_ = json.Unmarshal(want, &w)
	for _, k := range sortedKeys(got) {
		g, _ := json.MarshalIndent(got[k], " ", " ")
		var wk any
		_ = json.Unmarshal(w[k], &wk)
		wb, _ := json.MarshalIndent(wk, " ", " ")
		if string(g) != string(wb) {
			t.Errorf("%s:\n got %s\nwant %s", k, g, wb)
		}
	}
	if !t.Failed() {
		t.Error("testdata/effort_master_wire.json differs: re-record it on master")
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
