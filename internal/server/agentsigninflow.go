package server

// agentsigninflow.go — the guided sign-in's routes (D230; the flows:
// internal/signins/flow.go). xbind runs the coding agent's own sign-in in
// a throwaway sandbox; these routes show its person the page to open and
// take the code back. The credential the CLI prints goes from xbind's
// reader into the store: no answer here carries it.
//
//	POST   /api/xbin/agent/signins/guided          {harness, name?, default?} → 202 {flow}
//	POST   /api/xbin/agent/signins/guided/{flow}   {code} → 201 {signin} | 409 (the code again) | 502 | 504
//	DELETE /api/xbin/agent/signins/guided/{flow}   → {ok}

import (
	"net/http"
)

func (s *Server) signinFlowsOK(w http.ResponseWriter) bool {
	if s.SigninFlows == nil {
		WriteError(w, http.StatusNotImplemented, "this xbind runs no guided sign-ins", agentSigninsDocs)
		return false
	}
	return true
}

func (s *Server) apiSigninsGuidedStart(w http.ResponseWriter, r *http.Request) {
	user := s.signinsWritable(w, r)
	if user == "" || !s.signinFlowsOK(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var body struct {
		Harness string `json:"harness"`
		Name    string `json:"name"`
		Default bool   `json:"default"`
	}
	if err := DecodeJSON(r, &body); err != nil || body.Harness == "" {
		WriteError(w, http.StatusBadRequest, "need {harness, name?, default?}", agentSigninsDocs)
		return
	}
	fl, err := s.SigninFlows.Start(r.Context(), user, body.Harness, body.Name, body.Default)
	if err != nil {
		writeSigninsErr(w, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, map[string]any{"flow": fl})
}

func (s *Server) apiSigninsGuidedCode(w http.ResponseWriter, r *http.Request) {
	user := s.signinsWritable(w, r)
	if user == "" || !s.signinFlowsOK(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var body struct {
		Code string `json:"code"`
	}
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, http.StatusBadRequest, "need {code}", agentSigninsDocs)
		return
	}
	sg, replaced, err := s.SigninFlows.Code(user, r.PathValue("flow"), body.Code)
	if err != nil {
		writeSigninsErr(w, err)
		return
	}
	s.signinSaved(r, user, sg, replaced, "guided")
	WriteJSON(w, http.StatusCreated, map[string]any{"signin": sg})
}

func (s *Server) apiSigninsGuidedCancel(w http.ResponseWriter, r *http.Request) {
	user := s.signinsPerson(w, r)
	if user == "" || !s.signinFlowsOK(w) {
		return
	}
	if !s.SigninFlows.Cancel(user, r.PathValue("flow")) {
		WriteError(w, http.StatusNotFound, "no sign-in of yours is under way with that id", agentSigninsDocs)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
