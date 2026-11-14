package llm

import "encoding/json"

// ResponsesRequest is a Responses API request as far as xbin's callers
// build it. Input items are InputMessage, FunctionCall, FunctionCallOutput,
// or a raw item replayed as it came (a reasoning item with its
// encrypted_content).
type ResponsesRequest struct {
	Model        string          `json:"model"`
	Instructions string          `json:"instructions,omitempty"`
	Input        []any           `json:"input"`
	Tools        []ResponsesTool `json:"tools,omitempty"`
	Stream       bool            `json:"stream,omitempty"`
	Reasoning    *Reasoning      `json:"reasoning,omitempty"`
	// Store is sent as is: false keeps the conversation with the caller.
	Store   bool     `json:"store"`
	Include []string `json:"include,omitempty"` // e.g. "reasoning.encrypted_content"
}

// ResponsesTool is a function on the Responses wire (flat, unlike Tool).
type ResponsesTool struct {
	Type        string         `json:"type"` // "function"
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

// Reasoning asks a reasoning model for an effort and a summary ("auto").
// Only reasoning models accept it.
type Reasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// InputMessage is a message input item. Content is a string or an array of
// parts ({type: "input_text", text} | {type: "input_image", image_url}).
type InputMessage struct {
	Role    string `json:"role"` // user | assistant | developer | system
	Content any    `json:"content"`
}

// FunctionCall is an earlier tool call replayed as input.
type FunctionCall struct {
	Type      string `json:"type"` // "function_call"
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// FunctionCallOutput is a tool's result.
type FunctionCallOutput struct {
	Type   string `json:"type"` // "function_call_output"
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// Response is a Responses API answer: whole for a non-streaming call, and
// inside a stream's response.completed / response.incomplete /
// response.failed event. Output items are kept raw; OutputItem decodes one.
type Response struct {
	ID                string            `json:"id,omitempty"`
	Model             string            `json:"model,omitempty"`
	Status            string            `json:"status,omitempty"` // completed | incomplete | failed | in_progress
	Output            []json.RawMessage `json:"output"`
	Usage             *ResponsesUsage   `json:"usage,omitempty"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details,omitempty"`
	Error *Error `json:"error,omitempty"`
}

// OutputItem is one element of a response's output: a message (Content
// parts of type output_text), a function_call, or a reasoning item
// (Summary texts; replayed raw, with its encrypted_content).
type OutputItem struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Summary   []struct {
		Text string `json:"text"`
	} `json:"summary,omitempty"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content,omitempty"`
}

// The Responses stream event types a client folds.
const (
	EventOutputTextDelta       = "response.output_text.delta"
	EventReasoningSummaryDelta = "response.reasoning_summary_text.delta"
	EventReasoningTextDelta    = "response.reasoning_text.delta"
	EventOutputItemAdded       = "response.output_item.added"
	EventOutputItemDone        = "response.output_item.done"
	EventFunctionArgsDelta     = "response.function_call_arguments.delta"
	EventFunctionArgsDone      = "response.function_call_arguments.done"
	EventCompleted             = "response.completed"
	EventIncomplete            = "response.incomplete"
	EventFailed                = "response.failed"
	EventError                 = "error"
)

// ResponsesEvent is one data payload of a Responses stream. Which fields
// are set depends on Type; Response rides the terminal events and carries
// the usage.
type ResponsesEvent struct {
	Type         string          `json:"type"`
	OutputIndex  int             `json:"output_index"`
	SummaryIndex int             `json:"summary_index"`
	ContentIndex int             `json:"content_index"`
	Delta        string          `json:"delta,omitempty"`
	Arguments    *string         `json:"arguments,omitempty"`
	Item         json.RawMessage `json:"item,omitempty"`
	Response     *Response       `json:"response,omitempty"`
	Message      string          `json:"message,omitempty"` // type error
	Code         any             `json:"code,omitempty"`
}
