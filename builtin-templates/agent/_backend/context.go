// context.go — what the model sees: context assembly, tool names on the
// wire, and compaction.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"time"
)

// assembleContext builds the message list sent to the model: a system
// message (base prompt + date + memory blocks + skills + running summary)
// followed by the live (uncompacted) transcript. An assistant message carries
// its stored meta, so a wire can replay the reasoning it produced.
func (ag *Agent) assembleContext(ctx context.Context, run *Run, cfg Config) ([]wireMsg, error) {
	mem, err := ag.db.memory(run.ID)
	if err != nil {
		return nil, err
	}
	var sys strings.Builder
	sys.WriteString(cfg.System)
	// Date only (not a full timestamp) keeps the system prefix stable within a
	// day, so prompt caching keeps hitting.
	fmt.Fprintf(&sys, "\n\nToday's date (UTC): %s.", time.Now().UTC().Format("2006-01-02"))
	if len(mem) > 0 {
		sys.WriteString("\n\n# Memory blocks\n")
		for k, v := range mem {
			fmt.Fprintf(&sys, "- %s: %s\n", k, v)
		}
	}
	if cfg.feature("skills") {
		if skills := ag.db.visibleSkills(ag.scopeOf(run, cfg)); len(skills) > 0 {
			sys.WriteString("\n\n# Skills (call skill_view to load one's full steps)\n")
			for _, s := range skills {
				fmt.Fprintf(&sys, "- %s: %s\n", s.Name, s.Description)
			}
		}
	}
	live, err := ag.db.messages(run.ID, true)
	if err != nil {
		return nil, err
	}
	// Who said what, once more than one person is in the conversation (D83):
	// each person's message carries their id.
	senders := senders(live)
	shared := false
	if run.ParentID == 0 && len(senders) > 0 {
		shared = len(senders) > 1 || (run.Owner != "" && !senders[run.Owner])
	}
	if shared {
		sys.WriteString("\n\nThis conversation is shared: each person's message starts with [their id].")
	}
	if run.Summary != "" {
		sys.WriteString("\n\n# Summary of earlier conversation\n")
		sys.WriteString(run.Summary)
	}

	out := []wireMsg{{Role: "system", Content: sys.String()}}
	pos := make([]int, len(live))
	for i, m := range live {
		pos[i] = -1
		if m.Role == "system" {
			continue // the base/system prompt is rebuilt above
		}
		pos[i] = len(out)
		var content any = m.Content
		if m.Role == "user" {
			content = contentValue(m.Content)
			if s, ok := content.(string); ok && shared {
				if who := senderOf(m); who != "" {
					content = "[" + who + "] " + s
				}
			}
		}
		wm := wireMsg{Role: m.Role, Content: content, Name: m.Name, ToolCallID: m.ToolCallID}
		if m.ToolCalls != "" {
			_ = json.Unmarshal([]byte(m.ToolCalls), &wm.ToolCalls)
		}
		if m.Role == "assistant" && len(m.Meta) > 0 {
			var meta msgMeta
			if json.Unmarshal(m.Meta, &meta) == nil && len(meta.ReasoningRaw) > 0 {
				wm.Replay = &meta
			}
		}
		out = append(out, wm)
	}
	// Images are added here, at assembly, and never stored (attach.go).
	return ag.withImages(ctx, run, cfg, live, out, pos), nil
}

// validateWire checks the provider's contract before a request goes out:
// every assistant tool_calls block is followed immediately by exactly one
// tool message per call, and no tool message appears anywhere else.
func validateWire(msgs []wireMsg) error {
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Role {
		case "tool":
			return fmt.Errorf("message %d: a tool result with no call before it (%s)", i, m.ToolCallID)
		case "assistant":
			if len(m.ToolCalls) == 0 {
				continue
			}
			want := map[string]bool{}
			for _, c := range m.ToolCalls {
				if c.ID == "" {
					return fmt.Errorf("message %d: a tool call without an id", i)
				}
				want[c.ID] = true
			}
			j := i + 1
			for ; j < len(msgs) && msgs[j].Role == "tool"; j++ {
				if !want[msgs[j].ToolCallID] {
					return fmt.Errorf("message %d: result %s answers no call of this block (or answers one twice)", j, msgs[j].ToolCallID)
				}
				delete(want, msgs[j].ToolCallID)
			}
			if len(want) > 0 {
				return fmt.Errorf("message %d: %d tool call(s) without a result", i, len(want))
			}
			i = j - 1
		}
	}
	return nil
}

// --- tool names on the wire ---------------------------------------------------

// OpenAI-compatible providers accept tool names matching this; MCP tools are
// named "mcp:<server>:<tool>" internally (routing needs the server), so the
// name is translated at the wire boundary, both ways, per request.
var wireNameOK = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var wireNameBad = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func wireToolName(name string) string {
	if wireNameOK.MatchString(name) {
		return name
	}
	s := wireNameBad.ReplaceAllString(name, "_")
	if len(s) > 64 || s != name {
		h := fnv.New32a()
		_, _ = h.Write([]byte(name))
		suffix := fmt.Sprintf("_%08x", h.Sum32())
		if len(s) > 64-len(suffix) {
			s = s[:64-len(suffix)]
		}
		s += suffix
	}
	return s
}

// wireNames rewrites the tool names in a request; back maps a wire name to
// the internal one for the reply.
func wireNames(msgs []wireMsg, specs []toolSpec) ([]wireMsg, []toolSpec, map[string]string) {
	back := map[string]string{}
	name := func(n string) string {
		w := wireToolName(n)
		if w != n {
			back[w] = n
		}
		return w
	}
	outSpecs := make([]toolSpec, len(specs))
	for i, s := range specs {
		outSpecs[i] = s
		outSpecs[i].Function.Name = name(s.Function.Name)
	}
	outMsgs := make([]wireMsg, len(msgs))
	for i, m := range msgs {
		outMsgs[i] = m
		if len(m.ToolCalls) > 0 {
			calls := make([]toolCall, len(m.ToolCalls))
			copy(calls, m.ToolCalls)
			for j := range calls {
				calls[j].Function.Name = name(calls[j].Function.Name)
			}
			outMsgs[i].ToolCalls = calls
		}
		if m.Role == "tool" && m.Name != "" {
			outMsgs[i].Name = name(m.Name)
		}
	}
	return outMsgs, outSpecs, back
}

// --- compaction -------------------------------------------------------------------

// maybeCompact folds the oldest live turns into the running summary when the
// context exceeds the token budget (or when asked), never splitting an
// assistant tool-call from its results. A forced compaction consumes the
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
	total := estimateTokens(cfg.System) + estimateTokens(run.Summary)
	for _, m := range live {
		total += m.Tokens
	}
	if run.LastPromptTokens > 0 {
		total = run.LastPromptTokens // provider-reported truth beats the estimate
	}
	if !force && total <= cfg.TokenBudget {
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
	summary := e.summarize(ctx, run, cfg, run.Summary, buf.String())
	if summary == "" {
		return
	}
	ids := make([]int64, len(prefix))
	for i, m := range prefix {
		ids[i] = m.ID
	}
	_ = e.fenced(func(t *DB) error {
		if err := t.markCompacted(ids); err != nil {
			return err
		}
		if err := t.setSummary(run.ID, summary); err != nil {
			return err
		}
		t.setPromptTokens(run.ID, 1) // stale: the next call reports the new size
		e.emitStep(t, rootOf(run), t.journal(run.ID, "compaction",
			map[string]any{"messages": len(prefix), "summaryTokens": estimateTokens(summary)}))
		e.emitRun(t, run.ID)
		return nil
	})
}

func (e *Engine) consumeCompact(runID int64) {
	var ids []int64
	_ = e.fenced(func(t *DB) error {
		for _, r := range t.inboxRows(`WHERE run_id=? AND kind='compact' AND delivered_at=0`, runID) {
			if t.consume(r.ID, 0) {
				ids = append(ids, r.ID)
			}
		}
		return nil
	})
	for _, id := range ids {
		e.delivered(id)
	}
}

// summarize is one model call under the gate (it competes for the same slots
// as every other call).
func (e *Engine) summarize(ctx context.Context, run *Run, cfg Config, prior, transcript string) string {
	release, err := e.gate.acquire(ctx, run.Depth == 0)
	if err != nil {
		return ""
	}
	defer release()
	sys := "You compact a conversation for an AI agent. Merge the prior summary and the new transcript into a single concise summary that preserves facts, decisions, open threads, and anything needed to continue. Output only the summary."
	user := "Prior summary:\n" + prior + "\n\nNew transcript to fold in:\n" + transcript
	reply, err := e.llm.Chat(ctx, LLMRequest{
		Run: run.ID, Purpose: "compact",
		Model: modelFor(ctx, cfg, "memory"),
		Msgs:  []wireMsg{{Role: "system", Content: sys}, {Role: "user", Content: user}},
		Wire:  cfg.Wire,
	}, nil)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(asString(reply.Msg.Content))
}

// senderOf is who wrote a user message ("" for the agent's own prompts).
func senderOf(m *Message) string {
	if m.Role != "user" || len(m.Meta) == 0 {
		return ""
	}
	var meta msgMeta
	_ = json.Unmarshal(m.Meta, &meta)
	return meta.Sender
}

func senders(msgs []*Message) map[string]bool {
	out := map[string]bool{}
	for _, m := range msgs {
		if s := senderOf(m); s != "" {
			out[s] = true
		}
	}
	return out
}
