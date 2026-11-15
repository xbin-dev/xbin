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
		for _, serves := range []string{ServeConnector, ServeOwn, "only"} {
			c := params
			c.Serves = serves
			if serves == "only" { // the connector alone may serve
				c.Serves, c.Only = ServeConnector, true
			}
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
	if env, _ = cx.ConnectorEnv(c); !strings.Contains(env.Set["CODEX_CONFIG"], `"model":"up/unlisted"`) {
		t.Errorf("codex's default model: %q", env.Set["CODEX_CONFIG"])
	}
	// codex is left no other provider: the connector is the session's model
	// provider in its own config, over whatever config.toml names (the
	// gateway request alone is not applied where codex asks for no sign-in)
	if cfg := env.Set["CODEX_CONFIG"]; env.Set["MODEL_PROVIDER"] != "custom-gateway" || !strings.Contains(cfg, `"model_provider":"custom-gateway"`) ||
		!strings.Contains(cfg, `"custom-gateway":{"base_url":"http://xbin.test/v1","env_key":"XBIN_LLM_KEY","name":"LLM by xbin","wire_api":"responses"}`) || strings.Contains(cfg, c.Key) {
		t.Errorf("codex's session config: MODEL_PROVIDER %q, CODEX_CONFIG %s", env.Set["MODEL_PROVIDER"], cfg)
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

// Where the connector alone may serve, opencode knows no other provider;
// what a provider's list supplies is rendered only when it is safe in a
// configuration (opencode substitutes {env:NAME} and {file:path}); and a
// model option is checked against the connector's models.
func TestConnectorOnlyAndSafe(t *testing.T) {
	o, _ := Lookup("opencode")
	cx, _ := Lookup("codex")
	c := ConnectorParams{URL: "http://xbin.test/v1", Key: "xbl_SECRET", Serves: ServeConnector, Only: true,
		Models: []ConnectorModel{{ID: "up/m1"}, {ID: "up/m2"}}, Model: "up/m2"}
	env, _ := o.ConnectorEnv(c)
	cfg := env.Set["OPENCODE_CONFIG_CONTENT"]
	for _, want := range []string{`"enabled_providers":["xbin"]`, `"model":"xbin/up/m2"`, `"small_model":"xbin/up/m2"`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("opencode, the connector alone: no %s in %s", want, cfg)
		}
	}
	c.Only = false
	if env, _ = o.ConnectorEnv(c); strings.Contains(env.Set["OPENCODE_CONFIG_CONTENT"], "enabled_providers") {
		t.Errorf("opencode's own providers are switched off without the rule: %s", env.Set["OPENCODE_CONFIG_CONTENT"])
	}
	c.Only, c.Serves = true, ServeOwn
	if env, _ = o.ConnectorEnv(c); strings.Contains(env.Set["OPENCODE_CONFIG_CONTENT"], "enabled_providers") {
		t.Errorf("the person's own sign-in serves, yet their providers are off: %s", env.Set["OPENCODE_CONFIG_CONTENT"])
	}

	// ids and names from an upstream's list
	bad := []string{"{env:XBIN_LLM_KEY}", "up/{file:/etc/passwd}", "a b", "a\"b", "a$b", "a\\b", "", strings.Repeat("m", 201), "a\nb"}
	for _, id := range bad {
		if ValidConnectorModel(id) {
			t.Errorf("ValidConnectorModel(%q)", id)
		}
	}
	for _, id := range []string{"openai/gpt-5", "anthropic/claude-3.5-sonnet:beta", "local/llama3:8b-q4_K_M", "accounts/x/models/y@2024", "us.anthropic.claude-v2:1"} {
		if !ValidConnectorModel(id) {
			t.Errorf("!ValidConnectorModel(%q)", id)
		}
	}
	c = ConnectorParams{URL: "http://xbin.test/v1", Key: "xbl_SECRET", Serves: ServeConnector, Model: "{env:XBIN_LLM_KEY}",
		Models: []ConnectorModel{{ID: "{env:XBIN_LLM_KEY}"}, {ID: "up/{file:/etc/shadow}", Name: "x"}, {ID: "up/ok", Name: "{file:/etc/passwd}"}, {ID: "up/fine", Name: "Fine"}}}
	for _, p := range []Provider{o, cx, Fake([]string{"fakeacp"})} {
		env, _ := p.ConnectorEnv(c)
		for name, v := range env.Set {
			if name == "OPENCODE_CONFIG_CONTENT" {
				v = strings.Replace(v, `"apiKey":"{env:XBIN_LLM_KEY}"`, "", 1) // the one substitution, the table's own
			}
			if strings.Contains(v, "{env:") || strings.Contains(v, "{file:") {
				t.Errorf("%s: a substitution reached %s: %s", p.ID, name, v)
			}
		}
	}
	env, _ = o.ConnectorEnv(c)
	if cfg := env.Set["OPENCODE_CONFIG_CONTENT"]; !strings.Contains(cfg, `"model":"xbin/up/ok"`) || !strings.Contains(cfg, `"up/ok":{"name":"up/ok"}`) || !strings.Contains(cfg, `"up/fine":{"name":"Fine"}`) {
		t.Errorf("the safe models, the first of them the default: %s", cfg)
	}
	if env, _ = cx.ConnectorEnv(c); !strings.Contains(env.Set["CODEX_CONFIG"], `"model":"up/ok"`) {
		t.Errorf("codex's default with an unsafe one named: %q", env.Set["CODEX_CONFIG"])
	}
	for _, c := range []ConnectorParams{{URL: "http://x/{env:A}/v1", Key: "k"}, {URL: "http://x/v1", Key: "{file:/k}"}, {URL: "http://x/v1", Key: "k", Name: "{env:HOME}"}} {
		c.Serves = ServeConnector
		if env, ok := o.ConnectorEnv(c); !ok || len(env.Set) != 0 || env.UsesKey {
			t.Errorf("rendered with %+v: %+v", c, env)
		}
	}

	// a client's option, where the connector alone may serve
	models := []ConnectorModel{{ID: "up/m1"}, {ID: "gpt-5.5"}}
	for _, k := range []struct {
		p                   Provider
		id, category, value string
		want                bool
	}{
		{o, "model", "model", "xbin/up/m1", true},
		{o, "model", "model", "opencode/big-pickle", false},
		{o, "model", "model", "anthropic/claude-sonnet-4-5", false},
		{o, "model", "", "up/m1", false}, // opencode writes the connector's models under its provider id
		{o, "mode", "mode", "plan", true},
		{o, "anything", "model", "opencode/big-pickle", false}, // whatever its id, an option of the model category
		{cx, "model", "model", "up/m1", true},
		{cx, "model", "model", "gpt-5.5", true}, // a name the connector serves (an alias)
		{cx, "model", "model", "gpt-6-sol", false},
		{cx, "collaboration_mode", "collaboration_mode", "plan", true},
	} {
		if got := k.p.Connector.AllowsOption(k.id, k.category, k.value, models); got != k.want {
			t.Errorf("%s: AllowsOption(%q, %q, %q) = %v", k.p.ID, k.id, k.category, k.value, got)
		}
	}
	var none *Connector
	if none.AllowsOption("mode", "mode", "plan", models) {
		t.Error("a provider with no connector entry allows an option where the connector alone may serve")
	}
}
