// coord_research.go — a coordinator's research subagents (D222), whatever
// its profile (coord_profile.go): a coordinator has no reach into code of
// its own, so it asks a subagent that has — one whose config is built by
// the profile, never from the call's arguments: a CodeReadBinding
// (coord_reader.go) and a base prompt. The kit makes the run through the
// subagent machinery (it waits as a subagent does, or answers later as a
// message), holds it to its tools (runCodeReadTool) and to the profile's
// limits: so many at once, each with so long to answer — after that its
// tools refuse. Its class and lane are its parent's (childConfig), so it
// gains nothing the coordinator lacks but the code its binding names.
// Research children don't count against the coordinator tree's lifetime
// spawn budget (they can't spawn).
//
// A profile's research tool is its own (its arguments name what the
// profile coordinates); it resolves the coordinator, decides the binding
// and calls researchSpawn.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// researchQMax is the most a research question holds (bytes).
const researchQMax = 8 << 10

// researchStart is one research subagent to start, as its profile decided
// it.
type researchStart struct {
	Question string           // checked: researchQuestion
	Label    string           // a short name for it (the call's)
	Wait     bool             // the call waits for its answer
	Binding  *CodeReadBinding // what it reads
	System   string           // its base prompt
	// Limit is how many run at once in Scope ("this project": the words of
	// the refusal), Running how many do now — asked again in the
	// transaction that makes one; Timeout is its time to answer.
	Limit   int
	Scope   string
	Running func(t *DB) int
	Timeout time.Duration
	// Config is the profile's last word on the child's config, after the
	// kit took its subagents, sandbox, coding agent and optional features
	// away (nil: none).
	Config func(child *Config)
}

// researchQuestion is a research call's question, trimmed, or why it won't
// do.
func researchQuestion(q string) (string, error) {
	q = strings.TrimSpace(q)
	switch {
	case q == "":
		return "", errors.New("question: what to find out")
	case len(q) > researchQMax:
		return "", fmt.Errorf("question: at most %d bytes", researchQMax)
	}
	return q, nil
}

// researchRoom refuses a research subagent while s.Limit of them run in
// s.Scope (hint: the refusal says what to do instead).
func researchRoom(t *DB, s researchStart, hint bool) error {
	n := s.Running(t)
	if n < s.Limit {
		return nil
	}
	if hint {
		return fmt.Errorf("%d research subagents of %s are running (at most %d): wait for one (subagent_wait), or ask one of them more (subagent_message)", n, s.Scope, s.Limit)
	}
	return fmt.Errorf("%d research subagents of %s are running (at most %d)", n, s.Scope, s.Limit)
}

// researchTimeoutOf is how long research subagent run has to answer: its
// coordinator's profile's limit (a subagent carries its root's origin).
func researchTimeoutOf(run *Run) time.Duration {
	if p := coordProfiles.byOrigin[run.Origin]; p != nil && p.Limits.ResearchTimeout > 0 {
		return p.Limits.ResearchTimeout
	}
	return 10 * time.Minute
}

// researchSpawn makes s's research subagent as a child of the turn's run
// and answers what the call says now: "" while it waits (its placeholder
// stands until resolveAwait answers it), else that it started.
func (e *Engine) researchSpawn(ts *turnState, tc toolCall, s researchStart) (string, error) {
	run, cfg := ts.run, ts.cfg
	q := s.Question
	label := clip(oneLineTitle(s.Label), 80)
	title := "research: " + orStr(label, clip(oneLineTitle(q), 60))
	child := childConfig(cfg, s.System)
	child.CodeRead = s.Binding
	child.Subagents, child.Sandbox, child.Attached, child.Engine, child.Harness = false, nil, nil, "", nil
	if child.Features == nil {
		child.Features = map[string]bool{}
	}
	for _, f := range []string{"skills", "files", "repl", "workflow", "watcher", "threads"} {
		child.Features[f] = false
	}
	if s.Config != nil {
		s.Config(&child)
	}
	var childID int64
	err := e.fenced(func(t *DB) error {
		if err := researchRoom(t, s, false); err != nil { // again, in the transaction
			return err
		}
		cfgJSON, _ := json.Marshal(child)
		var err error
		if childID, err = t.createRunStamped(title, string(cfgJSON), run.ID, statusRunning, runStamp{}); err != nil {
			return err
		}
		if _, err := t.addMessage(&Message{RunID: childID, Role: "system", Content: child.System}); err != nil {
			return err
		}
		tm := &Message{RunID: childID, Role: "user", Content: q}
		if _, err := t.addMessage(tm); err != nil {
			return err
		}
		if err := t.recordAsk(tm, "parent", fmt.Sprintf("#%d", run.ID)); err != nil {
			return err
		}
		mode, tcID, deadline := "bg", "", int64(0)
		if s.Wait {
			mode, tcID, deadline = "fg", tc.ID, e.unix()+int64(s.Timeout/time.Second)+30
		}
		var linkID int64
		if err := t.q.QueryRow(`INSERT INTO links (parent_id, child_id, tool_call_id, mode, deadline, label, created)
			VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`, run.ID, childID, tcID, mode, deadline, orStr(label, "research"), e.unix()).Scan(&linkID); err != nil {
			return err
		}
		e.emitStep(t, ts.root, t.journal(run.ID, "spawn", map[string]any{"runId": childID, "linkId": linkID, "toolCallId": tc.ID,
			"task": clip(q, 200), "mode": mode, "research": true}))
		if s.Wait {
			if ok, _ := t.setToolPlaceholder(run.ID, tc.ID, fmt.Sprintf("(waiting for research subagent #%d…)", childID)); ok {
				if id, _, err := t.toolResultRow(run.ID, tc.ID); err == nil {
					e.emitMessageID(t, ts.root, run.ID, id)
				}
			}
		}
		e.emitRun(t, childID)
		e.emitLink(t, ts.root, linkID)
		t.AfterCommit(func() { e.Poke(childID) })
		return nil
	})
	if err != nil {
		return "", err
	}
	if s.Wait {
		return "", nil // the placeholder waits; resolveAwait answers it
	}
	return fmt.Sprintf("started research subagent #%d in the background; its answer arrives as a message when it is done", childID), nil
}

// researchTool is a profile's research tool as execTools runs it: f does
// the call (through researchSpawn) and this settles what it answers.
func researchTool(f func(e *Engine, ctx context.Context, ts *turnState, tc toolCall, args map[string]any) (string, error)) func(*Engine, context.Context, *turnState, toolCall) {
	return func(e *Engine, ctx context.Context, ts *turnState, tc toolCall) {
		args, _ := callArgs(tc, ts.own)
		out, err := f(e, ctx, ts, tc, args)
		if err != nil {
			out = "error: " + err.Error()
		}
		if out != "" {
			e.settleResult(ts, tc, out)
		}
	}
}
