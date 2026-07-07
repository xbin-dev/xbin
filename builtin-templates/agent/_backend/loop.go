// loop.go — the durable agent loop. One "drive" advances a run up to MaxIters
// steps; each step is a journaled boundary (context assembly → optional
// compaction → LLM call → tool execution) so a crash or backend unload resumes
// cleanly. Control-flow tools park the run (sleeping/waiting) or end it; the
// cron heartbeat re-drives due runs. See plans/agent.md.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxParallelTools = 6 // bound on concurrent tool calls in one turn

// driveAsync advances a run in the background (fire-and-forget from HTTP
// handlers). Concurrent drives of the same run are coalesced via claim().
func (ag *Agent) driveAsync(runID int64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		ag.drive(ctx, runID)
		ag.reconcileBeat() // turn the wake heartbeat on/off to match what's pending
	}()
}

// drive advances one run. Safe to call redundantly (heartbeat + handlers).
func (ag *Agent) drive(ctx context.Context, runID int64) {
	if !ag.claim(runID) {
		return // already being driven
	}
	defer ag.release(runID)

	cfg, err := ag.db.runConfig(runID)
	if err != nil {
		return
	}
	mcp := ag.mcpTools(ctx, cfg) // discover MCP tools once per drive

	run, err := ag.db.getRun(runID)
	if err != nil {
		return
	}

	// Resume path: an approved tool turn was parked; execute it, then continue.
	if run.Status == statusRunning && run.Pending != "" {
		var pend pending
		if json.Unmarshal([]byte(run.Pending), &pend) == nil && pend.Kind == "approval" {
			_ = ag.db.setStatus(runID, statusRunning, 0, run.Result, "")
			if parked, term := ag.executeToolCalls(ctx, run, cfg, mcp, pend.ToolCalls, true); parked || term {
				return
			}
		}
	}

	switch run.Status {
	case statusDone, statusError, statusWaiting:
		return
	}
	_ = ag.db.setStatus(runID, statusRunning, 0, run.Result, "")

	for i := 0; i < cfg.MaxIters; i++ {
		if ag.stopped(runID) {
			ag.db.journal(runID, "note", map[string]string{"text": "interrupted"})
			_ = ag.db.setStatus(runID, statusIdle, 0, "interrupted", "")
			return
		}
		run, err = ag.db.getRun(runID)
		if err != nil {
			return
		}

		ag.maybeCompact(ctx, run, cfg)

		msgs, err := ag.assembleContext(run, cfg)
		if err != nil {
			ag.fail(runID, "assemble context: "+err.Error())
			return
		}
		specs := toolSpecs(cfg, mcp)
		model := modelFor(ctx, cfg, "general")

		t0 := time.Now()
		asst, resp, err := callLLM(ctx, model, msgs, specs)
		if err != nil {
			ag.fail(runID, err.Error())
			return
		}
		// Provider-reported prompt tokens are the compaction trigger's ground
		// truth (beats the char/4 estimate); recorded for the next maybeCompact.
		ag.db.setPromptTokens(runID, resp.Usage.PromptTokens)
		ag.db.journal(runID, "llm_call", map[string]any{
			"model": model, "latencyMs": time.Since(t0).Milliseconds(),
			"promptTokens": resp.Usage.PromptTokens, "completionTokens": resp.Usage.CompletionTokens,
			"toolCalls": len(asst.ToolCalls), "finishReason": firstFinish(resp),
		})

		tcJSON := ""
		if len(asst.ToolCalls) > 0 {
			b, _ := json.Marshal(asst.ToolCalls)
			tcJSON = string(b)
		}
		_, _ = ag.db.addMessage(&Message{
			RunID: runID, Role: "assistant", Content: asst.Content, ToolCalls: tcJSON,
		})

		if len(asst.ToolCalls) == 0 {
			// A plain answer ends the turn; wait for the next user message.
			_ = ag.db.setStatus(runID, statusIdle, 0, run.Result, "")
			return
		}

		if parked, term := ag.executeToolCalls(ctx, run, cfg, mcp, asst.ToolCalls, false); parked || term {
			return
		}
	}

	// Hit the per-drive step budget without finishing: park briefly so the
	// heartbeat continues the work rather than spinning here.
	ag.db.journal(runID, "yield", map[string]string{"reason": "max iterations for this drive; will resume"})
	_ = ag.db.setStatus(runID, statusSleep, now(), "", "")
}

// pending is a parked action stored on the run (approval gate / ask).
type pending struct {
	Kind      string     `json:"kind"` // "approval" | "ask"
	ToolCalls []toolCall `json:"toolCalls,omitempty"`
}

// executeToolCalls runs a turn's tool calls in order, appending a tool result
// message for each so the transcript stays API-valid. Returns (parked, terminal):
// parked = run paused (waiting/sleeping/approval), terminal = run ended.
// approved skips the approval gate (resume-after-approval path).
func (ag *Agent) executeToolCalls(ctx context.Context, run *Run, cfg Config, mcp []toolSpec, calls []toolCall, approved bool) (parked, terminal bool) {
	// Approval gate: if any side-effecting tool needs approval, park the whole
	// turn before executing anything.
	if cfg.Approve && !approved {
		for _, tc := range calls {
			if sideEffect(tc.Function.Name) {
				pend, _ := json.Marshal(pending{Kind: "approval", ToolCalls: calls})
				ag.db.journal(run.ID, "ask", map[string]any{"kind": "approval", "tools": toolNames(calls)})
				_ = ag.db.setStatus(run.ID, statusWaiting, 0, "approve the pending tool call(s)", string(pend))
				return true, false
			}
		}
	}

	for idx := 0; idx < len(calls); {
		name := calls[idx].Function.Name
		if !isParkingTool(name) {
			// A run of consecutive non-parking tools executes in parallel
			// (spawn_subagent among them ⇒ parallel subagents; a configurable
			// per-tool timeout bounds the rest). Results append in call order so
			// the transcript stays API-valid.
			j := idx
			for j < len(calls) && !isParkingTool(calls[j].Function.Name) {
				j++
			}
			ag.runToolBatch(ctx, run, cfg, calls[idx:j])
			idx = j
			continue
		}

		// A parking control tool changes run status: handle it, then placeholder
		// the remaining calls (a run ends/pauses at the first one) and return.
		tc := calls[idx]
		args := decodeArgs(tc.Function.Arguments)
		switch name {
		case "finish":
			result, _ := args["result"].(string)
			ag.addToolResult(run.ID, tc, "finished")
			ag.db.journal(run.ID, "finish", map[string]string{"result": result})
			_ = ag.db.setStatus(run.ID, statusDone, 0, result, "")
			_ = publishEvent(run.ID, "done")
			terminal = true
		case "ask_user":
			q, _ := args["question"].(string)
			ag.addToolResult(run.ID, tc, "(asked the user; awaiting their reply)")
			ag.db.journal(run.ID, "ask", map[string]string{"kind": "ask_user", "question": q})
			_ = ag.db.setStatus(run.ID, statusWaiting, 0, q, "")
			_ = publishEvent(run.ID, "waiting_input")
			parked = true
		case "yield":
			secs := toInt(args["seconds"])
			if secs < 0 {
				secs = 0
			}
			ag.addToolResult(run.ID, tc, fmt.Sprintf("(yielded %ds)", secs))
			ag.db.journal(run.ID, "yield", map[string]any{"seconds": secs})
			_ = ag.db.setStatus(run.ID, statusSleep, now()+int64(secs), "", "")
			parked = true
		}
		for _, rest := range calls[idx+1:] {
			ag.addToolResult(run.ID, rest, "(not executed: run paused/ended)")
		}
		return parked, terminal
	}
	return parked, terminal
}

// isParkingTool reports control tools that change run status and stop the turn
// (finish/ask_user/yield). spawn_subagent is NOT one — it returns a result and
// the run continues — so it can join a parallel batch.
func isParkingTool(name string) bool {
	switch name {
	case "finish", "ask_user", "yield":
		return true
	}
	return false
}

// runToolBatch executes a group of non-parking tool calls concurrently (bounded)
// and appends their results in call order. spawn_subagent runs under the parent
// context (subagents can be long); every other tool gets cfg.ToolTimeout.
func (ag *Agent) runToolBatch(ctx context.Context, run *Run, cfg Config, calls []toolCall) {
	if len(calls) == 1 || !cfg.feature("parallelTools") {
		for _, tc := range calls {
			ag.addToolResult(run.ID, tc, ag.runOneTool(ctx, run, cfg, tc))
		}
		return
	}
	results := make([]string, len(calls))
	sem := make(chan struct{}, maxParallelTools)
	var wg sync.WaitGroup
	for i, tc := range calls {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, tc toolCall) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = ag.runOneTool(ctx, run, cfg, tc)
		}(i, tc)
	}
	wg.Wait()
	for i, tc := range calls {
		ag.addToolResult(run.ID, tc, results[i])
	}
}

// runOneTool executes a single non-parking tool and returns its textual result
// (also journaling non-subagent tools). Never parks — spawn_subagent runs its
// child to completion here.
func (ag *Agent) runOneTool(ctx context.Context, run *Run, cfg Config, tc toolCall) string {
	name := tc.Function.Name
	args := decodeArgs(tc.Function.Arguments)
	if name == "spawn_subagent" {
		task, _ := args["task"].(string)
		sys, _ := args["system"].(string)
		return ag.spawnSubagent(ctx, run, cfg, task, sys)
	}
	tctx := ctx
	if cfg.ToolTimeout > 0 {
		var cancel context.CancelFunc
		tctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.ToolTimeout)*time.Second)
		defer cancel()
	}
	out, err := ag.runTool(tctx, run, cfg, name, args)
	if err != nil {
		if tctx.Err() == context.DeadlineExceeded {
			out = fmt.Sprintf("error: tool timed out after %ds", cfg.ToolTimeout)
		} else {
			out = "error: " + err.Error()
		}
	}
	ag.db.journal(run.ID, "tool_call", map[string]any{"name": name, "args": args})
	ag.db.journal(run.ID, "tool_result", map[string]any{"name": name, "result": clip(out, 2000)})
	return out
}

func (ag *Agent) addToolResult(runID int64, tc toolCall, content string) {
	_, _ = ag.db.addMessage(&Message{
		RunID: runID, Role: "tool", Name: tc.Function.Name, ToolCallID: tc.ID, Content: content,
	})
}

// spawnSubagent runs a focused child run to completion and returns its result.
// The child cannot itself spawn subagents (bounded nesting).
func (ag *Agent) spawnSubagent(ctx context.Context, parent *Run, cfg Config, task, system string) string {
	child := cfg
	child.Subagents = false
	if system != "" {
		child.System = system
	}
	cfgJSON, _ := json.Marshal(child)
	childID, err := ag.db.createRun("subagent: "+clip(task, 60), string(cfgJSON), parent.ID)
	if err != nil {
		return "error creating subagent: " + err.Error()
	}
	_, _ = ag.db.addMessage(&Message{RunID: childID, Role: "system", Content: child.System})
	_, _ = ag.db.addMessage(&Message{RunID: childID, Role: "user", Content: task})
	ag.db.journal(parent.ID, "note", map[string]any{"text": fmt.Sprintf("spawned subagent #%d", childID)})

	ag.drive(ctx, childID) // synchronous, bounded by MaxIters

	cr, err := ag.db.getRun(childID)
	if err != nil {
		return "subagent error"
	}
	if cr.Result != "" {
		return cr.Result
	}
	return ag.lastAssistant(childID)
}

func (ag *Agent) lastAssistant(runID int64) string {
	msgs, _ := ag.db.messages(runID, false)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && msgs[i].Content != "" {
			return msgs[i].Content
		}
	}
	return "(no result)"
}

func (ag *Agent) fail(runID int64, msg string) {
	ag.db.journal(runID, "error", map[string]string{"error": msg})
	_ = ag.db.setStatus(runID, statusError, 0, msg, "")
	_ = publishEvent(runID, "error")
}

// --- context assembly + compaction --------------------------------------

// assembleContext builds the message list sent to the LLM: a system message
// (base prompt + memory blocks + running summary) followed by the live
// (uncompacted) transcript.
func (ag *Agent) assembleContext(run *Run, cfg Config) ([]wireMsg, error) {
	mem, err := ag.db.memory(run.ID)
	if err != nil {
		return nil, err
	}
	var sys strings.Builder
	sys.WriteString(cfg.System)
	// Date only (not a full timestamp) keeps the system prefix stable within a
	// day, so prompt caching keeps hitting (plans/agent-v2.md).
	fmt.Fprintf(&sys, "\n\nToday's date (UTC): %s.", time.Now().UTC().Format("2006-01-02"))
	if len(mem) > 0 {
		sys.WriteString("\n\n# Memory blocks\n")
		for k, v := range mem {
			fmt.Fprintf(&sys, "- %s: %s\n", k, v)
		}
	}
	if run.Summary != "" {
		sys.WriteString("\n\n# Summary of earlier conversation\n")
		sys.WriteString(run.Summary)
	}

	live, err := ag.db.messages(run.ID, true)
	if err != nil {
		return nil, err
	}
	out := []wireMsg{{Role: "system", Content: sys.String()}}
	for _, m := range live {
		if m.Role == "system" {
			continue // the base/system prompt is rebuilt above
		}
		wm := wireMsg{Role: m.Role, Content: m.Content, Name: m.Name, ToolCallID: m.ToolCallID}
		if m.ToolCalls != "" {
			_ = json.Unmarshal([]byte(m.ToolCalls), &wm.ToolCalls)
		}
		out = append(out, wm)
	}
	return out, nil
}

// maybeCompact folds the oldest live turns into the running summary when the
// assembled context exceeds the token budget, keeping the transcript valid
// (never splits an assistant tool-call from its results).
func (ag *Agent) maybeCompact(ctx context.Context, run *Run, cfg Config) {
	live, err := ag.db.messages(run.ID, true)
	if err != nil {
		return
	}
	total := estimateTokens(cfg.System) + estimateTokens(run.Summary)
	for _, m := range live {
		total += m.Tokens
	}
	if run.LastPromptTokens > 0 {
		total = run.LastPromptTokens // provider-reported truth beats the estimate
	}
	if total <= cfg.TokenBudget {
		return
	}

	const keepTail = 6
	cut := len(live) - keepTail
	if cut < 1 {
		cut = len(live) / 2
	}
	if cut < 1 {
		return
	}
	// Land the tail on a clean boundary: it must not start with a tool result.
	for cut < len(live) && live[cut].Role == "tool" {
		cut++
	}
	if cut >= len(live) {
		return
	}

	prefix := live[:cut]
	var buf strings.Builder
	for _, m := range prefix {
		fmt.Fprintf(&buf, "%s: %s\n", m.Role, m.Content)
	}
	summary := ag.summarize(ctx, cfg, run.Summary, buf.String())
	if summary == "" {
		return // summarizer failed; skip compaction this step
	}
	ids := make([]int64, len(prefix))
	for i, m := range prefix {
		ids[i] = m.ID
	}
	_ = ag.db.markCompacted(ids)
	_ = ag.db.setSummary(run.ID, summary)
	ag.db.journal(run.ID, "compaction", map[string]any{"messages": len(prefix), "summaryTokens": estimateTokens(summary)})
	run.Summary = summary
}

func (ag *Agent) summarize(ctx context.Context, cfg Config, prior, transcript string) string {
	sys := "You compact a conversation for an AI agent. Merge the prior summary and the new transcript into a single concise summary that preserves facts, decisions, open threads, and anything needed to continue. Output only the summary."
	user := "Prior summary:\n" + prior + "\n\nNew transcript to fold in:\n" + transcript
	msg, _, err := callLLM(ctx, modelFor(ctx, cfg, "memory"), []wireMsg{{Role: "system", Content: sys}, {Role: "user", Content: user}}, nil)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(msg.Content)
}

// --- small helpers ------------------------------------------------------

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func toolNames(calls []toolCall) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.Function.Name
	}
	return out
}

func firstFinish(r chatResp) string {
	if len(r.Choices) > 0 {
		return r.Choices[0].FinishReason
	}
	return ""
}
