package server

// agentprefsapi.go — a person's agent preferences (D229; the store and the
// shape: internal/agentprefs; docs/protocol/agents.md):
//
//	GET /api/xbin/agent/prefs   the caller's preferences, defaults filled in
//	PUT /api/xbin/agent/prefs   a JSON merge patch of them → the preferences now
//
// Both are a person's own act (partitionclass.go: PersonOnly — their
// session, app or device; never a tile's credentials, view-as or the root
// token): the preferences say what the person's workspace agent may start
// without asking, so nothing but the person writes them, and the agent is
// handed them by xbind (agentprefs.Store), never over this route.

import (
	"io"
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
)

const agentPrefsDocs = "/docs/protocol/agents.md"

func (s *Server) registerAgentPrefsAPI() {
	s.RegisterAPI("GET /agent/prefs", s.apiAgentPrefsGet)
	s.RegisterAPI("PUT /agent/prefs", s.apiAgentPrefsPut)
}

// agentPrefsPerson is the person the request is, or "" after it answered
// why there is none. The partition table already refused every credential
// that isn't a person's own; this holds when the handler is reached some
// other way.
func (s *Server) agentPrefsPerson(w http.ResponseWriter, r *http.Request) string {
	p := auth.PrincipalOf(r)
	switch {
	case s.AgentPrefs == nil:
		WriteError(w, http.StatusNotImplemented, "agent preferences are not enabled", agentPrefsDocs)
	case p.Component != "" || p.Impersonator != "" || p.UserID == "" || p.Via == "terminal" || p.Via == "instance" || p.Via == "frame":
		// (a terminal's or an agent session's token names its person, and is
		// never their own session: an agent must not set what it may do)
		WriteError(w, http.StatusForbidden, "agent preferences are a person's own: sign in and set them yourself — a tile's credentials can't", agentPrefsDocs)
	default:
		return p.UserID
	}
	return ""
}

func (s *Server) apiAgentPrefsGet(w http.ResponseWriter, r *http.Request) {
	if id := s.agentPrefsPerson(w, r); id != "" {
		WriteJSON(w, http.StatusOK, s.AgentPrefs.Get(id))
	}
}

func (s *Server) apiAgentPrefsPut(w http.ResponseWriter, r *http.Request) {
	id := s.agentPrefsPerson(w, r)
	if id == "" {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		WriteError(w, http.StatusRequestEntityTooLarge, "agent preferences are at most 64 KiB", agentPrefsDocs)
		return
	}
	out, err := s.AgentPrefs.Update(id, body)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error(), agentPrefsDocs)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}
