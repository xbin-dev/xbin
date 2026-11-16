package agent

import (
	"os"
	"slices"
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
// space-separated): sdk/acp's Fake, the one definition of the scripted
// agent as a provider, with two things of xbind's own. It signs in the way
// Claude Code does (claude's Signin), so the UI harness drives the guided
// sign-in against a scripted `claude` on the PATH; and its login line says
// that the fake, as xbind runs it (no --require-login), needs none.
func Providers() []Provider {
	out := acp.Providers()
	if cmd := os.Getenv(FakeEnv); cmd != "" {
		c, _ := acp.Lookup("claude")
		fake := acp.Fake(strings.Fields(cmd))
		fake.Login, fake.Signin = "the fake needs no login", c.Signin
		// a saved sign-in is made and kept for it as for Claude Code (D230):
		// run with --require-login the fake reads the same variables
		fake.Mint, fake.Keys = c.Mint, c.Keys
		if slices.Contains(fake.Argv, "--codex-auth") { // …or as for Codex: a key through authenticate, kept in a file
			x, _ := acp.Lookup("codex")
			fake.Mint, fake.Keys, fake.KeyHome = nil, x.Keys, x.KeyHome
		}
		out = append(out, fake)
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

// LoginHint is what to tell the operator when the agent has no credentials
// — sdk/acp's one sign-in sentence (Provider.SignInHint), naming where to
// run the command: a shell terminal on this tile. That $HOME is shared with
// agent sessions (D6), so one login serves the agent everywhere.
func LoginHint(p Provider, tile string) string { return p.SignInHint("a terminal on " + tile) }
