package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// The request half of translate.go: a Responses request rewritten as a
// chat completion. The rule (D244): nothing vanishes silently. What would
// change the request's meaning — state OpenAI keeps, a hosted tool the
// caller requires, content chat completions can't carry — is refused with
// a 400 naming the field; what is advisory, and a hosted tool merely
// offered, is not sent and is named in the answer's DroppedHeader.

// xlate is what the answer's translation needs of the request.
type xlate struct {
	stream   bool
	model    string          // as the caller named it: echoed in the answer
	custom   map[string]bool // tools that were "custom" (free-form): their calls go back as custom_tool_call
	tools    any             // the request's tools, echoed in the response object
	parallel bool            // its parallel_tool_calls
	truncate bool            // it asked for truncation: "auto"
	dropped  []string        // what of it was not sent
}

func (x *xlate) drop(what string) {
	if !slices.Contains(x.dropped, what) {
		x.dropped = append(x.dropped, what)
	}
}

type chatMsg struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func xlateRefuse(format string, a ...any) *refusal {
	return refuse(http.StatusBadRequest, "xbin_llm_translate",
		fmt.Sprintf(format, a...)+" — this upstream has chat completions only, and the connector translates Responses calls for it (docs: /docs/llm.md, \"Responses on a chat-only upstream\")")
}

func rawSet(v json.RawMessage) bool {
	s := strings.TrimSpace(string(v))
	return s != "" && s != "null"
}

// xlateSent are the request's fields the translation reads; any other is
// advisory to it: not sent, and named as dropped.
var xlateSent = map[string]bool{"model": true, "input": true, "instructions": true, "stream": true, "tools": true, "tool_choice": true,
	"parallel_tool_calls": true, "max_output_tokens": true, "temperature": true, "top_p": true, "user": true, "seed": true,
	"top_logprobs": true, "reasoning": true, "text": true, "truncation": true, "store": true, "background": true}

// responsesToChat rewrites a Responses request as a chat completion for
// upstreamModel. asked is the model as the caller named it.
func responsesToChat(body []byte, asked, upstreamModel string) ([]byte, *xlate, *refusal) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, nil, refuse(http.StatusBadRequest, "xbin_llm_body", "the request body is not a JSON object")
	}
	isSet := func(k string) bool { return rawSet(in[k]) }
	if isSet("previous_response_id") || isSet("conversation") {
		return nil, nil, xlateRefuse("previous_response_id and stored conversations are OpenAI's own service: send the conversation in input")
	}
	if isSet("prompt") {
		return nil, nil, xlateRefuse("prompt (a stored prompt template) is OpenAI's own service: send its text in instructions and input")
	}
	x := &xlate{model: asked, custom: map[string]bool{}, tools: []any{}, parallel: true}
	out := map[string]any{"model": upstreamModel}
	for k := range in {
		if !xlateSent[k] && isSet(k) {
			x.drop(k)
		}
	}
	if isSet("stream") {
		_ = json.Unmarshal(in["stream"], &x.stream)
	}
	if x.stream {
		out["stream"] = true
		out["stream_options"] = map[string]any{"include_usage": true}
	}
	if string(in["store"]) == "true" {
		x.drop("store") // nothing is stored: a later previous_response_id is refused
	}
	if s := ""; json.Unmarshal(in["truncation"], &s) == nil && s == "auto" {
		x.truncate = true // not applied; an overflow is refused in those words (ResponsesViaChat)
		x.drop("truncation")
	}

	var msgs []chatMsg
	if isSet("instructions") {
		var s string
		if json.Unmarshal(in["instructions"], &s) != nil {
			return nil, nil, xlateRefuse("instructions must be a string")
		}
		if s != "" {
			msgs = append(msgs, chatMsg{Role: "system", Content: s})
		}
	}
	more, ref := xlateInput(in["input"])
	if ref != nil {
		return nil, nil, ref
	}
	msgs = append(msgs, more...)
	if len(msgs) == 0 {
		return nil, nil, xlateRefuse("the request has no input")
	}
	out["messages"] = msgs

	tools, ref := xlateTools(in, x)
	if ref != nil {
		return nil, nil, ref
	}
	if len(tools.chat) > 0 {
		out["tools"] = tools.chat
		if tools.choice != nil {
			out["tool_choice"] = tools.choice
		}
		if isSet("parallel_tool_calls") {
			out["parallel_tool_calls"] = in["parallel_tool_calls"]
		}
	}
	if isSet("parallel_tool_calls") {
		_ = json.Unmarshal(in["parallel_tool_calls"], &x.parallel)
	}
	for from, to := range map[string]string{"max_output_tokens": "max_tokens", "temperature": "temperature", "top_p": "top_p",
		"user": "user", "seed": "seed", "top_logprobs": "top_logprobs"} {
		if isSet(from) {
			out[to] = in[from]
		}
	}
	if isSet("top_logprobs") {
		out["logprobs"] = true // chat completions answers top_logprobs only with it
	}
	if isSet("reasoning") {
		var r map[string]json.RawMessage
		if json.Unmarshal(in["reasoning"], &r) != nil {
			return nil, nil, xlateRefuse("reasoning must be an object")
		}
		for k, v := range r {
			var effort string
			switch {
			case !rawSet(v):
			case k == "effort" && json.Unmarshal(v, &effort) == nil && effort != "":
				out["reasoning_effort"] = effort
			default:
				x.drop("reasoning." + k)
			}
		}
	}
	if isSet("text") {
		var t map[string]json.RawMessage
		if json.Unmarshal(in["text"], &t) != nil {
			return nil, nil, xlateRefuse("text must be an object")
		}
		for k, v := range t {
			if !rawSet(v) {
				continue
			}
			if k != "format" {
				x.drop("text." + k)
				continue
			}
			var f map[string]json.RawMessage
			_ = json.Unmarshal(v, &f)
			var typ string
			_ = json.Unmarshal(f["type"], &typ)
			switch typ {
			case "text", "":
			case "json_object":
				out["response_format"] = map[string]any{"type": "json_object"}
			case "json_schema":
				js := map[string]any{}
				for _, k := range []string{"name", "schema", "strict", "description"} {
					if rawSet(f[k]) {
						js[k] = f[k]
					}
				}
				out["response_format"] = map[string]any{"type": "json_schema", "json_schema": js}
			default:
				return nil, nil, xlateRefuse("text.format of type %q can't be asked of a chat completion", typ)
			}
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, nil, refuse(http.StatusBadRequest, "xbin_llm_body", "the request body could not be rewritten for the upstream")
	}
	return b, x, nil
}

// chatTools is a request's tools as chat completions takes them.
type chatTools struct {
	chat   []any
	choice any // tool_choice, nil for the upstream's default
}

// xlateTools maps the request's tools and tool_choice. Function tools pass;
// a custom (free-form) tool becomes a function of one string argument; a
// hosted tool (web_search, local_shell, mcp, image_generation… — OpenAI's
// own service) is dropped, and said, when it is merely offered — and the
// request is refused when the caller requires it.
func xlateTools(in map[string]json.RawMessage, x *xlate) (chatTools, *refusal) {
	var out chatTools
	var names []string // of out.chat, in step
	hosted := map[string]bool{}
	if rawSet(in["tools"]) {
		var tools []map[string]json.RawMessage
		if json.Unmarshal(in["tools"], &tools) != nil {
			return out, xlateRefuse("tools must be a list")
		}
		x.tools = in["tools"]
		for _, t := range tools {
			var typ, name, desc string
			_ = json.Unmarshal(t["type"], &typ)
			_ = json.Unmarshal(t["name"], &name)
			_ = json.Unmarshal(t["description"], &desc)
			fn := map[string]any{"name": name}
			if desc != "" {
				fn["description"] = desc
			}
			switch typ {
			case "function":
				if rawSet(t["parameters"]) {
					fn["parameters"] = t["parameters"]
				}
				if rawSet(t["strict"]) {
					fn["strict"] = t["strict"]
				}
			case "custom": // free-form text in: one string argument
				x.custom[name] = true
				fn["parameters"] = map[string]any{"type": "object", "required": []string{"input"},
					"properties": map[string]any{"input": map[string]any{"type": "string", "description": "the tool's input, as plain text"}}}
			default:
				hosted[typ] = true
				x.drop("tools." + typ)
				continue
			}
			if name == "" {
				return out, xlateRefuse("a %s tool has no name", typ)
			}
			out.chat = append(out.chat, map[string]any{"type": "function", "function": fn})
			names = append(names, name)
		}
	}
	if !rawSet(in["tool_choice"]) {
		return out, nil
	}
	var mode string
	if json.Unmarshal(in["tool_choice"], &mode) == nil {
		switch {
		case mode == "required" && len(out.chat) == 0:
			return out, xlateRefuse("tool_choice: \"required\" with no tool that can be called here (hosted tools are OpenAI's own service)")
		case len(out.chat) > 0:
			out.choice = mode
		}
		return out, nil
	}
	var tc struct {
		Type, Name, Mode string
		Tools            []struct{ Type, Name string }
	}
	if json.Unmarshal(in["tool_choice"], &tc) != nil || tc.Type == "" {
		return out, xlateRefuse("tool_choice must be a string or an object with a type")
	}
	switch tc.Type {
	case "function", "custom":
		if !slices.Contains(names, tc.Name) {
			return out, xlateRefuse("tool_choice names the tool %q, which is not among the request's tools", tc.Name)
		}
		out.choice = map[string]any{"type": "function", "function": map[string]any{"name": tc.Name}}
	case "allowed_tools": // a subset of the tools: only those are offered
		var keep []any
		for i, n := range names {
			if slices.ContainsFunc(tc.Tools, func(a struct{ Type, Name string }) bool {
				return (a.Type == "function" || a.Type == "custom") && a.Name == n
			}) {
				keep = append(keep, out.chat[i])
			}
		}
		if len(keep) == 0 && tc.Mode == "required" {
			return out, xlateRefuse("tool_choice requires one of its allowed_tools, and none of them can be called here (hosted tools are OpenAI's own service)")
		}
		out.chat = keep
		if tc.Mode != "" && len(keep) > 0 {
			out.choice = tc.Mode
		}
	default:
		return out, xlateRefuse("tool_choice requires the hosted tool %q, which is OpenAI's own service", tc.Type)
	}
	return out, nil
}

// xlateInput turns Responses input — a string, or a list of items — into
// chat messages. Function calls that follow one another (and the assistant
// text before them) become one assistant message, as chat wants them; the
// images of a tool's output, which a chat tool message can't carry, follow
// the tool messages in a user message that says whose they are.
func xlateInput(raw json.RawMessage) ([]chatMsg, *refusal) {
	if !rawSet(raw) {
		return nil, nil
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return []chatMsg{{Role: "user", Content: str}}, nil
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil, xlateRefuse("input must be a string or a list of items")
	}
	var out []chatMsg
	var open *chatMsg // an assistant message still taking tool calls
	var shown []any   // the images of the tool outputs just written, waiting for the run of tool messages to end
	flush := func() {
		if open != nil {
			out = append(out, *open)
			open = nil
		}
	}
	show := func() {
		if len(shown) > 0 {
			out = append(out, chatMsg{Role: "user", Content: shown})
			shown = nil
		}
	}
	str1 := func(m map[string]json.RawMessage, k string) string {
		var s string
		_ = json.Unmarshal(m[k], &s)
		return s
	}
	call := func(id, name, args string) {
		if open == nil {
			open = &chatMsg{Role: "assistant", Content: nil}
		}
		tc := chatToolCall{ID: id, Type: "function"}
		tc.Function.Name, tc.Function.Arguments = name, args
		open.ToolCalls = append(open.ToolCalls, tc)
	}
	for _, it := range items {
		typ := str1(it, "type")
		if typ == "" && len(it["role"]) > 0 {
			typ = "message"
		}
		if typ != "function_call_output" && typ != "custom_tool_call_output" {
			show()
		}
		switch typ {
		case "message":
			role := str1(it, "role")
			if role == "developer" {
				role = "system"
			}
			content, ref := xlateContent(it["content"], role)
			if ref != nil {
				return nil, ref
			}
			flush()
			if role == "assistant" {
				open = &chatMsg{Role: role, Content: content}
			} else {
				out = append(out, chatMsg{Role: role, Content: content})
			}
		case "function_call":
			call(str1(it, "call_id"), str1(it, "name"), str1(it, "arguments"))
		case "custom_tool_call":
			args, _ := json.Marshal(map[string]string{"input": str1(it, "input")})
			call(str1(it, "call_id"), str1(it, "name"), string(args))
		case "function_call_output", "custom_tool_call_output":
			flush()
			id := str1(it, "call_id")
			text, images, ref := xlateOutput(it["output"])
			if ref != nil {
				return nil, ref
			}
			if len(images) > 0 {
				note := fmt.Sprintf("(what the tool call %s returned as an image)", id)
				if text == "" {
					text = "(an image: it follows)"
				}
				shown = append(append(shown, map[string]any{"type": "text", "text": note}), images...)
			}
			out = append(out, chatMsg{Role: "tool", ToolCallID: id, Content: text})
		case "reasoning":
			// the provider's own, opaque: not this upstream's to read
		default:
			return nil, xlateRefuse("an input item of type %q can't be sent as a chat completion", typ)
		}
	}
	flush()
	show()
	return out, nil
}

// xlateImage is an input_image part as chat's image_url part.
func xlateImage(p map[string]json.RawMessage) (any, *refusal) {
	var u string
	if json.Unmarshal(p["image_url"], &u) != nil || u == "" {
		return nil, xlateRefuse("an input_image without an image_url (a file id is OpenAI's own service)")
	}
	img := map[string]any{"url": u}
	var detail string
	if json.Unmarshal(p["detail"], &detail) == nil && detail != "" {
		img["detail"] = detail
	}
	return map[string]any{"type": "image_url", "image_url": img}, nil
}

// xlateContent is a message's content for chat: a string where it is only
// text, parts where it has images.
func xlateContent(raw json.RawMessage, role string) (any, *refusal) {
	if !rawSet(raw) {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return nil, xlateRefuse("a message's content must be a string or a list of parts: what else it holds would be lost")
	}
	var text []string
	var out []any
	images := false
	for _, p := range parts {
		var typ, t string
		_ = json.Unmarshal(p["type"], &typ)
		switch typ {
		case "input_text", "output_text", "text", "refusal", "summary_text":
			if json.Unmarshal(p["text"], &t) != nil {
				_ = json.Unmarshal(p["refusal"], &t)
			}
			text = append(text, t)
			out = append(out, map[string]any{"type": "text", "text": t})
		case "input_image":
			if role != "user" {
				return nil, xlateRefuse("an image in a message of role %q: chat completions takes images in user messages only", role)
			}
			img, ref := xlateImage(p)
			if ref != nil {
				return nil, ref
			}
			images = true
			out = append(out, img)
		default:
			return nil, xlateRefuse("a content part of type %q can't be sent as a chat completion", typ)
		}
	}
	if !images {
		return strings.Join(text, ""), nil
	}
	return out, nil
}

// xlateOutput is a tool's output as chat carries it: its text for the tool
// message, and its images apart (xlateInput shows them after it).
func xlateOutput(raw json.RawMessage) (text string, images []any, ref *refusal) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil, nil
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return string(raw), nil, nil // some other JSON: carried as its text
	}
	var b strings.Builder
	for _, p := range parts {
		var typ, t string
		_ = json.Unmarshal(p["type"], &typ)
		switch typ {
		case "input_text", "output_text", "text":
			_ = json.Unmarshal(p["text"], &t)
			b.WriteString(t)
		case "input_image":
			img, ref := xlateImage(p)
			if ref != nil {
				return "", nil, ref
			}
			images = append(images, img)
		default:
			return "", nil, xlateRefuse("a tool output's part of type %q (input_file and the like) can't be sent as a chat completion", typ)
		}
	}
	return b.String(), images, nil
}
