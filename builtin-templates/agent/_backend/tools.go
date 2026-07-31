// tools.go — the tool set the agent can call. Opinionated builtins plus a seam
// for MCP-sourced tools. Control-flow tools (finish/ask_user/yield/
// spawn_subagent) are advertised here but handled by the loop (loop.go), since
// they change run status; the rest execute here. These builtins are the parts
// you extend per instance — especially xbin_call, the point of the agent:
// moving data between xbin components (bounded by grants a human approved).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// obj builds a JSON-Schema object node from (name, schema) pairs.
func obj(required []string, props map[string]any) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// toolSpecs returns the tool specs advertised to the LLM for this run's config,
// including any MCP-sourced tools.
func toolSpecs(cfg Config, mcp []toolSpec) []toolSpec {
	specs := []toolSpec{
		{Type: "function", Function: funcDef{
			Name: "memory_set", Description: "Store a durable memory block (always kept in context). Use for facts, decisions, and running plans.",
			Parameters: obj([]string{"key", "value"}, map[string]any{"key": strProp("short block name"), "value": strProp("block contents")}),
		}},
		{Type: "function", Function: funcDef{
			Name: "memory_get", Description: "Read a memory block by key.",
			Parameters: obj([]string{"key"}, map[string]any{"key": strProp("block name")}),
		}},
		{Type: "function", Function: funcDef{
			Name: "note", Description: "Record a short visible note in the run timeline (for the human watching). No effect on control flow.",
			Parameters: obj([]string{"text"}, map[string]any{"text": strProp("the note")}),
		}},
		{Type: "function", Function: funcDef{
			Name: "xbin_call", Description: "Call another xbin component's API through the gateway (only components this agent has been granted). path like '/api/apps/other/thing'. Returns the response body.",
			Parameters: obj([]string{"method", "path"}, map[string]any{
				"method": strProp("HTTP method, e.g. GET or POST"),
				"path":   strProp("request path, e.g. /api/apps/calendar/events"),
				"body":   strProp("optional JSON request body"),
			}),
		}},
		// --- control-flow tools (handled by the loop) ---
		{Type: "function", Function: funcDef{
			Name: "finish", Description: "End the run with a final result.",
			Parameters: obj([]string{"result"}, map[string]any{"result": strProp("the outcome / answer")}),
		}},
		{Type: "function", Function: funcDef{
			Name: "ask_user", Description: "Pause and ask the human a question. The run resumes when they answer.",
			Parameters: obj([]string{"question"}, map[string]any{"question": strProp("what you need from the human")}),
		}},
		{Type: "function", Function: funcDef{
			Name: "yield", Description: "Sleep for a while, then resume automatically (durable). Use when you should wait before continuing.",
			Parameters: obj([]string{"seconds"}, map[string]any{"seconds": map[string]any{"type": "integer", "description": "how long to sleep"}}),
		}},
	}
	if cfg.feature("recall") {
		specs = append(specs, toolSpec{Type: "function", Function: funcDef{
			Name: "recall", Description: "Full-text search THIS run's entire history, including older turns compacted out of context. Use it to retrieve details you can no longer see. Returns the matching messages.",
			Parameters: obj([]string{"query"}, map[string]any{"query": strProp("search terms (plain words)")}),
		}})
	}
	specs = append(specs,
		toolSpec{Type: "function", Function: funcDef{
			Name: "schedule", Description: "Schedule future work: create a cron-agent that starts a run on a cadence. cron is a 5-field expression or '@every 30m'. Set watcher:true for a run that re-checks something and keeps only rounds where it reports a change.",
			Parameters: obj([]string{"cron", "goal"}, map[string]any{
				"cron":    strProp("5-field cron or @every <dur>"),
				"goal":    strProp("what each run should do"),
				"name":    strProp("optional label"),
				"watcher": map[string]any{"type": "boolean", "description": "watcher mode (discard no-change rounds)"},
			}),
		}},
		toolSpec{Type: "function", Function: funcDef{
			Name: "unschedule", Description: "Remove a cron-agent by its schedule id.",
			Parameters: obj([]string{"id"}, map[string]any{"id": map[string]any{"type": "integer", "description": "schedule id"}}),
		}})
	if cfg.feature("watcher") {
		specs = append(specs, toolSpec{Type: "function", Function: funcDef{
			Name: "state_changed", Description: "In a watcher run, report that the watched state changed since the last check, with a short summary. Calling this keeps the round in history; not calling it discards the round.",
			Parameters: obj([]string{"summary"}, map[string]any{"summary": strProp("what changed")}),
		}})
	}
	if cfg.feature("skills") {
		specs = append(specs,
			toolSpec{Type: "function", Function: funcDef{
				Name: "skills_list", Description: "List your saved skills (reusable procedures you authored) — names + one-line descriptions.",
				Parameters: obj(nil, map[string]any{}),
			}},
			toolSpec{Type: "function", Function: funcDef{
				Name: "skill_view", Description: "Load a saved skill's full content by name.",
				Parameters: obj([]string{"name"}, map[string]any{"name": strProp("skill name")}),
			}},
			toolSpec{Type: "function", Function: funcDef{
				Name: "skill_manage", Description: "Save or remove a skill. action 'save' upserts {name, description, content}; action 'remove' deletes {name}.",
				Parameters: obj([]string{"action", "name"}, map[string]any{
					"action":      strProp("save | remove"),
					"name":        strProp("skill name"),
					"description": strProp("one-line description (for save)"),
					"content":     strProp("the skill body — steps/knowledge (for save)"),
				}),
			}})
	}
	if cfg.Subagents {
		specs = append(specs, toolSpec{Type: "function", Function: funcDef{
			Name: "spawn_subagent", Description: "Delegate a focused task to a fresh subagent (its own context). Returns the subagent's final result. Emit several in one turn to run them in parallel.",
			Parameters: obj([]string{"task"}, map[string]any{
				"task":   strProp("the task for the subagent"),
				"system": strProp("optional system prompt override for the subagent"),
			}),
		}})
	}
	return append(specs, mcp...)
}

// isControlTool reports whether the loop, not runTool, handles this call.
func isControlTool(name string) bool {
	switch name {
	case "finish", "ask_user", "yield", "spawn_subagent":
		return true
	}
	return false
}

// sideEffect reports whether a tool mutates the world (gated by approval mode).
func sideEffect(name string) bool {
	switch name {
	case "xbin_call":
		return true
	}
	return strings.HasPrefix(name, "mcp:")
}

// runTool executes a non-control tool and returns its textual result.
func (ag *Agent) runTool(ctx context.Context, run *Run, cfg Config, name string, args map[string]any) (string, error) {
	switch name {
	case "memory_set":
		key, _ := args["key"].(string)
		val, _ := args["value"].(string)
		if key == "" {
			return "", fmt.Errorf("memory_set needs a key")
		}
		if err := ag.db.memorySet(run.ID, key, val); err != nil {
			return "", err
		}
		return "stored " + key, nil

	case "memory_get":
		key, _ := args["key"].(string)
		mem, err := ag.db.memory(run.ID)
		if err != nil {
			return "", err
		}
		if v, ok := mem[key]; ok {
			return v, nil
		}
		return "(no such memory block)", nil

	case "note":
		text, _ := args["text"].(string)
		ag.db.journal(run.ID, "note", map[string]string{"text": text})
		return "noted", nil

	case "state_changed":
		summary, _ := args["summary"].(string)
		ag.markWatcherChanged(run.ID)
		ag.db.journal(run.ID, "state_changed", map[string]string{"summary": summary})
		return "change recorded", nil

	case "schedule":
		s := &Schedule{
			Name:    fmt.Sprint(args["name"]),
			Cron:    strings.TrimSpace(fmt.Sprint(args["cron"])),
			Goal:    strings.TrimSpace(fmt.Sprint(args["goal"])),
			Watcher: args["watcher"] == true,
		}
		if s.Name == "<nil>" {
			s.Name = ""
		}
		if s.Cron == "" || s.Goal == "" {
			return "", fmt.Errorf("schedule needs cron and goal")
		}
		if s.Watcher && !cfg.feature("watcher") {
			return "", fmt.Errorf("watcher mode is disabled in Features")
		}
		id, err := ag.db.createSchedule(s)
		if err != nil {
			return "", err
		}
		s.ID, s.Enabled = id, true
		if err := ag.registerScheduleCron(s); err != nil {
			_ = ag.db.deleteSchedule(id)
			return "", fmt.Errorf("bad schedule: %w", err)
		}
		return fmt.Sprintf("scheduled #%d (%s)", id, s.Cron), nil

	case "unschedule":
		id := int64(toInt(args["id"]))
		ag.unregisterScheduleCron(id)
		if err := ag.db.deleteSchedule(id); err != nil {
			return "", err
		}
		return fmt.Sprintf("removed schedule #%d", id), nil

	case "recall":
		q := strings.TrimSpace(fmt.Sprint(args["query"]))
		if q == "" {
			return "", fmt.Errorf("recall needs a query")
		}
		msgs, err := ag.db.searchMessages(run.ID, ftsQuery(q), 8)
		if err != nil {
			return "", err
		}
		if len(msgs) == 0 {
			return "(no matches)", nil
		}
		var b strings.Builder
		for _, m := range msgs {
			fmt.Fprintf(&b, "[#%d %s] %s\n", m.Seq, m.Role, clip(m.Content, 500))
		}
		return strings.TrimSpace(b.String()), nil

	case "xbin_call":
		return ag.toolXBinCall(ctx, args)

	case "skills_list", "skill_view", "skill_manage":
		return ag.runSkillTool(name, args)
	}

	if strings.HasPrefix(name, "mcp:") {
		return ag.mcpCall(ctx, cfg, name, args)
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

// toolXBinCall lets the agent reach other components through the gateway.
// Attribution + policy are the xbin gateway's job: the call only succeeds for
// components this element holds a grant on — the agent cannot widen its own
// reach (docs/auth.md).
func (ag *Agent) toolXBinCall(ctx context.Context, args map[string]any) (string, error) {
	method, _ := args["method"].(string)
	path, _ := args["path"].(string)
	body, _ := args["body"].(string)
	if method == "" {
		method = http.MethodGet
	}
	if !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("path must start with /")
	}
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), "http://xbin"+path, rdr)
	if err != nil {
		return "", err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, strings.TrimSpace(string(out))), nil
}

// decodeArgs parses a tool call's JSON argument string into a map.
func decodeArgs(raw string) map[string]any {
	m := map[string]any{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	return m
}
