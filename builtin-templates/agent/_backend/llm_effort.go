// llm_effort.go — reasoning effort and reasoning replay (D249).
//
// The level: a conversation's pick (runs.config.effort, PATCH /runs/{id}),
// else its class's, else the tile's default (config.reasoningEffort, copied
// into each run at its start); "default" picks the model's own — nothing is
// sent. It is a word of OpenAI's ladder; the LLM connector maps it onto the
// model's own levels and says what it sent (X-XBin-LLM-Effort). The engine
// sends it only to a model that reasons: what the provider's model list says
// (effort, the connector's record — DeepSeek's own too), else, for a model
// nothing describes, as before (the OpenAI name rule on the Responses wire).
//
// Replay: a model's reasoning goes back to it on later turns — the same
// provider, model and wire that produced it — in the field it came in:
// reasoning_content, reasoning, or a leading <think> block in the content.
// An upstream that refuses it (400) gets the call once more without the
// plain reasoning (opaque blocks still go). Only when its refusal named the
// field and that call worked does the run stop sending that field, and the
// conversation says so; a later refusal while it is off brings it back,
// once per run. Nothing is remembered beyond the run, nor across restarts.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// effortLadder is OpenAI's: cheapest first.
var effortLadder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// effortDefault, as a conversation's or a class's pick: the model's own
// default — nothing is sent, whatever the tile's default says.
const effortDefault = "default"

func effortRank(w string) int { return slices.Index(effortLadder, w) }

// effortWords is the refusal of an effort that is none of them.
const effortWords = `effort: "default" (the model's own), "" (the class's or the tile's), or one of none, minimal, low, medium, high, xhigh, max`

// validEffort: what a conversation, a class or an ask may pick.
func validEffort(s string) bool { return s == "" || s == effortDefault || effortRank(s) >= 0 }

// effortRec is a model's reasoning record as its provider's list says it
// (the LLM connector's effort member; DeepSeek's own GET /models member).
// Untrusted: only ladder words, booleans and aliases from a ladder word to
// one of its levels are read. Aliases go to the composer's chip, which
// maps a level as the connector does (model/effort.js mapLevel; the rule's
// shared vectors: internal/llm/testdata/effort_mapping.json).
type effortRec struct {
	Levels          []string          `json:"supported_levels"`
	Default         string            `json:"default_level,omitempty"`
	CanDisable      *bool             `json:"can_disable,omitempty"`
	ThinksByDefault *bool             `json:"thinks_by_default,omitempty"`
	Aliases         map[string]string `json:"aliases,omitempty"`
	Replay          string            `json:"replay,omitempty"`
	Source          string            `json:"source,omitempty"`
	// Partial: a scale or a budget the record has and the connector doesn't
	// act on yet — without levels the record is not usable
	Partial bool `json:"partial,omitempty"`
}

// parseEffort reads an effort member; nil when it is no record.
func parseEffort(raw json.RawMessage) *effortRec {
	var m map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil || len(m) > 64 {
		return nil
	}
	var words []json.RawMessage
	if json.Unmarshal(m["supported_levels"], &words) != nil || words == nil || len(words) > 16 {
		return nil
	}
	r := &effortRec{Levels: []string{}}
	for _, w := range words {
		var s string
		if json.Unmarshal(w, &s) == nil && effortRank(s) > 0 && !slices.Contains(r.Levels, s) {
			r.Levels = append(r.Levels, s)
		}
	}
	if len(words) > 0 && len(r.Levels) == 0 {
		return nil // words, none of them ours: not a record
	}
	_ = json.Unmarshal(m["source"], &r.Source)
	if len(words) == 0 && !noReasoningSources[r.Source] {
		return nil // only the connector's own (an admin, the host, a maker's table) says a model has no reasoning
	}
	slices.SortFunc(r.Levels, func(a, b string) int { return effortRank(a) - effortRank(b) })
	var aliases map[string]json.RawMessage
	if json.Unmarshal(m["aliases"], &aliases) == nil && len(aliases) <= 16 {
		for from, raw := range aliases {
			var to string
			if effortRank(from) > 0 && json.Unmarshal(raw, &to) == nil && slices.Contains(r.Levels, to) {
				if r.Aliases == nil {
					r.Aliases = map[string]string{}
				}
				r.Aliases[from] = to
			}
		}
	}
	var s string
	if json.Unmarshal(m["default_level"], &s) == nil && effortRank(s) >= 0 {
		r.Default = s
	}
	var b bool
	if json.Unmarshal(m["can_disable"], &b) == nil {
		r.CanDisable = &b
	}
	var t bool
	if json.Unmarshal(m["thinks_by_default"], &t) == nil {
		r.ThinksByDefault = &t
	}
	if json.Unmarshal(m["replay"], &s) == nil && s == "never" {
		r.Replay = s
	}
	if len(r.Source) > 16 {
		r.Source = ""
	}
	r.Partial = len(m["scale"]) > 0 || len(m["budget"]) > 0 || string(m["partial"]) == "true"
	return r
}

// noReasoningSources are the records' sources that may say a model has no
// reasoning (supported_levels []): the connector's admin, its host, xbind's
// table on the maker's API. A provider's list never does.
var noReasoningSources = map[string]bool{"admin": true, "host": true, "xbin": true}

// reasoning answers whether the model reasons: known false when nothing
// usable says (a model nothing describes).
func (r *effortRec) reasoning() (known, yes bool) {
	if r == nil || (r.Partial && len(r.Levels) == 0) {
		return false, false
	}
	return true, len(r.Levels) > 0
}

// modelEffort is a model reference's record as its provider lists it (an
// alias: its own, else its target's); nil when nothing says.
func modelEffort(ctx context.Context, ref string) *effortRec {
	prov, id := splitModelRef(ref)
	c := modelCatalog(ctx)
	find := func(id string) *catalogModel {
		for i := range c.Models {
			m := &c.Models[i]
			if (m.ID == id || m.Ref == id) && (prov == "" || m.Provider == prov) {
				return m
			}
		}
		return nil
	}
	m := find(id)
	if m == nil {
		return nil
	}
	if m.Effort == nil && m.AliasOf != "" {
		if t := find(m.AliasOf); t != nil {
			return t.Effort
		}
	}
	return m.Effort
}

// effortFor is the level a turn of cfg asks for ("" sends none): the
// conversation's pick, else its class's, else the tile's default. A
// workspace agent's turns ask for none (it has no control of its own yet).
func effortFor(cfg Config) string {
	if cfg.Class == classWorkspace {
		return ""
	}
	pick := cfg.Effort
	if pick == "" {
		pick = classOf(cfg).Effort
	}
	switch pick {
	case effortDefault:
		return ""
	case "":
		return cfg.ReasoningEffort
	}
	return pick
}

// Reasons is LLMRequest.Reasons: what the model's record says of it.
const (
	reasonsUnknown = 0  // nothing describes it: as before (the name rule)
	reasonsYes     = 1  // it reasons
	reasonsNo      = -1 // it has no reasoning: no effort, no reasoning block
)

// effortOf is what the engine knows of model's reasoning: the bound
// provider's list (a test describes models with effortFn).
func (e *Engine) effortOf(ctx context.Context, model string) *effortRec {
	if e.effortFn != nil {
		return e.effortFn(model)
	}
	if e.ag == nil || e.ag.noGateway {
		return nil
	}
	return modelEffort(ctx, model)
}

// withEffort fills req's effort for a turn of cfg on req.Model.
func (e *Engine) withEffort(ctx context.Context, req *LLMRequest, cfg Config) {
	rec := e.effortOf(ctx, req.Model)
	req.ReasoningEffort = effortFor(cfg)
	switch known, yes := rec.reasoning(); {
	case known && yes:
		req.Reasons = reasonsYes
	case known:
		req.Reasons = reasonsNo
	}
	if rec != nil && rec.Replay == "never" {
		req.NoReplay = true
	}
}

// --- the transcript's line when the model changed -----------------------------

// effortWord is a ladder word as the transcript says it.
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

// effortSwitchNote is the line a conversation gets when its model changed
// and the level it asked for became another on the new model (header:
// X-XBin-LLM-Effort): "model → X · effort Max → High (its highest)". ""
// when the level is the model's as asked.
func effortSwitchNote(model, header string) string {
	parts := strings.Split(header, ";")
	sent := strings.TrimSpace(parts[0])
	asked, cant := "", false
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if a, ok := strings.CutPrefix(p, "asked="); ok {
			asked = a
		}
		cant = cant || p == "can't"
	}
	if asked == "" || effortRank(asked) < 0 || effortRank(sent) < 0 {
		return ""
	}
	why := ""
	switch {
	case cant:
		why = "it has no reasoning"
	case asked == "none":
		why = "it always thinks"
	case effortRank(sent) < effortRank(asked):
		why = "its highest at or below"
	default:
		why = "its own mapping"
	}
	to := effortWord(sent)
	if cant {
		to = "none"
	}
	return fmt.Sprintf("model → %s · effort %s → %s (%s)", bareModel(model), effortWord(asked), to, why)
}

// effortNotes writes the lines a call leaves in the conversation: the model
// changed and the level it asked for became another there; the provider
// refused the reasoning sent back.
func (e *Engine) effortNotes(ts *turnState, req LLMRequest, reply LLMReply) {
	var notes []string
	if reply.ReplayRefused != "" {
		notes = append(notes, replayNote(req.Model, reply.ReplayRefused))
	}
	if reply.ReplayBack {
		notes = append(notes, replayBackNote(req.Model))
	}
	if reply.Effort != "" {
		if prev := e.db.lastAssistantModel(ts.run.ID); prev != "" && prev != reply.Model {
			if n := effortSwitchNote(reply.Model, reply.Effort); n != "" {
				notes = append(notes, n)
			}
		}
	}
	if len(notes) == 0 {
		return
	}
	_ = e.fenced(func(t *DB) error {
		for _, n := range notes {
			e.emitStep(t, ts.root, t.journal(ts.run.ID, "note", map[string]string{"text": n}))
		}
		return nil
	})
}

// lastAssistantModel is the model of the run's newest answer ("" none).
func (d *DB) lastAssistantModel(runID int64) string {
	var raw string
	if d.q.QueryRow(`SELECT meta FROM messages WHERE run_id=? AND role='assistant' AND meta<>'' ORDER BY seq DESC LIMIT 1`, runID).Scan(&raw) != nil {
		return ""
	}
	var meta msgMeta
	_ = json.Unmarshal([]byte(raw), &meta)
	return meta.Model
}

// --- replay that an upstream refused --------------------------------------------

// runReplay is what a run remembers of its provider refusing plain
// reasoning sent back: the field it named (off), and whether it was brought
// back once already.
type runReplay struct {
	off  string
	back bool
}

// maxRunReplay bounds the runs remembered (an evicted one sends its
// reasoning again, and learns again).
const maxRunReplay = 4096

// replayOf fills req's PlainOff and Reenable from its run's memory.
func (e *Engine) replayOf(req *LLMRequest) {
	if req.Run == 0 {
		return
	}
	e.replayMu.Lock()
	defer e.replayMu.Unlock()
	if r := e.replayRuns[req.Run]; r != nil {
		req.PlainOff, req.Reenable = r.off, r.off != "" && !r.back
	}
}

// replayLearn keeps what a call's reply says of its run's replay.
func (e *Engine) replayLearn(req LLMRequest, reply LLMReply) {
	if req.Run == 0 || (reply.ReplayRefused == "" && !reply.ReplayBack) {
		return
	}
	e.replayMu.Lock()
	defer e.replayMu.Unlock()
	if e.replayRuns == nil {
		e.replayRuns = map[int64]*runReplay{}
	}
	r := e.replayRuns[req.Run]
	if r == nil {
		if len(e.replayRuns) >= maxRunReplay {
			for k := range e.replayRuns {
				delete(e.replayRuns, k)
				break
			}
		}
		r = &runReplay{}
		e.replayRuns[req.Run] = r
	}
	if reply.ReplayBack {
		r.off, r.back = "", true
		logf("llm: run %d: a call without the earlier reasoning was refused too; %s is sent it again in this conversation", req.Run, bareModel(req.Model))
	} else {
		r.off = reply.ReplayRefused
		logf("llm: run %d: %s refused the reasoning sent back with earlier answers (400, %s); it is not sent again in this conversation", req.Run, bareModel(req.Model), reply.ReplayRefused)
	}
}

// namedField is the field of sent that a refusal names as a word of its
// own ("" none): what a run may remember. A 400 that names no field it was
// sent is something else — and a caller can provoke one.
func namedField(err error, sent []string) string {
	low := strings.ToLower(err.Error())
	for _, f := range sent {
		if f == "think" {
			continue // in the content: no field to name
		}
		for i := 0; ; {
			j := strings.Index(low[i:], f)
			if j < 0 {
				break
			}
			j += i
			end := j + len(f)
			if (j == 0 || !wordByte(low[j-1])) && (end == len(low) || !wordByte(low[end])) {
				return f
			}
			i = end
		}
	}
	return ""
}

func wordByte(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }

// replayRefused: the call failed with a 400 the upstream gave, not about
// the context's size (a longer conversation is compaction's, and must
// never switch replay off for everyone).
func replayRefused(err error) bool {
	var f *llmFailed
	if !errors.As(err, &f) || f.Status != 400 {
		return false
	}
	low := strings.ToLower(err.Error())
	for _, mark := range []string{"context_length_exceeded", "context length", "context window", "maximum context", "too many tokens", "prompt is too long"} {
		if strings.Contains(low, mark) {
			return false
		}
	}
	return true
}

// replayNote is what the conversation says when that happened.
func replayNote(model, field string) string {
	return fmt.Sprintf("The model's provider refused the reasoning sent back with earlier answers (400, %s), so the call was made without it — %s is no longer sent its earlier reasoning in this conversation.", field, bareModel(model))
}

// replayBackNote is what it says when a call without it was refused too.
func replayBackNote(model string) string {
	return fmt.Sprintf("A call without the earlier reasoning was refused too, and the call with it worked — %s is sent its earlier reasoning again in this conversation.", bareModel(model))
}
