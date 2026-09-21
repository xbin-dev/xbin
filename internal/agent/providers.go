package agent

import (
	"os"
	"sort"
	"strings"
)

// Provider is one coding-agent CLI the daemon can drive.
type Provider struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Driver  string   `json:"-"`    // which Driver package: "acp"
	Argv    []string `json:"-"`    // the command inside the sandbox
	Keys    []string `json:"keys"` // vault keys (env names) copied into the agent env when present
	KeyGlob string   `json:"-"`    // additionally every vault key matching this suffix ("_API_KEY")
	Auth    string   `json:"-"`    // ACP authenticate methodId to call when the first key is present ("" = none)
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
	{ID: "claude", Name: "Claude Code", Driver: "acp", Argv: []string{"claude-agent-acp"},
		Keys: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"},
		Modes: []Mode{{ID: "default", Name: "Ask before acting"}, {ID: "acceptEdits", Name: "Accept edits"}, {ID: "plan", Name: "Plan"},
			{ID: "auto", Name: "Auto"}, {ID: "bypassPermissions", Name: "Bypass permissions", Explicit: true}},
		DefaultMode: "default"},
	{ID: "codex", Name: "Codex", Driver: "acp", Argv: []string{"codex-acp"},
		Keys: []string{"OPENAI_API_KEY", "CODEX_API_KEY"}, Auth: "api-key",
		Modes: []Mode{{ID: "read-only", Name: "Ask for approval"}, {ID: "agent", Name: "Approve for me"},
			{ID: "agent-full-access", Name: "Full access", Explicit: true}},
		DefaultMode: "read-only", Env: map[string]string{"NO_BROWSER": "1"}},
	{ID: "gemini", Name: "Gemini CLI", Driver: "acp", Argv: []string{"gemini", "--acp"},
		Keys: []string{"GEMINI_API_KEY", "GOOGLE_AI_API_KEY"},
		Modes: []Mode{{ID: "default", Name: "Ask before acting"}, {ID: "autoEdit", Name: "Auto edit"}, {ID: "plan", Name: "Plan"},
			{ID: "yolo", Name: "Auto-approve everything", Explicit: true}}},
	{ID: "opencode", Name: "OpenCode", Driver: "acp", Argv: []string{"opencode", "acp"},
		KeyGlob: "_API_KEY"},
}

// Providers lists the providers this daemon offers, fake included when
// FakeEnv is set (its command may carry arguments, space-separated).
func Providers() []Provider {
	out := append([]Provider(nil), providers...)
	if cmd := os.Getenv(FakeEnv); cmd != "" {
		out = append(out, Provider{ID: "fake", Name: "Fake agent (tests)", Driver: "acp", Argv: strings.Fields(cmd),
			Keys: []string{"FAKE_API_KEY"}, Modes: []Mode{{ID: "ask", Name: "Ask"}, {ID: "yolo", Name: "Yolo", Explicit: true}}, DefaultMode: "ask"})
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

// KeysFrom picks the env entries this provider gets from a tile's vault:
// its named keys, plus every key with the glob suffix. Sorted, "K=V".
func (p Provider) KeysFrom(vault map[string]string) (env []string, primary bool) {
	seen := map[string]bool{}
	for _, k := range p.Keys {
		if v, ok := vault[k]; ok && v != "" {
			env = append(env, k+"="+v)
			seen[k] = true
		}
	}
	if p.KeyGlob != "" {
		for k, v := range vault {
			if !seen[k] && v != "" && strings.HasSuffix(k, p.KeyGlob) {
				env = append(env, k+"="+v)
				seen[k] = true
			}
		}
	}
	sort.Strings(env)
	primary = len(p.Keys) > 0 && seen[p.Keys[0]] || p.KeyGlob != "" && len(env) > 0
	return env, primary
}

// KeyHint is the operator's instruction when no key is in the vault.
func (p Provider) KeyHint(tile string) string {
	k := p.KeyGlob
	if len(p.Keys) > 0 {
		k = p.Keys[0]
	} else if k != "" {
		k = "<PROVIDER>" + k
	}
	return "no " + k + " in this tile's vault — set it with: bx vault set " + tile + " " + k + " <value>  (or sign in with the CLI in a shell terminal; the same home serves both)"
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
