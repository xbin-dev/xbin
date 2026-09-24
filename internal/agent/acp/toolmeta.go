package acp

// Tool-call normalization (D77). The adapters say more about a tool call
// than ACP's own fields: the programmatic tool name, a human description of
// a shell command (claude keeps it out of `title`, which is the command),
// the subagent a call belongs to, terminal output riding _meta. This lifts
// those into plain event fields so every client renders from one record.
// Readers are keyed by _meta namespace (claudeCode, codex, and the
// terminal_* keys claude-agent-acp and codex-acp share) and check shapes
// strictly; anything unrecognised is ignored — the ACP extensibility rule.

import (
	"encoding/json"

	"github.com/xbin-dev/xbin/internal/agent"
)

// clientMeta is clientCapabilities._meta: the adapter extensions this client
// renders, so the adapters send them — a shell call's output as the call's
// own _meta (claude: once, when it completes; codex streams deltas) instead
// of a bare "[terminal id]" we could never resolve (we host no ACP terminals);
// a subagent's own text and thinking, tagged with the call it runs under
// (claude keeps them internal otherwise — the Task card would be a black box).
func clientMeta() map[string]any {
	return map[string]any{"terminal_output": true, "terminal_output_delta": true, "subagent-transcript": true}
}

// toolMeta is the part of a tool call's _meta we read.
type toolMeta struct {
	ToolName   string `json:"tool_name"` // the legacy key, from before tool calls had a name
	ClaudeCode struct {
		ToolName        string `json:"toolName"`
		Title           string `json:"title"`           // a shell command's description
		ParentToolUseID string `json:"parentToolUseId"` // on everything a subagent does
		Subagent        bool   `json:"subagent"`        // on the Task/Agent call itself
	} `json:"claudeCode"`
	Codex struct {
		Kind string `json:"kind"` // "plan_review": the plan-mode approval request
	} `json:"codex"`
	TerminalOutput *struct {
		Data string `json:"data"`
	} `json:"terminal_output"`
	TerminalOutputDelta *struct {
		Data string `json:"data"`
	} `json:"terminal_output_delta"`
	TerminalExit *struct {
		ExitCode *int `json:"exit_code"`
	} `json:"terminal_exit"`
}

// readToolMeta decodes each key on its own, so a key in a shape we don't
// know is dropped alone (a whole-object decode half-fills on a type error).
func readToolMeta(raw json.RawMessage) (m toolMeta) {
	var keys map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &keys) != nil {
		return m
	}
	for k, v := range keys {
		var one toolMeta
		kq, _ := json.Marshal(k)
		if json.Unmarshal([]byte(`{`+string(kq)+`:`+string(v)+`}`), &one) != nil {
			continue
		}
		switch k {
		case "tool_name":
			m.ToolName = one.ToolName
		case "claudeCode":
			m.ClaudeCode = one.ClaudeCode
		case "codex":
			m.Codex = one.Codex
		case "terminal_output":
			m.TerminalOutput = one.TerminalOutput
		case "terminal_output_delta":
			m.TerminalOutputDelta = one.TerminalOutputDelta
		case "terminal_exit":
			m.TerminalExit = one.TerminalExit
		}
	}
	return m
}

// toolName is the call's programmatic name: the first-class field, else the
// legacy meta key, else claude's.
func toolName(name *string, m toolMeta) string {
	switch {
	case name != nil && *name != "":
		return *name
	case m.ToolName != "":
		return m.ToolName
	}
	return m.ClaudeCode.ToolName
}

// addToolExtras adds the normalized fields to a tool.call/tool.update event:
// name, label (a human headline), parent (the subagent call it belongs to),
// subagent, planReview, and the terminal output (outputDelta appends, output
// replaces, exitCode).
func addToolExtras(d map[string]any, u ToolCallUpdate) {
	m := readToolMeta(u.Meta)
	name := toolName(u.Name, m)
	if name != "" {
		d["name"] = name
	}
	if m.ClaudeCode.Title != "" {
		d["label"] = m.ClaudeCode.Title
	} else if s := rawString(u.RawInput, "description"); s != "" {
		d["label"] = s // claude's Bash, gemini's run_shell_command
	}
	if m.ClaudeCode.ParentToolUseID != "" {
		d["parent"] = m.ClaudeCode.ParentToolUseID
	}
	if m.ClaudeCode.Subagent || name == "Task" || name == "Agent" {
		d["subagent"] = true
	}
	if m.Codex.Kind == "plan_review" {
		d["planReview"] = true
	}
	if m.TerminalOutputDelta != nil && m.TerminalOutputDelta.Data != "" {
		d["outputDelta"] = m.TerminalOutputDelta.Data
	}
	if m.TerminalOutput != nil {
		d["output"] = m.TerminalOutput.Data
	}
	if m.TerminalExit != nil && m.TerminalExit.ExitCode != nil {
		d["exitCode"] = *m.TerminalExit.ExitCode
	}
	if _, has := d["output"]; !has { // codex-acp: a finished command's whole output
		if s := rawString(u.RawOutput, "formatted_output"); s != "" {
			d["output"] = s
		}
	}
}

// toolRef is the permission request's view of its tool call.
func toolRef(u ToolCallUpdate) agent.ToolCallRef {
	tc := agent.ToolCallRef{ID: u.ToolCallID, RawInput: u.RawInput, Content: u.Content, Name: toolName(u.Name, readToolMeta(u.Meta))}
	if u.Title != nil {
		tc.Title = *u.Title
	}
	if u.Kind != nil {
		tc.Kind = *u.Kind
	}
	return tc
}

// withParent tags a message/thought delta with the subagent call it came
// from (claude forwards subagent text with _meta.claudeCode.parentToolUseId).
func withParent(d map[string]any, update json.RawMessage) map[string]any {
	var u struct {
		Meta json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(update, &u) == nil {
		if p := readToolMeta(u.Meta).ClaudeCode.ParentToolUseID; p != "" {
			d["parent"] = p
		}
	}
	return d
}

// rawString reads one string field of a JSON object ("" when absent or not
// a string).
func rawString(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m[key], &s) != nil {
		return ""
	}
	return s
}
