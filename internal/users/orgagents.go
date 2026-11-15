package users

import (
	"fmt"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/agentpolicy"
)

// Agent constraints (D229; internal/agentpolicy has the rules and how they
// are asked). An org says what agents may do on ITS tiles — whether the
// workspace agent may work there, which coding-agent harnesses run there,
// which models may see their data (Org.Agents) — and the workspace sets the
// outer bounds every tile falls under, plus whose credentials serve a
// coding agent's model calls by default. An org only narrows the bounds:
// SetOrgAgents refuses a list that reaches past them, and every call is
// asked against the bounds as well as the org, so bounds tightened later
// hold at once.
//
// Which orgs a tile falls under (TileOrgs): the org that owns it and every
// org it is shared into (Org.Tiles, an exact entry or a pattern). A tile
// under several gets the strictest of them; a tile under none follows the
// bounds alone.

// WorkspaceAgents is the workspace's outer bounds on agents and its
// credential mode (agentpolicy.CredentialsOwnFirst, …ConnectorFirst,
// …ConnectorOnly).
type WorkspaceAgents struct {
	agentpolicy.Rules
	Credentials string `json:"credentials"`
}

func (w WorkspaceAgents) normalized() WorkspaceAgents {
	w.Credentials, _ = agentpolicy.CredentialMode(w.Credentials)
	if !w.WorkspaceAgent.IsOff() {
		w.WorkspaceAgent = ""
	}
	return w
}

func (w WorkspaceAgents) isZero() bool {
	return w.Rules.IsZero() && w.normalized().Credentials == agentpolicy.CredentialsOwnFirst
}

// WorkspaceAgents is the workspace's bounds and credential mode (the
// defaults when nothing was set: everything allowed, own-first).
func (s *Store) WorkspaceAgents() WorkspaceAgents {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agents.normalized()
}

// SetWorkspaceAgents changes the bounds by p and, when credentials is not
// "", the credential mode. knownHarness names the harnesses this xbind
// runs (nil: any id). Orgs' own rules and pins are left as they are: a
// rule the new bounds no longer cover simply stops allowing anything the
// bounds refuse.
func (s *Store) SetWorkspaceAgents(p agentpolicy.Patch, credentials string, knownHarness func(string) bool) (WorkspaceAgents, error) {
	mode, ok := agentpolicy.CredentialMode(credentials)
	if !ok {
		return WorkspaceAgents{}, fmt.Errorf("credentials: %s, %s or %s, not %q", agentpolicy.CredentialsOwnFirst,
			agentpolicy.CredentialsConnectorFirst, agentpolicy.CredentialsConnectorOnly, credentials)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.agents.normalized()
	next.Rules = next.Rules.Apply(p)
	if err := next.Rules.Validate(knownHarness); err != nil {
		return WorkspaceAgents{}, err
	}
	if credentials != "" {
		next.Credentials = mode
	}
	was := s.agents
	s.agents = next
	if err := s.persistLocked(); err != nil {
		s.agents = was
		return WorkspaceAgents{}, err
	}
	return next, nil
}

// OrgAgents is an org's own rules and the rules pinned on it (the zero
// Rules for none or no such org).
func (s *Store) OrgAgents(orgID string) (own, pin agentpolicy.Rules) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if org := s.orgs[normalizeID(orgID)]; org != nil {
		return rulesOf(org.Agents), rulesOf(org.AgentsPin)
	}
	return own, pin
}

func rulesOf(r *agentpolicy.Rules) agentpolicy.Rules {
	if r == nil {
		return agentpolicy.Rules{}
	}
	return *r
}

// SetOrgAgents changes an org's own rules by p: each rule p names is set,
// the rest keep theirs. The result must sit within the workspace's bounds
// and within the rules pinned on the org (an org narrows, never widens).
// It answers the org's rules now.
func (s *Store) SetOrgAgents(orgID string, p agentpolicy.Patch, knownHarness func(string) bool) (agentpolicy.Rules, error) {
	return s.setOrgAgents(orgID, p, knownHarness, false)
}

// SetOrgAgentsPin changes the rules pinned on an org — a workspace
// admin's: the org's admins can narrow below them and never loosen them,
// because every call is asked against the pin as well as the org's own
// rules. The pin must sit within the workspace's bounds. The org's own
// rules are left as they are.
func (s *Store) SetOrgAgentsPin(orgID string, p agentpolicy.Patch, knownHarness func(string) bool) (agentpolicy.Rules, error) {
	return s.setOrgAgents(orgID, p, knownHarness, true)
}

func (s *Store) setOrgAgents(orgID string, p agentpolicy.Patch, knownHarness func(string) bool, pin bool) (agentpolicy.Rules, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	org := s.orgs[normalizeID(orgID)]
	if org == nil {
		return agentpolicy.Rules{}, fmt.Errorf("no such org %q", orgID)
	}
	cur := org.Agents
	if pin {
		cur = org.AgentsPin
	}
	next := rulesOf(cur).Apply(p)
	if err := next.Validate(knownHarness); err != nil {
		return agentpolicy.Rules{}, err
	}
	if err := next.Within(s.agents.Rules); err != nil {
		return agentpolicy.Rules{}, fmt.Errorf("%w: an org narrows the workspace's rules, it can't widen them", err)
	}
	if !pin {
		if err := next.Within(rulesOf(org.AgentsPin)); err != nil {
			return agentpolicy.Rules{}, fmt.Errorf("%s: a workspace admin pinned this org's rules, and its own can only narrow them",
				strings.Replace(err.Error(), "the workspace allows", "the pin allows", 1))
		}
	}
	no := *org
	var stored *agentpolicy.Rules
	if !next.IsZero() {
		stored = &next
	}
	if pin {
		no.AgentsPin = stored
	} else {
		no.Agents = stored
	}
	s.orgs[no.ID] = &no
	if err := s.persistLocked(); err != nil {
		s.orgs[no.ID] = org
		return agentpolicy.Rules{}, err
	}
	return next, nil
}

// TileOrgs is the orgs path falls under for agent constraints: owner is
// the org that owns it ("" when none does), orgs the owner and every org
// it is shared into, sorted. A path inside a tile — a session opened on a
// sub-directory, a component nested in another — falls under its own orgs
// and those of every path above it: stepping into a tile never steps out
// of its org's rules.
func (s *Store) TileOrgs(path string) (owner string, orgs []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	owner, orgs, _ = s.tileOrgsLocked(path)
	return owner, orgs
}

// TileOrgVia is TileOrgs with, for each org the path is under by a share
// and not by ownership, the share that covers it (the org's Tiles entry:
// a path or a pattern) — what a refusal names, so a rule that reaches a
// tile through a broad pattern can be traced.
func (s *Store) TileOrgVia(path string) (owner string, orgs []string, via map[string]string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tileOrgsLocked(path)
}

func (s *Store) tileOrgsLocked(path string) (owner string, orgs []string, via map[string]string) {
	in := map[string]bool{}
	owned := map[string]bool{}
	via = map[string]string{}
	for p := strings.Trim(path, "/"); p != ""; {
		if k, id, _ := ParseOwner(s.owners[p]); k == OwnerKindOrg {
			if owner == "" {
				owner = id // the nearest owning org
			}
			in[id], owned[id] = true, true
		}
		for id, o := range s.orgs {
			if in[id] {
				continue
			}
			pats := make([]string, 0, len(o.Tiles))
			for pat, level := range o.Tiles {
				if levelRank(level) > 0 && matchTile(pat, p) {
					pats = append(pats, pat)
				}
			}
			if len(pats) > 0 {
				sort.Strings(pats) // one share to name, the same every time
				in[id], via[id] = true, pats[0]
			}
		}
		i := strings.LastIndexByte(p, '/')
		if i < 0 {
			break
		}
		p = p[:i]
	}
	for id := range in {
		if s.orgs[id] == nil {
			continue
		}
		orgs = append(orgs, id)
		if owned[id] {
			delete(via, id) // owning beats sharing
		}
	}
	sort.Strings(orgs)
	return owner, orgs, via
}

// AgentLayers is the layers of rules tile falls under, for
// agentpolicy.Check: the workspace's bounds first, then for each of its
// orgs the rules pinned on it and its own.
func (s *Store) AgentLayers(path string) []agentpolicy.Layer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	layers := []agentpolicy.Layer{{Rules: s.agents.normalized().Rules}}
	_, orgs, via := s.tileOrgsLocked(path)
	for _, id := range orgs {
		o := s.orgs[id]
		layers = append(layers, agentpolicy.OrgLayers(id, o.Name, rulesOf(o.Agents), rulesOf(o.AgentsPin), via[id])...)
	}
	return layers
}
