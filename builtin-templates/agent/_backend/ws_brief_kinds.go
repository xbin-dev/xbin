// ws_brief_kinds.go — brief templates: the shape a known kind of hand-off
// takes, filled by the workspace agent with what only it knows (the
// specifics) and by this backend with what is always true of the kind (how
// to tell it is done, how to check, what not to touch). tile_agent_brief
// takes one by its id (`template`); the fields the model leaves out come
// from it, the ones it gives are kept, and leave_alone and known always
// carry the kind's own lines.
//
// One kind so far: "mcp-tool" — a tool an agent instance should get, built
// as an MCP server in a tile of its own, which may be empty when the tile
// agent starts (docs/mcp.md is the interface and carries a whole server):
// goal the tool's name, arguments and result; done when the manifest
// declares provides.mcp exactly as an agent's slot binds it, tools/list
// names the tool and a call returns that shape; check by the two calls
// from the tile's own sandbox; known: where the interface is and which
// files a Go backend tile needs; leave alone everything outside the tile,
// and the provide's role — a plan's binding waits for exactly that provide
// (xbind's planwait.go), and a wider one is not bound.
package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// wsMCPProvide is the provide an MCP tool's tile declares, as an agent's
// mcp slot binds it and as a plan's waiting line is pinned to it.
const wsMCPProvide = `"provides": {"mcp": {"kind": "http", "service": "mcp", "role": "reader"}}`

// wsBriefTemplateMCPTool is the id of the brief template for an MCP tool.
const wsBriefTemplateMCPTool = "mcp-tool"

// wsBriefTemplates are the ids tile_agent_brief's `template` takes.
var wsBriefTemplates = []string{wsBriefTemplateMCPTool}

// wsBriefTool is the tool a "mcp-tool" brief is for.
type wsBriefToolAsk struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Result    string `json:"result"`
}

// wsBriefFields are a brief's fields as the model gave them.
type wsBriefFields struct {
	Goal, DoneWhen, LeaveAlone, CheckBy, Known string
}

var wsToolNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// wsBriefTemplateProps are tile_agent_brief's parameters for a template.
func wsBriefTemplateProps() map[string]any {
	return map[string]any{
		"template": map[string]any{"type": "string", "enum": wsBriefTemplates,
			"description": "a brief template for a known kind of work; its fields fill what you leave out. \"mcp-tool\": a tool an agent instance (an assistant) should get, built as an MCP server in a tile of its own (an empty tile will do: the brief says what to write) — give `tool`, and goal and done_when may be left out"},
		"tool": obj([]string{"name", "result"}, map[string]any{
			"name":      strProp("the tool's name as a model calls it: a–z, 0–9, _ and - (roll_dice)"),
			"arguments": strProp("its arguments, each with its type and default (sides: integer, default 6; count: integer, default 1) — or \"none\""),
			"result":    strProp("what a call returns (each roll and their sum)"),
		}),
	}
}

// withBriefTemplate is tile_agent_brief's properties with the template's.
func withBriefTemplate(props map[string]any) map[string]any {
	for k, v := range wsBriefTemplateProps() {
		props[k] = v
	}
	return props
}

// wsApplyBriefTemplate fills f from the template id with tool; f's own
// fields are kept. It is the brief's content, not a check: tile_agent_brief
// still requires a goal and a done_when afterwards.
func wsApplyBriefTemplate(id string, tool *wsBriefToolAsk, f *wsBriefFields) error {
	switch id {
	case "":
		if tool != nil && tool.Name != "" {
			return errors.New(`tool: only with template "mcp-tool"`)
		}
		return nil
	case wsBriefTemplateMCPTool:
	default:
		return fmt.Errorf("template: one of %s", strings.Join(wsBriefTemplates, ", "))
	}
	if tool == nil || strings.TrimSpace(tool.Name) == "" || strings.TrimSpace(tool.Result) == "" {
		return errors.New(`tool: {name, arguments, result} — the tool the template "mcp-tool" is for`)
	}
	name := strings.TrimSpace(tool.Name)
	if !wsToolNameRe.MatchString(name) {
		return fmt.Errorf("tool.name: a–z, 0–9, _ and -, at most 64 (%q)", name)
	}
	args := strings.TrimSpace(tool.Arguments)
	if args == "" || strings.EqualFold(args, "none") {
		args = "none"
	}
	result := strings.TrimRight(strings.TrimSpace(tool.Result), ".")
	goal := fmt.Sprintf("Make this tile an MCP server with a tool `%s`, which an agent instance bound to this tile's `mcp` provide can call. Arguments: %s. It returns: %s.", name, args, result)
	if v := strings.TrimSpace(f.Goal); v != "" {
		goal += "\nIn your person's words: " + v
	}
	f.Goal = goal
	if strings.TrimSpace(f.DoneWhen) == "" {
		f.DoneWhen = fmt.Sprintf("this tile's xbin.json declares "+wsMCPProvide+" (with \"runtime\": \"go\"), tools/list on this tile's /mcp names `%s` with an inputSchema for its arguments, and a tools/call of `%s` returns %s (isError false; the text a model reads, and structuredContent where it has a shape).", name, name, result)
	}
	if strings.TrimSpace(f.CheckBy) == "" {
		f.CheckBy = "the two calls from this tile's own sandbox, once the backend has built:\n" +
			"curl -s -H \"Authorization: Bearer $XBIN_TOKEN\" -H 'Content-Type: application/json' -d '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}' \"$XBIN_URL/api/$XBIN_COMPONENT/mcp\"\n" +
			"curl -s -H \"Authorization: Bearer $XBIN_TOKEN\" -H 'Content-Type: application/json' -d '{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"" + name + "\",\"arguments\":{}}}' \"$XBIN_URL/api/$XBIN_COMPONENT/mcp\" (with arguments filled in)"
	}
	leave := "everything outside this tile; and the provide's role — \"reader\", exactly as above: xbin binds the assistant that waits for this tile only to that provide, and a wider role is not bound"
	if v := strings.TrimSpace(f.LeaveAlone); v != "" {
		leave += "; " + v
	}
	f.LeaveAlone = leave
	known := "The tile may be empty (a page and a manifest). /docs/mcp.md is the interface and carries a complete Go server to start from — read it from this sandbox: curl -s -H \"Authorization: Bearer $XBIN_TOKEN\" \"$XBIN_URL/docs/mcp.md?raw=1\". A Go backend tile is xbin.json (the manifest: with the provide, the \"expose\" role it names, as the page's manifest has it), go.mod (module <a name>, go 1.24, require github.com/xbin-dev/xbin/sdk v0.0.0 — xbin resolves the sdk), backend/main.go, and an API.md saying what the endpoint is (a tile that exposes a role ships one). xbin builds and restarts the backend on every save: you need not build it yourself, and a build error is in the tile's log (bx logs $XBIN_COMPONENT — bx takes the tile's full path, not its last part)."
	if v := strings.TrimSpace(f.Known); v != "" {
		known += " " + v
	}
	f.Known = known
	return nil
}
