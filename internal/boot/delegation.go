package boot

// delegation.go — people's workspace agents (docs/auth.md §Delegation;
// D228): the wiring of what the server's delegate gate, the broker's Route
// and the terminal manager ask.
//
// Two things are left to the package that owns them, one assignment each:
//
//   - WHICH COMPONENT IS THE AGENT HERE: srv.AgentHolder, from the same
//     table — the component whose page xbin's chrome frames below its
//     header (GET /delegate/state's agent; server/agentpage.go): the
//     row marked Capabilities.Agent (boot.go, beside this file's call).
//     A table without one — a release today — ships no agent: the chrome
//     says so, and the notice that names /xbin/agent is sent to nobody.
//   - WHO HOLDS DELEGATION: auth.SetDelegateHolders, pointed at the
//     sealed-component table (internal/sealed, Capabilities.Delegate) when
//     it is in the tree. Until then no lookup is installed and no component
//     of any workspace delegates: every route below answers, and no
//     credential passes the gate. Install it BEFORE the server step's
//     check (Server.DelegationChromeConflict, boot.go): a component that is
//     both a holder and trusted chrome stops the boot — a holder's page
//     holds a frame token and never the person's session.
//
// THE AGENT RULES are the broker's DelegationPolicy: the orgs' agent rules
// (D229, internal/agentpolicy through Broker.AgentTileAllows and
// AgentSessionAllows) and people's agent preferences (internal/agentprefs:
// whether their agent may start a tile agent without asking, and in which
// mode). A test may set State.AgentPolicy to another one before boot. A
// Server, Broker or terminal manager that was given none refuses every
// delegate call (delegation.NoPolicy): boot is the only place one is made.

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/agentpolicy"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/llm"

	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/term"
)

func (st *State) setupDelegation(srv *server.Server) {
	store := delegation.OpenStore(filepath.Join(st.WS, "data", "delegation"))
	srv.Delegation = store
	// an agent session, for the connection view its holder's page reads
	st.Broker.AgentHandle = func(user, holder, id string) (orgs, tiles []string, scoped bool, model string, ok bool) {
		h, ok := store.AgentSession(user, holder, id)
		if !ok {
			return nil, nil, false, "", false
		}
		if h.Scope != nil {
			return h.Scope.Orgs, h.Scope.Tiles, true, h.Model, true
		}
		return nil, nil, false, h.Model, true
	}
	if st.Term != nil { // a session a holder started changed status: its backend is told (an id, no more)
		status, tm, brk := st.Term.OnStatus, st.Term, st.Broker
		st.Term.OnStatus = func(cwd string, ch term.StatusChange) {
			if status != nil {
				status(cwd, ch)
			}
			if holder := tm.AgentOrigin(ch.ID); holder != "" {
				brk.NotifyHolder(holder, ch.User, "session", ch.ID)
			}
		}
	}
	// the off switches, asked on every delegate call: sandboxes on (--dev
	// and --no-auth excepted), the workspace's switch, the person's pause
	gate := server.DelegationGate(store, st.Cfg.Isolate, st.Cfg.Dev || st.Cfg.NoAuth)
	if us := st.Auth.Users; us != nil && !st.Cfg.NoAuth {
		// …and that the person was given the agent: an admin's role reads the
		// component (its system page) and is no agent acting as them
		gate = server.AgentGivenGate(gate, us.Given)
	}
	gate = srv.AgentHeldGate(gate) // …and that the agent isn't held (a newer xbin's data, a locked vault)
	st.Auth.SetDelegationGate(gate)
	srv.SetDelegationGate(gate) // the LLM connector's holder rule asks the same switches (holderCallPolicy)
	// people or their access changed: running delegate calls and standing
	// approvals are judged again at once (tiles: serve.go's watch loop)
	st.Broker.OnPeopleChanged = srv.SweepDelegation
	// a deleted account takes its delegation records with it: an account
	// made later under the same id inherits no approval, ticket, handle,
	// change set or log (delegation/purge.go)
	prevOut := st.Broker.OnUserSignedOut
	st.Broker.OnUserSignedOut = func(id string, deleted bool) {
		if prevOut != nil {
			prevOut(id, deleted)
		}
		if deleted {
			srv.DelegationPersonGone(id)
		}
	}
	srv.DelegationDev = st.Cfg.Dev || st.Cfg.NoAuth // developing an agent: no step-up; --no-auth's owner answers as the person
	if st.AgentPolicy == nil {
		var startPrefs func(user, harness string) (mode, net string, api, ok bool)
		if srv.AgentPrefs != nil {
			startPrefs = srv.AgentPrefs.StartPrefs
		}
		st.AgentPolicy = st.Broker.DelegationPolicy(startPrefs)
	}
	srv.SweepDelegation()          // accounts deleted or replaced while xbind was down: their records go before anything reads them
	srv.RearmDelegateNotices()     // the expiry notices of requests that waited across the restart
	st.Broker.ObserveAgentLevels() // what each tile's manifest says agents may do, against what was approved
	srv.AgentPolicy = st.AgentPolicy
	st.Broker.AgentPolicy = st.AgentPolicy
	if st.Term != nil {
		st.Term.AgentPolicy = st.AgentPolicy
	}
}

// holderCallPolicy is the LLM connector's side of an agent session (D228
// part C0): a model call from a delegation holder — the workspace agent's
// backend, in a person's partition — runs only under a live agent session
// of that person and that holder (X-XBin-Agent-Session), and only on that
// session's model. So the model the delegate gate asks the orgs' rules
// with is the model the conversation's text really goes to: the agent
// can't hold a session on a private model and call a hosted one. A model
// an org whose data the session has touched no longer allows is refused
// too (the rules changed since). Every other caller passes, as before.
func holderCallPolicy(srv *server.Server, brk *broker.Broker) llm.CallPolicy {
	return func(c llm.Caller, u llm.UpstreamInfo, model string) error {
		if c.Component == "" || !auth.HoldsDelegation(c.Component) {
			return nil
		}
		if c.Listing {
			// The list of models is no call: nothing of a conversation goes
			// anywhere. A person's agent shows it so they can pick — and
			// which of them a session may move to is xbind's to answer
			// when it is asked (the connection view's whatIf, the PATCH).
			// Holding the list to the session's own model left the agent's
			// model picker empty.
			return nil
		}
		// the off switches first, before the session is touched (a call
		// that is refused doesn't keep a handle alive): a paused or
		// switched-off agent runs no model turn
		if err := srv.DelegationOff(c.Component, c.User); err != nil {
			return fmt.Errorf("this person's workspace agent is off: %w", err)
		}
		const how = ": make one (POST /api/xbin/delegate/sessions) and send its id as " + llm.AgentSessionHeader
		if c.User == "" || c.AgentSession == "" || srv.Delegation == nil {
			return errors.New("a workspace agent's model calls run under an agent session" + how)
		}
		h, ok := srv.Delegation.AgentSession(c.User, c.Component, c.AgentSession)
		if !ok {
			return errors.New("no such agent session (it expired, or is another person's)" + how)
		}
		// the session's model as the connector resolves it now: a session
		// opened on a role's name ("xbin/agent": the agent role's model,
		// which an agent uses when it knows no other) is on whatever that
		// role names — compared literally, every call of such a session was
		// refused
		on := h.Model
		if ref := brk.ResolveModel(h.Model); ref != nil && ref.Upstream != "" && ref.Model != "" {
			on = ref.ID()
		}
		if want := (agentpolicy.ModelRef{Upstream: u.Name, Model: model}).ID(); on != want {
			return fmt.Errorf("this agent session is on %s: its calls can't use %s — change the session's model (PATCH /api/xbin/delegate/sessions/%s), which the orgs whose data it has seen must allow", h.Model, want, h.ID)
		}
		for _, org := range h.Tainted {
			if err := brk.AgentModelAllowedIn(org, h.Model); err != nil {
				return fmt.Errorf("this agent session has seen data of %s, which %s may no longer see: %w", org, h.Model, err)
			}
		}
		return nil
	}
}
