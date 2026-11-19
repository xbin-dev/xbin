package llm

// effort.go — reasoning effort (D249, docs/llm.md "Reasoning effort").
//
// A caller says how hard a model should think with OpenAI's words — chat's
// reasoning_effort, Responses' reasoning.effort — on one ordered ladder:
// none < minimal < low < medium < high < xhigh < max. Models take a subset
// of it, with a default of their own, some can't be turned off, and some
// have no reasoning at all. What the connector knows of an upstream's model
// is its capability record (EffortRecord), from, highest trust first:
//
//  1. the host's file or the admin's entry, upstreams.<name>.reasoning — a
//     table keyed like prices (exact id → the id after its last "/" → the
//     longest "prefix*"); an entry replaces the rest whole;
//  2. the upstream's own model list (GET /models: DeepSeek's effort,
//     OpenRouter's reasoning; a router's supported_features or
//     supported_parameters naming reasoning says only that the model
//     reasons) — untrusted: only ladder words, booleans and a bounded alias
//     map are read from it, and a list's silence says nothing (a router
//     that leaves reasoning_effort out of a model's parameters may well
//     take it);
//  3. a table vendored into xbind (effort_table.json), written from the
//     providers' docs: 2 is laid over it member by member.
//
// A model nothing describes is passed through exactly as today: nothing is
// added, nothing rewritten. For a described model the caller's level is
// mapped (mapEffort): a level the model has passes; else the provider's own
// alias for it; else the highest level the model has at or below it; else
// its lowest. "none" turns reasoning off only where the model can be; else
// it is its lowest. A word that is no ladder word passes untouched. A model
// whose record says it has no reasoning gets no effort: the caller's is
// dropped and named (X-XBin-LLM-Dropped). The answer says what was sent:
// X-XBin-LLM-Effort.
//
// A record's scale, budget and send members (a numeric knob, a token
// budget, a request patch) are read and kept but not acted on yet: a record
// that needs them to be usable is treated as unknown.

import (
	"embed"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

// EffortHeader, on an answer to a call that carried a reasoning effort to
// a described model, says what was sent: "high", "high; asked=medium" when
// the caller's level was mapped, "none; asked=high; can't" when the model
// has no reasoning (the level was dropped).
const EffortHeader = "X-XBin-LLM-Effort"

// effortLadder is the portable intent, cheapest first: OpenAI's words.
var effortLadder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// effortRank is a ladder word's place on the ladder, -1 for any other.
func effortRank(w string) int { return slices.Index(effortLadder, w) }

// Where a record came from (EffortRecord.Source): shown, never sent upstream.
const (
	EffortFromUpstream = "upstream"
	EffortFromXbin     = "xbin"
	EffortFromAdmin    = "admin"
	EffortFromHost     = "host"
)

// ReplayNever is the record's say that a model's reasoning is not to be sent
// back to it (an endpoint that refuses the field): the escape hatch.
const ReplayNever = "never"

// The bounds of what is read from a record.
const (
	maxEffortAliases = 16
	maxEffortLevels  = 16 // a longer list (the ladder has seven words) is no record: it is ignored whole
)

// EffortRecord is what the connector knows of one model's reasoning knob.
type EffortRecord struct {
	// Levels are the ladder words the model takes when it reasons, cheapest
	// first, "none" left out. nil: not said (the record is not usable);
	// empty: the model has no reasoning.
	Levels []string
	// Default is what the model does when nothing is sent ("" unknown; a
	// ladder word, "none" for a model that thinks only when asked).
	Default string
	// CanDisable: "none" turns its reasoning off. ThinksByDefault: it
	// reasons when nothing is sent. Reasons: it reasons (an upstream's list
	// that says so without its levels). nil: not said.
	CanDisable, ThinksByDefault, Reasons *bool
	// Aliases are the provider's own mapping of the ladder words the model
	// lacks (DeepSeek: medium → high).
	Aliases map[string]string
	// Scale, Budget and Send are kept, not yet acted on (D249 stages 4–5).
	Scale, Budget, Send json.RawMessage
	// Replay: "" — the model's reasoning goes back to it on later turns;
	// "never" — it doesn't (an endpoint that refuses the field).
	Replay string
	// Source is where the record came from (Effort…): shown, never sent.
	Source string
}

// effortJSON is a record's JSON shape (the member names are DeepSeek's own
// GET /models member and its first keys).
type effortJSON struct {
	SupportedLevels *[]string         `json:"supported_levels,omitempty"`
	DefaultLevel    string            `json:"default_level,omitempty"`
	CanDisable      *bool             `json:"can_disable,omitempty"`
	ThinksByDefault *bool             `json:"thinks_by_default,omitempty"`
	Reasons         *bool             `json:"reasons,omitempty"`
	Aliases         map[string]string `json:"aliases,omitempty"`
	Scale           json.RawMessage   `json:"scale,omitempty"`
	Budget          json.RawMessage   `json:"budget,omitempty"`
	Send            json.RawMessage   `json:"send,omitempty"`
	Replay          string            `json:"replay,omitempty"`
	Source          string            `json:"source,omitempty"`
}

func (r EffortRecord) toJSON(withSend bool) effortJSON {
	j := effortJSON{DefaultLevel: r.Default, CanDisable: r.CanDisable, ThinksByDefault: r.ThinksByDefault, Reasons: r.Reasons,
		Aliases: r.Aliases, Scale: r.Scale, Budget: r.Budget, Replay: r.Replay, Source: r.Source}
	if r.Levels != nil {
		l := slices.Clone(r.Levels)
		j.SupportedLevels = &l
	}
	if withSend {
		j.Send = r.Send
	}
	return j
}

// MarshalJSON writes the record as the configuration keeps it.
func (r EffortRecord) MarshalJSON() ([]byte, error) { return json.Marshal(r.toJSON(true)) }

// listing is the record as a model list carries it: never a send patch,
// which is server-side configuration only.
func (r EffortRecord) listing() json.RawMessage {
	b, _ := json.Marshal(r.toJSON(false))
	return b
}

// UnmarshalJSON reads a record as configuration writes it, strictly (an
// unknown key is an error, as everywhere in the host's file; validEffort
// judges the words) — or {"preset": "<name>"}, one of effortPresets.
func (r *EffortRecord) UnmarshalJSON(b []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil || probe == nil {
		return fmt.Errorf("a reasoning entry is an object: {\"preset\": %s} or a record", strings.Join(EffortPresetNames, " | "))
	}
	if raw, has := probe["preset"]; has {
		var name string
		if len(probe) != 1 || json.Unmarshal(raw, &name) != nil {
			return fmt.Errorf("{\"preset\": <name>} takes nothing beside it")
		}
		rec, ok := effortPresets[name]
		if !ok {
			return fmt.Errorf("preset %q — one of %s", name, strings.Join(EffortPresetNames, ", "))
		}
		*r = rec.clone()
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var j effortJSON
	if err := dec.Decode(&j); err != nil {
		return err
	}
	*r = EffortRecord{Default: j.DefaultLevel, CanDisable: j.CanDisable, ThinksByDefault: j.ThinksByDefault, Reasons: j.Reasons, Aliases: j.Aliases,
		Scale: j.Scale, Budget: j.Budget, Send: j.Send, Replay: j.Replay, Source: j.Source}
	if j.SupportedLevels != nil {
		r.Levels = append([]string{}, *j.SupportedLevels...)
		sort.SliceStable(r.Levels, func(a, b int) bool { return effortRank(r.Levels[a]) < effortRank(r.Levels[b]) }) // cheapest first, as the mapping reads them
	}
	return nil
}

// kind is how the mapping can use the record: "levels", "off" (the model has
// no reasoning), or "" — not usable now (nothing said of its levels, or it
// needs a scale, a budget or a send patch, which aren't acted on yet).
func (r *EffortRecord) kind() string {
	switch {
	case r == nil || r.Levels == nil || len(r.Send) > 0:
		return ""
	case len(r.Levels) == 0 && (len(r.Scale) > 0 || len(r.Budget) > 0):
		return ""
	case len(r.Levels) == 0:
		return "off"
	}
	return "levels"
}

func (r *EffortRecord) canDisable() bool { return r.CanDisable != nil && *r.CanDisable }

// replays reports whether the model's reasoning goes back to it.
func (r *EffortRecord) replays() bool { return r == nil || r.Replay != ReplayNever }

// validEffort checks a record as configuration writes it (the admin's or the
// host's): ladder words in their places, aliases onto the model's own
// levels, bounded sizes, and nothing a request could be patched with that a
// patch must never touch.
func validEffort(what string, r EffortRecord) error {
	if len(r.Levels) > maxEffortLevels {
		return fmt.Errorf("%s: supported_levels has more than %d entries", what, maxEffortLevels)
	}
	seen := map[string]bool{}
	for _, l := range r.Levels {
		if effortRank(l) <= 0 {
			return fmt.Errorf("%s: supported_levels: %q — one of %s (none is can_disable)", what, l, strings.Join(effortLadder[1:], ", "))
		}
		if seen[l] {
			return fmt.Errorf("%s: supported_levels names %s twice", what, l)
		}
		seen[l] = true
	}
	if r.Default != "" && effortRank(r.Default) < 0 {
		return fmt.Errorf("%s: default_level %q — a ladder word: %s", what, r.Default, strings.Join(effortLadder, ", "))
	}
	if r.Default != "" && r.Default != "none" && r.Levels != nil && !seen[r.Default] {
		return fmt.Errorf("%s: default_level %s is not among its supported_levels", what, r.Default)
	}
	if len(r.Aliases) > maxEffortAliases {
		return fmt.Errorf("%s: more than %d aliases", what, maxEffortAliases)
	}
	for from, to := range r.Aliases {
		if effortRank(from) <= 0 || !seen[to] {
			return fmt.Errorf("%s: alias %s → %s: from a ladder word (not none) to one of its supported_levels", what, from, to)
		}
	}
	switch r.Replay {
	case "", ReplayNever:
	default:
		return fmt.Errorf("%s: replay %q — \"never\", or leave it out (reasoning goes back to the model that produced it)", what, r.Replay)
	}
	for name, raw := range map[string]json.RawMessage{"scale": r.Scale, "budget": r.Budget, "send": r.Send} {
		if len(raw) == 0 {
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil || len(raw) > 4<<10 {
			return fmt.Errorf("%s: %s must be an object (at most 4 KiB)", what, name)
		}
	}
	if len(r.Send) > 0 {
		var send map[string]json.RawMessage
		_ = json.Unmarshal(r.Send, &send)
		for wire, patch := range send {
			var p map[string]json.RawMessage
			_ = json.Unmarshal(patch, &p)
			for k := range p {
				if forbiddenPatch[k] {
					return fmt.Errorf("%s: send.%s may not touch %s", what, wire, k)
				}
			}
		}
	}
	return nil
}

// forbiddenPatch are the request members a send patch may never write.
var forbiddenPatch = map[string]bool{"model": true, "messages": true, "input": true, "tools": true, "stream": true, "stream_options": true}

// ---- presets: what an admin picks on /xbin/llm and with bx llm ------------------

// effortPresets are the descriptions an admin can give a model in one pick
// ({"preset": "<name>"} in upstreams.<name>.reasoning).
var effortPresets = map[string]EffortRecord{
	// OpenAI's classic three: what most self-hosted servers take (vLLM, SGLang, llama.cpp, Ollama's gpt-oss)
	"openai": {Levels: []string{"low", "medium", "high"}, Default: "medium", CanDisable: boolp(false), ThinksByDefault: boolp(true),
		Aliases: map[string]string{"minimal": "low", "xhigh": "high", "max": "high"}},
	// thinking on or off (a hybrid model): "none" turns it off, any other level on
	"toggle": {Levels: []string{"high"}, CanDisable: boolp(true)},
	// DeepSeek's API: low, high, max; medium is high
	"deepseek": {Levels: []string{"low", "high", "max"}, Default: "high", CanDisable: boolp(true), ThinksByDefault: boolp(true),
		Aliases: map[string]string{"minimal": "low", "medium": "high", "xhigh": "high"}},
	// a model without reasoning: an effort a caller sends is dropped (strict providers refuse it)
	"none": {Levels: []string{}},
}

// EffortPresetNames are the presets, in the order a picker offers them.
var EffortPresetNames = []string{"openai", "toggle", "deepseek", "none"}

func boolp(b bool) *bool { return &b }

// validEntry checks one entry of upstreams.<name>.reasoning: a record that
// says its levels, valid; from the admin's API (admin), no send patch yet.
func validEntry(what string, rec EffortRecord, admin bool) error {
	if rec.Levels == nil {
		return fmt.Errorf("%s: a record says its supported_levels ([] for a model without reasoning)", what)
	}
	if admin && len(rec.Send) > 0 {
		return fmt.Errorf("%s: send patches are not taken from the admin API yet — pick a preset", what)
	}
	return validEffort(what, rec)
}

func (r EffortRecord) clone() EffortRecord {
	out := r
	if r.Levels != nil {
		out.Levels = slices.Clone(r.Levels)
	}
	out.Aliases = maps.Clone(r.Aliases)
	return out
}

// ---- the upstream's own list: untrusted --------------------------------------------

// effortFromListing reads what an upstream's model object says of its
// reasoning: DeepSeek's effort member (or another xbind's), else
// OpenRouter's reasoning object, else — a router's supported_features or
// supported_parameters naming reasoning — only that the model reasons (its
// levels then come from xbind's table, or an admin). Untrusted data: only
// ladder words, booleans and a bounded alias map are read; a scale, a
// budget, a send patch or a source in it are never taken; and what a list
// leaves out says nothing (a router lists a model without reasoning_effort
// that takes it).
func effortFromListing(extra map[string]json.RawMessage) (EffortRecord, bool) {
	if raw, ok := extra["effort"]; ok {
		if r, ok := effortFromUpstreamMember(raw); ok {
			return r, true
		}
	}
	if raw, ok := extra["reasoning"]; ok {
		if r, ok := effortFromOpenRouter(raw); ok {
			return r, true
		}
	}
	for _, key := range []string{"supported_features", "supported_parameters", "supported_sampling_parameters"} {
		var names []json.RawMessage
		if json.Unmarshal(extra[key], &names) != nil || len(names) > 256 {
			continue
		}
		for _, n := range names {
			var s string
			if json.Unmarshal(n, &s) == nil && (s == "reasoning" || s == "reasoning_effort") {
				return EffortRecord{Reasons: boolp(true), Source: EffortFromUpstream}, true
			}
		}
	}
	return EffortRecord{}, false
}

// ladderWords reads a list of ladder words: none (whether "none" was among
// them) and the rest, ordered and unique. ok is false for anything that is
// no bounded list of strings, or holds no ladder word.
func ladderWords(raw json.RawMessage) (levels []string, none, ok bool) {
	var words []json.RawMessage
	if json.Unmarshal(raw, &words) != nil || len(words) == 0 || len(words) > maxEffortLevels {
		return nil, false, false
	}
	seen := map[string]bool{}
	for _, w := range words {
		var s string
		if json.Unmarshal(w, &s) != nil {
			continue
		}
		switch r := effortRank(s); {
		case r == 0:
			none = true
		case r > 0 && !seen[s]:
			seen[s] = true
			levels = append(levels, s)
		}
	}
	sort.Slice(levels, func(i, j int) bool { return effortRank(levels[i]) < effortRank(levels[j]) })
	if levels == nil && !none {
		return nil, false, false
	}
	if levels == nil {
		levels = []string{}
	}
	return levels, none, true
}

func rawBool(raw json.RawMessage) *bool {
	var b bool
	if len(raw) == 0 || json.Unmarshal(raw, &b) != nil {
		return nil
	}
	return &b
}

func rawLadder(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil || effortRank(s) < 0 {
		return ""
	}
	return s
}

// effortFromUpstreamMember reads an upstream's effort member: DeepSeek's
// {supported_levels, default_level} and the keys of this record's shape.
func effortFromUpstreamMember(raw json.RawMessage) (EffortRecord, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || len(m) > 64 {
		return EffortRecord{}, false
	}
	r := EffortRecord{Source: EffortFromUpstream}
	if lv, ok := m["supported_levels"]; ok {
		levels, none, ok := ladderWords(lv)
		var empty []json.RawMessage
		switch {
		case ok:
			r.Levels = levels
			if none && rawBool(m["can_disable"]) == nil {
				r.CanDisable = boolp(true)
			}
		case json.Unmarshal(lv, &empty) == nil && empty != nil && len(empty) == 0:
			r.Levels = []string{} // an explicit [] : no reasoning
		}
	}
	r.Default = rawLadder(m["default_level"])
	if r.CanDisable == nil {
		r.CanDisable = rawBool(m["can_disable"])
	}
	r.ThinksByDefault = rawBool(m["thinks_by_default"])
	r.Reasons = rawBool(m["reasons"])
	r.Aliases = untrustedAliases(m["aliases"], r.Levels)
	if s := ""; json.Unmarshal(m["replay"], &s) == nil && s == ReplayNever {
		r.Replay = ReplayNever
	}
	if r.Levels == nil && r.Default == "" && r.CanDisable == nil && r.ThinksByDefault == nil && r.Reasons == nil {
		return EffortRecord{}, false
	}
	return r, true
}

// untrustedAliases keeps the aliases from a ladder word to one of levels.
func untrustedAliases(raw json.RawMessage, levels []string) map[string]string {
	var m map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil || len(m) > maxEffortAliases {
		return nil
	}
	out := map[string]string{}
	for from, v := range m {
		var to string
		if effortRank(from) > 0 && json.Unmarshal(v, &to) == nil && slices.Contains(levels, to) {
			out[from] = to
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// effortFromOpenRouter reads OpenRouter's reasoning object:
// {supported_efforts, default_effort, default_enabled, mandatory}.
func effortFromOpenRouter(raw json.RawMessage) (EffortRecord, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || len(m) > 64 {
		return EffortRecord{}, false
	}
	levels, none, ok := ladderWords(m["supported_efforts"])
	if !ok {
		return EffortRecord{}, false
	}
	r := EffortRecord{Levels: levels, Default: rawLadder(m["default_effort"]), ThinksByDefault: rawBool(m["default_enabled"]), Source: EffortFromUpstream}
	if mandatory := rawBool(m["mandatory"]); mandatory != nil {
		r.CanDisable = boolp(!*mandatory)
	} else if none {
		r.CanDisable = boolp(true)
	}
	if len(levels) == 0 && !none {
		return EffortRecord{}, false
	}
	return r, true
}

// overlay lays hi over lo member by member: what hi says wins, what it
// leaves out lo fills in. The source is the one whose levels the record has.
func overlay(hi, lo EffortRecord) EffortRecord {
	out := lo.clone()
	if hi.Levels != nil {
		out.Levels, out.Source = slices.Clone(hi.Levels), hi.Source
		if lo.Levels != nil {
			// lo's aliases onto levels hi doesn't have are lo's guess about another stack
			kept := map[string]string{}
			for k, v := range out.Aliases {
				if slices.Contains(out.Levels, v) {
					kept[k] = v
				}
			}
			out.Aliases = kept
		}
	} else if out.Levels == nil {
		out.Source = hi.Source
	}
	if hi.Default != "" {
		out.Default = hi.Default
	}
	if out.Default != "" && out.Default != "none" && !slices.Contains(out.Levels, out.Default) {
		out.Default = ""
	}
	if hi.CanDisable != nil {
		out.CanDisable = hi.CanDisable
	}
	if hi.ThinksByDefault != nil {
		out.ThinksByDefault = hi.ThinksByDefault
	}
	if hi.Reasons != nil {
		out.Reasons = hi.Reasons
	}
	for k, v := range hi.Aliases {
		if out.Aliases == nil {
			out.Aliases = map[string]string{}
		}
		out.Aliases[k] = v
	}
	if len(out.Aliases) == 0 {
		out.Aliases = nil
	}
	if hi.Replay != "" {
		out.Replay = hi.Replay
	}
	return out
}

// ---- the vendored table ------------------------------------------------------------

//go:embed effort_table.json
var effortTableFS embed.FS

// effortFamily is one entry of effort_table.json: the model ids it covers
// (keyed like prices) and the record, with where it was read.
type effortFamily struct {
	Family string          `json:"family"`
	Models []string        `json:"models"`
	Effort EffortRecord    `json:"effort"`
	Read   string          `json:"read"` // the provider's page(s) it was written from, and when
	Notes  json.RawMessage `json:"notes,omitempty"`
}

// vendoredEffort is the table, by model pattern; loaded once.
var vendoredEffort = func() map[string]EffortRecord {
	b, err := effortTableFS.ReadFile("effort_table.json")
	if err != nil {
		panic(err)
	}
	t, err := parseEffortTable(b)
	if err != nil {
		panic("effort_table.json: " + err.Error())
	}
	return t
}()

func parseEffortTable(b []byte) (map[string]EffortRecord, error) {
	var file struct {
		About    string         `json:"about"`
		Families []effortFamily `json:"families"`
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, err
	}
	out := map[string]EffortRecord{}
	for _, f := range file.Families {
		if f.Read == "" || len(f.Models) == 0 {
			return nil, fmt.Errorf("%s: every family says what it was read from, and its models", f.Family)
		}
		if f.Effort.Levels == nil {
			return nil, fmt.Errorf("%s: supported_levels missing", f.Family)
		}
		if err := validEffort(f.Family, f.Effort); err != nil {
			return nil, err
		}
		if len(f.Effort.Send) > 0 {
			return nil, fmt.Errorf("%s: no send patch in the vendored table", f.Family)
		}
		rec := f.Effort.clone()
		rec.Source = EffortFromXbin
		for _, m := range f.Models {
			if _, dup := out[m]; dup {
				return nil, fmt.Errorf("%s: model %s is in two families", f.Family, m)
			}
			out[m] = rec
		}
	}
	return out, nil
}

// lookupModel finds model in a table keyed like prices: its exact id, else
// the id after its last "/", else the longest key ending in "*" that
// prefixes either.
func lookupModel[V any](t map[string]V, model string) (V, bool) {
	if v, ok := t[model]; ok {
		return v, true
	}
	bare := model
	if i := strings.LastIndexByte(model, '/'); i >= 0 {
		bare = model[i+1:]
		if v, ok := t[bare]; ok {
			return v, true
		}
	}
	bestKey, bestLen := "", -1
	for k := range t {
		pre, wild := strings.CutSuffix(k, "*")
		if !wild || !(strings.HasPrefix(model, pre) || strings.HasPrefix(bare, pre)) {
			continue
		}
		if len(pre) > bestLen || (len(pre) == bestLen && k < bestKey) {
			bestKey, bestLen = k, len(pre)
		}
	}
	if bestLen >= 0 {
		return t[bestKey], true
	}
	var zero V
	return zero, false
}

// effortOf is what the connector knows of model on u: the host's or the
// admin's entry whole, else the upstream's own list over the vendored table
// (extra: the model's object in that list; nil when unknown). nil: nothing.
func effortOf(u effUpstream, model string, extra map[string]json.RawMessage) *EffortRecord {
	if rec, ok := lookupModel(u.Reasoning, model); ok {
		rec = rec.clone()
		rec.Source = EffortFromAdmin
		if u.Host {
			rec.Source = EffortFromHost
		}
		return &rec
	}
	table, inTable := lookupModel(vendoredEffort, model)
	own, inList := effortFromListing(extra)
	switch {
	case inTable && inList:
		rec := overlay(own, table)
		return &rec
	case inList:
		return &own
	case inTable:
		rec := table.clone()
		return &rec
	}
	return nil
}

// ---- the mapping -----------------------------------------------------------------

// effortMap is what becomes of a caller's level on a model.
type effortMap struct {
	sent   string // the level sent ("" with drop)
	drop   bool   // the model has no reasoning: the member is removed
	header string // X-XBin-LLM-Effort ("" none: nothing to say)
}

// mapEffort maps the caller's level asked onto rec. A model nothing
// describes (rec unusable), and a word that is no ladder word, pass
// untouched and unsaid.
func mapEffort(rec *EffortRecord, asked string) effortMap {
	rank := effortRank(asked)
	switch k := rec.kind(); {
	case k == "" || rank < 0:
		return effortMap{sent: asked}
	case k == "off":
		h := "none; can't"
		if asked != "none" {
			h = "none; asked=" + asked + "; can't"
		}
		return effortMap{drop: true, header: h}
	}
	levels := rec.Levels
	sent := ""
	switch {
	case asked == "none" && rec.canDisable():
		sent = "none"
	case asked == "none":
		sent = levels[0]
	case slices.Contains(levels, asked):
		sent = asked
	case slices.Contains(levels, rec.Aliases[asked]):
		sent = rec.Aliases[asked]
	default:
		sent = levels[0]
		for _, l := range levels {
			if effortRank(l) <= rank {
				sent = l
			}
		}
	}
	h := sent
	if sent != asked {
		h += "; asked=" + asked
	}
	return effortMap{sent: sent, header: h}
}

// ---- requests ---------------------------------------------------------------------

// effortRewrite applies the mapping to a request of either wire, in place
// (set and drop are the request's member writers). The effort is read where
// each wire carries it: chat's reasoning_effort (and an OpenRouter-style
// reasoning.effort beside it), Responses' reasoning.effort. dropped names
// the members taken out (a model with no reasoning); header is
// X-XBin-LLM-Effort's value ("" for none). Nothing is touched for a model
// rec doesn't describe.
func effortRewrite(rec *EffortRecord, chat bool, get func(string) json.RawMessage, set func(string, json.RawMessage)) (dropped []string, header string) {
	if rec.kind() == "" {
		return nil, ""
	}
	if chat {
		if raw := get("reasoning_effort"); rawSet(raw) {
			var asked string
			if json.Unmarshal(raw, &asked) == nil {
				m := mapEffort(rec, asked)
				switch {
				case m.drop:
					set("reasoning_effort", nil)
					dropped = append(dropped, "reasoning_effort")
				case m.sent != asked:
					set("reasoning_effort", jsonString(m.sent))
				}
				header = m.header
			}
		}
	}
	raw := get("reasoning")
	var obj map[string]json.RawMessage
	if !rawSet(raw) || json.Unmarshal(raw, &obj) != nil {
		return dropped, header
	}
	var asked string
	if json.Unmarshal(obj["effort"], &asked) != nil {
		return dropped, header
	}
	m := mapEffort(rec, asked)
	switch {
	case m.drop:
		delete(obj, "effort")
		dropped = append(dropped, "reasoning.effort")
	case m.sent != asked:
		obj["effort"] = jsonString(m.sent)
	default:
		if header == "" {
			header = m.header
		}
		return dropped, header
	}
	if header == "" {
		header = m.header
	}
	if len(obj) == 0 {
		set("reasoning", nil)
	} else {
		b, _ := json.Marshal(obj)
		set("reasoning", b)
	}
	return dropped, header
}

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}
