package host

import (
	"os"
	"strings"

	"github.com/xbin-dev/xbin/internal/agent/acp"
	sdkacp "github.com/xbin-dev/xbin/sdk/acp"
)

// llm.go — the LLM connector's part of the agent's environment (D226 part
// 2, sdk/acp connector.go). The daemon renders both outcomes and sends them
// with the rule; the host settles it, because "the person has a sign-in of
// their own" is a fact of the session's $HOME, which the daemon never reads
// (it runs nothing and trusts nothing there) and the host sees exactly as
// the agent will.

// settleLLM answers the environment the agent starts with, who serves its
// model calls ("connector" | "own"; "" without a connector) and which
// providers the person is signed in to here.
func settleLLM(env []string, l *acp.SpawnLLM) (out []string, serves string, logins map[string]bool) {
	if l == nil {
		return env, "", nil
	}
	getenv := func(name string) string {
		for i := len(env) - 1; i >= 0; i-- { // the last line of a name is the one os/exec keeps
			if v, ok := strings.CutPrefix(env[i], name+"="); ok {
				return v
			}
		}
		return ""
	}
	exists := func(word string) bool {
		p := sdkacp.ExpandWord(word, getenv)
		if p == "" || !strings.HasPrefix(p, "/") {
			return false
		}
		fi, err := os.Stat(p)
		return err == nil && !fi.IsDir() && fi.Size() > 0
	}
	own := false
	for _, k := range l.KeyEnvs {
		own = own || getenv(k) != ""
	}
	for _, f := range l.LoginFiles {
		own = own || exists(f)
	}
	serves = sdkacp.ConnectorServes(l.Rule, "", "", own)
	delta := l.Own
	if serves == sdkacp.ServeConnector {
		delta = l.Connector
	}
	set := map[string]bool{}
	for _, kv := range delta.Set {
		name, _, _ := strings.Cut(kv, "=")
		set[name] = true
	}
	for _, name := range delta.Unset {
		set[name] = true
	}
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); !set[name] {
			out = append(out, kv)
		}
	}
	out = append(out, delta.Set...)
	if len(l.Logins) > 0 {
		logins = map[string]bool{}
		for id, files := range l.Logins {
			for _, f := range files {
				logins[id] = logins[id] || exists(f)
			}
		}
	}
	return out, serves, logins
}
