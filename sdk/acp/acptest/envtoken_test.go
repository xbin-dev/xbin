package acptest

import (
	"strings"
	"testing"
)

// A credential in the environment outranks $HOME's (--require-login), as
// Claude Code's does: CLAUDE_CODE_OAUTH_TOKEN (or ANTHROPIC_API_KEY) signs
// every prompt in with no credentials file — while, for an OAuth token, the
// status after session/new and session/load says "none", as claude-agent-acp
// 0.81's probe does — and one that holds "refused" fails every prompt, a
// $HOME sign-in or not; `whoami` says which sign-in a turn used.
func TestEnvToken(t *testing.T) {
	for _, c := range []struct {
		env, want string
		ok        bool
	}{
		{"CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-work-AAAA9f3e", "account: token …9f3e", true},
		{"ANTHROPIC_API_KEY=sk-ant-api03-key-0001", "account: token …0001", true},
		{"CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-refused-0002", "", false},
		{"", "account: home", true},
	} {
		home := t.TempDir()
		k, v, _ := strings.Cut(c.env, "=")
		if !c.ok || k == "" { // a $HOME sign-in: used without a token, never over a refused one
			if err := writeCredentials(home, "fake-login"); err != nil {
				t.Fatal(err)
			}
		}
		o := Options{RequireLogin: true, Persist: true, wait: quick}
		o.Getenv = func(n string) string { return map[string]string{"HOME": home, k: v}[n] }
		d := runServe(t, home, o)
		d.call(1, "initialize", initParams)
		d.response(1)
		sid := str(get(d.sessionNew(2, newParams), "result", "sessionId"))
		probed := func(what string) {
			d.until(what, func(f *frame) bool {
				return f.Method == "_auth/status_update" && get(f, "params", "authStatus", "kind") == "none"
			})
		}
		if k == "CLAUDE_CODE_OAUTH_TOKEN" {
			probed("the probe's status after session/new")
		}
		prompt := strings.Replace(promptParams("whoami"), "fake-1", sid, 1)
		d.call(3, "session/prompt", prompt)
		if !c.ok {
			if r := d.response(3); get(r, "error", "code") != float64(-32000) || !strings.Contains(str(get(r, "error", "message")), "revoked") {
				t.Fatalf("%s: a prompt: %s", c.env, r.raw)
			}
			d.finish()
			continue
		}
		d.chunkText(c.want)
		d.response(3)
		d.call(4, "session/load", `{"sessionId":"`+sid+`","cwd":"/work","mcpServers":[]}`)
		d.response(4)
		if k == "CLAUDE_CODE_OAUTH_TOKEN" {
			probed("the probe's status after session/load")
		}
		d.finish()
	}
}
