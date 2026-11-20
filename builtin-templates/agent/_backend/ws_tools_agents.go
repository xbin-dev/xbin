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
	"net/http"
	"slices"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
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
				"For a tile's update (updates lists them: a tile made from a template, a builtin update filed as a proposal): template \"update\" with only the tile — the brief is xbin's, word for word. " +
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
			Description: "Start a tile agent from a brief (or continue one that stopped, with resume). Your person is asked (unless their preference lets it start unasked: then Start on the brief's card is their go-ahead): xbin shows the brief exactly as sent, and they pick the coding agent, model and mode there — their pick runs. It answers \"pending\"; what ran, or their refusal, reaches you as a workspace update. " +
				"If a tile agent this conversation started is idle on that tile, the brief goes to IT as a follow-up — one session — unless you pass new: true with a reason they see. A follow-up to a working session is tile_agent_message. " +
				"Pass harness, model or mode only when your person named one: their own choice for the tile and their preference come first (the brief's answer says them), and a mode that acts without asking is never picked by you. " +
				"The session is theirs to see and answer; you are told when it ends a turn, fails, or asks them a question.",
			Parameters: obj(nil, map[string]any{
				"brief":   coordIntProp("the brief's number (from tile_agent_brief)"),
				"resume":  strProp("instead of a brief: the number of a tile agent that stopped or failed (#3), to continue it where it was"),
				"harness": strProp("only when your person named one: the coding agent they asked for (an id: claude, codex, opencode, …) — a suggestion they see; their pick on the approval wins"),
				"model":   strProp("only when your person named one: the model they asked for — a suggestion"),
				"mode":    strProp("only when your person named one: the mode they asked for — a suggestion; one that acts without asking is never picked for them"),
				"new":     map[string]any{"type": "boolean", "description": "true: a new session even where one you started is idle on the tile (say why in reason)"},
				"reason":  strProp("with new: why a new session rather than the one there — shown to your person"),
				"dry_run": wsDryProp(),
			})}},
			Run: wsTool(wsStartTool)},
		{Write: true, Spec: toolSpec{Type: "function", Function: funcDef{Name: "tile_agent_message",
			Description: "Send a message to a tile agent you started: a correction, an answer to something it reported, the next piece of work — the way to follow up in the session that is there. It takes it at its next step, or as its next turn. Your person approved the brief, not each message: keep within what the brief was for — a tile agent in a mode that asks takes your messages as they are; one in a mode that acts without asking takes them only by your person's approval, which this tool asks for (the whole text shown; pending, like a start). " +
				"If the session waits for your person (a permission request, a question of its own), your message is held until they answered: you never answer for them, and nothing you send counts as their answer. Not for a session you didn't start.",
			Parameters: obj([]string{"agent", "text"}, map[string]any{
				"agent":   agent,
				"text":    strProp("the message, complete in itself"),
				"dry_run": wsDryProp(),
			})}},
			Run: wsTool(wsMessageTool)},
		// tile_agent_status: folded into tile_agent_result (D246) — not offered any more (wsSpecs), and still
		// answered, for one release, for a conversation whose history calls it
		{Spec: toolSpec{Type: "function", Function: funcDef{Name: "tile_agent_status",
			Description: "The old name of tile_agent_result, kept for conversations that call it: it answers exactly what tile_agent_result answers.",
			Parameters:  obj([]string{"agent"}, map[string]any{"agent": agent})}},
			Run: wsTool(wsAgentResultTool)},
		{Spec: toolSpec{Type: "function", Function: funcDef{Name: "tile_agent_result",
			Description: "Where a tile agent stands and what it left, in one read: its state now — starting, working, waiting for your person (and on what), ended its turn, failed or stopped — its last line and whether messages of yours are held behind your person's answer; and once a turn ended, its final answer, the files it changed as a count of lines (never the code), and the commands it ran to check with how each ended. A read. " +
				"You are told of changes anyway; use it when you need the state now, and before you tell your person the work is done — the report is the agent's own: check it against done-when, and say what you checked and what you didn't.",
			Parameters: obj([]string{"agent"}, map[string]any{"agent": agent})}},
			Run: wsTool(wsAgentResultTool)},
		{Write: true, Spec: toolSpec{Type: "function", Function: funcDef{Name: "tile_agent_cancel",
			Description: "Stop the turn a tile agent you started is running. What it already changed stays; the session stays open and its transcript is kept. " +
				"Use it when your person asks, or when it is plainly working on the wrong thing — say so to them afterwards.",
			Parameters: obj([]string{"agent"}, map[string]any{"agent": agent, "dry_run": wsDryProp()})}},
			Run: wsTool(wsAgentCancelTool)},
		{Spec: toolSpec{Type: "function", Function: funcDef{Name: "tile_agents",
			Description: "The tile agents this session started and where each stands — working, idle since when, waiting for your person; coding agent, model, mode; its brief in a line — the briefs not started yet, and the sessions your person runs themselves on those tiles (theirs: not yours to drive). With tile, the sessions on that tile. A read: look before you start another.",
			Parameters:  obj(nil, map[string]any{"tile": strProp("a tile: the sessions on it (optional)")})}},
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
	xbinBrief := ""
	if a.Template == wsBriefTemplateUpdate { // xbind's brief for the tile's update, word for word (ws_tools_updates.go)
		t, err := wsFindTile(ctx, ag, s, a.Tile)
		if err != nil {
			return "", err
		}
		if f, err = wsUpdateBriefFor(ctx, ag, s, t.Path); err != nil {
			return "", err
		}
		a.Template, a.Tool, xbinBrief = "", nil, wsBriefTemplateUpdate
	}
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
		CheckBy: strings.TrimSpace(a.CheckBy), Known: strings.TrimSpace(a.Known), State: "draft", CallID: toolCallOf(ctx), Created: nowMs(), Xbin: xbinBrief}
	err = ag.db.Tx(func(tx *DB) error {
		// a newer brief for the tile replaces drafts nobody started
		if _, err := tx.q.Exec(`UPDATE ws_briefs SET state='replaced', updated_ms=? WHERE run_id=? AND tile=? AND state='draft'`, nowMs(), s.Run, t.Path); err != nil {
			return err
		}
		return tx.q.QueryRow(`INSERT INTO ws_briefs (run_id, tile, goal, done_when, leave_alone, check_by, known, state, call_id, created_ms, updated_ms, xbin)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'draft', ?, ?, ?, ?) RETURNING id`, s.Run, b.Tile, b.Goal, b.DoneWhen, b.LeaveAlone, b.CheckBy, b.Known, b.CallID, b.Created, b.Created, b.Xbin).Scan(&b.ID)
	})
	if err != nil {
		return "", err
	}
	ag.wsChanged(s.Run, "brief")
	on := ""
	if w := ag.wsRunsOn(ctx, s, t.Path, ""); w != "" {
		on = " It would run on " + w + " — your person picks on the approval among what the tile allows; pass harness, model or mode to tile_agent_start only when they named one."
	}
	return fmt.Sprintf("Brief %d for %s is in front of your person, who can edit it. Nothing has started.%s "+
		"Next: tile_agent_start {\"brief\": %d} — xbin shows them the brief as it will be sent and asks them to allow the start. "+
		"If they asked you to change the brief instead, write a new one.", b.ID, t.name(), on, b.ID), nil
}

// --- starting ------------------------------------------------------------------------

// wsStartOf is the start a brief asks for: the tile, the brief as the
// session's first prompt, and the harness only when its person picked one.
// xbind's own brief (Xbin) is named, not sent: xbind writes its text, and
// the person's review labels it xbin's (D248 review, finding 7).
func wsStartOf(b *wsBrief, person string) wsAgentStart {
	if b.Xbin != "" {
		return wsAgentStart{Tile: b.Tile, Harness: b.Harness, Brief: b.Xbin, Name: clip(oneLineTitle(b.Goal), 60)}
	}
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
	id, asks, model, from := orStr(picked, d.Harness), false, "", ""
	if id == "" {
		return ""
	}
	h, _ := d.of(id)
	asks = h.Asks
	if c := d.Chosen; c != nil && picked == "" { // what the approval preselects: the person's own choice first (D250)
		id, asks, model = c.Harness, c.Asks, c.Model
		h, _ = d.of(id)
		switch c.From["harness"] {
		case "tile":
			from = " (your person's own choice for this tile)"
		case "preference":
			from = " (your person's preference)"
		}
	}
	on := orStr(h.Name, id) + from
	if model != "" {
		on += ", model " + model
	}
	on += ", " + map[bool]string{true: "asks before acting", false: "acts without asking"}[asks]
	if h.NoSignIn {
		on += ", on the workspace's model with no sign-in"
	}
	switch h.SignIn { // xbind's word on the sign-in (D230): never more than that
	case "saved":
		on += ", on a saved sign-in of the person's if they approve the start (xbin's approval names it)"
	case "needed":
		if h.SignInOffered {
			on += " — it would start signed out: xbin's approval offers the person their saved sign-in as a line they tick, each time (prefer a coding agent the workspace's model serves)"
		} else {
			on += " — it needs the person's sign-in first: the brief's card has the button, the agent can't sign in for them"
		}
	}
	return on
}

func wsStartTool(ctx context.Context, ag *Agent, s *wsSession, c0 coordCall) (string, error) {
	args := c0.Args
	var start wsAgentStart
	var brief *wsBrief
	var title, newWhy string
	var cont *wsAgentRow // the idle tile agent the start continues (D250)
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
			// (a real model, asked to continue a tile agent a restart had cut off, named its brief here, read "write
			// a new brief" and briefed the work anew instead of resuming it: the real-model run, 2026-11-17)
			return "", fmt.Errorf("brief %d was started already (tile_agents lists its tile agent). To continue that tile agent where it stopped — it failed, or a restart cut it off — tile_agent_start {\"resume\": its number}; write a new brief only for new work", b.ID)
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
		// the agent's suggestion, where its person named one (D250): xbin's approval preselects it below their own choices
		start.Harness = orStr(strings.TrimSpace(str(args["harness"])), start.Harness)
		start.Model, start.Mode = strings.TrimSpace(str(args["model"])), strings.TrimSpace(str(args["mode"]))
		// the session that is there: this conversation's own idle tile agent on the tile takes the brief as a
		// follow-up — unless a new one is asked for, with a reason the person sees
		if args["new"] != true {
			if there := ag.wsIdleAgentOn(s.Run, b.Tile); there != nil {
				start.Continue, cont = there.Session, there
				title = fmt.Sprintf("continue tile agent #%d in %s with a follow-up", there.ID, b.Tile)
			}
		} else if why := strings.TrimSpace(str(args["reason"])); why != "" {
			newWhy = clip(why, 200)
		}
	}
	if n := ag.db.wsOne(`SELECT count(*) FROM ws_agents WHERE run_id=? AND status IN ('starting','running','idle')`, s.Run); n >= wsAgentsLive && cont == nil {
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
		summary := wsStartSummary(start, brief)
		if newWhy != "" {
			summary = clip("A new session: "+newWhy+"\n"+summary, 600)
		}
		ask = func(sid string) (wsRequest, error) {
			return wsX.AskAgentStart(ctx, sid, start, summary)
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
	if cont != nil {
		out += fmt.Sprintf(" It continues tile agent #%d (%s), which is idle there: the brief goes to it as a follow-up and no new session opens — unless your person starts a new one on the approval.", cont.ID, wsAgentWords(cont))
	}
	if there := ag.wsSessionsOn(ctx, s, start.Tile, cont); there != "" {
		out += " " + there
	}
	return out, nil
}

// wsIdleAgentOn is this conversation's own live tile agent on tile that is
// idle — what a follow-up continues (D250) — nil when there is none.
func (ag *Agent) wsIdleAgentOn(run int64, tile string) *wsAgentRow {
	for _, row := range ag.db.wsAgents(`WHERE run_id=? AND tile=? AND status='idle' ORDER BY id DESC`, run, tile) {
		if !row.Waits {
			return row
		}
	}
	return nil
}

// wsAgentWords is a tile agent in a clause: its coding agent, model and
// mode, and where it stands ("OpenCode, model big, acts without asking,
// idle since 10:52 UTC; brief: a tic tac toe game").
func wsAgentWords(a *wsAgentRow) string {
	parts := []string{orStr(a.Harness, "a coding agent")}
	if a.Model != "" {
		parts = append(parts, "model "+a.Model)
	}
	if a.Mode != "" {
		parts = append(parts, map[bool]string{true: "asks before acting", false: "acts without asking"}[a.Asks])
	}
	switch {
	case a.Waits && a.live():
		parts = append(parts, "waiting for your person")
	case a.Status == "idle":
		parts = append(parts, "idle since "+time.UnixMilli(a.Updated).UTC().Format("15:04 UTC"))
	default:
		parts = append(parts, a.Status)
	}
	out := strings.Join(parts, ", ")
	if a.BriefLine != "" {
		out += "; brief: " + a.BriefLine
	}
	return out
}

// wsSessionsOn says the live coding-agent sessions on tile that this
// conversation didn't start (except), as xbind's one read of the tile gives
// them: the person's own — theirs, never the agent's to drive — and another
// conversation's. "" when there are none, or xbind doesn't say.
func (ag *Agent) wsSessionsOn(ctx context.Context, s *wsSession, tile string, except *wsAgentRow) string {
	var st wsTileView
	if err := ag.wsWith(ctx, s, func(sid string) (err error) { st, err = wsX.TileStanding(ctx, sid, tile); return }); err != nil {
		return ""
	}
	var mine, theirs []string
	for _, se := range st.Sessions {
		if se.Status == "ended" || se.Status == "exited" {
			continue
		}
		w := orStr(se.Provider, "a coding agent")
		if se.Model != "" {
			w += ", model " + se.Model
		}
		if se.Mode != "" {
			w += ", " + map[bool]string{true: "asks before acting", false: "acts without asking"}[se.Asks]
		}
		w += ", " + orStr(se.Status, "open")
		switch se.By {
		case "this agent":
			rows := ag.db.wsAgents(`WHERE session=?`, se.ID)
			if len(rows) > 0 && rows[0].Run == s.Run {
				if except == nil || rows[0].ID != except.ID {
					mine = append(mine, fmt.Sprintf("tile agent #%d (%s)", rows[0].ID, wsAgentWords(rows[0])))
				}
				continue
			}
			theirs = append(theirs, "one another conversation of yours started ("+w+")")
		case "another agent":
			theirs = append(theirs, "one another agent started ("+w+")")
		default:
			theirs = append(theirs, "your person's own session ("+w+") — not yours to drive")
		}
	}
	var out []string
	if len(mine) > 0 {
		out = append(out, "Also yours on "+tile+": "+strings.Join(mine, "; ")+" — follow up there with tile_agent_message.")
	}
	if len(theirs) > 0 {
		out = append(out, "Open on "+tile+" and not yours: "+strings.Join(theirs, "; ")+".")
	}
	return strings.Join(out, " ")
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
	if strings.HasPrefix(text, "/") {
		// a harness command is refused whatever the tile agent is doing: said now, in xbind's own refusal — not
		// held behind a question and refused when it would go in, by which time the model has told its person
		// "it gets it once you've answered" (the real-model run, 2026-11-17)
		return "", &xbin.DelegateError{Status: http.StatusForbidden, Refusal: "command", Message: "a message from a workspace agent can't begin with \"/\""}
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
	err = ag.wsWith(ctx, s, func(sid string) (err error) { outcome, err = wsX.AgentSay(ctx, sid, row.Session, text); return })
	if errors.Is(err, xbin.ErrSessionMode) {
		// it acts without asking, in a mode its person didn't approve it in: the message is theirs to approve (D250)
		return ag.wsAskMessage(ctx, s, row, text)
	}
	if err != nil {
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

// wsAskMessage files a message to row's session for its person's approval:
// the session acts without asking, so what it is told is an act of theirs
// to approve — the whole text, once (D250).
func (ag *Agent) wsAskMessage(ctx context.Context, s *wsSession, row *wsAgentRow, text string) (string, error) {
	c := messageCall(row.Session, text)
	req := &wsReq{Kind: wsReqMessage, Tile: row.Tile, Title: fmt.Sprintf("send a message to tile agent #%d in %s (it acts without asking)", row.ID, row.Tile),
		Method: c.Method, Path: c.Path, Body: []byte(text)}
	out, err := wsFile(ctx, ag, s, req, func(sid string) (wsRequest, error) {
		return wsX.AskMessage(ctx, sid, row.Session, text, clip(text, 500))
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Tile agent #%d is in a mode that acts without asking now, so your person approves this message first: xbin shows them the whole text, and their approval sends exactly it, once. ", row.ID) + out, nil
}

// wsAgentNow is where a tile agent stands now, with the messages of this
// conversation held behind its person's answer.
func wsAgentNow(ag *Agent, row *wsAgentRow) string {
	out := wsStatusWords(row)
	if _, held := ag.db.wsHeld(row.ID); len(held) > 0 {
		out += fmt.Sprintf(" %d of your messages are held behind your person's answer.", len(held))
	}
	return out
}

func wsAgentResultTool(ctx context.Context, ag *Agent, s *wsSession, c coordCall) (string, error) {
	row, err := ag.wsLiveAgent(ctx, s, fmt.Sprint(orAny(c.Args["agent"], "")))
	if err != nil {
		return "", err
	}
	if row.Status == "starting" || (row.Status == "running" && !row.Waits) {
		return wsAgentNow(ag, row) + " It has no result yet: you are told when its turn ends.", nil
	}
	var rep wsAgentReport
	if err := ag.wsWith(ctx, s, func(sid string) (err error) { rep, err = wsX.AgentReport(ctx, sid, row.Session); return }); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(wsAgentNow(ag, row) + "\n")
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
		// the edits first: files with no line changed (a build cache, binaries, scratch the agent made and
		// removed) are counted, not listed — a real run listed 407 of them and none of the three edits
		var edits []wsFileStat
		for _, f := range rep.Files {
			if f.Add+f.Del > 0 {
				edits = append(edits, f)
			}
		}
		fmt.Fprintf(&b, "Files changed (%d):\n", len(edits))
		for i, f := range edits {
			if i == 40 {
				fmt.Fprintf(&b, "… and %d more\n", len(edits)-40)
				break
			}
			fmt.Fprintf(&b, "- %s (+%d −%d)\n", wsPlain(f.Path), f.Add, f.Del)
		}
		if n := len(rep.Files) - len(edits); n > 0 {
			fmt.Fprintf(&b, "(and %d file(s) with no line of text changed — binaries, build output or scratch, e.g. %s — not listed)\n", n, wsPlain(clip(wsFirstQuiet(rep.Files), 80)))
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
	only := ""
	if t := strings.TrimSpace(str(c.Args["tile"])); t != "" {
		tl, err := wsFindTile(ctx, ag, s, t)
		if err != nil {
			return "", err
		}
		only = tl.Path
	}
	rows := ag.db.wsAgents(`WHERE run_id=? ORDER BY id`, s.Run)
	var b strings.Builder
	tiles := []string{}
	n := 0
	for _, a := range rows {
		if only != "" && a.Tile != only {
			continue
		}
		n++
		b.WriteString(wsStatusWords(a))
		if a.live() {
			b.WriteString(" Now: " + wsAgentWords(a) + ".")
		}
		b.WriteString("\n")
		if a.live() && !slices.Contains(tiles, a.Tile) {
			tiles = append(tiles, a.Tile)
		}
	}
	if n == 0 {
		b.WriteString("This session has started no tile agent" + map[bool]string{true: " on " + only, false: ""}[only != ""] + ".\n")
	}
	if only != "" && !slices.Contains(tiles, only) {
		tiles = append(tiles, only)
	}
	for i, t := range tiles {
		if i == 4 {
			break
		}
		if there := ag.wsSessionsOn(ctx, s, t, nil); there != "" {
			b.WriteString(there + "\n")
		}
	}
	for _, br := range ag.db.wsBriefs(s.Run) {
		if br.State == "draft" || br.State == "asked" {
			note := "not started"
			if br.State == "asked" {
				note = "waits for your person's decision"
			}
			// edited: its TEXT, by the person on the card — never a coding agent picked (D250)
			fmt.Fprintf(&b, "\n%s\n(%s%s)", wsBriefWords(br), note, map[bool]string{true: "; your person edited its text", false: ""}[br.Edited])
		}
	}
	return strings.TrimSpace(b.String()), nil
}

// wsFirstQuiet is the first of files with no line changed.
func wsFirstQuiet(files []wsFileStat) string {
	for _, f := range files {
		if f.Add+f.Del == 0 {
			return f.Path
		}
	}
	return ""
}
