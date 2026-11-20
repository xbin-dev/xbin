package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// llm_effort.go — bx llm's words for a model's reasoning effort (D249,
// docs/llm.md "Reasoning effort"): the line a record is shown by, and the
// --reasoning values an admin describes a model with.

// llmEffort is a model's reasoning-effort record (D249) as the connector
// answers it: on a model of GET /v1/models, and in an upstream's reasoning.
type llmEffort struct {
	Levels     *[]string `json:"supported_levels"`
	Default    string    `json:"default_level"`
	CanDisable *bool     `json:"can_disable"`
	Reasons    *bool     `json:"reasons"`
	Source     string    `json:"source"`
}

// valid keeps of e only ladder words, and a source the connector names
// (l2: a record is printed only as words bx knows); nil when no level is
// left of a list that had some.
func (e *llmEffort) valid() *llmEffort {
	if e == nil {
		return nil
	}
	out := *e
	if e.Levels != nil {
		levels := []string{}
		for _, l := range *e.Levels {
			if slicesIndex(effortLadder, l) > 0 && slicesIndex(levels, l) < 0 {
				levels = append(levels, l)
			}
		}
		if len(levels) == 0 && len(*e.Levels) > 0 {
			return nil
		}
		out.Levels = &levels
	}
	if slicesIndex(effortLadder, out.Default) < 0 {
		out.Default = ""
	}
	if _, ok := effortFrom[out.Source]; !ok {
		out.Source = ""
	}
	return &out
}

var effortFrom = map[string]string{"upstream": "from the upstream", "xbin": "from xbin", "admin": "from the admin", "host": "set by your host"}

// effortLine is "Reasoning: Low · High · Max (default High; can be off) ·
// from the upstream" — or what is known instead.
func effortLine(e *llmEffort) string {
	e = e.valid()
	if e == nil || (e.Levels == nil && (e.Reasons == nil || !*e.Reasons)) {
		return "Reasoning: not described — no control is offered"
	}
	from := effortFrom[e.Source]
	if from == "" {
		from = "from the connector"
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

// suggestLine is a model's line when it is not described on its upstream
// but xbin's table knows its maker's levels (GET /v1/models
// xbin_effort_suggestion): "not described — xbin knows the maker's levels:
// Low · High · Max (default High) — to use them: bx llm upstream set <u>
// --reasoning <model>=low,high,max/high". "" when s is no record.
func suggestLine(id string, s *llmEffort) string {
	s = s.valid()
	if s == nil || s.Levels == nil || len(*s.Levels) == 0 {
		return ""
	}
	words, spec := make([]string, 0, len(*s.Levels)), make([]string, 0, len(*s.Levels)+1)
	if s.CanDisable != nil && *s.CanDisable {
		spec = append(spec, "none")
	}
	for _, l := range *s.Levels {
		words, spec = append(words, effortWord(l)), append(spec, l)
	}
	def, defSpec := "", ""
	if s.Default != "" {
		def, defSpec = fmt.Sprintf(" (default %s)", effortWord(s.Default)), "/"+s.Default
	}
	up, model, _ := strings.Cut(id, "/")
	return fmt.Sprintf("Reasoning: not described — xbin knows the maker's levels: %s%s — to use them: bx llm upstream set %s --reasoning '%s=%s%s'",
		strings.Join(words, " · "), def, up, model, strings.Join(spec, ","), defSpec)
}

func llmModelsCmd(jsonOut bool) error {
	var out struct {
		Data []struct {
			ID      string          `json:"id"`
			OwnedBy string          `json:"owned_by"`
			AliasOf string          `json:"alias_of"`
			Effort  json.RawMessage `json:"effort"`
			Suggest json.RawMessage `json:"xbin_effort_suggestion"`
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
		var e, sg llmEffort
		if len(m.Effort) > 0 && json.Unmarshal(m.Effort, &e) == nil && (e.Levels != nil || (e.Reasons != nil && *e.Reasons)) && e.valid() != nil {
			fmt.Printf("%s  %s\n", m.ID, effortLine(&e))
			continue
		}
		if len(m.Suggest) > 0 && json.Unmarshal(m.Suggest, &sg) == nil {
			if line := suggestLine(m.ID, &sg); line != "" {
				fmt.Printf("%s  %s\n", m.ID, line)
				continue
			}
		}
		fmt.Println(m.ID)
	}
	for _, name := range llmKeys(out.Errors) {
		fmt.Fprintf(os.Stderr, "bx: upstream %s: %s\n", name, out.Errors[name])
	}
	return nil
}

// llmReplayOff is one model whose reasoning a translated call no longer
// sends back: its upstream refused the field (D249).
type llmReplayOff struct {
	Since time.Time `json:"since"`
	Field string    `json:"field"`
}

func printReplayOff(u llmUpstream) {
	for _, m := range llmKeys(u.ReplayOff) {
		r := u.ReplayOff[m]
		fmt.Printf("    %s: replay off since %s — the upstream refused %s (bx llm upstream replay-reset %s %s)\n", m, r.Since.Local().Format("2006-01-02 15:04"), r.Field, u.Name, m)
	}
}

// llmReplayReset — bx llm upstream replay-reset <name> [<model>].
func llmReplayReset(pos []string, usage error) error {
	if len(pos) < 1 || len(pos) > 2 {
		return usage
	}
	body := map[string]string{}
	if len(pos) == 2 {
		body["model"] = pos[1]
	}
	var v llmConfig
	if err := llmCall("POST", "/api/xbin/llm/upstreams/"+url.PathEscape(pos[0])+"/replay-reset", body, &v, nil); err != nil {
		return err
	}
	fmt.Printf("upstream %s: reasoning is sent back again\n", pos[0])
	return nil
}
