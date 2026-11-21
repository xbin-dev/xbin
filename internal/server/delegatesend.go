package server

// delegatesend.go — what a person's agent may tell a tile agent, and
// which one (D228, the final review): the sessions it drives, the text it
// sends them, the session's mode and its model source against the
// conversation's taint; and what a start's body asks for.

import (
	"cmp"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/xbin-dev/xbin/internal/agentpolicy"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/term"
)

// delegateDrives lets a delegate drive only the sessions it opened: a
// session the person opened themselves may be in a mode that asks nothing,
// and prompting it would be acting unasked. Everyone else passes. It
// answers the refusal itself.
func (s *Server) delegateDrives(w http.ResponseWriter, r *http.Request, id string) bool {
	if why := s.drivesRefusal(auth.PrincipalOf(r), id); why != "" {
		WriteError(w, http.StatusForbidden, why, "/docs/auth.md")
		return false
	}
	return true
}

// drivesRefusal is delegateDrives' rule ("": p drives session id).
func (s *Server) drivesRefusal(p auth.Principal, id string) string {
	if p.Agent == "" || s.Term == nil {
		return ""
	}
	if _, live := s.Term.Info(id); live && s.Term.AgentOrigin(id) != p.Agent {
		return "a workspace agent drives the sessions it started: this one is its person's own"
	}
	return ""
}

// delegateSends judges the text a delegate sends a session it started (a
// prompt, a queued message): everyone else passes. It answers the refusal
// itself (403 {refusal}). Three rules, each about acting unasked:
//
//   - the text doesn't begin with "/": every shipped harness reads that as
//     one of its own commands (a model change, a sign-out, a project
//     command that carries its own tool permissions) — the person's to give;
//   - the session's CURRENT mode asks before it acts: one in a mode that
//     doesn't — the default (D251), whatever mode its person approved it
//     in — takes a text only as its person's approval of that text, once
//     (a "message" request, delegatemessage.go);
//   - the session's model source satisfies every org the conversation
//     holds data of (sendRefusal).
func (s *Server) delegateSends(w http.ResponseWriter, r *http.Request, id, text string) bool {
	why, refusal := s.sendsRefusal(auth.PrincipalOf(r), id, text)
	if refusal == "mode" && messageApproved(r, id) {
		// its person approved exactly this message to this session, as it is now (delegatemessage.go): the mode
		// rule's way forward — the taint and the command rules still hold
		why, refusal = s.sendsRefusalPast(auth.PrincipalOf(r), id, text)
	}
	if why == "" {
		return true
	}
	WriteJSON(w, http.StatusForbidden, map[string]string{"error": why, "refusal": refusal, "docs": delegationDocs})
	return false
}

// sendsRefusal is delegateSends' rule ("": the text may go).
func (s *Server) sendsRefusal(p auth.Principal, id, text string) (why, refusal string) {
	if p.Agent == "" || s.Term == nil {
		return "", ""
	}
	info, live := s.Term.Info(id)
	if !live {
		return "", "" // the handler answers 404
	}
	why, refusal = harnessCommand(text), "command"
	if why == "" {
		why, refusal = s.sessionModeRefusal(id, info, p.UserID), "mode"
	}
	if why == "" {
		why, refusal = s.sendRefusal(p, s.liveTarget(id, info)), "taint"
	}
	if why == "" {
		return "", ""
	}
	return why, refusal
}

// sendsRefusalPast is sendsRefusal without the mode rule: for a message
// its person approved (D250).
func (s *Server) sendsRefusalPast(p auth.Principal, id, text string) (why, refusal string) {
	info, live := s.Term.Info(id)
	if !live {
		return "", ""
	}
	if why := harnessCommand(text); why != "" {
		return why, "command"
	}
	if why := s.sendRefusal(p, s.liveTarget(id, info)); why != "" {
		return why, "taint"
	}
	return "", ""
}

// liveTarget is live session id as what is sent to it reaches a model: its
// tile, who serves it now, and — on the connector — the connector's model
// it calls (the row's model is the harness's own name for its setting,
// which needn't be one of the connector's). A message to it, and a start
// that continues it (D250 review M3), are judged on this — never on what a
// new start there would run on.
func (s *Server) liveTarget(id string, info term.SessionInfo) sendTarget {
	t := sendTargetOf(info)
	if t.served == agentpolicy.ServedConnector {
		t.model = s.Term.AgentConnectorModel(id)
	}
	return t
}

// harnessCommand is why text may not go to a tile agent from a person's
// agent: it begins with "/" (after any white space), which the harnesses
// read as a command of their own, not as a message.
func harnessCommand(text string) string {
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
		return "a message from a workspace agent can't begin with \"/\": the coding agent reads that as one of its own commands, and those are its person's to give — say it in words, or begin with one"
	}
	return ""
}

// sessionModeRefusal is modeRefusal for live session id: on the mode it
// reports, and on a mode a switch asked for that it hasn't reported yet —
// the one it may be in by the time the text reaches it (D250 re-review 4b).
func (s *Server) sessionModeRefusal(id string, info term.SessionInfo, user string) string {
	if why := modeRefusal(info, user); why != "" {
		return why
	}
	if pending := s.Term.AgentPendingMode(id); pending != "" {
		in := info
		in.Mode = pending
		return modeRefusal(in, user)
	}
	return ""
}

// modeRefusal is why a session in info's current mode takes no message
// from its person's agent unapproved: the mode doesn't ask before every
// edit and command (acp.Provider.Asks) — the default of the sessions such
// an agent starts (D251) — so each text is its person's to approve, once
// (a message request; whatever mode they approved the session in).
func modeRefusal(info term.SessionInfo, user string) string {
	if term.MessageModeOK(info.Provider, info.Mode) { // (the one predicate: the held queue's delivery uses it too)
		return ""
	}
	return "this session acts without asking (mode " + strconv.Quote(info.Mode) + "): a message from " + user + "'s workspace agent goes to it only with their approval of that text — file it as a message request (POST /api/xbin/delegate/requests with this call); it goes once they allow it"
}

// sendTarget is the tile agent session a delegate would send text to: its
// tile, who serves its model calls (agentpolicy.Served…) and its model.
type sendTarget struct{ tile, served, model string }

func sendTargetOf(info term.SessionInfo) sendTarget {
	t := sendTarget{tile: info.Cwd, served: info.LLM, model: info.Model}
	if info.LLM == agentpolicy.ServedConnector && info.LLMOffered {
		t.served = agentpolicy.ServedOffered
	}
	if t.served == "" {
		// a running session with no word on who serves it — a workspace with
		// no connector (it is on its person's own sign-in), or one not
		// settled yet: not a session that is still to start
		t.served = agentpolicy.ServedUnsettled
	}
	if t.served != agentpolicy.ServedConnector {
		t.model = "" // only the connector's model is one xbind sees
	}
	return t
}

// sendRefusal is why p's conversation may not send text to a tile agent
// session ("": it may): the conversation holds data of the orgs its handle
// is tainted with, and what it tells a tile agent goes to THAT session's
// model — which must satisfy each of those orgs' model rules
// (AgentWorld.AgentBriefAllowed). A session on the person's own sign-in is
// refused wherever a tainted org has a rule other than "any": nobody can
// say which model it calls.
func (s *Server) sendRefusal(p auth.Principal, t sendTarget) string {
	h, ok := s.handleOf(p)
	if !ok || s.AgentWorld == nil {
		return ""
	}
	for _, o := range h.Tainted {
		if err := s.AgentWorld.AgentBriefAllowed(o, t.tile, t.served, t.model); err != nil {
			return "this conversation holds data of " + o + ", so a tile agent it briefs must be on a model that org allows — " + err.Error()
		}
	}
	return ""
}

// startRefusal is why a delegate's start is refused whatever approves it:
// its first prompt begins with a harness command, or the conversation's
// taint keeps it from briefing a session there. The session isn't started
// yet, so its model source is what xbind can vouch for beforehand
// (term.DelegateStartSource): a tile whose own rules hold its sessions to
// the tainted orgs' satisfies them; so does a session the agents policy
// holds to the connector, when every model it may start on — the one the
// start names and the connector's default — satisfies them. A start that
// would run on its person's own sign-in, or by a choice, is vouched for by
// nothing.
func (s *Server) startRefusal(p auth.Principal, ask sessionAsk) (refusal, why string) {
	if refusal, why := s.startScopeRefusal(p, ask); why != "" {
		return refusal, why
	}
	return s.startTaintRefusal(p, ask)
}

// startScopeRefusal is startRefusal's first half: what the brief says
// (a harness command) and where (a tile hidden from the person or outside
// the conversation's scope) — what holds whether the start opens a session
// or continues the one there.
func (s *Server) startScopeRefusal(p auth.Principal, ask sessionAsk) (refusal, why string) {
	if why := harnessCommand(ask.Prompt); why != "" {
		return "command", why
	}
	// a start is on a tile: one hidden from the person, or outside what they
	// let this conversation see, is no place for it to start a tile agent
	// (the gate's rule for every other request that names a tile) — except
	// a plan's start on a tile an earlier line of the plan makes (planned)
	if h, ok := s.handleOf(p); ok && ask.Cwd != "" && !ask.planned {
		switch {
		case s.tileHidden(p, ask.Cwd): // not there, or not theirs to read: one answer
			return "tile", noTileFor(p.UserID)
		case !s.inSessionScope(h, ask.Cwd):
			return "hidden", hiddenByScope(p.UserID)
		}
	}
	return "", ""
}

// startTaintRefusal is startRefusal's second half: the conversation's taint
// against the model a NEW session there would run on (a continued session
// is judged on its live source instead: liveTarget).
func (s *Server) startTaintRefusal(p auth.Principal, ask sessionAsk) (refusal, why string) {
	if h, ok := s.handleOf(p); !ok || len(h.Tainted) == 0 {
		return "", "" // nothing to hold it to: don't resolve a harness for it
	}
	targets := []sendTarget{{tile: ask.Cwd}}
	if s.Term != nil {
		harness := ask.Provider
		if harness == "" {
			harness = s.startDefaults(p, ask.Cwd).Harness
		}
		if held, models := s.Term.DelegateStartSource(p.UserID, ask.Cwd, harness, cmp.Or(ask.Model, ask.Options["model"])); held {
			targets = targets[:0]
			for _, m := range models {
				targets = append(targets, sendTarget{tile: ask.Cwd, served: agentpolicy.ServedConnector, model: m})
			}
			if len(targets) == 0 { // held, and the connector names no model: the tile's own rule or nothing
				targets = append(targets, sendTarget{tile: ask.Cwd, served: agentpolicy.ServedConnector})
			}
		}
	}
	for _, t := range targets {
		if why := s.sendRefusal(p, t); why != "" {
			return "taint", why
		}
	}
	return "", ""
}

// sessionAsk is what a POST /term/sessions body asks for ("" what it
// doesn't say): the tile, the harness, the first prompt, and whether it
// names a mode or a sandbox picker of its own.
type sessionAsk struct {
	Cwd, Provider, Prompt, Mode, Net string
	API                              *bool
	VM                               bool
	Resume                           string
	Model                            string
	Options                          map[string]string
	// Continue: the agent's own idle session on the tile, which the start
	// continues unless its person starts a new one (D250) — always asked
	Continue string
	// Kind and Signin: what the body says it is, and a saved sign-in it
	// names — neither is a workspace agent's to get wrong or to pick
	Kind, Signin string
	// planned: the tile is one an earlier line of the same plan makes
	// (previewStart): it isn't there yet, by design. Never read from a body.
	planned bool
}

func sessionAskOf(body []byte) sessionAsk {
	var b sessionAsk
	_ = json.Unmarshal(body, &b)
	b.Cwd = strings.Trim(b.Cwd, "/")
	return b
}

// startsUnasked reports whether the start a body asks for would run on
// the person's preference, with no ticket: their preference lets that
// harness start unasked on the tile, in a mode that asks before it acts,
// and the body names no mode or picker of its own (naming one is asking
// for something else). What a dry run of a start answers.
func (s *Server) startsUnasked(p auth.Principal, ask sessionAsk) bool {
	if ask.Mode != "" || ask.Net != "" || ask.API != nil || ask.VM || ask.Resume != "" || ask.Continue != "" {
		return false
	}
	provider, _, differs := s.unaskedStart(p, ask) // the person's own choice of coding agent and model wins (D250)
	if differs != "" {
		return false
	}
	def := s.startDefaults(p, ask.Cwd)
	h, ok := def.of(cmp.Or(provider, def.Harness))
	return ok && !h.NeedsTicket
}
