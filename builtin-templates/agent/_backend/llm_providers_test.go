package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeProvider is an OpenAI-compatible tile: it lists models and records the
// model each chat call asked for.
type fakeProvider struct {
	srv    *httptest.Server
	mu     sync.Mutex
	asked  []string
	models []string
}

func newFakeProvider(t *testing.T, models ...string) *fakeProvider {
	t.Helper()
	p := &fakeProvider{models: models}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/models":
			var data []map[string]string
			for _, m := range p.models {
				data = append(data, map[string]string{"id": m})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		case r.URL.Path == "/v1/chat/completions":
			var body struct{ Model string }
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			p.mu.Lock()
			p.asked = append(p.asked, body.Model)
			p.mu.Unlock()
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hi from `+body.Model+`"},"finish_reason":"stop"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// bindLLM injects the llm slot as the runner would, for these providers.
func bindLLM(t *testing.T, provs map[string]*fakeProvider, order ...string) {
	t.Helper()
	var eps []map[string]string
	for _, name := range order {
		eps = append(eps, map[string]string{"provider": name, "url": provs[name].srv.URL, "service": "openai"})
	}
	raw, _ := json.Marshal(eps)
	t.Setenv("XBIN_IFACE_LLM", string(raw))
	old := llmClient
	llmClient = func() *http.Client { return http.DefaultClient }
	catalogMu.Lock()
	catalogCache = nil
	catalogMu.Unlock()
	t.Cleanup(func() {
		llmClient = old
		catalogMu.Lock()
		catalogCache = nil
		catalogMu.Unlock()
	})
}

// Nothing bound: the legacy apps/llm-gw an older instance was granted.
func TestLLMProvidersLegacy(t *testing.T) {
	t.Setenv("XBIN_IFACE_LLM", "")
	p := llmProviders()
	if len(p) != 1 || !p[0].Legacy || p[0].URL != "http://xbin/api/apps/llm-gw" {
		t.Fatalf("unbound: %+v", p)
	}
	t.Setenv("XBIN_IFACE_LLM", `[{"provider":"apps/llm-gw","instance":"eu","url":"http://xbin/api/apps/llm-gw#eu/"}]`)
	if p := llmProviders(); len(p) != 1 || p[0].Path != "apps/llm-gw#eu" || p[0].Legacy || strings.HasSuffix(p[0].URL, "/") {
		t.Fatalf("an instance binding: %+v", p)
	}
}

// Several providers: the catalog merges them with qualified refs; a
// qualified ref picks its provider, a bare id the one that lists it, an
// unknown one the first; the request carries the bare id.
func TestLLMRoutesByModel(t *testing.T) {
	a := newFakeProvider(t, "shared-model", "only-a")
	b := newFakeProvider(t, "shared-model", "only-b")
	bindLLM(t, map[string]*fakeProvider{"apps/a": a, "apps/b": b}, "apps/a", "apps/b")

	cat := modelCatalog(context.Background())
	var refs []string
	for _, m := range cat.Models {
		refs = append(refs, m.Ref)
	}
	if got, want := strings.Join(refs, ","), "apps/a|only-a,apps/a|shared-model,apps/b|only-b,apps/b|shared-model"; got != want {
		t.Fatalf("catalog refs: %s, want %s", got, want)
	}

	g := &gatewayLLM{client: http.DefaultClient}
	for _, c := range []struct{ ref, who, id string }{
		{"apps/b|shared-model", "b", "shared-model"},
		{"apps/a|shared-model", "a", "shared-model"},
		{"only-b", "b", "only-b"},
		{"unknown", "a", "unknown"},
		{"apps/gone|x", "a", "x"}, // a provider no longer bound: the first, same id
	} {
		rep, err := g.Chat(context.Background(), LLMRequest{Model: c.ref, Msgs: []wireMsg{{Role: "user", Content: "hi"}}}, nil)
		if err != nil {
			t.Fatalf("%s: %v", c.ref, err)
		}
		p := map[string]*fakeProvider{"a": a, "b": b}[c.who]
		p.mu.Lock()
		last := p.asked[len(p.asked)-1]
		p.mu.Unlock()
		if last != c.id || rep.Model != c.id {
			t.Fatalf("%s: provider %s was asked %q (reply model %q), want %q", c.ref, c.who, last, rep.Model, c.id)
		}
	}
}

// GET /models is anyone's (the composer's picker), with every provider and
// why one failed.
func TestModelsForEveryone(t *testing.T) {
	a := newFakeProvider(t, "m1")
	_, mux := accessFixture(t)
	bindLLM(t, map[string]*fakeProvider{"apps/a": a}, "apps/a")
	w := callAs(t, mux, asAlice, "GET", "/models", nil)
	var out struct {
		Data      []catalogModel    `json:"data"`
		Providers []catalogProvider `json:"providers"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || len(out.Data) != 1 || out.Data[0].Ref != "m1" || out.Data[0].Provider != "apps/a" {
		t.Fatalf("one provider, a bare ref: %d %s", w.Code, w.Body)
	}
	a.srv.Close()
	catalogMu.Lock()
	catalogCache = nil
	catalogMu.Unlock()
	w = callAs(t, mux, asAlice, "GET", "/models", nil)
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || len(out.Data) != 0 || len(out.Providers) != 1 || out.Providers[0].OK || !strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("a provider down: %d %s", w.Code, w.Body)
	}
}

// A pick is per conversation: set by the ask, switched by PATCH (anyone who
// may talk in it), and the next turn asks for it.
func TestPickPerConversation(t *testing.T) {
	ag, mux := accessFixture(t)
	w := callAs(t, mux, asAlice, "POST", "/ask", map[string]string{"text": "hello", "model": "apps/b|fast"})
	if w.Code != 200 {
		t.Fatalf("ask: %d %s", w.Code, w.Body)
	}
	var run struct{ ID int64 }
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	waitQuiet(t, ag)
	if cfg, _ := ag.db.runConfig(run.ID); cfg.Pick != "apps/b|fast" {
		t.Fatalf("the ask's pick: %q", cfg.Pick)
	}
	f := fakeOf(ag)
	lastModel := func() string { // of the conversation's own turns (titles keep their tier)
		f.mu.Lock()
		defer f.mu.Unlock()
		for i := len(f.calls) - 1; i >= 0; i-- {
			if f.calls[i].Purpose == "turn" {
				return f.calls[i].Model
			}
		}
		return ""
	}
	if m := lastModel(); m != "apps/b|fast" {
		t.Fatalf("the first turn asked for %q", m)
	}
	if got := callAs(t, mux, asBob, "PATCH", fmt.Sprintf("/runs/%d", run.ID), map[string]string{"model": "x"}).Code; got != 404 {
		t.Fatalf("bob switches alice's private chat's model: %d", got)
	}
	if got := callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", run.ID), map[string]string{"model": "apps/a|big"}).Code; got != 200 {
		t.Fatalf("switch: %d", got)
	}
	send(t, ag, run.ID, "and now?")
	waitQuiet(t, ag)
	if m := lastModel(); m != "apps/a|big" {
		t.Fatalf("the next turn asked for %q", m)
	}
	if got := callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", run.ID), map[string]string{"model": "bad\x01"}).Code; got != 400 {
		t.Fatalf("a control character in a model: %d", got)
	}
	// "" goes back to the agent's default
	callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", run.ID), map[string]string{"model": ""})
	if cfg, _ := ag.db.runConfig(run.ID); cfg.Pick != "" {
		t.Fatalf("cleared: %q", cfg.Pick)
	}
}
