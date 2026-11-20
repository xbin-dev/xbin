package server

// delegatemessage.go — a message to a tile agent that acts without asking,
// as an approval (D250; plans/wsagent/approvals-ux.md §5c).
//
// A session the person's agent started takes the agent's messages freely
// while its mode asks before every edit and command: the mode is the guard.
// One whose mode doesn't ask — the person switched it, or approved it so —
// takes none (delegatesend.go modeRefusal): such a session acts on what it
// is told, so a message from an agent that can be prompt-injected would be
// an act nobody approved. That rule stands. What changes is the way
// forward: the agent asks its person for that one message — a request of
// kind "message" for POST /term/sessions/<id>/prompt or …/queue, the whole
// text shown — and the person's approval sends exactly that text, once, to
// that session: refused, spending nothing, when the session ended, is no
// longer this agent's, or is now on another coding agent or mode than the
// person saw.

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/term"
)

// messagePatterns are the routes a message request is for.
var messagePatterns = map[string]bool{"POST /term/sessions/{id}/prompt": true, "POST /term/sessions/{id}/queue": true}

// sessionOfPath is the session id of /api/xbin/term/sessions/<id>/… ("" none).
func sessionOfPath(path string) string {
	rest, ok := strings.CutPrefix(path, "/api/xbin/term/sessions/")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	return id
}

// messageTarget is session id as p's agent may message it: live, the
// person's, one this agent started — or why not, with a status.
func (s *Server) messageTarget(p auth.Principal, id string) (term.SessionInfo, int, string) {
	if s.Term == nil {
		return term.SessionInfo{}, http.StatusNotFound, "no such session"
	}
	info, live := s.Term.Info(id)
	if !live {
		return info, http.StatusNotFound, "that session ended: a message reaches only a live one"
	}
	mine := false
	for _, si := range s.Term.ListFor(term.HomeKey(personOf(p)), "", nil) {
		mine = mine || si.ID == id
	}
	if !mine || s.Term.AgentOrigin(id) != p.Agent || p.Agent == "" {
		return info, http.StatusForbidden, "a workspace agent drives the sessions it started: this one is its person's own"
	}
	return info, 0, ""
}

// messagePreview is what a person is shown of a message their agent asks
// to send a session that acts without asking — or why it isn't asked:
// the session isn't the agent's to message, the message needs no approval
// (its mode asks), or nothing approves it (a harness command, the
// conversation's taint).
func (s *Server) messagePreview(p auth.Principal, path string, body []byte) (map[string]any, string, int, string) {
	var b struct {
		Text string `json:"text"`
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if dec.Decode(&b) != nil || strings.TrimSpace(b.Text) == "" {
		return nil, "", http.StatusBadRequest, "a message for approval is {text}: the whole of what is sent, nothing else"
	}
	id := sessionOfPath(path)
	info, status, why := s.messageTarget(p, id)
	if why != "" {
		return nil, "", status, why
	}
	if why := harnessCommand(b.Text); why != "" {
		return nil, "", http.StatusForbidden, why
	}
	if s.sessionModeRefusal(id, info, p.UserID) == "" {
		return nil, "", http.StatusBadRequest, "this session asks its person before it acts (or runs in the mode they approved it in): send the message — it needs no approval"
	}
	t := sendTargetOf(info)
	if t.served == "connector" {
		t.model = s.Term.AgentConnectorModel(id)
	}
	if why := s.sendRefusal(p, t); why != "" {
		return nil, "", http.StatusForbidden, why
	}
	tile := s.sessionTile(info.Cwd)
	prov, _ := agent.Lookup(info.Provider)
	return map[string]any{"session": id, "provider": info.Provider, "providerName": cmp.Or(prov.Name, info.Provider), "mode": info.Mode, "asks": false,
		"model": info.Model, "name": info.Name, "status": info.Status, "text": b.Text}, tile, 0, ""
}

// messageTicketKey carries, to the handler, the session a message ticket
// was spent for: delegateSends lets that one message past the mode rule.
type messageTicketKey struct{}

// messageApproved reports that r carries a spent message ticket for
// session id.
func messageApproved(r *http.Request, id string) bool {
	sid, _ := r.Context().Value(messageTicketKey{}).(string)
	return sid != "" && sid == id
}

// messageStill is why an approved message can't go as approved, now —
// before its ticket is spent: the session ended, isn't this agent's, or is
// on another coding agent or mode than the person saw.
func (s *Server) messageStill(p auth.Principal, r delegation.Request, path string) string {
	shown, _ := r.Preview["message"].(map[string]any)
	sid, _ := shown["session"].(string)
	if sid == "" || sid != sessionOfPath(path) {
		return "this approval is of a message to another session: make exactly the approved call"
	}
	info, _, why := s.messageTarget(p, sid)
	if why != "" {
		return "the session your person approved this message to " + strings.TrimPrefix(why, "that session ") + ": file it again if it is still wanted"
	}
	if info.Provider != shown["provider"] || info.Mode != shown["mode"] {
		return "the session your person approved this message to is now " + info.Provider + " in mode " + quoteOr(info.Mode, "(its own)") + ", not " +
			strings.TrimSpace(asString(shown["provider"])) + " in mode " + quoteOr(asString(shown["mode"]), "(its own)") + " as approved: file it again"
	}
	return ""
}

func asString(v any) string { s, _ := v.(string); return s }

// withMessageTicket is r carrying the spent message ticket of session id.
func withMessageTicket(r *http.Request, id string) {
	*r = *r.WithContext(context.WithValue(r.Context(), messageTicketKey{}, id))
}
