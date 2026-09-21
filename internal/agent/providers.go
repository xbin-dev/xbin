package agent

import (
	"os"
	"strings"
)

// Provider is one coding-agent CLI the daemon can drive. Auth is the CLI's
// own: an agent session runs with the same per-user $HOME a shell terminal
// gets (D6), so a login done once in a terminal (claude /login, codex login,
// …) serves every agent session on every tile. There are no provider keys in
// the tile vault — the home is the single source of credentials, exactly as
// for a shell.
type Provider struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Driver string   `json:"-"`     // which Driver package: "acp"
	Argv   []string `json:"-"`     // the command inside the sandbox
	Login  string   `json:"login"` // the shell command that signs this CLI in (its $HOME serves agents)
	// Modes the provider advertises, in display order; Explicit ones
	// (bypass/full access) are never defaults and must be asked for by name.
	Modes       []Mode            `json:"modes"`
	DefaultMode string            `json:"defaultMode"` // "" = whatever the agent reports current
	Env         map[string]string `json:"-"`           // extra env the adapter wants (mode hints)
}

// Mode is one of a provider's session modes.
type Mode struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Explicit bool   `json:"explicit,omitempty"`
}

// FakeEnv names the env var that registers the "fake" provider (a scripted
// ACP agent for tests and the UI harness): its value is the command.
const FakeEnv = "XBIN_AGENT_FAKE"

var providers = []Provider{
	{ID: "claude", Name: "Claude Code", Driver: "acp", Argv: []string{"claude-agent-acp"}, Login: "claude /login",
		Modes: []Mode{{ID: "default", Name: "Ask before acting"}, {ID: "acceptEdits", Name: "Accept edits"}, {ID: "plan", Name: "Plan"},
			{ID: "auto", Name: "Auto"}, {ID: "bypassPermissions", Name: "Bypass permissions", Explicit: true}},
		DefaultMode: "default", Env: map[string]string{"CLAUDE_CODE_REMOTE": "1"}},
	{ID: "codex", Name: "Codex", Driver: "acp", Argv: []string{"codex-acp"}, Login: "codex login",
		Modes: []Mode{{ID: "read-only", Name: "Ask for approval"}, {ID: "agent", Name: "Approve for me"},
			{ID: "agent-full-access", Name: "Full access", Explicit: true}},
		DefaultMode: "read-only", Env: map[string]string{"NO_BROWSER": "1"}},
	{ID: "gemini", Name: "Gemini CLI", Driver: "acp", Argv: []string{"gemini", "--acp"}, Login: "gemini (then choose Login with Google)",
		Modes: []Mode{{ID: "default", Name: "Ask before acting"}, {ID: "autoEdit", Name: "Auto edit"}, {ID: "plan", Name: "Plan"},
			{ID: "yolo", Name: "Auto-approve everything", Explicit: true}}},
	{ID: "opencode", Name: "OpenCode", Driver: "acp", Argv: []string{"opencode", "acp"}, Login: "opencode auth login"},
}

// Providers lists the providers this daemon offers, fake included when
// FakeEnv is set (its command may carry arguments, space-separated).
func Providers() []Provider {
	out := append([]Provider(nil), providers...)
	if cmd := os.Getenv(FakeEnv); cmd != "" {
		out = append(out, Provider{ID: "fake", Name: "Fake agent (tests)", Driver: "acp", Argv: strings.Fields(cmd), Login: "the fake needs no login",
			Modes: []Mode{{ID: "ask", Name: "Ask"}, {ID: "yolo", Name: "Yolo", Explicit: true}}, DefaultMode: "ask"})
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

// ResolveMode validates a requested mode against the table: "" → the
// default; an unknown id is refused when the provider lists modes (a
// provider with no table accepts anything and lets the agent judge).
func (p Provider) ResolveMode(mode string) (string, error) {
	if mode == "" {
		return p.DefaultMode, nil
	}
	if len(p.Modes) == 0 {
		return mode, nil
	}
	for _, m := range p.Modes {
		if m.ID == mode {
			return mode, nil
		}
	}
	return "", errModeUnknown(p, mode)
}

// LoginHint is what to tell the operator when the agent has no credentials:
// sign the CLI in from a shell terminal on this tile — that $HOME is shared
// with agent sessions (D6), so one login serves the agent everywhere.
func (p Provider) LoginHint(tile string) string {
	how := p.Login
	if how == "" {
		how = "sign it in"
	}
	return "the agent isn't signed in — open a terminal on " + tile + " and run: " + how + "  (your terminal $HOME is shared with agent sessions, so one login serves every tile)"
}

type modeErr struct{ msg string }

func (e modeErr) Error() string { return e.msg }

func errModeUnknown(p Provider, mode string) error {
	ids := make([]string, 0, len(p.Modes))
	for _, m := range p.Modes {
		ids = append(ids, m.ID)
	}
	return modeErr{"unknown mode " + mode + " for " + p.ID + " (one of " + strings.Join(ids, ", ") + ")"}
}
