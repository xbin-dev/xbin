// compact.go — keeping a long run inside the model's window (D133).
//
// When the last call's prompt passed the budget, compaction runs in two
// stages:
//
//  1. Observation masking: tool results older than the newest keepObsSteps
//     steps are shown to the model as short stubs (messages.masked). The rows
//     and the search index keep every byte; message_get restores one. User
//     turns and the model's own calls stay verbatim.
//  2. Only if that was not enough: an LLM summary of the oldest turns, which
//     then leave the window (compacted=1). The summarizer is shown the pinned
//     task (to judge what matters, never to restate it) and the tool calls.
//     Summaries are kept as a history (summaries, searchable by recall);
//     runs.summary is the latest, for readers that know only it.
//
// The requests themselves are never summarised: they are in the task ledger
// (asks.go), pinned in every prompt. Masking alone matching or beating LLM
// summaries is the finding this follows (decision D133).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// legacyTokenBudget is the default every config stored before D133: it
	// reads as unset, so those runs get the window-derived budget too.
	legacyTokenBudget = 12000
	budgetFloor       = 32000 // the budget when the window is unknown, and its floor
	budgetShare       = 60    // % of the model's context window

	keepObsSteps     = 5    // the tool results of the newest steps are never masked
	maskMinBytes     = 1200 // a smaller result costs about what its stub does
	maskEnough       = 75   // % of the budget stage 1 must reach to skip stage 2
	keepAfterSummary = 50   // % of the budget the live window is cut back to
	keepTailMsgs     = 6    // a summary keeps at least these newest messages
	summaryAllowance = 1500 // tokens set aside for the new summary
	foldMaxChars     = 120000
)

// tokenBudget is the compaction trigger in prompt tokens, and where it came
// from. An explicit config value wins; otherwise 60% of the model's context
// window as its provider lists it, at least 32000 (never more than 80% of
// the window); 32000 when the window is unknown.
func (e *Engine) tokenBudget(ctx context.Context, cfg Config) (int, string) {
	if cfg.TokenBudget > 0 && cfg.TokenBudget != legacyTokenBudget {
		return cfg.TokenBudget, "config"
	}
	if e.ag == nil || e.ag.noGateway {
		return budgetFloor, "default"
	}
	model := modelFor(ctx, cfg, "general")
	if w := contextWindow(ctx, model); w > 0 {
		return budgetForWindow(w), fmt.Sprintf("%d%% of %s's %d-token window", budgetShare, bareModel(model), w)
	}
	return budgetFloor, "default"
}

func budgetForWindow(w int) int {
	b := w * budgetShare / 100
	if b < budgetFloor {
		b = min(budgetFloor, w*80/100)
	}
	return b
}

// maybeCompact compacts the run when its context passed the budget (or when
// asked: force runs both stages). A forced compaction consumes the
// compaction requests queued for the run.
func (e *Engine) maybeCompact(ctx context.Context, ts *turnState, force bool) {
	defer func() {
		if force {
			e.consumeCompact(ts.run.ID)
		}
	}()
	run, err := e.db.getRun(ts.run.ID)
	if err != nil {
		return
	}
	cfg := ts.cfg
	live, err := e.db.messages(run.ID, true)
	if err != nil {
		return
	}
	jobs := e.db.jobsByCall(run.ID, live)
	sent := 0
	for _, m := range live {
		sent += sentTokens(m, jobs)
	}
	total := estimateTokens(cfg.System) + estimateTokens(run.Summary) + sent
	if run.LastPromptTokens > 0 {
		total = run.LastPromptTokens // provider-reported truth beats the estimate
	}
	budget, why := e.tokenBudget(ctx, cfg)
	if !force && total <= budget {
		return
	}
	overhead := max(total-sent, 0) // the system prompt, the tools: what cutting messages can't shrink

	// Stage 1: mask old tool outputs.
	mask := maskCandidates(live, keepObsSteps)
	saved := 0
	for _, m := range mask {
		saved += m.Tokens - estimateTokens(maskStub(m, jobs[m.ToolCallID]))
		m.Masked = true
	}
	detail := map[string]any{"budget": budget, "budgetFrom": why, "promptTokens": total}
	if len(mask) > 0 {
		detail["masked"], detail["savedTokens"] = len(mask), saved
	}
	// write commits what this compaction did; the turn goes on with the run
	// as it now is (its summary, the pending note).
	write := func(extra func(t *DB) error) {
		defer func() {
			if r, err := e.db.getRun(run.ID); err == nil {
				ts.run = r
			}
		}()
		_ = e.fenced(func(t *DB) error {
			if err := t.markMasked(mask); err != nil {
				return err
			}
			if extra != nil {
				if err := extra(t); err != nil {
					return err
				}
			}
			_, _ = t.q.Exec(`UPDATE runs SET compact_note=1 WHERE id=?`, run.ID)
			t.setPromptTokens(run.ID, 1) // stale: the next call reports the new size
			e.emitStep(t, rootOf(run), t.journal(run.ID, "compaction", detail))
			e.emitRun(t, run.ID)
			return nil
		})
	}
	if !force && total-saved <= budget*maskEnough/100 {
		if len(mask) > 0 {
			write(nil)
		}
		return
	}

	// Stage 2: summarise the oldest turns.
	cut := foldCut(live, jobs, force, budget*keepAfterSummary/100-overhead-summaryAllowance)
	if cut <= 0 {
		if len(mask) > 0 {
			write(nil)
		}
		return
	}
	prefix := live[:cut]
	asks, _ := e.db.asks(run.ID)
	summary := e.summarize(ctx, run, cfg, taskBlock(asks), run.Summary, foldText(prefix, asks, jobs))
	if summary == "" {
		if len(mask) > 0 {
			write(nil)
		}
		return
	}
	ids := make([]int64, len(prefix))
	for i, m := range prefix {
		ids[i] = m.ID
	}
	detail["messages"], detail["summaryTokens"] = len(prefix), estimateTokens(summary)
	write(func(t *DB) error {
		if err := t.markCompacted(ids); err != nil {
			return err
		}
		if err := t.setSummary(run.ID, summary); err != nil {
			return err
		}
		return t.addSummary(run.ID, summary, prefix[len(prefix)-1].Seq, len(prefix))
	})
}

// maskCandidates are the tool results stage 1 hides: older than the newest
// keep steps (an assistant message that called tools), big enough to be worth
// a stub, settled, and not hidden already.
func maskCandidates(live []*Message, keep int) []*Message {
	steps, bound := 0, -1
	for i := len(live) - 1; i >= 0; i-- {
		if live[i].Role == "assistant" && live[i].ToolCalls != "" {
			if steps++; steps == keep {
				bound = i
				break
			}
		}
	}
	if bound < 0 {
		return nil
	}
	var out []*Message
	for _, m := range live[:bound] {
		if m.Role == "tool" && !m.Masked && len(m.Content) >= maskMinBytes && !isPlaceholder(m.Content) {
			out = append(out, m)
		}
	}
	return out
}

// foldCut is where stage 2 cuts: the newest messages that fit in keep tokens
// stay (at least keepTailMsgs; a forced compaction keeps just those), and a
// tool result never leaves without its call.
func foldCut(live []*Message, jobs map[string]int, force bool, keep int) int {
	n := len(live)
	cut := n - keepTailMsgs
	if !force {
		i, acc := n, 0
		for i > 0 && acc+sentTokens(live[i-1], jobs) <= keep {
			acc += sentTokens(live[i-1], jobs)
			i--
		}
		cut = min(i, cut)
	}
	if cut < 1 {
		cut = n / 2
	}
	if cut < 1 {
		return 0
	}
	for cut < n && live[cut].Role == "tool" {
		cut++
	}
	if cut >= n {
		return 0
	}
	return cut
}

// sentTokens is what a message costs in the prompt: its stub when masked.
func sentTokens(m *Message, jobs map[string]int) int {
	if m.Masked && m.Role == "tool" {
		return estimateTokens(maskStub(m, jobs[m.ToolCallID]))
	}
	return m.Tokens
}

// maskStub is how a masked tool result reads to the model: what it was, how
// to get it back, and its first and last words.
func maskStub(m *Message, job int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s output, %s — hidden to save context; message_get {\"seq\": %d} shows it",
		orStr(m.Name, "tool"), humanBytes(len(m.Content)), m.Seq)
	if job > 0 {
		fmt.Fprintf(&b, ", or bash_output {\"job\": %d, \"offset\": 0} rereads the job while the sandbox keeps it", job)
	}
	b.WriteString("]")
	c := strings.TrimSpace(m.Content)
	if c == "" {
		return b.String()
	}
	const head, tail = 200, 160
	if len(c) <= head+tail {
		fmt.Fprintf(&b, "\n%s", flatClip(c, head+tail))
		return b.String()
	}
	fmt.Fprintf(&b, "\nbegins: %s\nends: …%s", flatClip(c[:head], head),
		flatClip(strings.ToValidUTF8(c[len(c)-tail:], ""), tail))
	return b.String()
}

// jobsByCall maps a tool call to the sandbox job its result is about: the job
// a bash call started, or the one a bash_output/bash_kill call named.
func (d *DB) jobsByCall(runID int64, live []*Message) map[string]int {
	out := map[string]int{}
	rows, err := d.q.Query(`SELECT tool_call_id, job FROM sandbox_jobs WHERE run_id=? AND tool_call_id<>''`, runID)
	if err == nil {
		for rows.Next() {
			var tc string
			var job int
			if rows.Scan(&tc, &job) == nil {
				out[tc] = job
			}
		}
		rows.Close()
	}
	for _, m := range live {
		if m.Role != "assistant" || m.ToolCalls == "" {
			continue
		}
		var calls []toolCall
		if json.Unmarshal([]byte(m.ToolCalls), &calls) != nil {
			continue
		}
		for _, c := range calls {
			if c.Function.Name == "bash_output" || c.Function.Name == "bash_kill" {
				if n := toInt(decodeArgs(c.Function.Arguments)["job"]); n > 0 {
					out[c.ID] = n
				}
			}
		}
	}
	return out
}

func (d *DB) markMasked(ms []*Message) error {
	for _, m := range ms {
		if _, err := d.q.Exec(`UPDATE messages SET masked=1 WHERE id=?`, m.ID); err != nil {
			return err
		}
	}
	return nil
}

// addSummary appends a summary to the run's history and its search index.
func (d *DB) addSummary(runID int64, text string, uptoSeq, n int) error {
	var id int64
	if err := d.q.QueryRow(`INSERT INTO summaries (run_id, text, upto_seq, messages, at) VALUES (?, ?, ?, ?, ?) RETURNING id`,
		runID, text, uptoSeq, n, now()).Scan(&id); err != nil {
		return err
	}
	_, err := d.q.Exec(`INSERT INTO summaries_fts(text, run_id, sum_id) VALUES (?, ?, ?)`, text, runID, id)
	return err
}

// foldText is the transcript stage 2 folds, for the summarizer: requests by
// reference only (they are pinned verbatim), the model's calls with their
// arguments, tool results cut (or their stubs).
func foldText(prefix []*Message, asks []*Ask, jobs map[string]int) string {
	isAsk := map[int64]*Ask{}
	for _, a := range asks {
		isAsk[a.MsgID] = a
	}
	var parts []string
	for _, m := range prefix {
		switch m.Role {
		case "user":
			if a := isAsk[m.ID]; a != nil {
				parts = append(parts, fmt.Sprintf("#%d user (%s): [a request — pinned verbatim in the task; do not restate it]", m.Seq, askWho(a)))
			} else {
				parts = append(parts, fmt.Sprintf("#%d user: %s", m.Seq, clip(m.Content, 2000)))
			}
		case "assistant":
			s := fmt.Sprintf("#%d assistant: %s", m.Seq, clip(m.Content, 3000))
			var calls []toolCall
			if m.ToolCalls != "" && json.Unmarshal([]byte(m.ToolCalls), &calls) == nil {
				for _, c := range calls {
					s += fmt.Sprintf("\n  → %s %s", c.Function.Name, clip(c.Function.Arguments, 400))
				}
			}
			parts = append(parts, s)
		case "tool":
			body := clip(m.Content, 1500)
			if m.Masked {
				body = maskStub(m, jobs[m.ToolCallID])
			}
			parts = append(parts, fmt.Sprintf("#%d tool %s: %s", m.Seq, m.Name, body))
		}
	}
	s := strings.Join(parts, "\n")
	if len(s) > foldMaxChars {
		head, tail := foldMaxChars/3, foldMaxChars*2/3
		s = strings.ToValidUTF8(s[:head], "") +
			fmt.Sprintf("\n…[%d characters of this transcript left out — the agent can recall them]…\n", len(s)-head-tail) +
			strings.ToValidUTF8(s[len(s)-tail:], "")
	}
	return s
}

const summarizerSystem = "You compact an AI agent's working context. Merge the prior summary and the new transcript into one concise summary " +
	"that lets the agent continue: facts learned, decisions and their reasons, what was tried and what failed, the files, paths, ids " +
	"and commands that matter, the current state of the work, and open threads. The agent's task is pinned verbatim in its context and " +
	"outranks this summary: never restate, paraphrase or reinterpret it, and refer to requests by their #number. Cite #numbers for " +
	"details the agent may want to read again (message_get restores any message). Output only the summary."

// summarize is one model call under the gate (it competes for the same slots
// as every other call).
func (e *Engine) summarize(ctx context.Context, run *Run, cfg Config, task, prior, transcript string) string {
	release, err := e.gate.acquire(ctx, run.Depth == 0)
	if err != nil {
		return ""
	}
	defer release()
	var user strings.Builder
	if task = strings.TrimSpace(task); task != "" {
		user.WriteString("The agent's task, pinned verbatim in its context — for your reference only; do not restate it:\n")
		user.WriteString(task + "\n\n")
	}
	user.WriteString("Prior summary:\n" + orStr(prior, "(none)") + "\n\nNew transcript to fold in:\n" + transcript)
	reply, err := e.llm.Chat(ctx, LLMRequest{
		Run: run.ID, Purpose: "compact",
		Model: modelFor(ctx, cfg, "memory"),
		Msgs:  []wireMsg{{Role: "system", Content: summarizerSystem}, {Role: "user", Content: user.String()}},
		Wire:  cfg.Wire,
	}, nil)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(asString(reply.Msg.Content))
}
