package host

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent/acp"
)

// The host settles who serves the agent's model calls from what it sees:
// the rule, a credential in the environment, a login file in $HOME — and
// applies that outcome's environment, nothing of the other's.
func TestSettleLLM(t *testing.T) {
	home := t.TempDir()
	login := filepath.Join(home, ".cli", "auth.json")
	base := []string{"PATH=/bin", "HOME=" + home, "OWN_KEY=", "OLD=1"}
	l := func(rule string) *acp.SpawnLLM {
		return &acp.SpawnLLM{Rule: rule, LoginFiles: []string{`${CLI_HOME:-$HOME/.cli}/auth.json`}, KeyEnvs: []string{"OWN_KEY"},
			Connector: acp.EnvDelta{Set: []string{"LLM_URL=u", "LLM_KEY=k", "OLD=2"}, Unset: []string{"OWN_KEY"}},
			Own:       acp.EnvDelta{Set: []string{"LLM_URL=u"}},
			Logins:    map[string][]string{"cli": {`$HOME/.cli/auth.json`}, "other": {`$HOME/.other/creds`}}}
	}
	if env, serves, logins := settleLLM(base, nil); serves != "" || logins != nil || !slices.Equal(env, base) {
		t.Fatalf("no connector: %v %q %v", env, serves, logins)
	}
	// own-first, nothing of the person's: the connector, its delta applied
	env, serves, logins := settleLLM(base, l("own-first"))
	if serves != "connector" || !slices.Equal(env, []string{"PATH=/bin", "HOME=" + home, "LLM_URL=u", "LLM_KEY=k", "OLD=2"}) {
		t.Fatalf("own-first without a sign-in: %q %v", serves, env)
	}
	if logins["cli"] || logins["other"] || len(logins) != 2 {
		t.Fatalf("logins: %v", logins)
	}
	// an empty login file is no sign-in; a real one is
	if err := os.MkdirAll(filepath.Dir(login), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(login, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, serves, _ = settleLLM(base, l("own-first")); serves != "connector" {
		t.Fatalf("an empty login file counted as a sign-in: %q", serves)
	}
	if err := os.WriteFile(login, []byte(`{"k":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env, serves, logins = settleLLM(base, l("own-first"))
	if serves != "own" || !slices.Equal(env, []string{"PATH=/bin", "HOME=" + home, "OWN_KEY=", "OLD=1", "LLM_URL=u"}) || !logins["cli"] || logins["other"] {
		t.Fatalf("own-first with a login file: %q %v %v", serves, env, logins)
	}
	// the file is found where the CLI's own variable points
	elsewhere := append(slices.Clone(base), "CLI_HOME="+t.TempDir())
	if _, serves, _ = settleLLM(elsewhere, l("own-first")); serves != "connector" {
		t.Fatalf("CLI_HOME elsewhere: %q", serves)
	}
	// the rule outranks what is found
	if _, serves, _ = settleLLM(base, l("connector")); serves != "connector" {
		t.Fatalf("forced connector: %q", serves)
	}
	_ = os.Remove(login)
	if env, serves, _ = settleLLM(base, l("own")); serves != "own" || slices.Contains(env, "LLM_KEY=k") {
		t.Fatalf("forced own: %q %v", serves, env)
	}
	// a key of the person's in the environment is a sign-in — the last line of a name counts
	keyed := append(slices.Clone(base), "OWN_KEY=sk-theirs")
	if env, serves, _ = settleLLM(keyed, l("own-first")); serves != "own" || !slices.Contains(env, "OWN_KEY=sk-theirs") {
		t.Fatalf("a key in the environment: %q %v", serves, env)
	}
	// …and is dropped when the connector is forced
	if env, serves, _ = settleLLM(keyed, l("connector")); serves != "connector" || slices.Contains(env, "OWN_KEY=sk-theirs") {
		t.Fatalf("forced connector with their key: %q %v", serves, env)
	}
}
