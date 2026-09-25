// llm_responses.go — OpenAI's Responses API wire (POST /v1/responses through
// llm-gw, which proxies any /v1/ path). It is the only API that returns a
// GPT-5/o-series model's reasoning — as summaries — and, with store:false plus
// include:[reasoning.encrypted_content], hands back reasoning items that are
// replayed on the next turn so the model keeps its chain across tool calls.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type responsesCodec struct{}

func (responsesCodec) name() string { return "responses" }
func (responsesCodec) path() string { return "/v1/responses" }

// unsupported recognises a gateway or upstream that has no Responses API, so
// the call can fall back to Chat Completions.
func (responsesCodec) unsupported(code int, body string) bool {
	if code == http.StatusNotFound {
		return true
	}
	if code != http.StatusBadRequest {
		return false
	}
	b := strings.ToLower(body)
	for _, s := range []string{"responses", "endpoint", "not supported", "unknown"} {
		if strings.Contains(b, s) {
			return true
		}
	}
	return false
}

type respTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type respReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type respBody struct {
	Model        string         `json:"model"`
	Instructions string         `json:"instructions,omitempty"`
	Input        []any          `json:"input"`
	Tools        []respTool     `json:"tools,omitempty"`
	Stream       bool           `json:"stream,omitempty"`
	Reasoning    *respReasoning `json:"reasoning,omitempty"`
	Store        bool           `json:"store"`
	Include      []string       `json:"include,omitempty"`
}

type respMsg struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type respFuncCall struct {
	Type      string `json:"type"` // function_call
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type respFuncOutput struct {
	Type   string `json:"type"` // function_call_output
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// respContent maps a chat-shaped message content (a string, or a parts array
// of text/image_url) onto Responses input content.
func respContent(c any) any {
	switch v := c.(type) {
	case nil:
		return ""
	case string:
		return v
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return asString(c)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return string(raw)
	}
	out := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "text":
			out = append(out, map[string]any{"type": "input_text", "text": p.Text})
		case "image_url":
			out = append(out, map[string]any{"type": "input_image", "image_url": p.ImageURL.URL})
		}
	}
	return out
}

func (responsesCodec) encode(req LLMRequest) ([]byte, error) {
	b := respBody{Model: req.Model, Stream: req.Stream, Input: []any{}}
	if isReasoningModel(req.Model) || req.ReasoningEffort != "" {
		// Only a reasoning model accepts these; a non-reasoning model forced
		// onto this wire would 400 on them.
		b.Reasoning = &respReasoning{Effort: req.ReasoningEffort, Summary: "auto"}
		b.Include = []string{"reasoning.encrypted_content"}
	}
	for _, t := range req.Tools {
		b.Tools = append(b.Tools, respTool{Type: "function", Name: t.Function.Name,
			Description: t.Function.Description, Parameters: t.Function.Parameters})
	}
	sawSystem := false
	for _, m := range req.Msgs {
		switch m.Role {
		case "system":
			if !sawSystem {
				sawSystem = true
				b.Instructions = asString(m.Content)
				continue
			}
			b.Input = append(b.Input, respMsg{Role: "developer", Content: asString(m.Content)})
		case "user":
			b.Input = append(b.Input, respMsg{Role: "user", Content: respContent(m.Content)})
		case "assistant":
			if r := m.Replay; r != nil && r.Wire == "responses" && r.Model == req.Model && len(r.ReasoningRaw) > 0 {
				var items []json.RawMessage
				if json.Unmarshal(r.ReasoningRaw, &items) == nil {
					for _, it := range items {
						b.Input = append(b.Input, it)
					}
				}
			}
			if t := asString(m.Content); t != "" {
				b.Input = append(b.Input, respMsg{Role: "assistant", Content: t})
			}
			for _, tc := range m.ToolCalls {
				b.Input = append(b.Input, respFuncCall{Type: "function_call", CallID: tc.ID,
					Name: tc.Function.Name, Arguments: tc.Function.Arguments})
			}
		case "tool":
			b.Input = append(b.Input, respFuncOutput{Type: "function_call_output", CallID: m.ToolCallID, Output: asString(m.Content)})
		}
	}
	return json.Marshal(b)
}

type respUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	TotalTokens         int `json:"total_tokens"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u *respUsage) block() usageBlock {
	b := usageBlock{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, TotalTokens: u.TotalTokens}
	if b.TotalTokens == 0 {
		b.TotalTokens = u.InputTokens + u.OutputTokens
	}
	if u.OutputTokensDetails != nil {
		b.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
	}
	return b
}

// respItem is one element of a response's `output` array.
type respItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Summary   []struct {
		Text string `json:"text"`
	} `json:"summary"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type respResponse struct {
	Status            string            `json:"status"`
	Output            []json.RawMessage `json:"output"`
	Usage             *respUsage        `json:"usage"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error json.RawMessage `json:"error"`
}

// respAcc accumulates one reply from Responses events or an output array.
type respAcc struct {
	emit     func(LLMEvent)
	text     strings.Builder
	thought  strings.Builder
	thinkKey string
	calls    []*toolCall
	byOutput map[int]int // output_index → position in calls
	items    []json.RawMessage
	usage    usageBlock
	finish   string
}

func newRespAcc(emit func(LLMEvent)) *respAcc {
	if emit == nil {
		emit = func(LLMEvent) {}
	}
	return &respAcc{emit: emit, byOutput: map[int]int{}}
}

func (a *respAcc) addThought(key, delta string) {
	if delta == "" {
		return
	}
	if a.thinkKey != "" && key != a.thinkKey && a.thought.Len() > 0 {
		a.thought.WriteString("\n\n")
	}
	a.thinkKey = key
	a.thought.WriteString(delta)
	a.emit(LLMEvent{Kind: "thinking", Text: a.thought.String()})
}

func (a *respAcc) call(outputIndex int) (*toolCall, int) {
	pos, ok := a.byOutput[outputIndex]
	if !ok {
		pos = len(a.calls)
		a.byOutput[outputIndex] = pos
		a.calls = append(a.calls, &toolCall{Type: "function"})
	}
	return a.calls[pos], pos
}

func (a *respAcc) emitCall(pos int) {
	tc := a.calls[pos]
	a.emit(LLMEvent{Kind: "tool", Index: pos, ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments})
}

// item folds a finished output item: reasoning is kept raw for replay (and its
// summaries shown if no deltas carried them), calls get their final arguments.
func (a *respAcc) item(outputIndex int, raw json.RawMessage, streamed bool) {
	var it respItem
	if json.Unmarshal(raw, &it) != nil {
		return
	}
	switch it.Type {
	case "reasoning":
		a.items = append(a.items, raw)
		if !streamed {
			for i, s := range it.Summary {
				a.addThought(fmt.Sprintf("%d.%d", outputIndex, i), s.Text)
			}
		}
	case "function_call":
		tc, _ := a.call(outputIndex)
		if it.CallID != "" {
			tc.ID = it.CallID
		}
		if it.Name != "" {
			tc.Function.Name = it.Name
		}
		if it.Arguments != "" || !streamed {
			tc.Function.Arguments = it.Arguments
		}
	case "message":
		if a.text.Len() == 0 {
			for _, c := range it.Content {
				if c.Type == "output_text" {
					a.text.WriteString(c.Text)
				}
			}
		}
	}
}

func (a *respAcc) complete(r *respResponse) {
	if r.Usage != nil {
		a.usage = r.Usage.block()
	}
	if r.IncompleteDetails != nil && r.IncompleteDetails.Reason != "" {
		a.finish = r.IncompleteDetails.Reason
	}
}

func (a *respAcc) reply() LLMReply {
	msg := wireMsg{Role: "assistant", Content: a.text.String()}
	for _, tc := range a.calls {
		msg.ToolCalls = append(msg.ToolCalls, *tc)
	}
	rep := LLMReply{Msg: msg, Reasoning: a.thought.String(), Usage: a.usage, Finish: a.finish}
	if len(a.items) > 0 {
		rep.ReasoningRaw, _ = json.Marshal(a.items)
	}
	if rep.Finish == "" {
		rep.Finish = "stop"
		if len(msg.ToolCalls) > 0 {
			rep.Finish = "tool_calls"
		}
	}
	return rep
}

type respEvent struct {
	Type         string          `json:"type"`
	OutputIndex  int             `json:"output_index"`
	SummaryIndex int             `json:"summary_index"`
	ContentIndex int             `json:"content_index"`
	Delta        string          `json:"delta"`
	Arguments    *string         `json:"arguments"`
	Item         json.RawMessage `json:"item"`
	Response     *respResponse   `json:"response"`
	Message      string          `json:"message"`
	Code         any             `json:"code"`
}

func (responsesCodec) parseStream(r io.Reader, emit func(LLMEvent)) (LLMReply, error) {
	a := newRespAcc(emit)
	err := sseData(r, func(data []byte) error {
		var ev respEvent
		if json.Unmarshal(data, &ev) != nil {
			return nil
		}
		switch ev.Type {
		case "response.output_text.delta":
			if ev.Delta != "" {
				a.text.WriteString(ev.Delta)
				a.emit(LLMEvent{Kind: "text", Text: a.text.String()})
			}
		case "response.reasoning_summary_text.delta":
			a.addThought(fmt.Sprintf("%d.%d", ev.OutputIndex, ev.SummaryIndex), ev.Delta)
		case "response.reasoning_text.delta":
			a.addThought(fmt.Sprintf("%d.r%d", ev.OutputIndex, ev.ContentIndex), ev.Delta)
		case "response.output_item.added":
			var it respItem
			if json.Unmarshal(ev.Item, &it) == nil && it.Type == "function_call" {
				tc, pos := a.call(ev.OutputIndex)
				tc.ID, tc.Function.Name = it.CallID, it.Name
				tc.Function.Arguments += it.Arguments
				a.emitCall(pos)
			}
		case "response.function_call_arguments.delta":
			tc, pos := a.call(ev.OutputIndex)
			tc.Function.Arguments += ev.Delta
			a.emitCall(pos)
		case "response.function_call_arguments.done":
			if ev.Arguments != nil {
				tc, _ := a.call(ev.OutputIndex)
				tc.Function.Arguments = *ev.Arguments
			}
		case "response.output_item.done":
			if len(ev.Item) > 0 {
				a.item(ev.OutputIndex, ev.Item, true)
			}
		case "response.completed", "response.incomplete":
			if ev.Response != nil {
				a.complete(ev.Response)
			}
		case "response.failed":
			msg := "the model's response failed"
			if ev.Response != nil {
				if e := upstreamError(ev.Response.Error); e != nil {
					return e
				}
			}
			return &streamError{msg: "llm upstream error: " + msg}
		case "error":
			msg := ev.Message
			if msg == "" {
				msg = string(data)
			}
			return &streamError{msg: "llm upstream error: " + msg}
		}
		return nil
	})
	return a.reply(), err
}

func (responsesCodec) parseBody(raw []byte) (LLMReply, error) {
	var r respResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return LLMReply{}, fmt.Errorf("llm-gw bad response: %w", err)
	}
	if err := upstreamError(r.Error); err != nil {
		return LLMReply{}, err
	}
	if r.Status == "failed" {
		return LLMReply{}, &streamError{msg: "llm upstream error: the model's response failed"}
	}
	a := newRespAcc(nil)
	for i, it := range r.Output {
		a.item(i, it, false)
	}
	a.complete(&r)
	return a.reply(), nil
}
