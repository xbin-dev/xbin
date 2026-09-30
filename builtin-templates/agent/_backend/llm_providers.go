// llm_providers.go — where the agent's model calls go (D111): the providers
// bound to its `llm` interface slot (multi, service "openai": llm-gw, or any
// tile that speaks the OpenAI API), or — while nothing is bound — the
// apps/llm-gw an instance made before the slot was granted by name.
//
// A model reference is either a bare model id ("gpt-5", "fake/fake-chat" —
// every config written before the slot, and the only form while one provider
// is bound) or "<provider>|<model id>" when a model is picked from one of
// several. A bare id goes to the first provider that lists it, else the first
// provider; requests always carry the bare id.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// llmProvider is one place models are served from.
type llmProvider struct {
	Path   string // the provider component ("apps/llm-gw"; "apps/llm-gw#inst" for an instance)
	URL    string // its OpenAI-compatible base, no trailing slash
	Legacy bool   // the unbound fallback: apps/llm-gw by name
	// Personal: a person's own provider, bound into their partition only
	// (iface_personal.go: offered in their own conversations only).
	Personal bool
}

const legacyGateway = "apps/llm-gw"

// llmClient reaches the providers (through the xbin gateway); tests point it
// at plain HTTP servers.
var llmClient = xbin.Client

// llmProviders reads the `llm` slot's bindings from the runner-injected env
// (docs/overview/11-interfaces.md; rebinding restarts the backend). Nothing
// bound ⇒ the legacy gateway, which works where the old grant still stands.
func llmProviders() []llmProvider {
	var eps []struct {
		Provider string `json:"provider"`
		Instance string `json:"instance"`
		URL      string `json:"url"`
		Personal bool   `json:"personal"`
	}
	if raw := os.Getenv("XBIN_IFACE_LLM"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &eps)
	}
	out := make([]llmProvider, 0, len(eps))
	for _, e := range eps {
		if e.URL == "" {
			continue
		}
		name := e.Provider
		if e.Instance != "" {
			name += "#" + e.Instance
		}
		out = append(out, llmProvider{Path: name, URL: strings.TrimRight(e.URL, "/"), Personal: e.Personal})
	}
	if len(out) == 0 {
		out = append(out, llmProvider{Path: legacyGateway, URL: "http://xbin/api/" + legacyGateway, Legacy: true})
	}
	return out
}

// splitModelRef takes "<provider>|<id>" apart ("" provider for a bare id).
func splitModelRef(ref string) (provider, id string) {
	if p, m, ok := strings.Cut(ref, "|"); ok {
		return p, m
	}
	return "", ref
}

// modelRef names a model on a provider: bare while it is the only one.
func modelRef(provider, id string, many bool) string {
	if !many || provider == "" {
		return id
	}
	return provider + "|" + id
}

// resolveModel finds where a model reference is served and the id to ask
// for. A provider that is no longer bound falls back to the first one, with
// the same id — a config outliving a rebinding keeps working when it can.
func resolveModel(ctx context.Context, ref string) (llmProvider, string) {
	provs := llmProvidersIn(ctx)
	want, id := splitModelRef(ref)
	if want != "" {
		for _, p := range provs {
			if p.Path == want {
				return p, id
			}
		}
		return provs[0], id
	}
	if len(provs) > 1 && id != "" {
		for _, m := range modelCatalog(ctx).Models {
			if m.ID == id {
				for _, p := range provs {
					if p.Path == m.Provider {
						return p, id
					}
				}
			}
		}
	}
	return provs[0], id
}

// catalogModel is one model a provider lists (GET /v1/models), with the
// reference the agent stores for it.
type catalogModel struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Ref      string `json:"ref"`
	OwnedBy  string `json:"owned_by,omitempty"`
	AliasOf  string `json:"alias_of,omitempty"`
	// ContextWindow is the model's input limit in tokens when its provider
	// says (0: unknown). It sizes the compaction budget (D133).
	ContextWindow int `json:"contextWindow,omitempty"`
}

// contextKeys are where OpenAI-compatible model lists put a context size:
// Anthropic (max_input_tokens), OpenRouter and Together (context_length,
// top_provider.context_length), Groq (context_window), vLLM (max_model_len),
// LM Studio (max_context_length), Gemini (inputTokenLimit) — and our own
// field, when GET /models output is read back. llm-gw passes the upstream's
// model objects through untouched.
var contextKeys = []string{"contextWindow", "max_input_tokens", "context_length", "context_window",
	"max_model_len", "max_context_length", "inputTokenLimit"}

// UnmarshalJSON reads a model object field by field: the list is the
// provider's, and a field of an unexpected type must cost only that field,
// never the whole list.
func (m *catalogModel) UnmarshalJSON(b []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	m.ID, m.Provider, m.Ref = str(raw["id"]), str(raw["provider"]), str(raw["ref"])
	m.OwnedBy, m.AliasOf = str(raw["owned_by"]), str(raw["alias_of"])
	m.ContextWindow = 0
	for _, k := range contextKeys {
		if n := toInt(raw[k]); n > 0 {
			m.ContextWindow = n
			return nil
		}
	}
	if tp, ok := raw["top_provider"].(map[string]any); ok {
		m.ContextWindow = max(toInt(tp["context_length"]), 0)
	}
	return nil
}

// contextWindow is a model reference's context size as its provider lists it
// (an alias: its target's), 0 when unknown. Read from the cached catalog.
func contextWindow(ctx context.Context, ref string) int {
	prov, id := splitModelRef(ref)
	c := modelCatalog(ctx)
	find := func(id string) *catalogModel {
		for i := range c.Models {
			m := &c.Models[i]
			if (m.ID == id || m.Ref == id) && (prov == "" || m.Provider == prov) {
				return m
			}
		}
		return nil
	}
	m := find(id)
	if m == nil {
		// llm-gw takes "<backend>/<model>" but lists bare ids when it has one backend
		if i := strings.LastIndex(id, "/"); i >= 0 {
			m = find(id[i+1:])
		}
	}
	if m == nil {
		return 0
	}
	if m.ContextWindow == 0 && m.AliasOf != "" {
		if t := find(m.AliasOf); t != nil {
			return t.ContextWindow
		}
	}
	return m.ContextWindow
}

// catalogProvider is one provider's answer: its models, or why none.
type catalogProvider struct {
	Path   string `json:"path"`
	OK     bool   `json:"ok"`
	Legacy bool   `json:"legacy,omitempty"`
	Error  string `json:"error,omitempty"`
}

type catalog struct {
	Models    []catalogModel
	Providers []catalogProvider
	at        time.Time
}

var (
	catalogMu    sync.Mutex
	catalogCache *catalog
	catalogTTL   = 60 * time.Second
)

// modelCatalog is the model list a call in ctx may use (iface_personal.go:
// a person's own providers only in their own conversations).
func modelCatalog(ctx context.Context) *catalog { return catalogIn(ctx, fullCatalog(ctx)) }

// fullCatalog merges every provider's model list, cached briefly (a picker
// and bare-id routing both read it). Providers are asked in parallel; one
// that fails is reported, not fatal.
func fullCatalog(ctx context.Context) *catalog {
	catalogMu.Lock()
	if c := catalogCache; c != nil && time.Since(c.at) < catalogTTL {
		catalogMu.Unlock()
		return c
	}
	catalogMu.Unlock()
	provs := llmProviders()
	many := len(provs) > 1
	type answer struct {
		models []catalogModel
		prov   catalogProvider
	}
	answers := make([]answer, len(provs))
	var wg sync.WaitGroup
	for i, p := range provs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := answer{prov: catalogProvider{Path: p.Path, Legacy: p.Legacy}}
			ids, err := listModels(ctx, p)
			if err != nil {
				a.prov.Error = err.Error()
			} else {
				a.prov.OK = true
				for _, m := range ids {
					m.Provider, m.Ref = p.Path, modelRef(p.Path, m.ID, many)
					a.models = append(a.models, m)
				}
			}
			answers[i] = a
		}()
	}
	wg.Wait()
	c := &catalog{at: time.Now()}
	for _, a := range answers {
		c.Models = append(c.Models, a.models...)
		c.Providers = append(c.Providers, a.prov)
	}
	catalogMu.Lock()
	catalogCache = c
	catalogMu.Unlock()
	return c
}

// listModels is one provider's GET /v1/models.
func listModels(ctx context.Context, p llmProvider) ([]catalogModel, error) {
	ctx, cancel := context.WithTimeout(ctx, modelLookupTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := llmClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{code: resp.StatusCode}
	}
	var out struct {
		Data []catalogModel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	sort.SliceStable(out.Data, func(i, j int) bool { return out.Data[i].ID < out.Data[j].ID })
	return out.Data, nil
}

type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string {
	switch e.code {
	case http.StatusForbidden, http.StatusUnauthorized:
		return "not allowed — bind the agent's llm interface to this provider"
	case http.StatusNotFound:
		return "not found — is the provider installed and bound?"
	}
	return "HTTP " + http.StatusText(e.code)
}
