// transcript.go — keeping a run's transcript valid for the provider.
//
// The contract is strict: every assistant message carrying tool_calls must
// be followed IMMEDIATELY by one tool message per call id, and a tool message
// may appear nowhere else. The engine keeps it true going forward (the
// assistant message and its placeholders are one transaction; user messages
// are only appended at step boundaries; results are compare-and-swapped into
// their placeholders). repairTranscript heals what is already broken in a
// live database — a result lost to a crash, a placeholder a dead process left
// "running", and the ordering corruption the old engine could produce (a user
// message inserted between a call and its results, which made every later
// request fail).
package main

import (
	"encoding/json"
	"fmt"
)

// evResync tells a watching tile that a run's transcript was rewritten in a
// way it should re-read rather than patch (a repair).
const evResync = "resync"

// repairTranscript puts a run's transcript into canonical order and answers
// every call exactly once. Returns how many things it fixed. Idempotent: a
// valid transcript is left untouched.
func (e *Engine) repairTranscript(runID int64) int {
	msgs, err := e.db.messages(runID, false)
	if err != nil || len(msgs) == 0 {
		return 0
	}
	run, err := e.db.getRun(runID)
	if err != nil {
		return 0
	}
	// Placeholders that are legitimately unsettled: calls parked for approval,
	// foreground subagents still being waited for, subagent_wait calls.
	legit := map[string]bool{}
	p := parsePending(run.Pending)
	for _, tc := range p.ToolCalls {
		legit[tc.ID] = true
	}
	for _, w := range p.Waits {
		legit[w.TC] = true
	}
	for _, l := range e.db.queryLinks(`WHERE parent_id=? AND mode='fg' AND delivered=0`, runID) {
		legit[l.ToolCallID] = true
	}

	byCall := map[string][]*Message{}
	for _, m := range msgs {
		if m.Role == "tool" {
			byCall[m.ToolCallID] = append(byCall[m.ToolCallID], m)
		}
	}
	used := map[int64]bool{}
	type missing struct {
		after int // index in order after which it goes
		call  toolCall
	}
	var order []int64
	var miss []missing
	var rewrite []*Message
	for _, m := range msgs {
		if m.Role == "tool" {
			continue
		}
		order = append(order, m.ID)
		if m.Role != "assistant" || m.ToolCalls == "" {
			continue
		}
		var calls []toolCall
		if json.Unmarshal([]byte(m.ToolCalls), &calls) != nil {
			continue
		}
		for _, c := range calls {
			if c.ID == "" {
				continue
			}
			var pick *Message
			for _, r := range byCall[c.ID] {
				if used[r.ID] {
					continue
				}
				if pick == nil || (isPlaceholder(pick.Content) && !isPlaceholder(r.Content)) {
					pick = r
				}
			}
			if pick == nil {
				miss = append(miss, missing{after: len(order) - 1, call: c})
				order = append(order, 0) // filled in with the new row's id
				continue
			}
			used[pick.ID] = true
			order = append(order, pick.ID)
			if isPlaceholder(pick.Content) && !legit[c.ID] &&
				!(pick.Content == toolAwaitingApproval && p.Kind == "approval") {
				rewrite = append(rewrite, pick)
			}
		}
	}
	// Tool rows that answer no call (or answer one twice) leave the live
	// window: kept for history, never sent.
	var orphans []*Message
	newOrphans := 0
	for _, m := range msgs {
		if m.Role == "tool" && !used[m.ID] {
			orphans = append(orphans, m)
			if !m.Compacted {
				newOrphans++
			}
		}
	}
	reordered := false
	{
		i := 0
		for _, m := range msgs {
			if m.Role == "tool" && !used[m.ID] {
				continue
			}
			if i >= len(order) || order[i] != m.ID {
				reordered = true
				break
			}
			i++
		}
	}
	if !reordered && len(miss) == 0 && len(rewrite) == 0 && newOrphans == 0 {
		return 0
	}
	fixed := 0
	err = e.fenced(func(t *DB) error {
		for _, ms := range miss {
			nm := &Message{RunID: runID, Role: "tool", Name: ms.call.Function.Name, ToolCallID: ms.call.ID, Content: toolLostToRestart}
			if _, err := t.addMessage(nm); err != nil {
				return err
			}
			for k := ms.after + 1; k < len(order); k++ {
				if order[k] == 0 {
					order[k] = nm.ID
					break
				}
			}
			fixed++
		}
		for _, m := range rewrite {
			if err := t.rewriteMessage(runID, m.ID, toolLostToRestart); err != nil {
				return err
			}
			fixed++
		}
		for _, m := range orphans {
			if !m.Compacted {
				if _, err := t.q.Exec(`UPDATE messages SET compacted=1 WHERE id=?`, m.ID); err != nil {
					return err
				}
				fixed++
			}
			order = append(order, m.ID)
		}
		if reordered || len(miss) > 0 || newOrphans > 0 {
			if err := t.setSeqs(runID, order); err != nil {
				return err
			}
			if reordered {
				fixed++
			}
		}
		e.emitStep(t, rootOf(run), t.journal(runID, "note", map[string]string{
			"text": fmt.Sprintf("repaired the transcript (%d fix(es)): unanswered or misplaced tool results", fixed)}))
		t.AfterCommit(func() { e.hub.publish(&Event{Type: evResync, Run: runID, Root: rootOf(run)}) })
		return nil
	})
	if err != nil {
		return 0
	}
	return fixed
}
