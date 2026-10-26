// judge.go — did a coding agent's or a project's turn deliver? (D198)
//
// A coding agent (ACP, D147) or a project's task that ends its turn cleanly
// may still have stopped part-way: it made a plan and yielded, answered one
// question and stopped, said what it would do next. The run rests (idle) and
// nothing says it needs a person. At such a turn end a cheap model — the
// memory tier, title.go's way: once, in the background, never ahead of a
// person's model call, its cost on the run — reads the ask and the turn's
// final answer and says whether the turn delivered. One that didn't marks
// its conversation "not finished": the row's glyph, a Needs-you item (reason
// "unfinished"), a push — until a new turn starts, or someone who may talk
// to it dismisses the mark (POST /runs/{id}/unfinished/dismiss; for
// everyone, as answering a question is).
//
// Judged: top-level runs only (a subagent's answer is its parent's to
// read), of a coding agent (engine harness) or of a project (origin
// project), whose turn was answered and that rests after it. Bounded: the
// last ask and the final answer, judgeClip each; judgeTimeout; one
// judgement in flight per run; none while the background slot is busy (the
// next turn end judges again), without a configured memory tier, or with
// the run's `judge` feature off.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const (
	judgeClip      = 2000
	judgeTimeout   = 60 * time.Second
	needUnfinished = "unfinished" // GET /needs reason and push kind
)

const judgePrompt = "You check whether an AI agent's turn delivered what it was asked for. " +
	"You see the person's last request and the agent's final message of that turn. " +
	"Answer delivered=false when the agent stopped part-way: it only made a plan, lists steps it has not done, " +
	"says it will continue or asks to continue, asks the person a question or for a decision, " +
	"or reports a blocker or failure it did not resolve. " +
	"Answer delivered=true when the requested work is done or the question is fully answered. " +
	`Reply with JSON only: {"delivered": true|false, "reason": "<at most 15 words, for the person>"}`

const judgeSchema = `CREATE TABLE IF NOT EXISTS run_verdicts (
  run_id INTEGER PRIMARY KEY, delivered INTEGER NOT NULL, reason TEXT NOT NULL DEFAULT '',
  judged_ms INTEGER NOT NULL, dismissed_by TEXT NOT NULL DEFAULT '', dismissed_ms INTEGER NOT NULL DEFAULT 0)`

func init() {
	schemaAdds = append(schemaAdds, func(d *DB) error { _, err := d.q.Exec(judgeSchema); return err })
	runStatusHooks = append(runStatusHooks, judgeStatusChanged)
}

// turnVerdict is a run's last judgement.
type turnVerdict struct {
	Delivered             bool
	Reason, DismissedBy   string
	JudgedMs, DismissedMs int64
}

// shown: the mark is up — not delivered, not dismissed.
func (v *turnVerdict) shown() bool { return v != nil && !v.Delivered && v.DismissedMs == 0 }

func (d *DB) verdictOf(runID int64) *turnVerdict {
	var v turnVerdict
	var del int
	if d.q.QueryRow(`SELECT delivered, reason, judged_ms, dismissed_by, dismissed_ms FROM run_verdicts WHERE run_id=?`, runID).
		Scan(&del, &v.Reason, &v.JudgedMs, &v.DismissedBy, &v.DismissedMs) != nil {
		return nil
	}
	v.Delivered = del != 0
	return &v
}

// withVerdict sets a root's `unfinished` — {reason, at} while the mark is
// up, else null (so a summary on the stream takes it down).
func withVerdict(d *DB, root int64, data map[string]any) map[string]any {
	if v := d.verdictOf(root); v.shown() {
		data["unfinished"] = map[string]any{"reason": v.Reason, "at": v.JudgedMs}
	} else {
		data["unfinished"] = nil
	}
	return data
}

// judgeStatusChanged (runStatusHooks): a new turn starting takes the last
// verdict down — it spoke of a turn that is no longer the last.
func judgeStatusChanged(t *DB, runID int64, status string) {
	switch status {
	case statusRunning, statusQueued:
		_, _ = t.q.Exec(`DELETE FROM run_verdicts WHERE run_id=?`, runID)
	}
}

// judgeTurn, at a turn end (endTurnTx, endHarnessTurnTx): a top-level coding
// agent's or project's answered turn is judged once it committed.
func (e *Engine) judgeTurn(t *DB, run *Run, why string) {
	if run.ParentID != 0 || why != turnAnswered || (run.Engine != engineHarness && run.Origin != originProject) {
		return
	}
	t.AfterCommit(func() { e.maybeJudge(run.ID) })
}

func (e *Engine) maybeJudge(runID int64) {
	e.titleMu.Lock()
	if e.judging == nil {
		e.judging = map[int64]bool{}
	}
	if e.judging[runID] {
		e.titleMu.Unlock()
		return
	}
	e.judging[runID] = true
	e.titleMu.Unlock()
	go func() {
		defer func() { e.titleMu.Lock(); delete(e.judging, runID); e.titleMu.Unlock() }()
		e.judge(runID)
	}()
}

// memoryConfigured: the memory tier names a model (the config's, or
// llm-gw's preference for summarizing) — not only modelFor's last resort.
func memoryConfigured(ctx context.Context, cfg Config) bool {
	return firstNonEmpty(cfg.Models.Memory, cfg.Models.General, cfg.Model) != "" || preferredModel(ctx, "summarizing") != ""
}

func (e *Engine) judge(runID int64) {
	run, err := e.db.getRun(runID)
	if err != nil || run.ParentID != 0 || run.Status != statusIdle {
		return // moved on (a new turn, a park, an error): nothing to judge
	}
	cfg, err := e.db.runConfig(runID)
	if err != nil || !cfg.feature("judge") {
		return
	}
	ctx, cancel := context.WithTimeout(e.base, judgeTimeout)
	defer cancel()
	ctx = e.ag.personalCtx(ctx, run) // iface_personal.go
	if !memoryConfigured(ctx, cfg) {
		return
	}
	ask, answer := e.db.judgeInputs(run)
	if strings.TrimSpace(answer) == "" {
		return
	}
	release := e.tryBackgroundLLM()
	if release == nil {
		return // busy: the next turn end judges again
	}
	defer release()
	reply, err := e.llm.Chat(ctx, LLMRequest{
		Run: runID, Purpose: "judge", Model: modelFor(ctx, cfg, "memory"), Wire: cfg.Wire,
		Msgs: []wireMsg{{Role: "system", Content: judgePrompt},
			{Role: "user", Content: "The person's last request:\n" + clip(ask, judgeClip) + "\n\nThe agent's final message:\n" + clip(answer, judgeClip)}},
	}, nil)
	if err != nil {
		return
	}
	delivered, reason, ok := parseVerdict(asString(reply.Msg.Content))
	if !ok {
		return
	}
	shown := false
	_ = e.fenced(func(t *DB) error {
		cur, err := t.getRun(runID)
		if err != nil || cur.Status != statusIdle || cur.ActivityMs != run.ActivityMs {
			return err // a turn since: this verdict speaks of an older one
		}
		del := 0
		if delivered {
			del = 1
		}
		if _, err := t.q.Exec(`INSERT INTO run_verdicts (run_id, delivered, reason, judged_ms) VALUES (?, ?, ?, ?)
			ON CONFLICT(run_id) DO UPDATE SET delivered=excluded.delivered, reason=excluded.reason, judged_ms=excluded.judged_ms,
			  dismissed_by='', dismissed_ms=0`, runID, del, reason, time.Now().UnixMilli()); err != nil {
			return err
		}
		t.addRunCost(runID, reply.Usage.PromptTokens, reply.Usage.CompletionTokens)
		e.emitRun(t, runID)
		shown = !delivered
		return nil
	})
	if shown {
		e.ag.needsMoment(runID)
	}
}

// judgeInputs: the person's last request (the newest user row) and the
// turn's final answer (a coding agent's: harnessTurnText; else the last
// assistant text).
func (d *DB) judgeInputs(run *Run) (ask, answer string) {
	_ = d.q.QueryRow(`SELECT content FROM messages WHERE run_id=? AND role='user' ORDER BY id DESC LIMIT 1`, run.ID).Scan(&ask)
	if run.Engine == engineHarness {
		answer = d.harnessTurnText(run.ID)
		if answer == "(no answer)" {
			answer = ""
		}
	} else {
		answer = d.lastAssistant(run.ID)
	}
	return ask, answer
}

// parseVerdict reads the judge's reply: the JSON object in it (a model may
// wrap it in a fence or a sentence), else a leading yes/no. The reason is
// one line, at most 160 characters.
func parseVerdict(s string) (delivered bool, reason string, ok bool) {
	s = strings.TrimSpace(s)
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		var v struct {
			Delivered *bool  `json:"delivered"`
			Reason    string `json:"reason"`
		}
		if json.Unmarshal([]byte(s[i:j+1]), &v) == nil && v.Delivered != nil {
			return *v.Delivered, cleanReason(v.Reason), true
		}
	}
	l := strings.ToLower(s)
	switch {
	case strings.HasPrefix(l, "yes"), strings.HasPrefix(l, "delivered"):
		return true, "", true
	case strings.HasPrefix(l, "no"):
		return false, cleanReason(strings.TrimLeft(s[2:], " .,:-—")), true
	}
	return false, "", false
}

func cleanReason(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return clip(strings.Trim(s, "\"'`"), 160)
}

// handleDismissUnfinished: POST /runs/{id}/unfinished/dismiss (needParticipant)
// — the conversation's "not finished" mark goes, for everyone, as an
// answered question does.
func handleDismissUnfinished(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	run, err := agent.db.getRun(pathID(r))
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	root := rootOf(run)
	by := orStr(c.user, "system")
	err = agent.eng.fenced(func(t *DB) error {
		if _, err := t.q.Exec(`UPDATE run_verdicts SET dismissed_by=?, dismissed_ms=? WHERE run_id=? AND dismissed_ms=0`,
			by, time.Now().UnixMilli(), root); err != nil {
			return err
		}
		agent.eng.emitRun(t, root)
		return nil
	})
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]any{"ok": true})
}
