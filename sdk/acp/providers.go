package acp

import "strings"

// Provider is one coding-agent CLI a client can drive through its ACP
// adapter. Auth is the CLI's own: the adapter signs in from the $HOME it
// runs with, so a login done once in a terminal there (LoginCmd) serves
// every session with that $HOME. The JSON form is what xbind's GET
// /agent/providers serves (id, name, login, modes, defaultMode); the rest is
// the runner's.
type Provider struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Driver string   `json:"-"`     // the protocol: "acp"
	Argv   []string `json:"-"`     // the adapter's command where the agent runs
	Login  string   `json:"login"` // how to sign the CLI in, for a person to read
	// Modes the provider advertises, in display order; Explicit ones
	// (bypass/full access) are never defaults and must be asked for by name.
	Modes       []Mode            `json:"modes"`
	DefaultMode string            `json:"defaultMode"` // "" = whatever the agent reports current
	Env         map[string]string `json:"-"`           // extra env the adapter wants (mode hints)
	// SessionMeta rides session/new and session/load as _meta: per-adapter
	// knobs the protocol has no field for (claude: the thinking display).
	SessionMeta map[string]any `json:"-"`
	// LoginCmd is a shell command line that signs the CLI in, run in a
	// terminal where the agent runs (its $HOME keeps the login); it prints a
	// URL or a code rather than opening a browser.
	LoginCmd string `json:"-"`
	// Bins are the executables the provider needs on PATH: the adapter
	// first, then the CLI LoginCmd runs when that is another one.
	Bins []string `json:"-"`
	// AutoMode is the provider's auto-edit mode — edits go ahead, anything
	// else still asks — one of Modes and never an Explicit one; "" when it
	// has none.
	AutoMode string `json:"-"`
}

// Mode is one of a provider's session modes.
type Mode struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Explicit bool   `json:"explicit,omitempty"`
}

var catalog = []Provider{
	{ID: "claude", Name: "Claude Code", Driver: "acp", Argv: []string{"claude-agent-acp"}, Login: "claude /login",
		Modes: []Mode{{ID: "default", Name: "Ask before acting"}, {ID: "acceptEdits", Name: "Accept edits"}, {ID: "plan", Name: "Plan"},
			{ID: "auto", Name: "Auto"}, {ID: "bypassPermissions", Name: "Bypass permissions", Explicit: true}},
		DefaultMode: "default", Env: map[string]string{"CLAUDE_CODE_REMOTE": "1"},
		// recent models default thinking.display to "omitted" (signature-only
		// blocks, no text → no thought chunks); summarized makes it stream
		SessionMeta: map[string]any{"claudeCode": map[string]any{"options": map[string]any{
			"thinking": map[string]any{"type": "adaptive", "display": "summarized"}}}},
		LoginCmd: "CLAUDE_CODE_REMOTE=1 claude /login", Bins: []string{"claude-agent-acp", "claude"}, AutoMode: "acceptEdits"},
	{ID: "codex", Name: "Codex", Driver: "acp", Argv: []string{"codex-acp"}, Login: "codex login",
		Modes: []Mode{{ID: "read-only", Name: "Ask for approval"}, {ID: "agent", Name: "Approve for me"},
			{ID: "agent-full-access", Name: "Full access", Explicit: true}},
		DefaultMode: "read-only", Env: map[string]string{"NO_BROWSER": "1"},
		LoginCmd: "codex login --device-auth", Bins: []string{"codex-acp", "codex"}, AutoMode: "agent"},
	{ID: "gemini", Name: "Gemini CLI", Driver: "acp", Argv: []string{"gemini", "--acp"}, Login: "gemini (then choose Login with Google)",
		Modes: []Mode{{ID: "default", Name: "Ask before acting"}, {ID: "autoEdit", Name: "Auto edit"}, {ID: "plan", Name: "Plan"},
			{ID: "yolo", Name: "Auto-approve everything", Explicit: true}},
		LoginCmd: "NO_BROWSER=true gemini", Bins: []string{"gemini"}, AutoMode: "autoEdit"},
	{ID: "opencode", Name: "OpenCode", Driver: "acp", Argv: []string{"opencode", "acp"}, Login: "opencode auth login",
		LoginCmd: "opencode auth login", Bins: []string{"opencode"}},
}

// Providers is the catalog of adapters this package knows, in display
// order: claude, codex, gemini, opencode. A fresh slice each call; the
// entries' slices and maps are shared — treat them as read-only.
func Providers() []Provider { return append([]Provider(nil), catalog...) }

// Lookup finds a provider of the catalog by id.
func Lookup(id string) (Provider, bool) {
	for _, p := range catalog {
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

type modeErr struct{ msg string }

func (e modeErr) Error() string { return e.msg }

func errModeUnknown(p Provider, mode string) error {
	ids := make([]string, 0, len(p.Modes))
	for _, m := range p.Modes {
		ids = append(ids, m.ID)
	}
	return modeErr{"unknown mode " + mode + " for " + p.ID + " (one of " + strings.Join(ids, ", ") + ")"}
}
