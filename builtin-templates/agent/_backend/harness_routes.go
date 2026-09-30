// harness_routes.go — a coding agent's own routes (D-harness §4.2.4–§4.2.6;
// API.md §Coding agents): its summary and session (GET), a mode or config
// option change (PATCH — an RPC to the live adapter), an answer to its
// parked question and a sign-in through the adapter — and how the routes
// only the built-in agent has answer on a coding agent's run (§4.2.11).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/acp"
)

// hRPCFor bounds a mode or option change at the adapter.
const hRPCFor = 30 * time.Second

func harnessAPIRoutes() []routeDef {
	return []routeDef{
		{"GET /runs/{id}/harness", needViewer, handleGetHarness},
		{"PATCH /runs/{id}/harness", needParticipant, handlePatchHarness},
		{"POST /runs/{id}/harness/answer", needParticipant, handleHarnessAnswer},
		{"POST /runs/{id}/harness/authenticate", needParticipant, handleHarnessAuthenticate},
	}
}

// harnessRoute is route run id and its summary when a coding agent answers
// it (false once it answered: 404 no such run, 409 on a built-in run).
func harnessRoute(w http.ResponseWriter, id int64) (*Run, Config, map[string]any, bool) {
	cfg, err := harnessRunOf(id)
	if err != nil {
		writeSbxErr(w, err)
		return nil, cfg, nil, false
	}
	run, err := agent.db.getRun(id)
	if err != nil {
		xbin.WriteError(w, http.StatusNotFound, "no such run")
		return nil, cfg, nil, false
	}
	sum := harnessSummaryOf(run)
	if sum == nil {
		xbin.WriteError(w, http.StatusConflict, "not a coding-agent conversation")
		return nil, cfg, nil, false
	}
	return run, cfg, sum, true
}

// decodeHarnessBody reads a small JSON body (false once it answered 400).
func decodeHarnessBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		xbin.WriteError(w, http.StatusBadRequest, "bad body")
		return false
	}
	return true
}

// writeHeld is a 503 while another process holds the session (a handoff).
func writeHeld(w http.ResponseWriter, name string) {
	w.Header().Set("Retry-After", "1")
	xbin.WriteError(w, http.StatusServiceUnavailable, "another process of this agent holds "+name+"'s session right now — try again")
}

// handleGetHarness is a coding agent's summary (§4.3.2), its session and
// what "allow always" answers remember in this conversation.
//
//	GET /runs/{id}/harness → {harness, session: {gen, execId, acpSessionId, loadable, steering, startedAt, lastActive}, rules}
func handleGetHarness(w http.ResponseWriter, r *http.Request) {
	_, _, sum, ok := harnessRoute(w, pathID(r))
	if !ok {
		return
	}
	hs, err := agent.db.harnessSession(pathID(r))
	if err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if hs == nil {
		hs = &harnessSession{}
	}
	steering, _ := sum["steering"].(bool)
	rules := []acp.Rule{}
	if hs.Rules != "" {
		_ = json.Unmarshal([]byte(hs.Rules), &rules)
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"harness": sum, "rules": rules, "session": map[string]any{
		"gen": hs.Gen, "execId": hs.ExecID, "acpSessionId": hs.ACPSession, "loadable": hs.Loadable,
		"steering": steering || hs.Steering, "startedAt": hs.StartedMs, "lastActive": hs.LastActiveMs}})
}

// handlePatchHarness switches a coding agent's mode or one of its config
// options (§4.2.4): on a live session an RPC to the adapter (session/set_mode,
// or the config option of category mode when it speaks one; session/
// set_config_option), and either way the choice stored in Config.Harness for
// the next start. An explicit mode (a bypass) is the root conversation's
// owner's, a person's. The `harness` event says the change.
//
//	PATCH /runs/{id}/harness {mode?, option?: {id, value}} → {harness}
func handlePatchHarness(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	run, _, sum, ok := harnessRoute(w, id)
	if !ok {
		return
	}
	var body struct {
		Mode   *string `json:"mode"`
		Option *struct {
			ID    string `json:"id"`
			Value string `json:"value"`
		} `json:"option"`
	}
	if !decodeHarnessBody(w, r, &body) {
		return
	}
	if body.Mode == nil && body.Option == nil {
		xbin.WriteError(w, http.StatusBadRequest, "mode or option: name one")
		return
	}
	name, _ := sum["name"].(string)
	c := callerOf(r)
	if body.Mode != nil {
		avail, _ := sum["mode"].(map[string]any)["available"].([]map[string]any)
		var pick map[string]any
		var ids []string
		for _, m := range avail {
			ids = append(ids, fmt.Sprint(m["id"]))
			if m["id"] == *body.Mode {
				pick = m
			}
		}
		if pick == nil {
			xbin.WriteError(w, http.StatusBadRequest, "mode: one of "+strings.Join(ids, ", "))
			return
		}
		if pick["explicit"] == true {
			if root := rootRunOf(run); !grantOwner(c, root) {
				xbin.WriteError(w, http.StatusForbidden, fmt.Sprintf("only %s can switch %s to %s",
					orStr(root.Owner, "the conversation's owner"), name, pick["name"]))
				return
			}
		}
	}
	if o := body.Option; o != nil {
		opts, _ := sum["options"].([]acp.ConfigOption)
		if len(opts) == 0 { // no session yet: what the harness last reported (GET /harnesses)
			var seen []acp.ConfigOption
			_ = json.Unmarshal(agent.db.harnessOptions(fmt.Sprint(sum["provider"])), &seen)
			opts = harnessOptionsView(seen)
		}
		var pick *acp.ConfigOption
		var ids []string
		for i := range opts {
			ids = append(ids, opts[i].ID)
			if opts[i].ID == o.ID {
				pick = &opts[i]
			}
		}
		if pick == nil {
			xbin.WriteError(w, http.StatusBadRequest, "option: one of "+strings.Join(ids, ", "))
			return
		}
		if len(pick.Options) > 0 {
			var vals []string
			found := false
			for _, v := range pick.Options {
				vals = append(vals, v.Value)
				found = found || v.Value == o.Value
			}
			if !found {
				xbin.WriteError(w, http.StatusBadRequest, "value: one of "+strings.Join(vals, ", "))
				return
			}
		}
	}
	e := agent.eng
	s, err := e.harnessLive(r.Context(), run)
	if errors.Is(err, errHandoff) || errors.Is(err, errFenced) {
		writeHeld(w, name)
		return
	}
	if err != nil {
		xbin.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	if s != nil {
		ctx, cancel := context.WithTimeout(r.Context(), hRPCFor)
		if body.Mode != nil {
			err = s.setMode(ctx, *body.Mode)
		}
		if err == nil && body.Option != nil {
			err = s.c.SetOption(ctx, body.Option.ID, body.Option.Value)
		}
		cancel()
		var re *acp.Error
		switch {
		case errors.As(err, &re):
			xbin.WriteError(w, http.StatusBadGateway, re.Message)
			return
		case errors.Is(err, context.DeadlineExceeded):
			xbin.WriteError(w, http.StatusGatewayTimeout, name+" didn't answer")
			return
		case err != nil:
			xbin.WriteError(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	store := func(t *DB) error {
		cfg, err := t.runConfig(id)
		if err != nil || cfg.Harness == nil {
			return err
		}
		if body.Mode != nil {
			cfg.Harness.Mode = *body.Mode
		}
		if o := body.Option; o != nil {
			if cfg.Harness.Options == nil {
				cfg.Harness.Options = map[string]string{}
			}
			cfg.Harness.Options[o.ID] = o.Value
		}
		raw, _ := json.Marshal(cfg)
		_, err = t.q.Exec(`UPDATE runs SET config=? WHERE id=?`, string(raw), id)
		return err
	}
	if s != nil { // with the snapshot the answer refreshed
		err = s.commit(nil, func(t *DB, _ *harnessSession) error { return store(t) })
	}
	if s == nil || errors.Is(err, errHarnessGone) || errors.Is(err, errHarnessStale) {
		err = e.fenced(store)
	}
	switch {
	case errors.Is(err, errHandoff) || errors.Is(err, errFenced):
		writeHeld(w, name)
		return
	case err != nil:
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	e.publishHarness(run.ID)
	run, _ = agent.db.getRun(id)
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"harness": harnessSummaryOf(run)})
}

// runAnswer is the Run JSON a new conversation's POST /ask or /runs
// answers — with a coding agent's summary (§4.3.1: every place that serves
// a harness run carries its `harness`).
func runAnswer(run *Run) any {
	h := harnessSummaryOf(run)
	if h == nil {
		return run
	}
	b, _ := json.Marshal(run)
	var v map[string]any
	if json.Unmarshal(b, &v) != nil {
		return run
	}
	v["harness"] = h
	return v
}

// rootRunOf is run's root conversation (run itself when it is one).
func rootRunOf(run *Run) *Run {
	if run.ParentID == 0 {
		return run
	}
	if r, err := agent.db.getRun(rootOf(run)); err == nil {
		return r
	}
	return run
}

// harnessLive is run's session for an RPC: the one this process drives, or
// a predecessor's adapter with its session open, taken over; nil when no
// adapter runs, or one runs with no session yet to talk to (signed out) —
// a change is then only stored. errHandoff / errFenced: another process
// holds it (a handoff; one starting the adapter).
func (e *Engine) harnessLive(ctx context.Context, run *Run) (*hsess, error) {
	e.mu.Lock()
	closing, owned := e.closing, e.owned
	e.mu.Unlock()
	switch {
	case closing:
		return nil, errHandoff
	case !owned:
		return nil, errFenced
	}
	if e.harnessOf(run.ID) == nil {
		hs, err := e.db.harnessSession(run.ID)
		if err != nil {
			return nil, err
		}
		if hs == nil || hs.ExecID == "" || !hsRunning(hs.State) {
			return nil, nil
		}
		var st acp.SessionState
		if json.Unmarshal([]byte(hs.Snapshot), &st) != nil || st.SessionID == "" {
			return nil, errHandoff // it is being started, elsewhere: try again
		}
	}
	s, err := e.ensureHarnessAt(ctx, run, true)
	switch {
	case isHarnessFail(err):
		return nil, nil // it couldn't be taken over (stored failed): the change is stored
	case err != nil || s == nil:
		return nil, err
	case s.c == nil:
		return nil, nil
	}
	if sid, _ := s.c.Session(); sid == "" {
		return nil, nil
	}
	return s, nil
}

// setMode switches the adapter's mode: through its config option of
// category mode when it has one, else session/set_mode.
func (s *hsess) setMode(ctx context.Context, mode string) error {
	for _, o := range s.c.State().Options {
		if o.Category == "mode" {
			return s.c.SetOption(ctx, o.ID, mode)
		}
	}
	return s.c.SetOption(ctx, "mode", mode)
}

// publishHarness sends run's `harness` event (§4.3.3): through the session
// this process drives, else straight from the stored summary.
func (e *Engine) publishHarness(runID int64) {
	if s := e.harnessOf(runID); s != nil && !s.isHalted() {
		s.publishSummary()
		return
	}
	run, err := e.db.getRun(runID)
	if err != nil {
		return
	}
	if sum := harnessSummary(e.db, run); sum != nil {
		e.hub.publish(&Event{Type: evHarness, Run: run.ID, Root: rootOf(run), key: "harness:" + itoa(run.ID), Data: sum})
	}
}

// handleHarnessAnswer answers a coding agent's parked question (§4.2.5):
// an hanswer row the pass delivers — accept with the form's values,
// decline or cancel.
//
//	POST /runs/{id}/harness/answer {park, action, content?} → {ok}
func handleHarnessAnswer(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	run, _, _, ok := harnessRoute(w, id)
	if !ok {
		return
	}
	var body struct {
		Park    string          `json:"park"`
		Action  string          `json:"action"`
		Content json.RawMessage `json:"content"`
	}
	if !decodeHarnessBody(w, r, &body) {
		return
	}
	p := parsePending(run.Pending)
	switch {
	case run.Status != statusWaiting || p.Kind != "question" || p.Harness == nil:
		xbin.WriteError(w, http.StatusBadRequest, "no pending question")
		return
	case body.Park != "" && body.Park != p.Park:
		xbin.WriteError(w, http.StatusConflict, "that question is no longer pending — the agent is asking something else now")
		return
	case body.Action != "accept" && body.Action != "decline" && body.Action != "cancel":
		xbin.WriteError(w, http.StatusBadRequest, "action is accept, decline or cancel")
		return
	}
	var content json.RawMessage
	if body.Action == "accept" {
		var form map[string]any
		if len(body.Content) == 0 || json.Unmarshal(body.Content, &form) != nil || form == nil {
			xbin.WriteError(w, http.StatusBadRequest, "content: an object with the form's fields")
			return
		}
		content = body.Content
	}
	in := inboxBody{Park: p.Park, Action: body.Action, Content: content, Sender: callerOf(r).tag()}
	if _, _, err := agent.queue(id, inboxHAnswer, in, ""); err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// handleHarnessAuthenticate signs a coding agent in through its adapter
// (§4.2.6) — for a person who may use its sandbox (asked of the manager
// now), after a confirm on a sandbox others may use too (they act as that
// person with the harness there: its credentials live in the sandbox's
// HOME). The engine (harnessAuthenticate) talks to the adapter; the key
// rides the one authenticate call and is never stored, logged or echoed.
//
//	POST /runs/{id}/harness/authenticate {method, apiKey?, confirm?}
//	  → 200 {ok, state: "ready"} | 202 {ok, device: {url, message}}
func handleHarnessAuthenticate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	run, cfg, sum, ok := harnessRoute(w, id)
	if !ok {
		return
	}
	var body struct {
		Method  string `json:"method"`
		APIKey  string `json:"apiKey"`
		Confirm bool   `json:"confirm"`
	}
	if !decodeHarnessBody(w, r, &body) {
		return
	}
	name, _ := sum["name"].(string)
	denied := func(box string) string { return "only someone who may use " + box + " can sign it in" }
	c := callerOf(r)
	if sbxUserOf(c) == "" {
		label := cfg.Harness.Ref
		if b, ok := cfg.sandboxBinding(label); ok && b.Name != "" {
			label = b.Name
		} else if _, sid, ok := splitSandboxRef(label); ok {
			label = sid
		}
		xbin.WriteError(w, http.StatusForbidden, denied(label))
		return
	}
	hs, _ := agent.db.harnessSession(id)
	if hs == nil || hs.State != hsLogin {
		xbin.WriteError(w, http.StatusConflict, name+" is signed in")
		return
	}
	if msg := authMethodErr(hs, body.Method, body.APIKey); msg != "" {
		xbin.WriteError(w, http.StatusBadRequest, msg)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	_, _, box, err := personSandbox(ctx, c, cfg.Harness.Ref, func(b *sbxSandbox) string { return denied(sbxLabel(b)) })
	cancel()
	if err != nil {
		writeSbxErr(w, err)
		return
	}
	if sandboxShared(box) && !body.Confirm {
		xbin.WriteJSON(w, http.StatusConflict, map[string]any{"confirm": true,
			"error": fmt.Sprintf("anyone who may use %s acts as you with %s there — confirm to sign in", sbxLabel(box), name)})
		return
	}
	res, err := agent.eng.harnessAuthenticate(r.Context(), run, body.Method, body.APIKey)
	var ae *hAuthErr
	switch {
	case errors.As(err, &ae):
		xbin.WriteError(w, ae.Code, ae.Msg)
	case errors.Is(err, errHandoff) || errors.Is(err, errFenced):
		writeHeld(w, name)
	case err != nil:
		xbin.WriteError(w, http.StatusGatewayTimeout, name+" didn't answer its sign-in")
	case res.Device != nil:
		xbin.WriteJSON(w, http.StatusAccepted, map[string]any{"ok": "true", "device": res.Device})
	default:
		xbin.WriteJSON(w, http.StatusOK, map[string]any{"ok": "true", "state": orStr(res.State, "ready")})
	}
}

// authMethodErr checks a sign-in's method against the ones the session
// offers (harness.login.methods of kind api-key or device-code) before a
// confirm is asked for ("" = fine, or not known yet: the engine checks it
// against the live adapter).
func authMethodErr(hs *harnessSession, method, apiKey string) string {
	var l hLogin
	if hs.Login == "" || json.Unmarshal([]byte(hs.Login), &l) != nil || len(l.Methods) == 0 {
		return ""
	}
	var ids []string
	var pick *hLoginMethod
	for i, m := range l.Methods {
		if m.Kind != "api-key" && m.Kind != "device-code" {
			continue
		}
		ids = append(ids, m.ID)
		if m.ID == method {
			pick = &l.Methods[i]
		}
	}
	switch {
	case pick == nil:
		return "method: one of " + strings.Join(ids, ", ")
	case pick.Kind == "api-key" && apiKey == "":
		return "apiKey: needed for " + pick.Name
	case pick.Kind != "api-key" && apiKey != "":
		return "apiKey: only for an API-key method"
	}
	return ""
}

// --- the built-in agent's routes on a coding agent's run (§4.2.11) --------------------

// isHarnessRun: a coding agent answers run id.
func isHarnessRun(id int64) bool {
	r, err := agent.db.getRun(id)
	return err == nil && r.Engine == engineHarness
}

// refuseOnHarness answers 409 msg when a coding agent answers run id (true
// once it answered).
func refuseOnHarness(w http.ResponseWriter, id int64, msg string) bool {
	if !isHarnessRun(id) {
		return false
	}
	xbin.WriteError(w, http.StatusConflict, msg)
	return true
}

// harnessHasCommand: the coding agent of run advertises the slash command.
func harnessHasCommand(run *Run, name string) bool {
	hs, _ := agent.db.harnessSession(run.ID)
	if hs == nil {
		return false
	}
	var st acp.SessionState
	_ = json.Unmarshal([]byte(hs.Snapshot), &st)
	for _, c := range st.Commands {
		if strings.TrimPrefix(c.Name, "/") == name {
			return true
		}
	}
	return false
}

// rawHasHarness: a JSON body names `harness` (schedules and triggers run
// the built-in agent: D-harness §4.2.3).
func rawHasHarness(raw []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	_, ok := m["harness"]
	return ok
}
