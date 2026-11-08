// needs_text.go — what a run waiting for a person waits on, in words, and
// since when (#56): the question or approval GET /needs items carry (text,
// since), what the project coordinator reads of a task that waits
// (projects_coord_waits.go) and what the project's feed says when a task
// starts waiting (project_events.go). One reading of runs.pending for all
// of them: a coding agent's park (harnessNeed), the built-in engine's
// approval, a task's workspace park, else ask_user's question (run.result).
package main

import (
	"encoding/json"
	"sort"
	"strings"
)

// needTextMax bounds a need's text (a /needs item's, the coordinator's quote).
const needTextMax = 240

// runPark is what a waiting run waits on: its kind (a GET /needs reason:
// question | approval | login | project), its words, and since when (unix
// ms; 0 unknown).
type runPark struct {
	Kind  string
	Text  string
	Since int64
}

// parkOf is run's park in words. Its age is the park's own (pendingState
// since), else the engine's ask step's (the built-in question and approval
// journal one), else the run's last update. Every read goes through d (a
// transaction's handle, in a status hook — never the agent's own).
func (d *DB) parkOf(run *Run) runPark {
	ps := parsePending(run.Pending)
	pk := runPark{Kind: needQuestion, Since: ps.Since}
	switch {
	case ps.Harness != nil: // its name read through d: parkOf runs inside transactions (a status hook)
		name := "The coding agent"
		if cfg, err := d.runConfig(run.ID); err == nil && cfg.Harness != nil {
			name = d.harnessRunName(run.ID, cfg.Harness.Provider)
		}
		pk.Kind, pk.Text, _ = harnessNeedOf(name, ps)
		if ps.Kind == "question" {
			if q := questionWords(ps.Harness); q != "" {
				pk.Text = clip(q, needTextMax)
			}
		}
	case ps.Kind == "approval":
		pk.Kind = needApproval
		pk.Text, _ = approvalWords(ps)
	case ps.Kind == pendKindProject:
		pk.Kind = "project"
		pk.Text = clip(plainText(run.Result), needTextMax)
	default:
		pk.Text = clip(plainText(run.Result), needTextMax)
	}
	if pk.Since == 0 && ps.Harness == nil && ps.Kind != pendKindProject {
		var at int64
		_ = d.q.QueryRow(`SELECT COALESCE(MAX(created), 0) FROM steps WHERE run_id=? AND kind='ask'`, run.ID).Scan(&at)
		pk.Since = at * 1000
	}
	if pk.Since == 0 {
		pk.Since = run.Updated * 1000
	}
	return pk
}

// approvalWords is the built-in engine's approval as a person reads it,
// and its fingerprint (the parked calls).
func approvalWords(ps pendingState) (body, fp string) {
	var names []string
	for _, c := range ps.ToolCalls {
		names = append(names, c.Function.Name)
		fp += c.ID + "\x00"
	}
	body = "Wants to run " + clip(strings.Join(names, ", "), 120) + " — approve or deny."
	if ps.Grant != "" {
		body = "Asks to " + orStr(ps.GrantAsk, grantOf(ps.Grant).askText()) + " — allow or deny."
	}
	return body, fp
}

// questionWords are a coding agent's form question in words: Claude's
// AskUserQuestion says only "Please answer the following questions." —
// its questions are the schema's choice fields (their description, else
// title; the "Other" boxes left out). "" when it has none: the message
// says it (an MCP server's form).
func questionWords(h *hPark) string {
	if h == nil || len(h.Schema) == 0 {
		return ""
	}
	var s struct {
		Properties map[string]struct {
			Title       string          `json:"title"`
			Description string          `json:"description"`
			OneOf       json.RawMessage `json:"oneOf"`
			Enum        json.RawMessage `json:"enum"`
			Items       *struct {
				AnyOf json.RawMessage `json:"anyOf"`
				Enum  json.RawMessage `json:"enum"`
			} `json:"items"`
		} `json:"properties"`
	}
	if json.Unmarshal(h.Schema, &s) != nil {
		return ""
	}
	keys := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys) // question_0, question_1, … (a map keeps no order)
	var qs []string
	for _, k := range keys {
		f := s.Properties[k]
		choice := len(f.OneOf) > 0 || len(f.Enum) > 0 || (f.Items != nil && (len(f.Items.AnyOf) > 0 || len(f.Items.Enum) > 0))
		if q := plainText(orStr(f.Description, f.Title)); choice && q != "" {
			qs = append(qs, q)
		}
	}
	return strings.Join(qs, " · ")
}
