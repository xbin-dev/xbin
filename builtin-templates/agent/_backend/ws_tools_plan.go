// ws_tools_plan.go — plan_propose: multi-step work as ONE proposal
// (coord_workspace.go; docs/workspace-agent.md §Plans; xbind's
// docs/protocol/delegation.md §Plans). "Make a tool and give it to a new
// assistant" is two tiles, two bindings and a tile agent: filed as single
// requests that is a sheet a step, each waiting for the one before. A plan
// is one change set — xbind writes every line, with the tiles earlier
// lines create taken as made — which the person reviews once and applies
// in one go.
//
// This backend files it and tells the model it waits; when xbind says the
// person decided (ws_notify.go) it reads each line's result, takes the
// tile agents the plan started as its own to follow, and tells the
// conversation what was applied, what wasn't and what still waits for an
// admin. Nothing here applies anything.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

var wsPlanKinds = append([]string{"tile.create", "agent.start"}, wsChangeKinds...)

func wsPlanTools() []coordTool {
	return []coordTool{
		{Write: true, Spec: toolSpec{Type: "function", Function: funcDef{Name: "plan_propose",
			Description: "Propose multi-step work as ONE plan your person reviews once: create tiles, bind their slots, start a tile agent, and any admin change. " +
				"Use it whenever an outcome takes more than one change that creates or binds something — never a chain of single requests, which is a sheet a step. " +
				"Nothing is applied: xbin writes what each line does and its warnings; your person ticks lines and applies them in order. Order: a tile, the tile agent that builds it, then what binds to it; a later line names an earlier line's tile by its path. " +
				"A binding to a tile that provides nothing yet is allowed only after the agent.start line in that tile: it waits, and xbin applies it by itself once the tile agent declared the provide. " +
				"A line that takes an admin's rights (the workspace's LLM connector, a grant) starts unticked for an admin, and for anyone else xbin offers to ask one. " +
				"This returns xbin's description at once; the decision reaches you as a workspace update, with each line's result and the tile agents it started.",
			Parameters: obj([]string{"title", "changes"}, map[string]any{
				"title": strProp("what the plan is for, in a few words (shown as yours)"),
				"changes": map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "items": obj([]string{"kind", "args", "why"}, map[string]any{
					"kind": map[string]any{"type": "string", "enum": wsPlanKinds,
						"description": "tile.create {template, name} — a new tile, your person's (name: \"Dice\" or a path, apps/dice); " +
							"agent.start {tile, goal, done_when, leave_alone?, check_by?, known?} or {tile, template: \"mcp-tool\", tool: {name, arguments, result}} — a tile agent with that brief (tile: an existing tile, or the path of one this plan creates; harness only when your person named the coding agent: it is theirs to choose); " +
							"binding.set {component, slot, provider} — provider a tile's path, or xbin:llm for the workspace's LLM connector; access.set {tile, user, level} — one person's own level on a tile (user: their account id — account_lookup — or the name your person gave; level none, read, write or terminal: terminal starts unticked); and admin_propose's other kinds. " +
							"A tile that reads another tile's API needs a grant.add {from, target, role} line beside its start: tile_status says when a use is pending"},
					"args": map[string]any{"type": "object", "description": "the kind's arguments"},
					"why":  strProp("the reason for this one line, in plain words (shown as yours)"),
				})},
				"dry_run": wsDryProp(),
			})}},
			Run: wsTool(wsPlanProposeTool)},
	}
}

// wsPlanMeta is what a plan's request row keeps beside xbind's id: the
// tile each agent.start line names, for the tile agents it starts.
type wsPlanMeta struct {
	Plan    bool           `json:"plan"`
	Starts  map[int]string `json:"starts,omitempty"`  // line → tile
	Creates []string       `json:"creates,omitempty"` // the tiles it creates
	Lines   []wsPlanLine   `json:"lines,omitempty"`   // each line's outcome, once the plan is decided
}

// wsPlanLine is one line of a decided plan as the conversation's card
// keeps it: xbind's sentence and what came of it. xbin's review closes
// itself after an Apply in which nothing failed, so this is where the
// outcome stays. Every string is xbind's (the agent's own words about a
// line are not kept here) and cut to a line.
type wsPlanLine struct {
	N          int    `json:"n"`
	Kind       string `json:"kind"`
	Line       string `json:"line"`
	Result     string `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	NeedsAdmin bool   `json:"needsAdmin,omitempty"`
	Blocked    bool   `json:"blocked,omitempty"`
	// Waits: what a waiting line waits for, in xbind's terms ("apps/dice to
	// provide mcp"), and until when (ms).
	Waits   string `json:"waits,omitempty"`
	UntilMs int64  `json:"untilMs,omitempty"`
}

// wsPlanHasWaiting: a line of the decided plan still waits for xbind.
func wsPlanHasWaiting(cs wsChangeSet) bool {
	for _, it := range cs.Items {
		if it.Result == "waiting" {
			return true
		}
	}
	return false
}

// wsPlanWaited is what the conversation is told when a plan's waiting
// lines have all been applied by xbind, or ended (r: the plan's request).
func wsPlanWaited(r *wsReq, cs wsChangeSet) string {
	var meta wsPlanMeta
	_ = json.Unmarshal(r.Body, &meta)
	var b strings.Builder
	applied := false
	for _, l := range meta.Lines {
		if l.Waits == "" {
			continue
		}
		switch l.Result {
		case "applied":
			applied = true
			fmt.Fprintf(&b, "Line %d of the plan, which waited for %s, was applied by xbin now: %s.\n", l.N, l.Waits, wsPlain(l.Line))
		default:
			fmt.Fprintf(&b, "Line %d of the plan, which waited for %s, was NOT applied — %s", l.N, l.Waits, wsResultWords(l.Result))
			if l.Error != "" {
				fmt.Fprintf(&b, " (xbin said: %s)", wsPlain(l.Error))
			}
			b.WriteString(". Say so, and what it means for the outcome; propose it again if it is still wanted.\n")
		}
	}
	if applied {
		b.WriteString("Check the hook-up yourself before you say the outcome is reached (for an assistant's tool: a read of the assistant's GET /tools names it), and say what you checked and what you could not.")
	}
	return strings.TrimRight(b.String(), "\n")
}

// wsPlanLines keeps a decided plan's lines with its request row.
func (ag *Agent) wsPlanLines(r *wsReq, cs wsChangeSet) {
	var meta wsPlanMeta
	if json.Unmarshal(r.Body, &meta) != nil || !meta.Plan {
		return
	}
	meta.Lines = meta.Lines[:0]
	for _, it := range cs.Items {
		l := wsPlanLine{N: it.N, Kind: it.Kind, Line: clip(it.Line, 300), Result: it.Result, Error: clip(it.Error, 300),
			NeedsAdmin: it.NeedsAdmin, Blocked: it.Blocked != ""}
		if it.Wait != nil {
			l.Waits = clip(it.Wait.Tile+" to provide "+it.Wait.Service, 200)
			if !it.Wait.Until.IsZero() {
				l.UntilMs = it.Wait.Until.UnixMilli()
			}
		}
		meta.Lines = append(meta.Lines, l)
	}
	body, err := json.Marshal(meta)
	if err != nil {
		return
	}
	if _, err := ag.db.q.Exec(`UPDATE ws_requests SET body=? WHERE id=?`, body, r.ID); err == nil {
		r.Body = body
	}
}

// wsPlanCovers is the open plan of run that already holds this step — a
// creation at path, or a start in tile — or nil. A step of a plan that
// waits for its person is not filed again as a single request: that is the
// chain of sheets the plan is there to replace.
func (ag *Agent) wsPlanCovers(run int64, create, start string) *wsReq {
	for _, r := range ag.db.wsReqs(`WHERE run_id=? AND kind=? AND state IN ('proposed','applying') ORDER BY id`, run, wsReqChangeSet) {
		var meta wsPlanMeta
		if json.Unmarshal(r.Body, &meta) != nil || !meta.Plan {
			continue
		}
		if create != "" && hasStr(meta.Creates, create) {
			return r
		}
		for _, t := range meta.Starts {
			if start != "" && t == start {
				return r
			}
		}
	}
	return nil
}

// wsPlanWaits is what a single request for a step of an open plan answers.
func wsPlanWaits(r *wsReq) string {
	return fmt.Sprintf("That step is a line of the plan you proposed (request %d, %s), which waits for your person's review. Nothing was filed: one plan, one review — don't file its steps one by one. "+
		"You are told their decision as a workspace update; until then end your turn with one line saying the plan waits for them.", r.ID, r.Title)
}

// wsPlanStart turns an agent.start line's arguments — a brief's fields —
// into what xbind is sent: the tile and the brief as the tile agent is
// told it. made: the tiles earlier lines of this plan create.
func wsPlanStart(ctx context.Context, ag *Agent, s *wsSession, args map[string]any, made map[string]string) (map[string]any, string, error) {
	var a struct {
		Tile       string          `json:"tile"`
		Goal       string          `json:"goal"`
		DoneWhen   string          `json:"done_when"`
		LeaveAlone string          `json:"leave_alone"`
		CheckBy    string          `json:"check_by"`
		Known      string          `json:"known"`
		Template   string          `json:"template"`
		Tool       *wsBriefToolAsk `json:"tool"`
		Harness    string          `json:"harness"` // only what the person named: xbind resolves it otherwise
	}
	if err := coordArgs(args, &a); err != nil {
		return nil, "", err
	}
	f := wsBriefFields{Goal: a.Goal, DoneWhen: a.DoneWhen, LeaveAlone: a.LeaveAlone, CheckBy: a.CheckBy, Known: a.Known}
	if err := wsApplyBriefTemplate(a.Template, a.Tool, &f); err != nil {
		return nil, "", err
	}
	tile := strings.Trim(strings.TrimSpace(a.Tile), "/")
	if p, ok := made[strings.ToLower(tile)]; ok {
		tile = p
	} else {
		t, err := wsFindTile(ctx, ag, s, tile)
		if err != nil {
			return nil, "", fmt.Errorf("%w — or name the path of a tile an earlier tile.create line of this plan makes", err)
		}
		tile = t.Path
	}
	f.Goal, f.DoneWhen = strings.TrimSpace(f.Goal), strings.TrimSpace(f.DoneWhen)
	switch {
	case f.Goal == "":
		return nil, "", errors.New("goal: the outcome wanted")
	case f.DoneWhen == "":
		return nil, "", errors.New("done_when: what is true when it is done — something that can be checked")
	}
	for _, v := range []string{f.Goal, f.DoneWhen, f.LeaveAlone, f.CheckBy, f.Known} {
		if len(v) > wsBriefField {
			return nil, "", fmt.Errorf("a field of a brief holds at most %d characters", wsBriefField)
		}
	}
	b := &wsBrief{Tile: tile, Goal: f.Goal, DoneWhen: f.DoneWhen, LeaveAlone: strings.TrimSpace(f.LeaveAlone), CheckBy: strings.TrimSpace(f.CheckBy), Known: strings.TrimSpace(f.Known)}
	line := map[string]any{"tile": tile, "brief": wsBriefText(b, s.Owner)}
	if h := strings.TrimSpace(a.Harness); h != "" {
		line["harness"] = h
	}
	return line, tile, nil
}

func wsPlanProposeTool(ctx context.Context, ag *Agent, s *wsSession, c coordCall) (string, error) {
	var a struct {
		Title   string `json:"title"`
		Changes []struct {
			Kind string         `json:"kind"`
			Args map[string]any `json:"args"`
			Why  string         `json:"why"`
		} `json:"changes"`
		DryRun bool `json:"dry_run"`
	}
	if err := coordArgs(c.Args, &a); err != nil {
		return "", err
	}
	a.Title = strings.TrimSpace(a.Title)
	switch {
	case a.Title == "":
		return "", errors.New("title: what the plan is for")
	case len(a.Changes) == 0 || len(a.Changes) > 20:
		return "", errors.New("changes: one to twenty")
	}
	var tpls []wsTemplate
	made := map[string]string{} // a created tile's name and path (lower case) → its path
	meta := wsPlanMeta{Plan: true, Starts: map[int]string{}}
	items := make([]xbin.Change, 0, len(a.Changes))
	var briefs []string // the briefs a template wrote, as the tile agents will be told them
	for i, ch := range a.Changes {
		n := i + 1
		switch {
		case !hasStr(wsPlanKinds, ch.Kind):
			return "", fmt.Errorf("line %d: no kind %q — a plan's line is one of %s", n, clip(ch.Kind, 40), strings.Join(wsPlanKinds, ", "))
		case strings.TrimSpace(ch.Why) == "":
			return "", fmt.Errorf("line %d has no \"why\": add the reason for this change, in plain words (your person reads it on the review)", n)
		}
		args := any(ch.Args)
		switch ch.Kind {
		case "tile.create":
			if tpls == nil {
				if err := ag.wsWith(ctx, s, func(sid string) (err error) { tpls, err = wsX.Templates(ctx, sid); return }); err != nil {
					return "", err
				}
			}
			id, name := strings.TrimSpace(str(ch.Args["template"])), strings.TrimSpace(orStr(str(ch.Args["name"]), str(ch.Args["path"])))
			known := false
			var list []string
			for _, t := range tpls {
				known = known || t.ID == id
				list = append(list, t.ID)
			}
			switch {
			case !known:
				return "", fmt.Errorf("line %d: there is no template %q (templates: %s)", n, clip(id, 80), strings.Join(list, ", "))
			case name == "":
				return "", fmt.Errorf("line %d: name — what the new tile is called (Dice), or its path (apps/dice)", n)
			}
			path := strings.Trim(name, "/")
			if !strings.Contains(name, "/") {
				path = "apps/" + strings.Trim(wsSlugBad.ReplaceAllString(strings.ToLower(name), "-"), "-")
			}
			made[strings.ToLower(name)], made[strings.ToLower(path)] = path, path
			meta.Creates = append(meta.Creates, path)
			line := map[string]any{"template": id, "path": path}
			if o := strings.TrimSpace(str(ch.Args["owner"])); o != "" {
				line["owner"] = o
			}
			args = line
		case "access.set":
			line, err := wsAccessArgs(ctx, ag, s, ch.Args)
			if err != nil {
				return "", fmt.Errorf("line %d: %w", n, err)
			}
			args = line
		case "agent.start":
			line, tile, err := wsPlanStart(ctx, ag, s, ch.Args, made)
			if err != nil {
				return "", fmt.Errorf("line %d: %w", n, err)
			}
			args, meta.Starts[n] = line, tile
			if strings.TrimSpace(str(ch.Args["template"])) != "" { // a brief it did not write itself: shown to it, below
				briefs = append(briefs, fmt.Sprintf("Line %d's brief, as the template wrote it — what its tile agent is told first, and what your person reads (and may edit) on the review:\n%s\n", n, clip(str(line["brief"]), 2400)))
			}
		default:
			// a tile this plan creates may be named by the name it was given
			for _, k := range []string{"component", "provider", "from", "target", "tile"} {
				if v, ok := ch.Args[k].(string); ok {
					if p, ok := made[strings.ToLower(strings.TrimSpace(v))]; ok {
						ch.Args[k] = p
					}
				}
			}
		}
		items = append(items, xbin.Change{Kind: ch.Kind, Args: args, Why: clip(strings.TrimSpace(ch.Why), 400)})
	}
	if a.DryRun {
		plain := fmt.Sprintf("This would propose %d change(s) to your person as one plan. Nothing would be applied: xbin describes each line, they tick what to apply, and xbin applies those in order as them — the lines that take an admin's rights only for an admin, who ticks them there themselves. Nothing was proposed.", len(items))
		// xbind describes it without proposing it: the lines as its person would see them, and which of them
		// xbind could not apply — so a plan with a dead line is mended before it is in front of anyone
		var cs wsChangeSet
		if err := ag.wsWith(ctx, s, func(sid string) (err error) { cs, err = wsX.DescribePlan(ctx, sid, a.Title, items); return }); err != nil {
			if errors.Is(err, errWsUnavailable) {
				return plain, nil
			}
			return "", err
		}
		cs.ID, cs.State = "(not proposed)", "a dry run"
		return "As xbin would describe it — " + wsChangeSetText(cs) + "\n" + strings.Join(briefs, "") + wsPlanNotes(cs) + plain, nil
	}
	if e := projEng(); e != nil && e.halted() {
		return "", errors.New("this agent is halted: nothing is proposed until it is resumed")
	}
	var cs wsChangeSet
	if err := ag.wsWith(ctx, s, func(sid string) (err error) { cs, err = wsX.ProposePlan(ctx, sid, a.Title, items); return }); err != nil {
		return "", err
	}
	body, _ := json.Marshal(meta)
	_ = ag.db.wsAddReq(&wsReq{Run: s.Run, Kind: wsReqChangeSet, Ref: cs.ID, CallID: toolCallOf(ctx), Body: body,
		Title: fmt.Sprintf("a plan of %d change(s): %s", len(cs.Items), a.Title), State: "proposed"})
	wsArmPoll()
	ag.wsChanged(s.Run, "request")
	return wsChangeSetText(cs) + "\n" + strings.Join(briefs, "") + wsPlanNotes(cs) + "Proposed as one plan; nothing is applied. Your person reviews it in xbin (the lines above are xbin's, and what they see) and applies what they tick, in order. " +
		"Their decision reaches you as a workspace update — don't wait for it here, and don't file any of these steps again as single requests.", nil
}

// wsPlanNotes says what is special about a proposed plan's lines: which
// can't be applied, which are an admin's, which wait for one.
func wsPlanNotes(cs wsChangeSet) string {
	var blocked, admin, needs []string
	waiting := ""
	for _, it := range cs.Items {
		n := fmt.Sprint(it.N)
		if it.Later != "" {
			waiting += fmt.Sprintf("Line %s can't be part of this plan — xbin says: %s. Leave it out now; when the tile agent has reported and you have checked the tile, propose that one line as a plan of its own. ", n, wsPlain(it.Later))
		}
		if it.Wait != nil && it.Blocked == "" {
			waiting += fmt.Sprintf("Line %s waits by design: your person approves it on the review, and xbin applies it by itself once %s provides %s — which the tile agent of an earlier line writes (the brief template \"mcp-tool\" tells it to). ", n, it.Wait.Tile, it.Wait.Service)
		}
		switch {
		case it.Blocked != "":
			blocked = append(blocked, n)
		case it.NeedsAdmin:
			needs = append(needs, n)
		case it.Admin:
			admin = append(admin, n)
		}
	}
	out := waiting
	for _, it := range cs.Items {
		if it.Kind == "agent.start" && it.Blocked == "" {
			// (two real-model conversations read Codex's mode "read-only" as "it can't write", and warned their
			// person the work could not be done: the feedback run, 2026-11-17)
			out += "The mode named in a start line is the coding agent's own word for how it runs, not a limit on what it may change: xbin starts it in one that asks your person before it edits a file or runs a command (Codex calls that \"read-only\"), unless the line carries a warning that says otherwise. "
			break
		}
	}
	if len(blocked) > 0 {
		out += "xbin can't apply line " + strings.Join(blocked, ", ") + " as proposed (its reason is above): say so, or propose a plan without it. "
	}
	if len(admin) > 0 {
		out += "Line " + strings.Join(admin, ", ") + " takes an admin's rights: it starts unticked, your person ticks it on the review themselves and xbin asks them for a recent sign-in. Of such a line you are told only whether it was applied. "
	}
	if len(needs) > 0 {
		out += "Line " + strings.Join(needs, ", ") + " needs an admin, and your person is none: they can apply the rest and send that line to an admin from the review (\"Ask an admin\") — you can't ask for them. Tell them so in one line. "
	}
	return out
}

// wsPlanDecided is what a decided plan adds to its outcome: the tile
// agents it started become this conversation's to follow (r: the plan's
// request), and what still waits is said.
func (ag *Agent) wsPlanDecided(ctx context.Context, s *wsSession, r *wsReq, cs wsChangeSet) string {
	var meta wsPlanMeta
	_ = json.Unmarshal(r.Body, &meta)
	var b strings.Builder
	started, waits, failed := 0, 0, 0
	for _, it := range cs.Items {
		switch {
		case it.Result == "asked" || it.NeedsAdmin && it.Result == "":
			waits++
		case it.Result == "failed" || it.Result == "stale":
			failed++
		}
		if it.Kind != "agent.start" || it.Result != "applied" {
			continue
		}
		var out struct {
			Session string `json:"session"`
		}
		if json.Unmarshal(it.Out, &out) != nil || out.Session == "" {
			continue
		}
		var a wsAgent
		if err := ag.wsWith(ctx, s, func(sid string) (err error) { a, err = wsX.Agent(ctx, sid, out.Session); return }); err != nil {
			a = wsAgent{ID: out.Session, Tile: meta.Starts[it.N], Status: "starting", Asks: true, Mine: true}
		}
		row := ag.wsAddAgent(s, &wsReq{ID: r.ID, Run: r.Run, Tile: orStr(a.Tile, meta.Starts[it.N]), CallID: r.CallID}, a)
		started++
		fmt.Fprintf(&b, "\nLine %d started tile agent #%d in %s (%s, %s) and it was told the brief. It is your person's session: only they answer its questions. You are told when it ends its turn, fails or asks them a question (a permission it asks them for doesn't wake you).",
			it.N, row.ID, row.Tile, orStr(row.Harness, "coding agent"), map[bool]string{true: "asks before acting", false: "acts without asking"}[row.Asks])
	}
	for _, it := range cs.Items {
		if it.Result == "waiting" && it.Wait != nil {
			fmt.Fprintf(&b, "\nLine %d is approved and waits: xbin applies it by itself, as your person, once %s provides %s — an http interface for the role %s in its manifest, which is the tile agent's to write (its brief says so). Nothing is bound until then, a tile that declares anything else is not bound, and the line lapses in 24 hours. You are told when it is applied or ends; until then the outcome isn't reached — don't say it is.",
				it.N, it.Wait.Tile, it.Wait.Service, orStr(it.Wait.Role, "reader"))
		}
	}
	if failed > 0 {
		b.WriteString("\nA line failed, so the lines after it were skipped and the earlier ones stay. Say which, in xbin's words, and what you would propose instead — don't file the skipped steps as single requests.")
	}
	if waits > 0 {
		b.WriteString("\nA line that needs an admin was not applied: your person can send it to an admin from the plan's review (\"Ask an admin\"); admin_changes shows where it stands. Until an admin applies it, what depends on it doesn't work — say so.")
	}
	if started > 0 {
		b.WriteString("\nThe work isn't done until the tile agent reports and you have checked: read its result against done-when, then verify what you can yourself (workspace_map, a tile's status, a read of its API) before you tell your person it is done — and say what you checked and what you didn't.")
	} else if failed == 0 && waits == 0 {
		b.WriteString("\nVerify what you can yourself (workspace_map, a tile's status, a read of its API) before you tell your person it is done.")
	}
	return b.String()
}
