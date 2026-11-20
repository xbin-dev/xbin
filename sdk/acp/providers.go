package acp

import (
	"slices"
	"strings"
)

// Provider is one coding-agent CLI a client can drive through its ACP
// adapter. Auth is the CLI's own: the adapter signs in from the $HOME it
// runs with, so a login done once there (LoginCmd in a terminal, or Signin
// driven for a person) serves every session with that $HOME. The JSON form
// is what xbind's GET /agent/providers serves (id, name, login, modes,
// defaultMode, signin); the rest is the runner's.
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
	// Signin is the sign-in a client can drive for a person without showing
	// them a terminal (signin.go): the command and how to read its output.
	// nil: none — the person signs in at a terminal (Login). Shared:
	// read-only.
	Signin *Signin `json:"signin,omitempty"`
	// Mint is a sign-in driven the same way that prints a long-lived
	// credential instead of keeping a login in $HOME (Signin.Token says what
	// it looks like): claude's `claude setup-token`. A client keeps it as
	// the person's own secret and hands it to the CLI in its environment
	// (Keys). nil: none. Shared: read-only.
	Mint *Signin `json:"-"`
	// Keys are the environment variables the CLI reads a credential from —
	// a token Mint printed, or a key the person pastes — in the order a
	// client offers them (KeyFor). Shared: read-only.
	Keys []Key `json:"-"`
	// AuthFile is where the CLI writes an API key handed to its adapter
	// through ACP authenticate, as a shell word ($HOME, $CODEX_HOME):
	// codex's auth.json. "" for a CLI that keeps such a key in memory only.
	// A client that hands over a key it keeps elsewhere removes the file
	// once the adapter took it (codex 0.156 keeps it in memory then); the
	// file would otherwise outlive the client's own forgetting of the key.
	AuthFile string `json:"-"`
	// LoginFiles are where the CLI keeps the login it signs in from (the
	// one LoginCmd or Signin leaves in $HOME), as shell words ($HOME,
	// $CODEX_HOME, …). A client that copies a $HOME — a sandbox cloned
	// from another — removes them in the copy: two copies of one login
	// share a refresh token, and the one that refreshes first signs the
	// other out.
	LoginFiles []string `json:"-"`
	// KeyHome is where the CLI writes a key handed to it through ACP
	// authenticate (AuthFile, taken apart): a host that keeps the key
	// itself points the variable at a directory private to the session
	// (credential.go). nil for a CLI that keeps such a key in memory.
	KeyHome *KeyHome `json:"-"`
	// Bins are the executables the provider needs on PATH: the adapter
	// first, then the CLI LoginCmd runs when that is another one.
	Bins []string `json:"-"`
	// AutoMode is the provider's auto-edit mode — edits go ahead, anything
	// else still asks — one of Modes and never an Explicit one; "" when it
	// has none.
	AutoMode string `json:"-"`
	// ApproveMode is the mode that asks before every edit and command (a
	// person's "Always approve"); PlanMode the one that plans without
	// changing anything (a plan-only delegation). Both are among Modes and
	// never Explicit; "" when the provider has none — it asks as its own
	// settings say.
	ApproveMode string `json:"-"`
	// AskModes are more modes in which the harness asks before every edit
	// and command (Asks), for a harness whose modes aren't in Modes.
	AskModes []string `json:"-"`
	PlanMode string   `json:"-"`
	// SafeModes are modes outside Modes this catalog knows never take the
	// agent past its own asks (Safe): the ones an adapter speaks only as a
	// config option of category mode (opencode's build and plan agents).
	SafeModes []string `json:"-"`
	// OptionModes maps a permission option that switches the session's mode
	// without naming it to the mode it switches to: a plan approval's
	// options (claude-agent-acp's "Yes, and bypass permissions" is
	// exit-plan-bypass). A consumer that keeps some modes to some people
	// judges such an option by its mode (Safe).
	OptionModes map[string]string `json:"-"`
	// Connector is how the CLI is pointed at an LLM connector — an
	// OpenAI-compatible endpoint a host serves in place of the vendor's —
	// from its environment alone (connector.go: ConnectorEnv renders it).
	// nil: it can't be (gemini speaks Google's wire only). Shared:
	// read-only.
	Connector *Connector `json:"-"`
}

// Key is one environment variable a CLI takes a credential from.
type Key struct {
	Env   string `json:"env"`   // CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY, …
	Label string `json:"label"` // for people: "Anthropic API key"
	// Kind is "setup-token" (a token Mint prints, or one pasted that Prefix
	// says is such) or "api-key".
	Kind string `json:"kind"`
	// Prefix: a pasted value that starts with it goes here (the first such
	// in Keys wins); "" takes a value no prefix claims.
	Prefix string `json:"prefix,omitempty"`
}

// KeyFor is the key of p's Keys a pasted value goes to: the first whose
// Prefix it starts with, else the first with none (false: p takes no
// pasted credential, or none fits).
func (p Provider) KeyFor(value string) (Key, bool) {
	for _, k := range p.Keys {
		if k.Prefix != "" && strings.HasPrefix(value, k.Prefix) {
			return k, true
		}
	}
	for _, k := range p.Keys {
		if k.Prefix == "" {
			return k, true
		}
	}
	return Key{}, false
}

// claudeKeys: a setup-token (any sk-ant-oat token) as
// CLAUDE_CODE_OAUTH_TOKEN — it outranks a $HOME login — and any other value
// as ANTHROPIC_API_KEY.
var claudeKeys = []Key{{Env: "CLAUDE_CODE_OAUTH_TOKEN", Label: "Claude subscription token (claude setup-token)", Kind: "setup-token", Prefix: "sk-ant-oat"},
	{Env: "ANTHROPIC_API_KEY", Label: "Anthropic API key", Kind: "api-key"}}

// Mode is one of a provider's session modes.
type Mode struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Explicit bool   `json:"explicit,omitempty"`
}

var catalog = []Provider{
	// No CLAUDE_CODE_REMOTE in Env (D178): it made the adapter offer its
	// full-screen `--cli` sign-in, and it puts Claude Code itself — which
	// inherits the adapter's env — in Anthropic's own remote-session mode
	// (auto memory off, a 2-minute API timeout, a settings defaultMode of
	// bypassPermissions refused). Without it the adapter offers `auth login`.
	{ID: "claude", Name: "Claude Code", Driver: "acp", Argv: []string{"claude-agent-acp"}, Login: "claude auth login",
		Modes: []Mode{{ID: "default", Name: "Ask before acting"}, {ID: "acceptEdits", Name: "Accept edits"}, {ID: "plan", Name: "Plan"},
			{ID: "auto", Name: "Auto"}, {ID: "bypassPermissions", Name: "Bypass permissions", Explicit: true}},
		DefaultMode: "default", Signin: claudeSignin,
		// recent models default thinking.display to "omitted" (signature-only
		// blocks, no text → no thought chunks); summarized makes it stream
		SessionMeta: map[string]any{"claudeCode": map[string]any{"options": map[string]any{
			"thinking": map[string]any{"type": "adaptive", "display": "summarized"}}}},
		// a terminal's sign-in (D178; Signin is the one a client drives), Mint
		// the long-lived token and Keys where a saved credential goes (D179)
		LoginCmd: "claude auth login", Bins: []string{"claude-agent-acp", "claude"}, AutoMode: "acceptEdits",
		Mint: claudeSetupToken, Keys: claudeKeys, LoginFiles: []string{`${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.credentials.json`},
		ApproveMode: "default", PlanMode: "plan", Connector: claudeConnector,
		// its ExitPlanMode approval's options (claude-agent-acp 0.81)
		OptionModes: map[string]string{"exit-plan-default": "default", "exit-plan-accept-edits": "acceptEdits",
			"exit-plan-clear-accept-edits": "acceptEdits", "exit-plan-auto": "auto", "exit-plan-clear-auto": "auto",
			"exit-plan-bypass": "bypassPermissions", "exit-plan-clear-bypass": "bypassPermissions"}},
	{ID: "codex", Name: "Codex", Driver: "acp", Argv: []string{"codex-acp"}, Login: "codex login",
		Modes: []Mode{{ID: "read-only", Name: "Ask for approval"}, {ID: "agent", Name: "Approve for me"},
			{ID: "agent-full-access", Name: "Full access", Explicit: true}},
		DefaultMode: "read-only", Env: map[string]string{"NO_BROWSER": "1"},
		LoginCmd: "codex login --device-auth", Bins: []string{"codex-acp", "codex"}, AutoMode: "agent",
		// codex's app-server reads no CODEX_API_KEY at start: a key goes
		// through authenticate, which writes it to auth.json (its file store)
		Keys:        []Key{{Env: "CODEX_API_KEY", Label: "OpenAI API key", Kind: "api-key"}},
		AuthFile:    `${CODEX_HOME:-$HOME/.codex}/auth.json`,
		KeyHome:     &KeyHome{Env: "CODEX_HOME", Dir: `$HOME/.codex`, File: "auth.json"},
		LoginFiles:  []string{`${CODEX_HOME:-$HOME/.codex}/auth.json`},
		ApproveMode: "read-only", PlanMode: "read-only", Connector: codexConnector},
	{ID: "gemini", Name: "Gemini CLI", Driver: "acp", Argv: []string{"gemini", "--acp"}, Login: "gemini (then choose Login with Google)",
		Modes: []Mode{{ID: "default", Name: "Ask before acting"}, {ID: "autoEdit", Name: "Auto edit"}, {ID: "plan", Name: "Plan"},
			{ID: "yolo", Name: "Auto-approve everything", Explicit: true}},
		LoginCmd: "NO_BROWSER=true gemini", Bins: []string{"gemini"}, AutoMode: "autoEdit",
		Keys:        []Key{{Env: "GEMINI_API_KEY", Label: "Gemini API key", Kind: "api-key"}},
		LoginFiles:  []string{`$HOME/.gemini/oauth_creds.json`},
		ApproveMode: "default", PlanMode: "plan"},
	{ID: "opencode", Name: "OpenCode", Driver: "acp", Argv: []string{"opencode", "acp"}, Login: "opencode auth login",
		LoginCmd: "opencode auth login", Bins: []string{"opencode"},
		LoginFiles: []string{`${XDG_DATA_HOME:-$HOME/.local/share}/opencode/auth.json`}, Connector: opencodeConnector,
		// the provider keys opencode reads from its environment
		Keys: []Key{{Env: "ANTHROPIC_API_KEY", Label: "Anthropic API key", Kind: "api-key", Prefix: "sk-ant-"},
			{Env: "OPENROUTER_API_KEY", Label: "OpenRouter API key", Kind: "api-key", Prefix: "sk-or-"},
			{Env: "OPENAI_API_KEY", Label: "OpenAI API key", Kind: "api-key", Prefix: "sk-"},
			{Env: "GOOGLE_GENERATIVE_AI_API_KEY", Label: "Google AI API key", Kind: "api-key", Prefix: "AIza"},
			{Env: "GROQ_API_KEY", Label: "Groq API key", Kind: "api-key", Prefix: "gsk_"},
			{Env: "XAI_API_KEY", Label: "xAI API key", Kind: "api-key", Prefix: "xai-"}},
		// its build and plan agents, as its config option of category mode
		// (opencode 1.18): neither asks before every edit and command. As
		// read from opencode 1.18.32's own agent table: every agent starts
		// from {"*": "allow"} (asking only for paths outside the project and
		// for .env files); build adds nothing that asks; plan denies edits
		// (but its own plan files) and still runs every command and fetches
		// every page unasked. And a tile's own opencode.json, .opencode/
		// config, agents and plugins load into the session — a writer to the
		// tile can loosen any agent — so no mode of it is one xbin can vouch
		// for: no AskModes (D250 review M2). Both are Safe: neither is a bypass
		// the catalog keeps from people.
		SafeModes: []string{"build", "plan"}},
}

// Providers is the catalog of adapters this package knows, in display
// order: claude, codex, gemini, opencode. A fresh slice each call; the
// entries' slices and maps are shared — treat them as read-only.
func Providers() []Provider { return append([]Provider(nil), catalog...) }

// Fake is the scripted test agent (hack/fakeacp, sdk/acp/acptest) as a
// provider, run as argv: id "fake", modes ask, auto and yolo (explicit),
// signed in by "<argv[0]> login" (its login: $HOME/.fakeacp/credentials,
// --require-login's). A consumer offers it only where a test
// fixture advertises it (a sandbox manager's hello); it is never in the
// catalog. It takes an LLM connector the way codex does — its variables
// replace the sign-in, set only when the connector serves the session
// (FAKEACP_LLM_MODEL names the default model, FAKEACP_LLM_ONLY=1 says the
// connector alone may serve; its `llm-call` script calls the endpoint) — or, with --llm-additive in argv, the way opencode does:
// always declared, the model only when the connector serves. With
// --no-default-mode in argv its entry names no default, approve or plan
// mode (a start that names no mode opens as the agent opens itself), its
// one asking mode in AskModes; with --no-asking-mode, none that asks
// either — as OpenCode's entry is.
func Fake(argv []string) Provider {
	p := Provider{ID: "fake", Name: "Fake agent (tests)", Driver: "acp", Argv: append([]string(nil), argv...),
		Modes:       []Mode{{ID: "ask", Name: "Ask before acting"}, {ID: "auto", Name: "Auto"}, {ID: "yolo", Name: "Yolo", Explicit: true}},
		DefaultMode: "ask", AutoMode: "auto", ApproveMode: "ask", PlanMode: "ask",
		LoginFiles: []string{`$HOME/.fakeacp/credentials`},
		Connector: &Connector{Wire: WireChat, Additive: slices.Contains(argv, "--llm-additive"), Never: []string{"FAKE_API_KEY"}, ModelOption: "model",
			env: func(c ConnectorParams, def bool) map[string]string {
				out := map[string]string{}
				if def && c.Model != "" {
					out["FAKEACP_LLM_MODEL"] = c.Model
				}
				if def && c.Only {
					out["FAKEACP_LLM_ONLY"] = "1" // the connector alone may serve: said, for a test to read
				}
				return out
			}}}
	if len(argv) > 0 {
		p.Login = argv[0] + " login"
		p.LoginCmd, p.Bins = p.Login, []string{argv[0]}
	}
	if slices.Contains(argv, "--no-default-mode") {
		// no default mode — it opens as it opens itself — and no mode a consumer may pick for it as "the
		// one that asks" by its approve or plan name: its one asking mode is in AskModes
		p.DefaultMode, p.ApproveMode, p.PlanMode, p.AskModes = "", "", "", []string{"ask"}
	}
	if slices.Contains(argv, "--no-asking-mode") {
		// …or as OpenCode's entry is: no default mode, and none xbin can vouch asks (D250 review M2)
		p.DefaultMode, p.ApproveMode, p.PlanMode, p.AskModes = "", "", "", nil
	}
	return p
}

// Lookup finds a provider of the catalog by id.
func Lookup(id string) (Provider, bool) {
	for _, p := range catalog {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// AskingMode is the mode in which p asks its person before every edit and
// command — its ask mode, else its plan mode, else the first of AskModes —
// and "" for a harness with none (OpenCode, as xbin knows it). It is the
// one answer to "which mode does a session that must ask open in" (xbind's
// start-defaults, the agent preferences' unasked start, an approval's
// default): a harness with none never opens unasked, and is never
// preselected for anyone.
func (p Provider) AskingMode() string {
	for _, m := range append([]string{p.ApproveMode, p.PlanMode}, p.AskModes...) {
		if p.Asks(m) {
			return m
		}
	}
	return ""
}

// Asks reports whether mode is one in which the harness asks its person
// before EVERY edit and command: its ask mode (ApproveMode) or its plan
// mode (PlanMode), and no other. It is narrower than Safe, which only
// excludes the bypass modes: Claude's acceptEdits and auto, Codex's agent
// and Gemini's autoEdit are Safe — they keep some asks — and edit files or
// run commands without asking each time. A consumer that starts a session
// for someone who isn't there to watch it (xbind's workspace agent,
// without a ticket) or tells a person "it asks before it acts" uses Asks.
// A mode the catalog doesn't know, and any mode of a provider with no ask
// mode, doesn't ask.
func (p Provider) Asks(mode string) bool {
	if mode == "" {
		return false
	}
	for _, m := range p.AskModes {
		if m == mode {
			return true
		}
	}
	return mode == p.ApproveMode || mode == p.PlanMode
}

// Safe reports whether mode is one this catalog knows never takes the agent
// past its own asks: a non-Explicit entry of Modes, or one of SafeModes.
// Anything else is not — an Explicit mode, a mode a newer adapter reports
// that the catalog doesn't list, any mode of a provider the catalog lacks
// (a zero Provider): a consumer that keeps bypass modes to some people
// treats every such mode as one (default-deny).
func (p Provider) Safe(mode string) bool {
	if mode == "" {
		return false
	}
	for _, m := range p.Modes {
		if m.ID == mode {
			return !m.Explicit
		}
	}
	for _, m := range p.SafeModes {
		if m == mode {
			return true
		}
	}
	return false
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
