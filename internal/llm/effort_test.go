package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// effort_test.go — D249: the reasoning-effort record and its mapping, pure.

// eu is an upstream named name at base.
func eu(name, base string) effUpstream {
	u := effUpstream{Name: name}
	u.BaseURL = base
	return u
}

func rec(levels []string, def string, canDisable bool, aliases map[string]string) *EffortRecord {
	return &EffortRecord{Levels: levels, Default: def, CanDisable: boolp(canDisable), Aliases: aliases, Source: EffortFromXbin}
}

// covers D249 — every kind of record against every ladder word and a word
// off it: exact, the provider's alias, the highest at or below, the lowest;
// none only where it can be; an undescribed model and a non-ladder word
// pass untouched and unsaid; a model without reasoning drops it.
func TestEffortMapping(t *testing.T) {
	deepseek := rec([]string{"low", "high", "max"}, "high", true, map[string]string{"minimal": "low", "medium": "high", "xhigh": "high"})
	alwaysOn := rec([]string{"low", "high", "max"}, "max", false, nil) // Kimi K3, GLM-5.3
	openai := rec([]string{"low", "medium", "high"}, "medium", false, nil)
	toggle := rec([]string{"high"}, "", true, nil)
	// a router's glm-5.2 as an admin described it (2026-11-20: it takes none, medium, high, xhigh and 400s on
	// minimal, low, max) — this gateway's behaviour, so an admin's entry, never the vendored table's
	glm52 := &EffortRecord{Levels: []string{"medium", "high", "xhigh"}, CanDisable: boolp(true), Source: EffortFromAdmin}
	off := &EffortRecord{Levels: []string{}}
	type want struct{ sent, header string }
	pass := func(w string) want { return want{w, ""} }
	cases := []struct {
		name string
		rec  *EffortRecord
		in   map[string]want
	}{
		{"undescribed", nil, map[string]want{"none": pass("none"), "low": pass("low"), "medium": pass("medium"), "max": pass("max"), "ultra": pass("ultra")}},
		{"levels unsaid", &EffortRecord{Default: "high"}, map[string]want{"medium": pass("medium")}},
		// m3: whether it can be turned off isn't known: none goes as it came (the rest maps)
		{"can_disable unknown", &EffortRecord{Levels: []string{"low", "high"}}, map[string]want{"none": {"none", "none"}, "medium": {"low", "low; asked=medium"}}},
		{"a budget-only model (not yet)", &EffortRecord{Levels: []string{}, Budget: json.RawMessage(`{"min":1024}`)}, map[string]want{"high": pass("high")}},
		{"a send patch (not yet)", &EffortRecord{Levels: []string{"low"}, Send: json.RawMessage(`{"chat":{}}`)}, map[string]want{"high": pass("high")}},
		{"DeepSeek", deepseek, map[string]want{
			"none": {"none", "none"}, "minimal": {"low", "low; asked=minimal"}, "low": {"low", "low"}, "medium": {"high", "high; asked=medium"},
			"high": {"high", "high"}, "xhigh": {"high", "high; asked=xhigh"}, "max": {"max", "max"}, "ultra": pass("ultra"), "": pass("")}},
		{"can't be turned off", alwaysOn, map[string]want{
			"none": {"low", "low; asked=none"}, "minimal": {"low", "low; asked=minimal"}, "medium": {"low", "low; asked=medium"},
			"xhigh": {"high", "high; asked=xhigh"}, "max": {"max", "max"}}},
		{"OpenAI's three", openai, map[string]want{"minimal": {"low", "low; asked=minimal"}, "xhigh": {"high", "high; asked=xhigh"}, "max": {"high", "high; asked=max"},
			"none": {"low", "low; asked=none"}}},
		{"on or off", toggle, map[string]want{"none": {"none", "none"}, "low": {"high", "high; asked=low"}, "max": {"high", "high; asked=max"}, "high": {"high", "high"}}},
		{"a router's glm-5.2", glm52, map[string]want{"none": {"none", "none"}, "minimal": {"medium", "medium; asked=minimal"}, "low": {"medium", "medium; asked=low"},
			"medium": {"medium", "medium"}, "xhigh": {"xhigh", "xhigh"}, "max": {"xhigh", "xhigh; asked=max"}}},
	}
	for _, c := range cases {
		for asked, w := range c.in {
			m := mapEffort(c.rec, asked)
			if m.drop || m.sent != w.sent || m.header != w.header {
				t.Errorf("%s, %q: sent %q header %q drop %v — want %q, %q", c.name, asked, m.sent, m.header, m.drop, w.sent, w.header)
			}
		}
	}
	for asked, h := range map[string]string{"high": "none; asked=high; can't", "none": "none; can't"} {
		if m := mapEffort(off, asked); !m.drop || m.header != h {
			t.Errorf("no reasoning, %q: %+v — want dropped, %q", asked, m, h)
		}
	}
	if m := mapEffort(off, "ultra"); m.drop || m.sent != "ultra" {
		t.Errorf("a word off the ladder passes even to a model without reasoning: %+v", m)
	}
}

// covers D249 (m1) — the mapping rule's shared vectors: the connector's
// mapEffort sends what the agent template's chip shows (model/effort.js
// mapLevel reads the same file: hack/agent-template-effort.test.mjs).
func TestEffortMappingVectors(t *testing.T) {
	b, err := os.ReadFile("testdata/effort_mapping.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Records []struct {
			Name   string            `json:"name"`
			Record json.RawMessage   `json:"record"`
			Map    map[string]string `json:"map"`
		} `json:"records"`
	}
	if err := json.Unmarshal(b, &file); err != nil || len(file.Records) < 5 {
		t.Fatalf("%v (%d records)", err, len(file.Records))
	}
	for _, c := range file.Records {
		var rec *EffortRecord
		if string(c.Record) != "null" {
			rec = &EffortRecord{}
			if err := json.Unmarshal(c.Record, rec); err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
		}
		for asked, want := range c.Map {
			if m := mapEffort(rec, asked); m.drop || m.sent != want {
				t.Errorf("%s, %q: sent %q (drop %v), want %q", c.Name, asked, m.sent, m.drop, want)
			}
		}
	}
}

// covers D249 — the rewrite on each wire: in place, only the effort, and
// nothing at all for a model nothing describes.
func TestEffortRewriteWires(t *testing.T) {
	deepseek := rec([]string{"low", "high", "max"}, "high", true, map[string]string{"medium": "high"})
	off := &EffortRecord{Levels: []string{}}
	run := func(r *EffortRecord, chat bool, body string) (map[string]json.RawMessage, []string, string, bool) {
		m := map[string]json.RawMessage{}
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatal(err)
		}
		touched := false
		dropped, h := effortRewrite(r, chat, func(k string) json.RawMessage { return m[k] }, func(k string, v json.RawMessage) {
			touched = true
			if v == nil {
				delete(m, k)
			} else {
				m[k] = v
			}
		})
		return m, dropped, h, touched
	}
	for _, c := range []struct {
		name    string
		r       *EffortRecord
		chat    bool
		body    string
		want    map[string]string // member → its JSON after ("" absent)
		dropped string
		header  string
	}{
		{"chat, mapped", deepseek, true, `{"reasoning_effort":"medium","model":"m"}`, map[string]string{"reasoning_effort": `"high"`, "model": `"m"`}, "", "high; asked=medium"},
		{"chat, as it is", deepseek, true, `{"reasoning_effort":"max"}`, map[string]string{"reasoning_effort": `"max"`}, "", "max"},
		{"chat, a template's own knob beside it untouched", deepseek, true, `{"reasoning_effort":"medium","chat_template_kwargs":{"thinking":true,"reasoning_effort":62}}`,
			map[string]string{"reasoning_effort": `"high"`, "chat_template_kwargs": `{"reasoning_effort":62,"thinking":true}`}, "", "high; asked=medium"},
		{"chat, an OpenRouter-style object beside it", deepseek, true, `{"reasoning":{"effort":"medium","exclude":true}}`, map[string]string{"reasoning": `{"effort":"high","exclude":true}`}, "", "high; asked=medium"},
		{"chat, no reasoning", off, true, `{"reasoning_effort":"high"}`, map[string]string{"reasoning_effort": ""}, "reasoning_effort", "none; asked=high; can't"},
		{"responses, mapped, summary kept", deepseek, false, `{"reasoning":{"effort":"medium","summary":"auto"}}`, map[string]string{"reasoning": `{"effort":"high","summary":"auto"}`}, "", "high; asked=medium"},
		{"responses, no reasoning, only effort", off, false, `{"reasoning":{"effort":"low"}}`, map[string]string{"reasoning": ""}, "reasoning.effort", "none; asked=low; can't"},
		{"responses: chat's member is not its own", deepseek, false, `{"reasoning_effort":"medium"}`, map[string]string{"reasoning_effort": `"medium"`}, "", ""},
		{"a word off the ladder", deepseek, true, `{"reasoning_effort":"ultra"}`, map[string]string{"reasoning_effort": `"ultra"`}, "", ""},
		{"not a string", deepseek, true, `{"reasoning_effort":7}`, map[string]string{"reasoning_effort": `7`}, "", ""},
	} {
		m, dropped, h, _ := run(c.r, c.chat, c.body)
		for k, w := range c.want {
			got := ""
			if v, ok := m[k]; ok {
				var x any
				_ = json.Unmarshal(v, &x)
				b, _ := json.Marshal(x)
				got = string(b)
			}
			if got != w {
				t.Errorf("%s: %s = %s, want %s", c.name, k, got, w)
			}
		}
		if strings.Join(dropped, ",") != c.dropped || h != c.header {
			t.Errorf("%s: dropped %v header %q — want %q, %q", c.name, dropped, h, c.dropped, c.header)
		}
	}
	// a model nothing describes: the request is not touched at all
	for _, body := range []string{`{"reasoning_effort":"medium"}`, `{"reasoning":{"effort":"none","summary":"x"}}`, `{"thinking":{"type":"enabled"}}`} {
		for _, chat := range []bool{true, false} {
			if _, d, h, touched := run(nil, chat, body); touched || d != nil || h != "" {
				t.Errorf("undescribed %s: touched %v dropped %v header %q", body, touched, d, h)
			}
		}
	}
}

// covers D249 — the vendored table: every family valid and cited, keys
// resolved as prices are (exact, after the last "/", the longest prefix*),
// and the owner's own model id described.
func TestEffortVendoredTable(t *testing.T) {
	b, err := effortTableFS.ReadFile("effort_table.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseEffortTable(b); err != nil {
		t.Fatal(err)
	}
	for model, want := range map[string]string{
		"deepseek/deepseek-v4.1-flash": "low,high,max", "deepseek-flash": "low,high,max", "deepseek-v4-pro": "low,high,max",
		"kimi-k3": "low,high,max", "GLM-5.3": "low,high,max", "MiniMax-M3.1-Flash": "low,medium,high,xhigh,max",
		"x-ai/grok-4.5": "low,medium,high", "mistral-small-2603": "high", "qwen4-max": "",
		"gpt-5": "", "grok-4.7-fast-non-reasoning": "", "deepseek-r1": "", "llama-4": "",
	} {
		r, ok := lookupModel(vendoredEffort, model)
		got := ""
		if ok {
			got = strings.Join(r.rec.Levels, ",")
			if r.rec.Source != EffortFromXbin || len(r.hosts) == 0 {
				t.Errorf("%s: source %q, hosts %v", model, r.rec.Source, r.hosts)
			}
		}
		if got != want {
			t.Errorf("%s: levels %q, want %q", model, got, want)
		}
	}
	// the lookup's order
	table := map[string]int{"a/b": 1, "b": 2, "b*": 3, "bc*": 4}
	for model, want := range map[string]int{"a/b": 1, "x/b": 2, "bz": 3, "bcd": 4, "x/bcd": 4, "zz": 0} {
		if got, _ := lookupModel(table, model); got != want {
			t.Errorf("lookup %s: %d, want %d", model, got, want)
		}
	}
	// a broken table is refused (it is xbind's own: a release that ships one fails its tests)
	for _, bad := range []string{
		`{"families":[{"family":"x","models":["m"],"hosts":["h"],"effort":{"supported_levels":["low"]}}]}`,                               // not cited
		`{"families":[{"family":"x","models":["m"],"hosts":["h"],"read":"u","effort":{"supported_levels":["turbo"]}}]}`,                  // off the ladder
		`{"families":[{"family":"x","models":["m"],"hosts":["h"],"read":"u","effort":{"default_level":"high"}}]}`,                        // no levels
		`{"families":[{"family":"x","models":["m"],"hosts":["h"],"read":"u","effort":{"supported_levels":["low"],"send":{"chat":{}}}}]}`, // a patch
		`{"families":[{"family":"x","models":["m"],"hosts":["h"],"read":"u","effort":{"supported_levels":["low"]}},{"family":"y","models":["m"],"hosts":["h"],"read":"u","effort":{"supported_levels":["low"]}}]}`,
		`{"families":[{"family":"x","models":["m"],"read":"u","effort":{"supported_levels":["low"]}}]}`,                              // no hosts: it would apply everywhere
		`{"families":[{"family":"x","models":["m"],"hosts":["https://api.x.ai"],"read":"u","effort":{"supported_levels":["low"]}}]}`, // a URL, not a host
		`{"families":[{"family":"x","models":["m"],"hosts":["*.x.ai"],"read":"u","effort":{"supported_levels":["low"]}}]}`,           // a wildcard
		`{"families":[{"family":"x","models":["m"],"hosts":["API.X.AI"],"read":"u","effort":{"supported_levels":["low"]}}]}`,         // not lowercase
	} {
		if _, err := parseEffortTable([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

// covers D249 (security) — an upstream's model list is untrusted: only
// ladder words, booleans and aliases onto its own levels are read; a list
// too long, a word off the ladder, a wrong type, and anything like a send
// patch, a scale or a source are ignored.
func TestEffortFromAnUntrustedListing(t *testing.T) {
	read := func(obj string) (*EffortRecord, bool) {
		var extra map[string]json.RawMessage
		if err := json.Unmarshal([]byte(obj), &extra); err != nil {
			t.Fatal(err)
		}
		r, ok := effortFromListing(extra)
		return &r, ok
	}
	r, ok := read(`{"effort":{"supported_levels":["max","low","turbo","high","low",7,null],"default_level":"high","can_disable":"yes",
		"aliases":{"medium":"high","xhigh":"ultra","bogus":"low","minimal":7},"send":{"chat":{"model":"evil"}},"scale":{"min":1},"source":"admin","replay":"never"}}`)
	if !ok || strings.Join(r.Levels, ",") != "low,high,max" || r.Default != "high" || r.CanDisable != nil || len(r.Send) > 0 || len(r.Scale) > 0 ||
		r.Source != EffortFromUpstream || r.Replay != ReplayNever || fmt.Sprint(r.Aliases) != "map[medium:high]" {
		t.Errorf("DeepSeek-shaped, with junk: %+v (ok %v)", r, ok)
	}
	if r.listing() == nil || strings.Contains(string(r.listing()), "send") || strings.Contains(string(r.listing()), "evil") {
		t.Errorf("the listing carries a send patch: %s", r.listing())
	}
	long := `{"effort":{"supported_levels":[` + strings.Repeat(`"low",`, 40) + `"high"]}}`
	for name, obj := range map[string]string{
		"a huge array":                    long,
		"no ladder word":                  `{"effort":{"supported_levels":["turbo","fast"]}}`,
		"levels of a wrong type":          `{"effort":{"supported_levels":"low,high"}}`,
		"an effort that is text":          `{"effort":"high"}`,
		"an OpenRouter list of nothing":   `{"reasoning":{"supported_efforts":[]}}`,
		"supported_parameters not a list": `{"supported_parameters":"tools"}`,
	} {
		if r, ok := read(obj); ok && r.kind() != "" {
			t.Errorf("%s: read as %+v", name, r)
		}
	}
	// OpenRouter's reasoning object; none among the efforts, or not mandatory: it can be turned off
	r, ok = read(`{"reasoning":{"supported_efforts":["max","high","low","none"],"default_effort":"high","default_enabled":true,"mandatory":false}}`)
	if !ok || strings.Join(r.Levels, ",") != "low,high,max" || !r.canDisable() || r.Default != "high" || r.ThinksByDefault == nil || !*r.ThinksByDefault {
		t.Errorf("OpenRouter: %+v", r)
	}
	r, _ = read(`{"reasoning":{"supported_efforts":["high","low"],"mandatory":true}}`)
	if r.canDisable() {
		t.Errorf("mandatory reasoning can be turned off: %+v", r)
	}
	// a list's silence says nothing: a router left reasoning_effort out of a model that takes it (2026-11-20)
	if r, ok := read(`{"supported_parameters":["tools","temperature"],"supported_sampling_parameters":["temperature","top_p"]}`); ok {
		t.Errorf("no reasoning parameter listed, read as %+v", r)
	}
	// a router's listing as it came (OpenRouter-shaped, no effort member): it reasons, its levels unknown — a
	// record that is no use alone, and fills in nothing over the table's levels
	gw := `{"id":"deepseek/deepseek-v4.1-flash","context_length":1048576,"max_output_tokens":393216,"aliases":["deepseek/deepseek-v4-flash"],
		"supported_features":["reasoning","json_mode","structured_outputs","logprobs","tools"],"supported_sampling_parameters":["temperature","top_p","max_tokens"]}`
	if r, ok := read(gw); !ok || r.Reasons == nil || !*r.Reasons || r.Levels != nil || r.kind() != "" {
		t.Errorf("a router's reasoning feature: %+v", r)
	}
	var extra map[string]json.RawMessage
	_ = json.Unmarshal([]byte(gw), &extra)
	// on a router the table's levels are a suggestion only (H1): it reasons, its levels unknown — no mapping
	gwu := eu("gw", "https://gateway.example/v1")
	if got, sg := effortInfo(gwu, "deepseek/deepseek-v4.1-flash", extra); got == nil || got.kind() != "" || got.Reasons == nil || sg == nil || strings.Join(sg.Levels, ",") != "low,high,max" {
		t.Errorf("a router's reasoning feature over the table: %+v, suggestion %+v", got, sg)
	}
	// on DeepSeek's own API, the table's levels under its list's word
	if got := effortOf(eu("ds", "https://api.deepseek.com"), "deepseek-v4.1-flash", extra); got == nil || strings.Join(got.Levels, ",") != "low,high,max" ||
		got.Source != EffortFromXbin || got.Reasons == nil {
		t.Errorf("the table's levels on the maker's API: %+v", got)
	}
	if got := effortOf(gwu, "some/other-model", extra); got == nil || got.kind() != "" {
		t.Errorf("a reasoning model nothing else describes: %+v", got)
	}
	// m2: a list never says "no reasoning" — an explicit [] or a none-only list is unknown
	for _, obj := range []string{`{"effort":{"supported_levels":[]}}`, `{"effort":{"supported_levels":["none"]}}`, `{"reasoning":{"supported_efforts":["none"]}}`,
		`{"reasoning":{"supported_efforts":[]}}`, `{"effort":{"supported_levels":[],"reasons":false}}`} {
		if r, ok := read(obj); ok && (r.kind() != "" || (r.Reasons != nil && !*r.Reasons && r.kind() == "off")) {
			t.Errorf("%s: read as %+v (kind %q)", obj, r, r.kind())
		}
		var ex map[string]json.RawMessage
		_ = json.Unmarshal([]byte(obj), &ex)
		if m := mapEffort(effortOf(gwu, "m", ex), "high"); m.drop || m.sent != "high" || m.header != "" {
			t.Errorf("%s: a caller's effort was touched on a list's word: %+v", obj, m)
		}
	}
}

// covers D249 (H1) — xbind's table applies only on its maker's own API, the
// host compared exactly: on a router, a local server or a company's gateway
// the same model id may take other levels — there it is a suggestion, the
// model undescribed, its requests relayed as they came. A list's explicit
// levels apply on any upstream.
func TestEffortTableOnlyOnTheMakersHost(t *testing.T) {
	makers := map[string]string{"kimi-k3": "https://api.moonshot.ai/v1", "glm-5.2": "https://api.z.ai/api/paas/v4",
		"my-org/deepseek-v4-finetune": "https://api.deepseek.com", "deepseek-v4.1-flash": "https://API.DeepSeek.com:443/v1"}
	others := []string{"https://gateway.example/v1", "http://localhost:8000/v1", "https://api.deepseek.com.evil.example/v1",
		"https://evil-api.deepseek.com/v1", "https://openrouter.ai/api/v1", "", "::bad"}
	for model, maker := range makers {
		for _, base := range others {
			u := eu("o", base)
			rec, sg := effortInfo(u, model, nil)
			if rec != nil || sg == nil {
				t.Errorf("%s on %q: described %+v (suggestion %v)", model, base, rec, sg != nil)
			}
			for _, chat := range []bool{true, false} {
				m := map[string]json.RawMessage{"reasoning_effort": json.RawMessage(`"medium"`), "reasoning": json.RawMessage(`{"effort":"none"}`)}
				d, h := effortRewrite(rec, chat, func(k string) json.RawMessage { return m[k] }, func(k string, v json.RawMessage) { m[k] = v })
				if d != nil || h != "" || string(m["reasoning_effort"]) != `"medium"` || string(m["reasoning"]) != `{"effort":"none"}` {
					t.Errorf("%s on %q: rewritten (%v %q) %v", model, base, d, h, m)
				}
			}
		}
		rec, sg := effortInfo(eu("m", maker), model, nil)
		if rec == nil || rec.kind() != "levels" || rec.Source != EffortFromXbin || sg != nil {
			t.Errorf("%s on its maker's %s: %+v", model, maker, rec)
		}
		if m := mapEffort(rec, "medium"); m.header == "" {
			t.Errorf("%s on its maker's: medium not mapped: %+v", model, m)
		}
	}
	// a list's explicit levels apply anywhere: OpenRouter's reasoning object, DeepSeek's effort member
	gw := eu("gw", "https://gateway.example/v1")
	for _, obj := range []string{`{"reasoning":{"supported_efforts":["low","high"]}}`, `{"effort":{"supported_levels":["low","high"]}}`} {
		var ex map[string]json.RawMessage
		_ = json.Unmarshal([]byte(obj), &ex)
		rec, sg := effortInfo(gw, "kimi-k3", ex)
		if rec == nil || strings.Join(rec.Levels, ",") != "low,high" || rec.Source != EffortFromUpstream || sg != nil {
			t.Errorf("%s on a router: %+v (suggestion %v)", obj, rec, sg != nil)
		}
		if rec != nil && rec.Aliases != nil {
			t.Errorf("%s: the table's aliases came along off the maker's API: %v", obj, rec.Aliases)
		}
	}
	// an admin's entry applies anywhere, and is the whole truth
	gw.Reasoning = map[string]EffortRecord{"glm-5.2": {Levels: []string{"medium", "high", "xhigh"}, CanDisable: boolp(true)}}
	if rec, sg := effortInfo(gw, "glm-5.2", nil); rec == nil || rec.Source != EffortFromAdmin || sg != nil || mapEffort(rec, "low").sent != "medium" {
		t.Errorf("an admin's glm-5.2 on a router: %+v", rec)
	}
}

// covers D249 — precedence: an admin's (or the host's) entry is the whole
// truth; else the upstream's own words over xbind's table, member by member.
func TestEffortPrecedence(t *testing.T) {
	listed := map[string]json.RawMessage{"effort": json.RawMessage(`{"supported_levels":["low","high","max"],"default_level":"max"}`)}
	u := eu("house", "https://api.deepseek.com/v1")
	got := effortOf(u, "deepseek/deepseek-v4.1-flash", listed)
	if got == nil || got.Source != EffortFromUpstream || got.Default != "max" || !got.canDisable() || got.Aliases["medium"] != "high" {
		t.Errorf("the upstream's over the table: %+v", got)
	}
	// the upstream's levels leave out the table's alias target: that alias goes
	got = effortOf(u, "deepseek-v4-pro", map[string]json.RawMessage{"effort": json.RawMessage(`{"supported_levels":["low","max"]}`)})
	if got == nil || got.Aliases["medium"] != "" || got.Default != "" {
		t.Errorf("an alias onto a level the upstream doesn't list: %+v", got)
	}
	if got := effortOf(u, "deepseek-v4-pro", nil); got == nil || got.Source != EffortFromXbin {
		t.Errorf("the table alone: %+v", got)
	}
	if got := effortOf(u, "some-local-model", nil); got != nil {
		t.Errorf("an unknown model has a record: %+v", got)
	}
	// m4: an entry that says only "replay": "never" turns replay off and changes nothing else
	u.Reasoning = map[string]EffortRecord{"deepseek-v4-pro": {Replay: ReplayNever}, "local-thinker": {Replay: ReplayNever}}
	if got := effortOf(u, "deepseek-v4-pro", nil); got == nil || got.Source != EffortFromXbin || got.replays() || strings.Join(got.Levels, ",") != "low,high,max" {
		t.Errorf("replay never over the table: %+v", got)
	}
	if got := effortOf(u, "local-thinker", nil); got == nil || got.replays() || got.kind() != "" || mapEffort(got, "medium").header != "" {
		t.Errorf("replay never on an undescribed model switched effort mapping on: %+v", got)
	}
	u.Reasoning = map[string]EffortRecord{"deepseek*": {Levels: []string{}}}
	if got := effortOf(u, "deepseek/deepseek-v4.1-flash", listed); got == nil || got.kind() != "off" || got.Source != EffortFromAdmin {
		t.Errorf("the admin's entry wins whole: %+v", got)
	}
	u.Host = true
	if got := effortOf(u, "deepseek-flash", nil); got == nil || got.Source != EffortFromHost {
		t.Errorf("the host's entry: %+v", got)
	}
}

// covers D249 — what an admin or a host may write: presets, or records with
// ladder words in their places; no send patch from the admin's API.
func TestEffortAdminRecords(t *testing.T) {
	for _, p := range EffortPresetNames {
		var r EffortRecord
		if err := json.Unmarshal([]byte(`{"preset":"`+p+`"}`), &r); err != nil || validEntry(p, r, true) != nil {
			t.Errorf("preset %s: %v %v", p, err, validEntry(p, r, true))
		}
	}
	var r EffortRecord
	_ = json.Unmarshal([]byte(`{"preset":"deepseek"}`), &r)
	if strings.Join(r.Levels, ",") != "low,high,max" || r.Aliases["medium"] != "high" || effortPresets["deepseek"].Aliases == nil {
		t.Errorf("deepseek preset: %+v", r)
	}
	r.Aliases["medium"] = "max" // a preset handed out is a copy
	if effortPresets["deepseek"].Aliases["medium"] != "high" {
		t.Error("a preset was changed through a copy")
	}
	for name, raw := range map[string]string{
		"an unknown preset":         `{"preset":"turbo"}`,
		"a preset with more":        `{"preset":"deepseek","default_level":"low"}`,
		"a word off the ladder":     `{"supported_levels":["low","turbo"]}`,
		"none among the levels":     `{"supported_levels":["none","low"]}`,
		"twice":                     `{"supported_levels":["low","low"]}`,
		"a default it lacks":        `{"supported_levels":["low"],"default_level":"high"}`,
		"an alias onto nothing":     `{"supported_levels":["low"],"aliases":{"high":"max"}}`,
		"an alias from none":        `{"supported_levels":["low"],"aliases":{"none":"low"}}`,
		"no levels said":            `{"default_level":"high"}`,
		"replay never and more":     `{"replay":"never","default_level":"high"}`,
		"replay without never":      `{"replay":"always"}`,
		"an unknown key":            `{"supported_levels":["low"],"turbo":true}`,
		"replay sometimes":          `{"supported_levels":["low"],"replay":"tools"}`,
		"a send patch":              `{"supported_levels":["low"],"send":{"chat":{"thinking":{"type":"enabled"}}}}`,
		"not an object":             `"deepseek"`,
		"a budget that is a number": `{"supported_levels":["low"],"budget":5}`,
	} {
		var r EffortRecord
		err := json.Unmarshal([]byte(raw), &r)
		if err == nil {
			err = validEntry(name, r, true)
		}
		if err == nil {
			t.Errorf("%s: accepted %s", name, raw)
		}
	}
	// the host's file may carry a send patch (kept, not acted on yet), never one touching the messages
	var host EffortRecord
	_ = json.Unmarshal([]byte(`{"supported_levels":["low"],"send":{"chat":{"chat_template_kwargs":{"thinking":true}}}}`), &host)
	if err := validEntry("host", host, false); err != nil || host.kind() != "" {
		t.Errorf("a host's send patch: %v, kind %q", err, host.kind())
	}
	_ = json.Unmarshal([]byte(`{"supported_levels":["low"],"send":{"chat":{"messages":[]}}}`), &host)
	if validEntry("host", host, false) == nil {
		t.Error("a send patch that writes messages was accepted")
	}
	var only EffortRecord
	if err := json.Unmarshal([]byte(`{"replay":"never"}`), &only); err != nil || validEntry("replay only", only, true) != nil || only.kind() != "" {
		t.Errorf("a replay-only entry: %v %v, kind %q", err, validEntry("replay only", only, true), only.kind())
	}
	if !slices.Equal(EffortPresetNames, []string{"openai", "toggle", "deepseek", "none"}) {
		t.Errorf("presets: %v", EffortPresetNames)
	}
}
