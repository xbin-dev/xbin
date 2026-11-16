package main

// bx org agents / bx agent connection — org constraints on agents (D229,
// docs/auth.md §Agent constraints):
//
//	bx org agents [<org>] [--pin] [--workspace-agent on|off] [--harnesses any|none|<id,…>]
//	              [--models any|private|none|<id,…>] [--web ask|allow|never] [--mcp any|listed|org|never]
//	              [--credentials own-first|connector-first|connector-only] [--json]
//	    an org's rules for agents on its tiles — with --pin, the rules a
//	    workspace admin pins on it (its admins can't loosen those); with no
//	    org, the workspace's bounds (and its credential mode, which only
//	    the workspace has)
//	bx agent connection [--model <upstream/model>] [--orgs <id,…>] [--tiles <tile,…>]
//	                    [--what-if <upstream/model>] [--json]
//	    what a workspace agent session with that model and scope is connected to

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

const noAgentPolicyMsg = "this xbind has no agent constraints (no /api/xbin/agents/policy); upgrade xbind"

func init() {
	dcUsage["org agents"] = "bx org agents [<org>] [--pin] [--workspace-agent on|off] [--harnesses any|none|<id,…>] [--models any|private|none|<id,…>] [--web ask|allow|never] [--mcp any|listed|org|never] [--credentials own-first|connector-first|connector-only] [--json]"
	dcUsage["agent connection"] = "bx agent connection [--model <upstream/model>] [--orgs <id,…>] [--tiles <tile,…>] [--what-if <upstream/model>] [--json]"
}

// agentPolicyCall is dcCall; an xbind without the routes says so.
func agentPolicyCall(method, path string, body any) ([]byte, error) {
	b, err := dcCall(method, path, body)
	if e, ok := err.(*dcError); ok && e.code == exitNoDeployments && e.msg == oldXbindMsg {
		e.msg = noAgentPolicyMsg
	}
	return b, err
}

// flagValues reads "--flag value" and "--flag=value" for the flags named in
// takes, and --json; anything else is positional or an error.
func flagValues(cmd string, args []string, takes ...string) (pos []string, vals map[string]string, asJSON bool, err error) {
	vals = map[string]string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !isFlag(arg) {
			pos = append(pos, arg)
			continue
		}
		name, val, inline := strings.Cut(arg, "=")
		if name == "--json" && !inline {
			asJSON = true
			continue
		}
		known := false
		for _, t := range takes {
			known = known || t == name
		}
		if !known {
			return nil, nil, false, usageError(cmd, "unknown flag %s", name)
		}
		if !inline {
			v, err := nextArg(args, &i)
			if err != nil {
				return nil, nil, false, usageError(cmd, "%v", err)
			}
			val = v
		}
		vals[name] = val
	}
	return pos, vals, asJSON, nil
}

// allowValue is a --harnesses / --models value as the API takes it: "any",
// "private", or a list ("none": the empty list).
func allowValue(v string) any {
	switch v {
	case "any", "private":
		return v
	case "none":
		return []string{}
	}
	return splitList(v)
}

// agentRules mirrors the rules as the API answers them.
type agentRules struct {
	WorkspaceAgent string          `json:"workspaceAgent"`
	Harnesses      json.RawMessage `json:"harnesses"`
	Models         json.RawMessage `json:"models"`
	// Web, MCP: what a workspace agent's conversation holding the data may
	// do on the web and with MCP servers (D239); "" unset (ask, any)
	Web         string `json:"web"`
	MCP         string `json:"mcp"`
	Credentials string `json:"credentials"`
}

// allowText renders "any" | "private" | [ids] for a person.
func allowText(raw json.RawMessage) string {
	var word string
	if json.Unmarshal(raw, &word) == nil {
		return word
	}
	var ids []string
	if json.Unmarshal(raw, &ids) == nil {
		if len(ids) == 0 {
			return "none"
		}
		return strings.Join(ids, ", ")
	}
	return "any"
}

func printAgentRules(who string, r agentRules, bounds *agentRules) {
	fmt.Fprintf(dcOut, "%s\n", who)
	fmt.Fprintf(dcOut, "  workspace agent  %s\n", r.WorkspaceAgent)
	fmt.Fprintf(dcOut, "  harnesses        %s\n", allowText(r.Harnesses))
	fmt.Fprintf(dcOut, "  models           %s\n", allowText(r.Models))
	if r.Credentials != "" {
		fmt.Fprintf(dcOut, "  credentials      %s\n", r.Credentials)
	}
	if r.Web != "" || r.MCP != "" { // set only (the defaults, ask and any, aren't printed: older output stays as it was)
		fmt.Fprintf(dcOut, "  web              %s\n", orDefault(r.Web, "ask"))
		fmt.Fprintf(dcOut, "  mcp              %s\n", orDefault(r.MCP, "any"))
	}
	if bounds != nil {
		fmt.Fprintf(dcOut, "within the workspace's bounds: workspace agent %s · harnesses %s · models %s\n",
			bounds.WorkspaceAgent, allowText(bounds.Harnesses), allowText(bounds.Models))
	}
}

func cmdOrgAgents(args []string) error {
	cmd := "org agents"
	pin := false
	rest := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--pin" {
			pin = true
		} else {
			rest = append(rest, a)
		}
	}
	pos, vals, asJSON, err := flagValues(cmd, rest, "--workspace-agent", "--harnesses", "--models", "--web", "--mcp", "--credentials")
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageError(cmd, "one org at most (none: the workspace's bounds)")
	}
	if pin && len(pos) == 0 {
		return usageError(cmd, "--pin pins rules on an org: name it (the workspace's own rules are its bounds)")
	}
	body := map[string]any{}
	if v, ok := vals["--workspace-agent"]; ok {
		if v != "on" && v != "off" {
			return usageError(cmd, "--workspace-agent is on or off")
		}
		body["workspaceAgent"] = v
	}
	if v, ok := vals["--harnesses"]; ok {
		if v == "private" {
			return usageError(cmd, "--harnesses is any, none or a list of harness ids")
		}
		body["harnesses"] = allowValue(v)
	}
	if v, ok := vals["--models"]; ok {
		body["models"] = allowValue(v)
	}
	if v, ok := vals["--web"]; ok {
		if v != "ask" && v != "allow" && v != "never" {
			return usageError(cmd, "--web is ask, allow or never")
		}
		body["web"] = v
	}
	if v, ok := vals["--mcp"]; ok {
		if v != "any" && v != "listed" && v != "org" && v != "never" {
			return usageError(cmd, "--mcp is any, listed, org or never")
		}
		body["mcp"] = v
	}
	if v, ok := vals["--credentials"]; ok {
		if len(pos) == 1 {
			return usageError(cmd, "--credentials is the workspace's default: leave the org out")
		}
		body["credentials"] = v
	}

	if len(pos) == 0 { // the workspace's bounds
		method := "GET"
		var send any
		if len(body) > 0 {
			method, send = "PUT", body
		}
		b, err := agentPolicyCall(method, "/api/xbin/agents/policy", send)
		if err != nil {
			return err
		}
		if asJSON {
			printRaw(dcOut, b)
			return nil
		}
		var out agentRules
		if err := decodeAnswer(b, &out); err != nil {
			return err
		}
		printAgentRules("the workspace's bounds (every tile; an org only narrows them)", out, nil)
		return nil
	}

	org := pos[0]
	if len(body) == 0 {
		b, err := agentPolicyCall("GET", "/api/xbin/orgs", nil)
		if err != nil {
			return err
		}
		var out struct {
			Orgs []struct {
				ID        string
				Agents    *agentRules
				AgentsPin *agentRules
			}
		}
		if err := decodeAnswer(b, &out); err != nil {
			return err
		}
		for _, o := range out.Orgs {
			if o.ID != org {
				continue
			}
			if asJSON {
				bs, _ := json.Marshal(map[string]any{"agents": o.Agents, "pin": o.AgentsPin})
				printRaw(dcOut, bs)
				return nil
			}
			if o.AgentsPin != nil {
				printAgentRules("pinned on "+org+" by a workspace admin (its admins can't loosen these)", *o.AgentsPin, nil)
			}
			if o.Agents == nil {
				fmt.Fprintf(dcOut, "%s: no rules of its own — its tiles follow %s (bx org agents)\n", org,
					map[bool]string{true: "its pin and the workspace's bounds", false: "the workspace's bounds"}[o.AgentsPin != nil])
				return nil
			}
			printAgentRules(org+"'s rules for agents on its tiles", *o.Agents, nil)
			return nil
		}
		return &dcError{code: exitFailed, msg: "no such org (or you don't manage it): " + org}
	}
	path := "/api/xbin/orgs/" + url.PathEscape(org) + "/agents"
	if pin {
		path += "/pin"
	}
	b, err := agentPolicyCall("PUT", path, body)
	if err != nil {
		return err
	}
	if asJSON {
		printRaw(dcOut, b)
		return nil
	}
	var out struct{ Agents, Pin, Bounds agentRules }
	if err := decodeAnswer(b, &out); err != nil {
		return err
	}
	if pin || !agentRulesZero(out.Pin) {
		printAgentRules("pinned on "+org+" by a workspace admin (its admins can't loosen these)", out.Pin, nil)
	}
	printAgentRules(org+"'s rules for agents on its tiles", out.Agents, &out.Bounds)
	return nil
}

// agentRulesZero: rules that narrow nothing (or none at all in the answer).
func agentRulesZero(r agentRules) bool {
	return r.WorkspaceAgent != "off" && allowText(r.Harnesses) == "any" && allowText(r.Models) == "any"
}

func cmdAgentConnection(args []string) error {
	cmd := "agent connection"
	pos, vals, asJSON, err := flagValues(cmd, args, "--model", "--orgs", "--tiles", "--what-if")
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError(cmd, "unexpected %q", pos[0])
	}
	q := url.Values{}
	for flag, key := range map[string]string{"--model": "model", "--orgs": "orgs", "--tiles": "tiles", "--what-if": "whatIf"} {
		if v := vals[flag]; v != "" {
			q.Set(key, v)
		}
	}
	path := "/api/xbin/agents/connection"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	b, err := agentPolicyCall("GET", path, nil)
	if err != nil {
		return err
	}
	if asJSON {
		printRaw(dcOut, b)
		return nil
	}
	type tileRow struct{ Tile, Org, State, Reason string }
	var out struct {
		Orgs []struct {
			ID, Name, Kind, State, Reason string
			Tiles, Connected              int
		}
		Tiles  []tileRow
		Counts struct{ Summary string }
		WhatIf *struct {
			Connect, Disconnect []tileRow
			Counts              struct{ Summary string }
		}
	}
	if err := decodeAnswer(b, &out); err != nil {
		return err
	}
	fmt.Fprintln(dcOut, out.Counts.Summary)
	for _, o := range out.Orgs {
		line := fmt.Sprintf("%-14s %-10s %d/%d tiles", o.ID, o.State, o.Connected, o.Tiles)
		if o.Reason != "" {
			line += "  — " + o.Reason
		}
		fmt.Fprintln(dcOut, line)
		for _, t := range out.Tiles {
			if t.Org != o.ID {
				continue
			}
			line := fmt.Sprintf("  %-28s %s", t.Tile, t.State)
			if t.Reason != "" && t.Reason != o.Reason {
				line += "  — " + t.Reason
			}
			fmt.Fprintln(dcOut, line)
		}
	}
	for _, t := range out.Tiles {
		if t.Org == "" { // what the scope names and the caller can't see
			fmt.Fprintf(dcOut, "%-30s %s\n", t.Tile, t.State)
		}
	}
	if w := out.WhatIf; w != nil {
		fmt.Fprintf(dcOut, "with %s: %s\n", vals["--what-if"], w.Counts.Summary)
		for _, t := range w.Connect {
			fmt.Fprintf(dcOut, "  + %s\n", t.Tile)
		}
		for _, t := range w.Disconnect {
			fmt.Fprintf(dcOut, "  - %-28s %s\n", t.Tile, t.Reason)
		}
	}
	return nil
}

// orDefault is s, or def when it is "".
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
