// llm.go — the LLM connection. Like the chat tile, the agent talks to the
// llm-gw component's OpenAI-compatible surface (uses: apps/llm-gw:writer); the
// upstream API key stays in llm-gw's vault. Non-streaming: we journal the whole
// response as one durable step, and the tile watches the journal for progress.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	xbin "github.com/magik6k/xbin/sdk"
)

// asString renders a wireMsg content value (string, raw JSON parts, or nil) as
// text for storage/summarization.
func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case json.RawMessage:
		return string(s)
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// ModelTiers is the per-job model set. Any empty tier is resolved at runtime
// from llm-gw's workspace "preferred" model for the mapped use-type (general←
// agent, code←coding, memory←summarizing, vlm←vlm), so a workspace sets its
// models once in llm-gw and every agent inherits them. See plans/agent-v2.md.
type ModelTiers struct {
	General string `json:"general,omitempty"` // the main loop
	Code    string `json:"code,omitempty"`    // code-heavy work / code subagents
	Memory  string `json:"memory,omitempty"`  // compaction + memory management
	VLM     string `json:"vlm,omitempty"`     // vision fallback when general lacks it
}

// Config is an agent's knobs — a default lives in settings, and each run
// snapshots one at creation (so changing the default doesn't disturb live runs).
type Config struct {
	Model       string      `json:"model"`       // legacy/general fallback (empty ⇒ llm-gw preferred)
	Models      ModelTiers  `json:"models"`      // per-tier models
	System      string      `json:"system"`      // base system prompt
	TokenBudget int         `json:"tokenBudget"` // context assembly budget
	MaxIters    int         `json:"maxIters"`    // steps per drive before yielding
	ToolTimeout int         `json:"toolTimeout"` // seconds per tool call (0 ⇒ default)
	Subagents   bool        `json:"subagents"`   // expose spawn_subagent
	Approve     bool        `json:"approve"`     // require approval before side-effecting tools
	MCP         []MCPServer `json:"mcp"`         // legacy static MCP servers (now bound via the mcp interface)
	// Features toggles optional capabilities — a "Features" menu in the tile.
	// Absent or true = on; set a key false to turn it off. Known keys are in
	// featureKeys; unlisted keys default on so older configs get everything.
	Features map[string]bool `json:"features"`
}

// featureKeys are the toggleable capabilities shown in the tile's Features menu.
var featureKeys = []string{"recall", "skills", "streaming", "vision", "parallelTools", "watcher"}

// feature reports whether an optional capability is enabled (default on).
func (c Config) feature(name string) bool {
	if c.Features == nil {
		return true
	}
	v, ok := c.Features[name]
	return !ok || v
}

func defaultConfig() Config {
	return Config{
		Model:       "", // resolved from llm-gw preferred (use-type "agent")
		System:      "You are a helpful autonomous agent running inside xbin. Work toward the user's goal using the available tools. Use memory_set to remember durable facts. Call finish when the goal is done, ask_user when you need input, and yield when you should wait before continuing.",
		TokenBudget: 12000,
		MaxIters:    12,
		ToolTimeout: 120,
		Subagents:   true,
		Approve:     false,
		MCP:         nil,
	}
}

func parseConfig(raw string) Config {
	c := defaultConfig()
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	if c.TokenBudget <= 0 {
		c.TokenBudget = 12000
	}
	if c.MaxIters <= 0 {
		c.MaxIters = 12
	}
	if c.ToolTimeout <= 0 {
		c.ToolTimeout = 120
	}
	return c
}

// --- OpenAI-compatible wire types ---------------------------------------

type wireMsg struct {
	Role string `json:"role"`
	// Content is usually a string, but a multimodal user message carries the
	// OpenAI content-parts array (text + image_url); contentValue keeps a stored
	// JSON-array string as raw JSON so it marshals as an array, not a quoted
	// string (plans/agent-v2.md §multimodal).
	Content    any        `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
}

// contentValue passes a content-parts array through as raw JSON; plain text
// stays a string.
func contentValue(s string) any {
	if strings.HasPrefix(strings.TrimSpace(s), "[") {
		return json.RawMessage(s)
	}
	return s
}

// visionModelFor picks the main-loop model: the general tier, but the vlm tier
// when a message carries image content and the general model isn't vision-
// capable. Gated by the "vision" feature. Explicitly setting the vlm tier
// overrides the name heuristic.
func visionModelFor(ctx context.Context, cfg Config, msgs []wireMsg) string {
	general := modelFor(ctx, cfg, "general")
	if !cfg.feature("vision") || modelHasVision(general) || !hasVisionContent(msgs) {
		return general
	}
	return modelFor(ctx, cfg, "vlm")
}

func hasVisionContent(msgs []wireMsg) bool {
	for _, m := range msgs {
		if raw, ok := m.Content.(json.RawMessage); ok && strings.Contains(string(raw), `"image_url"`) {
			return true
		}
	}
	return false
}

// modelHasVision is a name heuristic — the OpenAI-compatible models list doesn't
// expose capability. When it guesses wrong, set the vlm tier explicitly.
func modelHasVision(model string) bool {
	m := strings.ToLower(model)
	for _, s := range []string{"4o", "4.1", "4.5", "vision", "gpt-5", "claude-3", "claude-4", "sonnet", "opus", "gemini", "llama-3.2", "llama-4", "pixtral", "-vl"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON string
	} `json:"function"`
}

type toolSpec struct {
	Type     string  `json:"type"` // "function"
	Function funcDef `json:"function"`
}

type funcDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON Schema
}

type chatReq struct {
	Model    string     `json:"model"`
	Messages []wireMsg  `json:"messages"`
	Tools    []toolSpec `json:"tools,omitempty"`
}

type usageBlock struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatResp struct {
	Choices []struct {
		Message      wireMsg `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage usageBlock      `json:"usage"`
	Error json.RawMessage `json:"error"`
}

type streamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type streamReq struct {
	Model         string      `json:"model"`
	Messages      []wireMsg   `json:"messages"`
	Tools         []toolSpec  `json:"tools,omitempty"`
	Stream        bool        `json:"stream"`
	StreamOptions *streamOpts `json:"stream_options,omitempty"`
}

// --- model tier resolution (plans/agent-v2.md) --------------------------

var (
	prefMu    sync.Mutex
	prefCache = map[string]prefEntry{}
)

type prefEntry struct {
	model string
	at    time.Time
}

// preferredModel asks llm-gw for the workspace's preferred model for a use-type
// (agent/coding/summarizing/vlm/…). Cached briefly so it's not a round-trip per
// step; a down gateway returns "" and the caller falls back.
func preferredModel(ctx context.Context, use string) string {
	prefMu.Lock()
	if e, ok := prefCache[use]; ok && time.Since(e.at) < 60*time.Second {
		m := e.model
		prefMu.Unlock()
		return m
	}
	prefMu.Unlock()
	u := "http://xbin/api/apps/" + gwPath() + "/preferred?use=" + url.QueryEscape(use)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var out struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	prefMu.Lock()
	prefCache[use] = prefEntry{model: out.Model, at: time.Now()}
	prefMu.Unlock()
	return out.Model
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// modelFor resolves the model for a tier: explicit config → the general model →
// llm-gw's preferred model for the mapped use-type → a safe default so a fresh
// workspace still runs.
func modelFor(ctx context.Context, cfg Config, tier string) string {
	var explicit, use string
	switch tier {
	case "code":
		explicit, use = firstNonEmpty(cfg.Models.Code, cfg.Models.General, cfg.Model), "coding"
	case "memory":
		explicit, use = firstNonEmpty(cfg.Models.Memory, cfg.Models.General, cfg.Model), "summarizing"
	case "vlm":
		explicit, use = cfg.Models.VLM, "vlm"
	default: // general
		explicit, use = firstNonEmpty(cfg.Models.General, cfg.Model), "agent"
	}
	if explicit != "" {
		return explicit
	}
	if m := preferredModel(ctx, use); m != "" {
		return m
	}
	return firstNonEmpty(cfg.Model, "gpt-4o-mini")
}

// --- the LLM call -------------------------------------------------------

const llmRetries = 3

func retryableLLM(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// llmBackoff waits before a retry (exp + jitter, capped); false if ctx canceled.
func llmBackoff(ctx context.Context, attempt int) bool {
	base := 500 * time.Millisecond * time.Duration(int64(1)<<(attempt-1))
	d := base + time.Duration(rand.Int63n(int64(base/2)+1))
	if d > 20*time.Second {
		d = 20 * time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// callLLM runs one chat completion through llm-gw, retrying transient failures
// (transport errors — e.g. llm-gw reloading after an idle unload — and 429/5xx)
// with backoff. Fatal statuses (400/401/404) return immediately. Returns the
// assistant message and the usage block.
func callLLM(ctx context.Context, model string, msgs []wireMsg, tools []toolSpec) (wireMsg, chatResp, error) {
	body, err := json.Marshal(chatReq{Model: model, Messages: msgs, Tools: tools})
	if err != nil {
		return wireMsg{}, chatResp{}, err
	}
	endpoint := "http://xbin/api/apps/" + gwPath() + "/v1/chat/completions"
	var lastErr error
	for attempt := 0; attempt <= llmRetries; attempt++ {
		if attempt > 0 && !llmBackoff(ctx, attempt) {
			break // ctx canceled during backoff
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return wireMsg{}, chatResp{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := xbin.Client().Do(req)
		if err != nil {
			lastErr = fmt.Errorf("llm-gw call: %w", err)
			continue // transport error ⇒ retry
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("llm-gw %s: %s", resp.Status, strings.TrimSpace(string(raw)))
			if retryableLLM(resp.StatusCode) {
				continue
			}
			return wireMsg{}, chatResp{}, lastErr // fatal
		}
		var cr chatResp
		if err := json.Unmarshal(raw, &cr); err != nil {
			return wireMsg{}, chatResp{}, fmt.Errorf("llm-gw bad response: %w", err)
		}
		if len(cr.Error) > 0 && string(cr.Error) != "null" {
			return wireMsg{}, cr, fmt.Errorf("llm-gw upstream error: %s", string(cr.Error))
		}
		if len(cr.Choices) == 0 {
			return wireMsg{}, cr, fmt.Errorf("llm-gw returned no choices")
		}
		return cr.Choices[0].Message, cr, nil
	}
	if lastErr == nil {
		lastErr = ctx.Err()
	}
	return wireMsg{}, chatResp{}, lastErr
}

// callLLMStream is callLLM over SSE (stream:true + usage): it calls onDelta with
// the accumulated assistant text as tokens arrive (for a live tile draft), and
// returns the same final message + usage. Retries only before any token flows.
func callLLMStream(ctx context.Context, model string, msgs []wireMsg, tools []toolSpec, onDelta func(string)) (wireMsg, chatResp, error) {
	body, err := json.Marshal(streamReq{Model: model, Messages: msgs, Tools: tools, Stream: true, StreamOptions: &streamOpts{IncludeUsage: true}})
	if err != nil {
		return wireMsg{}, chatResp{}, err
	}
	endpoint := "http://xbin/api/apps/" + gwPath() + "/v1/chat/completions"
	var lastErr error
	for attempt := 0; attempt <= llmRetries; attempt++ {
		if attempt > 0 && !llmBackoff(ctx, attempt) {
			break
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return wireMsg{}, chatResp{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		resp, err := xbin.Client().Do(req)
		if err != nil {
			lastErr = fmt.Errorf("llm-gw call: %w", err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
			resp.Body.Close()
			lastErr = fmt.Errorf("llm-gw %s: %s", resp.Status, strings.TrimSpace(string(raw)))
			if retryableLLM(resp.StatusCode) {
				continue
			}
			return wireMsg{}, chatResp{}, lastErr
		}
		msg, usage, perr := parseSSE(resp.Body, onDelta)
		resp.Body.Close()
		if perr != nil {
			// Nothing produced yet ⇒ safe to retry; otherwise surface the partial.
			if asString(msg.Content) == "" && len(msg.ToolCalls) == 0 && attempt < llmRetries {
				lastErr = perr
				continue
			}
			return msg, chatResp{Usage: usage}, perr
		}
		return msg, chatResp{Usage: usage}, nil
	}
	if lastErr == nil {
		lastErr = ctx.Err()
	}
	return wireMsg{}, chatResp{}, lastErr
}

// parseSSE consumes an OpenAI streaming response, assembling the assistant
// message (content + tool_calls by index) and the usage block.
func parseSSE(r io.Reader, onDelta func(string)) (wireMsg, usageBlock, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	var content strings.Builder
	tcs := map[int]*toolCall{}
	var order []int
	var usage usageBlock
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *usageBlock `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta
		if d.Content != "" {
			content.WriteString(d.Content)
			if onDelta != nil {
				onDelta(content.String())
			}
		}
		for _, t := range d.ToolCalls {
			tc := tcs[t.Index]
			if tc == nil {
				tc = &toolCall{Type: "function"}
				tcs[t.Index] = tc
				order = append(order, t.Index)
			}
			if t.ID != "" {
				tc.ID = t.ID
			}
			if t.Function.Name != "" {
				tc.Function.Name = t.Function.Name
			}
			tc.Function.Arguments += t.Function.Arguments
		}
	}
	if err := sc.Err(); err != nil {
		return wireMsg{}, usage, err
	}
	msg := wireMsg{Role: "assistant", Content: content.String()}
	for _, idx := range order {
		msg.ToolCalls = append(msg.ToolCalls, *tcs[idx])
	}
	return msg, usage, nil
}

// gwPath is the llm-gw component path relative to apps/. The agent is authored
// against "apps/llm-gw"; template instantiation rewrites the agent's OWN path
// but leaves cross-component references intact, so this stays "llm-gw".
func gwPath() string { return "llm-gw" }

// estimateTokens is a tokenizer-free rough estimate (~4 chars/token). Good
// enough for a context budget; swap in a real tokenizer per instance if needed.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}
