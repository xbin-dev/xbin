package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// covers D226 — bx llm speaks the connector's API: each command's request,
// a key read from a file and never from the command line's echo, what a
// lock and an OpenAI-shaped error look like, and an xbind without the
// connector.
func TestBxLLMRequests(t *testing.T) {
	const view = `{"configured":true,"enabled":true,"people":"admins","upstreams":[{"name":"main","kind":"openai","baseURL":"https://api.example/v1",
		"key":{"set":true,"source":"vault","hint":"…cdef"},"models":["gpt-5"],"private":false,"prices":{"gpt-5":{"in":1.25,"out":10}},"source":"admin","locked":false,
		"reasoning":{"gpt-5*":{"supported_levels":["low","medium","high"],"default_level":"medium","can_disable":false,"source":"admin"}},
		"replayOff":{"strict-thinker":{"since":"2026-11-20T10:00:00Z","field":"reasoning_content","callers":2}}},
		{"name":"hosted","kind":"openai","baseURL":"https://host.example","key":{"set":true,"source":"host"},"models":[],"private":true,"source":"host","locked":true}],
		"defaults":{"model":"main/gpt-5","roles":{"agent":"main/gpt-5"}},"aliases":{"fast":"main/gpt-5"},"quotas":{},
		"locks":{"upstreams.hosted":{"locked":true,"reason":"set by your host"},"people":{"locked":true,"reason":"set by your host"}},
		"host":{"active":true},"vault":"open","problems":["alias old: no upstream offers it"],"concurrency":16,"notices":["the key of upstream main was removed: its address changed"]}`
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, strings.TrimSpace(r.Method+" "+r.URL.RequestURI()+" "+string(b)))
		switch {
		case r.URL.Path == "/api/xbin/llm":
			_, _ = io.WriteString(w, `{"configured":true,"enabled":true,"callable":true,"url":"/api/xbin/llm/v1","ref":"xbin:llm","people":"admins","admin":true,"user":"owner",
				"defaults":{"model":"main/gpt-5"},"quota":{"limits":[{"name":"tokensPerDay","value":1000,"source":"default","action":"stop","used":1200,"resets":"2026-11-15T00:00:00Z","state":"stop"}],"workspace":[],"state":"stop"}}`)
		case r.URL.Path == "/api/xbin/llm/v1/models":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"main/gpt-5"},{"id":"fast","alias_of":"main/gpt-5"},
				{"id":"main/deepseek-v4.1-flash","effort":{"supported_levels":["low","high","max"],"default_level":"high","can_disable":true,"source":"upstream"}},
				{"id":"main/plain","effort":{"supported_levels":[],"source":"admin"}},{"id":"main/odd","effort":"junk"},{"id":"main/router","effort":{"reasons":true,"source":"upstream"}},
				{"id":"main/kimi-k3","xbin_effort_suggestion":{"supported_levels":["low","high","max","turbo"],"default_level":"max","can_disable":false,"aliases":{"medium":"high","xhigh":"ultra"},"source":"xbin"}},
				{"id":"main/sly","effort":{"supported_levels":["turbo","\u001b[31mred","high"],"default_level":"\u001b[2J","source":"\u001b[0m"}},
				{"id":"main/junk","effort":{"supported_levels":["turbo"]}}],"upstream_errors":{"hosted":"its model list is unavailable"}}`)
		case r.URL.Path == "/api/xbin/llm/usage":
			_, _ = io.WriteString(w, `{"period":"day","from":"2026-11-01","to":"2026-11-14","scope":"all","rows":[{"day":"2026-11-14","user":"bob","component":"apps/agent","model":"main/gpt-5","requests":3,"inputTokens":300,"outputTokens":60,"cost":0.0009,"unpriced":1}],
				"totals":{"requests":3,"inputTokens":300,"outputTokens":60,"cost":0.0009,"unpriced":1}}`)
		case r.URL.Path == "/api/xbin/llm/quotas":
			_, _ = io.WriteString(w, `{"quotas":{"default":{"tokensPerDay":2000000,"action":"warn"},"users":{"bob":{"costPerMonth":25,"tokensPerDay":-1}}},"locks":{"quotas.default":{"locked":true,"reason":"set by your host"}},
				"effective":{"user":"bob","limits":[{"name":"costPerMonth","value":25,"source":"user","action":"warn","used":3.5,"resets":"2026-12-01T00:00:00Z","state":"ok"}],"workspace":[]}}`)
		case strings.HasSuffix(r.URL.Path, "/test"):
			_, _ = io.WriteString(w, `{"ok":true,"model":"gpt-5","status":200,"ms":412,"tokens":17}`)
		case r.URL.Path == "/api/xbin/llm/upstreams/hosted":
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"upstreams.hosted: set by your host — it can't be changed here","locked":["upstreams.hosted"],"reason":"set by your host"}`)
		default:
			_, _ = io.WriteString(w, view)
		}
	}))
	defer srv.Close()
	t.Setenv("XBIN_URL", srv.URL)
	t.Setenv("XBIN_TOKEN", "t")
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte("sk-from-a-file-0001\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out string
	for _, args := range [][]string{
		{},
		{"upstream", "add", "main", "https://api.example/v1", "--key-file", keyFile, "--models", "gpt-5,gpt-5-mini", "--private", "--price", "gpt-5=1.25:10:0.125"},
		{"upstream", "add", "local", "http://127.0.0.1:11434/v1", "--no-key"},
		{"upstream", "set", "main", "--any-model", "--public", "--no-key"},
		{"upstream", "test", "main", "gpt-5"},
		{"upstream", "replay-reset", "main", "strict-thinker"},
		{"upstream", "rm", "main"},
		{"models"},
		{"default", "set", "main/gpt-5"},
		{"default", "set", "main/gpt-5-mini", "--role", "harness"},
		{"default", "unset", "--role=agent"},
		{"alias", "set", "*", "main/gpt-5"},
		{"alias", "rm", "fast"},
		{"people", "everyone"},
		{"concurrency", "8"},
		{"upstream", "set", "main", "--price", "*=2:4"},
		{"upstream", "set", "main", "--reasoning", "deepseek*=deepseek", "--reasoning=plain=-", "--reasoning", "z-ai/glm-5.2=xhigh,none,medium,high/high"},
		{"upstream", "set", "main", "--reasoning", "kimi-k3=maker"},
		{"off"},
		{"usage", "--month", "--user", "bob", "--from", "2026-01-01"},
		{"quota"},
		{"quota", "show", "bob"},
		{"quota", "set", "default", "tokens/day=2M", "cost/month=50", "action=warn"},
		{"quota", "set", "user:bob", "tokens/day=unlimited", "tokens/month=500k"},
		{"quota", "rm", "org:eng"},
	} {
		out += captureStdoutF10(t, func() {
			if err := cmdLLM(args); err != nil {
				t.Fatalf("bx llm %v: %v", args, err)
			}
		})
	}
	want := []string{
		"GET /api/xbin/llm",
		"GET /api/xbin/llm/config",
		`PUT /api/xbin/llm/upstreams/main {"baseURL":"https://api.example/v1","key":"sk-from-a-file-0001","models":["gpt-5","gpt-5-mini"],"prices":{"gpt-5":{"in":1.25,"out":10,"cachedIn":0.125}},"private":true}`,
		`PUT /api/xbin/llm/upstreams/local {"baseURL":"http://127.0.0.1:11434/v1","key":""}`,
		`PUT /api/xbin/llm/upstreams/main {"key":"","models":[],"private":false}`,
		`POST /api/xbin/llm/upstreams/main/test {"model":"gpt-5"}`,
		`POST /api/xbin/llm/upstreams/main/replay-reset {"model":"strict-thinker"}`,
		"DELETE /api/xbin/llm/upstreams/main",
		"GET /api/xbin/llm/v1/models",
		`PUT /api/xbin/llm/config {"defaults":{"model":"main/gpt-5"}}`,
		`PUT /api/xbin/llm/config {"defaults":{"roles":{"harness":"main/gpt-5-mini"}}}`,
		`PUT /api/xbin/llm/config {"defaults":{"roles":{"agent":""}}}`,
		`PUT /api/xbin/llm/config {"aliases":{"*":"main/gpt-5"}}`,
		`PUT /api/xbin/llm/config {"aliases":{"fast":""}}`,
		`PUT /api/xbin/llm/config {"people":"everyone"}`,
		`PUT /api/xbin/llm/config {"concurrency":8}`,
		"GET /api/xbin/llm/config",
		`PUT /api/xbin/llm/upstreams/main {"prices":{"*":{"in":2,"out":4},"gpt-5":{"in":1.25,"out":10}}}`,
		`PUT /api/xbin/llm/upstreams/main {"reasoning":{"deepseek*":{"preset":"deepseek"},"plain":null,"z-ai/glm-5.2":{"can_disable":true,"default_level":"high","supported_levels":["medium","high","xhigh"]}}}`,
		// the record /xbin/llm's "Use these" stores for that suggestion (hack/llm-kit.test.mjs, suggestRecord)
		"GET /api/xbin/llm/v1/models",
		`PUT /api/xbin/llm/upstreams/main {"reasoning":{"kimi-k3":{"aliases":{"medium":"high"},"can_disable":false,"default_level":"max","supported_levels":["low","high","max"]}}}`,
		`PUT /api/xbin/llm/config {"enabled":false}`,
		"GET /api/xbin/llm/usage?from=2026-01-01&period=month&user=bob",
		"GET /api/xbin/llm/quotas",
		"GET /api/xbin/llm/quotas?user=bob",
		`PUT /api/xbin/llm/quotas {"default":{"tokensPerDay":2000000,"costPerMonth":50,"action":"warn"}}`,
		`PUT /api/xbin/llm/quotas {"users":{"bob":{"tokensPerDay":-1,"tokensPerMonth":500000}}}`,
		`PUT /api/xbin/llm/quotas {"orgs":{"eng":null}}`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("requests:\n got  %s\n want %s", strings.Join(got, "\n      "), strings.Join(want, "\n      "))
	}
	for _, line := range []string{
		"LLM connector: on",
		"people who may call directly: admins  [set by your host]",
		"upstream main  https://api.example/v1  (key …cdef)",
		"upstream hosted  https://host.example  (key: the host's, private)  [set by your host]",
		"models: any model (its own list)",
		"price gpt-5: in $1.25, out $10 per million tokens",
		"main/deepseek-v4.1-flash  Reasoning: Low · High · Max (default High; can be off) · from the upstream",
		"main/plain  Reasoning: none (an effort sent is dropped) · from the admin",
		"main/odd\n",
		"main/router  Reasoning: yes, its levels not described — no control is offered (bx llm upstream set … --reasoning) · from the upstream",
		"    gpt-5*: Reasoning: Low · Medium · High (default Medium; always thinks) · from the admin",
		"    strict-thinker: replay off for 2 callers since ",
		"the upstream refused reasoning_content (bx llm upstream replay-reset main strict-thinker)",
		"upstream main: reasoning is sent back again",
		"main/kimi-k3  Reasoning: not described — xbin knows the maker's levels: Low · High · Max (default Max) — to use them: bx llm upstream set main --reasoning 'kimi-k3=maker'",
		"main/sly  Reasoning: High · from the connector\n",
		"main/junk\n",
		"agent model: main/gpt-5",
		"alias fast → main/gpt-5",
		"! alias old: no upstream offers it",
		"calls in flight per caller: 16",
		"note: the key of upstream main was removed: its address changed",
		"you (owner) may call it at /api/xbin/llm/v1",
		"tokens/day (default, stop): 1200 of 1000, resets 2026-11-15T00:00:00Z  ← reached (stop)",
		"upstream main: ok — gpt-5 answered in 412 ms (17 tokens)",
		"fast → main/gpt-5",
		"everyone's LLM usage, 2026-11-01 to 2026-11-14 (UTC), per day",
		"$0.0009+",
		"default (each person)  tokens/day=2000000 action=warn  [set by your host]",
		"user:bob               tokens/day=unlimited cost/month=$25",
		"cost/month (user, warn): $3.50 of $25.00, resets 2026-12-01T00:00:00Z",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}
	if strings.Contains(out, "sk-from-a-file") {
		t.Error("the key was printed")
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "Turbo") {
		t.Error("words off the ladder from a model list were printed (l2)")
	}

	// a locked setting, an OpenAI-shaped refusal, a usage error
	if err := cmdLLM([]string{"upstream", "set", "hosted", "--url", "https://x"}); err == nil || !strings.Contains(err.Error(), "set by your host") || !strings.Contains(err.Error(), "409") {
		t.Errorf("a locked upstream: %v", err)
	}
	// a key on the command line is refused, and never sent
	before := len(got)
	if err := cmdLLM([]string{"upstream", "add", "x", "http://y", "--key", "sk-on-the-command-line"}); err == nil || !strings.Contains(err.Error(), "no key on the command line") || len(got) != before {
		t.Errorf("--key on the command line: %v (requests sent: %d)", err, len(got)-before)
	}
	if err := cmdLLM([]string{"upstream", "set", "main", "--key=sk-on-the-command-line"}); err == nil || len(got) != before {
		t.Errorf("--key= on the command line: %v", err)
	}
	for _, bad := range [][]string{{"upstream", "add", "x"}, {"quota", "set", "team:x", "tokens/day=5"}, {"quota", "set", "default", "tokens/day=lots"},
		{"upstream", "add", "x", "http://y", "--no-key", "--price", "gpt-5=cheap"}, {"upstream", "set", "main", "--reasoning", "deepseek"}, {"upstream", "set", "main", "--reasoning", "m=low,turbo"}, {"usage", "--bogus"}, {"nonsense"}} {
		before := len(got)
		if err := cmdLLM(bad); err == nil {
			t.Errorf("bx llm %v: no error", bad)
		}
		if len(got) != before && bad[0] != "upstream" {
			t.Errorf("bx llm %v sent a request", bad)
		}
	}
	oai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"this workspace's LLM connector is not configured","type":"service_unavailable_error","param":null,"code":"xbin_llm_not_configured"}}`)
	}))
	defer oai.Close()
	t.Setenv("XBIN_URL", oai.URL)
	if err := cmdLLM([]string{"models"}); err == nil || !strings.HasPrefix(err.Error(), "this workspace's LLM connector is not configured (503") {
		t.Errorf("an OpenAI-shaped error: %v", err)
	}
	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	t.Setenv("XBIN_URL", old.URL)
	if err := cmdLLM(nil); err == nil || err.Error() != noLLMMsg {
		t.Errorf("an xbind without the connector: %v", err)
	}
}
