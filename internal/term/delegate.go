package term

// delegate.go — tile agent sessions a person's workspace agent opens for
// them (a delegate principal, auth/delegate.go; docs/auth.md §Delegation).
//
// A delegate's open passes the person's own gates first — terminal level on
// the tile, the target and partition rules — because the principal is the
// person's. Then it is narrowed, since POST /term/sessions mixes route
// classes: the same body opens a session that asks its person before
// acting, or one that never asks, and a delegate can never answer a tile
// agent's permission request (internal/server/delegateclass.go). So mode,
// network and API pickers come from one of two places and never from the
// agent's say-so:
//
//   - a TICKET (DelegateStart.Approved): the person approved exactly this
//     start, having been shown its provider, mode, network and API access.
//     Any value they could pick themselves;
//   - their PREFERENCES (DelegateStart.Prefs): they let their agent start
//     sessions without asking. The pickers are the preference's; the mode
//     is the preference's or the provider's default, asking, mode.
//
// In both cases the session targets the tile's default deployment, the
// tile's org must allow the workspace agent and this harness there
// (Manager.AgentPolicy), and the session is marked: SessionInfo.Origin
// "agent". A delegate drives — prompts, cancels, ends — only the sessions
// it opened (AgentOrigin); the person drives them too, and answers
// everything they ask.

import (
	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
)

// OriginAgent is SessionInfo.Origin of a session a person's workspace agent
// opened for them.
const OriginAgent = "agent"

func originOf(holder string) string {
	if holder != "" {
		return OriginAgent
	}
	return ""
}

// DelegateStart is how a delegate's open was authorised: the server's
// delegate gate decides it and nothing a caller sends does.
type DelegateStart struct {
	// Approved: the person approved this start (a ticket bound to this
	// request's body).
	Approved bool
	// Prefs: the person's preferences for sessions their agent starts
	// without asking; nil when they allow none.
	Prefs *delegation.StartPrefs
}

// AgentOrigin is the sealed component that opened live session id for its
// person; "" when the person opened it themselves, and for an unknown id.
func (m *Manager) AgentOrigin(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[id]; s != nil {
		return s.origin
	}
	return ""
}

// delegateOpen applies the file comment's rules to delegate p's open a on
// tile rel: the mode and network the session opens with (a.NoAPI is set in
// place), or why it is refused. resumed is the past session being reopened,
// if any.
func (m *Manager) delegateOpen(p auth.Principal, a *AgentOpen, rel string, prov agent.Provider, mode, net string, resumed *HistoryMeta) (string, string, string) {
	const who = "a workspace agent "
	d := a.Delegate
	switch {
	case a.Deployment != "":
		return "", "", who + "opens a tile agent on the tile's default deployment: another target is the person's to pick"
	case d == nil || !d.Approved && d.Prefs == nil:
		return "", "", who + "starts a tile agent with its person's approval: ask for one (POST /api/xbin/delegate/requests), or the person allows starts without asking in their agent preferences"
	}
	if err := delegation.PolicyOr(m.AgentPolicy).SessionAllows(delegation.Subject{Holder: p.Agent, User: p.UserID, Tile: rel, Model: p.AgentModel}, prov.ID); err != nil {
		return "", "", err.Error()
	}
	if d.Approved {
		if mode == "" && resumed != nil {
			mode = resumed.Mode
		}
		return mode, net, "" // what the person saw and approved; their own gates decide the rest
	}
	// the person's preferences: nothing the agent sent picks the sandbox
	switch {
	case a.VM || a.GPU != "":
		return "", "", who + "starting without asking opens the tile's ordinary sandbox: a VM or GPU sandbox needs the person's approval"
	case len(a.Options) > 1 || len(a.Options) == 1 && a.Options["model"] == "":
		// an agent's settings can carry its permission mode under another id
		return "", "", who + "starting without asking may choose the tile agent's model: every other option needs the person's approval"
	case resumed != nil && resumed.Agent != p.Agent:
		return "", "", who + "resumes without asking only the sessions it started: this one needs the person's approval"
	}
	mode = d.Prefs.Mode
	if mode == "" {
		mode = prov.DefaultMode
		if !prov.Safe(mode) {
			return "", "", who + "starting without asking needs a mode known to ask its person: choose one for " + prov.ID + " in the agent preferences"
		}
	}
	a.NoAPI = !d.Prefs.API
	return mode, d.Prefs.Net, ""
}
