// llm_chat.go — the Chat Completions wire. Reasoning arrives in whichever shape
// the provider speaks: delta.reasoning_content (DeepSeek, vLLM), delta.reasoning
// (OpenRouter, Ollama), reasoning_details (OpenRouter's structured blocks, kept
// raw so they can be replayed), or a leading <think>…</think> in the content.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type chatCodec struct{}

func (chatCodec) name() string                           { return "chat" }
func (chatCodec) path() string                           { return "/v1/chat/completions" }
func (chatCodec) unsupported(int, string) bool           { return false }
func (chatCodec) parseBody(raw []byte) (LLMReply, error) { return parseChatBody(raw) }

// chatMsg is a wireMsg plus the one field replay may add. reasoning_details is
// sent only back to the wire and model that produced it: an unknown field is a
// 400 on strict providers.
type chatMsg struct {
	wireMsg
	ReasoningDetails json.RawMessage `json:"reasoning_details,omitempty"`
}

type chatStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatBody struct {
	Model           string          `json:"model"`
	Messages        []chatMsg       `json:"messages"`
	Tools           []toolSpec      `json:"tools,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
	StreamOptions   *chatStreamOpts `json:"stream_options,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
}

func (chatCodec) encode(req LLMRequest) ([]byte, error) {
	b := chatBody{Model: req.Model, Tools: req.Tools, ReasoningEffort: req.ReasoningEffort}
	if req.Stream {
		b.Stream, b.StreamOptions = true, &chatStreamOpts{IncludeUsage: true}
	}
	for _, m := range req.Msgs {
		cm := chatMsg{wireMsg: m}
		if r := m.Replay; m.Role == "assistant" && r != nil && r.Wire == "chat" && r.Model == req.Model && len(r.ReasoningRaw) > 0 {
			cm.ReasoningDetails = r.ReasoningRaw
		}
		b.Messages = append(b.Messages, cm)
	}
	return json.Marshal(b)
}

type chatUsage struct {
	PromptTokens            int `json:"prompt_tokens"`
	CompletionTokens        int `json:"completion_tokens"`
	TotalTokens             int `json:"total_tokens"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u *chatUsage) block() usageBlock {
	b := usageBlock{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
	if u.CompletionTokensDetails != nil {
		b.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	return b
}

type chatToolDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content          *string           `json:"content"`
			ReasoningContent *string           `json:"reasoning_content"`
			Reasoning        json.RawMessage   `json:"reasoning"`
			ReasoningDetails []json.RawMessage `json:"reasoning_details"`
			ToolCalls        []chatToolDelta   `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage      `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// rawString returns a JSON value's string when it is one ("" otherwise) —
// delta.reasoning is a string on every provider that sends it, but null too.
func rawString(r json.RawMessage) string {
	var s string
	if len(r) > 0 && r[0] == '"' && json.Unmarshal(r, &s) == nil {
		return s
	}
	return ""
}

func upstreamError(r json.RawMessage) error {
	if len(r) == 0 || string(r) == "null" {
		return nil
	}
	var e struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	}
	if json.Unmarshal(r, &e) == nil && e.Message != "" {
		return &streamError{msg: fmt.Sprintf("llm upstream error: %s", e.Message)}
	}
	return &streamError{msg: "llm upstream error: " + string(r)}
}

// chatAcc accumulates one assistant reply from chat deltas.
type chatAcc struct {
	emit     func(LLMEvent)
	text     strings.Builder
	plain    strings.Builder // reasoning_content / reasoning
	details  detailsAcc
	think    thinkSplit
	tcs      map[int]*toolCall
	order    []int
	finish   string
	usage    usageBlock
	sawPlain bool
}

func newChatAcc(emit func(LLMEvent)) *chatAcc {
	if emit == nil {
		emit = func(LLMEvent) {}
	}
	return &chatAcc{emit: emit, tcs: map[int]*toolCall{}}
}

func (a *chatAcc) thinking() string {
	if a.sawPlain {
		return a.plain.String()
	}
	if t := a.think.thought.String(); t != "" {
		return t
	}
	return a.details.text.String()
}

func (a *chatAcc) addThinking(plain string, detailText string) {
	if plain != "" {
		a.sawPlain = true
		a.plain.WriteString(plain)
	}
	if plain != "" || (detailText != "" && !a.sawPlain) {
		a.emit(LLMEvent{Kind: "thinking", Text: a.thinking()})
	}
}

func (a *chatAcc) addContent(s string) {
	text, thought := a.think.feed(s)
	if thought != "" {
		a.emit(LLMEvent{Kind: "thinking", Text: a.thinking()})
	}
	if text != "" {
		a.text.WriteString(text)
		a.emit(LLMEvent{Kind: "text", Text: a.text.String()})
	}
}

func (a *chatAcc) addTool(t chatToolDelta) {
	tc := a.tcs[t.Index]
	if tc == nil {
		tc = &toolCall{Type: "function"}
		a.tcs[t.Index] = tc
		a.order = append(a.order, t.Index)
	}
	if t.ID != "" {
		tc.ID = t.ID
	}
	if t.Function.Name != "" {
		tc.Function.Name = t.Function.Name
	}
	tc.Function.Arguments += t.Function.Arguments
	pos := 0
	for i, idx := range a.order {
		if idx == t.Index {
			pos = i
		}
	}
	a.emit(LLMEvent{Kind: "tool", Index: pos, ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments})
}

func (a *chatAcc) reply() LLMReply {
	// Content the splitter was still holding back (an unfinished "<thi" or an
	// unterminated think block) is flushed where it belongs.
	text, _ := a.think.flush()
	a.text.WriteString(text)
	msg := wireMsg{Role: "assistant", Content: a.text.String()}
	for _, idx := range a.order {
		msg.ToolCalls = append(msg.ToolCalls, *a.tcs[idx])
	}
	rep := LLMReply{Msg: msg, Reasoning: a.thinking(), ReasoningRaw: a.details.raw(), Usage: a.usage, Finish: a.finish}
	if rep.Finish == "" {
		rep.Finish = "stop"
		if len(msg.ToolCalls) > 0 {
			rep.Finish = "tool_calls"
		}
	}
	return rep
}

func (chatCodec) parseStream(r io.Reader, emit func(LLMEvent)) (LLMReply, error) {
	a := newChatAcc(emit)
	err := sseData(r, func(data []byte) error {
		var c chatChunk
		if json.Unmarshal(data, &c) != nil {
			return nil // a keep-alive or a shape we do not know: skip it
		}
		if err := upstreamError(c.Error); err != nil {
			return err
		}
		if c.Usage != nil {
			a.usage = c.Usage.block()
		}
		if len(c.Choices) == 0 {
			return nil
		}
		ch := c.Choices[0]
		d := ch.Delta
		plain := ""
		if d.ReasoningContent != nil {
			plain += *d.ReasoningContent
		}
		plain += rawString(d.Reasoning)
		detailText := a.details.add(d.ReasoningDetails)
		if plain != "" || detailText != "" {
			a.addThinking(plain, detailText)
		}
		if d.Content != nil && *d.Content != "" {
			a.addContent(*d.Content)
		}
		for _, t := range d.ToolCalls {
			a.addTool(t)
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			a.finish = *ch.FinishReason
		}
		return nil
	})
	rep := a.reply()
	return rep, err
}

func parseChatBody(raw []byte) (LLMReply, error) {
	var cr struct {
		Choices []struct {
			Message struct {
				Content          json.RawMessage   `json:"content"`
				ReasoningContent *string           `json:"reasoning_content"`
				Reasoning        json.RawMessage   `json:"reasoning"`
				ReasoningDetails []json.RawMessage `json:"reasoning_details"`
				ToolCalls        []toolCall        `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *chatUsage      `json:"usage"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &cr); err != nil {
		return LLMReply{}, fmt.Errorf("llm-gw bad response: %w", err)
	}
	if err := upstreamError(cr.Error); err != nil {
		return LLMReply{}, err
	}
	if len(cr.Choices) == 0 {
		return LLMReply{}, fmt.Errorf("llm-gw returned no choices")
	}
	m := cr.Choices[0].Message
	a := newChatAcc(nil)
	plain := ""
	if m.ReasoningContent != nil {
		plain = *m.ReasoningContent
	}
	plain += rawString(m.Reasoning)
	a.addThinking(plain, a.details.add(m.ReasoningDetails))
	content := rawString(m.Content)
	if content == "" && len(m.Content) > 0 && m.Content[0] != 'n' && m.Content[0] != '"' {
		content = string(m.Content) // a parts array: keep it readable rather than lose it
	}
	a.addContent(content)
	rep := a.reply()
	for i := range m.ToolCalls {
		if m.ToolCalls[i].Type == "" {
			m.ToolCalls[i].Type = "function"
		}
	}
	rep.Msg.ToolCalls = m.ToolCalls
	if cr.Usage != nil {
		rep.Usage = cr.Usage.block()
	}
	rep.Finish = cr.Choices[0].FinishReason
	if rep.Finish == "" {
		rep.Finish = "stop"
		if len(rep.Msg.ToolCalls) > 0 {
			rep.Finish = "tool_calls"
		}
	}
	return rep, nil
}

// detailsAcc merges streamed reasoning_details. Providers stream a block as
// fragments sharing an "index"; the replay wants whole blocks, so fragments of
// the same index and type are merged (text/summary/data concatenated, other
// fields kept from the latest fragment that set them).
type detailsAcc struct {
	items []map[string]any
	text  strings.Builder // display text: the blocks' text/summary
}

func (d *detailsAcc) add(raw []json.RawMessage) (displayDelta string) {
	var b strings.Builder
	for _, r := range raw {
		var it map[string]any
		if json.Unmarshal(r, &it) != nil || it == nil {
			continue
		}
		for _, k := range []string{"text", "summary"} {
			if s, ok := it[k].(string); ok {
				b.WriteString(s)
			}
		}
		idx, hasIdx := it["index"].(float64)
		var into map[string]any
		if hasIdx {
			for _, prev := range d.items {
				if pi, ok := prev["index"].(float64); ok && pi == idx && prev["type"] == it["type"] {
					into = prev
				}
			}
		}
		if into == nil {
			d.items = append(d.items, it)
			continue
		}
		for k, v := range it {
			s, isStr := v.(string)
			if prev, ok := into[k].(string); ok && isStr && (k == "text" || k == "summary" || k == "data") {
				into[k] = prev + s
			} else if v != nil {
				into[k] = v
			}
		}
	}
	d.text.WriteString(b.String())
	return b.String()
}

func (d *detailsAcc) raw() json.RawMessage {
	if len(d.items) == 0 {
		return nil
	}
	b, _ := json.Marshal(d.items)
	return b
}

// thinkSplit peels a leading <think>…</think> block (DeepSeek-R1 distills,
// Qwen, many local models) off streamed content. Tags may be split across
// chunks, so an undecided prefix and a possible partial closing tag are held
// back until they resolve.
type thinkSplit struct {
	state    int // 0 undecided, 1 inside <think>, 2 plain text
	hold     strings.Builder
	thought  strings.Builder
	trimLead bool
}

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// feed takes one content delta and returns the text and thought it releases.
func (t *thinkSplit) feed(s string) (text, thought string) {
	switch t.state {
	case 0:
		t.hold.WriteString(s)
		b := t.hold.String()
		trimmed := strings.TrimLeft(b, " \t\r\n")
		switch {
		case strings.HasPrefix(trimmed, thinkOpen):
			t.state = 1
			t.hold.Reset()
			return t.inside(trimmed[len(thinkOpen):])
		case len(trimmed) < len(thinkOpen) && strings.HasPrefix(thinkOpen, trimmed):
			return "", "" // still could be a <think>
		}
		t.state = 2
		t.hold.Reset()
		return b, ""
	case 1:
		return t.inside(s)
	}
	return t.plain(s), ""
}

func (t *thinkSplit) inside(s string) (text, thought string) {
	b := t.hold.String() + s
	t.hold.Reset()
	if i := strings.Index(b, thinkClose); i >= 0 {
		t.state, t.trimLead = 2, true
		thought = b[:i]
		t.thought.WriteString(thought)
		return t.plain(b[i+len(thinkClose):]), thought
	}
	keep := partialSuffix(b, thinkClose)
	t.hold.WriteString(b[len(b)-keep:])
	thought = b[:len(b)-keep]
	t.thought.WriteString(thought)
	return "", thought
}

func (t *thinkSplit) plain(s string) string {
	if t.trimLead {
		s = strings.TrimLeft(s, " \t\r\n")
		if s == "" {
			return ""
		}
		t.trimLead = false
	}
	return s
}

// flush releases whatever is still held at the end of the stream.
func (t *thinkSplit) flush() (text, thought string) {
	b := t.hold.String()
	t.hold.Reset()
	switch t.state {
	case 0:
		return b, ""
	case 1:
		t.thought.WriteString(b)
		return "", b
	}
	return "", ""
}

// partialSuffix is the length of the longest suffix of s that is a proper
// prefix of tag.
func partialSuffix(s, tag string) int {
	for n := len(tag) - 1; n > 0; n-- {
		if n <= len(s) && strings.HasSuffix(s, tag[:n]) {
			return n
		}
	}
	return 0
}
