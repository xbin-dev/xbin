// workflow_tools.go — the model-facing surface of the run graph.
//
// Node ids ARE run ids, so the journal, recall, session files and the tile's
// click-through all work on a node for free.
//
// None of these is a sideEffect() tool. sideEffect means "reaches outside this
// agent" (xbin_call, mcp:*); spawning touches nothing outside, and a child's
// own world-touching calls hit the approval gate at their own boundary because
// Approve is inherited. What spawning risks is SPEND, not effect, and spend is
// bounded structurally by the ceiling, the depth limit and the tree budget.
package main

import (
	"context"
	"fmt"
	"strings"
)

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func idsProp(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": desc}
}

func workflowToolSpecs(cfg Config, depth int) []toolSpec {
	if !cfg.Subagents || !cfg.feature("workflow") {
		return nil
	}
	specs := []toolSpec{
		{Type: "function", Function: funcDef{
			Name: "workflow_status",
			Description: "Snapshot of the background runs you started — each with its status, label and result size — without waiting. " +
				"Use it to recover node ids after a compaction, or to decide whether it is worth waiting yet. Results are listed, not included; read one with workflow_result.",
			Parameters: obj(nil, map[string]any{"ids": idsProp("limit the listing to these node ids (default: all of yours)")}),
		}},
		{Type: "function", Function: funcDef{
			Name:        "workflow_result",
			Description: "Read one finished node's FULL result — status and the arrival report both truncate. Pass offset/limit to read a line range of a long result.",
			Parameters: obj([]string{"id"}, map[string]any{
				"id":     intProp("node id"),
				"offset": intProp("1-based first line to return (default 1)"),
				"limit":  intProp("how many lines to return (default: to the end)"),
			}),
		}},
	}
	if depth >= cfg.maxDepth() {
		return specs // a leaf may still look around, but not delegate
	}
	return append(specs,
		toolSpec{Type: "function", Function: funcDef{
			Name: "workflow_cancel",
			Description: "Stop background runs you no longer need — they stop at their next step and report as cancelled, and anything waiting on them is released. " +
				"Do this before you finish if work is still in flight, so it stops spending.",
			Parameters: obj([]string{"ids"}, map[string]any{
				"ids":    idsProp("node ids to cancel"),
				"reason": strProp("short reason, shown on the cancelled run"),
			}),
		}},
		toolSpec{Type: "function", Function: funcDef{
			Name: "workflow_spawn",
			Description: "Start a subagent run in the BACKGROUND and get its node id back immediately — you keep working while it runs. " +
				"Use it for work that takes minutes, for gathering you want to overlap with your own, and whenever one task needs another's output: pass after:[ids] and the dependency's result is handed to the new node automatically. " +
				"For a single lookup whose answer you need before your next thought, use spawn_subagent instead — that one waits for you. " +
				"The child starts BLANK: it cannot see this conversation, your memory blocks or your files, so put every id, name, date and constraint it needs into task, and say what shape of answer you want back. " +
				"It runs in your capability lane and cannot ask the human anything. Collect with workflow_status and workflow_result.",
			Parameters: obj([]string{"task"}, map[string]any{
				"task":   strProp("the complete, self-contained task — everything the child needs, including the output shape and size you want back"),
				"label":  strProp("short name for the run list, e.g. 'vendor pricing'"),
				"after":  idsProp("node ids that must finish first; this node stays blocked until they do, and their results are prepended to its task"),
				"system": strProp("optional system-prompt override for this child; it cannot change the capability lane"),
			}),
		}})
}

var workflowToolNames = map[string]bool{
	"workflow_spawn": true, "workflow_status": true, "workflow_result": true,
	"workflow_cancel": true,
}

// workflowNode resolves a model-supplied node id to a run inside the CALLER's
// own subtree. This is the whole cross-run boundary of the layer: before it, no
// run could see another run's anything, and an unscoped integer would let a
// web-lane run read a private-lane run's result by guessing.
//
// Scope is the whole subtree rather than direct children: a parent owns
// everything it caused, and `after` chains legitimately nest.
func (ag *Agent) workflowNode(caller *Run, id int64) (*Run, error) {
	if id == caller.ID {
		return nil, fmt.Errorf("#%d is this run, not one of your nodes", id)
	}
	n, err := ag.db.getRun(id)
	if err != nil {
		return nil, fmt.Errorf("no node #%d — workflow_status lists yours", id)
	}
	root := caller.RootID
	if root == 0 {
		root = caller.ID
	}
	if n.RootID != root {
		return nil, fmt.Errorf("#%d is not one of your subagent runs — workflow_status lists the ones that are", id)
	}
	// Belt-and-braces: inheritance already makes a lane mismatch impossible, but
	// this is the one place a future bug would be worth catching loudly.
	if cfg, err := ag.db.runConfig(n.ID); err == nil {
		if ccfg, err2 := ag.db.runConfig(caller.ID); err2 == nil && cfg.toolset() != ccfg.toolset() {
			return nil, fmt.Errorf("#%d is in a different capability lane", id)
		}
	}
	return n, nil
}

func (ag *Agent) runWorkflowTool(ctx context.Context, run *Run, cfg Config, name string, args map[string]any) (string, error) {
	switch name {
	case "workflow_spawn":
		task, _ := args["task"].(string)
		system, _ := args["system"].(string)
		label, _ := args["label"].(string)
		var after []int64
		for _, v := range toInt64Slice(args["after"]) {
			dep, err := ag.workflowNode(run, v)
			if err != nil {
				return "", fmt.Errorf("after: %w", err)
			}
			after = append(after, dep.ID)
		}
		id, err := ag.startChild(run, cfg, task, system, label, after, "")
		if err != nil {
			return "", err
		}
		msg := fmt.Sprintf("started node #%d in the background", id)
		if len(after) > 0 {
			msg += fmt.Sprintf(" (blocked until %s finish; their results will be prepended to its task)", idList(after))
		}
		return msg + "\n\n" + ag.workflowIndex(run), nil

	case "workflow_status":
		return ag.workflowIndex(run), nil

	case "workflow_cancel":
		reason, _ := args["reason"].(string)
		var stopped []int64
		for _, v := range toInt64Slice(args["ids"]) {
			n, err := ag.workflowNode(run, v)
			if err != nil {
				return "", err
			}
			stopped = append(stopped, ag.requestCancel(n.ID, true, reason)...)
		}
		if len(stopped) == 0 {
			return "nothing to cancel — those runs had already finished\n\n" + ag.workflowIndex(run), nil
		}
		return fmt.Sprintf("cancelled %s (and anything below them)\n\n%s", idList(stopped), ag.workflowIndex(run)), nil

	case "workflow_result":
		n, err := ag.workflowNode(run, int64(toInt(args["id"])))
		if err != nil {
			return "", err
		}
		if n.SettledAt == 0 {
			return fmt.Sprintf("#%d is still %s — no result yet", n.ID, nodeWord(n)), nil
		}
		return sliceLines(n.Result, toInt(args["offset"]), toInt(args["limit"])), nil
	}
	return "", fmt.Errorf("unknown workflow tool %q", name)
}

// workflowIndex renders the caller's subtree. One formatter, used by spawn's
// receipt and by status, so the model never learns two shapes.
func (ag *Agent) workflowIndex(run *Run) string {
	root := run.RootID
	if root == 0 {
		root = run.ID
	}
	nodes, err := ag.db.treeRuns(root)
	if err != nil || len(nodes) == 0 {
		return "workflow: no background runs"
	}
	var b strings.Builder
	var running, done, blocked, failed int
	var lines []string
	for _, n := range nodes {
		if n.ID == root {
			continue
		}
		switch {
		case n.Outcome == outcomeError || n.Outcome == outcomeCanceled:
			failed++
		case n.SettledAt != 0:
			done++
		case n.Status == statusBlocked:
			blocked++
		default:
			running++
		}
		line := fmt.Sprintf("#%d %-9s %s", n.ID, nodeWord(n), n.Title)
		if n.SettledAt != 0 && n.Result != "" {
			line += fmt.Sprintf("   %s", humanBytes(len(n.Result)))
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "workflow: no background runs"
	}
	fmt.Fprintf(&b, "workflow · %d node(s) · %d running · %d done · %d blocked · %d failed\n",
		len(lines), running, done, blocked, failed)
	b.WriteString(strings.Join(lines, "\n"))
	return b.String()
}

// nodeWord maps a run's internal state onto a small closed vocabulary, each
// word mapping to a different correct reaction. "queued" and "blocked" stay
// split deliberately: blocked means "expected, just wait", queued means "the
// workspace is saturated — maybe spawn fewer".
func nodeWord(n *Run) string {
	if n.Outcome != "" {
		switch n.Outcome {
		case outcomeError:
			return "error"
		case outcomeCanceled:
			return "cancelled"
		case outcomeIncomplete:
			return "incomplete"
		}
		return "done"
	}
	switch n.Status {
	case statusBlocked:
		return "blocked"
	case statusQueued:
		return "queued"
	case statusWaiting:
		return "blocked"
	case statusDone:
		return "done"
	case statusError:
		return "error"
	case statusCanceled:
		return "cancelled"
	}
	return "running"
}

func idList(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("#%d", id)
	}
	return strings.Join(parts, ", ")
}

func toInt64Slice(v any) []int64 {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []int64
	for _, x := range raw {
		if n := toInt(x); n != 0 {
			out = append(out, int64(n))
		}
	}
	return out
}

// treeRuns returns every run in a tree, oldest first. root_id is denormalized
// precisely so this is an index scan rather than a recursive walk.
func (d *DB) treeRuns(rootID int64) ([]*Run, error) {
	rows, err := d.sql.Query(
		`SELECT id FROM runs WHERE root_id=? ORDER BY created, id`, rootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*Run, 0, len(ids))
	for _, id := range ids {
		if r, err := d.getRun(id); err == nil {
			out = append(out, r)
		}
	}
	return out, nil
}
