package agent

import (
	"os"
	"strings"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// Provider is one coding-agent CLI the daemon can drive: sdk/acp's catalog
// entry. Auth is the CLI's own: an agent session runs with the same
// per-user $HOME a shell terminal gets (D6), so a login done once in a
// terminal (claude auth login, codex login, …) — or through the Agent tab's
// guided sign-in, which runs the provider's Signin in a terminal session of
// its own (D178) — serves every agent session on every tile. There are no
// provider keys in the tile vault — the home is the single source of
// credentials, exactly as for a shell.
type Provider = acp.Provider

// Mode is one of a provider's session modes.
type Mode = acp.Mode

// FakeEnv names the env var that registers the "fake" provider (a scripted
// ACP agent for tests and the UI harness): its value is the command.
const FakeEnv = "XBIN_AGENT_FAKE"

// Providers lists the providers this daemon offers — sdk/acp's catalog —
// fake included when FakeEnv is set (its command may carry arguments,
// space-separated). The fake signs in the way Claude Code does (claude's
// Signin), so the UI harness drives the guided sign-in against a scripted
// `claude` on the PATH.
func Providers() []Provider {
	out := acp.Providers()
	if cmd := os.Getenv(FakeEnv); cmd != "" {
		c, _ := acp.Lookup("claude")
		out = append(out, Provider{ID: "fake", Name: "Fake agent (tests)", Driver: "acp", Argv: strings.Fields(cmd), Login: "the fake needs no login",
			Modes: []Mode{{ID: "ask", Name: "Ask"}, {ID: "yolo", Name: "Yolo", Explicit: true}}, DefaultMode: "ask", Signin: c.Signin})
	}
	return out
}

// Lookup finds a provider by id.
func Lookup(id string) (Provider, bool) {
	for _, p := range Providers() {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// LoginHint is what to tell the operator when the agent has no credentials:
// sign the CLI in from a shell terminal on this tile — that $HOME is shared
// with agent sessions (D6), so one login serves the agent everywhere.
func LoginHint(p Provider, tile string) string {
	how := p.Login
	if how == "" {
		how = "sign it in"
	}
	return "the agent isn't signed in — open a terminal on " + tile + " and run: " + how + "  (your terminal $HOME is shared with agent sessions, so one login serves every tile)"
}
