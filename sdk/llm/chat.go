package llm

import "encoding/json"

// The two wires and their paths under a provider's base URL.
const (
	WireChat      = "chat"
	WireResponses = "responses"

	PathChat      = "/v1/chat/completions"
	PathResponses = "/v1/responses"
	PathModels    = "/v1/models"
)

// ChatRequest is a Chat Completions request as far as xbin's callers build
// it. A relay that must keep every field a client sent rewrites the raw
// JSON instead (OpenRequest).
type ChatRequest struct {
	Model           string         `json:"model"`
	Messages        []ChatMessage  `json:"messages"`
	Tools           []Tool         `json:"tools,omitempty"`
	Stream          bool           `json:"stream,omitempty"`
	StreamOptions   *StreamOptions `json:"stream_options,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"` // minimal | low | medium | high
}

// StreamOptions is stream_options: include_usage makes the stream end with
// a chunk whose choices are empty and whose usage is the call's.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// WithStreamUsage turns streaming on with the usage chunk asked for.
func (r ChatRequest) WithStreamUsage() ChatRequest {
	r.Stream, r.StreamOptions = true, &StreamOptions{IncludeUsage: true}
	return r
}

// ChatMessage is one message. Content is a string, or an array of parts
// ({type: "text", text} | {type: "image_url", image_url: {url}}); null on
// an assistant message that only calls tools.
type ChatMessage struct {
	Role       string     `json:"role"` // system | developer | user | assistant | tool
	Content    any        `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // role tool: the call it answers
	// ReasoningDetails replays a model's own reasoning blocks to the provider
	// and model that produced them (OpenRouter); strict providers refuse the
	// field, so it is sent nowhere else.
	ReasoningDetails json.RawMessage `json:"reasoning_details,omitempty"`
}

// Tool is a function the model may call.
type Tool struct {
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction describes a function: Parameters is its JSON Schema.
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolCall is a call the model made: Arguments is JSON text.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type,omitempty"` // "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ChatResponse is a non-streaming Chat Completions answer.
type ChatResponse struct {
	ID      string       `json:"id,omitempty"`
	Model   string       `json:"model,omitempty"`
	Choices []ChatChoice `json:"choices"`
	Usage   *ChatUsage   `json:"usage,omitempty"`
	Error   *Error       `json:"error,omitempty"`
}

// ChatChoice is one choice of a ChatResponse.
type ChatChoice struct {
	Index        int             `json:"index"`
	Message      ChatReplyFields `json:"message"`
	FinishReason string          `json:"finish_reason,omitempty"`
}

// ChatReplyFields is what a model's reply carries, whole (a choice's
// message) or as a delta (a chunk's). Reasoning arrives under one of three
// names: reasoning_content (DeepSeek and most OpenAI-compatible servers),
// reasoning (a string on the providers that send it, null too) and
// reasoning_details (OpenRouter's typed blocks, which are also what is
// replayed).
type ChatReplyFields struct {
	Role             string            `json:"role,omitempty"`
	Content          json.RawMessage   `json:"content,omitempty"`
	ReasoningContent *string           `json:"reasoning_content,omitempty"`
	Reasoning        json.RawMessage   `json:"reasoning,omitempty"`
	ReasoningDetails []json.RawMessage `json:"reasoning_details,omitempty"`
	ToolCalls        []ToolCallDelta   `json:"tool_calls,omitempty"`
}

// Text is Content when it is a JSON string ("" for null or parts).
func (f ChatReplyFields) Text() string { return rawString(f.Content) }

// ReasoningText is the plain reasoning text of the first two names.
func (f ChatReplyFields) ReasoningText() string {
	s := ""
	if f.ReasoningContent != nil {
		s = *f.ReasoningContent
	}
	return s + rawString(f.Reasoning)
}

// ToolCallDelta is a tool call in a reply: whole in a message, and in a
// stream a fragment to append at Index (the id and name come once, the
// arguments in pieces).
type ToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// ChatChunk is one data payload of a Chat Completions stream. The usage
// chunk has no choices; an upstream failure mid-stream arrives as Error.
type ChatChunk struct {
	ID      string            `json:"id,omitempty"`
	Model   string            `json:"model,omitempty"`
	Choices []ChatChunkChoice `json:"choices"`
	Usage   *ChatUsage        `json:"usage,omitempty"`
	Error   *Error            `json:"error,omitempty"`
}

// ChatChunkChoice is one choice of a ChatChunk: FinishReason is null until
// the chunk that ends the reply.
type ChatChunkChoice struct {
	Index        int             `json:"index"`
	Delta        ChatReplyFields `json:"delta"`
	FinishReason *string         `json:"finish_reason"`
}

// rawString is a JSON value's string when it is one, "" otherwise.
func rawString(r json.RawMessage) string {
	var s string
	if len(r) > 0 && r[0] == '"' && json.Unmarshal(r, &s) == nil {
		return s
	}
	return ""
}
