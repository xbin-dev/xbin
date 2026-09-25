// subagent_tools.go — the model's side of delegation (links.go is the
// machinery):
//
//	subagent_spawn   start a subagent; wait for its answer (default) or not
//	subagent_wait    wait for some of your subagents (all/any, with a timeout)
//	subagent_status  what they are doing right now
//	subagent_result  one subagent's full answer
//	subagent_message send a running (or finished) subagent more instructions
//	subagent_cancel  stop subagents you no longer need
//
// Node ids are run ids, and every id resolves inside the caller's OWN subtree
// and lane — the boundary that keeps one run from reading another's work.
// The pre-rewrite names (spawn_subagent, workflow_*) still execute, because
// models imitate their own history; they are just no longer advertised.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func idsProp(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

var subagentAliases = map[string]string{
	"spawn_subagent":  "subagent_spawn",
	"workflow_spawn":  "subagent_spawn",
	"workflow_status": "subagent_status",
	"workflow_result": "subagent_result",
	"workflow_cancel": "subagent_cancel",
}

func isSubagentTool(name string) bool {
	switch name {
	case "subagent_spawn", "subagent_wait", "subagent_status", "subagent_result", "subagent_message", "subagent_cancel":
		return true
	}
	_, ok := subagentAliases[name]
	return ok
}

// subagentToolSpecs is the advertised set: none at the depth limit (a leaf
// cannot delegate, and leaves are the most numerous runs in a fan-out).
func subagentToolSpecs(cfg Config, depth int) []toolSpec {
	if !cfg.Subagents || depth >= cfg.maxDepth() {
		return nil
	}
	bg := cfg.feature("workflow")
	spawnProps := map[string]any{
		"task":      strProp("the complete, self-contained task — the subagent starts BLANK: it cannot see this conversation, your memory or your files, so include every id, name, date and constraint, and the shape and size of answer you want back"),
		"label":     strProp("short name shown in the session, e.g. 'vendor pricing'"),
		"timeout_s": intProp(fmt.Sprintf("how long to wait for the answer before it moves to the background (default %d, 30–3600)", cfg.subagentTimeout())),
		"system":    strProp("optional system-prompt override for the subagent; it cannot change the capability lane"),
	}
	desc := "Start a subagent on a focused task in its own fresh context and wait for its answer. Emit several in one step to run them in parallel. " +
		"If it takes longer than timeout_s it moves to the background: you get a progress digest now and its answer later as a message."
	if bg {
		spawnProps["wait"] = boolProp("true (default): wait for the answer. false: start it in the background and continue; its answer arrives as a message when it finishes")
		spawnProps["after"] = idsProp("subagent ids that must finish first; this one starts after them and receives their results (implies wait:false)")
		desc += " With wait:false it runs in the background while you keep working; use after:[ids] to chain subagents."
	}
	specs := []toolSpec{
		{Type: "function", Function: funcDef{Name: "subagent_spawn", Description: desc, Parameters: obj([]string{"task"}, spawnProps)}},
		{Type: "function", Function: funcDef{
			Name: "subagent_wait",
			Description: "Wait for subagents you started (background ones, or ones that moved to the background). Returns the answers of those that finished and a progress digest for those still working. " +
				"timeout_s 0 just checks. A message from the owner ends the wait early.",
			Parameters: obj([]string{"ids"}, map[string]any{
				"ids":       idsProp("subagent ids to wait for"),
				"mode":      strProp("all (default): until every one finished · any: until the first one finishes"),
				"timeout_s": intProp("longest to wait (default 300, max 3600)"),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name:        "subagent_status",
			Description: "What your subagents are doing right now: phase, time, model calls, their last few tool calls and latest text. Does not wait.",
			Parameters: obj(nil, map[string]any{
				"ids":    idsProp("limit to these ids (default: all of yours)"),
				"detail": boolProp("include recent activity (default: true for up to 3 ids)"),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name:        "subagent_result",
			Description: "Read one subagent's FULL answer (delivered answers are clipped). Pass offset/limit to read a line range of a long one.",
			Parameters: obj([]string{"id"}, map[string]any{
				"id":     intProp("subagent id"),
				"offset": intProp("1-based first line (default 1)"),
				"limit":  intProp("lines to return (default: to the end)"),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name:        "subagent_cancel",
			Description: "Stop subagents you no longer need (and everything they started). Do this before you answer if work is still in flight.",
			Parameters: obj([]string{"ids"}, map[string]any{
				"ids":    idsProp("subagent ids to stop"),
				"reason": strProp("short reason, shown on the stopped run"),
			}),
		}},
	}
	if bg {
		specs = append(specs, toolSpec{Type: "function", Function: funcDef{
			Name: "subagent_message",
			Description: "Send one of your subagents more instructions. A working subagent reads it at its next step; a finished one starts working again on it. " +
				"wait:true waits for its next answer.",
			Parameters: obj([]string{"id", "text"}, map[string]any{
				"id":        intProp("subagent id"),
				"text":      strProp("the message"),
				"wait":      boolProp("wait for its next answer (default false: it arrives as a message)"),
				"timeout_s": intProp("with wait: longest to wait before it moves to the background"),
			}),
		}})
	}
	return specs
}

// subagentTool executes one subagent call; its placeholder is settled here,
// or moved to a waiting state the pass resolves.
func (e *Engine) subagentTool(ctx context.Context, ts *turnState, tc toolCall) {
	name := tc.Function.Name
	args, _ := callArgs(tc, ts.own)
	if canon, ok := subagentAliases[name]; ok {
		switch name {
		case "spawn_subagent":
			args["wait"] = true
		case "workflow_spawn":
			args["wait"] = false
		}
		name = canon
	}
	cfg := ts.cfg
	var out string
	var err error
	switch {
	case !cfg.Subagents:
		err = fmt.Errorf("subagents are turned off for this agent")
	case ts.run.Depth >= cfg.maxDepth() && (name == "subagent_spawn" || name == "subagent_message"):
		err = fmt.Errorf("you are at the delegation depth limit (%d) — do the work yourself", cfg.maxDepth())
	default:
		switch name {
		case "subagent_spawn":
			out, err = e.spawn(ts, tc, args)
		case "subagent_wait":
			out, err = e.waitTool(ts, tc, args)
		case "subagent_status":
			out, err = e.statusTool(ts, args)
		case "subagent_result":
			out, err = e.resultTool(ts, args)
		case "subagent_message":
			out, err = e.messageTool(ts, tc, args)
		case "subagent_cancel":
			out, err = e.cancelTool(ts, args)
		}
	}
	if err != nil {
		out = "error: " + err.Error()
	}
	if out != "" {
		e.settleResult(ts, tc, out)
	}
}

// node resolves a model-supplied id to a run inside the caller's own subtree
// and lane. direct additionally requires the caller to be its parent.
func (e *Engine) node(caller *Run, id int64, direct bool) (*Run, error) {
	if id == caller.ID {
		return nil, fmt.Errorf("#%d is this run, not one of your subagents", id)
	}
	n, err := e.db.getRun(id)
	if err != nil {
		return nil, fmt.Errorf("no subagent #%d — subagent_status lists yours", id)
	}
	inside := false
	for p := n; p != nil && p.ParentID != 0; {
		if p.ParentID == caller.ID {
			inside = true
			break
		}
		if p, err = e.db.getRun(p.ParentID); err != nil {
			break
		}
	}
	if !inside {
		return nil, fmt.Errorf("#%d is not one of your subagents — subagent_status lists the ones that are", id)
	}
	if direct && n.ParentID != caller.ID {
		return nil, fmt.Errorf("#%d was started by one of your subagents, not by you — ask that one", id)
	}
	if nc, err := e.db.runConfig(n.ID); err == nil {
		if cc, err2 := e.db.runConfig(caller.ID); err2 == nil && nc.toolset() != cc.toolset() {
			return nil, fmt.Errorf("#%d is in a different capability lane", id)
		}
	}
	return n, nil
}

// subagentTimeoutFloor is the shortest foreground wait a model may ask for
// (a var so tests can wait one second instead of thirty).
var subagentTimeoutFloor = 30

func clampTimeout(v, def, lo, hi int) int {
	if lo > subagentTimeoutFloor {
		lo = subagentTimeoutFloor
	}
	if v <= 0 {
		v = def
	}
	if v < lo {
		v = lo
	}
	if v > hi {
		v = hi
	}
	return v
}

// spawn starts a child. Foreground: the call's placeholder waits for it.
// Background (or after:[…]): answered at once with the new id.
func (e *Engine) spawn(ts *turnState, tc toolCall, args map[string]any) (string, error) {
	run, cfg := ts.run, ts.cfg
	task := strings.TrimSpace(str(args["task"]))
	if task == "" {
		return "", fmt.Errorf("task is required")
	}
	if e.halted() {
		return "", fmt.Errorf("the owner has halted this agent — no new subagents until they resume it")
	}
	if ts.spawnedThisStep >= cfg.maxSpawnTurn() {
		return "", fmt.Errorf("at most %d subagents per step (maxSpawnPerTurn) — wait for these first", cfg.maxSpawnTurn())
	}
	var after []*Run
	for _, v := range toInt64Slice(args["after"]) {
		dep, err := e.node(run, v, false)
		if err != nil {
			return "", fmt.Errorf("after: %w", err)
		}
		after = append(after, dep)
	}
	wait := args["wait"] != false && len(after) == 0
	if !cfg.feature("workflow") {
		wait, after = true, nil
	}
	timeout := clampTimeout(toInt(args["timeout_s"]), cfg.subagentTimeout(), 30, 3600)
	label := strings.TrimSpace(str(args["label"]))
	title := label
	if title == "" {
		title = "subagent: " + clip(task, 60)
	}
	var childID int64
	err := e.fenced(func(t *DB) error {
		root := ts.root
		ok, spawned, max := t.reserveSpawn(root, cfg.maxSpawn())
		if !ok {
			return fmt.Errorf("this workflow has already created %d runs (its lifetime budget of %d) — finish with what you have, or ask the owner to raise maxSpawn", spawned, max)
		}
		child := childConfig(cfg, str(args["system"]))
		cfgJSON, _ := json.Marshal(child)
		status, pending := statusRunning, ""
		var depLinks []*Link
		for _, d := range after {
			if l := t.latestLink(d.ID); l != nil {
				depLinks = append(depLinks, l)
				if l.State == linkRunning {
					status, pending = statusAwait, `{"kind":"deps"}`
				}
			}
		}
		var err error
		childID, err = t.createRunStatus(title, string(cfgJSON), run.ID, status)
		if err != nil {
			return err
		}
		if pending != "" {
			_, _ = t.q.Exec(`UPDATE runs SET pending=? WHERE id=?`, pending, childID)
		}
		if _, err := t.addMessage(&Message{RunID: childID, Role: "system", Content: child.System}); err != nil {
			return err
		}
		if _, err := t.addMessage(&Message{RunID: childID, Role: "user", Content: task}); err != nil {
			return err
		}
		mode, tcID, deadline := "bg", "", int64(0)
		if wait {
			mode, tcID, deadline = "fg", tc.ID, e.unix()+int64(timeout)
		}
		var linkID int64
		if err := t.q.QueryRow(`INSERT INTO links (parent_id, child_id, tool_call_id, mode, deadline, label, created)
			VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`, run.ID, childID, tcID, mode, deadline, label, e.unix()).Scan(&linkID); err != nil {
			return err
		}
		for _, l := range depLinks {
			_, _ = t.q.Exec(`INSERT OR IGNORE INTO link_deps (child_id, dep_link, dep_run) VALUES (?, ?, ?)`, childID, l.ID, l.ChildID)
		}
		if status == statusRunning && len(depLinks) > 0 {
			// Every dependency already finished: hand over their results now.
			_, _ = t.q.Exec(`UPDATE runs SET status=?, pending='{"kind":"deps"}' WHERE id=?`, statusAwait, childID)
		}
		e.emitStep(t, root, t.journal(run.ID, "spawn", map[string]any{
			"runId": childID, "linkId": linkID, "toolCallId": tc.ID, "task": clip(task, 200),
			"mode": mode, "after": idsOf(after),
		}))
		if wait {
			if ok, _ := t.setToolPlaceholder(run.ID, tc.ID, fmt.Sprintf("(waiting for subagent #%d…)", childID)); ok {
				if id, _, err := t.toolResultRow(run.ID, tc.ID); err == nil {
					e.emitMessageID(t, root, run.ID, id)
				}
			}
		}
		e.emitRun(t, childID)
		e.emitLink(t, root, linkID)
		t.AfterCommit(func() { e.Poke(childID) })
		return nil
	})
	if err != nil {
		return "", err
	}
	ts.spawnedThisStep++
	if wait {
		return "", nil // the placeholder waits; resolveAwait answers it
	}
	msg := fmt.Sprintf("started subagent #%d in the background; its answer will arrive as a message when it finishes", childID)
	if len(after) > 0 {
		msg += fmt.Sprintf(" (it starts after %s finish, with their results)", idList(idsOf(after)))
	}
	return msg, nil
}

// waitTool registers a subagent_wait — or answers at once when there is
// nothing to wait for (or timeout_s is 0).
func (e *Engine) waitTool(ts *turnState, tc toolCall, args map[string]any) (string, error) {
	var linkIDs []int64
	for _, v := range toInt64Slice(args["ids"]) {
		n, err := e.node(ts.run, v, true)
		if err != nil {
			return "", err
		}
		if l := e.db.latestLink(n.ID); l != nil && l.ParentID == ts.run.ID {
			linkIDs = append(linkIDs, l.ID)
		}
	}
	if len(linkIDs) == 0 {
		return "", fmt.Errorf("give the ids of subagents you started (subagent_status lists them)")
	}
	need := "all"
	if strings.EqualFold(str(args["mode"]), "any") {
		need = "any"
	}
	timeout := 300
	if v, ok := args["timeout_s"]; ok {
		timeout = toInt(v)
	}
	if timeout > 3600 {
		timeout = 3600
	}
	ls := e.db.queryLinksIn(linkIDs)
	if timeout <= 0 || waitDone(ls, need) {
		var out string
		err := e.fenced(func(t *DB) error { out = e.waitResult(t, ls); return nil })
		return out, err
	}
	w := waitEntry{TC: tc.ID, Links: linkIDs, Need: need, Deadline: e.unix() + int64(timeout)}
	ids := make([]string, len(ls))
	for i, l := range ls {
		ids[i] = fmt.Sprintf("#%d", l.ChildID)
	}
	err := e.fenced(func(t *DB) error {
		if ok, _ := t.setToolPlaceholder(ts.run.ID, tc.ID, "(waiting for "+strings.Join(ids, ", ")+"…)"); ok {
			if id, _, err := t.toolResultRow(ts.run.ID, tc.ID); err == nil {
				e.emitMessageID(t, ts.root, ts.run.ID, id)
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	ts.waits = append(ts.waits, w)
	return "", nil
}

func (e *Engine) statusTool(ts *turnState, args map[string]any) (string, error) {
	var kids []*Run
	if ids := toInt64Slice(args["ids"]); len(ids) > 0 {
		for _, id := range ids {
			n, err := e.node(ts.run, id, false)
			if err != nil {
				return "", err
			}
			kids = append(kids, n)
		}
	} else {
		kids, _ = e.db.queryRuns(`WHERE parent_id=? ORDER BY id`, ts.run.ID)
	}
	if len(kids) == 0 {
		return "no subagents", nil
	}
	detail := len(kids) <= 3
	if v, ok := args["detail"].(bool); ok {
		detail = v
	}
	var parts []string
	_ = e.fenced(func(t *DB) error {
		for _, k := range kids {
			parts = append(parts, e.childDigest(t, k.ID, detail))
		}
		return nil
	})
	return strings.Join(parts, "\n"), nil
}

func (e *Engine) resultTool(ts *turnState, args map[string]any) (string, error) {
	n, err := e.node(ts.run, int64(toInt(args["id"])), false)
	if err != nil {
		return "", err
	}
	l := e.db.latestLink(n.ID)
	if l == nil || l.State == linkRunning {
		var d string
		_ = e.fenced(func(t *DB) error { d = e.childDigest(t, n.ID, true); return nil })
		return "still working — no answer yet\n" + d, nil
	}
	body := l.Result
	if strings.TrimSpace(body) == "" {
		body = n.Result
	}
	return sliceLines(body, toInt(args["offset"]), toInt(args["limit"])), nil
}

// messageTool steers a working subagent, or sets a finished one to work
// again (a new link, so its next answer comes back).
func (e *Engine) messageTool(ts *turnState, tc toolCall, args map[string]any) (string, error) {
	n, err := e.node(ts.run, int64(toInt(args["id"])), true)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(str(args["text"]))
	if text == "" {
		return "", fmt.Errorf("text is required")
	}
	wait := args["wait"] == true
	timeout := clampTimeout(toInt(args["timeout_s"]), ts.cfg.subagentTimeout(), 30, 3600)
	open := e.db.latestLink(n.ID)
	working := open != nil && open.State == linkRunning
	var reply string
	err = e.fenced(func(t *DB) error {
		if _, _, err := t.enqueue(n.ID, inboxUser, inboxBody{Text: text, Source: "parent", From: ts.run.ID}, ""); err != nil {
			return err
		}
		e.emitInbox(t, ts.root, n.ID)
		switch {
		case working && !wait:
			reply = fmt.Sprintf("sent to #%d — it reads it at its next step", n.ID)
		case working && wait:
			ts.waits = append(ts.waits, waitEntry{TC: tc.ID, Links: []int64{open.ID}, Need: "all", Deadline: e.unix() + int64(timeout)})
			_, _ = t.setToolPlaceholder(ts.run.ID, tc.ID, fmt.Sprintf("(waiting for #%d…)", n.ID))
		default:
			mode, tcID, deadline := "bg", "", int64(0)
			if wait {
				mode, tcID, deadline = "fg", tc.ID, e.unix()+int64(timeout)
			}
			var linkID int64
			if err := t.q.QueryRow(`INSERT INTO links (parent_id, child_id, tool_call_id, mode, deadline, label, created)
				VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`, ts.run.ID, n.ID, tcID, mode, deadline, n.Title, e.unix()).Scan(&linkID); err != nil {
				return err
			}
			e.emitLink(t, ts.root, linkID)
			if wait {
				_, _ = t.setToolPlaceholder(ts.run.ID, tc.ID, fmt.Sprintf("(waiting for subagent #%d…)", n.ID))
			} else {
				reply = fmt.Sprintf("#%d is working on it; its answer will arrive as a message", n.ID)
			}
		}
		t.AfterCommit(func() { e.Poke(n.ID) })
		return nil
	})
	return reply, err
}

func (e *Engine) cancelTool(ts *turnState, args map[string]any) (string, error) {
	var stopped []int64
	for _, v := range toInt64Slice(args["ids"]) {
		n, err := e.node(ts.run, v, false)
		if err != nil {
			return "", err
		}
		_ = e.db.Tx(func(t *DB) error {
			stopped = append(stopped, e.ag.cancelRuns(t, n.ID, true, str(args["reason"]))...)
			return nil
		})
	}
	if len(stopped) == 0 {
		return "nothing to cancel — they had already finished", nil
	}
	return fmt.Sprintf("stopped %s", idList(stopped)), nil
}

// --- helpers --------------------------------------------------------------------------

func idList(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("#%d", id)
	}
	return strings.Join(parts, ", ")
}

func idsOf(runs []*Run) []int64 {
	out := make([]int64, len(runs))
	for i, r := range runs {
		out[i] = r.ID
	}
	return out
}

func toInt64Slice(v any) []int64 {
	raw, ok := v.([]any)
	if !ok {
		if n := toInt(v); n != 0 {
			return []int64{int64(n)}
		}
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

func decodeCalls(raw string, calls *[]toolCall) bool {
	return raw != "" && json.Unmarshal([]byte(raw), calls) == nil
}
