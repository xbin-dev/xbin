package broker

// agentpagealert.go — the one notice that names the workspace agent's page
// (D232; internal/server agentpage.go): a shell from before the agent's
// chip shows GET /alerts' messages and their Dismiss, so that is how its
// person learns /xbin/agent exists. The server decides whether a person
// gets it (workspace agents on, an agent to show, not dismissed by them);
// the broker decides who the person behind a request is.

import (
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
)

// alertPerson is the person a request's alerts are for: their own session,
// app or device, or a trusted chrome frame minted under their own login —
// the shell they drive (chromeFrameDriver). Not the root token (it is
// nobody's agent's person), a view-as session, a tile's frame, a terminal,
// or the agent itself.
func (b *Broker) alertPerson(p auth.Principal) (user string, ok bool) {
	if p.Agent != "" {
		return "", false
	}
	if ownSession(p) { // (agentpolicy.go: not a view-as session, a terminal's or an agent session's token)
		return p.UserID, p.UserID != ""
	}
	if d, ok := b.chromeFrameDriver(p); ok && d.UserID != "" {
		return d.UserID, true
	}
	return "", false
}

func (b *Broker) agentPageAlert(p auth.Principal) []Alert {
	user, ok := b.alertPerson(p)
	if !ok || b.agentPage == nil {
		return nil
	}
	msg, ok := b.agentPage.AgentPageNotice(user)
	if !ok {
		return nil
	}
	return []Alert{{Level: "warn", Kind: server.AgentPageAlertKind, Message: msg, Dismiss: server.AgentPageDismissRoute}}
}

// apiAgentPageDismiss — POST /delegate/page/dismiss: the notice is gone for
// the person, for good. Their own session, or the shell they drive (which
// shows the alert with its Dismiss and posts with its own token). Nothing
// else of their agent changes.
func (b *Broker) apiAgentPageDismiss(w http.ResponseWriter, r *http.Request) {
	user, ok := b.alertPerson(auth.PrincipalOf(r))
	if !ok || b.agentPage == nil {
		server.WriteError(w, http.StatusForbidden, "a person dismisses their own notice: sign in, or use the shell", "/docs/protocol/delegation.md")
		return
	}
	if err := b.agentPage.AgentPageSeen(user); err != nil {
		server.WriteError(w, http.StatusInternalServerError, "the notice can't be dismissed: the agent's settings can't be written", "/docs/protocol/delegation.md")
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
