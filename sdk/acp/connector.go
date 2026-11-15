package acp

import (
	"encoding/json"
	"slices"
	"sort"
)

// connector.go — pointing a provider's CLI at an LLM connector: an
// OpenAI-compatible endpoint a host serves in place of the vendor's (xbind's
// built-in one, docs/llm.md), with a key the host minted for one session.
// Each catalog entry says how its CLI takes one from its environment alone
// (Provider.Connector), and ConnectorEnv renders that environment for one
// session. One table serves every host that runs these CLIs — xbind's tile
// agent sessions and a tile backend that runs them in sandboxes.
//
// Who serves a session's model calls is the host's to decide before it
// renders (ConnectorServes): the session's own choice, else the person's
// saved default for the provider, else the workspace's — where "own-first"
// means the person's own sign-in when they have one (HasOwnLogin), and the
// connector otherwise. A sign-in the person already has keeps working.

// The wires a connector may serve (Connector.Wire).
const (
	WireChat      = "chat"      // OpenAI Chat Completions
	WireResponses = "responses" // OpenAI Responses
	WireAnthropic = "anthropic" // Anthropic Messages
)

// A credential choice, at any level of the precedence (ConnectorServes).
const (
	UseConnector = "connector" // the connector serves the session
	UseOwn       = "own"       // the person's own sign-in does
	UseOwnFirst  = "own-first" // their own sign-in when they have one, else the connector
)

// Who serves a session, once decided (ConnectorParams.Serves).
const (
	ServeConnector = "connector"
	ServeOwn       = "own"
)

// The variables every connector environment names the endpoint in, and the
// provider id and name the CLIs show it under.
const (
	ConnectorURLEnv = "XBIN_LLM_URL" // the OpenAI base URL (…/v1)
	ConnectorKeyEnv = "XBIN_LLM_KEY" // the session's key
	ConnectorID     = "xbin"         // the provider id in a CLI's own config (opencode: models are xbin/<id>)
	ConnectorName   = "LLM by xbin"
)

// Connector is how to point a provider's CLI at an LLM connector from its
// environment alone. A provider without one cannot be (gemini: its CLI
// speaks Google's wire only). Shared: read-only.
type Connector struct {
	// Wire is what the CLI speaks to the endpoint. A host offers the
	// connector for a provider only when its connector serves this wire.
	Wire string
	// Additive: the CLI keeps the person's own providers next to the
	// connector's (opencode), so the connector is always declared and the
	// precedence only decides whether it is the session's default model.
	// false: the variables replace the person's sign-in for the session —
	// they are set only when the connector serves it.
	Additive bool
	// ModelOption is the ACP config option a client picks the session's
	// model with ("model"; "" = none), and ModelPrefix what the CLI puts
	// before one of the connector's model ids there ("xbin/" for opencode,
	// "" for a CLI that sends the id as it is). The default model itself
	// travels in the environment.
	ModelOption, ModelPrefix string
	// Never are variables a host never sets to the connector's key: one
	// that would carry it to the vendor's own address, or replace what the
	// person signed in with. For a provider that is not Additive they also
	// outrank or contradict the connector's variables, so a host drops them
	// (ConnectorEnv's Unset) when the connector serves the session; an
	// Additive provider's are the person's own and stay.
	Never []string

	env func(c ConnectorParams, def bool) map[string]string
}

// ConnectorModel is one model a connector offers.
type ConnectorModel struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`    // for people ("" = the id)
	Context int    `json:"context,omitempty"` // context window in tokens (0 = unknown)
	Output  int    `json:"output,omitempty"`  // most output tokens (0 = unknown)
}

// ConnectorParams is one connector endpoint for one session.
type ConnectorParams struct {
	// URL is the OpenAI base URL as the CLI reaches it, ending in /v1
	// (Anthropic clients are given it without the /v1: they append
	// /v1/messages themselves).
	URL string
	// Key is the session's key: narrow (good for the connector only) and
	// short-lived — some CLIs write their environment to disk.
	Key string
	// Name is what people see the connector called ("" = ConnectorName).
	Name string
	// Models are the models offered, and Model the default among them by id
	// ("" = the first). A CLI that lists no models on its own (opencode)
	// offers exactly these.
	Models []ConnectorModel
	Model  string
	// Serves is who serves the session: ServeConnector or ServeOwn (the
	// precedence's outcome, ConnectorServes).
	Serves string
	// Only: the connector alone may serve the session — a rule of the
	// host's that nobody's choice overrides. With Serves ServeConnector the
	// CLI is then configured so that nothing else can: an Additive
	// provider's own providers are switched off for the session (opencode:
	// enabled_providers names the connector alone, so neither a sign-in in
	// the person's auth.json, a provider in their or the project's config,
	// nor the vendor's free models can be picked). A replacing provider's
	// variables already leave it nothing else. The host still checks what
	// its client asks the CLI to switch to (AllowsOption).
	Only bool
}

// ConnectorEnv is a session's connector environment, rendered.
type ConnectorEnv struct {
	// Set are the variables to add to the CLI's environment (sorted by
	// name in Lines). Empty when the provider's variables replace a sign-in
	// and the person's own serves the session.
	Set map[string]string
	// Unset are variables to drop from the environment the CLI starts with
	// (a replacing provider's Never, when the connector serves the session).
	Unset []string
	// UsesKey reports whether Set carries the key: false means the session
	// has no use for it and the host revokes it at once.
	UsesKey bool
}

// Lines is Set as an environment's lines ("K=V"), sorted by name.
func (e ConnectorEnv) Lines() []string {
	out := make([]string, 0, len(e.Set))
	for k, v := range e.Set {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// Apply is env (an environment's "K=V" lines) with Unset dropped and Set
// added: what the CLI starts with. env itself is not changed.
func (e ConnectorEnv) Apply(env []string) []string {
	out := make([]string, 0, len(env)+len(e.Set))
	for _, kv := range env {
		name := kv
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				name = kv[:i]
				break
			}
		}
		if _, set := e.Set[name]; set || slices.Contains(e.Unset, name) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, e.Lines()...)
}

// ConnectorServes resolves who serves a session, first choice wins: the
// session's own (session), the person's saved default for the provider
// (saved), the workspace's (workspace) — each UseConnector, UseOwn,
// UseOwnFirst or "" (says nothing); nothing said anywhere is UseOwnFirst.
// own reports whether the person has their own sign-in for the provider
// (HasOwnLogin), which decides UseOwnFirst. It answers ServeConnector or
// ServeOwn; a host with no connector for the provider never asks.
func ConnectorServes(session, saved, workspace string, own bool) string {
	for _, c := range []string{session, saved, workspace, UseOwnFirst} {
		switch c {
		case UseConnector:
			return ServeConnector
		case UseOwn:
			return ServeOwn
		case UseOwnFirst:
			if own {
				return ServeOwn
			}
			return ServeConnector
		}
	}
	return ServeConnector
}

// ConnectorEnv renders the environment that points p's CLI at the connector
// c for one session. ok is false when p has no Connector (nothing to
// render). The outcome is c.Serves:
//
//   - ServeConnector: the provider's variables, with the connector as the
//     default (Model, or the first of Models), and a replacing provider's
//     Never in Unset.
//   - ServeOwn, an Additive provider: the connector declared, so its models
//     can be picked, and no default model — the person's own stays theirs.
//   - ServeOwn, a replacing provider: nothing.
//
// Whenever Set carries the key it also names the endpoint in
// ConnectorURLEnv and the key in ConnectorKeyEnv, for whatever else runs in
// the session. The key is never put in a variable of p.Keys.
//
// What is rendered is data a provider's list supplied, so only what is safe
// in every CLI's configuration goes in: a model whose id is not a
// ValidConnectorModel is left out (the default too — then the first that
// is), a model's name that is not plain text is replaced by its id, and
// nothing is rendered at all for a URL, key or connector name that could be
// read as a substitution ({env:…}, {file:…}).
func (p Provider) ConnectorEnv(c ConnectorParams) (env ConnectorEnv, ok bool) {
	k := p.Connector
	if k == nil || k.env == nil {
		return ConnectorEnv{}, false
	}
	if c.Name == "" {
		c.Name = ConnectorName
	}
	if !plainText(c.Name) || !plainText(c.URL) || !plainText(c.Key) {
		return ConnectorEnv{}, true
	}
	models := make([]ConnectorModel, 0, len(c.Models))
	for _, m := range c.Models {
		if !ValidConnectorModel(m.ID) {
			continue
		}
		if !plainText(m.Name) {
			m.Name = ""
		}
		models = append(models, m)
	}
	c.Models = models
	if !ValidConnectorModel(c.Model) {
		c.Model = ""
	}
	if c.Model == "" && len(c.Models) > 0 {
		c.Model = c.Models[0].ID
	}
	serves := c.Serves == ServeConnector
	if !serves && !k.Additive {
		return ConnectorEnv{}, true
	}
	env.Set = k.env(c, serves)
	env.Set[ConnectorURLEnv], env.Set[ConnectorKeyEnv] = c.URL, c.Key
	env.UsesKey = true
	if serves && !k.Additive {
		env.Unset = append([]string(nil), k.Never...)
	}
	return env, true
}

// maxConnectorModel bounds a model id a configuration is rendered with.
const maxConnectorModel = 200

// ValidConnectorModel reports whether id is a model id safe to put in any
// CLI's configuration: 1–200 of letters, digits and . _ : / @ + ~ = , - —
// what model ids are made of, and nothing a configuration format gives a
// meaning to (braces: opencode substitutes {env:NAME} and {file:path};
// quotes, $, spaces, backslashes).
func ValidConnectorModel(id string) bool {
	if id == "" || len(id) > maxConnectorModel {
		return false
	}
	for i := 0; i < len(id); i++ {
		switch c := id[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == ':', c == '/', c == '@', c == '+', c == '~', c == '=', c == ',', c == '-':
		default:
			return false
		}
	}
	return true
}

// plainText reports whether s can stand in a rendered configuration as it
// is: no braces (a substitution's form), no control characters.
func plainText(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '{' || c == '}' || c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// AllowsOption reports whether a session that only the connector may serve
// (ConnectorParams.Only) can have the CLI's config option id — category as
// the agent advertised it ("model", "mode", …; "" unknown) — set to value:
// an option that picks no model passes, and the model's must name one of
// models as this CLI writes it (ModelPrefix + id). A host asks before it
// passes a client's choice on, at the session's start and later.
func (k *Connector) AllowsOption(id, category, value string, models []ConnectorModel) bool {
	if k == nil {
		return false
	}
	if category != "model" && (k.ModelOption == "" || id != k.ModelOption) {
		return true
	}
	for _, m := range models {
		if value == k.ModelPrefix+m.ID {
			return true
		}
	}
	return false
}

// HasOwnLogin reports whether a person has their own sign-in for p, from
// what a host can see where the CLI runs: exists says whether one of
// p.LoginFiles is there (each a shell word — $HOME, ${CODEX_HOME:-…} — the
// host expands), and env is the environment the CLI would start with (a
// non-empty variable of p.Keys is a credential of the person's).
func (p Provider) HasOwnLogin(env []string, exists func(shellWord string) bool) bool {
	for _, kv := range env {
		for _, key := range p.Keys {
			if len(kv) > len(key.Env)+1 && kv[len(key.Env)] == '=' && kv[:len(key.Env)] == key.Env {
				return true
			}
		}
	}
	if exists == nil {
		return false
	}
	for _, f := range p.LoginFiles {
		if exists(f) {
			return true
		}
	}
	return false
}

// opencodeConnector: an inline config (OPENCODE_CONFIG_CONTENT, merged over
// the person's own and winning over it) that declares the connector as a
// provider, its key read from the environment. Its "model" member is what
// makes the connector the session's default — without it opencode keeps the
// person's model, or with no sign-in at all its vendor's free models. Where
// the connector alone may serve (Only), "enabled_providers" names it alone:
// the inline config is merged last, so it wins over the person's and the
// project's own lists, and opencode then knows no other provider — its
// model list holds the connector's models and nothing else can be set
// (checked against opencode 1.18.32 with a sign-in in auth.json, a custom
// provider in the global config and a project config naming others).
// OPENCODE_AUTH_CONTENT would replace the person's own sign-ins; a key in
// OPENAI_API_KEY would go to OpenAI's own address.
var opencodeConnector = &Connector{Wire: WireChat, Additive: true, ModelOption: "model", ModelPrefix: ConnectorID + "/",
	Never: []string{"OPENCODE_AUTH_CONTENT", "OPENAI_API_KEY"},
	env: func(c ConnectorParams, def bool) map[string]string {
		type limit struct {
			Context int `json:"context"`
			Output  int `json:"output"`
		}
		type model struct {
			Name  string `json:"name"`
			Limit *limit `json:"limit,omitempty"`
		}
		models := map[string]model{}
		for _, m := range c.Models {
			e := model{Name: m.Name}
			if e.Name == "" {
				e.Name = m.ID
			}
			if m.Context > 0 && m.Output > 0 {
				e.Limit = &limit{m.Context, m.Output}
			}
			models[m.ID] = e
		}
		if _, listed := models[c.Model]; c.Model != "" && !listed {
			models[c.Model] = model{Name: c.Model}
		}
		cfg := map[string]any{"provider": map[string]any{ConnectorID: map[string]any{
			"npm": "@ai-sdk/openai-compatible", "name": c.Name,
			"options": map[string]string{"baseURL": c.URL, "apiKey": "{env:" + ConnectorKeyEnv + "}"},
			"models":  models}}}
		if def && c.Model != "" {
			cfg["model"] = ConnectorID + "/" + c.Model
		}
		if def && c.Only { // nothing but the connector's provider exists in this session
			cfg["enabled_providers"] = []string{ConnectorID}
			if c.Model != "" {
				cfg["small_model"] = ConnectorID + "/" + c.Model // titles and summaries: not the vendor's small model
			}
		}
		b, _ := json.Marshal(cfg)
		return map[string]string{"OPENCODE_CONFIG_CONTENT": string(b)}
	}}

// codexGatewayID is the model provider codex-acp names its gateway
// (CUSTOM_GATEWAY_PROVIDER_ID): the connector is declared under the same
// id in the session's config, so both ways in name one provider.
const codexGatewayID = "custom-gateway"

// codexConnector: two things that together leave Codex no other provider.
// codex-acp's gateway auth request (DEFAULT_AUTH_REQUEST) — a base URL and
// headers, with no sign-in and nothing written to auth.json — is what a
// signed-out Codex needs to open a session at all. But the adapter applies
// it only where Codex asks for a sign-in: with a model_provider of the
// person's own in ~/.codex/config.toml (one that needs no OpenAI account)
// it is never applied, and the session would run on that provider. So the
// session's config (CODEX_CONFIG, JSON the adapter merges over the
// person's and a project's config.toml) declares the connector as a model
// provider itself — its key read from the environment (env_key) — and
// names it (model_provider, and MODEL_PROVIDER, which the adapter passes
// with every thread it starts). Checked against codex-acp 1.13.1 with no
// config, a personal config.toml and a project's, each naming another
// provider: the turn went to the connector every time.
//
// The CLI speaks Responses only. While these are set the person's own
// `codex login` is not used, so they are set only when the connector
// serves the session. The default model rides the same config: without it
// the adapter asks for its own default, a name only OpenAI serves. Codex
// has no metadata for a model it doesn't know and says so in each answer
// ("Model metadata … not found"); its own model names stay in its picker,
// for a connector that serves or aliases them. Codex writes its
// environment — the gateway request, the key in it — to
// $CODEX_HOME/shell_snapshots, where it stays: the key must be the
// session's own and end with it.
var codexConnector = &Connector{Wire: WireResponses, ModelOption: "model",
	Never: []string{"CODEX_API_KEY", "OPENAI_API_KEY"},
	env: func(c ConnectorParams, _ bool) map[string]string {
		type gateway struct {
			BaseURL      string            `json:"baseUrl"`
			Headers      map[string]string `json:"headers"`
			ProviderName string            `json:"providerName"`
		}
		b, _ := json.Marshal(map[string]any{"methodId": "gateway", "_meta": map[string]any{"gateway": gateway{
			BaseURL: c.URL, Headers: map[string]string{"Authorization": "Bearer " + c.Key}, ProviderName: c.Name}}})
		cfg := map[string]any{"model_provider": codexGatewayID, "model_providers": map[string]any{codexGatewayID: map[string]string{
			"name": c.Name, "base_url": c.URL, "wire_api": "responses", "env_key": ConnectorKeyEnv}}}
		if c.Model != "" {
			cfg["model"] = c.Model
		}
		m, _ := json.Marshal(cfg)
		return map[string]string{"DEFAULT_AUTH_REQUEST": string(b), "CODEX_CONFIG": string(m), "MODEL_PROVIDER": codexGatewayID}
	}}

// claudeConnector: Claude Code's gateway variables. ANTHROPIC_AUTH_TOKEN
// (sent as Authorization: Bearer) outranks every other credential of the
// CLI's, so they are set only when the connector serves the session — and
// never ANTHROPIC_BASE_URL alone, which would send the person's own token
// to the connector. It speaks Anthropic's Messages wire, so a host offers
// it only where its connector serves that.
var claudeConnector = &Connector{Wire: WireAnthropic,
	Never: []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"},
	env: func(c ConnectorParams, _ bool) map[string]string {
		root := c.URL
		if n := len(root); n >= 3 && root[n-3:] == "/v1" {
			root = root[:n-3] // the CLI appends /v1/messages
		}
		out := map[string]string{"ANTHROPIC_BASE_URL": root, "ANTHROPIC_AUTH_TOKEN": c.Key,
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}
		if c.Model != "" {
			for _, k := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL"} {
				out[k] = c.Model
			}
		}
		return out
	}}

// ExpandWord expands a shell word of the catalog (LoginFiles, AuthFile) the
// way a POSIX shell would: $NAME, ${NAME} and ${NAME:-default} (the default
// expanded in turn), with getenv as the environment the CLI runs with.
// Nothing else is special — the catalog's words use nothing else.
func ExpandWord(word string, getenv func(string) string) string {
	var out []byte
	for i := 0; i < len(word); i++ {
		if word[i] != '$' || i+1 == len(word) {
			out = append(out, word[i])
			continue
		}
		if word[i+1] == '{' {
			end, depth := -1, 0
			for j := i + 1; j < len(word); j++ {
				if word[j] == '{' {
					depth++
				} else if word[j] == '}' {
					if depth--; depth == 0 {
						end = j
						break
					}
				}
			}
			if end < 0 {
				out = append(out, word[i])
				continue
			}
			body := word[i+2 : end]
			name, def, hasDef := body, "", false
			for j := 0; j+1 < len(body); j++ {
				if body[j] == ':' && body[j+1] == '-' {
					name, def, hasDef = body[:j], body[j+2:], true
					break
				}
			}
			v := getenv(name)
			if v == "" && hasDef {
				v = ExpandWord(def, getenv)
			}
			out = append(out, v...)
			i = end
			continue
		}
		j := i + 1
		for j < len(word) && (word[j] == '_' || word[j] >= 'A' && word[j] <= 'Z' || word[j] >= 'a' && word[j] <= 'z' || j > i+1 && word[j] >= '0' && word[j] <= '9') {
			j++
		}
		if j == i+1 {
			out = append(out, word[i])
			continue
		}
		out = append(out, getenv(word[i+1:j])...)
		i = j - 1
	}
	return string(out)
}
