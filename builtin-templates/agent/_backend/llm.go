// llm.go — the LLM connection. Like the chat tile, the agent talks to the
// llm-gw component's OpenAI-compatible surface (uses: apps/llm-gw:writer); the
// upstream API key stays in llm-gw's vault. Non-streaming: we journal the whole
// response as one durable step, and the tile watches the journal for progress.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	xbin "github.com/magik6k/xbin/sdk"
)

// Config is an agent's knobs — a default lives in settings, and each run
// snapshots one at creation (so changing the default doesn't disturb live runs).
type Config struct {
	Model       string      `json:"model"`       // llm-gw model id or alias
	System      string      `json:"system"`      // base system prompt
	TokenBudget int         `json:"tokenBudget"` // context assembly budget (rough)
	MaxIters    int         `json:"maxIters"`    // steps per drive before yielding to heartbeat
	Subagents   bool        `json:"subagents"`   // expose spawn_subagent
	Approve     bool        `json:"approve"`     // require approval before side-effecting tools
	MCP         []MCPServer `json:"mcp"`         // MCP servers whose tools are merged in
}

func defaultConfig() Config {
	return Config{
		Model:       "gpt-4o-mini",
		System:      "You are a helpful autonomous agent running inside xbin. Work toward the user's goal using the available tools. Use memory_set to remember durable facts. Call finish when the goal is done, ask_user when you need input, and yield when you should wait before continuing.",
		TokenBudget: 12000,
		MaxIters:    12,
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
	if c.Model == "" {
		c.Model = "gpt-4o-mini"
	}
	return c
}

// --- OpenAI-compatible wire types ---------------------------------------

type wireMsg struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
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

type chatResp struct {
	Choices []struct {
		Message      wireMsg `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// callLLM runs one chat completion through llm-gw. Returns the assistant
// message and the usage block.
func callLLM(ctx context.Context, model string, msgs []wireMsg, tools []toolSpec) (wireMsg, chatResp, error) {
	body, err := json.Marshal(chatReq{Model: model, Messages: msgs, Tools: tools})
	if err != nil {
		return wireMsg{}, chatResp{}, err
	}
	url := "http://xbin/api/apps/" + gwPath() + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return wireMsg{}, chatResp{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return wireMsg{}, chatResp{}, fmt.Errorf("llm-gw call: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return wireMsg{}, chatResp{}, fmt.Errorf("llm-gw %s: %s", resp.Status, strings.TrimSpace(string(raw)))
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
