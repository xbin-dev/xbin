// actor_tools.go — executing one step's tool calls.
//
// The calls are handled in the order the model wrote them: consecutive plain
// tools run as a parallel batch, subagent tools (spawn, wait, message, …)
// take effect at once, and a control tool (finish, ask_user, yield) ends the
// step there. Every result settles its placeholder with a compare-and-swap,
// so a tool that finishes after its step was interrupted cannot overwrite
// what the model was already told.
//
// Tool summaries: every tool schema gets a `summary` parameter — "one short
// line for the person watching" — that the tile shows as the call's headline
// (and subagent_status reports to a parent). It is stripped before dispatch;
// tools whose own schema already has a `summary` keep theirs.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const maxParallelTools = 4 // concurrent tool calls in one batch
const maxToolsGlobal = 12  // concurrent tool calls across every run

// A tool result larger than this is elided in the middle before it enters
// the transcript (head-heavy: openings carry the signal).
const maxToolResult = 16 << 10

const summaryParam = "summary"

var summarySchema = map[string]any{
	"type": "string",
	"description": "One short line (≤ 10 words) for the person watching: what this call does, " +
		"e.g. \"Look up open invoices for ACME\". Not passed to the tool.",
}

// injectSummaries adds the summary parameter to every tool (a copy — the
// specs are rebuilt per step, but MCP schemas come from a shared cache).
// Returns the tools whose own schema already defines `summary`.
func injectSummaries(specs []toolSpec) ([]toolSpec, map[string]bool) {
	own := map[string]bool{}
	out := make([]toolSpec, len(specs))
	for i, s := range specs {
		out[i] = s
		params := map[string]any{}
		for k, v := range s.Function.Parameters {
			params[k] = v
		}
		if len(params) == 0 {
			params["type"] = "object"
		}
		props := map[string]any{}
		if p, ok := params["properties"].(map[string]any); ok {
			for k, v := range p {
				props[k] = v
			}
		}
		if _, has := props[summaryParam]; has {
			own[s.Function.Name] = true
			continue
		}
		props[summaryParam] = summarySchema
		params["properties"] = props
		var req []any
		switch r := params["required"].(type) {
		case []string:
			for _, x := range r {
				req = append(req, x)
			}
		case []any:
			req = append(req, r...)
		}
		params["required"] = append(req, summaryParam)
		out[i].Function.Parameters = params
	}
	return out, own
}

// callArgs decodes a call's arguments and takes the injected summary out.
func callArgs(tc toolCall, own map[string]bool) (args map[string]any, summary string) {
	args = decodeArgs(tc.Function.Arguments)
	s, _ := args[summaryParam].(string)
	if !own[tc.Function.Name] {
		delete(args, summaryParam)
	}
	return args, strings.TrimSpace(s)
}

func isControlTool(name string) bool {
	switch name {
	case "finish", "ask_user", "yield":
		return true
	}
	return false
}

// execTools runs a step's calls. true means the turn stops here (parked,
// ended, or the step was cancelled).
func (e *Engine) execTools(ctx context.Context, ts *turnState, calls []toolCall, approved bool) bool {
	run, cfg := ts.run, ts.cfg
	ts.waits, ts.spawnedThisStep = nil, 0
	if cfg.Approve && !approved {
		for _, tc := range calls {
			if sideEffect(tc.Function.Name) {
				e.parkApproval(ts, calls)
				return true
			}
		}
	}
	for i := 0; i < len(calls); {
		name := calls[i].Function.Name
		switch {
		case isControlTool(name):
			e.controlTool(ctx, ts, calls[i], calls[i+1:])
			return true
		case isSubagentTool(name):
			e.subagentTool(ctx, ts, calls[i])
			i++
		default:
			j := i
			for j < len(calls) && !isControlTool(calls[j].Function.Name) && !isSubagentTool(calls[j].Function.Name) {
				j++
			}
			e.runToolBatch(ctx, ts, calls[i:j])
			if ctx.Err() != nil {
				return true
			}
			i = j
		}
	}
	_ = run
	return e.parkIfWaiting(ts)
}

// parkApproval parks the whole step for the owner's verdict. Placeholders say
// so, which keeps the transcript valid for as long as the run waits.
func (e *Engine) parkApproval(ts *turnState, calls []toolCall) {
	run := ts.run
	pend, _ := json.Marshal(pendingState{Kind: "approval", ToolCalls: calls})
	_ = e.fenced(func(t *DB) error {
		for _, tc := range calls {
			if ok, _ := t.setToolPlaceholder(run.ID, tc.ID, toolAwaitingApproval); ok {
				if id, _, err := t.toolResultRow(run.ID, tc.ID); err == nil {
					e.emitMessageID(t, ts.root, run.ID, id)
				}
			}
		}
		e.emitStep(t, ts.root, t.journal(run.ID, "ask", map[string]any{"kind": "approval", "tools": toolNames(calls)}))
		if err := t.setStatus(run.ID, statusWaiting, 0, "approve the pending tool call(s)", string(pend)); err != nil {
			return err
		}
		e.emitRun(t, run.ID)
		t.AfterCommit(func() { publishEvent(run.ID, "waiting_input") })
		return nil
	})
}

// runToolBatch runs plain tools in parallel (bounded per batch and across
// every run) and settles each result as it finishes.
func (e *Engine) runToolBatch(ctx context.Context, ts *turnState, calls []toolCall) {
	if len(calls) == 1 || !ts.cfg.feature("parallelTools") {
		for _, tc := range calls {
			if ctx.Err() != nil {
				return
			}
			e.settleResult(ts, tc, e.runOneTool(ctx, ts, tc))
		}
		return
	}
	sem := make(chan struct{}, maxParallelTools)
	var wg sync.WaitGroup
	for _, tc := range calls {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		select {
		case e.ag.toolSem <- struct{}{}:
		case <-ctx.Done():
			<-sem
			wg.Wait()
			return
		}
		wg.Add(1)
		go func(tc toolCall) {
			defer wg.Done()
			defer func() { <-sem; <-e.ag.toolSem }()
			e.settleResult(ts, tc, e.runOneTool(ctx, ts, tc))
		}(tc)
	}
	wg.Wait()
}

// runOneTool executes a single plain tool under the per-tool timeout.
func (e *Engine) runOneTool(ctx context.Context, ts *turnState, tc toolCall) string {
	cfg := ts.cfg
	args, _ := callArgs(tc, ts.own)
	tctx := ctx
	if cfg.ToolTimeout > 0 {
		var cancel context.CancelFunc
		tctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.ToolTimeout)*time.Second)
		defer cancel()
	}
	out, err := e.ag.runTool(tctx, ts.run, cfg, tc.Function.Name, args)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			out = "(" + causeText(ctx) + ")"
		case tctx.Err() == context.DeadlineExceeded:
			out = fmt.Sprintf("error: tool timed out after %ds", cfg.ToolTimeout)
		default:
			out = "error: " + err.Error()
		}
	}
	return capToolResult(out)
}

func causeText(ctx context.Context) string {
	switch context.Cause(ctx) {
	case errInterrupt:
		return "interrupted by the owner"
	case errCancel:
		return "cancelled"
	case errHandoff:
		return toolLostToRestart[1 : len(toolLostToRestart)-1]
	}
	return "stopped"
}

// settleResult settles one call's placeholder. After a handoff the successor
// owns the transcript, so nothing is written (fenced fails, or the base
// context is gone): repair there answers the call.
func (e *Engine) settleResult(ts *turnState, tc toolCall, content string) {
	if context.Cause(e.base) == errHandoff {
		return
	}
	_ = e.fenced(func(t *DB) error {
		ok, err := t.casToolResult(ts.run.ID, tc.ID, content)
		if err != nil || !ok {
			return err
		}
		if id, _, err := t.toolResultRow(ts.run.ID, tc.ID); err == nil {
			e.emitMessageID(t, ts.root, ts.run.ID, id)
		}
		return nil
	})
}

// notExecuted answers the calls after a control tool: the step ended there.
func (e *Engine) notExecuted(t *DB, ts *turnState, rest []toolCall, why string) {
	for _, tc := range rest {
		if ok, _ := t.casToolResult(ts.run.ID, tc.ID, "(not executed: "+why+")"); ok {
			if id, _, err := t.toolResultRow(ts.run.ID, tc.ID); err == nil {
				e.emitMessageID(t, ts.root, ts.run.ID, id)
			}
		}
	}
}

// settleCall rewrites one placeholder inside a transaction and emits it.
func (e *Engine) settleCall(t *DB, ts *turnState, tc toolCall, content string) {
	if ok, _ := t.casToolResult(ts.run.ID, tc.ID, content); ok {
		if id, _, err := t.toolResultRow(ts.run.ID, tc.ID); err == nil {
			e.emitMessageID(t, ts.root, ts.run.ID, id)
		}
	}
}

// controlTool handles finish / ask_user / yield — each ends the step.
func (e *Engine) controlTool(ctx context.Context, ts *turnState, tc toolCall, rest []toolCall) {
	run := ts.run
	args, _ := callArgs(tc, ts.own)
	switch tc.Function.Name {
	case "finish":
		result, _ := args["result"].(string)
		_ = e.fenced(func(t *DB) error {
			e.settleCall(t, ts, tc, "finished")
			e.notExecuted(t, ts, rest, "the run finished")
			e.demoteStep(t, ts, "the run finished")
			return e.endTurnTx(t, ts, endFinished, result)
		})
	case "ask_user":
		q, _ := args["question"].(string)
		if run.ParentID != 0 {
			// A subagent has nobody to ask: report the blocker as its result
			// and let its parent decide.
			msg := "BLOCKED: " + q
			_ = e.fenced(func(t *DB) error {
				e.settleCall(t, ts, tc, "(you are a subagent and cannot reach the human; reported the blocker to your parent instead)")
				e.notExecuted(t, ts, rest, "run ended")
				if err := t.setStatus(run.ID, statusDone, 0, msg, ""); err != nil {
					return err
				}
				e.emitStep(t, ts.root, t.journal(run.ID, "finish", map[string]string{"result": msg}))
				e.settleOwnLink(t, run, linkDone, outcomeIncomplete, msg)
				e.ag.cancelBelow(t, run.ID, "its parent's turn ended")
				e.emitRun(t, run.ID)
				return nil
			})
			return
		}
		_ = e.fenced(func(t *DB) error {
			e.settleCall(t, ts, tc, "(asked the user; awaiting their reply)")
			e.notExecuted(t, ts, rest, "run paused")
			e.demoteStep(t, ts, "the run is waiting for the owner")
			e.emitStep(t, ts.root, t.journal(run.ID, "ask", map[string]string{"kind": "ask_user", "question": q}))
			if err := t.setStatus(run.ID, statusWaiting, 0, q, ""); err != nil {
				return err
			}
			e.emitRun(t, run.ID)
			t.AfterCommit(func() { publishEvent(run.ID, "waiting_input") })
			return nil
		})
	case "yield":
		secs := toInt(args["seconds"])
		if secs < 0 {
			secs = 0
		}
		wake := e.unix() + int64(secs)
		err := e.fenced(func(t *DB) error {
			e.settleCall(t, ts, tc, fmt.Sprintf("(yielded %ds)", secs))
			e.notExecuted(t, ts, rest, "run paused")
			e.demoteStep(t, ts, "the run went to sleep")
			e.emitStep(t, ts.root, t.journal(run.ID, "yield", map[string]any{"seconds": secs}))
			if err := t.setStatus(run.ID, statusSleep, wake, run.Result, ""); err != nil {
				return err
			}
			e.emitRun(t, run.ID)
			return nil
		})
		if err == nil {
			e.armTimer(run.ID, wake)
		}
	}
}

// parkIfWaiting parks the run on what this step started waiting for (a
// foreground subagent, a subagent_wait) — or, when it all resolved already,
// carries on. true = parked.
func (e *Engine) parkIfWaiting(ts *turnState) bool {
	run := ts.run
	var open int
	_ = e.db.q.QueryRow(`SELECT count(*) FROM links WHERE parent_id=? AND mode='fg' AND delivered=0`, run.ID).Scan(&open)
	if open == 0 && len(ts.waits) == 0 {
		return false
	}
	var wake int64
	err := e.fenced(func(t *DB) error {
		deadline := t.earliestDeadline(run.ID)
		for _, w := range ts.waits {
			if w.Deadline > 0 && (deadline == 0 || w.Deadline < deadline) {
				deadline = w.Deadline
			}
		}
		pend, _ := json.Marshal(pendingState{Kind: "await", Waits: ts.waits})
		if err := t.setStatus(run.ID, statusAwait, deadline, run.Result, string(pend)); err != nil {
			return err
		}
		e.emitRun(t, run.ID)
		wake = deadline
		return nil
	})
	if err != nil {
		return true
	}
	if wake > 0 {
		e.armTimer(run.ID, wake)
	}
	// A child may have settled before we parked: look again at once.
	e.Poke(run.ID)
	return true
}

func toolNames(calls []toolCall) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.Function.Name
	}
	return out
}

func capToolResult(s string) string {
	if len(s) <= maxToolResult {
		return s
	}
	head, tail := maxToolResult*3/4, maxToolResult/4
	h := strings.ToValidUTF8(s[:head], "")
	t := strings.ToValidUTF8(s[len(s)-tail:], "")
	return fmt.Sprintf("%s\n…[%d bytes elided — output truncated; narrow the query/read a range if you need the middle]…\n%s",
		h, len(s)-len(h)-len(t), t)
}
