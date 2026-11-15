// ws_sessions.go — a person's workspace-agent sessions (docs/workspace-
// agent.md; D231): Main, the long-lived one made on first use and never
// archived or deleted, and further ones with their own name, scope and
// model. A session is a conversation of the workspace profile
// (coord_workspace.go) and one handle at xbind (ws_xbin.go), made with it
// and closed with it: xbind judges every call by the handle's scope and
// model, and remembers which orgs' data the conversation has read.
//
// The model is the connector's for the agent role unless the person picked
// another; one model serves the whole conversation — its turns, its
// summaries, its titles — because the connector takes only the handle's.
// A model change asks xbind first: a conversation that holds an org's data
// never moves to a model that org doesn't allow (the taint is xbind's; the
// orgs it last reported are checked here too, so a handle xbind had to make
// again can't forget them).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// wsRoleModel is the connector's name for "the model of the agent role".
const wsRoleModel = "xbin/agent"

// wsMainName is the default session's name (the copy deck's).
const wsMainName = "Main"

// wsConnFresh is how long a session's connection view is trusted before
// the next tool call reads it again.
const wsConnFresh = 60 * time.Second

func wsSessionKey(owner string, main bool) string {
	if main {
		return "ws:" + owner + ":main"
	}
	return "ws:" + owner
}

// wsConfig is a workspace conversation's config on model: the fixed class,
// the profile's denials, one model for everything, and none of the
// template's optional features but recall and streaming.
func wsConfig(model string) Config {
	cfg := coordWorkspace.forcedConfig(defaultConfig(), wsClass())
	cfg.System = wsSystem()
	cfg.Pick, cfg.Model = model, model
	cfg.Models = ModelTiers{General: model, Code: model, Memory: model, VLM: model}
	cfg.Subagents, cfg.Approve, cfg.MCP = true, false, nil
	cfg.MaxTurnSteps = 60
	// parallelTools off: a step's calls run in the order the model wrote
	// them — two messages to a tile agent, a read after a change
	cfg.Features = map[string]bool{"skills": false, "vision": false, "watcher": false, "files": false, "repl": false,
		"workflow": false, "threads": false, "judge": false, "docs": false, "parallelTools": false}
	return cfg
}

// wsOwner is the person a call acts for: in a person's partition that
// person and nobody else; in an unpartitioned instance (development) the
// caller.
func wsOwner(c who) (string, error) {
	if c.kind != whoUser || c.viewedBy != "" {
		return "", errors.New("a workspace agent is a person's own: ask from your own account")
	}
	if userMode() && c.user != runUser {
		return "", errors.New("this is another person's workspace agent")
	}
	return c.user, nil
}

// wsEnsureMain is owner's Main session, made now when they have none.
func (ag *Agent) wsEnsureMain(ctx context.Context, owner string) (*wsSession, error) {
	if id := ag.db.wsOne(`SELECT run_id FROM ws_sessions WHERE owner=? AND main=1`, owner); id != 0 {
		return ag.db.wsSession(id)
	}
	return ag.wsNewSession(ctx, owner, wsMainName, nil, "", true)
}

// wsNewSession makes a session: its handle at xbind first (a conversation
// xbind won't connect is not made), then the conversation.
func (ag *Agent) wsNewSession(ctx context.Context, owner, name string, scope *wsScope, model string, main bool) (*wsSession, error) {
	if model == "" {
		ms, err := wsX.Models(ctx)
		if err != nil {
			return nil, err
		}
		model = orStr(ms.Default, wsRoleModel)
	}
	h, err := wsX.OpenSession(ctx, scope, model)
	if err != nil {
		return nil, err
	}
	if h.Model == "" {
		h.Model = model
	}
	title, src := strings.TrimSpace(name), "user"
	if title == "" {
		title, src = "New session", "clip" // named from its first request (title.go)
	}
	if main {
		src = "origin"
	}
	var id int64
	err = ag.db.Tx(func(t *DB) error {
		if main {
			if id = t.wsOne(`SELECT run_id FROM ws_sessions WHERE owner=? AND main=1`, owner); id != 0 {
				return nil // two first uses made one
			}
		}
		var err error
		id, err = ag.startRunTx(t, runOpts{Title: clip(title, 120), Cfg: wsConfig(h.Model), Hold: true, Stamp: runStamp{
			Owner: owner, Visibility: visPrivate, TeamRole: roleViewer, Origin: originWorkspace,
			SessionKey: wsSessionKey(owner, main), TitleSrc: src}})
		if err != nil {
			return err
		}
		_, err = t.q.Exec(`INSERT INTO ws_sessions (run_id, owner, main, handle, model, scope, conn, conn_ms, created_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, owner, main, h.ID, h.Model, wsScopeJSON(h.Scope), mustJSON(h), nowMs(), nowMs())
		return err
	})
	if err != nil {
		_ = wsX.CloseSession(ctx, h.ID)
		return nil, err
	}
	s, err := ag.db.wsSession(id)
	if err == nil && s.Handle != h.ID { // the other first use won: its handle stands
		_ = wsX.CloseSession(ctx, h.ID)
	}
	return s, err
}

// wsGone: xbind no longer knows the handle (unused for a day, or pushed
// out by newer ones): the session opens another.
func wsGone(err error) bool { return errors.Is(err, xbin.ErrNoSession) }

// wsReopen gives s a new handle with its scope and model. xbind's record
// of what the conversation read went with the old handle; what was last
// seen of it stays on record here (wsChange checks both), and the map is
// read once so xbind's own record covers what is connected again.
func (ag *Agent) wsReopen(ctx context.Context, s *wsSession) error {
	h, err := wsX.OpenSession(ctx, s.Scope, s.Model)
	if err != nil {
		return err
	}
	if len(s.Conn.Tainted) > 0 {
		if _, err := wsX.Map(ctx, h.ID); err == nil {
			if again, err := wsX.Session(ctx, h.ID); err == nil {
				h = again
			}
		}
	}
	h.Tainted = wsUnion(s.Conn.Tainted, h.Tainted)
	if h.Model == "" {
		h.Model = s.Model
	}
	if err := ag.db.wsPutHandle(s.Run, h); err != nil {
		return err
	}
	s.Handle, s.Conn, s.ConnMs = h.ID, h, nowMs()
	return nil
}

// wsWith runs f with s's handle, once more with a new one when xbind says
// the handle is gone.
func (ag *Agent) wsWith(ctx context.Context, s *wsSession, f func(sid string) error) error {
	if s.Handle == "" {
		if err := ag.wsReopen(ctx, s); err != nil {
			return err
		}
	}
	err := f(s.Handle)
	if err != nil && wsGone(err) {
		if rerr := ag.wsReopen(ctx, s); rerr != nil {
			return rerr
		}
		err = f(s.Handle)
	}
	return err
}

// wsRefresh reads s's handle again when what is stored is older than
// wsConnFresh (force: now): its connection view and what it holds.
func (ag *Agent) wsRefresh(ctx context.Context, s *wsSession, force bool) {
	if !force && nowMs()-s.ConnMs < wsConnFresh.Milliseconds() {
		return
	}
	var h wsHandle
	if err := ag.wsWith(ctx, s, func(sid string) (err error) { h, err = wsX.Session(ctx, sid); return }); err != nil {
		return
	}
	h.Tainted = wsUnion(s.Conn.Tainted, h.Tainted)
	if h.Model == "" {
		h.Model = s.Model
	}
	if ag.db.wsPutHandle(s.Run, h) == nil {
		s.Conn, s.ConnMs, s.Scope, s.Model = h, nowMs(), h.Scope, h.Model
	}
	// the orgs' rules may have moved too — asked again at most every
	// wsAllowFresh: the question is an act at xbind (rate-limited with the
	// agent's others), and a stale yes shows nothing xbind wouldn't: the
	// taint a page's reading records is refused for a model its org forbids
	if last, _ := wsAllowAt.Load(s.Run); last == nil || nowMs()-last.(int64) > wsAllowFresh.Milliseconds() {
		ag.wsAllow(ctx, s)
	}
}

// wsAllowFresh is how long a session's allow answers are trusted;
// wsAllowAt when each session last asked.
const wsAllowFresh = 5 * time.Minute

var wsAllowAt sync.Map

func wsUnion(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, x := range b {
		if !hasStr(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// wsChange changes s's scope, its model or both. A model the conversation's
// data may not be sent to is *wsTaintError, and nothing changed: xbind
// refuses it by the handle's own record, and it is refused here first by
// what was last seen of that record — which outlives a handle xbind had to
// make again.
func (ag *Agent) wsChange(ctx context.Context, s *wsSession, ch wsHandleChange) error {
	if ch.Model != "" && ch.Model != s.Model && len(s.Conn.Tainted) > 0 {
		var cur wsHandle
		if err := ag.wsWith(ctx, s, func(sid string) (err error) { cur, err = wsX.Session(ctx, sid); return }); err != nil {
			return err
		}
		var lost []string // held, by this backend's record, and unknown to xbind's handle
		for _, org := range s.Conn.Tainted {
			if !hasStr(cur.Tainted, org) {
				lost = append(lost, org)
			}
		}
		if len(lost) > 0 {
			return &wsTaintError{Model: ch.Model, Orgs: lost, Reason: "xbin renewed this conversation's session and can no longer check the move against what it read from them"}
		}
	}
	var h wsHandle
	if err := ag.wsWith(ctx, s, func(sid string) (err error) { h, err = wsX.UpdateSession(ctx, sid, ch); return }); err != nil {
		return err
	}
	h.Tainted = wsUnion(s.Conn.Tainted, h.Tainted)
	if h.Model == "" {
		h.Model = orStr(ch.Model, s.Model)
	}
	return ag.db.Tx(func(t *DB) error {
		if err := t.wsPutHandle(s.Run, h); err != nil {
			return err
		}
		if h.Model != s.Model { // one model for the whole conversation
			cfg, err := t.runConfig(s.Run)
			if err != nil {
				return err
			}
			cfg.Pick, cfg.Model = h.Model, h.Model
			cfg.Models = ModelTiers{General: h.Model, Code: h.Model, Memory: h.Model, VLM: h.Model}
			raw, _ := json.Marshal(cfg)
			if _, err := t.q.Exec(`UPDATE runs SET config=? WHERE id=?`, string(raw), s.Run); err != nil {
				return err
			}
		}
		s.Handle, s.Conn, s.ConnMs, s.Scope, s.Model = h.ID, h, nowMs(), h.Scope, h.Model
		if ag.eng != nil {
			ag.eng.emitRun(t, s.Run)
		}
		t.AfterCommit(func() { ag.wsAllow(ctx, s) }) // another model: which pages it may read is asked again
		return nil
	})
}

// wsDrop deletes a session that isn't Main: its handle, what it started
// stays the person's (their tile agents keep running, as their own).
func (ag *Agent) wsDrop(ctx context.Context, s *wsSession) error {
	if s.Main {
		return errors.New("Main is always here: it can be renamed, not deleted or archived")
	}
	for _, r := range ag.db.wsReqs(`WHERE run_id=? AND state='pending' AND kind<>?`, s.Run, wsReqChangeSet) {
		_ = wsX.Withdraw(ctx, s.Handle, r.Ref) // nobody is left to use the answer
	}
	if s.Handle != "" {
		if err := wsX.CloseSession(ctx, s.Handle); err != nil && !wsGone(err) && !wsMissing(err) {
			return err
		}
	}
	acl, _ := ag.db.loadACL(s.Run) // who could see it, for the list event: gone with the run
	_ = ag.db.Tx(func(t *DB) error {
		ag.cancelRuns(t, s.Run, true, "session deleted")
		return nil
	})
	if err := ag.deleteRunTree(s.Run); err != nil {
		return err
	}
	ag.acl.flush(s.Run)
	_, _ = ag.db.q.Exec(`DELETE FROM run_user_state WHERE run_id=?`, s.Run)
	if ag.eng != nil {
		ag.eng.hub.publish(&Event{Type: evRun, Run: s.Run, Root: s.Run, Data: map[string]any{"id": s.Run, "deleted": true}, acl: acl})
	}
	return nil
}

// wsSessionOfRun is the session run (or the root of run, for a research
// subagent) belongs to.
func (ag *Agent) wsSessionOfRun(run *Run) (*wsSession, error) {
	if run == nil || run.Origin != originWorkspace {
		return nil, errors.New("not a workspace agent's conversation")
	}
	s, err := ag.db.wsSession(rootOf(run))
	if err != nil {
		return nil, errors.New("this conversation isn't a workspace-agent session")
	}
	return s, nil
}

// wsConnLine is a connection in one sentence (xbind's when it gave one).
func wsConnLine(c wsConnection) string {
	if c.Counts.Summary != "" {
		return c.Counts.Summary
	}
	out := fmt.Sprintf("%d tiles · %d orgs connected", c.Counts.Tiles, c.Counts.Orgs)
	if c.Counts.NotConnected > 0 {
		out += fmt.Sprintf(" · %d not connected", c.Counts.NotConnected)
	}
	return out
}

// wsAllow asks xbind which of the notebook's stamped orgs allow s's
// model, and keeps the answer on the session: what its pages' readability
// is decided by (ws_notebook.go). One call for every org on any stamp.
func (ag *Agent) wsAllow(ctx context.Context, s *wsSession) {
	store, err := ag.db.knowID(wsNotebook(s.Owner), false)
	if err != nil {
		return
	}
	var orgs []string
	rows, err := ag.db.q.Query(`SELECT orgs FROM ws_pages WHERE store_id=?`, store)
	if err != nil {
		return
	}
	for rows.Next() {
		var raw string
		var list []string
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &list) == nil {
			orgs = wsUnion(orgs, list)
		}
	}
	rows.Close()
	allow := map[string]string{}
	if len(orgs) > 0 {
		var ok bool
		var reasons map[string]string
		if err := ag.wsWith(ctx, s, func(sid string) (err error) { ok, reasons, err = wsX.Allows(ctx, sid, orgs); return }); err != nil {
			return // as last asked
		}
		for _, o := range orgs {
			why, refused := reasons[o]
			switch {
			case refused:
				allow[o] = orStr(why, "it doesn't allow this session's model")
			case ok || len(reasons) > 0:
				allow[o] = ""
			default: // refused with no org named: none is vouched for
				allow[o] = "its rules don't allow this session's model"
			}
		}
	}
	if _, err := ag.db.q.Exec(`UPDATE ws_sessions SET allow=? WHERE run_id=?`, mustJSON(allow), s.Run); err == nil {
		s.Allow = allow
		wsAllowAt.Store(s.Run, nowMs())
	}
}

// wsAllowKnown asks xbind (wsAllow) only when the notebook holds a stamp
// with an org the session has no answer for yet — a page another session
// wrote since, or a session that never asked.
func (ag *Agent) wsAllowKnown(ctx context.Context, s *wsSession, store int64) {
	rows, err := ag.db.q.Query(`SELECT orgs FROM ws_pages WHERE store_id=?`, store)
	if err != nil {
		return
	}
	missing := false
	for rows.Next() {
		var raw string
		var list []string
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &list) == nil {
			for _, o := range list {
				if _, asked := s.Allow[o]; !asked {
					missing = true
				}
			}
		}
	}
	rows.Close()
	if missing {
		ag.wsAllow(ctx, s)
	}
}

// wsHold records at xbind that the conversation now holds data of orgs —
// before a notebook page stamped with them is read or shown to it. An
// error means the page isn't shown.
func (ag *Agent) wsHold(ctx context.Context, s *wsSession, orgs []string) error {
	var need []string
	for _, o := range orgs {
		if !hasStr(s.Conn.Tainted, o) {
			need = append(need, o)
		}
	}
	if len(need) == 0 {
		return nil
	}
	if err := ag.wsWith(ctx, s, func(sid string) error { return wsX.Taint(ctx, sid, need) }); err != nil {
		return err
	}
	h := s.Conn
	h.Tainted = wsUnion(h.Tainted, need)
	if err := ag.db.wsPutHandle(s.Run, h); err != nil {
		return err
	}
	s.Conn = h
	return nil
}

func init() { llmHeaderHooks = append(llmHeaderHooks, wsLLMHeader) }

// wsLLMHeader marks a workspace conversation's model calls — its turns,
// its research subagents', its summaries and titles — with the session's
// handle: the connector serves it on the handle's model only, and checks
// that model against the orgs whose data the conversation holds. Nothing
// in a normal instance.
func wsLLMHeader(req LLMRequest, h http.Header) {
	ag := projAg()
	if !wsMode() || ag == nil || ag.db == nil || req.Run == 0 {
		return
	}
	run, err := ag.db.getRun(req.Run)
	if err != nil || run.Origin != originWorkspace {
		return
	}
	if s, err := ag.db.wsSession(rootOf(run)); err == nil && s.Handle != "" {
		h.Set(xbin.AgentSessionHeader, s.Handle)
	}
}
