package users

import (
	"cmp"
	"fmt"
	"sort"

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
// credential default: own-first (a person's own sign-in when they have
// one, else the LLM connector) or connector (forced for everyone).
type WorkspaceAgents struct {
	agentpolicy.Rules
	Credentials string `json:"credentials"`
}

func (w WorkspaceAgents) normalized() WorkspaceAgents {
	if w.Credentials != agentpolicy.CredentialsConnector {
		w.Credentials = agentpolicy.CredentialsOwnFirst
	}
	if !w.WorkspaceAgent.IsOff() {
		w.WorkspaceAgent = ""
	}
	return w
}

func (w WorkspaceAgents) isZero() bool {
	return w.Rules.IsZero() && w.Credentials != agentpolicy.CredentialsConnector
}

// WorkspaceAgents is the workspace's bounds and credential default (the
// defaults when nothing was set: everything allowed, own-first).
func (s *Store) WorkspaceAgents() WorkspaceAgents {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agents.normalized()
}

// SetWorkspaceAgents changes the bounds by p and, when credentials is not
// "", the credential default. knownHarness names the harnesses this xbind
// runs (nil: any id). Orgs' own rules are left as they are: a rule the new
// bounds no longer cover simply stops allowing anything the bounds refuse.
func (s *Store) SetWorkspaceAgents(p agentpolicy.Patch, credentials string, knownHarness func(string) bool) (WorkspaceAgents, error) {
	switch credentials {
	case "", agentpolicy.CredentialsOwnFirst, agentpolicy.CredentialsConnector:
	default:
		return WorkspaceAgents{}, fmt.Errorf("credentials: %q or %q, not %q", agentpolicy.CredentialsOwnFirst, agentpolicy.CredentialsConnector, credentials)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.agents.normalized()
	next.Rules = next.Rules.Apply(p)
	if err := next.Rules.Validate(knownHarness); err != nil {
		return WorkspaceAgents{}, err
	}
	next.Credentials = cmp.Or(credentials, next.Credentials)
	was := s.agents
	s.agents = next
	if err := s.persistLocked(); err != nil {
		s.agents = was
		return WorkspaceAgents{}, err
	}
	return next, nil
}

// OrgAgents is an org's own rules (the zero Rules for none or no such org).
func (s *Store) OrgAgents(orgID string) agentpolicy.Rules {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if org := s.orgs[normalizeID(orgID)]; org != nil && org.Agents != nil {
		return *org.Agents
	}
	return agentpolicy.Rules{}
}

// SetOrgAgents changes an org's rules by p: each rule p names is set, the
// rest keep theirs. The result must sit within the workspace's bounds (an
// org narrows, never widens). It answers the org's rules now.
func (s *Store) SetOrgAgents(orgID string, p agentpolicy.Patch, knownHarness func(string) bool) (agentpolicy.Rules, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	org := s.orgs[normalizeID(orgID)]
	if org == nil {
		return agentpolicy.Rules{}, fmt.Errorf("no such org %q", orgID)
	}
	var next agentpolicy.Rules
	if org.Agents != nil {
		next = *org.Agents
	}
	next = next.Apply(p)
	if err := next.Validate(knownHarness); err != nil {
		return agentpolicy.Rules{}, err
	}
	if err := next.Within(s.agents.Rules); err != nil {
		return agentpolicy.Rules{}, fmt.Errorf("%w: an org narrows the workspace's rules, it can't widen them", err)
	}
	no := *org
	no.Agents = nil
	if !next.IsZero() {
		no.Agents = &next
	}
	s.orgs[no.ID] = &no
	if err := s.persistLocked(); err != nil {
		s.orgs[no.ID] = org
		return agentpolicy.Rules{}, err
	}
	return next, nil
}

// TileOrgs is the orgs tile falls under for agent constraints: owner is
// the org that owns it ("" when none does), orgs the owner and every org
// the tile is shared into, sorted.
func (s *Store) TileOrgs(path string) (owner string, orgs []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tileOrgsLocked(path)
}

func (s *Store) tileOrgsLocked(path string) (owner string, orgs []string) {
	if k, id, _ := ParseOwner(s.owners[path]); k == OwnerKindOrg {
		owner = id
		orgs = append(orgs, id)
	}
	for id, o := range s.orgs {
		if id == owner {
			continue
		}
		for pat, level := range o.Tiles {
			if levelRank(level) > 0 && matchTile(pat, path) {
				orgs = append(orgs, id)
				break
			}
		}
	}
	sort.Strings(orgs)
	return owner, orgs
}

// AgentLayers is the layers of rules tile falls under, for
// agentpolicy.Check: the workspace's bounds first, then each of its orgs.
func (s *Store) AgentLayers(path string) []agentpolicy.Layer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	layers := []agentpolicy.Layer{{Rules: s.agents.normalized().Rules}}
	_, orgs := s.tileOrgsLocked(path)
	for _, id := range orgs {
		o := s.orgs[id]
		l := agentpolicy.Layer{ID: id, Name: o.Name}
		if o.Agents != nil {
			l.Rules = *o.Agents
		}
		layers = append(layers, l)
	}
	return layers
}
