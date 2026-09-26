package server

// Agent sessions over HTTP (D74, docs/protocol.md §/api/xbin): create one
// on a tile, prompt it, cancel a turn, answer a permission request, replay
// or follow its event log. Live events ride /ws/events as `session`
// events (topic session.<id>), filtered like `term` events to the owner
// and admins. The session itself lives in internal/term (agent.go).

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	s.RegisterAPI("POST /term/sessions/{id}/restart", s.apiAgentRestart)
	s.RegisterAPI("POST /term/sessions/{id}/permissions/{pid}", s.apiAgentPermit)
	s.RegisterAPI("POST /term/sessions/{id}/elicitations/{eid}", s.apiAgentElicit)
	s.RegisterAPI("POST /term/sessions/{id}/options", s.apiAgentSetOption)
	s.RegisterAPI("GET /term/sessions/{id}/events", s.apiAgentEvents)
	s.RegisterAPI("GET /term/sessions/{id}/log", s.apiAgentLog)
	s.RegisterAPI("GET /term/sessions/{id}/diff", s.apiAgentDiff) // the full patch behind a files.changed
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
// mode?, net?, api?, gpu?, vm?, name?, resume?} → SessionInfo (status starting).
// net/api/gpu/vm are the sandbox pickers a shell's socket takes (api false = a
// code-only sandbox, no terminal token; vm true = a VM sandbox). resume names a past session of the
// caller's on this tile (GET /agent/history) to reopen (session/load); 409
// when that agent cannot.
func (s *Server) apiAgentCreate(w http.ResponseWriter, r *http.Request) {
	if s.Term == nil {
		apiErr(w, http.StatusNotImplemented, "terminals are not enabled")
		return
	}
	var body struct {
		Cwd, Kind, Provider, Mode, Net, GPU, Name, Model, Resume string
		API                                                      *bool
		VM                                                       bool
		Options                                                  map[string]string
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		apiErr(w, http.StatusBadRequest, "need {cwd, kind:\"agent\", provider, mode?, model?, options?, net?, api?, gpu?, vm?, name?, resume?}")
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
	info, code, err := s.Term.OpenAgentWith(auth.PrincipalOf(r), term.AgentOpen{Cwd: body.Cwd, Net: body.Net, GPU: body.GPU,
		NoAPI: body.API != nil && !*body.API, VM: body.VM, Provider: body.Provider, Mode: body.Mode, Name: body.Name, Resume: body.Resume, Options: body.Options})
	if err != nil {
		apiErr(w, code, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, info)
}

// apiAgentRestart restarts an agent session with other sandbox pickers
// {net?, api?, gpu?, vm?} (fixed when a sandbox starts; vm absent = keep): the session ends and a
// new one opens on the same tile with the same provider, mode, settings and
// name — resuming the conversation where the agent can reopen its own
// session. Creator only (the new session is the caller's). → {session,
// resumed}.
func (s *Server) apiAgentRestart(w http.ResponseWriter, r *http.Request) {
	id, ok := s.drive(w, r)
	if !ok {
		return
	}
	var body struct {
		Net, GPU string
		API, VM  *bool
	}
	if r.ContentLength != 0 && json.NewDecoder(r.Body).Decode(&body) != nil {
		apiErr(w, http.StatusBadRequest, "need {net?, api?, gpu?, vm?}")
		return
	}
	info, resumed, code, err := s.Term.RestartAgent(auth.PrincipalOf(r), id, body.Net, body.GPU, body.API == nil || *body.API, body.VM)
	if err != nil {
		apiErr(w, code, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"session": info, "resumed": resumed})
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
	case errors.Is(err, term.ErrNoSession), errors.Is(err, term.ErrNoPermission), errors.Is(err, term.ErrNoQuestion), errors.Is(err, term.ErrNoDiff):
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
	if qs, err := s.Term.AgentQuestions(id); err == nil {
		out["elicitations"] = qs
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

// maxPromptBody bounds a prompt's request: the attachments' 20 MiB as
// base64, plus the text.
const maxPromptBody = 32 << 20

// apiAgentPrompt starts a turn: {text, attachments?:[{name, mime, data}]}
// → {turn}. 409 while one runs; 413 past the attachment limits.
func (s *Server) apiAgentPrompt(w http.ResponseWriter, r *http.Request) {
	id, ok := s.drive(w, r)
	if !ok {
		return
	}
	p, code, err := decodePrompt(http.MaxBytesReader(w, r.Body, maxPromptBody))
	if err != nil {
		apiErr(w, code, err.Error())
		return
	}
	turn, err := s.Term.AgentPromptWith(r.Context(), id, p)
	if err != nil {
		apiErr(w, agentStatus(err), err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "turn": turn})
}

// decodePrompt reads a prompt body: the text, and the attachments decoded
// (standard base64, padded or not) and normalised
// (agent.PrepareAttachments). The int is the status of a refusal.
func decodePrompt(body io.Reader) (agent.Prompt, int, error) {
	var b struct {
		Text        string `json:"text"`
		Attachments []struct {
			Name string `json:"name"`
			Mime string `json:"mime"`
			Data string `json:"data"`
		} `json:"attachments"`
	}
	if err := json.NewDecoder(body).Decode(&b); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return agent.Prompt{}, http.StatusRequestEntityTooLarge, fmt.Errorf("the prompt is over %d MiB (attachments: at most %d MiB together)", maxPromptBody>>20, agent.MaxAttachmentsBytes>>20)
		}
		return agent.Prompt{}, http.StatusBadRequest, errors.New("need {text, attachments?:[{name, mime, data (base64)}]}")
	}
	if strings.TrimSpace(b.Text) == "" && len(b.Attachments) == 0 {
		return agent.Prompt{}, http.StatusBadRequest, errors.New("need {text}")
	}
	if len(b.Attachments) > agent.MaxAttachments {
		return agent.Prompt{}, http.StatusBadRequest, fmt.Errorf("%d attachments (at most %d)", len(b.Attachments), agent.MaxAttachments)
	}
	atts := make([]agent.Attachment, len(b.Attachments))
	for i, a := range b.Attachments {
		data, err := base64.StdEncoding.DecodeString(a.Data)
		if err != nil {
			if data, err = base64.RawStdEncoding.DecodeString(a.Data); err != nil {
				return agent.Prompt{}, http.StatusBadRequest, fmt.Errorf("attachment %d (%s): data is not base64", i+1, a.Name)
			}
		}
		atts[i] = agent.Attachment{Name: a.Name, Mime: a.Mime, Data: data}
	}
	atts, err := agent.PrepareAttachments(atts)
	if err != nil {
		if errors.Is(err, agent.ErrAttachmentTooLarge) {
			return agent.Prompt{}, http.StatusRequestEntityTooLarge, err
		}
		return agent.Prompt{}, http.StatusBadRequest, err
	}
	if len(atts) == 0 {
		atts = nil
	}
	return agent.Prompt{Text: b.Text, Attachments: atts}, 0, nil
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

// apiAgentElicit answers a question the agent asked (elicitation.request):
// {action: accept | decline | cancel, content?} — content the form's values
// on accept. First answer wins (404 after).
func (s *Server) apiAgentElicit(w http.ResponseWriter, r *http.Request) {
	id, ok := s.drive(w, r)
	if !ok {
		return
	}
	var body struct {
		Action  string          `json:"action"`
		Content json.RawMessage `json:"content"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Action == "" {
		apiErr(w, http.StatusBadRequest, "need {action: accept|decline|cancel, content?}")
		return
	}
	if len(body.Content) > 0 && string(body.Content) != "null" && body.Content[0] != '{' {
		apiErr(w, http.StatusBadRequest, "content must be an object (the form's values)")
		return
	}
	p := auth.PrincipalOf(r)
	by := "owner"
	if p.UserID != "" {
		by = "user:" + p.UserID
	}
	if err := s.Term.AgentElicit(id, r.PathValue("eid"), body.Action, body.Content, by); err != nil {
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

// apiAgentDiff is the complete patch behind a files.changed event (whose
// own patch is capped): ?toolCallId=<id> | ?turn=<n>, ?path=<file> narrows
// it to one file → text/x-diff (a git patch; X-Truncated: true when cut at
// 16 MiB). 404 when the session kept no snapshot for it.
func (s *Server) apiAgentDiff(w http.ResponseWriter, r *http.Request) {
	id, ok := s.drive(w, r)
	if !ok {
		return
	}
	tool, turn, err := diffQuery(r.URL.Query())
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	patch, truncated, err := s.Term.AgentDiff(r.Context(), id, tool, turn, r.URL.Query().Get("path"))
	if err != nil {
		apiErr(w, agentStatus(err), err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if truncated {
		w.Header().Set("X-Truncated", "true")
	}
	_, _ = w.Write(patch)
}

// diffQuery reads the diff route's selector: exactly one of toolCallId and
// turn (a positive turn number).
func diffQuery(q url.Values) (tool string, turn int64, err error) {
	tool, turnArg := q.Get("toolCallId"), q.Get("turn")
	if (tool == "") == (turnArg == "") {
		return "", 0, errors.New("need ?toolCallId= or ?turn= (from a files.changed event), not both")
	}
	if turnArg != "" {
		if turn, err = strconv.ParseInt(turnArg, 10, 64); err != nil || turn < 1 {
			return "", 0, errors.New("turn must be a turn number (turn.end's turn)")
		}
	}
	return tool, turn, nil
}
