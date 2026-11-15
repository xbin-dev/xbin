package server

// agentwho.go — who gets a workspace agent (D231 × D227;
// docs/workspace-agent.md §Running it): the third of the three things an
// admin does before anyone has one.
//
// A person's agent is their partition of the sealed component that is the
// agent here (AgentHolder), and they have one when they may read it. Nobody
// may until it is given to them: a sealed component is reached only by an
// access entry that names it (internal/users/sealed.go) — in the workspace
// defaults (everyone), a person's own entries, or an org's shares. An admin
// reads it without one, as they manage it.
//
// So "who gets one" is ordinary access, kept where access is kept, and
// this file adds only what xbin's chrome needs to make the choice in one
// click: PUT /delegate/workspace {who}, beside the switch — "everyone"
// (the defaults name it), "me" (the admin's own entry; nobody else yet),
// "chosen" (the defaults' entry goes; whoever has an entry of their own
// keeps it). People and orgs one by one are access entries like any other
// (PUT /users/{id}/access, an org's shares).

import (
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
)

// agentAccess: the person behind p has an agent at holder — it was given
// to them (users.Access.Given). An admin who wasn't reads the component as
// they manage it and has no agent: administering it and having one are two
// things, and `who` alone decides the second.
func (s *Server) agentAccess(p auth.Principal, holder string) bool {
	if s.devOwner(p) {
		return true // --no-auth: the implicit owner is everyone there is
	}
	p = personOf(p)
	return p.UserID != "" && s.userHasAgent(p.UserID, holder)
}

// userHasAgent is agentAccess by a person's id, for what xbind tells a
// person unasked (the notice that names the agent's page) and for the
// delegation gate (AgentGivenGate).
func (s *Server) userHasAgent(user, holder string) bool {
	if s.Auth == nil || s.Auth.Users == nil {
		return true // --no-auth: the owner
	}
	return s.Auth.Users.Given(user, holder)
}

// AgentGivenGate adds to a delegation gate (DelegationGate) that the
// person was GIVEN the agent: an admin's role lets them read the sealed
// component — its system page — and is no agent acting as them. given nil
// (no accounts: --no-auth) adds nothing.
func AgentGivenGate(gate func(holder, user string) error, given func(user, holder string) bool) func(holder, user string) error {
	if given == nil {
		return gate
	}
	return func(holder, user string) error {
		if err := gate(holder, user); err != nil {
			return err
		}
		if !given(user, holder) {
			return &OffError{"no-agent", "you weren't given a workspace agent: an admin chooses who gets one (administering it isn't having one)"}
		}
		return nil
	}
}

// AgentWho is who was given a workspace agent, as an admin reads it.
type AgentWho struct {
	Everyone bool `json:"everyone"` // the workspace defaults name the agent
	Entries  int  `json:"entries"`  // people and orgs with an entry of their own
	Chosen   bool `json:"chosen"`   // someone was given one: the setup's third step is done
}

// agentWho answers an admin (ok false for anyone else, and without a users
// store: --no-auth, where the owner is everyone there is).
func (s *Server) agentWho(p auth.Principal, holder string) (AgentWho, bool) {
	if !personOf(p).IsAdmin() || s.Auth == nil || s.Auth.Users == nil {
		return AgentWho{}, false
	}
	all, n := s.Auth.Users.SealedGrants(holder)
	return AgentWho{Everyone: all, Entries: n, Chosen: all || n > 0}, true
}

// setAgentWho makes the choice for admin p; code 0 when it was made.
func (s *Server) setAgentWho(p auth.Principal, who string) (code int, msg string) {
	holder := s.agentHolder()
	switch {
	case holder == "":
		return http.StatusConflict, "this xbin ships no workspace agent to give anyone"
	case s.Auth == nil || s.Auth.Users == nil:
		return http.StatusConflict, "this workspace has no accounts to give an agent to (it runs without sign-in)"
	}
	st := s.Auth.Users
	var err error
	switch who {
	case "everyone":
		err = st.SetSealedEveryone(holder, true)
	case "chosen":
		err = st.SetSealedEveryone(holder, false)
	case "me":
		if p.UserID == "" {
			return http.StatusForbidden, "sign in as yourself to give yourself an agent"
		}
		if err = st.SetSealedEveryone(holder, false); err == nil {
			err = st.SetUserTile(p.UserID, holder, "read")
		}
	default:
		return http.StatusBadRequest, `who is "everyone", "me" or "chosen"`
	}
	if err != nil {
		return http.StatusInternalServerError, "who gets an agent can't be written: " + err.Error()
	}
	if s.PeopleChanged != nil {
		s.PeopleChanged() // access moved: panels refresh, partitions of people who lost it stop, delegation is judged again
	}
	return 0, ""
}

// delegateWorkspaceView is GET and PUT /delegate/workspace's answer: the
// switch, and who was given an agent.
func (s *Server) delegateWorkspaceView(p auth.Principal) map[string]any {
	ws := s.Delegation.Workspace()
	out := map[string]any{"enabled": ws.Enabled, "by": ws.By, "at": ws.At}
	if h := s.agentHolder(); h != "" {
		out["agent"] = h
		if who, ok := s.agentWho(p, h); ok {
			out["who"] = who
		}
	}
	return out
}

// AgentHeldGate adds to a delegation gate that the agent isn't HELD (its
// data is a newer xbin's, the vault is locked, its release can't run
// here: AgentHeld). A held component's backends aren't started; this is
// for one that was running when the hold began, and for its model turns.
func (s *Server) AgentHeldGate(gate func(holder, user string) error) func(holder, user string) error {
	return func(holder, user string) error {
		if err := gate(holder, user); err != nil {
			return err
		}
		if kind := s.agentHeldKind(holder); kind != "" {
			return &OffError{"held", "the workspace agent is held on this xbin (" + kind + "): it does nothing until that is cleared"}
		}
		return nil
	}
}

// agentHeldKind is why holder — the agent here — is held ("" when it
// isn't, and for any other component).
func (s *Server) agentHeldKind(holder string) string {
	if s.AgentHeld == nil || holder == "" || holder != s.agentHolder() {
		return ""
	}
	kind, _ := s.AgentHeld()
	return kind
}
