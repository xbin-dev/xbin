// ws_tools_agents.go — the workspace agent's tile agents (coord_workspace.go;
// docs/workspace-agent.md §Tile agents): coding-agent sessions in a tile,
// opened as the person, which do the building the workspace agent never
// does itself. "Scope, then hand off": tile_agent_brief writes the brief
// its person can edit, tile_agent_start asks for the start — xbind shows
// the person the brief exactly as it will be sent, with the harness and
// mode it will run in, which come from the person's preferences and the
// tile's org's rules and are never the model's to choose.
//
// A started session is the person's own: they see it, open it and answer
// it. The workspace agent may message, read and stop the sessions it
// started — and never answers one: a session that waits for its person (a
// permission request, a question) gets the agent's message only after the
// person answered; until then the message is held, in order (ws_held,
// coord_hold.go's rule).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	wsBriefField = 4000 // one field of a brief
	wsAgentsLive = 6    // tile agents one session keeps live
)

func wsAgentTools() []coordTool {
	agent := strProp("the tile agent: its number as tile_agents lists it (#3)")
	return []coordTool{
		{Spec: toolSpec{Type: "function", Function: funcDef{Name: "tile_agent_brief",
			Description: "Write the brief for a tile agent — a coding agent that works inside one tile — and show it to your person, who can edit it. This starts nothing. " +
				"Use it whenever an outcome needs something built, changed or fixed in a tile: you don't change code, a tile's agent does. " +
				"Write the brief for someone who saw none of this conversation: the outcome wanted (not the steps), how anyone can tell it is done, what must not be touched, how to check, and what you already found out. " +
				"For a tool an agent instance should get (an MCP server in a tile of its own, bound to the instance's mcp slot): template \"mcp-tool\" with tool {name, arguments, result} — it fills done_when, check_by, what is known and what to leave alone. " +
				"Next: tile_agent_start with the brief's number.",
			Parameters: obj([]string{"tile"}, withBriefTemplate(map[string]any{
				"tile":        strProp("the tile's path, as workspace_map lists it"),
				"goal":        strProp("the outcome wanted, in the person's terms"),
				"done_when":   strProp("what is true when it is done — something that can be checked"),
				"leave_alone": strProp("what it must not change (data, other features, the look)"),
				"check_by":    strProp("how it should verify its work (which tests, which request, what to look at)"),
				"known":       strProp("what you already found out that it would otherwise have to rediscover (from research, logs, the person)"),
			}))}},
			Run: wsTool(wsBriefTool)},
		{Write: true, Spec: toolSpec{Type: "function", Function: funcDef{Name: "tile_agent_start",
			Description: "Start a tile agent from a brief (or continue one that stopped, with resume). Your person is asked, unless their preference lets tile agents start without asking (then Start on the brief's card is their go-ahead): xbin shows them the brief exactly as it will be sent, and the coding agent and mode it will run in. This tool answers \"pending\" at once; the session, or their refusal, reaches you as a workspace update. " +
				"What they approve is what xbin sends the session first: they may have edited the brief. You choose neither the coding agent nor its mode: those are your person's preferences and the tile's rules. " +
				"Once started the session is theirs to see and to answer; you are told when it ends a turn, fails, or waits for them.",
			Parameters: obj(nil, map[string]any{
				"brief":   coordIntProp("the brief's number (from tile_agent_brief)"),
				"resume":  strProp("instead of a brief: the number of a tile agent that stopped or failed (#3), to continue it where it was"),
				"dry_run": wsDryProp(),
			})}},
			Run: wsTool(wsStartTool)},
		{Write: true, Spec: toolSpec{Type: "function", Function: funcDef{Name: "tile_agent_message",
			Description: "Send a message to a tile agent you started: a correction, an answer to something it reported, the next piece of work. It takes it at its next step, or as its next turn. Your person approved the brief, not each message: keep within what the brief was for — a tile agent you start runs in an ask or plan mode, which asks them before it acts; one they switched to a mode that doesn't takes no more of your messages. " +
				"If the session waits for your person (a permission request, a question of its own), your message is held until they answered: you never answer for them, and nothing you send counts as their answer. Not for a session you didn't start.",
			Parameters: obj([]string{"agent", "text"}, map[string]any{
				"agent":   agent,
				"text":    strProp("the message, complete in itself"),
				"dry_run": wsDryProp(),
			})}},
			Run: wsTool(wsMessageTool)},
		{Spec: toolSpec{Type: "function", Function: funcDef{Name: "tile_agent_status",
			Description: "Where one tile agent stands now: starting, working, waiting for your person (with what it asks), ended its turn, failed or stopped — and its last line. A read. You are told of changes anyway; use this when you need the state at this moment.",
			Parameters:  obj([]string{"agent"}, map[string]any{"agent": agent})}},
			Run: wsTool(wsAgentStatusTool)},
		{Spec: toolSpec{Type: "function", Function: funcDef{Name: "tile_agent_result",
			Description: "What a tile agent's last turn left: its final answer, the files it changed as a count of lines (never the code), and the commands it ran to check with how each ended. A read. " +
				"This is the agent's own report — check it against done-when before you tell your person the work is done, and say what you checked and what you didn't.",
			Parameters: obj([]string{"agent"}, map[string]any{"agent": agent})}},
			Run: wsTool(wsAgentResultTool)},
		{Write: true, Spec: toolSpec{Type: "function", Function: funcDef{Name: "tile_agent_cancel",
			Description: "Stop the turn a tile agent you started is running. What it already changed stays; the session stays open and its transcript is kept. " +
				"Use it when your person asks, or when it is plainly working on the wrong thing — say so to them afterwards.",
			Parameters: obj([]string{"agent"}, map[string]any{"agent": agent, "dry_run": wsDryProp()})}},
			Run: wsTool(wsAgentCancelTool)},
		{Spec: toolSpec{Type: "function", Function: funcDef{Name: "tile_agents",
			Description: "The tile agents this session started and where each stands, with the briefs that have not been started. A read.",
			Parameters:  obj(nil, map[string]any{})}},
			Run: wsTool(wsAgentsTool)},
	}
}

// --- briefs --------------------------------------------------------------------------

// wsBriefText is the brief as the tile agent is told it: the objective,
// what is known, the boundaries, how to verify, and the report wanted.
func wsBriefText(b *wsBrief, person string) string {
	var s strings.Builder
	fmt.Fprintf(&s, "This is a piece of work in this tile, handed to you by %s's workspace agent on their behalf.\n\n", orStr(person, "your person"))
	fmt.Fprintf(&s, "Goal: %s\n", strings.TrimSpace(b.Goal))
	fmt.Fprintf(&s, "Done when: %s\n", strings.TrimSpace(b.DoneWhen))
	if v := strings.TrimSpace(b.LeaveAlone); v != "" {
		fmt.Fprintf(&s, "Leave alone: %s\n", v)
	}
	if v := strings.TrimSpace(b.CheckBy); v != "" {
		fmt.Fprintf(&s, "Check by: %s\n", v)
	}
	if v := strings.TrimSpace(b.Known); v != "" {
		fmt.Fprintf(&s, "Already known: %s\n", v)
	}
	s.WriteString("\nWork inside this tile only. When you are done, answer with: what you changed (each file in a sentence), what you ran to check it and how it ended, and anything you could not do or could not verify. " +
		"If you need a decision or a permission, ask: your questions go to " + orStr(person, "the person") + ", not to the workspace agent.")
	return s.String()
}

func wsBriefWords(b *wsBrief) string {
	out := fmt.Sprintf("Brief %d for %s (%s):\n- Goal: %s\n- Done when: %s", b.ID, b.Tile, b.State, b.Goal, b.DoneWhen)
	if b.LeaveAlone != "" {
		out += "\n- Leave alone: " + b.LeaveAlone
	}
	if b.CheckBy != "" {
		out += "\n- Check by: " + b.CheckBy
	}
	return out
}

func wsBriefTool(ctx context.Context, ag *Agent, s *wsSession, c coordCall) (string, error) {
	var a struct {
		Tile       string          `json:"tile"`
		Goal       string          `json:"goal"`
		DoneWhen   string          `json:"done_when"`
		LeaveAlone string          `json:"leave_alone"`
		CheckBy    string          `json:"check_by"`
		Known      string          `json:"known"`
		Template   string          `json:"template"` // a brief template's id (ws_brief_kinds.go)
		Tool       *wsBriefToolAsk `json:"tool"`
	}
	if err := coordArgs(c.Args, &a); err != nil {
		return "", err
	}
	f := wsBriefFields{Goal: a.Goal, DoneWhen: a.DoneWhen, LeaveAlone: a.LeaveAlone, CheckBy: a.CheckBy, Known: a.Known}
	if err := wsApplyBriefTemplate(a.Template, a.Tool, &f); err != nil {
		return "", err
	}
	a.Goal, a.DoneWhen, a.LeaveAlone, a.CheckBy, a.Known = f.Goal, f.DoneWhen, f.LeaveAlone, f.CheckBy, f.Known
	t, err := wsFindTile(ctx, ag, s, a.Tile)
	if err != nil {
		return "", err
	}
	a.Goal, a.DoneWhen = strings.TrimSpace(a.Goal), strings.TrimSpace(a.DoneWhen)
	switch {
	case a.Goal == "":
		return "", errors.New("goal: the outcome wanted")
	case a.DoneWhen == "":
		return "", errors.New("done_when: what is true when it is done — something the tile agent, you and your person can check")
	}
	for _, f := range []string{a.Goal, a.DoneWhen, a.LeaveAlone, a.CheckBy, a.Known} {
		if len(f) > wsBriefField {
			return "", fmt.Errorf("a field of a brief holds at most %d characters: put the detail in a notebook page and name it", wsBriefField)
		}
	}
	b := &wsBrief{Run: s.Run, Tile: t.Path, Goal: a.Goal, DoneWhen: a.DoneWhen, LeaveAlone: strings.TrimSpace(a.LeaveAlone),
		CheckBy: strings.TrimSpace(a.CheckBy), Known: strings.TrimSpace(a.Known), State: "draft", CallID: toolCallOf(ctx), Created: nowMs()}
	err = ag.db.Tx(func(tx *DB) error {
		// a newer brief for the tile replaces drafts nobody started
		if _, err := tx.q.Exec(`UPDATE ws_briefs SET state='replaced', updated_ms=? WHERE run_id=? AND tile=? AND state='draft'`, nowMs(), s.Run, t.Path); err != nil {
			return err
		}
		return tx.q.QueryRow(`INSERT INTO ws_briefs (run_id, tile, goal, done_when, leave_alone, check_by, known, state, call_id, created_ms, updated_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'draft', ?, ?, ?) RETURNING id`, s.Run, b.Tile, b.Goal, b.DoneWhen, b.LeaveAlone, b.CheckBy, b.Known, b.CallID, b.Created, b.Created).Scan(&b.ID)
	})
	if err != nil {
		return "", err
	}
	ag.wsChanged(s.Run, "brief")
	on := ""
	if w := ag.wsRunsOn(ctx, s, t.Path, ""); w != "" {
		on = " It would run on " + w + " (your person's preferences and the tile's rules; they can pick another the tile allows)."
	}
	return fmt.Sprintf("Brief %d for %s is in front of your person, who can edit it. Nothing has started.%s "+
		"Next: tile_agent_start {\"brief\": %d} — xbin shows them the brief as it will be sent and asks them to allow the start. "+
		"If they asked you to change the brief instead, write a new one.", b.ID, t.name(), on, b.ID), nil
}

// --- starting ------------------------------------------------------------------------

// wsStartOf is the start a brief asks for: the tile, the brief as the
// session's first prompt, and the harness only when its person picked one.
func wsStartOf(b *wsBrief, person string) wsAgentStart {
	return wsAgentStart{Tile: b.Tile, Harness: b.Harness, Prompt: wsBriefText(b, person), Name: clip(oneLineTitle(b.Goal), 60)}
}

// wsStartSummary is the agent's own line beside a start's request. The
// brief itself is in the request: xbind shows it as what the session is
// told first.
func wsStartSummary(a wsAgentStart, b *wsBrief) string {
	if a.Resume != "" || b == nil {
		return "Continue the tile agent in " + a.Tile + " where it stopped."
	}
	return clip(b.Goal, 600)
}

// wsRunsOn is what a start in tile would run on, in a clause ("Claude
// Code, asks before acting"), by xbind's defaults and the person's pick;
// "" when xbind doesn't say.
func (ag *Agent) wsRunsOn(ctx context.Context, s *wsSession, tile, picked string) string {
	var d wsStartDefaults
	if err := ag.wsWith(ctx, s, func(sid string) (err error) { d, err = wsX.StartDefaults(ctx, sid, tile); return }); err != nil {
		return ""
	}
	id := orStr(picked, d.Harness)
	if id == "" {
		return ""
	}
	h, _ := d.of(id)
	on := orStr(h.Name, id) + ", " + map[bool]string{true: "asks before acting", false: "acts without asking"}[h.Asks]
	if h.NoSignIn {
		on += ", on the workspace's model with no sign-in"
	}
	switch h.SignIn { // xbind's word on the sign-in (D230): never more than that
	case "saved":
		on += ", on a saved sign-in of the person's if they approve the start (xbin's approval names it)"
	case "needed":
		on += " — it needs the person's sign-in first: the brief's card has the button, the agent can't sign in for them"
	}
	return on
}

func wsStartTool(ctx context.Context, ag *Agent, s *wsSession, c0 coordCall) (string, error) {
	args := c0.Args
	var start wsAgentStart
	var brief *wsBrief
	var title string
	if name := strings.TrimSpace(fmt.Sprint(orAny(args["resume"], ""))); name != "" {
		old, err := ag.db.wsAgentOf(s.Run, name)
		if err != nil {
			return "", fmt.Errorf("there is no tile agent %s in this session (tile_agents lists them)", clip(name, 40))
		}
		if old.live() {
			return "", fmt.Errorf("tile agent #%d is still open (%s): message it instead of continuing it", old.ID, old.Status)
		}
		start = wsAgentStart{Tile: old.Tile, Resume: old.Session}
		title = fmt.Sprintf("continue tile agent #%d in %s", old.ID, old.Tile)
	} else {
		id := int64(toInt(args["brief"]))
		b, err := ag.db.wsBrief(s.Run, id)
		if err != nil {
			return "", errors.New("brief: the number of a brief of this session (tile_agent_brief writes one; tile_agents lists them)")
		}
		switch b.State {
		case "started":
			return "", fmt.Errorf("brief %d was started already (tile_agents lists its tile agent): write a new brief for new work", b.ID)
		case "asked":
			return "", fmt.Errorf("brief %d already waits for your person's decision: don't ask twice", b.ID)
		case "replaced":
			return "", fmt.Errorf("brief %d was replaced by a newer one for %s (tile_agents lists them)", b.ID, b.Tile)
		}
		brief, start = b, wsStartOf(b, s.Owner)
		title = fmt.Sprintf("start a tile agent in %s", b.Tile)
		if r := ag.wsPlanCovers(s.Run, "", b.Tile); r != nil && args["dry_run"] != true {
			return wsPlanWaits(r), nil
		}
	}
	if n := ag.db.wsOne(`SELECT count(*) FROM ws_agents WHERE run_id=? AND status IN ('starting','running','idle')`, s.Run); n >= wsAgentsLive {
		return "", fmt.Errorf("this session already has %d tile agents open (at most %d): let one finish, or message one of them", n, wsAgentsLive)
	}
	var dry *wsDry
	if err := ag.wsWith(ctx, s, func(sid string) (err error) { _, dry, err = wsX.AgentStart(ctx, sid, start, "", true); return }); err != nil {
		return "", err
	}
	if args["dry_run"] == true || dry == nil || !dry.Allowed {
		ag.wsDryRefused(ctx, s, dry, start.Tile)
		out := wsDryWords("To "+title, dry)
		if dry != nil && dry.Allowed {
			if on := ag.wsRunsOn(ctx, s, start.Tile, start.Harness); on != "" {
				out += " It would run on " + on + "."
			}
			out += " The coding agent, its mode and its network are your person's preferences and the tile's rules; its questions and permission requests go to your person, never to you. The approval is of the brief: what you message it later is not approved message by message — its mode is what asks. What it changes in the tile stays until someone changes it back."
		}
		return out, nil
	}
	body, _ := json.Marshal(start)
	req := &wsReq{Kind: wsReqSession, Tile: start.Tile, Title: title, Method: "POST", Path: "/api/xbin/term/sessions", Body: body}
	var ask func(string) (wsRequest, error)
	if dry.NeedsApproval {
		ask = func(sid string) (wsRequest, error) {
			return wsX.AskAgentStart(ctx, sid, start, wsStartSummary(start, brief))
		}
	} else if brief != nil {
		// their preferences let a tile agent start without xbin asking: the
		// brief is then the one confirmation — Start on the brief's card
		req.Ref = fmt.Sprintf("brief:%d:%d", s.Run, brief.ID)
	} else {
		req.Ref = fmt.Sprintf("resume:%d:%s:%d", s.Run, start.Resume, nowMs())
	}
	if brief != nil {
		req.Brief = brief.ID
	}
	out, err := wsFile(ctx, ag, s, req, ask)
	if err != nil {
		return "", err
	}
	if brief != nil {
		_, _ = ag.db.q.Exec(`UPDATE ws_briefs SET state='asked', updated_ms=? WHERE id=?`, nowMs(), brief.ID)
	}
	if ask == nil && brief == nil { // a resume their preferences let through: nothing to confirm
		go ag.wsDecideByID(req.ID)
	}
	ag.wsChanged(s.Run, "request")
	return out, nil
}

func orAny(v, def any) any {
	if v == nil {
		return def
	}
	return v
}

// --- steering and reading ------------------------------------------------------------

// wsLiveAgent is run's tile agent name, as it is now: the row brought up
// to date from xbind when it may still change.
func (ag *Agent) wsLiveAgent(ctx context.Context, s *wsSession, name string) (*wsAgentRow, error) {
	row, err := ag.db.wsAgentOf(s.Run, name)
	if err != nil {
		return nil, fmt.Errorf("there is no tile agent %s in this session: you steer and read only the ones this session started (tile_agents lists them)", clip(strings.TrimSpace(name), 40))
	}
	if !row.live() {
		return row, nil
	}
	var a wsAgent
	if err := ag.wsWith(ctx, s, func(sid string) (err error) { a, err = wsX.Agent(ctx, sid, row.Session); return }); err != nil {
		return nil, err
	}
	if ag.wsSeen(ctx, s, row, a, false) {
		ag.wsWake(s.Run) // read from inside a turn: its next step takes it
	}
	return ag.db.wsAgentOf(s.Run, name)
}

func wsStatusWords(a *wsAgentRow) string {
	who := fmt.Sprintf("Tile agent #%d in %s (%s", a.ID, a.Tile, orStr(a.Harness, "coding agent"))
	if a.Mode != "" {
		who += ", " + map[bool]string{true: "asks before acting", false: "acts without asking"}[a.Asks]
	}
	who += ")"
	switch {
	case a.Waits && a.live():
		return who + " " + wsWaitsWords(a) + " Only they can answer."
	case a.Status == "idle":
		return who + " ended its turn" + wsLast(a) + ". tile_agent_result has its report."
	case a.Status == "running":
		return who + " is working" + wsLast(a) + "."
	}
	return who + " — " + a.Status + wsLast(a) + "."
}

func wsMessageTool(ctx context.Context, ag *Agent, s *wsSession, c coordCall) (string, error) {
	text := strings.TrimSpace(str(c.Args["text"]))
	switch {
	case text == "":
		return "", errors.New("text: the message")
	case len(text) > 32<<10:
		return "", errors.New("text: at most 32 KiB — put the detail in a notebook page and name it")
	}
	row, err := ag.wsLiveAgent(ctx, s, fmt.Sprint(orAny(c.Args["agent"], "")))
	if err != nil {
		return "", err
	}
	if !row.live() {
		return "", fmt.Errorf("tile agent #%d is %s: it takes no message. tile_agent_start with resume continues it (your person is asked)", row.ID, row.Status)
	}
	if c.Args["dry_run"] == true {
		if row.Waits {
			return fmt.Sprintf("This would be held, not delivered: tile agent #%d %s It would go in after they answered. Nothing was sent.", row.ID, wsWaitsWords(row)), nil
		}
		return fmt.Sprintf("This would be delivered to tile agent #%d in %s now: it takes it at its next step, or starts a turn on it. It steers work your person already approved, so nobody is asked; what the tile agent then changes stays. Nothing was sent.", row.ID, row.Tile), nil
	}
	if row.Waits { // the never-answers rule: what waits for a person is theirs
		if n := ag.db.wsOne(`SELECT count(*) FROM ws_held WHERE agent_id=?`, row.ID); n >= 8 {
			return "", fmt.Errorf("%d of your messages to tile agent #%d are already held behind your person's answer: wait for them", n, row.ID)
		}
		if _, err := ag.db.q.Exec(`INSERT INTO ws_held (agent_id, text, created_ms) VALUES (?, ?, ?)`, row.ID, text, nowMs()); err != nil {
			return "", err
		}
		return fmt.Sprintf("Held, not delivered: tile agent #%d %s Your message goes in after they answered — it is not their answer, and you can't give one. Tell your person it waits for them.", row.ID, wsWaitsWords(row)), nil
	}
	var outcome string
	if err := ag.wsWith(ctx, s, func(sid string) (err error) { outcome, err = wsX.AgentSay(ctx, sid, row.Session, text); return }); err != nil {
		return "", err
	}
	switch outcome {
	case "steered":
		return fmt.Sprintf("Delivered to tile agent #%d: it takes it at its next step.", row.ID), nil
	case "queued":
		return fmt.Sprintf("Tile agent #%d is in the middle of a turn: your message goes in when that turn ends.", row.ID), nil
	}
	return fmt.Sprintf("Delivered to tile agent #%d: it starts a turn on it. You are told when it ends.", row.ID), nil
}

func wsAgentStatusTool(ctx context.Context, ag *Agent, s *wsSession, c coordCall) (string, error) {
	row, err := ag.wsLiveAgent(ctx, s, fmt.Sprint(orAny(c.Args["agent"], "")))
	if err != nil {
		return "", err
	}
	out := wsStatusWords(row)
	if _, held := ag.db.wsHeld(row.ID); len(held) > 0 {
		out += fmt.Sprintf(" %d of your messages are held behind your person's answer.", len(held))
	}
	return out, nil
}

func wsAgentResultTool(ctx context.Context, ag *Agent, s *wsSession, c coordCall) (string, error) {
	row, err := ag.wsLiveAgent(ctx, s, fmt.Sprint(orAny(c.Args["agent"], "")))
	if err != nil {
		return "", err
	}
	if row.Status == "starting" || (row.Status == "running" && !row.Waits) {
		return wsStatusWords(row) + " It has no result yet: you are told when its turn ends.", nil
	}
	var rep wsAgentReport
	if err := ag.wsWith(ctx, s, func(sid string) (err error) { rep, err = wsX.AgentReport(ctx, sid, row.Session); return }); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(wsStatusWords(row) + "\n")
	if row.Status == "stopped" {
		b.WriteString("An xbin restart cut it off before it finished. What follows is what it had written by then; tile_agent_start with resume continues it.\n")
	} else if !row.live() && rep.Status == "running" {
		// xbind doesn't say why a session ended: one that ended inside a turn was cut off
		b.WriteString("Its log ends in the middle of a turn: it was cut off, not finished. What follows is what it had written by then; tile_agent_start with resume continues it.\n")
	}
	if rep.Answer == "" {
		b.WriteString("It left no final answer.\n")
	} else {
		b.WriteString(wsData("tile agent #"+fmt.Sprint(row.ID)+" in "+row.Tile, "its own report of its last turn — check it, don't take it as proof", clip(rep.Answer, 6000)) + "\n")
	}
	if len(rep.Files) == 0 {
		b.WriteString("Files changed: none reported.\n")
	} else {
		fmt.Fprintf(&b, "Files changed (%d):\n", len(rep.Files))
		for i, f := range rep.Files {
			if i == 40 {
				fmt.Fprintf(&b, "… and %d more\n", len(rep.Files)-40)
				break
			}
			fmt.Fprintf(&b, "- %s (+%d −%d)\n", wsPlain(f.Path), f.Add, f.Del)
		}
	}
	if len(rep.Verified) == 0 {
		b.WriteString("Commands it ran to check: none recorded — nothing here shows the work was verified.")
	} else {
		b.WriteString("Commands it ran:\n")
		for _, v := range rep.Verified {
			b.WriteString("- " + wsPlain(v) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func wsAgentCancelTool(ctx context.Context, ag *Agent, s *wsSession, c coordCall) (string, error) {
	row, err := ag.wsLiveAgent(ctx, s, fmt.Sprint(orAny(c.Args["agent"], "")))
	if err != nil {
		return "", err
	}
	if c.Args["dry_run"] == true {
		if row.Status != "running" {
			return fmt.Sprintf("Tile agent #%d is %s: there is no turn to stop. Nothing was done.", row.ID, row.Status), nil
		}
		return fmt.Sprintf("This would stop the turn tile agent #%d is running in %s. What it already changed stays, its session stays open and its transcript is kept; a permission request it has open is withdrawn. Nobody is asked. Nothing was done.", row.ID, row.Tile), nil
	}
	if row.Status != "running" {
		return fmt.Sprintf("Tile agent #%d is %s: there is no turn to stop.", row.ID, row.Status), nil
	}
	if err := ag.wsWith(ctx, s, func(sid string) error { return wsX.AgentCancel(ctx, sid, row.Session) }); err != nil {
		return "", err
	}
	_, _ = ag.db.q.Exec(`DELETE FROM ws_held WHERE agent_id=?`, row.ID)
	return fmt.Sprintf("Stopped the turn of tile agent #%d. What it already changed stays; tile_agent_result shows what it did so far. Tell your person you stopped it and why.", row.ID), nil
}

func wsAgentsTool(ctx context.Context, ag *Agent, s *wsSession, c coordCall) (string, error) {
	ag.wsReconcileSession(ctx, s, false)
	rows := ag.db.wsAgents(`WHERE run_id=? ORDER BY id`, s.Run)
	var b strings.Builder
	if len(rows) == 0 {
		b.WriteString("This session has started no tile agent.")
	}
	for _, a := range rows {
		b.WriteString(wsStatusWords(a) + "\n")
	}
	for _, br := range ag.db.wsBriefs(s.Run) {
		if br.State == "draft" || br.State == "asked" {
			note := "not started"
			if br.State == "asked" {
				note = "waits for your person's decision"
			}
			fmt.Fprintf(&b, "\n%s\n(%s%s)", wsBriefWords(br), note, map[bool]string{true: "; your person edited it", false: ""}[br.Edited])
		}
	}
	return strings.TrimSpace(b.String()), nil
}
