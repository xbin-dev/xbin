package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
)

// bx llm — the workspace's LLM connector (D226, docs/bx.md "LLM connector",
// docs/llm.md): its upstreams, default models, aliases, who may call, what
// was used and the quotas. Settings are a workspace admin's; status, models
// and your own usage are anyone's the connector admits. What the host's
// deployment file sets shows as "set by your host" and can't be changed
// here. Against an xbind without the connector every command says so.

func init() { moreCmds["llm"] = cmdLLM }

const llmUsage = `  bx llm [status] [--json]              the LLM connector: upstreams, default models, your quota
  bx llm upstream add <name> <base-url> [--key-file <f> | --key-stdin | --no-key]
                 [--models a,b] [--private] [--price <model>=<in>:<out>[:<cached-in>]]…
                 [--responses-via auto|upstream|chat] [--reasoning <model>=<preset>]…
                                        add a provider (the key goes to the vault; it is
                                        asked for when no --key… is given: Enter for none;
                                        never on the command line). --price '*=…' prices
                                        every model the upstream has no price for
  bx llm upstream set <name> [--url <u>] [--key…|--no-key] [--models a,b|--any-model]
                 [--private|--public] [--price …] [--responses-via auto|upstream|chat]
                 [--reasoning <model>=openai|toggle|deepseek|none|<levels>[/<default>]|-]…
                                        --reasoning describes a model's reasoning effort (or a
                                        family: 'deepseek*=deepseek'): a preset, or the levels it
                                        takes ('none,medium,high,xhigh/high' — none: it can be
                                        turned off; /high: its default); - forgets it
  bx llm upstream rm <name> | test <name> [<model>]
  bx llm models [--json]                the models offered (<upstream>/<model>, aliases),
                                        each with its reasoning effort when described
  bx llm default [set <model> [--role agent|harness|…] | unset [--role <role>]]
  bx llm alias [set <alias> <model> | rm <alias>]      "*" catches every unknown model name
  bx llm on | off | people admins|everyone|nobody | concurrency <n>
                                        concurrency: calls one caller may have in flight
  bx llm sessions [agents=on|off] [terminals=on|off] [openai-env=on|off]
                                        what coding agents and terminals get of it
  bx llm usage [--month] [--from <day>] [--to <day>] [--user <id>] [--component <tile>] [--json]
  bx llm quota [show [<user>]] | set <default|workspace|user:<id>|org:<id>>
                 [tokens/day=<n>] [tokens/month=<n>] [cost/day=<usd>] [cost/month=<usd>]
                 [action=stop|warn] | rm <default|workspace|user:<id>|org:<id>>
                                        n: a number, 2M, 500k, or unlimited
`

const noLLMMsg = "this xbind has no LLM connector (no /api/xbin/llm); upgrade xbind"

type llmKeyView struct {
	Set    bool   `json:"set"`
	Source string `json:"source"`
	Hint   string `json:"hint"`
}

type llmPrice struct {
	In       float64  `json:"in"`
	Out      float64  `json:"out"`
	CachedIn *float64 `json:"cachedIn,omitempty"`
}

type llmUpstream struct {
	Name    string              `json:"name"`
	BaseURL string              `json:"baseURL"`
	Key     llmKeyView          `json:"key"`
	Models  []string            `json:"models"`
	Private bool                `json:"private"`
	Prices  map[string]llmPrice `json:"prices"`
	Source  string              `json:"source"`
	Locked  bool                `json:"locked"`
	// how its Responses calls are served (D244); Translating: found to have no Responses API
	ResponsesVia     string `json:"responsesVia"`
	Translating      bool   `json:"translating"`
	TranslatingSince string `json:"translatingSince"`
	// its own description of its models' reasoning effort (D249), keyed like prices
	Reasoning map[string]llmEffort `json:"reasoning"`
}

// llmEffort is a model's reasoning-effort record (D249) as the connector
// answers it: on a model of GET /v1/models, and in an upstream's reasoning.
type llmEffort struct {
	Levels     *[]string `json:"supported_levels"`
	Default    string    `json:"default_level"`
	CanDisable *bool     `json:"can_disable"`
	Reasons    *bool     `json:"reasons"`
	Source     string    `json:"source"`
}

// effortLine is "Reasoning: Low · High · Max (default High; can be off) ·
// from the upstream" — or what is known instead.
func effortLine(e *llmEffort) string {
	if e == nil || (e.Levels == nil && (e.Reasons == nil || !*e.Reasons)) {
		return "Reasoning: not described — no control is offered"
	}
	from := map[string]string{"upstream": "from the upstream", "xbin": "from xbin", "admin": "from the admin", "host": "set by your host"}[e.Source]
	if from == "" {
		from = "from " + e.Source
	}
	if e.Levels == nil {
		return "Reasoning: yes, its levels not described — no control is offered (bx llm upstream set … --reasoning) · " + from
	}
	if len(*e.Levels) == 0 {
		return "Reasoning: none (an effort sent is dropped) · " + from
	}
	words := make([]string, len(*e.Levels))
	for i, l := range *e.Levels {
		words[i] = effortWord(l)
	}
	var notes []string
	if e.Default != "" {
		notes = append(notes, "default "+effortWord(e.Default))
	}
	if e.CanDisable != nil && *e.CanDisable {
		notes = append(notes, "can be off")
	} else if e.CanDisable != nil {
		notes = append(notes, "always thinks")
	}
	line := "Reasoning: " + strings.Join(words, " · ")
	if len(notes) > 0 {
		line += " (" + strings.Join(notes, "; ") + ")"
	}
	return line + " · " + from
}

// effortLadder is the connector's: OpenAI's words, cheapest first.
var effortLadder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// reasoningSpec is one --reasoning value as the API takes it: nil for "-"
// (forget the entry), {preset} for a preset's name, else a record of the
// levels listed ("none" among them: it can be turned off), with "/<level>"
// its default.
func reasoningSpec(v string) (any, error) {
	switch v {
	case "-":
		return nil, nil
	case "openai", "toggle", "deepseek", "none":
		return map[string]string{"preset": v}, nil
	}
	list, def, _ := strings.Cut(v, "/")
	rec := map[string]any{"can_disable": false}
	levels := []string{}
	for _, w := range strings.Split(list, ",") {
		w = strings.TrimSpace(w)
		switch {
		case w == "none":
			rec["can_disable"] = true
		case w != "" && slicesIndex(effortLadder, w) > 0:
			levels = append(levels, w)
		default:
			return nil, fmt.Errorf("%q is no preset (openai, toggle, deepseek, none) and no level (%s)", w, strings.Join(effortLadder, ", "))
		}
	}
	sort.Slice(levels, func(i, j int) bool {
		return slicesIndex(effortLadder, levels[i]) < slicesIndex(effortLadder, levels[j])
	})
	rec["supported_levels"] = levels
	if def != "" {
		rec["default_level"] = def
	}
	return rec, nil
}

func slicesIndex(l []string, w string) int {
	for i, x := range l {
		if x == w {
			return i
		}
	}
	return -1
}

// effortWord is a ladder word as the line says it.
func effortWord(w string) string {
	switch w {
	case "none":
		return "Off"
	case "xhigh":
		return "Extra high"
	case "":
		return ""
	}
	return strings.ToUpper(w[:1]) + w[1:]
}

type llmDefaults struct {
	Model string            `json:"model"`
	Roles map[string]string `json:"roles"`
}

type llmQuota struct {
	TokensPerDay   int64   `json:"tokensPerDay,omitempty"`
	TokensPerMonth int64   `json:"tokensPerMonth,omitempty"`
	CostPerDay     float64 `json:"costPerDay,omitempty"`
	CostPerMonth   float64 `json:"costPerMonth,omitempty"`
	Action         string  `json:"action,omitempty"`
}

type llmQuotaRules struct {
	Workspace *llmQuota           `json:"workspace"`
	Default   *llmQuota           `json:"default"`
	Users     map[string]llmQuota `json:"users"`
	Orgs      map[string]llmQuota `json:"orgs"`
}

type llmLock struct {
	Locked bool   `json:"locked"`
	Reason string `json:"reason"`
}

type llmConfig struct {
	Configured bool               `json:"configured"`
	Enabled    bool               `json:"enabled"`
	People     string             `json:"people"`
	Upstreams  []llmUpstream      `json:"upstreams"`
	Defaults   llmDefaults        `json:"defaults"`
	Aliases    map[string]string  `json:"aliases"`
	Quotas     llmQuotaRules      `json:"quotas"`
	Sessions   *llmSessions       `json:"sessions"` // nil: an xbind from before the session hook-up
	Locks      map[string]llmLock `json:"locks"`
	Host       struct {
		Active  bool   `json:"active"`
		LockAll bool   `json:"lockAll"`
		Error   string `json:"error"`
	} `json:"host"`
	Vault       string   `json:"vault"`
	Problems    []string `json:"problems"`
	StoreError  string   `json:"storeError"`
	Concurrency int      `json:"concurrency"`
	Notices     []string `json:"notices"`
}

type llmLimit struct {
	Name   string  `json:"name"`
	Value  float64 `json:"value"`
	Source string  `json:"source"`
	Action string  `json:"action"`
	Used   float64 `json:"used"`
	Resets string  `json:"resets"`
	State  string  `json:"state"`
}

type llmQuotaView struct {
	User      string     `json:"user"`
	Orgs      []string   `json:"orgs"`
	Limits    []llmLimit `json:"limits"`
	Workspace []llmLimit `json:"workspace"`
	State     string     `json:"state"`
}

type llmStatus struct {
	Configured bool          `json:"configured"`
	Enabled    bool          `json:"enabled"`
	Callable   bool          `json:"callable"`
	Reason     string        `json:"reason"`
	URL        string        `json:"url"`
	Ref        string        `json:"ref"`
	People     string        `json:"people"`
	Host       bool          `json:"host"`
	Defaults   llmDefaults   `json:"defaults"`
	User       string        `json:"user"`
	Component  string        `json:"component"`
	Admin      bool          `json:"admin"`
	Quota      *llmQuotaView `json:"quota"`
}

// llmCall is apiJSON for the connector's routes: it also reads an
// OpenAI-shaped error ({"error": {"message"}}), and says when this xbind
// has no connector. raw, when set, gets the answer's bytes.
func llmCall(method, path string, body, out any, raw *[]byte) error {
	resp, err := api(method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound && !strings.Contains(string(b), `"error"`) {
		return errors.New(noLLMMsg)
	}
	if resp.StatusCode >= 400 {
		var e struct {
			Error  json.RawMessage `json:"error"`
			Locked []string        `json:"locked"`
		}
		msg := strings.TrimSpace(string(b))
		if json.Unmarshal(b, &e) == nil && len(e.Error) > 0 {
			var obj struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(e.Error, &msg) != nil && json.Unmarshal(e.Error, &obj) == nil && obj.Message != "" {
				msg = obj.Message
			}
		}
		hint := ""
		if c := os.Getenv("XBIN_COMPONENT"); resp.StatusCode == http.StatusForbidden && c != "" {
			hint = fmt.Sprintf(" — this terminal is scoped to %s; the connector's settings need bx from the host or the admin console", c)
		}
		return fmt.Errorf("%s (%s)%s", msg, resp.Status, hint)
	}
	if raw != nil {
		*raw = b
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

func cmdLLM(args []string) error {
	usage := fmt.Errorf("usage:\n%s", llmUsage)
	jsonOut := false
	var pos []string
	for _, a := range args {
		if a == "--json" {
			jsonOut = true
		} else {
			pos = append(pos, a)
		}
	}
	if len(pos) == 0 {
		pos = []string{"status"}
	}
	switch pos[0] {
	case "status":
		return llmStatusCmd(jsonOut)
	case "upstream", "upstreams":
		return llmUpstreamCmd(pos[1:])
	case "models":
		return llmModelsCmd(jsonOut)
	case "default", "defaults":
		return llmDefaultCmd(pos[1:])
	case "alias", "aliases":
		return llmAliasCmd(pos[1:])
	case "on", "off":
		return llmPutConfig(map[string]any{"enabled": pos[0] == "on"})
	case "people":
		if len(pos) != 2 {
			return usage
		}
		return llmPutConfig(map[string]any{"people": pos[1]})
	case "sessions":
		return llmSessionsCmd(pos[1:], usage)
	case "concurrency":
		n, err := strconv.Atoi(strings.Join(pos[1:], " "))
		if err != nil {
			return usage
		}
		return llmPutConfig(map[string]any{"concurrency": n})
	case "usage":
		return llmUsageCmd(pos[1:], jsonOut)
	case "quota", "quotas":
		return llmQuotaCmd(pos[1:], jsonOut)
	}
	return usage
}

func llmPutConfig(body map[string]any) error {
	var v llmConfig
	if err := llmCall("PUT", "/api/xbin/llm/config", body, &v, nil); err != nil {
		return err
	}
	printLLMConfig(v)
	return nil
}

func llmStatusCmd(jsonOut bool) error {
	var st llmStatus
	var raw []byte
	if err := llmCall("GET", "/api/xbin/llm", nil, &st, &raw); err != nil {
		return err
	}
	var cfg *llmConfig
	if st.Admin {
		var c llmConfig
		var rawCfg []byte
		if err := llmCall("GET", "/api/xbin/llm/config", nil, &c, &rawCfg); err == nil {
			cfg = &c
			if jsonOut {
				fmt.Printf("{\"status\": %s, \"config\": %s}\n", strings.TrimSpace(string(raw)), strings.TrimSpace(string(rawCfg)))
				return nil
			}
		}
	}
	if jsonOut {
		fmt.Println(strings.TrimSpace(string(raw)))
		return nil
	}
	if cfg != nil {
		printLLMConfig(*cfg)
	} else {
		switch {
		case !st.Configured:
			fmt.Println("LLM connector: not configured")
		case !st.Enabled:
			fmt.Println("LLM connector: off")
		default:
			fmt.Printf("LLM connector: on — default model %s\n", orDash(st.Defaults.Model))
		}
	}
	if st.Callable {
		fmt.Printf("you (%s) may call it at %s\n", llmWho(st.User, st.Component), st.URL)
	} else if st.Reason != "" {
		fmt.Printf("you may not call it: %s\n", st.Reason)
	}
	if st.Quota != nil {
		printLLMLimits("  ", append(st.Quota.Limits, st.Quota.Workspace...))
	}
	return nil
}

func llmWho(user, component string) string {
	switch {
	case user != "" && component != "":
		return user + " through " + component
	case component != "":
		return component
	}
	return user
}

func printLLMConfig(v llmConfig) {
	state := "on"
	switch {
	case !v.Configured:
		state = "not configured (add an upstream: bx llm upstream add <name> <base-url>)"
	case !v.Enabled:
		state = "off"
	}
	lock := func(path string) string {
		if v.Locks[path].Locked || v.Locks["*"].Locked {
			return "  [set by your host]"
		}
		return ""
	}
	fmt.Printf("LLM connector: %s%s\n", state, lock("enabled"))
	if v.Host.Active {
		all := ""
		if v.Host.LockAll {
			all = " — every setting is your host's"
		}
		fmt.Printf("  a host configuration is in force%s\n", all)
	}
	if v.Host.Error != "" {
		fmt.Printf("  ! %s\n", v.Host.Error)
	}
	if v.StoreError != "" {
		fmt.Printf("  ! %s\n", v.StoreError)
	}
	fmt.Printf("  people who may call directly: %s%s\n", v.People, lock("people"))
	if v.Concurrency > 0 {
		fmt.Printf("  calls in flight per caller: %d%s\n", v.Concurrency, lock("concurrency"))
	}
	printLLMSessions(v.Sessions, lock)
	if v.Vault != "open" && v.Vault != "" {
		fmt.Printf("  vault: %s\n", v.Vault)
	}
	for _, u := range v.Upstreams {
		key := "no key"
		switch {
		case u.Key.Set && u.Key.Source == "host":
			key = "key: the host's"
		case u.Key.Set && u.Key.Hint != "":
			key = "key " + u.Key.Hint
		case u.Key.Set:
			key = "key in the vault"
		}
		models := "any model (its own list)"
		if len(u.Models) > 0 {
			models = strings.Join(u.Models, ", ")
		}
		private := ""
		if u.Private {
			private = ", private"
		}
		fmt.Printf("  upstream %s  %s  (%s%s)%s\n    models: %s\n", u.Name, u.BaseURL, key, private, lock("upstreams."+u.Name), models)
		switch {
		case u.ResponsesVia == "chat":
			fmt.Println("    Responses calls: translated to chat completions (responsesVia: chat)")
		case u.Translating:
			fmt.Printf("    Responses calls: translated to chat completions — the upstream has no Responses API (detected %s; bx llm upstream set %s --responses-via auto asks it again)\n", u.TranslatingSince, u.Name)
		case u.ResponsesVia == "upstream":
			fmt.Println("    Responses calls: the upstream's own, never translated")
		}
		for _, m := range llmKeys(u.Reasoning) {
			e := u.Reasoning[m]
			fmt.Printf("    %s: %s\n", m, effortLine(&e))
		}
		for _, m := range llmKeys(u.Prices) {
			p := u.Prices[m]
			cached := ""
			if p.CachedIn != nil {
				cached = fmt.Sprintf(", cached in $%g", *p.CachedIn)
			}
			fmt.Printf("    price %s: in $%g, out $%g%s per million tokens\n", m, p.In, p.Out, cached)
		}
	}
	if v.Defaults.Model != "" {
		fmt.Printf("  default model: %s%s\n", v.Defaults.Model, lock("defaults.model"))
	}
	for _, r := range llmKeys(v.Defaults.Roles) {
		fmt.Printf("  %s model: %s%s\n", r, v.Defaults.Roles[r], lock("defaults.roles."+r))
	}
	for _, a := range llmKeys(v.Aliases) {
		fmt.Printf("  alias %s → %s%s\n", a, v.Aliases[a], lock("aliases."+a))
	}
	for _, p := range v.Problems {
		fmt.Printf("  ! %s\n", p)
	}
	for _, n := range v.Notices {
		fmt.Printf("  note: %s\n", n)
	}
}

func llmKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// llmFlags parses flags that take a value (given in takes) and switches;
// what is left are positionals.
func llmFlags(args []string, takes map[string]bool, switches map[string]bool) (pos []string, vals map[string][]string, on map[string]bool, err error) {
	vals, on = map[string][]string{}, map[string]bool{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, inline := strings.Cut(a, "=")
		switch {
		case !isFlag(a):
			pos = append(pos, a)
		case takes[name]:
			if !inline {
				if val, err = nextArg(args, &i); err != nil {
					return nil, nil, nil, err
				}
			}
			vals[name] = append(vals[name], val)
		case switches[a]:
			on[a] = true
		default:
			return nil, nil, nil, fmt.Errorf("unknown flag %s\nusage:\n%s", name, llmUsage)
		}
	}
	return pos, vals, on, nil
}

func llmUpstreamCmd(args []string) error {
	usage := fmt.Errorf("usage:\n%s", llmUsage)
	if len(args) == 0 {
		return llmStatusCmd(false)
	}
	pos, vals, on, err := llmFlags(args[1:], map[string]bool{"--key": true, "--key-file": true, "--url": true, "--models": true, "--price": true, "--responses-via": true, "--reasoning": true},
		map[string]bool{"--key-stdin": true, "--no-key": true, "--private": true, "--public": true, "--any-model": true})
	if err != nil {
		return err
	}
	one := func(name string) string {
		if v := vals[name]; len(v) > 0 {
			return v[len(v)-1]
		}
		return ""
	}
	switch args[0] {
	case "add", "set":
		add := args[0] == "add"
		if (add && len(pos) != 2) || (!add && len(pos) != 1) {
			return usage
		}
		body := map[string]any{}
		if add {
			body["baseURL"] = pos[1]
		} else if u := one("--url"); u != "" {
			body["baseURL"] = u
		}
		if _, given := vals["--key"]; given {
			// a key on the command line is in the shell's history and in every process listing
			return errors.New("bx llm takes no key on the command line: pass --key-file <file> or --key-stdin, or run it without one and type the key when asked")
		}
		switch {
		case one("--key-file") != "":
			b, err := os.ReadFile(one("--key-file"))
			if err != nil {
				return err
			}
			body["key"] = strings.TrimSpace(string(b))
		case on["--no-key"]:
			body["key"] = ""
		case on["--key-stdin"] || add:
			prompt := ""
			if !on["--key-stdin"] {
				prompt = "API key for " + pos[0] + " (Enter for none): "
			}
			k, err := readPassphrase(prompt)
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			if k = strings.TrimSpace(k); k != "" {
				body["key"] = k
			}
		}
		if m := one("--models"); m != "" {
			body["models"] = splitList(m)
		} else if on["--any-model"] {
			body["models"] = []string{}
		}
		if on["--private"] || on["--public"] {
			body["private"] = on["--private"]
		}
		if v := one("--responses-via"); v != "" {
			body["responsesVia"] = v
		}
		if len(vals["--price"]) > 0 {
			prices := map[string]llmPrice{}
			if !add { // prices are replaced as a set: keep the ones not named
				var cur llmConfig
				if err := llmCall("GET", "/api/xbin/llm/config", nil, &cur, nil); err != nil {
					return err
				}
				for _, u := range cur.Upstreams {
					if u.Name == pos[0] {
						for m, p := range u.Prices {
							prices[m] = p
						}
					}
				}
			}
			for _, spec := range vals["--price"] {
				model, p, err := parseLLMPrice(spec)
				if err != nil {
					return err
				}
				prices[model] = p
			}
			body["prices"] = prices
		}
		if len(vals["--reasoning"]) > 0 { // by model: the rest of the upstream's table stays (the API merges)
			reasoning := map[string]any{}
			for _, spec := range vals["--reasoning"] {
				model, preset, ok := strings.Cut(spec, "=")
				if !ok || model == "" || preset == "" {
					return fmt.Errorf("--reasoning %q: want <model>=<preset> (openai, toggle, deepseek, none) or <model>=- to forget it", spec)
				}
				switch r, err := reasoningSpec(preset); {
				case err != nil:
					return fmt.Errorf("--reasoning %q: %v", spec, err)
				default:
					reasoning[model] = r
				}
			}
			body["reasoning"] = reasoning
		}
		var v llmConfig
		if err := llmCall("PUT", "/api/xbin/llm/upstreams/"+url.PathEscape(pos[0]), body, &v, nil); err != nil {
			return err
		}
		printLLMConfig(v)
		return nil
	case "rm", "remove":
		if len(pos) != 1 {
			return usage
		}
		var v llmConfig
		if err := llmCall("DELETE", "/api/xbin/llm/upstreams/"+url.PathEscape(pos[0]), nil, &v, nil); err != nil {
			return err
		}
		fmt.Printf("removed upstream %s\n", pos[0])
		return nil
	case "test":
		if len(pos) < 1 || len(pos) > 2 {
			return usage
		}
		body := map[string]string{}
		if len(pos) == 2 {
			body["model"] = pos[1]
		}
		var res struct {
			OK     bool   `json:"ok"`
			Model  string `json:"model"`
			Models int    `json:"models"`
			Status int    `json:"status"`
			Ms     int64  `json:"ms"`
			Tokens int64  `json:"tokens"`
			Error  string `json:"error"`
		}
		if err := llmCall("POST", "/api/xbin/llm/upstreams/"+url.PathEscape(pos[0])+"/test", body, &res, nil); err != nil {
			return err
		}
		switch {
		case !res.OK:
			return fmt.Errorf("upstream %s: the test failed after %d ms: %s", pos[0], res.Ms, res.Error)
		case res.Model == "":
			fmt.Printf("upstream %s: ok — it lists %d models (%d ms); name a model to test a call: bx llm upstream test %s <model>\n", pos[0], res.Models, res.Ms, pos[0])
		default:
			fmt.Printf("upstream %s: ok — %s answered in %d ms (%d tokens)\n", pos[0], res.Model, res.Ms, res.Tokens)
		}
		return nil
	}
	return usage
}

// parseLLMPrice reads <model>=<in>:<out>[:<cached-in>], USD per million tokens.
func parseLLMPrice(spec string) (string, llmPrice, error) {
	bad := fmt.Errorf("--price %q: want <model>=<in>:<out>[:<cached-in>] in USD per million tokens, e.g. gpt-5=1.25:10:0.125", spec)
	model, rest, ok := strings.Cut(spec, "=")
	parts := strings.Split(rest, ":")
	if !ok || model == "" || len(parts) < 2 || len(parts) > 3 {
		return "", llmPrice{}, bad
	}
	var nums []float64
	for _, p := range parts {
		f, err := strconv.ParseFloat(p, 64)
		if err != nil || f < 0 {
			return "", llmPrice{}, bad
		}
		nums = append(nums, f)
	}
	p := llmPrice{In: nums[0], Out: nums[1]}
	if len(nums) == 3 {
		p.CachedIn = &nums[2]
	}
	return model, p, nil
}

func llmModelsCmd(jsonOut bool) error {
	var out struct {
		Data []struct {
			ID      string          `json:"id"`
			OwnedBy string          `json:"owned_by"`
			AliasOf string          `json:"alias_of"`
			Effort  json.RawMessage `json:"effort"`
		} `json:"data"`
		Errors map[string]string `json:"upstream_errors"`
	}
	var raw []byte
	if err := llmCall("GET", "/api/xbin/llm/v1/models", nil, &out, &raw); err != nil {
		return err
	}
	if jsonOut {
		fmt.Println(strings.TrimSpace(string(raw)))
		return nil
	}
	for _, m := range out.Data {
		if m.AliasOf != "" {
			fmt.Printf("%s → %s\n", m.ID, m.AliasOf)
			continue
		}
		var e llmEffort
		if len(m.Effort) == 0 || json.Unmarshal(m.Effort, &e) != nil || (e.Levels == nil && (e.Reasons == nil || !*e.Reasons)) {
			fmt.Println(m.ID)
			continue
		}
		fmt.Printf("%s  %s\n", m.ID, effortLine(&e))
	}
	for _, name := range llmKeys(out.Errors) {
		fmt.Fprintf(os.Stderr, "bx: upstream %s: %s\n", name, out.Errors[name])
	}
	return nil
}

func llmDefaultCmd(args []string) error {
	usage := fmt.Errorf("usage:\n%s", llmUsage)
	if len(args) == 0 {
		return llmStatusCmd(false)
	}
	pos, vals, _, err := llmFlags(args[1:], map[string]bool{"--role": true}, nil)
	if err != nil {
		return err
	}
	role := ""
	if v := vals["--role"]; len(v) > 0 {
		role = v[len(v)-1]
	}
	model := ""
	switch {
	case args[0] == "set" && len(pos) == 1:
		model = pos[0]
	case args[0] == "unset" && len(pos) == 0:
	default:
		return usage
	}
	if role == "" || role == "default" {
		return llmPutConfig(map[string]any{"defaults": map[string]any{"model": model}})
	}
	return llmPutConfig(map[string]any{"defaults": map[string]any{"roles": map[string]string{role: model}}})
}

func llmAliasCmd(args []string) error {
	switch {
	case len(args) == 0:
		return llmStatusCmd(false)
	case args[0] == "set" && len(args) == 3:
		return llmPutConfig(map[string]any{"aliases": map[string]string{args[1]: args[2]}})
	case args[0] == "rm" && len(args) == 2:
		return llmPutConfig(map[string]any{"aliases": map[string]string{args[1]: ""}})
	}
	return fmt.Errorf("usage:\n%s", llmUsage)
}

type llmUsageRow struct {
	Day             string  `json:"day"`
	Month           string  `json:"month"`
	User            string  `json:"user"`
	Component       string  `json:"component"`
	Model           string  `json:"model"`
	Requests        int64   `json:"requests"`
	Failed          int64   `json:"failed"`
	InputTokens     int64   `json:"inputTokens"`
	OutputTokens    int64   `json:"outputTokens"`
	CachedTokens    int64   `json:"cachedTokens"`
	ReasoningTokens int64   `json:"reasoningTokens"`
	Cost            float64 `json:"cost"`
	Unpriced        int64   `json:"unpriced"`
}

func llmUsageCmd(args []string, jsonOut bool) error {
	pos, vals, on, err := llmFlags(args, map[string]bool{"--from": true, "--to": true, "--user": true, "--component": true, "--model": true}, map[string]bool{"--month": true})
	if err != nil || len(pos) != 0 {
		return fmt.Errorf("usage:\n%s", llmUsage)
	}
	q := url.Values{}
	if on["--month"] {
		q.Set("period", "month")
	}
	for flag, key := range map[string]string{"--from": "from", "--to": "to", "--user": "user", "--component": "component", "--model": "model"} {
		if v := vals[flag]; len(v) > 0 {
			q.Set(key, v[len(v)-1])
		}
	}
	var out struct {
		Period string        `json:"period"`
		From   string        `json:"from"`
		To     string        `json:"to"`
		Scope  string        `json:"scope"`
		Rows   []llmUsageRow `json:"rows"`
		Totals llmUsageRow   `json:"totals"`
	}
	var raw []byte
	path := "/api/xbin/llm/usage"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	if err := llmCall("GET", path, nil, &out, &raw); err != nil {
		return err
	}
	if jsonOut {
		fmt.Println(strings.TrimSpace(string(raw)))
		return nil
	}
	whose := "everyone's"
	if out.Scope == "own" {
		whose = "your"
	}
	fmt.Printf("%s LLM usage, %s to %s (UTC), per %s\n", whose, out.From, out.To, out.Period)
	if len(out.Rows) == 0 {
		fmt.Println("  nothing used")
		return nil
	}
	fmt.Printf("  %-10s  %-14s  %-18s  %-26s  %6s  %10s  %10s  %9s\n", out.Period, "user", "component", "model", "calls", "in", "out", "cost")
	line := func(when string, r llmUsageRow) {
		cost := fmt.Sprintf("$%.4f", r.Cost)
		if r.Unpriced > 0 {
			cost += "+" // some calls on a model without a price
		}
		fmt.Printf("  %-10s  %-14s  %-18s  %-26s  %6d  %10d  %10d  %9s\n", when, orDash(r.User), orDash(r.Component), r.Model, r.Requests, r.InputTokens, r.OutputTokens, cost)
	}
	for _, r := range out.Rows {
		when := r.Day
		if when == "" {
			when = r.Month
		}
		line(when, r)
	}
	line("total", llmUsageRow{Model: "", Requests: out.Totals.Requests, InputTokens: out.Totals.InputTokens, OutputTokens: out.Totals.OutputTokens, Cost: out.Totals.Cost, Unpriced: out.Totals.Unpriced})
	if out.Totals.Unpriced > 0 {
		fmt.Println("  +: some calls used a model without a price (bx llm upstream set <name> --price …)")
	}
	return nil
}

func printLLMLimits(indent string, limits []llmLimit) {
	for _, l := range limits {
		used, value := strconv.FormatFloat(l.Used, 'f', -1, 64), strconv.FormatFloat(l.Value, 'f', -1, 64)
		if strings.HasPrefix(l.Name, "cost") {
			used, value = fmt.Sprintf("$%.2f", l.Used), fmt.Sprintf("$%.2f", l.Value)
		}
		mark := ""
		if l.State != "ok" {
			mark = "  ← reached (" + l.State + ")"
		}
		fmt.Printf("%s%s (%s, %s): %s of %s, resets %s%s\n", indent, llmLimitLabel(l.Name), l.Source, l.Action, used, value, l.Resets, mark)
	}
}

func llmLimitLabel(name string) string {
	switch name {
	case "tokensPerDay":
		return "tokens/day"
	case "tokensPerMonth":
		return "tokens/month"
	case "costPerDay":
		return "cost/day"
	}
	return "cost/month"
}

func (q llmQuota) String() string {
	var parts []string
	num := func(label string, v float64, cost bool) {
		switch {
		case v == -1:
			parts = append(parts, label+"=unlimited")
		case v != 0 && cost:
			parts = append(parts, fmt.Sprintf("%s=$%g", label, v))
		case v != 0:
			parts = append(parts, fmt.Sprintf("%s=%d", label, int64(v)))
		}
	}
	num("tokens/day", float64(q.TokensPerDay), false)
	num("tokens/month", float64(q.TokensPerMonth), false)
	num("cost/day", q.CostPerDay, true)
	num("cost/month", q.CostPerMonth, true)
	if q.Action != "" {
		parts = append(parts, "action="+q.Action)
	}
	if len(parts) == 0 {
		return "(nothing set)"
	}
	return strings.Join(parts, " ")
}

// parseLLMAmount reads a quota number: 2000000, 2M, 500k, 1.5m, unlimited.
func parseLLMAmount(s string, cost bool) (float64, error) {
	t := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "$"))
	if t == "unlimited" || t == "none" {
		return -1, nil
	}
	mult := 1.0
	if !cost {
		switch {
		case strings.HasSuffix(t, "k"):
			mult, t = 1e3, strings.TrimSuffix(t, "k")
		case strings.HasSuffix(t, "m"):
			mult, t = 1e6, strings.TrimSuffix(t, "m")
		case strings.HasSuffix(t, "b"):
			mult, t = 1e9, strings.TrimSuffix(t, "b")
		}
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("%q: a positive number (tokens: 2000000, 2M, 500k; cost: USD), or unlimited", s)
	}
	return f * mult, nil
}

func llmQuotaCmd(args []string, jsonOut bool) error {
	usage := fmt.Errorf("usage:\n%s", llmUsage)
	if len(args) == 0 {
		args = []string{"show"}
	}
	type rulesView struct {
		Quotas    llmQuotaRules      `json:"quotas"`
		Locks     map[string]llmLock `json:"locks"`
		Effective *llmQuotaView      `json:"effective"`
	}
	show := func(v rulesView) {
		lock := func(part string) string {
			if v.Locks["quotas."+part].Locked {
				return "  [set by your host]"
			}
			return ""
		}
		row := func(label string, q *llmQuota, part string) {
			if q != nil {
				fmt.Printf("  %-22s %s%s\n", label, q, lock(part))
			} else {
				fmt.Printf("  %-22s (none)%s\n", label, lock(part))
			}
		}
		fmt.Println("LLM quotas (windows are UTC days and months)")
		row("workspace (everyone)", v.Quotas.Workspace, "workspace")
		row("default (each person)", v.Quotas.Default, "default")
		for _, id := range llmKeys(v.Quotas.Orgs) {
			q := v.Quotas.Orgs[id]
			row("org:"+id, &q, "orgs")
		}
		for _, id := range llmKeys(v.Quotas.Users) {
			q := v.Quotas.Users[id]
			row("user:"+id, &q, "users")
		}
		if e := v.Effective; e != nil {
			fmt.Printf("%s's limits now:\n", e.User)
			if len(e.Limits)+len(e.Workspace) == 0 {
				fmt.Println("  none")
			}
			printLLMLimits("  ", append(e.Limits, e.Workspace...))
		}
	}
	target := func(t string) (part, id string, err error) {
		switch kind, rest, _ := strings.Cut(t, ":"); {
		case t == "default" || t == "workspace":
			return t, "", nil
		case kind == "user" && rest != "":
			return "users", rest, nil
		case kind == "org" && rest != "":
			return "orgs", rest, nil
		}
		return "", "", fmt.Errorf("%q: default, workspace, user:<id> or org:<id>", t)
	}
	put := func(part, id string, q any) error {
		body := map[string]any{part: q}
		if id != "" {
			body = map[string]any{part: map[string]any{id: q}}
		}
		var v rulesView
		if err := llmCall("PUT", "/api/xbin/llm/quotas", body, &v, nil); err != nil {
			return err
		}
		show(v)
		return nil
	}
	switch args[0] {
	case "show":
		if len(args) > 2 {
			return usage
		}
		path := "/api/xbin/llm/quotas"
		if len(args) == 2 {
			path += "?user=" + url.QueryEscape(args[1])
		}
		var v rulesView
		var raw []byte
		if err := llmCall("GET", path, nil, &v, &raw); err != nil {
			return err
		}
		if jsonOut {
			fmt.Println(strings.TrimSpace(string(raw)))
			return nil
		}
		show(v)
		return nil
	case "set":
		if len(args) < 3 {
			return usage
		}
		part, id, err := target(args[1])
		if err != nil {
			return err
		}
		var q llmQuota
		for _, kv := range args[2:] {
			k, val, ok := strings.Cut(kv, "=")
			if !ok {
				return usage
			}
			switch k {
			case "action":
				q.Action = val
				continue
			case "tokens/day", "tokens/month", "cost/day", "cost/month":
			default:
				return fmt.Errorf("%q: tokens/day, tokens/month, cost/day, cost/month or action", k)
			}
			f, err := parseLLMAmount(val, strings.HasPrefix(k, "cost"))
			if err != nil {
				return err
			}
			switch k {
			case "tokens/day":
				q.TokensPerDay = int64(f)
			case "tokens/month":
				q.TokensPerMonth = int64(f)
			case "cost/day":
				q.CostPerDay = f
			case "cost/month":
				q.CostPerMonth = f
			}
		}
		return put(part, id, q)
	case "rm":
		if len(args) != 2 {
			return usage
		}
		part, id, err := target(args[1])
		if err != nil {
			return err
		}
		return put(part, id, nil)
	}
	return usage
}
