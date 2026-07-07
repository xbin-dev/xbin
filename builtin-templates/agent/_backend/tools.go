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

	xbin "github.com/magik6k/xbin/sdk"
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

	case "xbin_call":
		return ag.toolXBinCall(ctx, args)
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
