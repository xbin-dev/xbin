// llm-gw backend: an OpenAI-compatible LLM gateway multiplexing MULTIPLE
// upstream backends. Configuration (named backends with base URLs, model
// aliases) lives in the scope's kv resource; each backend's API key lives in
// this component's vault (key "api-token-<name>"). GET /v1/models aggregates
// every backend (ids namespaced "<backend>/<model>" when more than one);
// /v1/* routes by the model's backend prefix. Per-backend usage counters
// (requests, tokens in/out, active) are kept for the tile UI. /config and
// /stats are for this tile's own settings panel only.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"

	xbin "github.com/magik6k/xbin/sdk"
)

const defaultBaseURL = "https://api.openai.com"

type backend struct {
	BaseURL string `json:"baseURL"`
}

type gwConfig struct {
	// Legacy single-backend field (pre-multi); migrated to Backends["default"].
	BaseURL  string             `json:"baseURL,omitempty"`
	Backends map[string]backend `json:"backends"`
	Aliases  map[string]string  `json:"aliases"` // alias -> "<backend>/<model>" (or bare model)
}

type stats struct {
	Reqs   int64 `json:"reqs"`
	TokIn  int64 `json:"tokIn"`
	TokOut int64 `json:"tokOut"`
}

var (
	kv    = xbin.KV(xbin.Resource("state"))
	cfgMu sync.Mutex

	statsMu sync.Mutex
	statsBy map[string]*stats // backend -> persisted counters
	active  = map[string]int{}

	// No overall timeout: chat completions can stream for a long time.
	// Bound by the inbound request's context (client disconnect cancels it).
	upstream = &http.Client{}
)

func loadConfig() gwConfig {
	var c gwConfig
	_ = kv.GetJSON("config", &c)
	if c.Backends == nil {
		c.Backends = map[string]backend{}
	}
	// Migrate the legacy single-backend config: the old baseURL (or the
	// implicit OpenAI default) becomes the "default" backend.
	if len(c.Backends) == 0 {
		u := c.BaseURL
		if u == "" {
			u = defaultBaseURL
		}
		c.Backends["default"] = backend{BaseURL: u}
	}
	if c.Aliases == nil {
		c.Aliases = map[string]string{}
	}
	return c
}

func saveConfig(c gwConfig) error {
	c.BaseURL = "" // legacy field never written back
	return kv.PutJSON("config", c)
}

// tokenFor returns a backend's API token: "api-token-<name>", falling back to
// the legacy "api-token" key for the migrated "default" backend.
func tokenFor(name string) string {
	tok, _ := xbin.Secret("api-token-" + name)
	if tok == "" && name == "default" {
		tok, _ = xbin.Secret("api-token")
	}
	return tok
}

func loadStats() {
	statsMu.Lock()
	defer statsMu.Unlock()
	if statsBy == nil {
		statsBy = map[string]*stats{}
		_ = kv.GetJSON("stats", &statsBy)
	}
}

func bumpStats(name string, tokIn, tokOut int64) {
	loadStats()
	statsMu.Lock()
	s := statsBy[name]
	if s == nil {
		s = &stats{}
		statsBy[name] = s
	}
	s.Reqs++
	s.TokIn += tokIn
	s.TokOut += tokOut
	snapshot := statsBy
	statsMu.Unlock()
	_ = kv.PutJSON("stats", snapshot)
}

func setActive(name string, d int) {
	statsMu.Lock()
	active[name] += d
	if active[name] < 0 {
		active[name] = 0
	}
	statsMu.Unlock()
}

// upstreamURL joins a configured base URL with a /v1/... request path.
// Base URLs are commonly given either bare ("https://api.openai.com") or
// already including the version prefix ("https://openrouter.ai/api/v1", the
// convention most OpenAI SDKs default to) — avoid doubling /v1 in the
// latter case.
func upstreamURL(baseURL, path, rawQuery string) string {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	target := base + path
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	return target
}

// resolveModel maps a requested model id to (backend name, upstream model id):
// aliases first, then a "<backend>/" prefix, else the sole/default backend.
// The bare form keeps single-backend setups and old callers working.
func resolveModel(c gwConfig, model string) (string, string) {
	if real, ok := c.Aliases[model]; ok {
		model = real
	}
	if b, rest, ok := strings.Cut(model, "/"); ok {
		if _, exists := c.Backends[b]; exists {
			return b, rest
		}
	}
	return defaultBackend(c), model
}

func defaultBackend(c gwConfig) string {
	if _, ok := c.Backends["default"]; ok {
		return "default"
	}
	if names := backendNames(c); len(names) > 0 {
		return names[0]
	}
	return "default"
}

func backendNames(c gwConfig) []string {
	names := make([]string, 0, len(c.Backends))
	for n := range c.Backends {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func main() {
	mux := http.NewServeMux()

	// Settings + stats — self (this tile's own frontend, always admin of
	// itself) or the owner only. Not part of the reader/writer surface.
	mux.Handle("GET /config", xbin.RoleFunc("admin", handleGetConfig))
	mux.Handle("PUT /config", xbin.RoleFunc("admin", handlePutConfig))
	mux.Handle("PUT /config/backend", xbin.RoleFunc("admin", handlePutBackend))
	mux.Handle("DELETE /config/backend/{name}", xbin.RoleFunc("admin", handleDelBackend))
	mux.Handle("GET /stats", xbin.RoleFunc("admin", handleStats))

	// OpenAI-compatible surface.
	mux.Handle("GET /v1/models", xbin.RoleFunc("reader", handleModels))
	mux.Handle("/v1/{rest...}", xbin.RoleFunc("writer", handleProxy))

	xbin.Serve(mux)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ---- settings ----

func handleGetConfig(w http.ResponseWriter, r *http.Request) {
	c := loadConfig()
	type beOut struct {
		Name     string `json:"name"`
		BaseURL  string `json:"baseURL"`
		HasToken bool   `json:"hasToken"`
	}
	out := make([]beOut, 0, len(c.Backends))
	for _, n := range backendNames(c) {
		out = append(out, beOut{Name: n, BaseURL: c.Backends[n].BaseURL, HasToken: tokenFor(n) != ""})
	}
	writeJSON(w, http.StatusOK, map[string]any{"backends": out, "aliases": c.Aliases})
}

var backendNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// handlePutBackend adds or updates one named backend {name, baseURL}. The
// token is set by the frontend directly into this tile's vault
// ("api-token-<name>") — it never passes through kv.
func handlePutBackend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		BaseURL string `json:"baseURL"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "need JSON body: {name, baseURL}")
		return
	}
	name := strings.TrimSpace(body.Name)
	u := strings.TrimRight(strings.TrimSpace(body.BaseURL), "/")
	if !backendNameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad backend name (lowercase letters, digits, . _ -)")
		return
	}
	if u == "" {
		writeErr(w, http.StatusBadRequest, "baseURL required")
		return
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	c := loadConfig()
	c.Backends[name] = backend{BaseURL: u}
	if err := saveConfig(c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func handleDelBackend(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfgMu.Lock()
	defer cfgMu.Unlock()
	c := loadConfig()
	if len(c.Backends) == 1 {
		writeErr(w, http.StatusBadRequest, "can't remove the last backend")
		return
	}
	delete(c.Backends, name)
	if err := saveConfig(c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Aliases map[string]string `json:"aliases"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "need JSON body: {aliases?}")
		return
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	c := loadConfig()
	if body.Aliases != nil {
		aliases := make(map[string]string, len(body.Aliases))
		for alias, target := range body.Aliases {
			alias, target = strings.TrimSpace(alias), strings.TrimSpace(target)
			if alias == "" || target == "" {
				continue
			}
			aliases[alias] = target
		}
		c.Aliases = aliases
	}
	if err := saveConfig(c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"aliases": c.Aliases})
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	loadStats()
	c := loadConfig()
	statsMu.Lock()
	out := map[string]map[string]int64{}
	for _, n := range backendNames(c) {
		s := statsBy[n]
		if s == nil {
			s = &stats{}
		}
		out[n] = map[string]int64{
			"reqs": s.Reqs, "tokIn": s.TokIn, "tokOut": s.TokOut,
			"active": int64(active[n]),
		}
	}
	statsMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"backends": out})
}

// ---- OpenAI-compatible surface ----

// handleModels aggregates every backend's model list. With more than one
// backend, ids are namespaced "<backend>/<model>" so routing is unambiguous;
// a single backend keeps bare ids (and old callers keep working).
func handleModels(w http.ResponseWriter, r *http.Request) {
	c := loadConfig()
	names := backendNames(c)
	prefix := len(names) > 1

	type result struct {
		name string
		data []map[string]any
		err  string
	}
	results := make(chan result, len(names))
	for _, name := range names {
		go func(name string) {
			tok := tokenFor(name)
			if tok == "" {
				results <- result{name: name, err: "no API token configured"}
				return
			}
			url := upstreamURL(c.Backends[name].BaseURL, "/v1/models", "")
			req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
			if err != nil {
				results <- result{name: name, err: err.Error()}
				return
			}
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := upstream.Do(req)
			if err != nil {
				results <- result{name: name, err: fmt.Sprintf("GET %s: %s", url, err.Error())}
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
				results <- result{name: name, err: fmt.Sprintf("GET %s: %s: %s", url, resp.Status, strings.TrimSpace(string(body)))}
				return
			}
			var up struct {
				Data []map[string]any `json:"data"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&up); err != nil {
				results <- result{name: name, err: fmt.Sprintf("GET %s: bad response: %s", url, err.Error())}
				return
			}
			results <- result{name: name, data: up.Data}
		}(name)
	}

	seen := map[string]bool{}
	out := make([]map[string]any, 0, 64)
	for alias, target := range c.Aliases {
		out = append(out, map[string]any{"id": alias, "object": "model", "owned_by": "alias", "alias_of": target})
		seen[alias] = true
	}
	errs := map[string]string{}
	for range names {
		res := <-results
		if res.err != "" {
			errs[res.name] = res.err
			continue
		}
		for _, m := range res.data {
			id, _ := m["id"].(string)
			if id == "" {
				continue
			}
			if prefix {
				id = res.name + "/" + id
				m["id"] = id
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			m["backend"] = res.name
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprint(out[i]["id"]) < fmt.Sprint(out[j]["id"])
	})
	resp := map[string]any{"object": "list", "data": out}
	if len(errs) > 0 {
		var parts []string
		for _, n := range backendNames(c) {
			if e, ok := errs[n]; ok {
				parts = append(parts, n+": "+e)
			}
		}
		resp["error"] = strings.Join(parts, " · ")
	}
	writeJSON(w, http.StatusOK, resp)
}

var usageRe = regexp.MustCompile(`"prompt_tokens"\s*:\s*(\d+)[\s\S]*?"completion_tokens"\s*:\s*(\d+)`)

func handleProxy(w http.ResponseWriter, r *http.Request) {
	c := loadConfig()

	ct := r.Header.Get("Content-Type")
	var body io.Reader = r.Body
	defer r.Body.Close()

	// Resolve which backend serves this request from the JSON "model" field
	// (alias → "<backend>/<model>" prefix → default), rewriting it to the
	// upstream's bare id on the way out.
	beName := defaultBackend(c)
	if strings.Contains(ct, "application/json") {
		raw, rerr := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if rerr != nil {
			writeErr(w, http.StatusBadRequest, "failed reading request body")
			return
		}
		if len(raw) > 0 {
			var payload map[string]any
			if json.Unmarshal(raw, &payload) == nil {
				if model, _ := payload["model"].(string); model != "" {
					b, real := resolveModel(c, model)
					beName = b
					if real != model {
						payload["model"] = real
						if remarshaled, merr := json.Marshal(payload); merr == nil {
							raw = remarshaled
						}
					}
				}
			}
		}
		body = bytes.NewReader(raw)
	}

	be, ok := c.Backends[beName]
	if !ok {
		writeErr(w, http.StatusBadGateway, "no such backend: "+beName)
		return
	}
	tok := tokenFor(beName)
	if tok == "" {
		writeErr(w, http.StatusBadGateway, "no API token configured for backend "+beName+" — set one in the llm-gw tile")
		return
	}

	target := upstreamURL(be.BaseURL, r.URL.Path, r.URL.RawQuery)

	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ct != "" {
		upReq.Header.Set("Content-Type", ct)
	}
	if acc := r.Header.Get("Accept"); acc != "" {
		upReq.Header.Set("Accept", acc)
	}
	upReq.Header.Set("Authorization", "Bearer "+tok)

	setActive(beName, +1)
	defer setActive(beName, -1)

	resp, err := upstream.Do(upReq)
	if err != nil {
		bumpStats(beName, 0, 0)
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("%s %s: %s", r.Method, target, err.Error()))
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		if k == "Content-Length" || k == "Connection" {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Relay, keeping a bounded tail so token usage can be scraped afterwards —
	// works for plain JSON responses and for SSE streams whose final chunk
	// carries a usage object (stream_options.include_usage).
	flusher, _ := w.(http.Flusher)
	var tail []byte
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
			tail = append(tail, buf[:n]...)
			if len(tail) > 8<<10 {
				tail = tail[len(tail)-(8<<10):]
			}
		}
		if rerr != nil {
			break
		}
	}
	var tokIn, tokOut int64
	if m := usageRe.FindSubmatch(tail); m != nil {
		fmt.Sscan(string(m[1]), &tokIn)
		fmt.Sscan(string(m[2]), &tokOut)
	}
	bumpStats(beName, tokIn, tokOut)
}
