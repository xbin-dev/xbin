package main

// llm_wire_golden_test.go — the two model wires, pinned byte for byte:
// what each request encodes to, and what each recorded answer (a stream or
// a body, as providers send them, their oddities included) is read as — the
// reply, the events on the way, the error. The expectations are
// testdata/llm_wire_golden.json; they were recorded before the wire types
// moved to the SDK (sdk/llm, D226) and held across the move.
//
// Regenerate the file after a deliberate change (review the diff):
//
//	LLM_WIRE_GOLDEN_OUT=$PWD/builtin-templates/agent/_backend/testdata/llm_wire_golden.json \
//	  TILE_TEST_FLAGS="-count=1 -run TestLLMWireGolden" ./hack/tile-check.sh agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// goldenRead is what reading one answer gave.
type goldenRead struct {
	Reply        LLMReply `json:"reply"`
	ToolCallsNil bool     `json:"toolCallsNil"`
	Events       []string `json:"events,omitempty"`
	Err          string   `json:"err,omitempty"`
	// Final: the upstream said so inside the stream (never retried).
	Final bool `json:"final,omitempty"`
}

func goldenOf(rep LLMReply, evs []LLMEvent, err error) goldenRead {
	g := goldenRead{Reply: rep, ToolCallsNil: rep.Msg.ToolCalls == nil}
	for _, e := range evs {
		switch e.Kind {
		case "tool":
			g.Events = append(g.Events, fmt.Sprintf("tool|%d|%s|%s|%s", e.Index, e.ID, e.Name, e.Args))
		default:
			g.Events = append(g.Events, e.Kind+"|"+e.Text)
		}
	}
	if err != nil {
		g.Err = err.Error()
		var se *streamError
		g.Final = errors.As(err, &se)
	}
	return g
}

// dataLines joins stream lines the way a compliant server writes them.
func dataLines(data ...string) string {
	var b strings.Builder
	for _, d := range data {
		b.WriteString("data: " + d + "\n\n")
	}
	return b.String()
}

var goldenChatStreams = map[string]string{
	"openai": dataLines(
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4.1","choices":[{"index":0,"delta":{"role":"assistant","content":"","refusal":null},"logprobs":null,"finish_reason":null}],"usage":null}`,
		`{"id":"chatcmpl-1","model":"gpt-4.1","choices":[{"index":0,"delta":{"content":"The \"usage\" field"},"finish_reason":null}],"usage":null}`,
		`{"id":"chatcmpl-1","model":"gpt-4.1","choices":[{"index":0,"delta":{"content":" is { reported last."},"finish_reason":null}],"usage":null}`,
		`{"id":"chatcmpl-1","model":"gpt-4.1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":null}`,
		`{"id":"chatcmpl-1","model":"gpt-4.1","choices":[],"usage":{"prompt_tokens":2048,"completion_tokens":310,"total_tokens":2358,"prompt_tokens_details":{"cached_tokens":1024,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":192,"audio_tokens":0}}}`,
		`[DONE]`),
	"reasoning-three-names": dataLines(
		`{"choices":[{"delta":{"reasoning_content":"Let me "}}]}`,
		`{"choices":[{"delta":{"reasoning":"think. ","content":null}}]}`,
		`{"choices":[{"delta":{"reasoning":null,"reasoning_details":[{"type":"reasoning.text","index":0,"text":"Step one"}]}}]}`,
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","index":0,"text":", step two","signature":"sig"},{"type":"reasoning.encrypted","index":1,"data":"AAA"}]}}]}`,
		`{"choices":[{"delta":{"content":"Done."},"finish_reason":"stop"}]}`,
		`[DONE]`),
	"details-only": dataLines(
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.summary","index":0,"summary":"Plan: "}]}}]}`,
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.summary","index":0,"summary":"answer."}]}}]}`,
		`{"choices":[{"delta":{"content":"42"}}]}`),
	"think-tags": dataLines(
		`{"choices":[{"delta":{"content":"<thi"}}]}`,
		`{"choices":[{"delta":{"content":"nk>pondering</th"}}]}`,
		`{"choices":[{"delta":{"content":"ink>\n\nThe answer."}}]}`),
	"tool-calls": dataLines(
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"note","arguments":""}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"text\":"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}},{"index":1,"function":{"arguments":"\"x\"}"}}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"completion_tokens_details":{"reasoning_tokens":9}}}`,
		`[DONE]`),
	"tool-calls-no-finish": dataLines(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"note","arguments":"{}"}}]}}]}`),
	"finish-length": dataLines(
		`{"choices":[{"delta":{"content":"cut"},"finish_reason":"length"}]}`, `[DONE]`),
	// usage as providers vary it: no total; reasoning beyond the completion
	// count (Gemini counts thoughts apart); DeepSeek's and OpenRouter's extras
	"usage-no-total":         dataLines(`{"choices":[{"delta":{"content":"a"}}]}`, `{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":6}}`),
	"usage-reasoning-apart":  dataLines(`{"choices":[{"delta":{"content":"a"}}]}`, `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":114,"completion_tokens_details":{"reasoning_tokens":100}}}`),
	"usage-extras":           dataLines(`{"choices":[{"delta":{"content":"a"}}]}`, `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14,"prompt_cache_hit_tokens":8,"prompt_cache_miss_tokens":2,"cost":0.00012,"cost_details":{"upstream_inference_cost":null},"prompt_tokens_details":null,"completion_tokens_details":{"reasoning_tokens":0}}}`),
	"usage-every-chunk":      dataLines(`{"choices":[{"delta":{"content":"a"}}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`, `{"choices":[{"delta":{"content":"b"}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`),
	"usage-on-content-chunk": dataLines(`{"choices":[{"delta":{"content":"a"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`),
	// errors inside a stream, in the shapes servers send them
	"error-object":          dataLines(`{"choices":[{"delta":{"content":"par"}}]}`, `{"error":{"message":"model overloaded","type":"overloaded_error","code":529}}`),
	"error-string":          dataLines(`{"error":"boom"}`),
	"error-without-message": dataLines(`{"error":{"code":"rate_limited","type":"x"}}`),
	"error-null":            dataLines(`{"error":null,"choices":[{"delta":{"content":"fine"}}]}`),
	// framing: no blank lines, CRLF, padded lines, comments, event lines, a
	// last line without its newline, payloads that aren't JSON
	"frame-no-blank-lines": "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\ndata: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\ndata: [DONE]\ndata: {\"choices\":[{\"delta\":{\"content\":\"never\"}}]}\n",
	"frame-crlf":           ": keep-alive\r\n\r\nevent: message\r\ndata: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\r\n\r\ndata:{\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\r\n\r\ndata: [DONE]\r\n\r\n",
	"frame-padded":         "  data:   {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}  \n\n\tdata: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\ndata:\n\nid: 3\nretry: 5\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"c\"}}]}",
	"frame-not-json":       "data: ping\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: {broken\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\n",
	"frame-empty":          "",
	// a delta whose content is a parts array (Mistral's thinking chunks) says nothing here
	"content-parts-delta": dataLines(`{"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"hm"}]}]}}]}`, `{"choices":[{"delta":{"content":"said"}}]}`),
}

var goldenChatBodies = map[string]string{
	"plain":           `{"id":"c1","object":"chat.completion","model":"gpt-4.1","choices":[{"index":0,"message":{"role":"assistant","content":"Hello.","refusal":null},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":64},"completion_tokens_details":{"reasoning_tokens":7}}}`,
	"tool-calls":      `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","function":{"name":"note","arguments":"{\"text\":\"x\"}"}},{"id":"c2","type":"function","function":{"name":"bash","arguments":"{}"}}]}}],"usage":{"prompt_tokens":5,"completion_tokens":6}}`,
	"tool-calls-none": `{"choices":[{"message":{"role":"assistant","content":"hi","tool_calls":[]},"finish_reason":"stop"}]}`,
	"content-parts":   `{"choices":[{"message":{"role":"assistant","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]},"finish_reason":"stop"}]}`,
	"reasoning":       `{"choices":[{"message":{"role":"assistant","content":"<think>inner</think>Out.","reasoning_content":"A","reasoning":"B","reasoning_details":[{"type":"reasoning.text","text":"C","index":0}]},"finish_reason":"stop"}]}`,
	"details-only":    `{"choices":[{"message":{"content":"Out.","reasoning":null,"reasoning_details":[{"type":"reasoning.summary","summary":"S","index":0},{"type":"reasoning.encrypted","data":"E","index":1}]}}]}`,
	"length":          `{"choices":[{"message":{"content":"cut"},"finish_reason":"length"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"completion_tokens_details":{"reasoning_tokens":9}}}`,
	"error-object":    `{"error":{"message":"no such model","type":"invalid_request_error","param":null,"code":"model_not_found"}}`,
	"error-string":    `{"error":"boom"}`,
	"error-odd":       `{"error":{"code":500}}`,
	"no-choices":      `{"choices":[]}`,
	"not-json":        `<html>bad gateway</html>`,
}

var goldenResponsesStreams = map[string]string{
	"openai": "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"gpt-5\",\"output\":[],\"usage\":null}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"summary\":[]}}\n\n" +
		"event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"item_id\":\"rs_1\",\"output_index\":0,\"summary_index\":0,\"delta\":\"Count\"}\n\n" +
		"event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"item_id\":\"rs_1\",\"output_index\":0,\"summary_index\":0,\"delta\":\"ing.\"}\n\n" +
		"event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"item_id\":\"rs_1\",\"output_index\":0,\"summary_index\":1,\"delta\":\"Then answer.\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"encrypted_content\":\"ENC\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"Counting.\"},{\"type\":\"summary_text\",\"text\":\"Then answer.\"}]}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":1,\"content_index\":0,\"delta\":\"Counted \"}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":1,\"content_index\":0,\"delta\":\"once.\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Counted once.\"}]}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":2,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"note\",\"arguments\":\"\"}}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"output_index\":2,\"delta\":\"{\\\"text\\\":\"}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"output_index\":2,\"delta\":\"\\\"x\\\"}\"}\n\n" +
		"event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fc_1\",\"output_index\":2,\"arguments\":\"{\\\"text\\\":\\\"x\\\"}\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":2,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"note\",\"arguments\":\"{\\\"text\\\":\\\"x\\\"}\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5\",\"output\":[],\"usage\":{\"input_tokens\":4096,\"input_tokens_details\":{\"cached_tokens\":3072},\"output_tokens\":640,\"output_tokens_details\":{\"reasoning_tokens\":512},\"total_tokens\":4736}}}\n\n",
	"usage-no-total":  dataLines(`{"type":"response.output_text.delta","delta":"a"}`, `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":100,"output_tokens":20,"output_tokens_details":{"reasoning_tokens":7}}}}`),
	"usage-odd-total": dataLines(`{"type":"response.output_text.delta","delta":"a"}`, `{"type":"response.completed","response":{"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":50,"output_tokens_details":{"reasoning_tokens":70}}}}`),
	"no-usage":        dataLines(`{"type":"response.output_text.delta","delta":"a"}`, `{"type":"response.completed","response":{"status":"completed","usage":null}}`),
	"incomplete":      dataLines(`{"type":"response.output_text.delta","delta":"cut"}`, `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`),
	"reasoning-text":  dataLines(`{"type":"response.reasoning_text.delta","output_index":0,"content_index":0,"delta":"raw "}`, `{"type":"response.reasoning_text.delta","output_index":0,"content_index":1,"delta":"thoughts"}`, `{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"summary"}`, `{"type":"response.output_text.delta","delta":"ok"}`),
	// the item carries what no delta did: a call's arguments, a message's text; a summary is not repeated
	"items-only": dataLines(
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"unseen"}]}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","content":[{"type":"output_text","text":"whole "},{"type":"refusal","text":"no"},{"type":"output_text","text":"text"}]}}`,
		`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","call_id":"call_9","name":"bash","arguments":"{\"command\":\"ls\"}"}}`,
		`{"type":"response.output_item.done","output_index":3,"item":"not an object"}`),
	"arguments-done-wins":  dataLines(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"c","name":"n","arguments":"{\"a\""}}`, `{"type":"response.function_call_arguments.delta","output_index":0,"delta":":1"}`, `{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"a\":2}"}`, `{"type":"response.function_call_arguments.done","output_index":0}`),
	"failed-with-error":    dataLines(`{"type":"response.output_text.delta","delta":"par"}`, `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"The model failed."}}}`),
	"failed-bare":          dataLines(`{"type":"response.failed"}`),
	"failed-null-error":    dataLines(`{"type":"response.failed","response":{"status":"failed","error":null}}`),
	"failed-odd-error":     dataLines(`{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error"}}}`),
	"error-event":          dataLines(`{"type":"error","code":"rate_limit_exceeded","message":"Slow down.","param":null}`),
	"error-event-bare":     dataLines(`{"type":"error","code":429}`),
	"unknown-events":       dataLines(`{"type":"response.in_progress"}`, `{"type":"response.content_part.added","part":{"type":"output_text","text":""}}`, `{"type":"response.output_text.delta","delta":"a"}`, `{"type":"response.output_text.done","text":"a"}`, `not json`),
	"frame-no-blank-lines": "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"a\"}\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"b\"}\n",
}

var goldenResponsesBodies = map[string]string{
	"completed": `{"id":"resp_1","object":"response","status":"completed","model":"gpt-5","output":[` +
		`{"type":"reasoning","id":"rs_1","encrypted_content":"ENC","summary":[{"type":"summary_text","text":"First."},{"type":"summary_text","text":"Second."}]},` +
		`{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Hello."}]},` +
		`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"note","arguments":"{\"text\":\"x\"}"},` +
		`{"type":"reasoning","id":"rs_2","summary":[{"type":"summary_text","text":"Third."}]}],` +
		`"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":64},"output_tokens":20,"output_tokens_details":{"reasoning_tokens":7},"total_tokens":120}}`,
	"no-total":     `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"a"}]}],"usage":{"input_tokens":7,"output_tokens":8}}`,
	"incomplete":   `{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[{"type":"message","content":[{"type":"output_text","text":"cut"}]}]}`,
	"failed":       `{"status":"failed","output":[]}`,
	"error-object": `{"status":"failed","error":{"code":"server_error","message":"The model failed."},"output":[]}`,
	"error-string": `{"error":"boom"}`,
	"empty-call":   `{"status":"completed","output":[{"type":"function_call","call_id":"c","name":"n","arguments":""}]}`,
	"not-json":     `nope`,
}

// goldenMsgs is a conversation with every kind of message the engine sends.
func goldenMsgs() []wireMsg {
	tc := func(id, typ, name, args string) toolCall {
		c := toolCall{ID: id, Type: typ}
		c.Function.Name, c.Function.Arguments = name, args
		return c
	}
	parts := `[{"type":"text","text":"look <&>"},{"type":"image_url","image_url":{"url":"data:image/png;base64,xx"}}]`
	return []wireMsg{
		{Role: "system", Content: "You are an agent."},
		{Role: "user", Content: "hello"},
		{Role: "system", Content: "A later system note."},
		{Role: "user", Content: contentValue(parts)},
		{Role: "user", Content: contentValue(`[#1 user] not parts`), Name: "alice"},
		{Role: "assistant", Content: "calling", ToolCalls: []toolCall{tc("call_1", "function", "note", `{"text":"x"}`), tc("call_2", "", "bash", `{}`)},
			Replay: &msgMeta{Wire: "chat", Model: "kimi-k3", Reasoning: "shown only", ReasoningRaw: json.RawMessage(`[{"type":"reasoning.text","text":"T","index":0}]`)}},
		{Role: "tool", ToolCallID: "call_1", Name: "note", Content: "noted"},
		{Role: "tool", ToolCallID: "call_2", Content: ""},
		{Role: "assistant", Content: "", ToolCalls: []toolCall{tc("call_3", "function", "bash", `{"command":"ls"}`)},
			Replay: &msgMeta{Wire: "responses", Model: "gpt-5", ReasoningRaw: json.RawMessage(`[{"type":"reasoning","id":"rs_1","encrypted_content":"E","summary":[]}]`)}},
		{Role: "tool", ToolCallID: "call_3", Content: json.RawMessage(`{"files":["a"]}`)},
		{Role: "assistant", ToolCalls: []toolCall{tc("call_4", "function", "note", `{}`)}},
		{Role: "tool", ToolCallID: "call_4", Content: "ok"},
		{Role: "assistant", Content: "Done."},
	}
}

func goldenTools() []toolSpec {
	return []toolSpec{
		{Type: "function", Function: funcDef{Name: "note", Description: "Jot a note.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}}}},
		{Type: "function", Function: funcDef{Name: "bare", Parameters: map[string]any{"type": "object"}}},
		{Type: "function", Function: funcDef{Name: "nil-schema"}},
	}
}

var goldenRequests = map[string]LLMRequest{
	"chat":                {Model: "kimi-k3", Stream: true, ReasoningEffort: "high"},
	"chat-other-model":    {Model: "gpt-4.1"},
	"chat-no-tools":       {Model: "m", Stream: true},
	"responses":           {Model: "gpt-5", Stream: true, ReasoningEffort: "low"},
	"responses-plain":     {Model: "gpt-4.1"},
	"responses-reasoning": {Model: "openai/o4-mini"},
}

const goldenModelList = `{"object":"list","data":[
 {"id":"gpt-5","object":"model","created":1700000000,"owned_by":"openai"},
 {"id":"claude","max_input_tokens":200000,"context_length":1},
 {"id":"or","context_length":128000,"top_provider":{"context_length":64000}},
 {"id":"or-top","top_provider":{"context_length":64000,"max_completion_tokens":8000}},
 {"id":"groq","context_window":131072},
 {"id":"vllm","max_model_len":32768},
 {"id":"lmstudio","max_context_length":"8192"},
 {"id":"gemini","inputTokenLimit":1048576.0},
 {"id":"ours","contextWindow":4096,"provider":"apps/llm-gw","ref":"apps/llm-gw|ours","backend":"default"},
 {"id":"fast","object":"model","owned_by":"alias","alias_of":"default/gpt-5"},
 {"id":"zero","context_length":0,"context_window":-5,"max_model_len":null},
 {"id":"odd","owned_by":7,"alias_of":null,"context_length":"lots","top_provider":"x"},
 {"id":42,"context_length":10},
 {"owned_by":"nobody"}
]}`

// goldenRun produces every case's output, keyed by name.
func goldenRun(t *testing.T) map[string]any {
	out := map[string]any{}
	read := func(c wireCodec, in string) goldenRead {
		var evs []LLMEvent
		rep, err := c.parseStream(strings.NewReader(in), func(e LLMEvent) { evs = append(evs, e) })
		return goldenOf(rep, evs, err)
	}
	for name, in := range goldenChatStreams {
		out["chat-stream/"+name] = read(chatCodec{}, in)
		// the same stream a byte at a time: how it is cut into reads must not matter
		if one := read(chatCodec{}, in); fmt.Sprint(one) != fmt.Sprint(goldenOfSlow(chatCodec{}, in)) {
			t.Errorf("chat stream %s reads differently a byte at a time", name)
		}
	}
	for name, in := range goldenResponsesStreams {
		out["responses-stream/"+name] = read(responsesCodec{}, in)
		if one := read(responsesCodec{}, in); fmt.Sprint(one) != fmt.Sprint(goldenOfSlow(responsesCodec{}, in)) {
			t.Errorf("responses stream %s reads differently a byte at a time", name)
		}
	}
	for name, in := range goldenChatBodies {
		rep, err := chatCodec{}.parseBody([]byte(in))
		out["chat-body/"+name] = goldenOf(rep, nil, err)
	}
	for name, in := range goldenResponsesBodies {
		rep, err := responsesCodec{}.parseBody([]byte(in))
		out["responses-body/"+name] = goldenOf(rep, nil, err)
	}
	for name, req := range goldenRequests {
		var c wireCodec = chatCodec{}
		if strings.HasPrefix(name, "responses") {
			c = responsesCodec{}
		}
		req.Msgs = goldenMsgs()
		if name != "chat-no-tools" {
			req.Tools = goldenTools()
		}
		b, err := c.encode(req)
		if err != nil {
			t.Fatalf("encode %s: %v", name, err)
		}
		out["request/"+name] = string(b)
	}
	var list struct {
		Data []catalogModel `json:"data"`
	}
	if err := json.Unmarshal([]byte(goldenModelList), &list); err != nil {
		t.Fatal(err)
	}
	out["models"] = list.Data
	out["paths"] = chatCodec{}.path() + " " + responsesCodec{}.path()
	return out
}

// oneByte reads its source a byte per Read.
type oneByte struct{ r io.Reader }

func (o oneByte) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return o.r.Read(p[:1])
}

func goldenOfSlow(c wireCodec, in string) goldenRead {
	var evs []LLMEvent
	rep, err := c.parseStream(oneByte{strings.NewReader(in)}, func(e LLMEvent) { evs = append(evs, e) })
	return goldenOf(rep, evs, err)
}

func TestLLMWireGolden(t *testing.T) {
	got, err := json.MarshalIndent(goldenRun(t), "", " ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if path := os.Getenv("LLM_WIRE_GOLDEN_OUT"); path != "" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile("testdata/llm_wire_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == string(want) {
		return
	}
	var g, w map[string]json.RawMessage
	_ = json.Unmarshal(got, &g)
	_ = json.Unmarshal(want, &w)
	for name := range g {
		if string(g[name]) != string(w[name]) {
			t.Errorf("%s:\n got %s\nwant %s", name, g[name], w[name])
		}
	}
	for name := range w {
		if _, ok := g[name]; !ok {
			t.Errorf("%s: in the golden file, no longer produced", name)
		}
	}
	if !t.Failed() {
		t.Error("testdata/llm_wire_golden.json differs in its formatting: regenerate it")
	}
}

// A stream line past 8 MiB ends the read with an error that is not the
// upstream's own (so the call is retried), and one just under it is read.
func TestLLMWireLineCap(t *testing.T) {
	big := strings.Repeat("x", 7<<20)
	rep, err := chatCodec{}.parseStream(strings.NewReader(`data: {"choices":[{"delta":{"content":"`+big+`"}}]}`+"\n\n"), nil)
	if err != nil || len(asString(rep.Msg.Content)) != len(big) {
		t.Fatalf("a 7 MiB line: %v, %d bytes", err, len(asString(rep.Msg.Content)))
	}
	_, err = chatCodec{}.parseStream(strings.NewReader("data: "+strings.Repeat("x", 9<<20)), nil)
	var se *streamError
	if err == nil || err.Error() != "sse line too long" || errors.As(err, &se) {
		t.Fatalf("a 9 MiB line: %v", err)
	}
}
