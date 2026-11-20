// llm_chat.go — the Chat Completions wire. Reasoning arrives in whichever shape
// the provider speaks: delta.reasoning_content (DeepSeek, vLLM), delta.reasoning
// (OpenRouter, Ollama), reasoning_details (OpenRouter's structured blocks, kept
// raw so they can be replayed), or a leading <think>…</think> in the content.
// What an answer looks like on the wire — a chunk, a body, a usage block — is
// the SDK's (sdk/llm, D226); the request's messages and tools are the
// engine's own types, written as they are stored.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/xbin-dev/xbin/sdk/llm"
)

type chatCodec struct{}

func (chatCodec) name() string                           { return "chat" }
func (chatCodec) path() string                           { return llm.PathChat }
func (chatCodec) unsupported(int, string) bool           { return false }
func (chatCodec) parseBody(raw []byte) (LLMReply, error) { return parseChatBody(raw) }

// chatMsg is a wireMsg plus the fields replay may add: reasoning_details, or
// the plain reasoning in the field it came in (reasoning_content, reasoning)
// — sent only back to the provider, model and wire that produced it
// (sameSource): an unknown field is a 400 on strict providers, and another
// model's reasoning is not this one's.
type chatMsg struct {
	wireMsg
	ReasoningDetails json.RawMessage `json:"reasoning_details,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	Reasoning        string          `json:"reasoning,omitempty"`
}

// sameSource reports whether reasoning stored as r goes back on req (the
// wire it is encoded for): the same wire and model — and, for a message
// that names them, the same provider and served model (Via): the same
// model id on another provider is another model. A message stored before
// Via existed keeps the rule it had (wire and model).
func sameSource(r *msgMeta, wire string, req LLMRequest) bool {
	return r != nil && !req.NoReplay && r.Wire == wire && r.Model == req.Model && (r.Via == "" || r.Via == req.Via)
}

// plainFields is the fields of plain reasoning a chat request on req sends
// back (what an upstream that refuses them 400s on): reasoning_content,
// reasoning, think — each once, in the order met.
func plainFields(req LLMRequest) []string {
	var out []string
	for _, m := range req.Msgs {
		if r := m.Replay; m.Role == "assistant" && sameSource(r, "chat", req) && len(r.ReasoningRaw) == 0 && r.ReplayField != "" && r.Via != "" && r.Reasoning != "" &&
			!plainOff(req, r.ReplayField) && !slices.Contains(out, r.ReplayField) {
			out = append(out, r.ReplayField)
		}
	}
	return out
}

// plainOff reports whether req sends no plain reasoning in field.
func plainOff(req LLMRequest, field string) bool { return req.PlainOff == "*" || req.PlainOff == field }

// chatBody is llm.ChatRequest over the engine's messages and tools: they
// omit and keep members by the engine's rules (a nil content is left out, a
// tool's empty description is written), and what a provider is sent must
// not change with where the type lives.
type chatBody struct {
	Model           string             `json:"model"`
	Messages        []chatMsg          `json:"messages"`
	Tools           []toolSpec         `json:"tools,omitempty"`
	Stream          bool               `json:"stream,omitempty"`
	StreamOptions   *llm.StreamOptions `json:"stream_options,omitempty"`
	ReasoningEffort string             `json:"reasoning_effort,omitempty"`
}

func (chatCodec) encode(req LLMRequest) ([]byte, error) {
	b := chatBody{Model: req.Model, Tools: req.Tools, ReasoningEffort: req.ReasoningEffort}
	if req.Reasons == reasonsNo { // its record says it has no reasoning: a strict provider refuses the field
		b.ReasoningEffort = ""
	}
	if req.Stream {
		b.Stream, b.StreamOptions = true, &llm.StreamOptions{IncludeUsage: true}
	}
	for _, m := range req.Msgs {
		cm := chatMsg{wireMsg: m}
		if r := m.Replay; m.Role == "assistant" && sameSource(r, "chat", req) {
			switch {
			case len(r.ReasoningRaw) > 0: // OpenRouter's blocks win: they are what it asks back
				cm.ReasoningDetails = r.ReasoningRaw
			case r.Via == "" || r.Reasoning == "": // plain reasoning goes back only to a source it names
			case plainOff(req, r.ReplayField): // its provider refused it (llm_gateway.go)
			case r.ReplayField == "reasoning_content":
				cm.ReasoningContent = r.Reasoning
			case r.ReplayField == "reasoning":
				cm.Reasoning = r.Reasoning
			case r.ReplayField == "think": // as it came: a leading block of the content
				edge := thinkEdge{}
				if r.ThinkEdge != nil {
					edge = *r.ThinkEdge
				}
				cm.Content = edge.Lead + thinkOpen + r.Reasoning + thinkClose + edge.Gap + asString(m.Content)
			}
		}
		b.Messages = append(b.Messages, cm)
	}
	return json.Marshal(b)
}

// chatUsageBlock is a usage block as its provider counted it: nothing is
// filled in or bounded (a provider that counts reasoning apart from the
// completion reports more of it than completion tokens).
func chatUsageBlock(u *llm.ChatUsage) usageBlock {
	b := usageBlock{PromptTokens: int(u.PromptTokens), CompletionTokens: int(u.CompletionTokens), TotalTokens: int(u.TotalTokens)}
	if u.CompletionTokensDetails != nil {
		b.ReasoningTokens = int(u.CompletionTokensDetails.ReasoningTokens)
	}
	return b
}

// chatChunk and chatAnswer are the wire's stream chunk and body with the
// error member kept as it came: what a person is shown is its message, or
// else the member whole (upstreamError), whatever shape a server gave it.
type chatChunk struct {
	llm.ChatChunk
	Error json.RawMessage `json:"error"`
}

type chatAnswer struct {
	llm.ChatResponse
	Error json.RawMessage `json:"error"`
}

func upstreamError(r json.RawMessage) error {
	if len(r) == 0 || string(r) == "null" {
		return nil
	}
	var e llm.Error
	if r[0] == '{' && json.Unmarshal(r, &e) == nil && e.Message != "" {
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
	field    string // the plain reasoning's field: reasoning_content, else reasoning
}

// plainField is the field a delta's plain reasoning came in ("" none).
func plainField(f llm.ChatReplyFields) string {
	if f.ReasoningContent != nil && *f.ReasoningContent != "" {
		return "reasoning_content"
	}
	var s string
	if len(f.Reasoning) > 0 && f.Reasoning[0] == '"' && json.Unmarshal(f.Reasoning, &s) == nil && s != "" {
		return "reasoning"
	}
	return ""
}

func (a *chatAcc) noteField(f llm.ChatReplyFields) {
	if k := plainField(f); k != "" && a.field != "reasoning_content" {
		a.field = k // reasoning_content where any part came in it: the name most servers take back
	}
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

func (a *chatAcc) addTool(t llm.ToolCallDelta) {
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
	switch {
	case a.sawPlain:
		rep.ReplayField = a.field
	case a.think.closed && a.think.thought.Len() > 0:
		rep.ReplayField = "think"
		rep.ThinkEdge = thinkEdge{Lead: a.think.lead, Gap: a.think.gap.String()}
	}
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
			a.usage = chatUsageBlock(c.Usage)
		}
		if len(c.Choices) == 0 {
			return nil
		}
		ch := c.Choices[0]
		d := ch.Delta
		a.noteField(d)
		plain := d.ReasoningText()
		detailText := a.details.add(d.ReasoningDetails)
		if plain != "" || detailText != "" {
			a.addThinking(plain, detailText)
		}
		if text := d.Text(); text != "" {
			a.addContent(text)
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
	var cr chatAnswer
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
	a.noteField(m)
	a.addThinking(m.ReasoningText(), a.details.add(m.ReasoningDetails))
	content := m.Text()
	if content == "" && len(m.Content) > 0 && m.Content[0] != 'n' && m.Content[0] != '"' {
		content = string(m.Content) // a parts array: keep it readable rather than lose it
	}
	a.addContent(content)
	rep := a.reply()
	if m.ToolCalls != nil {
		rep.Msg.ToolCalls = make([]toolCall, len(m.ToolCalls))
	}
	for i, t := range m.ToolCalls {
		tc := toolCall{ID: t.ID, Type: t.Type}
		if tc.Type == "" {
			tc.Type = "function"
		}
		tc.Function.Name, tc.Function.Arguments = t.Function.Name, t.Function.Arguments
		rep.Msg.ToolCalls[i] = tc
	}
	if cr.Usage != nil {
		rep.Usage = chatUsageBlock(cr.Usage)
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
	// what replay puts back as it came: the whitespace before <think>, the
	// whitespace trimmed after </think>, and whether the block closed
	lead   string
	gap    strings.Builder
	closed bool
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
			t.lead = b[:len(b)-len(trimmed)]
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
		t.state, t.trimLead, t.closed = 2, true, true
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
		rest := strings.TrimLeft(s, " \t\r\n")
		t.gap.WriteString(s[:len(s)-len(rest)])
		if s = rest; s == "" {
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
