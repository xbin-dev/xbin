package main

// agentdeploy.go — `bx agent run --deployment <name>` (11-contract §9.3,
// §7.4): an agent session whose token, calls and bx commands reach that
// deployment of the tile, fixed for the session's life. `--tile
// <tile>+<name>` asks for the same; bx resolves the ref through GET
// /deployments (the tile and deployment= apart, deployref.go), and a tile
// whose own path holds the + stays that tile. The
// target rides POST /term/sessions?deployment= (the body is unchanged), and
// the answer must echo it: an xbind that ignores the parameter would have
// opened a session on the primary, so bx ends it and says so. Every other
// `bx agent` invocation is agent.go's, byte for byte; this file only takes
// the ones that name a deployment, through moreCmds, which cmdExtra asks
// before its own switch.

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

func init() {
	moreCmds["agent"] = cmdAgentDeployment
	dcUsage["agent run"] = "bx agent run [--tile <tile>[+<name>]] [--deployment <name>] [--provider p] [--mode m] [--model m] [--option id=v] [--net scope] [--vm] \"<prompt>\""
}

// cmdAgentDeployment runs `bx agent run` naming a deployment itself, and
// hands every other agent command to cmdAgent unchanged.
func cmdAgentDeployment(args []string) error {
	if len(args) == 0 || args[0] != "run" {
		return cmdAgent(args)
	}
	rest, dep, given, err := takeDeployment("agent run", args[1:])
	if err == nil && !given && !qualifiedTile(rest) {
		return cmdAgent(args)
	}
	return dcCommand(func([]string) error {
		if err != nil {
			return err
		}
		return agentRunOn(rest, dep, given)
	})(nil)
}

// qualifiedTile: the line's --tile (the last one, as parseAgentRun reads
// it) holds a +, which may name a deployment.
func qualifiedTile(args []string) bool {
	q := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--tile" {
			q = strings.Contains(args[i+1], "+")
		}
	}
	return q
}

// agentRunOn opens an agent session targeting deployment dep (or the one
// the tile ref's qualifier names), prompts it and follows the turn.
func agentRunOn(args []string, dep string, given bool) error {
	o, err := parseAgentRun(args)
	if err != nil {
		return usageError("agent run", "%v", err)
	}
	if len(o.rest) == 0 {
		return usageError("agent run", "what should the agent do? give the prompt")
	}
	if given {
		if err := checkName("agent run", dep); err != nil {
			return err
		}
	}
	st, _, err := getDeployState(o.tile)
	if err != nil {
		return err
	}
	switch {
	case st.Selected != "" && given && st.Selected != dep:
		return usageError("agent run", "--tile %s names %s, --deployment names %s: name one", o.tile, st.Selected, dep)
	case st.Selected != "":
		dep = st.Selected
	case !given: // --tile named a tile whose own path holds the +
		return cmdAgentRun(args)
	}
	body := map[string]any{"cwd": st.Tile, "kind": "agent", "provider": o.provider, "mode": o.mode, "net": o.net, "name": o.name, "vm": o.vm}
	if len(o.options) > 0 {
		body["options"] = o.options
	}
	b, err := dcCall("POST", "/api/xbin/term/sessions?deployment="+url.QueryEscape(dep), body)
	if err != nil {
		return err
	}
	var info struct {
		ID, Provider, Mode, Cwd string
		Deployment              string `json:"deployment"`
	}
	if err := decodeAnswer(b, &info); err != nil {
		return err
	}
	if !echoes(info.Deployment, dep, st.primary()) {
		if info.ID != "" {
			_, _ = dcCall("DELETE", "/api/xbin/term/sessions/"+url.PathEscape(info.ID), nil)
		}
		return &dcError{code: exitNoDeployments, msg: fmt.Sprintf("this xbind doesn't point agent sessions at a deployment (no deployment echo): session %s would have called the primary of %s, not %s, so bx ended it; upgrade xbind", firstOf(info.ID, "-"), st.Tile, dep)}
	}
	where := firstOf(info.Cwd, st.Tile)
	if dep != st.primary() {
		where += "+" + dep
	}
	fmt.Fprintf(os.Stderr, "session %s: %s on %s (mode %s)\n", info.ID, info.Provider, where, orDash(info.Mode))
	if err := agentPrompt(info.ID, strings.Join(o.rest, " ")); err != nil {
		return err
	}
	return agentFollow(info.ID, 0, true)
}
