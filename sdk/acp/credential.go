package acp

// credential.go — a session started on a credential of the host's own: a
// token or key the host keeps for the person (a saved sign-in) and put in
// the agent's environment (Provider.Keys says which variable). Three things
// differ from a session that signs in from its $HOME:
//
//   - the adapter's status probe is not believed: claude-agent-acp 0.81
//     reports an environment token's sign-in as "none" though every turn
//     works, so only the agent's own refusal (-32000 to a session or a
//     prompt) marks such a session signed out (AuthState);
//   - an adapter that reads no key from its environment at start (codex's
//     app-server; gemini with another sign-in selected) refuses its session
//     signed out: the client then hands it the key once, through the
//     adapter's own API-key authenticate method, and opens the session
//     again;
//   - the host is told of a refusal (OnRefused), to mark the credential.
//
// The key rides one authenticate frame's _meta and is never logged.

import (
	"context"
	"errors"
)

// Credential is a sign-in of the host's own the agent was started with
// (Config.Credential).
type Credential struct {
	// APIKey is handed to the agent through its API-key authenticate
	// method when it refuses to open its session signed out. "" for a
	// credential the CLI can only read from its environment (a
	// subscription token).
	APIKey string
	// OnRefused is told when the agent itself refuses a session or a prompt
	// as signed out while it holds the credential (msg: its own words,
	// which may echo the credential — mask before keeping them). It runs on
	// the caller's goroutine and must not block.
	OnRefused func(msg string)
}

// KeyHome says where a CLI writes a key handed to it through authenticate
// (Provider.KeyHome): File under the directory variable Env names, Dir
// when the variable is unset (a shell word: $HOME). A host that keeps the
// key elsewhere points Env at a directory private to the session, so the
// file never lands where other sessions of the person's read.
type KeyHome struct {
	Env  string // CODEX_HOME
	Dir  string // $HOME/.codex
	File string // auth.json
}

// APIKeyMeta is the _meta of an API-key authenticate for provider: the key
// itself for gemini-cli (its dispatcher reads _meta["api-key"] as the
// key), {apiKey} for codex-acp and everyone else.
func APIKeyMeta(provider, key string) map[string]any {
	if provider == "gemini" {
		return map[string]any{"api-key": key}
	}
	return map[string]any{"api-key": map[string]any{"apiKey": key}}
}

// apiKeyMethod is the agent's authenticate method that takes an API key:
// the first that advertises _meta["api-key"] ("": none).
func apiKeyMethod(methods []AuthMethod) string {
	for _, m := range methods {
		if _, ok := m.Meta["api-key"]; ok {
			return m.ID
		}
	}
	return ""
}

// injected reports a session started on a credential of the host's.
func (c *Client) injected() bool { return c.cfg.Credential != nil }

// awaitsKey reports a session whose agent has not been handed the host's
// API key yet: its first refusal is the cue to hand it over, not a refusal
// of the credential.
func (c *Client) awaitsKey() bool {
	cred := c.cfg.Credential
	if cred == nil || cred.APIKey == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.keyHanded
}

// keySignIn hands the host's API key to an agent that refused its session
// signed out. false: there is no key to hand, or no method takes one, or
// the agent refused it.
func (c *Client) keySignIn(ctx context.Context, refusal error) bool {
	cred := c.cfg.Credential
	if cred == nil || cred.APIKey == "" || !IsAuthRequired(refusal) {
		return false
	}
	c.mu.Lock()
	method := apiKeyMethod(c.authMethods)
	c.keyHanded = true // whatever comes of it: a later refusal is the credential's
	c.mu.Unlock()
	if method == "" {
		return false
	}
	if err := c.conn.CallCtx(ctx, MAuthenticate, AuthenticateParams{MethodID: method, Meta: APIKeyMeta(c.cfg.Provider.ID, cred.APIKey)}, nil); err != nil {
		var re *Error
		if errors.As(err, &re) {
			c.logf("the agent refused the saved key (authenticate %s: code %d)", method, re.Code) // its message may echo the key
		}
		return false
	}
	c.mu.Lock()
	c.authNeeded = false
	c.mu.Unlock()
	return true
}

// refusedCredential tells the host the agent refused while it held the
// host's credential.
func (c *Client) refusedCredential(msg string) {
	if cred := c.cfg.Credential; cred != nil && cred.OnRefused != nil {
		cred.OnRefused(msg)
	}
}
