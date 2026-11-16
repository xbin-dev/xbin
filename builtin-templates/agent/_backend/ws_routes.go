// ws_routes.go — the workspace agent's own HTTP API (API.md §Workspace
// mode), registered only in workspace mode: what the agent's page needs
// beside the conversation itself, which it reads and writes through the
// template's run routes (the transcript, the stream, a message, stop).
//
//	GET    /ws/state                         who, the switches (read-only: pausing is xbind's), the models
//	GET    /ws/sessions                      the person's sessions, Main first
//	POST   /ws/sessions                      {main: true} Main, made on first use | {name?, scope?, model?, from?}
//	PATCH  /ws/sessions/{id}                 {name?, archived?, scope?, model?}; 409 model_taint with the offer
//	DELETE /ws/sessions/{id}                 not Main
//	GET    /ws/sessions/{id}/connection      ?whatIf=<model>: what a model change would connect, as counts
//	GET    /ws/sessions/{id}/cards           requests, tile agents, briefs, the plan
//	PUT    /ws/sessions/{id}/briefs/{bid}    the person's edit of a brief
//	POST   /ws/sessions/{id}/briefs/{bid}/start   "Review and start…": the request to review, or the start
//	GET    /ws/sessions/{id}/briefs/{bid}/start-defaults   what the start would run on, and the harnesses to pick from
//	GET    /ws/needs                         what waits for the person because of the agent
//	GET    /ws/empty                         the empty state: can / can't, starters (ws_empty.go)
//	GET    /ws/notebook                      pages, with the ones a session can't read marked
//	GET    /ws/notebook/{slug}               one page · PUT edits · DELETE forgets
//	GET    /ws/prefs · PUT /ws/prefs         what this backend keeps for the page
//	POST   /delegate/notify                  xbind's doorbell (ws_notify.go)
//
// Every route is the person's own (wsOwner): in a person's partition that
// person, nobody else. Names for the scope panel come from xbind directly;
// these routes serve what the conversation needs.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

type wsRouteDef struct {
	pattern string
	need    need
	h       http.HandlerFunc
}

func wsRouteTable() []wsRouteDef {
	return []wsRouteDef{
		{"GET /ws/state", needAny, handleWsState},
		{"GET /ws/sessions", needAny, handleWsSessions},
		{"POST /ws/sessions", needAny, handleWsNewSession},
		{"PATCH /ws/sessions/{id}", needOwner, handleWsPatchSession},
		{"DELETE /ws/sessions/{id}", needOwner, handleWsDeleteSession},
		{"GET /ws/sessions/{id}/connection", needOwner, handleWsConnection},
		{"GET /ws/sessions/{id}/cards", needOwner, handleWsCards},
		{"PUT /ws/sessions/{id}/briefs/{bid}", needOwner, handleWsBriefPut},
		{"POST /ws/sessions/{id}/briefs/{bid}/start", needOwner, handleWsBriefStart},
		{"GET /ws/sessions/{id}/briefs/{bid}/start-defaults", needOwner, handleWsBriefDefaults},
		{"GET /ws/needs", needAny, handleWsNeeds},
		{"GET /ws/empty", needAny, handleWsEmpty},
		{"GET /ws/notebook", needAny, handleWsNotebook},
		{"GET /ws/notebook/{slug}", needAny, handleWsPage},
		{"PUT /ws/notebook/{slug}", needAny, handleWsPagePut},
		{"DELETE /ws/notebook/{slug}", needAny, handleWsPageDelete},
		{"GET /ws/prefs", needAny, handleWsPrefs},
		{"PUT /ws/prefs", needAny, handleWsPrefsPut},
	}
}

// wsRoutes registers the workspace agent's routes — in workspace mode
// only: a normal instance has none of them.
func wsRoutes(mux *http.ServeMux) {
	if !wsMode() {
		return
	}
	for _, rt := range wsRouteTable() {
		wsMay[rt.pattern] = true
		mux.Handle(rt.pattern, agentRole(guard(rt.need, rt.h)))
	}
	wsMay[wsNotifyPath] = true
	mux.Handle(wsNotifyPath, agentRuled(nil, http.HandlerFunc(handleWsNotify)))
}

// wsCaller is the person a route acts for (403 otherwise).
func wsCaller(w http.ResponseWriter, r *http.Request) (string, bool) {
	owner, err := wsOwner(callerOf(r))
	if err != nil {
		xbin.WriteError(w, http.StatusForbidden, err.Error())
		return "", false
	}
	return owner, true
}

// wsRouteSession is the session a /ws/sessions/{id} route names, the
// caller's own.
func wsRouteSession(w http.ResponseWriter, r *http.Request) (*wsSession, bool) {
	owner, ok := wsCaller(w, r)
	if !ok {
		return nil, false
	}
	s, err := agent.db.wsSession(pathID(r))
	if err != nil || s.Owner != owner {
		xbin.WriteError(w, http.StatusNotFound, "no such session")
		return nil, false
	}
	return s, true
}

// wsHeldItem is a held conversation as the page shows it: the model, the
// orgs and xbind's reason, and the ways forward — back to the model it was
// on (offer.model, where its model now names another), or a new session.
func wsHeldItem(h wsHeld) map[string]any {
	offer := map[string]any{"newSession": map[string]any{"model": h.Model}}
	if h.Was != "" {
		offer["model"] = h.Was
	}
	return map[string]any{"code": orStr(h.Code, "model_taint"), "model": h.Model, "orgs": h.Orgs, "reason": h.Reason, "atMs": h.At, "offer": offer}
}

// wsWriteErr answers an error of xbind's in its own status and words.
func wsWriteErr(w http.ResponseWriter, err error) {
	var he *wsHeldError
	var te *wsTaintError
	var de *xbin.DelegateError
	switch {
	case errors.As(err, &he):
		out := wsHeldItem(he.Held)
		out["error"], out["code"], out["held"] = he.Error(), "model_taint", true
		xbin.WriteJSON(w, http.StatusConflict, out)
	case errors.As(err, &te):
		// the copy deck's card: this session can't move; a new one can start
		xbin.WriteJSON(w, http.StatusConflict, map[string]any{"error": te.Error(), "code": "model_taint", "model": te.Model, "orgs": te.Orgs,
			"reason": te.Reason, "offer": map[string]any{"newSession": map[string]any{"model": te.Model}}})
	case errors.As(err, &de):
		code := de.Status
		if code < 400 {
			code = http.StatusBadGateway
		}
		xbin.WriteJSON(w, code, map[string]any{"error": de.Message, "refusal": de.Refusal})
	case errors.Is(err, errWsUnavailable):
		xbin.WriteError(w, http.StatusNotImplemented, err.Error())
	default:
		xbin.WriteError(w, http.StatusBadGateway, err.Error())
	}
}

// --- state and sessions --------------------------------------------------------------

func handleWsState(w http.ResponseWriter, r *http.Request) {
	owner, ok := wsCaller(w, r)
	if !ok {
		return
	}
	out := map[string]any{"user": owner, "mode": wsModeValue}
	if st, err := wsX.State(r.Context()); err == nil {
		out["delegation"] = st // paused, the workspace's switch, the admin opt-in: read-only here
	} else {
		out["delegationError"] = wsRefusal(err)
	}
	if ms, err := wsX.Models(r.Context()); err == nil {
		// each with private and where it runs, when the connector's list says;
		// else, for a model one of their sessions is on, as xbind's connection
		// view of that session says
		for i := range ms.Models {
			m := &ms.Models[i]
			for _, s := range agent.db.wsSessions(owner) {
				if ref := s.Conn.Connection.Model; m.Private == nil && ref != nil && s.Model == m.ID {
					private := ref.Private
					m.Private, m.Where, m.Upstream = &private, wsWhere(private), orStr(m.Upstream, ref.Upstream)
				}
			}
		}
		out["models"] = ms
	}
	if q := wsQuotaNow(agent.db); q != nil {
		out["quota"] = q // the stopping quota a model call met (ws_context.go)
	}
	xbin.WriteJSON(w, http.StatusOK, out)
}

// wsSessionItem is a session as the list and the header show it.
func wsSessionItem(s *wsSession, owner string) map[string]any {
	it := map[string]any{"id": s.Run, "main": s.Main, "model": s.Model, "scope": s.Scope, "handle": s.Handle,
		"connection": s.Conn.Connection, "summary": wsConnLine(s.Conn.Connection), "tainted": s.Tainted, "createdMs": s.Created,
		"web": s.Conn.Web, "reach": s.Conn.Reach} // the web and MCP tools: what the header says (D239)
	if s.Held != nil { // it can't go on on its model: why, and the ways forward
		it["held"] = wsHeldItem(*s.Held)
	}
	if run, err := agent.db.getRun(s.Run); err == nil {
		it["name"], it["status"], it["activityMs"] = run.Title, run.Status, run.ActivityMs
		if run.Status == statusWaiting { // the agent's own question
			it["waiting"] = run.Result
			if a := wsAskOf(agent.db, s.Run); a != nil {
				it["ask"] = a
			}
		} else if w := wsWaitingWords(agent.db, s.Run); w != "" { // requests open for its person: the conversation itself goes on
			it["waiting"] = w
		}
	}
	st := agent.db.userStates(owner, []int64{s.Run})[s.Run]
	it["archivedAt"] = st.ArchivedAt
	it["agents"] = agent.db.wsOne(`SELECT count(*) FROM ws_agents WHERE run_id=? AND status IN ('starting','running','idle')`, s.Run)
	it["requests"] = agent.db.wsOne(`SELECT count(*) FROM ws_requests WHERE run_id=? AND state IN ('pending','proposed')`, s.Run)
	return it
}

func handleWsSessions(w http.ResponseWriter, r *http.Request) {
	owner, ok := wsCaller(w, r)
	if !ok {
		return
	}
	items := []map[string]any{}
	for _, s := range agent.db.wsSessions(owner) {
		items = append(items, wsSessionItem(s, owner))
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"sessions": items})
}

func handleWsNewSession(w http.ResponseWriter, r *http.Request) {
	owner, ok := wsCaller(w, r)
	if !ok {
		return
	}
	var body struct {
		Main  bool     `json:"main"`
		Name  string   `json:"name"`
		Scope *wsScope `json:"scope"`
		Model string   `json:"model"`
		From  int64    `json:"from"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	var s *wsSession
	var err error
	switch {
	case body.Main:
		s, err = agent.wsEnsureMain(r.Context(), owner)
	default:
		if from, ferr := agent.db.wsSession(body.From); body.From != 0 && ferr == nil && from.Owner == owner {
			// "New session…" copies the scope and model of the one in view
			if body.Scope == nil {
				body.Scope = from.Scope
			}
			if body.Model == "" {
				body.Model = from.Model
			}
		}
		if body.Model == "" {
			body.Model = wsPrefs(agent.db).Model
		}
		s, err = agent.wsNewSession(r.Context(), owner, body.Name, body.Scope, body.Model, false)
	}
	if err != nil {
		wsWriteErr(w, err)
		return
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"session": wsSessionItem(s, owner)})
}

func handleWsPatchSession(w http.ResponseWriter, r *http.Request) {
	s, ok := wsRouteSession(w, r)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if !decodeBody(w, r, &raw) {
		return
	}
	var body struct {
		Name     *string  `json:"name"`
		Archived *bool    `json:"archived"`
		Scope    *wsScope `json:"scope"`
		Model    *string  `json:"model"`
	}
	b, _ := json.Marshal(raw)
	if err := json.Unmarshal(b, &body); err != nil {
		xbin.WriteError(w, http.StatusBadRequest, "name, archived, scope ({orgs, tiles} or null) and model")
		return
	}
	if body.Archived != nil && *body.Archived && s.Main {
		xbin.WriteError(w, http.StatusConflict, "Main is always here: it can be renamed, not archived")
		return
	}
	ch := wsHandleChange{}
	if _, has := raw["scope"]; has { // null: everything the rules allow again
		ch.SetScope, ch.Scope = true, body.Scope
	}
	if body.Model != nil && strings.TrimSpace(*body.Model) != "" && *body.Model != s.Model {
		ch.Model = strings.TrimSpace(*body.Model)
	}
	if ch.SetScope || ch.Model != "" {
		if run, err := agent.db.getRun(s.Run); err == nil && active(run.Status) && ch.Model != "" {
			xbin.WriteError(w, http.StatusConflict, "the agent is working: stop it, or wait for its turn to end, before changing the model")
			return
		}
		if err := agent.wsChange(r.Context(), s, ch); err != nil {
			wsWriteErr(w, err)
			return
		}
	}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			xbin.WriteError(w, http.StatusBadRequest, "the name can't be empty")
			return
		}
		_ = agent.db.Tx(func(t *DB) error {
			if _, err := t.q.Exec(`UPDATE runs SET title=?, title_src='user' WHERE id=?`, clip(name, 120), s.Run); err != nil {
				return err
			}
			agent.eng.emitRun(t, s.Run)
			return nil
		})
	}
	if body.Archived != nil {
		at := int64(0)
		if *body.Archived {
			at = nowMs()
		}
		st := agent.db.setUserState(s.Run, s.Owner, func(u *userState) { u.ArchivedAt = at })
		agent.emitUserState(s.Run, s.Owner, st)
	}
	cur, _ := agent.db.wsSession(s.Run)
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"session": wsSessionItem(cur, s.Owner)})
}

func handleWsDeleteSession(w http.ResponseWriter, r *http.Request) {
	s, ok := wsRouteSession(w, r)
	if !ok {
		return
	}
	if s.Main {
		xbin.WriteError(w, http.StatusConflict, "Main is always here: it can be renamed, not deleted")
		return
	}
	if err := agent.wsDrop(r.Context(), s); err != nil {
		wsWriteErr(w, err)
		return
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// handleWsConnection is the session's connection now — totals and one row
// an org — or, with whatIf, what moving to another model would change, as
// counts, and whether the conversation's data allows the move (when not:
// the refusal a switch would get, with the new-session offer). The names
// of the tiles are the page's to read from xbind itself.
func handleWsConnection(w http.ResponseWriter, r *http.Request) {
	s, ok := wsRouteSession(w, r)
	if !ok {
		return
	}
	whatIf := strings.TrimSpace(r.URL.Query().Get("whatIf"))
	var c wsConnection
	var what *wsWhatIf
	if err := agent.wsWith(r.Context(), s, func(sid string) (err error) { c, what, err = wsX.Connection(r.Context(), sid, whatIf); return }); err != nil {
		wsWriteErr(w, err)
		return
	}
	out := map[string]any{"connection": c, "summary": wsConnLine(c), "model": s.Model, "handle": s.Handle}
	if what != nil {
		out["whatIf"] = map[string]any{"model": what.Model, "connects": what.Connects, "disconnects": what.Disconnects, "counts": what.Counts,
			"summary": wsConnLine(wsConnection{Counts: what.Counts}), "allowed": what.Allowed, "taintedOrgs": what.TaintedOrgs}
		if !what.Allowed {
			out["offer"] = map[string]any{"newSession": map[string]any{"model": what.Model}}
		}
	}
	xbin.WriteJSON(w, http.StatusOK, out)
}

// --- cards ---------------------------------------------------------------------------

func handleWsCards(w http.ResponseWriter, r *http.Request) {
	s, ok := wsRouteSession(w, r)
	if !ok {
		return
	}
	reqs := agent.db.wsReqs(`WHERE run_id=? ORDER BY id DESC LIMIT 100`, s.Run)
	agents := agent.db.wsAgents(`WHERE run_id=? ORDER BY id`, s.Run)
	type agentCard struct {
		*wsAgentRow
		Held int `json:"held"`
	}
	cards := make([]agentCard, 0, len(agents))
	for _, a := range agents {
		ids, _ := agent.db.wsHeld(a.ID)
		cards = append(cards, agentCard{a, len(ids)})
	}
	var plan []wsPlanStep
	if s.Plan != "" {
		_ = json.Unmarshal([]byte(s.Plan), &plan)
	}
	if reqs == nil {
		reqs = []*wsReq{}
	}
	briefs := agent.db.wsBriefs(s.Run)
	if briefs == nil {
		briefs = []*wsBrief{}
	}
	refusals := agent.db.wsRefusals(s.Run)
	if refusals == nil {
		refusals = []wsRefused{}
	}
	out := map[string]any{"requests": reqs, "agents": cards, "briefs": briefs, "plan": plan, "refusals": refusals}
	if run, err := agent.db.getRun(s.Run); err == nil && run.Status == statusWaiting {
		if a := wsAskOf(agent.db, s.Run); a != nil {
			out["ask"] = a // the question the session waits on, in fields
		}
	}
	xbin.WriteJSON(w, http.StatusOK, out)
}

// wsBriefOf is the brief a /briefs/{bid} route names.
func wsBriefOf(w http.ResponseWriter, r *http.Request, s *wsSession) (*wsBrief, bool) {
	bid, _ := strconv.ParseInt(r.PathValue("bid"), 10, 64)
	b, err := agent.db.wsBrief(s.Run, bid)
	if err != nil {
		xbin.WriteError(w, http.StatusNotFound, "no such brief")
		return nil, false
	}
	return b, true
}

// handleWsBriefPut is the person's edit of a brief nobody started. An
// approval already asked for the old words is withdrawn: what is approved
// is what is sent.
func handleWsBriefPut(w http.ResponseWriter, r *http.Request) {
	s, ok := wsRouteSession(w, r)
	if !ok {
		return
	}
	b, ok := wsBriefOf(w, r, s)
	if !ok {
		return
	}
	if b.State != "draft" && b.State != "asked" {
		xbin.WriteError(w, http.StatusConflict, "this brief was "+b.State+": it can't be edited any more")
		return
	}
	var body struct{ Goal, DoneWhen, LeaveAlone, CheckBy, Harness *string }
	if !decodeBody(w, r, &body) {
		return
	}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = clip(strings.TrimSpace(*v), wsBriefField)
		}
	}
	set(&b.Goal, body.Goal)
	set(&b.DoneWhen, body.DoneWhen)
	set(&b.LeaveAlone, body.LeaveAlone)
	set(&b.CheckBy, body.CheckBy)
	if b.Goal == "" || b.DoneWhen == "" {
		xbin.WriteError(w, http.StatusBadRequest, "a brief needs a goal and a done-when")
		return
	}
	if body.Harness != nil { // the person's pick, among what xbind says the tile allows ("": xbind's default again)
		pick := strings.TrimSpace(*body.Harness)
		if pick != "" {
			var d wsStartDefaults
			if err := agent.wsWith(r.Context(), s, func(sid string) (err error) { d, err = wsX.StartDefaults(r.Context(), sid, b.Tile); return }); err != nil {
				wsWriteErr(w, err)
				return
			}
			if _, allowed := d.of(pick); !allowed {
				xbin.WriteError(w, http.StatusConflict, "that coding agent isn't allowed on "+b.Tile+": pick one of the ones listed")
				return
			}
		}
		b.Harness = pick
	}
	if _, err := agent.db.q.Exec(`UPDATE ws_briefs SET goal=?, done_when=?, leave_alone=?, check_by=?, harness=?, edited=1, updated_ms=? WHERE id=?`,
		b.Goal, b.DoneWhen, b.LeaveAlone, b.CheckBy, b.Harness, nowMs(), b.ID); err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// a request filed for the old brief: asked again with the new one, on the same row
	for _, req := range agent.db.wsReqs(`WHERE run_id=? AND brief_id=? AND state='pending'`, s.Run, b.ID) {
		if wsLocalRef(req.Ref) {
			continue
		}
		start := wsStartOf(b, s.Owner)
		var xr wsRequest
		if err := agent.wsWith(r.Context(), s, func(sid string) (err error) {
			xr, err = wsX.AskAgentStart(r.Context(), sid, start, wsStartSummary(start, b))
			return
		}); err != nil {
			wsWriteErr(w, err)
			return
		}
		raw, _ := json.Marshal(start)
		old := req.Ref
		if _, err := agent.db.q.Exec(`UPDATE ws_requests SET ref=?, body=?, expires_ms=? WHERE id=? AND state='pending'`, xr.ID, raw, xr.Expires*1000, req.ID); err == nil {
			_ = wsX.Withdraw(r.Context(), s.Handle, old)
		}
	}
	b.Edited = true
	agent.wsChanged(s.Run, "brief")
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"brief": b})
}

// handleWsBriefDefaults is what a brief's start would run on, for its
// card: xbind's answer for the tile — the harness (its name), the mode and
// whether it asks before acting, the network, whether xbind will ask the
// person, and the harnesses they may pick instead — with their pick.
func handleWsBriefDefaults(w http.ResponseWriter, r *http.Request) {
	s, ok := wsRouteSession(w, r)
	if !ok {
		return
	}
	b, ok := wsBriefOf(w, r, s)
	if !ok {
		return
	}
	var d wsStartDefaults
	if err := agent.wsWith(r.Context(), s, func(sid string) (err error) { d, err = wsX.StartDefaults(r.Context(), sid, b.Tile); return }); err != nil {
		wsWriteErr(w, err)
		return
	}
	id := orStr(b.Harness, d.Harness)
	h, _ := d.of(id)
	out := map[string]any{"defaults": d, "picked": b.Harness, "harness": id, "harnessName": orStr(h.Name, id), "mode": h.Mode, "asks": h.Asks, "net": h.Net, "ticket": h.NeedsTicket}
	if id == "" { // no coding agent may run there: xbind says why
		out["line"] = orStr(d.Why, "No coding agent may run in this tile")
	} else {
		out["line"] = orStr(h.Name, id) + " · " + map[bool]string{true: "asks before acting", false: "acts without asking"}[h.Asks]
	}
	xbin.WriteJSON(w, http.StatusOK, out)
}

// handleWsBriefStart is "Review and start…" on a brief's card: it answers
// the request xbind's approval chrome opens ({request}), filing one when
// the agent has not asked yet; where the person's preferences let tile
// agents start without asking, the click is the confirmation and the
// session starts ({started}).
func handleWsBriefStart(w http.ResponseWriter, r *http.Request) {
	s, ok := wsRouteSession(w, r)
	if !ok {
		return
	}
	b, ok := wsBriefOf(w, r, s)
	if !ok {
		return
	}
	ctx := r.Context()
	if b.State == "started" || b.State == "replaced" {
		xbin.WriteError(w, http.StatusConflict, "this brief was "+b.State)
		return
	}
	for _, req := range agent.db.wsReqs(`WHERE run_id=? AND brief_id=? AND state='pending'`, s.Run, b.ID) {
		if !wsLocalRef(req.Ref) {
			xbin.WriteJSON(w, http.StatusOK, map[string]any{"request": req.Ref, "id": req.ID})
			return
		}
		// the click is the confirmation only where xbind says, now, that
		// none is needed: its dry run answers for the person's preference as
		// it stands, not as it stood when the agent asked
		start := wsStartOf(b, s.Owner)
		var dry *wsDry
		if err := agent.wsWith(ctx, s, func(sid string) (err error) { _, dry, err = wsX.AgentStart(ctx, sid, start, "", true); return }); err != nil {
			wsWriteErr(w, err)
			return
		}
		if dry == nil || !dry.Allowed {
			xbin.WriteError(w, http.StatusForbidden, orStr(dryErr(dry), "xbin would refuse this start"))
			return
		}
		if !dry.NeedsApproval {
			agent.wsDecide(ctx, s, req, wsRequest{})
		} else if err := agent.wsRefile(ctx, s, req, start); err != nil {
			wsWriteErr(w, err)
			return
		}
		cur, _ := agent.db.wsReq(req.ID)
		if cur != nil && cur.State == "pending" && !wsLocalRef(cur.Ref) { // filed for their approval: the chrome opens it
			xbin.WriteJSON(w, http.StatusOK, map[string]any{"request": cur.Ref, "id": cur.ID})
			return
		}
		xbin.WriteJSON(w, http.StatusOK, map[string]any{"started": cur != nil && cur.State == "used", "id": req.ID, "outcome": cur.Outcome})
		return
	}
	// the agent wrote the brief and has not asked: ask for it
	start := wsStartOf(b, s.Owner)
	var dry *wsDry
	if err := agent.wsWith(ctx, s, func(sid string) (err error) { _, dry, err = wsX.AgentStart(ctx, sid, start, "", true); return }); err != nil {
		wsWriteErr(w, err)
		return
	}
	if dry == nil || !dry.Allowed {
		xbin.WriteError(w, http.StatusForbidden, orStr(dryErr(dry), "xbin would refuse this start"))
		return
	}
	raw, _ := json.Marshal(start)
	req := &wsReq{Run: s.Run, Kind: wsReqSession, Tile: b.Tile, Title: "start a tile agent in " + b.Tile, Method: "POST",
		Path: "/api/xbin/term/sessions", Body: raw, Brief: b.ID, Ref: fmt.Sprintf("brief:%d:%d", s.Run, b.ID)}
	if dry.NeedsApproval {
		var xr wsRequest
		if err := agent.wsWith(ctx, s, func(sid string) (err error) {
			xr, err = wsX.AskAgentStart(ctx, sid, start, wsStartSummary(start, b))
			return
		}); err != nil {
			wsWriteErr(w, err)
			return
		}
		req.Ref, req.Expires = xr.ID, xr.Expires*1000
	}
	if err := agent.db.wsAddReq(req); err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_, _ = agent.db.q.Exec(`UPDATE ws_briefs SET state='asked', updated_ms=? WHERE id=?`, nowMs(), b.ID)
	wsArmPoll()
	if dry.NeedsApproval {
		agent.wsChanged(s.Run, "request")
		xbin.WriteJSON(w, http.StatusOK, map[string]any{"request": req.Ref, "id": req.ID})
		return
	}
	agent.wsDecide(ctx, s, req, wsRequest{})
	cur, _ := agent.db.wsReq(req.ID)
	if cur != nil && cur.State == "pending" && !wsLocalRef(cur.Ref) { // xbind wanted an approval after all: filed, for the chrome to open
		xbin.WriteJSON(w, http.StatusOK, map[string]any{"request": cur.Ref, "id": cur.ID})
		return
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"started": cur != nil && cur.State == "used", "id": req.ID, "outcome": cur.Outcome})
}

// dryErr is a dry run's refusal in xbind's words ("" none).
func dryErr(d *wsDry) string {
	if d == nil {
		return ""
	}
	return d.Error
}

// handleWsNeeds is what waits for the person because of their agent: the
// requests to review, the proposals, the agent's own questions, and the
// tile agents it started that wait for them — oldest first.
func handleWsNeeds(w http.ResponseWriter, r *http.Request) {
	owner, ok := wsCaller(w, r)
	if !ok {
		return
	}
	items := []map[string]any{}
	for _, s := range agent.db.wsSessions(owner) {
		name := ""
		run, err := agent.db.getRun(s.Run)
		if err == nil {
			name = run.Title
		}
		for _, q := range agent.db.wsReqs(`WHERE run_id=? AND state IN ('pending','proposed') ORDER BY id`, s.Run) {
			kind := "approval"
			if q.Kind == wsReqChangeSet {
				kind = "changeset"
			}
			items = append(items, map[string]any{"kind": kind, "session": s.Run, "sessionName": name, "request": q.Ref, "id": q.ID,
				"title": q.Title, "tile": q.Tile, "since": q.Created, "local": wsLocalRef(q.Ref), "brief": q.Brief})
		}
		for _, a := range agent.db.wsAgents(`WHERE run_id=? AND waits=1 AND status IN ('starting','running','idle') ORDER BY id`, s.Run) {
			items = append(items, map[string]any{"kind": "tile-agent", "session": s.Run, "sessionName": name, "agent": a.ID, "tileSession": a.Session,
				"tile": a.Tile, "harness": a.Harness, "text": a.Waiting, "since": a.Updated})
		}
		if err == nil && run.Status == statusWaiting {
			it := map[string]any{"kind": "question", "session": s.Run, "sessionName": name, "text": run.Result, "since": run.Updated * 1000}
			if a := wsAskOf(agent.db, s.Run); a != nil {
				it["ask"], it["choices"], it["callId"] = a.Kind, a.Choices, a.CallID
				if a.Tile != "" {
					it["tile"] = a.Tile
				}
			}
			items = append(items, it)
		}
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// --- the notebook --------------------------------------------------------------------

// wsQuerySession is the session ?session= names (the caller's Main when
// it names none, nil when they have no session yet).
func wsQuerySession(r *http.Request, owner string) *wsSession {
	if id, _ := strconv.ParseInt(r.URL.Query().Get("session"), 10, 64); id != 0 {
		if s, err := agent.db.wsSession(id); err == nil && s.Owner == owner {
			return s
		}
	}
	if id := agent.db.wsOne(`SELECT run_id FROM ws_sessions WHERE owner=? AND main=1`, owner); id != 0 {
		s, _ := agent.db.wsSession(id)
		return s
	}
	return nil
}

// wsPageItem is a page as the person's list shows it: theirs to see
// whatever a session is connected to; locked says the session in view
// can't read it, and why.
func wsPageItem(p WikiPage, st wsStamp, s *wsSession) map[string]any {
	it := map[string]any{"slug": p.Slug, "title": p.Title, "summary": st.Summary, "version": p.Version, "updatedBy": p.UpdatedBy,
		"updatedMs": p.UpdatedMs, "size": p.Size, "orgs": st.Orgs, "tile": st.Tile, "rev": st.Rev}
	if s != nil && !wsReadable(s, st) {
		it["locked"] = true
		it["lockedWhy"] = "Not available in this session: " + wsLockedWhy(s, st)
	}
	return it
}

func handleWsNotebook(w http.ResponseWriter, r *http.Request) {
	owner, ok := wsCaller(w, r)
	if !ok {
		return
	}
	s := wsQuerySession(r, owner)
	items := []map[string]any{}
	if store, err := agent.db.knowID(wsNotebook(owner), false); err == nil {
		if s != nil {
			agent.wsAllowKnown(r.Context(), s, store)
		}
		for _, p := range agent.db.wikiPages(store, false) {
			items = append(items, wsPageItem(p, agent.db.wsStamp(store, p.Slug), s))
		}
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"pages": items})
}

// wsRoutePage is the page a /ws/notebook/{slug} route names.
func wsRoutePage(w http.ResponseWriter, r *http.Request, owner string) (int64, *WikiPage, bool) {
	slug, err := wikiSlug(r.PathValue("slug"))
	if err != nil {
		xbin.WriteError(w, http.StatusBadRequest, err.Error())
		return 0, nil, false
	}
	store, err := agent.db.knowID(wsNotebook(owner), false)
	if err != nil {
		xbin.WriteError(w, http.StatusNotFound, "no such page")
		return 0, nil, false
	}
	p, err := agent.db.wikiPage(store, slug)
	if err != nil {
		xbin.WriteError(w, http.StatusNotFound, "no such page")
		return 0, nil, false
	}
	return store, p, true
}

func handleWsPage(w http.ResponseWriter, r *http.Request) {
	owner, ok := wsCaller(w, r)
	if !ok {
		return
	}
	store, p, ok := wsRoutePage(w, r, owner)
	if !ok {
		return
	}
	it := wsPageItem(*p, agent.db.wsStamp(store, p.Slug), wsQuerySession(r, owner))
	it["body"], it["archived"] = p.Body, p.Archived
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"page": it})
}

// handleWsPagePut is the person's own write: a new page ("Add a note") or
// an edit, from the version they saw (base; 409 when it moved). A page the
// person writes keeps the stamps it had — their edit doesn't unmark what
// the page already held, and nothing here takes a stamp off: this route is
// reachable with the page's own credential, and the page renders what a
// model wrote. A page is unmarked only by deleting it and having a session
// that holds none of that data write it again (wsNotebookChange).
func handleWsPagePut(w http.ResponseWriter, r *http.Request) {
	owner, ok := wsCaller(w, r)
	if !ok {
		return
	}
	var body struct {
		Title, Body, Summary string
		Base                 int64
	}
	if !decodeBody(w, r, &body) {
		return
	}
	store, err := agent.db.knowID(wsNotebook(owner), true)
	if err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var p *WikiPage
	err = agent.db.Tx(func(t *DB) error {
		var err error
		if p, err = t.wikiPut(store, wikiActor{By: owner}, wikiChange{Slug: r.PathValue("slug"), Title: body.Title, Body: body.Body, Note: "edited by " + owner, Base: &body.Base}); err != nil {
			return err
		}
		st := t.wsStamp(store, p.Slug)
		if s := clip(oneLineTitle(body.Summary), wsSummaryMax); s != "" {
			st.Summary = s
		} else if st.Summary == "" {
			st.Summary = clip(oneLineTitle(p.Body), wsSummaryMax)
		}
		return t.wsPutStamp(store, p.Slug, st)
	})
	if err != nil {
		var we *wikiErr
		if errors.As(err, &we) {
			xbin.WriteJSON(w, we.code, map[string]any{"error": wsNotebookWords.Replace(we.msg), "current": we.current})
			return
		}
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	it := wsPageItem(*p, agent.db.wsStamp(store, p.Slug), nil)
	it["body"] = p.Body
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"page": it})
}

// handleWsPageDelete makes the agent forget a page: it leaves the list,
// the search and the prompt; its history stays in the person's data.
func handleWsPageDelete(w http.ResponseWriter, r *http.Request) {
	owner, ok := wsCaller(w, r)
	if !ok {
		return
	}
	store, p, ok := wsRoutePage(w, r, owner)
	if !ok {
		return
	}
	if !p.Archived {
		if _, err := agent.db.wikiPut(store, wikiActor{By: owner}, wikiChange{Slug: p.Slug, Archive: true, Note: "deleted by " + owner, Base: &p.Version}); err != nil {
			xbin.WriteError(w, http.StatusConflict, wsNotebookWords.Replace(err.Error()))
			return
		}
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// --- preferences this backend keeps --------------------------------------------------

// wsPrefsData is what the backend keeps for its page. What a tile agent
// starts with, and whether it starts without asking, are xbind's (the
// person's agent preferences): not here.
type wsPrefsData struct {
	Model       string `json:"model"`       // the model a new session starts on ("": the connector's for the agent role)
	Harness     string `json:"harness"`     // kept for the page; a start's harness is xbind's to resolve, or the person's pick on the brief
	LastSession int64  `json:"lastSession"` // the session the page last showed
}

const wsPrefsKey = "ws_prefs"

func wsPrefs(d *DB) wsPrefsData {
	var p wsPrefsData
	_ = json.Unmarshal([]byte(d.getSetting(wsPrefsKey)), &p)
	return p
}

func handleWsPrefs(w http.ResponseWriter, r *http.Request) {
	if _, ok := wsCaller(w, r); !ok {
		return
	}
	xbin.WriteJSON(w, http.StatusOK, wsPrefs(agent.db))
}

func handleWsPrefsPut(w http.ResponseWriter, r *http.Request) {
	owner, ok := wsCaller(w, r)
	if !ok {
		return
	}
	var body struct {
		Model       *string `json:"model"`
		Harness     *string `json:"harness"`
		LastSession *int64  `json:"lastSession"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	p := wsPrefs(agent.db)
	if body.Model != nil {
		p.Model = clip(strings.TrimSpace(*body.Model), 200)
	}
	if body.Harness != nil {
		p.Harness = clip(strings.TrimSpace(*body.Harness), 64)
	}
	if body.LastSession != nil {
		if s, err := agent.db.wsSession(*body.LastSession); err == nil && s.Owner == owner {
			p.LastSession = s.Run
		}
	}
	if err := agent.db.putSetting(wsPrefsKey, mustJSON(p)); err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	xbin.WriteJSON(w, http.StatusOK, p)
}
