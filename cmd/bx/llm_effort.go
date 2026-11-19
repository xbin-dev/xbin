package main

import (
	"fmt"
	"sort"
	"strings"
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
