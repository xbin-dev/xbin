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
//     start, having been shown the harness, mode, options, network and API
//     access it AMOUNTS TO (DelegateEffective: a resumed session's harness
//     and mode come from its history entry). Any value they could pick
//     themselves — and the open is refused if it would be anything else;
//   - their PREFERENCES (DelegateStart.Prefs): they let their agent start
//     sessions without asking. The pickers are the preference's; the mode
//     is the preference's or the provider's default, asking, mode.
//
// In both cases no option moves the mode (acp.Config.SkipModeOptions: an
// agent's config option of category mode is applied after the mode), a
// resumed session is one this agent started, the tile is no sealed
// component, the session targets the tile's default deployment, the
// tile's org must allow the workspace agent and this harness there
// (Manager.AgentPolicy), and the session is marked: SessionInfo.Origin
// "agent". A delegate drives — prompts, cancels, ends — only the sessions
// it opened (AgentOrigin); the person drives them too, and answers
// everything they ask.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/util"
	sdkacp "github.com/xbin-dev/xbin/sdk/acp"
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
	// request's body). Provider and Mode are the EFFECTIVE harness and mode
	// they were shown (DelegateEffective: what the body and the history
	// entry it resumes add up to); the open is refused if it would be
	// anything else.
	Approved       bool
	Provider, Mode string
	// Net is the network the person was shown, used when the request names
	// none (xbind resolved it from their preferences: the request's preview).
	Net string
	// Prefs: the person's preferences for sessions their agent starts
	// without asking; nil when they allow none.
	Prefs *delegation.StartPrefs
}

// EffectiveStart is what a delegate's session start amounts to once the
// history entry it resumes is counted: what its person is shown and
// approves.
type EffectiveStart struct {
	Tile     string
	Provider agent.Provider
	Mode     string // resolved: never ""
	Resumes  bool
}

// DelegateEffective computes it for delegate p's open a, with the refusals
// a start would meet anyway: no terminal level, an unknown harness or mode,
// a history entry that isn't this tile's, can't be reopened, or wasn't a
// session this agent started. The int is the HTTP status of a refusal.
func (m *Manager) DelegateEffective(p auth.Principal, a AgentOpen) (EffectiveStart, int, error) {
	_, rel, err := util.SafeJoin(m.Root, a.Cwd)
	if a.Cwd == "" || err != nil || rel == "" || !p.CanTerminalTileVia(rel) {
		return EffectiveStart{}, 403, fmt.Errorf("%s doesn't have terminal access to %s: their agent can't start a tile agent there", p.UserID, strings.Trim(a.Cwd, "/"))
	}
	if auth.HoldsDelegation(rel) {
		return EffectiveStart{}, 403, errors.New("a workspace agent starts no tile agent on a system component")
	}
	providerID, mode := a.Provider, a.Mode
	if a.Resume != "" {
		meta, _, err := m.historyMetaFrom(HistoryOf(p), a.Resume)
		switch {
		case err != nil:
			return EffectiveStart{}, 404, fmt.Errorf("no such past session %q", a.Resume)
		case meta.Cwd != rel:
			return EffectiveStart{}, 400, errors.New("a past session resumes on the tile it ran on")
		case meta.Agent != p.Agent:
			return EffectiveStart{}, 403, errors.New(resumeOwn)
		case !meta.Loadable || meta.ACPSessionID == "":
			return EffectiveStart{}, 409, agent.ErrResumeUnsupported
		}
		providerID = meta.Provider
		if mode == "" {
			mode = meta.Mode
		}
	}
	prov, ok := agent.Lookup(providerID)
	if !ok {
		return EffectiveStart{}, 400, fmt.Errorf("unknown provider %q", providerID)
	}
	if mode, err = prov.ResolveMode(mode); err != nil {
		return EffectiveStart{}, 400, err
	}
	return EffectiveStart{Tile: rel, Provider: prov, Mode: mode, Resumes: a.Resume != ""}, 0, nil
}

// resumeOwn: a delegate reopens only what it opened — a past session of
// the person's own may have run in a mode that asks nothing, and its
// transcript is theirs.
const resumeOwn = "a workspace agent resumes only the sessions it started: this one is its person's own"

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

// DelegateStartSource is who would serve the model calls of an agent
// session of harness started for userID on tile rel, as far as xbind can
// vouch for it before it exists: held reports that the agents policy gives
// the session the LLM connector alone (CredentialRule.Forced — the
// workspace's connector-only mode, or a model rule on the tile), and
// models are the ones it may start on: the model the start asks for
// (asked; checked against the connector's when the session opens) and the
// connector's default for a harness, which it runs on when the start names
// none or the harness doesn't take the one named. Not held: nothing says
// which model it will call (the person's own sign-in, or a choice).
func (m *Manager) DelegateStartSource(userID, rel, harness, asked string) (held bool, models []string) {
	rule := m.credentialRule(openOpts{userID: userID}, rel, harness, "")
	if !rule.Forced {
		return false, nil
	}
	if asked != "" {
		models = append(models, asked)
	}
	if m.LLM != nil {
		if offer, ok := m.LLM.Offer(context.Background(), llmUser(openOpts{userID: userID}), rel, KindAgent); ok && offer.Model != "" && offer.Model != asked {
			models = append(models, offer.Model)
		}
	}
	return true, models
}

// AgentConnectorModel is the connector's model that live agent session id
// calls, as far as xbind knows: the one its model setting names, when that
// is one of the connector's as the harness writes it (ModelPrefix + id),
// else the connector's default, which the CLI was started on — a harness's
// own name for its default ("fake-default") is no model of the
// connector's. "" for a session the connector doesn't serve, and for an
// unknown id.
func (m *Manager) AgentConnectorModel(id string) string {
	_, st, err := m.agentOf(id)
	if err != nil || st.llm == nil || st.llmServes() != sdkacp.ServeConnector {
		return ""
	}
	st.mu.Lock()
	cur, k := st.model, st.provider.Connector
	st.mu.Unlock()
	if k != nil && cur != "" {
		for _, cm := range st.llm.offer.Models {
			if cur == k.ModelPrefix+cm.ID {
				return cm.ID
			}
		}
	}
	return st.llm.offer.Model
}

// AgentOriginMode is the mode live session id was opened in by the sealed
// component that started it for its person — the mode the person approved
// on the start's card, or their preference's; "" for a session they opened
// themselves, and for an unknown id.
func (m *Manager) AgentOriginMode(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[id]; s != nil && s.origin != "" {
		return s.originMode
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
	switch {
	case auth.HoldsDelegation(rel):
		return "", "", who + "starts no tile agent on a system component"
	case resumed != nil && resumed.Agent != p.Agent:
		return "", "", resumeOwn
	}
	if d.Approved {
		if mode == "" && resumed != nil {
			mode = resumed.Mode
		}
		// what the person saw and approved: the caller checks the provider
		// and the resolved mode against it; their own gates decide the rest.
		// What the request left out is what the card showed in its place.
		return cmp.Or(mode, d.Mode), cmp.Or(net, d.Net), ""
	}
	// the person's preferences: nothing the agent sent picks the sandbox
	switch {
	case a.VM || a.GPU != "":
		return "", "", who + "starting without asking opens the tile's ordinary sandbox: a VM or GPU sandbox needs the person's approval"
	case len(a.Options) > 1 || len(a.Options) == 1 && a.Options["model"] == "":
		// an agent's settings can carry its permission mode under another id
		return "", "", who + "starting without asking may choose the tile agent's model: every other option needs the person's approval"
	}
	if d.Provider != prov.ID {
		// the preference was read for the harness the request named; what
		// opens (a resumed session's harness, say) is another
		return "", "", who + "starts without asking only the harness its person's preference is for: that preference is for " + cmp.Or(d.Provider, "none") + ", and this would open " + prov.ID
	}
	mode = cmp.Or(d.Prefs.Mode, prov.DefaultMode)
	if !prov.Asks(mode) { // whatever the store handed over: unasked, only a mode that asks before every edit and command
		return "", "", who + "starting without asking needs a mode known to ask its person: choose one for " + prov.ID + " in the agent preferences"
	}
	a.NoAPI = !d.Prefs.API
	return mode, d.Prefs.Net, ""
}

// DelegateProvider is the harness a delegate's start opens when its
// request names none: the one the gate resolved for it (the approved
// card's, or the person's preference's).
func DelegateProvider(a AgentOpen) string {
	if a.Provider == "" && a.Resume == "" && a.Delegate != nil {
		return a.Delegate.Provider
	}
	return a.Provider
}

// AgentEnded says how an agent session of p that is no longer live ended:
// its history entry's end time and true, or false when xbind has no record
// of an end — the session was running when xbind stopped (a session that
// ends while xbind runs always leaves its entry).
func (m *Manager) AgentEnded(p auth.Principal, id string) (ended string, recorded bool) {
	meta, _, err := m.historyMetaFrom(HistoryOf(p), id)
	if err != nil || meta.ID == "" {
		return "", false
	}
	return meta.Ended, true
}

// OpenAgentPrompted is OpenAgentWith for a start that carries its first
// prompt — the brief a person approved with a workspace agent's start: the
// session is opened and xbind itself hands it prompt (prompted at once, or
// held until the session is up). A session that can't take the brief it
// was approved with doesn't run: it is ended and the start fails.
func (m *Manager) OpenAgentPrompted(ctx context.Context, p auth.Principal, a AgentOpen, prompt string) (SessionInfo, QueueResult, int, error) {
	info, code, err := m.OpenAgentWith(p, a)
	if err != nil || prompt == "" {
		return info, QueueResult{}, code, err
	}
	res, err := m.AgentQueue(ctx, info.ID, agent.Prompt{Text: prompt}, false)
	if err != nil {
		m.Kill(info.ID)
		return SessionInfo{}, QueueResult{}, 502, fmt.Errorf("the session started but its first prompt couldn't be handed over, so it was ended: %w", err)
	}
	return info, res, code, nil
}

// DropAgentQueues takes back every message held for a session of user that
// a workspace agent started (holder "": any agent's): what the agent
// queued for a tile agent is not delivered once the agent is stopped. It
// answers how many were dropped.
func (m *Manager) DropAgentQueues(user, holder string) int {
	n := 0
	for _, si := range m.ListFor(user, "", nil) {
		if origin := m.AgentOrigin(si.ID); origin == "" || holder != "" && origin != holder {
			continue
		}
		held, err := m.AgentQueued(si.ID)
		if err != nil {
			continue
		}
		for _, q := range held {
			if _, err := m.AgentUnqueue(si.ID, q.ID); err == nil {
				n++
			}
		}
	}
	return n
}
