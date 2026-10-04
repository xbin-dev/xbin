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
// message (base prompt + the pinned task + date + sandbox + the model's notes
// + skills + running summary) followed by the live (uncompacted) transcript,
// masked tool results as their stubs, and the task reminder appended to the
// last message (asks.go; never stored). An assistant message carries its
// stored meta, so a wire can replay the reasoning it produced.
//
// Everything before the reminder changes only when a compaction runs, a note
// is written or the day turns — so the provider's prompt cache keeps hitting.
func (ag *Agent) assembleContext(ctx context.Context, run *Run, cfg Config) ([]wireMsg, error) {
	mem, err := ag.db.memory(run.ID)
	if err != nil {
		return nil, err
	}
	asks, err := ag.db.asks(run.ID)
	if err != nil {
		return nil, err
	}
	var sys strings.Builder
	sys.WriteString(cfg.System)
	sys.WriteString(taskBlock(asks)) // D133: verbatim, before anything the model wrote
	// Date only (not a full timestamp) keeps the system prefix stable within a
	// day, so prompt caching keeps hitting.
	fmt.Fprintf(&sys, "\n\nToday's date (UTC): %s.", time.Now().UTC().Format("2006-01-02"))
	sys.WriteString(sandboxPrompt(cfg))            // the bound sandbox; "" when none (sandbox_tools.go)
	sys.WriteString(ag.projectPromptFor(run, cfg)) // a project's task or coordinator: # Project (project_gate.go)
	sys.WriteString(notesBlock(mem))               // sorted: the same bytes every call
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
	var jobs map[string]int
	lastID := int64(0) // the newest message the model sees
	for i, m := range live {
		pos[i] = -1
		if m.Role == "system" {
			continue // the base/system prompt is rebuilt above
		}
		pos[i] = len(out)
		lastID = m.ID
		var content any = m.Content
		if m.Masked && m.Role == "tool" {
			if jobs == nil {
				jobs = ag.db.jobsByCall(run.ID, live)
			}
			content = maskStub(m, jobs[m.ToolCallID])
		}
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
	out = ag.withImages(ctx, run, cfg, live, out, pos)
	// The reminder: the task's title and latest request, unless the last
	// message is that request; the one-time note after a compaction.
	var latest *Ask
	if len(asks) > 0 {
		latest = asks[len(asks)-1]
	}
	return withReminder(out, reminderText(run.Title, latest, latest != nil && latest.MsgID != lastID, run.CompactNote)), nil
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

// --- compaction (compact.go) ---------------------------------------------------

// consumeCompact marks the compaction requests queued for a run delivered.
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
