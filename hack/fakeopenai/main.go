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
//	my schedules schedules_list (this conversation's, D111) → "Found: <its first line>"
//	all my threads
//	             threads_list scope all — the owner is asked to allow it
//	             (D111) → "Found: <its first line>"
//	sandbox pwd  bash "pwd; echo $SANDBOX_NAME" in the bound sandbox (D115) → "Ran: <its first line>"
//	sandbox write
//	             write hello.txt "hi from the agent" → "Wrote it."
//	sandbox edit edit hello.txt hi → hello → "Edited it."
//	sandbox find glob **/*.txt → "Found: <its first line>"
//	sandbox cat  bash "cat hello.txt" → "Ran: <its first line>" (what write/edit left)
//	sandbox long bash "sleep 3; echo slow done" with timeout_s 1 → it becomes a
//	             job → bash_output {job, wait_s 20} → "Job: <its first line>"
//	sandbox pkill
//	             bash "echo before; pkill -f marker-h1; echo after marker-h1":
//	             refused (it kills by name, D134) → the same with force:true →
//	             "Ran: <its first line>" (the job outlived its own pkill -f)
//	sandbox kill bash "…3000 lines…; echo last words; sleep 60" in the background
//	             → bash_kill {job} → "Killed: <its last line>" (the footer)
//	sandbox restart
//	             bash "sleep 8; echo survived" (the harness restarts the agent's
//	             backend under it); the lost call names its job → bash_output
//	sandbox browser
//	             browser_check {target "./page.html", screenshots [0], a script
//	             reading #out} (D136) → "Checked: <its first line>", plus
//	             " · saw the screenshots" when the next request carried them
//	sandbox download twice
//	             sandbox_download hello.txt, then again → "Downloaded: <the
//	             second result's first line>" (in place: "unchanged: …")
//	sandbox serve
//	             write site/index.html (a page whose script reports what it can
//	             reach: cookie, storage, whoami, the tile API), bash
//	             "python3 -m http.server <port> --bind 127.0.0.1" as a background
//	             job (port: this server's + 1), then preview_port {port,
//	             "/index.html"} (D135) → "Showing it live."
//	sandbox report
//	             bash writing rep/index.html — a report over 64 KB (4000
//	             rows), then render_html "./rep/index.html"; the answer then
//	             takes 6 s → "Report shown." (the pane opens during the turn)
//	new sandbox  sandbox_create {name "scratch"} — the owner is asked to allow
//	             it → "Created: <its first line>"
//	restart me   the FIRST request of that turn hangs 30 s (the harness restarts
//	             the agent's backend under it), a re-issue answers at once:
//	             a note "Survive a restart" → "Noted it."
//	long N       (starts the message) N units, streamed without pauses: each a
//	             markdown line "**Unit k** of the long turn…" with a note call
//	             "Note unit k", then "done: N units" — a long transcript fast
//	             (N ≤ the agent's maxTurnSteps − 1)
//	paras N      (starts the message) an answer of N paragraphs "Paragraph k of
//	             the answer, streamed.", word by word
//	huge context "ok: <text>", reporting a 5 000 000-token prompt: the agent's
//	             next step compacts (D133)
//	(else)       "ok: <text>"
//
// The agent naming a conversation (its title prompt) gets "Titled <the first
// three words of the first message>"; its compaction summarizer (D133) gets
// "SUMMARY: <n> transcript line(s) folded".
//
// Every request reports a 100-token prompt unless its plan says otherwise.
// The agent's task reminder (D133: a <task-reminder> block appended to the
// last message, never stored) is cut off before a message is scripted, and
// recorded on its own. /v1/models lists a 128 000-token context window.
//
// A subagent's task (its first user message) drives it the same way:
// "count…" → thinking + "one, two, three"; "slow job…" → 6 s, then "slow job done".
//
// GET /debug/requests lists every model request (start/end in unix ms, the
// last user text, whether the caller hung up first, the start of the system
// prompt, the task reminder) so a pass can measure, e.g., how long a
// restarted backend took to re-issue its call, or check what a call after a
// compaction carried.
//
//	go run ./hack/fakeopenai -addr 127.0.0.1:18977
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"strconv"
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
	System   string `json:"system"`   // the system prompt's first 4000 characters
	Reminder string `json:"reminder"` // the task reminder on the last message ("" = none)
	Purpose  string `json:"purpose"`  // turn | compact | title
}

// Where the agent's task reminder starts (the agent's asks.go): after two
// newlines in a text, or as a parts array's last text part (which
// contentText joins on without a separator).
const reminderOpen = "<task-reminder>"

// cutReminder splits a message's text from the task reminder appended to it.
func cutReminder(s string) (text, reminder string) {
	if i := strings.Index(s, reminderOpen); i >= 0 {
		return strings.TrimRight(s[:i], "\n"), strings.TrimSpace(s[i:])
	}
	return s, ""
}

var (
	recMu sync.Mutex
	recs  []*reqRec
	seen  = map[string]bool{} // "restart me" turns already asked once
	jobRe = regexp.MustCompile(`\bjob (\d+)`)
	// pkillCmd would kill its own shell if the command were in its cmdline
	pkillCmd = "echo before; pkill -f marker-h1; echo after marker-h1"
)

func record(wire, model string, conv []turn, system, reminder string) *reqRec {
	r := &reqRec{ID: callSeq.Add(1), Wire: wire, Model: model, Sub: strings.Contains(system, "You are a subagent"), Start: time.Now().UnixMilli(),
		System: clip(system, 4000), Reminder: reminder, Purpose: purposeOf(system)}
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
	Fast     bool // stream without the per-word and per-call pauses
	Prompt   int  // the prompt tokens the reply reports (0: 100)
}

func (p plan) promptTokens() int {
	if p.Prompt > 0 {
		return p.Prompt
	}
	return 100
}

// purposeOf tells the agent's calls apart by their system prompt.
func purposeOf(system string) string {
	switch {
	case strings.Contains(system, "Name this conversation"):
		return "title"
	case strings.Contains(system, "You compact an AI agent"):
		return "compact"
	}
	return "turn"
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
	// the agent compacting its context (D133): a summary of what it was given
	if purposeOf(system) == "compact" {
		folded := strings.Count(conv[len(conv)-1].Text, "\n#")
		return plan{Text: fmt.Sprintf("SUMMARY: %d transcript line(s) folded", folded)}
	}
	sub := strings.Contains(system, "You are a subagent")
	last := conv[len(conv)-1]
	// images a tool showed (file_view, browser_check) ride a user message
	// after the tool results: the turn still answers the tool
	const shownLead = "(images you asked to see"
	saw := false
	if n := len(conv); n > 1 && last.Role == "user" && strings.HasPrefix(last.Text, shownLead) {
		last, saw = conv[n-2], true
	}
	lastUser := ""
	for i := len(conv) - 1; i >= 0; i-- {
		if conv[i].Role == "user" && !strings.HasPrefix(conv[i].Text, shownLead) {
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
	// long N: N units, each a markdown answer with a note call, then "done: N units"
	// (a long transcript fast); paras N: an answer of N paragraphs, streamed slowly
	if n, ok := countAfter(lastUser, "long "); ok {
		done := 0
		for i := len(conv) - 1; i >= 0 && conv[i].Role != "user"; i-- {
			if conv[i].Role == "tool" {
				done++
			}
		}
		if done >= n {
			return plan{Fast: true, Text: fmt.Sprintf("done: %d units", n)}
		}
		k := done + 1
		return plan{Fast: true, Text: fmt.Sprintf("**Unit %d** of the long turn: a paragraph with `code %d`, a [link](https://example.com/%d) and *emphasis*.", k, k, k),
			Calls: []call{{"note", map[string]any{"text": fmt.Sprintf("unit %d", k), "summary": fmt.Sprintf("Note unit %d", k)}}}}
	}
	if n, ok := countAfter(lastUser, "paras "); ok && last.Role == "user" {
		ps := make([]string, n)
		for i := range ps {
			ps[i] = fmt.Sprintf("Paragraph %d of the answer, streamed.", i+1)
		}
		return plan{Text: strings.Join(ps, "\n\n")}
	}
	if strings.Contains(lastUser, "sandbox serve") {
		return serveScript(conv)
	}
	if strings.Contains(lastUser, "sandbox report") {
		return reportScript(conv)
	}
	if last.Role == "tool" {
		switch last.Tool {
		case "note":
			return plan{Text: "Noted it."}
		case "file_write":
			return plan{Calls: []call{{"attach_to_reply", map[string]any{"paths": []string{"note.txt"}, "summary": "Attach the file"}}}}
		case "attach_to_reply":
			return plan{Text: "Here is the file."}
		case "threads_list", "schedules_list", "glob":
			first, _, _ := strings.Cut(strings.TrimSpace(last.Text), "\n")
			return plan{Text: "Found: " + first}
		case "bash":
			if strings.HasPrefix(last.Text, "not run:") { // the kill-by-name guard (D134): force it
				return plan{Calls: []call{{"bash", map[string]any{"command": pkillCmd, "force": true, "summary": "Kill by name anyway"}}}}
			}
			if m := jobRe.FindStringSubmatch(last.Text); m != nil && strings.Contains(lastUser, "sandbox kill") {
				n, _ := strconv.Atoi(m[1])
				time.Sleep(300 * time.Millisecond) // let it write
				return plan{Calls: []call{{"bash_kill", map[string]any{"job": n, "summary": "Stop the job"}}}}
			}
			// a command still running (a timeout, or lost to a restart) names its job
			if m := jobRe.FindStringSubmatch(last.Text); m != nil && !strings.Contains(last.Text, "[exit") {
				n, _ := strconv.Atoi(m[1])
				return plan{Calls: []call{{"bash_output", map[string]any{"job": n, "wait_s": 20, "summary": "Wait for the job"}}}}
			}
			first, _, _ := strings.Cut(strings.TrimSpace(last.Text), "\n")
			return plan{Text: "Ran: " + first}
		case "bash_output":
			first, _, _ := strings.Cut(strings.TrimSpace(last.Text), "\n")
			return plan{Text: "Job: " + first}
		case "bash_kill":
			lines := strings.Split(strings.TrimSpace(last.Text), "\n")
			return plan{Text: "Killed: " + lines[len(lines)-1]}
		case "browser_check":
			first, _, _ := strings.Cut(strings.TrimSpace(last.Text), "\n")
			if saw {
				first += " · saw the screenshots"
			}
			return plan{Text: "Checked: " + first}
		case "sandbox_download":
			n := 0
			for i := len(conv) - 1; i >= 0 && !(conv[i].Role == "user" && !strings.HasPrefix(conv[i].Text, shownLead)); i-- {
				if conv[i].Tool == "sandbox_download" {
					n++
				}
			}
			if n < 2 {
				return plan{Calls: []call{{"sandbox_download", map[string]any{"path": "hello.txt", "summary": "Download it again"}}}}
			}
			first, _, _ := strings.Cut(strings.TrimSpace(last.Text), "\n")
			return plan{Text: "Downloaded: " + first}
		case "write":
			return plan{Text: "Wrote it."}
		case "edit":
			return plan{Text: "Edited it."}
		case "sandbox_create":
			first, _, _ := strings.Cut(strings.TrimSpace(last.Text), "\n")
			return plan{Text: "Created: " + first}
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
	case strings.Contains(lastUser, "huge context"):
		return plan{Text: "ok: " + lastUser, Prompt: 5_000_000}
	case strings.Contains(lastUser, "my schedules"):
		return plan{Calls: []call{{"schedules_list", map[string]any{"summary": "List my schedules"}}}}
	case strings.Contains(lastUser, "all my threads"):
		return plan{Calls: []call{{"threads_list", map[string]any{"scope": "all", "summary": "List your conversations"}}}}
	case strings.Contains(lastUser, "sandbox pwd"):
		return plan{Calls: []call{{"bash", map[string]any{"command": "pwd; echo $SANDBOX_NAME", "summary": "Where am I"}}}}
	case strings.Contains(lastUser, "sandbox write"):
		return plan{Calls: []call{{"write", map[string]any{"path": "hello.txt", "content": "hi from the agent\n", "summary": "Write hello.txt"}}}}
	case strings.Contains(lastUser, "sandbox edit"):
		return plan{Calls: []call{{"edit", map[string]any{"path": "hello.txt", "old_string": "hi", "new_string": "hello", "summary": "Edit hello.txt"}}}}
	case strings.Contains(lastUser, "sandbox cat"):
		return plan{Calls: []call{{"bash", map[string]any{"command": "cat hello.txt", "summary": "Show hello.txt"}}}}
	case strings.Contains(lastUser, "sandbox find"):
		return plan{Calls: []call{{"glob", map[string]any{"pattern": "**/*.txt", "summary": "Find text files"}}}}
	case strings.Contains(lastUser, "sandbox long"):
		return plan{Calls: []call{{"bash", map[string]any{"command": "sleep 3; echo slow done", "timeout_s": 1, "summary": "Run something slow"}}}}
	case strings.Contains(lastUser, "sandbox pkill"):
		return plan{Calls: []call{{"bash", map[string]any{"command": pkillCmd, "summary": "Kill by name"}}}}
	case strings.Contains(lastUser, "sandbox kill"):
		return plan{Calls: []call{{"bash", map[string]any{"command": "for i in $(seq 1 3000); do echo line $i; done; echo last words; sleep 60",
			"background": true, "summary": "Start a chatty job"}}}}
	case strings.Contains(lastUser, "sandbox restart"):
		return plan{Calls: []call{{"bash", map[string]any{"command": "sleep 8; echo survived", "summary": "Survive a restart"}}}}
	case strings.Contains(lastUser, "sandbox browser"):
		return plan{Calls: []call{{"browser_check", map[string]any{"target": "./page.html", "wait_ms": 300, "screenshots_ms": []int{0},
			"script": "return await page.locator('#out').textContent()", "summary": "Check the page in a browser"}}}}
	case strings.Contains(lastUser, "sandbox download twice"):
		return plan{Calls: []call{{"sandbox_download", map[string]any{"path": "hello.txt", "summary": "Download hello.txt"}}}}
	case strings.Contains(lastUser, "new sandbox"):
		return plan{Calls: []call{{"sandbox_create", map[string]any{"name": "scratch", "summary": "Make a sandbox"}}}}
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
	system, reminder := "", ""
	for _, m := range req.Messages {
		text, rem := cutReminder(contentText(m.Content))
		if rem != "" {
			reminder = rem
		}
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
	rec := record("chat", req.Model, conv, system, reminder)
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
			"usage": map[string]any{"prompt_tokens": p.promptTokens(), "completion_tokens": 20, "total_tokens": p.promptTokens() + 20}})
		return
	}
	sse := startSSE(w)
	chunk := func(delta map[string]any, fin any) {
		sse(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": fin}}})
	}
	for _, t := range p.Thinking {
		chunk(map[string]any{"reasoning_content": t}, nil)
		p.pause(150 * time.Millisecond)
	}
	for _, word := range p.chunks() {
		chunk(map[string]any{"content": word}, nil)
		p.pause(40 * time.Millisecond)
	}
	for i, c := range p.Calls {
		b, _ := json.Marshal(c.Args)
		args := string(b)
		id := fmt.Sprintf("call_%d", callIDs.Add(1))
		half := len(args) / 2
		chunk(map[string]any{"tool_calls": []any{map[string]any{"index": i, "id": id, "type": "function",
			"function": map[string]any{"name": c.Name, "arguments": args[:half]}}}}, nil)
		p.pause(60 * time.Millisecond)
		chunk(map[string]any{"tool_calls": []any{map[string]any{"index": i, "function": map[string]any{"arguments": args[half:]}}}}, nil)
	}
	chunk(map[string]any{}, finish(p))
	sse(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": p.promptTokens(), "completion_tokens": 20, "total_tokens": p.promptTokens() + 20,
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
	system, reminder := req.Instructions, ""
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
			text, rem := cutReminder(it.Output)
			if rem != "" {
				reminder = rem
			}
			conv = append(conv, turn{Role: "tool", Text: text, Tool: names[it.CallID]})
		case it.Type == "reasoning":
		case it.Role == "system" || it.Role == "developer":
			system += contentText(it.Content)
		case it.Role != "":
			text, rem := cutReminder(contentText(it.Content))
			if rem != "" {
				reminder = rem
			}
			conv = append(conv, turn{Role: it.Role, Text: text})
		}
	}
	if len(conv) == 0 {
		http.Error(w, `{"error":{"message":"empty input"}}`, 400)
		return
	}
	p := script(conv, system)
	rec := record("responses", req.Model, conv, system, reminder)
	if !wait(r.Context(), p.Delay) {
		rec.done(true)
		return
	}
	defer rec.done(false)
	var output []any
	usage := map[string]any{"input_tokens": p.promptTokens(), "output_tokens": 20, "output_tokens_details": map[string]any{"reasoning_tokens": 7}}
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
			p.pause(150 * time.Millisecond)
		}
		item := map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "enc",
			"summary": []any{map[string]any{"type": "summary_text", "text": strings.Join(p.Thinking, "")}}}
		sse(map[string]any{"type": "response.output_item.done", "output_index": idx, "item": item})
		output = append(output, item)
		idx++
	}
	if p.Text != "" {
		sse(map[string]any{"type": "response.output_item.added", "output_index": idx, "item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "content": []any{}}})
		for _, word := range p.chunks() {
			sse(map[string]any{"type": "response.output_text.delta", "output_index": idx, "content_index": 0, "item_id": "msg_1", "delta": word})
			p.pause(40 * time.Millisecond)
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
		p.pause(60 * time.Millisecond)
		sse(map[string]any{"type": "response.function_call_arguments.delta", "output_index": idx, "item_id": "fc_" + cid, "delta": args[half:]})
		item := map[string]any{"type": "function_call", "id": "fc_" + cid, "call_id": cid, "name": c.Name, "arguments": args}
		sse(map[string]any{"type": "response.output_item.done", "output_index": idx, "item": item})
		output = append(output, item)
		idx++
	}
	sse(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_1", "status": "completed", "output": output, "usage": usage}})
}

// --- helpers ------------------------------------------------------------------------

// countAfter reads "<prefix><n>" at the start of s (n in 1…2000).
func countAfter(s, prefix string) (int, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), prefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.Fields(rest + " x")[0])
	if err != nil || n < 1 || n > 2000 {
		return 0, false
	}
	return n, true
}

// pause sleeps d unless the plan streams fast.
func (p plan) pause(d time.Duration) {
	if !p.Fast {
		time.Sleep(d)
	}
}

// chunks is the text as it streams: word by word, or whole when fast.
func (p plan) chunks() []string {
	if p.Fast {
		if p.Text == "" {
			return nil
		}
		return []string{p.Text}
	}
	return words(p.Text)
}

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
	if _, p, err := net.SplitHostPort(*addr); err == nil {
		n, _ := strconv.Atoi(p)
		servePort = n + 1
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"object": "list", "data": []any{
			map[string]any{"id": "fake-chat", "object": "model", "context_length": 128000},
			map[string]any{"id": "gpt-5-fake", "object": "model", "context_length": 128000}}})
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

// servePort is "sandbox serve"'s: this server's port + 1 (main).
var servePort = 18978

// livePage is "sandbox serve"'s page: its script says it ran and what it
// could reach, in the DOM the harness reads (#r).
const livePage = `<!doctype html><html><head><title>live</title></head><body>
<h1 id="h">static</h1><pre id="r">…</pre>
<script>
(async () => {
  document.getElementById('h').textContent = 'scripts ran';
  const r = { origin: String(self.origin) };
  try { r.cookie = document.cookie; } catch (e) { r.cookie = 'threw'; }
  try { localStorage.setItem('x', '1'); r.storage = 'usable'; } catch (e) { r.storage = 'threw'; }
  for (const [k, u] of [['whoami', '/api/xbin/whoami'], ['tileApi', '/api/apps/agent/runs']]) {
    try { const res = await fetch(u, { credentials: 'include' }); r[k] = res.status; } catch (e) { r[k] = 'threw'; }
  }
  try { top.location.href = 'about:blank#escaped'; r.topNav = 'no throw'; } catch (e) { r.topNav = 'threw'; }
  try { parent.postMessage({ op: 'close' }, '*'); } catch (e) { /* none */ }
  document.getElementById('r').textContent = JSON.stringify(r);
})();
</script></body></html>
`

// reportCmd writes a report over the agent's 64 KB text cap: stored as a
// binary session file, it must still render (up to 2 MB).
const reportCmd = `mkdir -p rep && { printf '<!doctype html><html><body><h1 id="t">big report</h1>\n'; ` +
	`i=1; while [ $i -le 4000 ]; do printf '<p>row %d of the big report</p>\n' $i; i=$((i+1)); done; printf '</body></html>\n'; } > rep/index.html && wc -c < rep/index.html`

func reportScript(conv []turn) plan {
	did := map[string]bool{}
	for i := len(conv) - 1; i >= 0 && conv[i].Role != "user"; i-- {
		if conv[i].Role == "tool" {
			did[conv[i].Tool] = true
		}
	}
	switch {
	case !did["bash"]:
		return plan{Calls: []call{{"bash", map[string]any{"command": reportCmd, "summary": "Write the report"}}}}
	case !did["render_html"]:
		return plan{Calls: []call{{"render_html", map[string]any{"path": "./rep/index.html", "summary": "Show the report"}}}}
	}
	return plan{Delay: 6 * time.Second, Text: "Report shown."}
}

// serveScript is "sandbox serve": write the page and start its server, then
// preview_port it, then say so.
func serveScript(conv []turn) plan {
	did := map[string]bool{}
	for i := len(conv) - 1; i >= 0 && conv[i].Role != "user"; i-- {
		if conv[i].Role == "tool" {
			did[conv[i].Tool] = true
		}
	}
	switch {
	case !did["write"]:
		return plan{Calls: []call{
			{"write", map[string]any{"path": "site/index.html", "content": livePage, "summary": "Write the page"}},
			{"bash", map[string]any{"command": fmt.Sprintf("cd site && exec python3 -m http.server %d --bind 127.0.0.1", servePort), "background": true, "summary": "Serve it"}},
		}}
	case !did["preview_port"]:
		return plan{Delay: 1500 * time.Millisecond, Calls: []call{{"preview_port", map[string]any{"port": servePort, "path": "/index.html", "summary": "Show it live"}}}}
	}
	return plan{Text: "Showing it live."}
}
