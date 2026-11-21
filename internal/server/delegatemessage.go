package server

// delegatemessage.go — a message to a tile agent that acts without asking,
// as an approval (D250, D251; plans/wsagent/approvals-ux.md §5c).
//
// A session the person's agent started takes the agent's messages freely
// only while its mode asks before every edit and command: the mode is the
// guard. One whose mode doesn't ask — the default of the sessions it
// starts (D251), whatever mode its person approved it in — takes none
// unapproved (delegatesend.go modeRefusal): such a session acts on what it
// is told, so a message from an agent that can be prompt-injected would be
// an act nobody approved. The way forward: the agent asks its person for
// that one message — a request of
// kind "message" for POST /term/sessions/<id>/prompt or …/queue, the whole
// text shown — and the person's approval sends exactly that text, once, to
// that session: refused, spending nothing, when the session ended, is no
// longer this agent's, or is now on another coding agent or mode than the
// person saw.

import (
	"cmp"
	"context"
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
	var b messageBody
	if err := StrictDecode(body, &b); err != nil || strings.TrimSpace(b.Text) == "" { // read one way only (D252)
		why := "no text"
		if err != nil {
			why = err.Error()
		}
		return nil, "", http.StatusBadRequest, `a message for approval is {"text"}: the whole of what is sent, nothing else, written once in lower case (` + why + ")"
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
		return nil, "", http.StatusBadRequest, "this session asks its person before it acts: send the message — it needs no approval"
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
	out := map[string]any{"session": id, "provider": info.Provider, "providerName": cmp.Or(prov.Name, info.Provider), "mode": info.Mode, "asks": false,
		"model": info.Model, "name": info.Name, "status": info.Status, "text": b.Text}
	if sid := s.Term.SigninIDOf(id); sid != "" {
		out["signInId"] = sid // a fact of the session, shown as one (D251), as a continue's
	}
	return out, tile, 0, ""
}

// messageTicketKey carries, to the handler, the message ticket spent for
// the call (a spentMessage): delegateSends lets that one message past the
// mode rule, and the text that goes is the one its person was shown.
type messageTicketKey struct{}

// spentMessage is a spent message ticket: the session it was for and the
// text its person approved — xbind's own reading of the body when it was
// filed (preview.message.text), which the session is sent whatever a later
// reading of the bytes would say (D252).
type spentMessage struct{ session, text string }

// messageApproved reports that r carries a spent message ticket for
// session id.
func messageApproved(r *http.Request, id string) bool {
	_, ok := approvedMessage(r, id)
	return ok
}

// approvedMessage is the text r's spent message ticket for session id
// approved (ok false: none).
func approvedMessage(r *http.Request, id string) (string, bool) {
	m, _ := r.Context().Value(messageTicketKey{}).(spentMessage)
	return m.text, m.session != "" && m.session == id
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

// withMessageTicket is r carrying the spent message ticket req, for
// session id: the text it approved goes.
func withMessageTicket(r *http.Request, id string, req delegation.Request) {
	shown, _ := req.Preview["message"].(map[string]any)
	*r = *r.WithContext(context.WithValue(r.Context(), messageTicketKey{}, spentMessage{session: id, text: asString(shown["text"])}))
}

// approvedPrompt is the prompt a message call sends: with a message ticket
// spent for session id, the text its person approved and nothing else (the
// body, read again, can't say another — D252); otherwise p as read.
func approvedPrompt(r *http.Request, id string, p agent.Prompt) agent.Prompt {
	if text, ok := approvedMessage(r, id); ok {
		return agent.Prompt{Text: text}
	}
	return p
}
