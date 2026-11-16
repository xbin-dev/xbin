// ws_brief_kinds.go — brief templates: the shape a known kind of hand-off
// takes, filled by the workspace agent with what only it knows (the
// specifics) and by this backend with what is always true of the kind (how
// to tell it is done, how to check, what not to touch). tile_agent_brief
// takes one by its id (`template`); the fields the model leaves out come
// from it, the ones it gives are kept, and leave_alone and known always
// carry the kind's own lines.
//
// One kind so far: "mcp-tool" — a tool an agent instance should get, built
// in an MCP tile made from the mcp-server template (docs/mcp.md): goal the
// tool's name, arguments and result; done when tools/list names it and a
// call returns that shape; check by the two calls from the tile's own
// sandbox; leave alone the manifest's provides and the wire.
package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

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
			"description": "a brief template for a known kind of work; its fields fill what you leave out. \"mcp-tool\": a tool an agent instance (an assistant) should get, built in an MCP tile made from the mcp-server template — give `tool`, and goal and done_when may be left out"},
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
	goal := fmt.Sprintf("An MCP tool `%s` in this tile, which an agent instance bound to this tile's `mcp` provide can call. Arguments: %s. It returns: %s.", name, args, result)
	if v := strings.TrimSpace(f.Goal); v != "" {
		goal += "\nIn your person's words: " + v
	}
	f.Goal = goal
	if strings.TrimSpace(f.DoneWhen) == "" {
		f.DoneWhen = fmt.Sprintf("tools/list on this tile's /mcp names `%s` with an inputSchema for its arguments, and a tools/call of `%s` returns %s (isError false; the text a model reads, and structuredContent where it has a shape).", name, name, result)
	}
	if strings.TrimSpace(f.CheckBy) == "" {
		f.CheckBy = "the two calls from this tile's own sandbox, then `go test ./...` in _backend:\n" +
			"curl -s -H \"Authorization: Bearer $XBIN_TOKEN\" -H 'Content-Type: application/json' -d '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}' \"$XBIN_URL/api/$XBIN_COMPONENT/mcp\"\n" +
			"curl -s -H \"Authorization: Bearer $XBIN_TOKEN\" -H 'Content-Type: application/json' -d '{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"" + name + "\",\"arguments\":{}}}' \"$XBIN_URL/api/$XBIN_COMPONENT/mcp\" (with arguments filled in)"
	}
	leave := "the manifest's \"provides\" (the `mcp` provide an agent's slot binds: kind http, service mcp, role reader) and the wire in _backend/mcp.go"
	if v := strings.TrimSpace(f.LeaveAlone); v != "" {
		leave += "; " + v
	}
	f.LeaveAlone = leave
	known := "This tile is a copy of the mcp-server template: tools go in _backend/tools.go (one function returning a Tool, and its line in the list; _backend/dice.go is the example), and its AGENTS.md says how to add one and check it. /docs/mcp.md has the rest."
	if v := strings.TrimSpace(f.Known); v != "" {
		known += " " + v
	}
	f.Known = known
	return nil
}
