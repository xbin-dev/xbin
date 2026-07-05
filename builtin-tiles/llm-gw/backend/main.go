// llm-gw backend: an OpenAI-compatible LLM gateway. Configuration (base URL,
// model aliases) lives in the scope's kv resource; the upstream API key
// lives in this component's vault. GET /v1/models and everything else under
// /v1/ are proxied to the configured base URL, resolving aliased model names
// on the way out. /config is for this tile's own settings panel only.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"

	xbin "github.com/magik6k/xbin/sdk"
)

const defaultBaseURL = "https://api.openai.com"

type gwConfig struct {
	BaseURL string            `json:"baseURL"`
	Aliases map[string]string `json:"aliases"` // alias -> real upstream model id
}

var (
	kv    = xbin.KV(xbin.Resource("state"))
	cfgMu sync.Mutex

	// No overall timeout: chat completions can stream for a long time.
	// Bound by the inbound request's context (client disconnect cancels it).
	upstream = &http.Client{}
)

func loadConfig() gwConfig {
	var c gwConfig
	_ = kv.GetJSON("config", &c)
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	if c.Aliases == nil {
		c.Aliases = map[string]string{}
	}
	return c
}

func saveConfig(c gwConfig) error {
	return kv.PutJSON("config", c)
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

func main() {
	mux := http.NewServeMux()

	// Settings — self (this tile's own frontend, always admin of itself)
	// or the owner only. Not part of the reader/writer surface other tiles use.
	mux.Handle("GET /config", xbin.RoleFunc("admin", handleGetConfig))
	mux.Handle("PUT /config", xbin.RoleFunc("admin", handlePutConfig))

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
	tok, _ := xbin.Secret("api-token")
	writeJSON(w, http.StatusOK, map[string]any{
		"baseURL":  c.BaseURL,
		"hasToken": tok != "",
		"aliases":  c.Aliases,
	})
}

func handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BaseURL *string           `json:"baseURL"`
		Aliases map[string]string `json:"aliases"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "need JSON body: {baseURL?, aliases?}")
		return
	}

	cfgMu.Lock()
	defer cfgMu.Unlock()
	c := loadConfig()
	if body.BaseURL != nil {
		u := strings.TrimRight(strings.TrimSpace(*body.BaseURL), "/")
		if u == "" {
			u = defaultBaseURL
		}
		c.BaseURL = u
	}
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
	writeJSON(w, http.StatusOK, map[string]any{"baseURL": c.BaseURL, "aliases": c.Aliases})
}

// ---- OpenAI-compatible surface ----

func handleModels(w http.ResponseWriter, r *http.Request) {
	c := loadConfig()
	tok, _ := xbin.Secret("api-token")

	var up struct {
		Data []map[string]any `json:"data"`
	}
	var upstreamErr string
	if tok == "" {
		upstreamErr = "no upstream API token configured"
	} else {
		url := upstreamURL(c.BaseURL, "/v1/models", "")
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
		if err != nil {
			upstreamErr = err.Error()
		} else {
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := upstream.Do(req)
			if err != nil {
				upstreamErr = fmt.Sprintf("GET %s: %s", url, err.Error())
			} else {
				func() {
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
						upstreamErr = fmt.Sprintf("GET %s: %s: %s", url, resp.Status, strings.TrimSpace(string(body)))
						return
					}
					if err := json.NewDecoder(resp.Body).Decode(&up); err != nil {
						upstreamErr = fmt.Sprintf("GET %s: bad response: %s", url, err.Error())
					}
				}()
			}
		}
	}

	seen := make(map[string]bool, len(c.Aliases)+len(up.Data))
	out := make([]map[string]any, 0, len(up.Data)+len(c.Aliases))
	for alias, target := range c.Aliases {
		out = append(out, map[string]any{"id": alias, "object": "model", "owned_by": "alias", "alias_of": target})
		seen[alias] = true
	}
	for _, m := range up.Data {
		id, _ := m["id"].(string)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprint(out[i]["id"]) < fmt.Sprint(out[j]["id"])
	})
	resp := map[string]any{"object": "list", "data": out}
	if upstreamErr != "" {
		resp["error"] = upstreamErr
	}
	writeJSON(w, http.StatusOK, resp)
}

func handleProxy(w http.ResponseWriter, r *http.Request) {
	c := loadConfig()
	tok, err := xbin.Secret("api-token")
	if err != nil || tok == "" {
		writeErr(w, http.StatusBadGateway, "no upstream API token configured — set one in the llm-gw tile")
		return
	}

	ct := r.Header.Get("Content-Type")
	var body io.Reader = r.Body
	defer r.Body.Close()

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
					if real, aliased := c.Aliases[model]; aliased {
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

	target := upstreamURL(c.BaseURL, r.URL.Path, r.URL.RawQuery)

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

	resp, err := upstream.Do(upReq)
	if err != nil {
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

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}
