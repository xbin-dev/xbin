package acp

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The connector environments, per provider and outcome, against goldens
// (testdata/connector/<provider>-<outcome>.env: the variables to set, one a
// line and sorted; then "unset NAME" lines; "(nothing)" for an empty one).
// UPDATE_CONNECTOR_GOLDEN=1 rewrites them.
func TestConnectorEnvGolden(t *testing.T) {
	params := ConnectorParams{URL: "http://10.0.2.2:8080/api/xbin/llm/v1", Key: "xbl_TESTKEY",
		Models: []ConnectorModel{{ID: "openai/gpt-5", Name: "GPT-5", Context: 400000, Output: 128000}, {ID: "local/qwen"}},
		Model:  "openai/gpt-5"}
	provs := append(Providers(), Fake([]string{"fakeacp"}), Fake([]string{"fakeacp", "--llm-additive"}))
	names := []string{"claude", "codex", "gemini", "opencode", "fake", "fake-additive"}
	for i, p := range provs {
		for _, serves := range []string{ServeConnector, ServeOwn} {
			c := params
			c.Serves = serves
			env, ok := p.ConnectorEnv(c)
			if ok != (p.Connector != nil) {
				t.Fatalf("%s: ok %v with connector %v", names[i], ok, p.Connector != nil)
			}
			var b strings.Builder
			if !ok {
				b.WriteString("(no connector)\n")
			} else {
				for _, l := range env.Lines() {
					b.WriteString(l + "\n")
				}
				for _, u := range env.Unset {
					b.WriteString("unset " + u + "\n")
				}
				if len(env.Set) == 0 && len(env.Unset) == 0 {
					b.WriteString("(nothing)\n")
				}
				if env.UsesKey != (len(env.Set) > 0) {
					t.Errorf("%s/%s: UsesKey %v with %d variables", names[i], serves, env.UsesKey, len(env.Set))
				}
			}
			file := filepath.Join("testdata", "connector", names[i]+"-"+serves+".env")
			if os.Getenv("UPDATE_CONNECTOR_GOLDEN") != "" {
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if b.String() != string(want) {
				t.Errorf("%s\n got:\n%s\nwant:\n%s", file, b.String(), want)
			}
		}
	}
}

// What must hold whatever the goldens say: the key is in the connector's
// own variable and the provider's own request only, never in a variable the
// CLI reads a vendor's key from; a replacing provider gets nothing when the
// person's own sign-in serves the session, and an additive one no default
// model.
func TestConnectorEnvRules(t *testing.T) {
	c := ConnectorParams{URL: "http://xbin.test/v1", Key: "xbl_SECRET", Models: []ConnectorModel{{ID: "up/m1"}, {ID: "up/m2"}}}
	for _, p := range append(Providers(), Fake([]string{"fakeacp"})) {
		k := p.Connector
		if k == nil {
			if p.ID != "gemini" {
				t.Errorf("%s has no connector entry", p.ID)
			}
			continue
		}
		if !slices.Contains([]string{WireChat, WireResponses, WireAnthropic}, k.Wire) {
			t.Errorf("%s: wire %q", p.ID, k.Wire)
		}
		for _, serves := range []string{ServeConnector, ServeOwn} {
			c.Serves = serves
			env, _ := p.ConnectorEnv(c)
			for _, key := range p.Keys {
				if _, set := env.Set[key.Env]; set {
					t.Errorf("%s/%s: the connector environment sets %s, a variable the CLI reads a vendor's key from", p.ID, serves, key.Env)
				}
			}
			for _, n := range k.Never {
				if _, set := env.Set[n]; set {
					t.Errorf("%s/%s: sets %s, which is on its Never list", p.ID, serves, n)
				}
			}
			for name, v := range env.Set {
				if strings.Contains(v, c.Key) && name != ConnectorKeyEnv && name != "DEFAULT_AUTH_REQUEST" && name != "ANTHROPIC_AUTH_TOKEN" {
					t.Errorf("%s/%s: the key is in %s", p.ID, serves, name)
				}
			}
			switch {
			case serves == ServeOwn && !k.Additive:
				if len(env.Set) != 0 || len(env.Unset) != 0 || env.UsesKey {
					t.Errorf("%s: a replacing provider's environment when the person's own sign-in serves: %+v", p.ID, env)
				}
			case serves == ServeOwn:
				if len(env.Unset) != 0 {
					t.Errorf("%s: an additive provider drops variables when the person's own serves: %+v", p.ID, env)
				}
				for name, v := range env.Set {
					if strings.Contains(v, `"model":"`) {
						t.Errorf("%s: %s names a default model though the person's own sign-in serves", p.ID, name)
					}
				}
			case k.Additive:
				if len(env.Unset) != 0 {
					t.Errorf("%s: an additive provider drops the person's own variables: %v", p.ID, env.Unset)
				}
			default:
				if !slices.Equal(env.Unset, k.Never) {
					t.Errorf("%s: unset %v, Never %v", p.ID, env.Unset, k.Never)
				}
			}
			if env.UsesKey && (env.Set[ConnectorURLEnv] != c.URL || env.Set[ConnectorKeyEnv] != c.Key) {
				t.Errorf("%s/%s: %s/%s missing", p.ID, serves, ConnectorURLEnv, ConnectorKeyEnv)
			}
		}
	}
	// the default model: Model, else the first listed
	o, _ := Lookup("opencode")
	c.Serves, c.Model = ServeConnector, ""
	env, _ := o.ConnectorEnv(c)
	if !strings.Contains(env.Set["OPENCODE_CONFIG_CONTENT"], `"model":"xbin/up/m1"`) {
		t.Errorf("opencode's default without Model: %s", env.Set["OPENCODE_CONFIG_CONTENT"])
	}
	c.Model = "up/unlisted"
	env, _ = o.ConnectorEnv(c)
	if cfg := env.Set["OPENCODE_CONFIG_CONTENT"]; !strings.Contains(cfg, `"model":"xbin/up/unlisted"`) || !strings.Contains(cfg, `"up/unlisted":{"name":"up/unlisted"}`) {
		t.Errorf("opencode's default outside Models is not declared: %s", cfg)
	}
	cx, _ := Lookup("codex")
	if env, _ = cx.ConnectorEnv(c); env.Set["CODEX_CONFIG"] != `{"model":"up/unlisted"}` {
		t.Errorf("codex's default model: %q", env.Set["CODEX_CONFIG"])
	}
}

func TestConnectorServes(t *testing.T) {
	for _, c := range []struct {
		session, saved, workspace string
		own                       bool
		want                      string
	}{
		{"", "", "", false, ServeConnector}, // nothing said: own-first, no sign-in
		{"", "", "", true, ServeOwn},
		{"", "", UseOwnFirst, true, ServeOwn},
		{"", "", UseConnector, true, ServeConnector}, // the workspace's default is the connector
		{"", UseOwn, UseConnector, false, ServeOwn},  // the person's saved default outranks the workspace's
		{"", UseConnector, UseOwnFirst, true, ServeConnector},
		{UseOwn, UseConnector, UseConnector, false, ServeOwn}, // the session's choice outranks both
		{UseConnector, UseOwn, UseOwnFirst, true, ServeConnector},
		{UseOwnFirst, UseConnector, UseConnector, true, ServeOwn},
		{"bogus", "", UseConnector, true, ServeConnector}, // an unknown word says nothing
	} {
		if got := ConnectorServes(c.session, c.saved, c.workspace, c.own); got != c.want {
			t.Errorf("ConnectorServes(%q, %q, %q, %v) = %s, want %s", c.session, c.saved, c.workspace, c.own, got, c.want)
		}
	}
}

func TestHasOwnLogin(t *testing.T) {
	o, _ := Lookup("opencode")
	none := func(string) bool { return false }
	if o.HasOwnLogin([]string{"PATH=/bin", "OPENAI_API_KEY="}, none) {
		t.Error("an empty key variable is no sign-in")
	}
	if !o.HasOwnLogin([]string{"OPENAI_API_KEY=sk-x"}, none) {
		t.Error("a key in the environment is the person's own sign-in")
	}
	if o.HasOwnLogin([]string{"XOPENAI_API_KEY=sk-x", "OPENAI_API_KEY_2=sk-x"}, nil) {
		t.Error("a variable that only resembles a key's name")
	}
	var asked []string
	if !o.HasOwnLogin(nil, func(w string) bool { asked = append(asked, w); return true }) || !slices.Equal(asked, o.LoginFiles[:1]) {
		t.Errorf("login files asked: %v", asked)
	}
}

func TestConnectorEnvApply(t *testing.T) {
	e := ConnectorEnv{Set: map[string]string{"B": "2", "A": "1"}, Unset: []string{"GONE"}}
	base := []string{"PATH=/bin", "GONE=x", "A=old", "GONEX=stays", "NOEQ"}
	got := e.Apply(base)
	want := []string{"PATH=/bin", "GONEX=stays", "NOEQ", "A=1", "B=2"}
	if !slices.Equal(got, want) || base[1] != "GONE=x" {
		t.Errorf("Apply = %v, want %v (base %v)", got, want, base)
	}
}

func TestExpandWord(t *testing.T) {
	env := map[string]string{"HOME": "/home/u", "CODEX_HOME": "", "XDG_DATA_HOME": "/data", "A1": "x"}
	get := func(k string) string { return env[k] }
	for word, want := range map[string]string{
		`$HOME/.gemini/oauth_creds.json`:                          "/home/u/.gemini/oauth_creds.json",
		`${CODEX_HOME:-$HOME/.codex}/auth.json`:                   "/home/u/.codex/auth.json",
		`${XDG_DATA_HOME:-$HOME/.local/share}/opencode/auth.json`: "/data/opencode/auth.json",
		`${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.credentials.json`:   "/home/u/.claude/.credentials.json",
		`${NOPE:-${HOME}/x}/y`:                                    "/home/u/x/y",
		`${HOME}`:                                                 "/home/u",
		`plain`:                                                   "plain",
		`$A1$`:                                                    "x$",
		`a$1b`:                                                    "a$1b",
		`${unclosed`:                                              "${unclosed",
	} {
		if got := ExpandWord(word, get); got != want {
			t.Errorf("ExpandWord(%q) = %q, want %q", word, got, want)
		}
	}
	// every word of the catalog expands to an absolute path under $HOME
	for _, p := range append(Providers(), Fake([]string{"fakeacp"})) {
		for _, w := range append(append([]string(nil), p.LoginFiles...), p.AuthFile) {
			if w == "" {
				continue
			}
			if got := ExpandWord(w, func(k string) string { return map[string]string{"HOME": "/h"}[k] }); !strings.HasPrefix(got, "/h/") || strings.ContainsAny(got, "${}") {
				t.Errorf("%s: %q expands to %q", p.ID, w, got)
			}
		}
	}
}
