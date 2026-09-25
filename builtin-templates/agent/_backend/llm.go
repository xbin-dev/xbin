// llm.go — the LLM connection: config, model tiers, and the LLM interface the
// engine calls. Like the chat tile, the agent talks to the llm-gw component
// (uses: apps/llm-gw:writer); the upstream API key stays in llm-gw's vault.
package main

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
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
// models once in llm-gw and every agent inherits them. See API.md.
type ModelTiers struct {
	General string `json:"general,omitempty"` // the main loop
	Code    string `json:"code,omitempty"`    // code-heavy work / code subagents
	Memory  string `json:"memory,omitempty"`  // compaction + memory management
	VLM     string `json:"vlm,omitempty"`     // vision fallback when general lacks it
}

// Config is an agent's knobs — a default lives in settings, and each run
// snapshots one at creation (so changing the default doesn't disturb live runs).
type Config struct {
	Model       string     `json:"model"`       // legacy/general fallback (empty ⇒ llm-gw preferred)
	Models      ModelTiers `json:"models"`      // per-tier models
	System      string     `json:"system"`      // base system prompt
	TokenBudget int        `json:"tokenBudget"` // context assembly budget
	MaxIters    int        `json:"maxIters"`    // legacy: sizes the default turn step cap (8×)
	ToolTimeout int        `json:"toolTimeout"` // seconds per tool call (0 ⇒ default)
	// REPL sandbox limits (0 ⇒ defaults). The time budget is per statement and
	// far below ToolTimeout on purpose: a runaway loop should come back as a
	// normal tool error the model can react to, not eat the whole tool slot.
	ReplTimeoutMs int `json:"replTimeoutMs,omitempty"`
	ReplMemMB     int `json:"replMemMB,omitempty"`
	// Workflow limits. MaxDepth/MaxSpawn bound ONE tree and are snapshotted
	// into run_trees when a root is created, so a live workflow keeps the
	// budget it started with. MaxActiveRuns bounds the PROCESS, so it is read
	// live from the global config — a per-run snapshot of it would be
	// meaningless.
	MaxDepth      int `json:"maxDepth,omitempty"`        // delegation depth, root = 0
	MaxSpawn      int `json:"maxSpawn,omitempty"`        // lifetime runs per tree
	MaxSpawnTurn  int `json:"maxSpawnPerTurn,omitempty"` // spawns in a single turn
	MaxActiveRuns int `json:"maxActiveRuns,omitempty"`   // concurrent MODEL CALLS, process-wide
	// MaxTurnSteps bounds one turn (model calls between a human message and the
	// agent's answer); 0 ⇒ 8×MaxIters. SubagentTimeout is the default seconds a
	// foreground subagent is waited for before it moves to the background.
	MaxTurnSteps    int `json:"maxTurnSteps,omitempty"`
	SubagentTimeout int `json:"subagentTimeout,omitempty"`
	// Wire picks the model API: "auto" (Responses for gpt-5*/o-series, else
	// Chat Completions), "chat" or "responses". ReasoningEffort is passed to
	// reasoning models ("" = the provider's default).
	Wire            string `json:"wire,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	Subagents       bool   `json:"subagents"` // expose spawn_subagent
	Approve         bool   `json:"approve"`   // require approval before side-effecting tools
	// Toolset is the run's IMMUTABLE capability lane — the exfiltration
	// firewall: "private" (default; internal reach via xbin_call + mcp:*
	// tools, NO web) or "web" (web_search/web_fetch only, NO internal reach).
	// A run never holds both private data and an egress channel; subagents
	// and agent-created schedules inherit it.
	Toolset string `json:"toolset,omitempty"`
	// Deny names tools this run never gets ("mcp:*" style prefixes end in
	// '*'): hidden from the model and refused if called anyway. Set per run
	// (a channel session's profile, D86) and inherited by its subagents.
	Deny []string    `json:"deny,omitempty"`
	MCP  []MCPServer `json:"mcp"` // legacy static MCP servers (now bound via the mcp interface)
	// Features toggles optional capabilities — a "Features" menu in the tile.
	// Absent or true = on; set a key false to turn it off. Known keys are in
	// featureKeys; unlisted keys default on so older configs get everything.
	Features map[string]bool `json:"features"`
}

// featureKeys are the toggleable capabilities shown in the tile's Features menu.
var featureKeys = []string{"recall", "skills", "streaming", "vision", "parallelTools", "watcher", "files", "repl", "workflow", "titles"}

// toolset normalizes the capability lane: anything but "web" is "private".
func (c Config) toolset() string {
	if c.Toolset == "web" {
		return "web"
	}
	return "private"
}

// denied reports whether Deny covers the tool name. finish never is: a run
// must always be able to end.
func (c Config) denied(name string) bool {
	if name == "finish" {
		return false
	}
	for _, d := range c.Deny {
		if d == name || (strings.HasSuffix(d, "*") && strings.HasPrefix(name, strings.TrimSuffix(d, "*"))) {
			return true
		}
	}
	return false
}

// normalizeToolset validates a requested capability lane ("web" or private).
func normalizeToolset(s string) string {
	if strings.TrimSpace(strings.ToLower(s)) == "web" {
		return "web"
	}
	return "private"
}

func (c Config) replTimeout() time.Duration {
	if c.ReplTimeoutMs <= 0 {
		return 5 * time.Second
	}
	if c.ReplTimeoutMs > 60000 {
		return 60 * time.Second
	}
	return time.Duration(c.ReplTimeoutMs) * time.Millisecond
}

// Workflow defaults. The tree budget, not the depth, is the real backstop:
// depth is the cheap guard, maxSpawn is what stops a spawn→finish→spawn loop
// running forever (it counts lifetime creations, not live children).
const (
	defaultMaxDepth      = 3
	defaultMaxSpawn      = 32
	defaultMaxSpawnTurn  = 8
	defaultMaxActiveRuns = 4
)

func clampCfg(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

func (c Config) maxDepth() int      { return clampCfg(c.MaxDepth, defaultMaxDepth, 8) }
func (c Config) maxSpawn() int      { return clampCfg(c.MaxSpawn, defaultMaxSpawn, 500) }
func (c Config) maxSpawnTurn() int  { return clampCfg(c.MaxSpawnTurn, defaultMaxSpawnTurn, 32) }
func (c Config) maxActiveRuns() int { return clampCfg(c.MaxActiveRuns, defaultMaxActiveRuns, 32) }
func (c Config) maxTurnSteps() int {
	if c.MaxTurnSteps > 0 {
		return clampCfg(c.MaxTurnSteps, 96, 500)
	}
	return clampCfg(8*c.MaxIters, 96, 500)
}
func (c Config) subagentTimeout() int { return clampCfg(c.SubagentTimeout, 900, 3600) }

func (c Config) replMemMB() int {
	if c.ReplMemMB <= 0 {
		return 256
	}
	return c.ReplMemMB
}

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
	// string (API.md).
	Content    any        `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	// Replay is an assistant message's stored meta (reasoning it produced).
	// Never marshalled as-is: each wire decides whether to send it back — only
	// to the same wire and model that produced it.
	Replay *msgMeta `json:"-"`
}

// contentValue returns the wire value for stored USER message content: plain
// text, or — only for content that verifiably is a multimodal parts array
// (what the vision path stores) — the raw JSON. A bare '[' prefix sniff is
// wrong twice over: tool results legitimately start with '[' (recall's
// "[#1 user] …" made the whole request unmarshalable), and a tool result that
// IS valid JSON would silently ship as parts.
func contentValue(s string) any {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "[") || !json.Valid([]byte(t)) {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(t), &parts) != nil || len(parts) == 0 {
		return s
	}
	for _, p := range parts {
		if p.Type != "text" && p.Type != "image_url" {
			return s
		}
	}
	return json.RawMessage(t)
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

type usageBlock struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// ReasoningTokens is the part of CompletionTokens spent thinking, when the
	// provider reports it (0 otherwise).
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// modelLookupTimeout bounds model resolution. It runs after the run is already
// marked running and before any chat request, so a hang there is the exact
// shape of "thinking forever with no LLM activity". A var so tests can shorten
// it.
var modelLookupTimeout = 10 * time.Second

// --- model tier resolution (API.md) --------------------------

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
	// Bounded independently of the drive context: xbin.Client() has no Timeout,
	// so on the 10-minute drive ctx a gateway that accepts the connection and
	// then goes quiet parks the drive at status=running with no chat request
	// ever made — which is indistinguishable from a hung agent.
	ctx, cancel := context.WithTimeout(ctx, modelLookupTimeout)
	defer cancel()
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
//
// The engine talks to a model through the LLM interface below; gatewayLLM
// (llm_gateway.go) implements it over llm-gw with two wires — Chat Completions
// (llm_chat.go) and OpenAI's Responses API (llm_responses.go), the only way to
// get GPT-5/o-series reasoning summaries. Tests swap in a scripted fake.

// LLM is one model call. onEvent (may be nil) receives live progress; the
// returned reply is the whole message. Implementations retry transient
// failures only before anything was streamed, and honour ctx at every read.
type LLM interface {
	Chat(ctx context.Context, req LLMRequest, onEvent func(LLMEvent)) (LLMReply, error)
}

// LLMRequest is what the engine asks for. Wire is "auto" | "chat" |
// "responses"; Replay lets a wire re-send the opaque reasoning it produced on
// earlier turns (kept per assistant message in messages.meta).
type LLMRequest struct {
	// Run and Purpose ("turn" | "compact") identify the caller; the wires
	// ignore them (logs and tests use them).
	Run             int64
	Purpose         string
	Model           string
	Msgs            []wireMsg
	Tools           []toolSpec
	Stream          bool
	Wire            string
	ReasoningEffort string // "" = provider default; minimal|low|medium|high
}

// LLMEvent is live progress from a streaming call. Text-like events carry the
// ACCUMULATED text, so a consumer that drops one loses nothing.
type LLMEvent struct {
	Kind  string // "text" | "thinking" | "tool"
	Text  string // text/thinking: accumulated so far
	Index int    // tool: position in the tool_calls list
	ID    string // tool: call id, once known
	Name  string // tool: function name, once known
	Args  string // tool: accumulated (partial) JSON arguments
}

// LLMReply is a finished call. Reasoning is the human-readable thinking (a
// summary on the Responses wire); ReasoningRaw is whatever the wire needs to
// replay it next turn (chat: reasoning_details; responses: reasoning items with
// encrypted_content), tagged with the wire and model that produced it.
type LLMReply struct {
	Msg          wireMsg
	Reasoning    string
	ReasoningRaw json.RawMessage
	ReasoningMs  int64
	Usage        usageBlock
	Finish       string
	Wire         string // the wire actually used
	Model        string
}

// msgMeta is the additive messages.meta column: per assistant message, what the
// transcript's flat OpenAI shape has no field for.
type msgMeta struct {
	Reasoning    string          `json:"reasoning,omitempty"`
	ReasoningRaw json.RawMessage `json:"reasoningRaw,omitempty"`
	ReasoningMs  int64           `json:"reasoningMs,omitempty"`
	Wire         string          `json:"wire,omitempty"`
	Model        string          `json:"model,omitempty"`
	Usage        *usageBlock     `json:"usage,omitempty"`
	Finish       string          `json:"finish,omitempty"`
	// On user messages (D83): who sent it, and what delivered it when it
	// wasn't a person typing (a schedule, a channel, a trigger). The model
	// never sees these fields; the transcript text carries what it needs.
	Sender   string `json:"sender,omitempty"`
	Origin   string `json:"origin,omitempty"`
	OriginID int64  `json:"originId,omitempty"`
	Label    string `json:"label,omitempty"`
}

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
