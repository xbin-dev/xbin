// fakeopenai is a scripted OpenAI-compatible upstream for the UI harness: the
// agent template reaches it through the llm-gw tile, so a harness pass can
// drive real turns — streamed thinking, tool calls with summaries, subagents,
// slow turns to steer — without a real model. It speaks both wires the agent
// uses: Chat Completions (reasoning as delta.reasoning_content) for "fake-chat",
// and the Responses API (reasoning summaries) for "gpt-5-fake".
//
// The script is chosen by words in the last user message (lower-cased):
//
//	hello        thinking "Considering the greeting…", then "Hello from the fake model."
//	use a tool   a note call with summary "Jot down a quick note" → "Noted it."
//	make a file  file_write note.txt, then attach_to_reply it (a chat channel's
//	             reply files, D86) → "Here is the file."
//	delegate     subagent_spawn {task:"count to three", label:"counter"} → the
//	             subagent thinks and answers "one, two, three" → "The helper counted."
//	slow tool    2.5 s, then a note "Take a slow note"; a later steer is answered
//	steer…       "Got your steer: <text>"
//	fan out      three background subagent_spawn (slow jobs, 6 s each) → "Started three helpers."
//	quick        "Quick answer."
//	restart me   the FIRST request of that turn hangs 30 s (the harness restarts
//	             the agent's backend under it), a re-issue answers at once:
//	             a note "Survive a restart" → "Noted it."
//	(else)       "ok: <text>"
//
// The agent naming a conversation (its title prompt) gets "Titled <the first
// three words of the first message>".
//
// A subagent's task (its first user message) drives it the same way:
// "count…" → thinking + "one, two, three"; "slow job…" → 6 s, then "slow job done".
//
// GET /debug/requests lists every model request (start/end in unix ms, the
// last user text, whether the caller hung up first) so a pass can measure,
// e.g., how long a restarted backend took to re-issue its call.
//
//	go run ./hack/fakeopenai -addr 127.0.0.1:18977
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var callSeq, callIDs atomic.Int64

// reqRec is one model request, for GET /debug/requests.
type reqRec struct {
	ID       int64  `json:"id"`
	Wire     string `json:"wire"`
	Model    string `json:"model"`
	Last     string `json:"last"` // the last user text
	Sub      bool   `json:"sub"`  // a subagent's request
	Start    int64  `json:"start"`
	End      int64  `json:"end"`
	Canceled bool   `json:"canceled"` // the caller hung up before the answer
}

var (
	recMu sync.Mutex
	recs  []*reqRec
	seen  = map[string]bool{} // "restart me" turns already asked once
)

func record(wire, model string, conv []turn, system string) *reqRec {
	r := &reqRec{ID: callSeq.Add(1), Wire: wire, Model: model, Sub: strings.Contains(system, "You are a subagent"), Start: time.Now().UnixMilli()}
	for i := len(conv) - 1; i >= 0; i-- {
		if conv[i].Role == "user" {
			r.Last = conv[i].Text
			break
		}
	}
	recMu.Lock()
	recs = append(recs, r)
	recMu.Unlock()
	log.Printf("#%d %s %s sub=%v last=%q", r.ID, wire, model, r.Sub, clip(r.Last, 60))
	return r
}

func (r *reqRec) done(canceled bool) {
	recMu.Lock()
	r.End, r.Canceled = time.Now().UnixMilli(), canceled
	recMu.Unlock()
}

// wait sleeps d, or less if the caller hangs up (false then).
func wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

type call struct {
	Name string
	Args map[string]any
}

type plan struct {
	Delay    time.Duration
	Thinking []string
	Text     string
	Calls    []call
}

// turn is one message of the conversation, wire-independent.
type turn struct {
	Role, Text, Tool string // Tool: for a tool result, the name of the call it answers
}

func script(conv []turn, system string) plan {
	// the agent naming a conversation (title.go): "Titled <first words>"
	if strings.Contains(system, "Name this conversation") {
		first := conv[len(conv)-1].Text
		if _, rest, ok := strings.Cut(first, "First message:\n"); ok {
			first = strings.SplitN(rest, "\n", 2)[0]
		}
		words := strings.Fields(first)
		if len(words) > 3 {
			words = words[:3]
		}
		return plan{Text: "Titled " + strings.Join(words, " ")}
	}
	sub := strings.Contains(system, "You are a subagent")
	last := conv[len(conv)-1]
	lastUser := ""
	for i := len(conv) - 1; i >= 0; i-- {
		if conv[i].Role == "user" {
			lastUser = strings.ToLower(conv[i].Text)
			break
		}
	}
	if sub {
		task := ""
		for _, t := range conv {
			if t.Role == "user" {
				task = strings.ToLower(t.Text)
				break
			}
		}
		switch {
		case strings.Contains(task, "count"):
			return plan{Thinking: []string{"Counting ", "carefully…"}, Text: "one, two, three"}
		case strings.Contains(task, "slow job"):
			return plan{Delay: 6 * time.Second, Text: "slow job done"}
		}
		return plan{Text: "subagent: " + task}
	}
	if last.Role == "tool" {
		switch last.Tool {
		case "note":
			return plan{Text: "Noted it."}
		case "file_write":
			return plan{Calls: []call{{"attach_to_reply", map[string]any{"paths": []string{"note.txt"}, "summary": "Attach the file"}}}}
		case "attach_to_reply":
			return plan{Text: "Here is the file."}
		case "subagent_spawn", "spawn_subagent":
			if strings.Contains(last.Text, "background") {
				return plan{Text: "Started three helpers."}
			}
			return plan{Text: "The helper counted."}
		}
		return plan{Text: "done with " + last.Tool}
	}
	switch {
	case strings.Contains(lastUser, "steer"):
		return plan{Text: "Got your steer: " + lastUser}
	case strings.Contains(lastUser, "hello"):
		return plan{Thinking: []string{"Considering ", "the ", "greeting…"}, Text: "Hello from the fake model."}
	case strings.Contains(lastUser, "make a file"):
		return plan{Calls: []call{{"file_write", map[string]any{"path": "note.txt", "content": "made by the fake model", "summary": "Write the file"}}}}
	case strings.Contains(lastUser, "use a tool"):
		return plan{Calls: []call{{"note", map[string]any{"text": "checked", "summary": "Jot down a quick note"}}}}
	case strings.Contains(lastUser, "delegate"):
		return plan{Calls: []call{{"subagent_spawn", map[string]any{"task": "count to three", "label": "counter", "summary": "Ask a helper to count"}}}}
	case strings.Contains(lastUser, "slow tool"):
		return plan{Delay: 2500 * time.Millisecond, Calls: []call{{"note", map[string]any{"text": "slow", "summary": "Take a slow note"}}}}
	case strings.Contains(lastUser, "fan out"):
		var cs []call
		for i := 1; i <= 3; i++ {
			cs = append(cs, call{"subagent_spawn", map[string]any{"task": fmt.Sprintf("slow job %d", i), "wait": false, "label": fmt.Sprintf("helper %d", i), "summary": fmt.Sprintf("Start helper %d", i)}})
		}
		return plan{Calls: cs}
	case strings.Contains(lastUser, "quick"):
		return plan{Text: "Quick answer."}
	case strings.Contains(lastUser, "restart me"):
		key := fmt.Sprintf("%d:%s", len(conv), lastUser)
		recMu.Lock()
		again := seen[key]
		seen[key] = true
		recMu.Unlock()
		d := 30 * time.Second
		if again {
			d = 200 * time.Millisecond
		}
		return plan{Delay: d, Calls: []call{{"note", map[string]any{"text": "restarted", "summary": "Survive a restart"}}}}
	}
	return plan{Text: "ok: " + lastUser}
}

// --- Chat Completions -------------------------------------------------------------

func chatCompletions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			Name       string          `json:"name"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Messages) == 0 {
		http.Error(w, `{"error":{"message":"bad request"}}`, 400)
		return
	}
	names := map[string]string{}
	var conv []turn
	system := ""
	for _, m := range req.Messages {
		text := contentText(m.Content)
		switch m.Role {
		case "system":
			system += text
			continue
		case "assistant":
			for _, c := range m.ToolCalls {
				names[c.ID] = c.Function.Name
			}
		}
		conv = append(conv, turn{Role: m.Role, Text: text, Tool: names[m.ToolCallID]})
	}
	p := script(conv, system)
	rec := record("chat", req.Model, conv, system)
	if !wait(r.Context(), p.Delay) {
		rec.done(true)
		return
	}
	defer rec.done(false)
	if !req.Stream {
		msg := map[string]any{"role": "assistant", "content": p.Text}
		if len(p.Thinking) > 0 {
			msg["reasoning_content"] = strings.Join(p.Thinking, "")
		}
		if len(p.Calls) > 0 {
			var tcs []map[string]any
			for _, c := range p.Calls {
				b, _ := json.Marshal(c.Args)
				tcs = append(tcs, map[string]any{"id": fmt.Sprintf("call_%d", callIDs.Add(1)), "type": "function",
					"function": map[string]any{"name": c.Name, "arguments": string(b)}})
			}
			msg["tool_calls"] = tcs
		}
		writeJSON(w, map[string]any{"choices": []any{map[string]any{"message": msg, "finish_reason": finish(p)}},
			"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120}})
		return
	}
	sse := startSSE(w)
	chunk := func(delta map[string]any, fin any) {
		sse(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": fin}}})
	}
	for _, t := range p.Thinking {
		chunk(map[string]any{"reasoning_content": t}, nil)
		time.Sleep(150 * time.Millisecond)
	}
	for _, word := range words(p.Text) {
		chunk(map[string]any{"content": word}, nil)
		time.Sleep(40 * time.Millisecond)
	}
	for i, c := range p.Calls {
		b, _ := json.Marshal(c.Args)
		args := string(b)
		id := fmt.Sprintf("call_%d", callIDs.Add(1))
		half := len(args) / 2
		chunk(map[string]any{"tool_calls": []any{map[string]any{"index": i, "id": id, "type": "function",
			"function": map[string]any{"name": c.Name, "arguments": args[:half]}}}}, nil)
		time.Sleep(60 * time.Millisecond)
		chunk(map[string]any{"tool_calls": []any{map[string]any{"index": i, "function": map[string]any{"arguments": args[half:]}}}}, nil)
	}
	chunk(map[string]any{}, finish(p))
	sse(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120,
		"completion_tokens_details": map[string]any{"reasoning_tokens": len(p.Thinking) * 3}}})
	sse("[DONE]")
}

// --- the Responses API ---------------------------------------------------------

func responses(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model        string            `json:"model"`
		Stream       bool              `json:"stream"`
		Instructions string            `json:"instructions"`
		Input        []json.RawMessage `json:"input"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, `{"error":{"message":"bad request"}}`, 400)
		return
	}
	names := map[string]string{}
	var conv []turn
	system := req.Instructions
	for _, raw := range req.Input {
		var it struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			CallID  string          `json:"call_id"`
			Name    string          `json:"name"`
			Output  string          `json:"output"`
		}
		_ = json.Unmarshal(raw, &it)
		switch {
		case it.Type == "function_call":
			names[it.CallID] = it.Name
		case it.Type == "function_call_output":
			conv = append(conv, turn{Role: "tool", Text: it.Output, Tool: names[it.CallID]})
		case it.Type == "reasoning":
		case it.Role == "system" || it.Role == "developer":
			system += contentText(it.Content)
		case it.Role != "":
			conv = append(conv, turn{Role: it.Role, Text: contentText(it.Content)})
		}
	}
	if len(conv) == 0 {
		http.Error(w, `{"error":{"message":"empty input"}}`, 400)
		return
	}
	p := script(conv, system)
	rec := record("responses", req.Model, conv, system)
	if !wait(r.Context(), p.Delay) {
		rec.done(true)
		return
	}
	defer rec.done(false)
	var output []any
	usage := map[string]any{"input_tokens": 100, "output_tokens": 20, "output_tokens_details": map[string]any{"reasoning_tokens": 7}}
	if !req.Stream {
		if len(p.Thinking) > 0 {
			output = append(output, map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "enc",
				"summary": []any{map[string]any{"type": "summary_text", "text": strings.Join(p.Thinking, "")}}})
		}
		if p.Text != "" {
			output = append(output, map[string]any{"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": p.Text}}})
		}
		for _, c := range p.Calls {
			b, _ := json.Marshal(c.Args)
			output = append(output, map[string]any{"type": "function_call", "call_id": fmt.Sprintf("call_%d", callIDs.Add(1)),
				"name": c.Name, "arguments": string(b)})
		}
		writeJSON(w, map[string]any{"id": "resp_1", "status": "completed", "output": output, "usage": usage})
		return
	}
	sse := startSSE(w)
	idx := 0
	if len(p.Thinking) > 0 {
		sse(map[string]any{"type": "response.output_item.added", "output_index": idx, "item": map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}}})
		for _, t := range p.Thinking {
			sse(map[string]any{"type": "response.reasoning_summary_text.delta", "output_index": idx, "summary_index": 0, "item_id": "rs_1", "delta": t})
			time.Sleep(150 * time.Millisecond)
		}
		item := map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "enc",
			"summary": []any{map[string]any{"type": "summary_text", "text": strings.Join(p.Thinking, "")}}}
		sse(map[string]any{"type": "response.output_item.done", "output_index": idx, "item": item})
		output = append(output, item)
		idx++
	}
	if p.Text != "" {
		sse(map[string]any{"type": "response.output_item.added", "output_index": idx, "item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "content": []any{}}})
		for _, word := range words(p.Text) {
			sse(map[string]any{"type": "response.output_text.delta", "output_index": idx, "content_index": 0, "item_id": "msg_1", "delta": word})
			time.Sleep(40 * time.Millisecond)
		}
		item := map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": p.Text}}}
		sse(map[string]any{"type": "response.output_item.done", "output_index": idx, "item": item})
		output = append(output, item)
		idx++
	}
	for _, c := range p.Calls {
		b, _ := json.Marshal(c.Args)
		args := string(b)
		cid := fmt.Sprintf("call_%d", callIDs.Add(1))
		sse(map[string]any{"type": "response.output_item.added", "output_index": idx,
			"item": map[string]any{"type": "function_call", "id": "fc_" + cid, "call_id": cid, "name": c.Name, "arguments": ""}})
		half := len(args) / 2
		sse(map[string]any{"type": "response.function_call_arguments.delta", "output_index": idx, "item_id": "fc_" + cid, "delta": args[:half]})
		time.Sleep(60 * time.Millisecond)
		sse(map[string]any{"type": "response.function_call_arguments.delta", "output_index": idx, "item_id": "fc_" + cid, "delta": args[half:]})
		item := map[string]any{"type": "function_call", "id": "fc_" + cid, "call_id": cid, "name": c.Name, "arguments": args}
		sse(map[string]any{"type": "response.output_item.done", "output_index": idx, "item": item})
		output = append(output, item)
		idx++
	}
	sse(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_1", "status": "completed", "output": output, "usage": usage}})
}

// --- helpers ------------------------------------------------------------------------

func finish(p plan) string {
	if len(p.Calls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func words(s string) []string {
	var out []string
	for _, w := range strings.SplitAfter(s, " ") {
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// contentText reads a message's content: a string, or a parts array.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return ""
}

func startSSE(w http.ResponseWriter) func(v any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	fl, _ := w.(http.Flusher)
	return func(v any) {
		if s, ok := v.(string); ok {
			fmt.Fprintf(w, "data: %s\n\n", s)
		} else {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		if fl != nil {
			fl.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18977", "listen address")
	flag.Parse()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"object": "list", "data": []any{
			map[string]any{"id": "fake-chat", "object": "model"}, map[string]any{"id": "gpt-5-fake", "object": "model"}}})
	})
	mux.HandleFunc("POST /v1/chat/completions", chatCompletions)
	mux.HandleFunc("POST /v1/responses", responses)
	mux.HandleFunc("GET /debug/requests", func(w http.ResponseWriter, r *http.Request) {
		recMu.Lock()
		defer recMu.Unlock()
		writeJSON(w, recs)
	})
	log.Printf("fakeopenai on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
