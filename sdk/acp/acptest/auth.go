package acptest

// initialize and signing in: the plain agent's single "api-key" method
// that accepts anything, and --require-login's three (terminal, API key,
// device code), shaped like claude-agent-acp's and codex-acp's.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// initializeResult is acp.InitializeResult with the agent's side of the
// auth methods (type, args, _meta), which the client's types don't carry.
type initializeResult struct {
	ProtocolVersion   int                    `json:"protocolVersion"`
	AgentCapabilities *acp.AgentCapabilities `json:"agentCapabilities,omitempty"`
	AuthMethods       []authMethod           `json:"authMethods,omitempty"`
	AgentInfo         *acp.Info              `json:"agentInfo,omitempty"`
	Meta              map[string]any         `json:"_meta,omitempty"`
}

type authMethod struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Type        string         `json:"type,omitempty"` // "" (the agent signs in) | "terminal"
	Args        []string       `json:"args,omitempty"` // terminal: the agent's own argv for its sign-in
	Meta        map[string]any `json:"_meta,omitempty"`
}

// clientCaps notes what initialize says the client takes.
func (f *fake) clientCaps(params json.RawMessage) {
	var p struct {
		Caps struct {
			Elicitation struct {
				URL json.RawMessage `json:"url"`
			} `json:"elicitation"`
			Meta map[string]any `json:"_meta"`
		} `json:"clientCapabilities"`
	}
	_ = json.Unmarshal(params, &p)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.elicitURL = len(p.Caps.Elicitation.URL) > 0 && string(p.Caps.Elicitation.URL) != "null"
	f.termAuth = p.Caps.Meta["terminal-auth"] == true
}

func (f *fake) initialize() initializeResult {
	res := initializeResult{ProtocolVersion: 1, AgentInfo: &acp.Info{Name: "fakeacp", Version: "1"},
		AgentCapabilities: &acp.AgentCapabilities{LoadSession: true, // session/load replays a canned history (resume tests)
			PromptCapabilities: &acp.PromptCapabilities{Image: true, EmbeddedContext: true}}, // as the real adapters do
		AuthMethods: []authMethod{{ID: "api-key", Name: "API key"}}}
	if f.o.RequireLogin {
		login := authMethod{ID: "fake-login", Name: "Fake login", Description: "Run the fake agent's sign-in in a terminal",
			Type: "terminal", Args: []string{"login"}}
		f.mu.Lock()
		if f.termAuth && len(f.o.Self) > 0 {
			login.Meta = map[string]any{"terminal-auth": map[string]any{"command": f.o.Self[0],
				"args": append(append([]string{}, f.o.Self[1:]...), "login"), "label": "Fake login"}}
		}
		f.mu.Unlock()
		res.AuthMethods = []authMethod{login,
			{ID: "fake-api-key", Name: "API key", Description: "Use an API key to authenticate", Meta: map[string]any{"api-key": map[string]any{"provider": "fake"}}},
			{ID: "fake-device", Name: "Fake (device code)", Description: "Sign in by opening a verification page and entering a one-time code"}}
	}
	if f.o.Steer {
		res.Meta = map[string]any{"steering": map[string]any{"supported": true}}
	}
	return res
}

// authenticate signs in with one of --require-login's methods (any other
// method, or any without the flag, answers {}, as the plain agent always did).
func (f *fake) authenticate(m *acp.Message) (any, *acp.Error) {
	if !f.o.RequireLogin {
		return map[string]any{}, nil
	}
	var p struct {
		MethodID string                     `json:"methodId"`
		Meta     map[string]json.RawMessage `json:"_meta"`
	}
	_ = json.Unmarshal(m.Params, &p)
	switch p.MethodID {
	case "fake-api-key":
		var key struct {
			APIKey string `json:"apiKey"`
		}
		_ = json.Unmarshal(p.Meta["api-key"], &key)
		switch key.APIKey {
		case "":
			return nil, &acp.Error{Code: acp.CodeInvalidParams, Message: "no key"}
		case "bad":
			return nil, &acp.Error{Code: acp.CodeInternal, Message: "invalid API key"}
		}
		if err := f.signIn("fake-api-key"); err != nil {
			return nil, &acp.Error{Code: acp.CodeInternal, Message: err.Error()}
		}
		return map[string]any{}, nil
	case "fake-device":
		f.mu.Lock()
		ok := f.elicitURL
		f.mu.Unlock()
		if !ok {
			return nil, &acp.Error{Code: acp.CodeInvalidRequest, Message: "device code needs URL elicitation"}
		}
		go f.device(m.ID)
		return nil, nil // answered when the sign-in completes
	case "fake-login":
		return nil, &acp.Error{Code: acp.CodeInvalidParams, Message: "fake-login signs in in a terminal: run the agent with `login`"}
	}
	return map[string]any{}, nil
}

// device is the device-code sign-in: the URL goes to the client, and the
// sign-in completes DeviceDelay after the client accepts it.
func (f *fake) device(id json.RawMessage) {
	const elicitation = "fake-device-1"
	var res struct {
		Action string `json:"action"`
	}
	err := f.conn.Call(acp.MElicitCreate, map[string]any{"mode": "url", "requestId": id, "elicitationId": elicitation,
		"url": "https://example.invalid/device", "message": "Enter code FAKE-1234 at https://example.invalid/device"}, &res)
	if err != nil || res.Action != "accept" {
		_ = f.conn.Reply(id, nil, &acp.Error{Code: acp.CodeInternal, Message: "the device sign-in was not accepted"})
		return
	}
	switch d := f.o.DeviceDelay; {
	case d == 0:
		f.sleep(time.Second)
	case d > 0:
		f.sleep(d)
	}
	if err := f.signIn("fake-device"); err != nil {
		_ = f.conn.Reply(id, nil, &acp.Error{Code: acp.CodeInternal, Message: err.Error()})
		return
	}
	_ = f.conn.Notify(acp.MElicitComplete, map[string]any{"elicitationId": elicitation})
	_ = f.conn.Reply(id, map[string]any{}, nil)
}

// requireLogin refuses a prompt while signed out (--require-login), as
// Claude does: the sign-out status, then -32000.
func (f *fake) requireLogin() *acp.Error {
	if !f.o.RequireLogin {
		return nil
	}
	switch tok := f.envToken(); {
	case strings.Contains(tok, "refused"): // a credential in the env outranks $HOME, refused or not
		_ = f.conn.Notify(acp.MAuthStatus, map[string]any{"authStatus": map[string]any{"kind": "none"}})
		return &acp.Error{Code: acp.CodeAuthRequired, Message: "Authentication required: OAuth token has been revoked"}
	case tok != "":
		return nil
	}
	f.mu.Lock()
	in := f.signedIn
	f.mu.Unlock()
	if !in && f.credentials() { // a terminal sign-in since
		f.mu.Lock()
		f.signedIn, in = true, true
		f.mu.Unlock()
	}
	if in {
		return nil
	}
	_ = f.conn.Notify(acp.MAuthStatus, map[string]any{"authStatus": map[string]any{"kind": "none"}})
	return &acp.Error{Code: acp.CodeAuthRequired, Message: "Authentication required: sign in with fake-login, fake-api-key or fake-device"}
}

func (f *fake) signIn(method string) error {
	f.fileMu.Lock()
	err := errors.New("the agent stopped")
	if !f.stopped {
		err = writeCredentials(f.getenv("HOME"), method)
	}
	f.fileMu.Unlock()
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.signedIn = f.credentials()
	f.mu.Unlock()
	return nil
}

// envToken is the credential Claude Code would take from its environment
// (--require-login): CLAUDE_CODE_OAUTH_TOKEN, else ANTHROPIC_API_KEY ("":
// none). One that holds "refused" is refused at every prompt.
func (f *fake) envToken() string {
	if t := f.getenv("CLAUDE_CODE_OAUTH_TOKEN"); t != "" {
		return t
	}
	return f.getenv("ANTHROPIC_API_KEY")
}

// probeStatus is what claude-agent-acp 0.81's `claude auth status` probe
// pushes once a session opens while an OAuth token in the environment signs
// Claude Code in: loggedIn but no subscription it can name, which it maps
// to kind "none" — though every turn works (--require-login only).
func (f *fake) probeStatus() {
	if f.o.RequireLogin && f.getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" {
		_ = f.conn.Notify(acp.MAuthStatus, map[string]any{"authStatus": map[string]any{"kind": "none"}})
	}
}

// account is which sign-in a turn uses: "token …<last 4>" for one in the
// environment, "home" for $HOME's, "none".
func (f *fake) account() string {
	if t := f.envToken(); t != "" {
		return "token …" + t[max(0, len(t)-4):]
	}
	if f.credentials() {
		return "home"
	}
	return "none"
}

// credentials reports whether $HOME/.fakeacp/credentials exists.
func (f *fake) credentials() bool {
	_, err := os.Stat(credentialsFile(f.getenv("HOME")))
	return err == nil
}

func credentialsFile(home string) string { return filepath.Join(home, ".fakeacp", "credentials") }

// writeCredentials records a sign-in (how, never a secret).
func writeCredentials(home, method string) error {
	file := credentialsFile(home)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]string{"method": method})
	return os.WriteFile(file, append(b, '\n'), 0o600)
}
