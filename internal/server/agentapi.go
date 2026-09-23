package server

// Agent sessions over HTTP (D74, docs/protocol.md §/api/xbin): create one
// on a tile, prompt it, cancel a turn, answer a permission request, replay
// or follow its event log. Live events ride /ws/events as `session`
// events (topic session.<id>), filtered like `term` events to the owner
// and admins. The session itself lives in internal/term (agent.go).

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/term"
)

func (s *Server) registerAgentAPI() {
	s.RegisterAPI("GET /agent/providers", s.apiAgentProviders)
	s.RegisterAPI("POST /term/sessions", s.apiAgentCreate)
	s.RegisterAPI("GET /term/sessions/{id}", s.apiAgentGet)
	s.RegisterAPI("DELETE /term/sessions/{id}", s.apiAgentDelete)
	s.RegisterAPI("POST /term/sessions/{id}/prompt", s.apiAgentPrompt)
	s.RegisterAPI("POST /term/sessions/{id}/cancel", s.apiAgentCancel)
	s.RegisterAPI("POST /term/sessions/{id}/permissions/{pid}", s.apiAgentPermit)
	s.RegisterAPI("POST /term/sessions/{id}/options", s.apiAgentSetOption)
	s.RegisterAPI("GET /term/sessions/{id}/events", s.apiAgentEvents)
	s.RegisterAPI("GET /term/sessions/{id}/log", s.apiAgentLog)
	// past sessions (term/history.go): the persisted transcripts
	s.RegisterAPI("GET /agent/history", s.apiAgentHistory)
	s.RegisterAPI("GET /agent/history/{id}/events", s.apiAgentHistoryEvents)
	s.RegisterAPI("DELETE /agent/history/{id}", s.apiAgentHistoryDelete)
}

// SessionEvent is the Manager.OnEvent hook: every logged agent event, as
// one `session` hub event.
func (s *Server) SessionEvent(cwd string, ev term.SessionEvent) {
	if s.Hub == nil {
		return
	}
	s.Hub.Publish(events.Event{Type: "session", Topic: "session." + ev.ID, Component: cwd, Data: ev})
}

// apiAgentProviders lists what this daemon can run.
func (s *Server) apiAgentProviders(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, agent.Providers())
}

// apiAgentCreate opens an agent session: {cwd, kind:"agent", provider,
// mode?, net?, name?, resume?} → SessionInfo (status starting). resume names a
// past session of the caller's on this tile (GET /agent/history) to reopen
// (session/load); 409 when that agent cannot.
func (s *Server) apiAgentCreate(w http.ResponseWriter, r *http.Request) {
	if s.Term == nil {
		apiErr(w, http.StatusNotImplemented, "terminals are not enabled")
		return
	}
	var body struct {
		Cwd, Kind, Provider, Mode, Net, Name, Model, Resume string
		Options                                             map[string]string
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		apiErr(w, http.StatusBadRequest, "need {cwd, kind:\"agent\", provider, mode?, model?, options?, net?, name?, resume?}")
		return
	}
	if body.Model != "" { // shorthand for options.model
		if body.Options == nil {
			body.Options = map[string]string{}
		}
		body.Options["model"] = body.Model
	}
	if body.Kind != term.KindAgent {
		apiErr(w, http.StatusBadRequest, "kind must be \"agent\" (shell sessions open on /ws/term)")
		return
	}
	if len(body.Name) > 64 {
		body.Name = body.Name[:64]
	}
	info, code, err := s.Term.OpenAgent(auth.PrincipalOf(r), body.Cwd, body.Net, body.Provider, body.Mode, body.Name, body.Resume, body.Options)
	if err != nil {
		apiErr(w, code, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, info)
}

// drive gates the per-session routes: the session's creator (still
// terminal-level on the tile) or an admin. Writes the refusal itself.
func (s *Server) drive(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if s.Term == nil {
		apiErr(w, http.StatusNotImplemented, "terminals are not enabled")
		return "", false
	}
	if err := s.Term.MayDrive(id, auth.PrincipalOf(r)); err != nil {
		apiErr(w, agentStatus(err), err.Error())
		return "", false
	}
	return id, true
}

// agentStatus maps the term package's errors to HTTP statuses.
func agentStatus(err error) int {
	switch {
	case errors.Is(err, term.ErrNoSession), errors.Is(err, term.ErrNoPermission):
		return http.StatusNotFound
	case errors.Is(err, term.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, term.ErrNotAgent), errors.Is(err, agent.ErrBusy), errors.Is(err, agent.ErrEnded), errors.Is(err, agent.ErrResumeUnsupported):
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

// historyScope gates the past-session routes like apiTermSessions: the
// caller's own history, on tiles they may still open a terminal on (admins
// see all of their own). Writes the refusal itself.
func (s *Server) historyScope(w http.ResponseWriter, r *http.Request) (homeKey string, may func(string) bool, ok bool) {
	p := auth.PrincipalOf(r)
	if s.Term == nil || !(p.CanTerminal() || p.Via == "terminal") {
		apiErr(w, http.StatusForbidden, "terminal access required")
		return "", nil, false
	}
	may = p.CanTerminalTileVia
	if p.IsAdmin() {
		may = nil
	}
	return term.HomeKey(p), may, true
}

// apiAgentHistory lists the caller's past agent sessions, newest first
// (?cwd= narrows to a tile): the persisted transcripts (term/history.go).
func (s *Server) apiAgentHistory(w http.ResponseWriter, r *http.Request) {
	homeKey, may, ok := s.historyScope(w, r)
	if !ok {
		return
	}
	WriteJSON(w, http.StatusOK, s.Term.ListHistory(homeKey, r.URL.Query().Get("cwd"), may))
}

// apiAgentHistoryEvents is a past session's transcript, {meta, events} in
// the live /events shape so the same client renders it.
func (s *Server) apiAgentHistoryEvents(w http.ResponseWriter, r *http.Request) {
	homeKey, may, ok := s.historyScope(w, r)
	if !ok {
		return
	}
	meta, evs, err := s.Term.ReadHistory(homeKey, r.PathValue("id"))
	if err != nil || (may != nil && !may(meta.Cwd)) {
		apiErr(w, http.StatusNotFound, "no such past session")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"meta": meta, "events": evs})
}

func (s *Server) apiAgentHistoryDelete(w http.ResponseWriter, r *http.Request) {
	homeKey, may, ok := s.historyScope(w, r)
	if !ok {
		return
	}
	meta, _, err := s.Term.ReadHistory(homeKey, r.PathValue("id"))
	if err != nil || (may != nil && !may(meta.Cwd)) {
		apiErr(w, http.StatusNotFound, "no such past session")
		return
	}
	if err := s.Term.DeleteHistory(homeKey, r.PathValue("id")); err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiAgentGet(w http.ResponseWriter, r *http.Request) {
	id, ok := s.drive(w, r)
	if !ok {
		return
	}
	info, found := s.Term.Info(id)
	if !found {
		apiErr(w, http.StatusNotFound, "no such session")
		return
	}
	out := map[string]any{"session": info}
	if pend, err := s.Term.AgentPending(id); err == nil {
		out["permissions"] = pend
	}
	WriteJSON(w, http.StatusOK, out)
}

// apiAgentDelete ends a session of either kind (the API-side twin of
// DELETE /ws/term?session=).
func (s *Server) apiAgentDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := s.drive(w, r)
	if !ok {
		return
	}
	if !s.Term.Kill(id) {
		apiErr(w, http.StatusNotFound, "no such session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiAgentPrompt starts a turn: {text} → {turn}. 409 while one runs.
func (s *Server) apiAgentPrompt(w http.ResponseWriter, r *http.Request) {
	id, ok := s.drive(w, r)
	if !ok {
		return
	}
	var body struct{ Text string }
	if json.NewDecoder(r.Body).Decode(&body) != nil || strings.TrimSpace(body.Text) == "" {
		apiErr(w, http.StatusBadRequest, "need {text}")
		return
	}
	turn, err := s.Term.AgentPrompt(r.Context(), id, body.Text)
	if err != nil {
		apiErr(w, agentStatus(err), err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "turn": turn})
}

func (s *Server) apiAgentCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := s.drive(w, r)
	if !ok {
		return
	}
	if err := s.Term.AgentCancel(id); err != nil {
		apiErr(w, agentStatus(err), err.Error())
		return
	}
	WriteOK(w)
}

// apiAgentPermit answers a permission request: {optionId} or {decision:
// allow_once|allow_always|reject_once|reject_always}. First answer wins.
func (s *Server) apiAgentPermit(w http.ResponseWriter, r *http.Request) {
	id, ok := s.drive(w, r)
	if !ok {
		return
	}
	var body struct{ OptionID, Decision string }
	if json.NewDecoder(r.Body).Decode(&body) != nil || (body.OptionID == "" && body.Decision == "") {
		apiErr(w, http.StatusBadRequest, "need {optionId} or {decision}")
		return
	}
	p := auth.PrincipalOf(r)
	by := "owner"
	if p.UserID != "" {
		by = "user:" + p.UserID
	}
	if err := s.Term.AgentPermit(id, r.PathValue("pid"), body.OptionID, body.Decision, by); err != nil {
		apiErr(w, agentStatus(err), err.Error())
		return
	}
	WriteOK(w)
}

// apiAgentSetOption changes a session setting the agent advertised
// (model, effort, …): {id, value}. The refreshed options ride the next
// status event.
func (s *Server) apiAgentSetOption(w http.ResponseWriter, r *http.Request) {
	id, ok := s.drive(w, r)
	if !ok {
		return
	}
	var body struct{ ID, Value string }
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.ID == "" {
		apiErr(w, http.StatusBadRequest, "need {id, value}")
		return
	}
	if err := s.Term.AgentSetOption(r.Context(), id, body.ID, body.Value); err != nil {
		apiErr(w, agentStatus(err), err.Error())
		return
	}
	WriteOK(w)
}

// apiAgentEvents replays the log after ?since= (0 = all), or with
// ?follow=1 streams it as NDJSON until the client or the session goes.
func (s *Server) apiAgentEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := s.drive(w, r)
	if !ok {
		return
	}
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	if r.URL.Query().Get("follow") != "1" {
		evs, next, truncated, err := s.Term.AgentEvents(id, since)
		if err != nil {
			apiErr(w, agentStatus(err), err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"events": evs, "next": next, "truncated": truncated})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	cursor := since
	ctx := r.Context()
	first := true
	for {
		// the wait channel first, so an append between the replay and the
		// wait still wakes us
		wait, done, err := s.Term.AgentWait(id)
		if err != nil {
			return
		}
		evs, next, truncated, _ := s.Term.AgentEvents(id, cursor)
		if first && truncated { // the cursor predates the ring: say so before the first event
			_ = enc.Encode(agent.Event{Type: agent.EvGap, Data: json.RawMessage(`{"before":` + strconv.FormatUint(cursor, 10) + `}`)})
		}
		first = false
		for _, e := range evs {
			if enc.Encode(e) != nil {
				return
			}
		}
		if len(evs) > 0 {
			cursor = next
			if fl != nil {
				fl.Flush()
			}
		}
		select {
		case <-wait:
		case <-done:
			if evs, _, _, err := s.Term.AgentEvents(id, cursor); err == nil { // the last words
				for _, e := range evs {
					_ = enc.Encode(e)
				}
			}
			return
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) apiAgentLog(w http.ResponseWriter, r *http.Request) {
	id, ok := s.drive(w, r)
	if !ok {
		return
	}
	text, err := s.Term.AgentLog(id)
	if err != nil {
		apiErr(w, agentStatus(err), err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(text))
}
