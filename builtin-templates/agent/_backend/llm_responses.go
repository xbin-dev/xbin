// llm_responses.go — OpenAI's Responses API wire (POST /v1/responses through
// llm-gw, which proxies any /v1/ path). It is the only API that returns a
// GPT-5/o-series model's reasoning — as summaries — and, with store:false plus
// include:[reasoning.encrypted_content], hands back reasoning items that are
// replayed on the next turn so the model keeps its chain across tool calls.
// The wire's types — the request, its input items, an answer, a stream event
// and its names, a usage block — are the SDK's (sdk/llm, D226).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/sdk/llm"
)

type responsesCodec struct{}

func (responsesCodec) name() string { return "responses" }
func (responsesCodec) path() string { return llm.PathResponses }

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
	b := llm.ResponsesRequest{Model: req.Model, Stream: req.Stream, Input: []any{}}
	if isReasoningModel(req.Model) || req.ReasoningEffort != "" {
		// Only a reasoning model accepts these; a non-reasoning model forced
		// onto this wire would 400 on them.
		b.Reasoning = &llm.Reasoning{Effort: req.ReasoningEffort, Summary: "auto"}
		b.Include = []string{"reasoning.encrypted_content"}
	}
	for _, t := range req.Tools {
		b.Tools = append(b.Tools, llm.ResponsesTool{Type: "function", Name: t.Function.Name,
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
			b.Input = append(b.Input, llm.InputMessage{Role: "developer", Content: asString(m.Content)})
		case "user":
			b.Input = append(b.Input, llm.InputMessage{Role: "user", Content: respContent(m.Content)})
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
				b.Input = append(b.Input, llm.InputMessage{Role: "assistant", Content: t})
			}
			for _, tc := range m.ToolCalls {
				b.Input = append(b.Input, llm.FunctionCall{Type: "function_call", CallID: tc.ID,
					Name: tc.Function.Name, Arguments: tc.Function.Arguments})
			}
		case "tool":
			b.Input = append(b.Input, llm.FunctionCallOutput{Type: "function_call_output", CallID: m.ToolCallID, Output: asString(m.Content)})
		}
	}
	return json.Marshal(b)
}

// respUsageBlock is a usage block as its provider counted it, with the
// total summed when it gave none.
func respUsageBlock(u *llm.ResponsesUsage) usageBlock {
	b := usageBlock{PromptTokens: int(u.InputTokens), CompletionTokens: int(u.OutputTokens), TotalTokens: int(u.TotalTokens)}
	if b.TotalTokens == 0 {
		b.TotalTokens = b.PromptTokens + b.CompletionTokens
	}
	if u.OutputTokensDetails != nil {
		b.ReasoningTokens = int(u.OutputTokensDetails.ReasoningTokens)
	}
	return b
}

// respResponse and respEvent are the wire's answer and stream event with
// the answer's error member kept as it came (upstreamError, llm_chat.go).
type respResponse struct {
	llm.Response
	Error json.RawMessage `json:"error"`
}

type respEvent struct {
	llm.ResponsesEvent
	Response *respResponse `json:"response"`
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
	var it llm.OutputItem
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
		a.usage = respUsageBlock(r.Usage)
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

func (responsesCodec) parseStream(r io.Reader, emit func(LLMEvent)) (LLMReply, error) {
	a := newRespAcc(emit)
	err := sseData(r, func(data []byte) error {
		var ev respEvent
		if json.Unmarshal(data, &ev) != nil {
			return nil
		}
		switch ev.Type {
		case llm.EventOutputTextDelta:
			if ev.Delta != "" {
				a.text.WriteString(ev.Delta)
				a.emit(LLMEvent{Kind: "text", Text: a.text.String()})
			}
		case llm.EventReasoningSummaryDelta:
			a.addThought(fmt.Sprintf("%d.%d", ev.OutputIndex, ev.SummaryIndex), ev.Delta)
		case llm.EventReasoningTextDelta:
			a.addThought(fmt.Sprintf("%d.r%d", ev.OutputIndex, ev.ContentIndex), ev.Delta)
		case llm.EventOutputItemAdded:
			var it llm.OutputItem
			if json.Unmarshal(ev.Item, &it) == nil && it.Type == "function_call" {
				tc, pos := a.call(ev.OutputIndex)
				tc.ID, tc.Function.Name = it.CallID, it.Name
				tc.Function.Arguments += it.Arguments
				a.emitCall(pos)
			}
		case llm.EventFunctionArgsDelta:
			tc, pos := a.call(ev.OutputIndex)
			tc.Function.Arguments += ev.Delta
			a.emitCall(pos)
		case llm.EventFunctionArgsDone:
			if ev.Arguments != nil {
				tc, _ := a.call(ev.OutputIndex)
				tc.Function.Arguments = *ev.Arguments
			}
		case llm.EventOutputItemDone:
			if len(ev.Item) > 0 {
				a.item(ev.OutputIndex, ev.Item, true)
			}
		case llm.EventCompleted, llm.EventIncomplete:
			if ev.Response != nil {
				a.complete(ev.Response)
			}
		case llm.EventFailed:
			msg := "the model's response failed"
			if ev.Response != nil {
				if e := upstreamError(ev.Response.Error); e != nil {
					return e
				}
			}
			return &streamError{msg: "llm upstream error: " + msg}
		case llm.EventError:
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
