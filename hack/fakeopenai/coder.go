package main

import (
	"slices"
	"strings"
)

// coder.go — the keyword a coding CLI's turn is driven with, for the LLM
// connector's live runs (D226: real OpenCode and Codex, pointed at xbind's
// connector with this fake as its upstream). The agent template's keywords
// call the template's own tools; a coding CLI offers its own, so this one
// picks from what the request offers:
//
//	shell echo   the request's shell tool, running `echo connector-tool-ok`
//	             — OpenCode's bash {command, description}, Codex's shell
//	             {command: ["bash", "-lc", …]} or exec_command {cmd} — then
//	             "Shell said: <its output's first line>"; "No shell tool
//	             offered." when the request has none of them

// shellMarker is what the scripted command prints.
const shellMarker = "connector-tool-ok"

// coderScript answers the coding-CLI keyword; false for any other turn.
func coderScript(conv []turn, tools []string) (plan, bool) {
	last, lastUser := conv[len(conv)-1], ""
	for i := len(conv) - 1; i >= 0; i-- {
		if conv[i].Role == "user" {
			lastUser = strings.ToLower(conv[i].Text)
			break
		}
	}
	if !strings.Contains(lastUser, "shell echo") {
		return plan{}, false
	}
	if last.Role == "tool" {
		line := strings.TrimSpace(last.Text)
		if i := strings.Index(line, shellMarker); i >= 0 { // a CLI may wrap the output (JSON, a header)
			line = shellMarker
		} else if first, _, ok := strings.Cut(line, "\n"); ok {
			line = first
		}
		return plan{Text: "Shell said: " + line}, true
	}
	cmd := "echo " + shellMarker
	switch {
	case slices.Contains(tools, "bash"):
		return plan{Calls: []call{{"bash", map[string]any{"command": cmd, "description": "Print a marker"}}}}, true
	case slices.Contains(tools, "exec_command"):
		return plan{Calls: []call{{"exec_command", map[string]any{"cmd": cmd}}}}, true
	case slices.Contains(tools, "shell"):
		return plan{Calls: []call{{"shell", map[string]any{"command": []string{"bash", "-lc", cmd}}}}}, true
	case slices.Contains(tools, "local_shell"):
		return plan{Calls: []call{{"local_shell", map[string]any{"command": []string{"bash", "-lc", cmd}}}}}, true
	}
	return plan{Text: "No shell tool offered."}, true
}
