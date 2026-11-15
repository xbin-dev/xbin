package broker

// delegatelist.go — what a person's workspace agent sees of the broker's
// listings, and the delegation.Policy xbind runs with (D228, D229).
//
// A delegate reads a listing as its person does, less the rows about tiles
// whose org keeps the workspace agent out (on the model it says it is on).
// That is asked in ONE place — server.DelegateHides, which asks the
// installed delegation.Policy — and the broker's listings use it here:
// GET /alerts and GET /screens. Anything a row names that can't be read as
// a tile is left out for an agent, never shown unjudged.

import (
	"encoding/json"
	"errors"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
)

// agentHides reports a tile that p's listings leave out: p is a person's
// workspace agent and the tile's org's rules don't let it in. Without the
// server's filter installed an agent sees no tile row at all.
func (b *Broker) agentHides(p auth.Principal, tile string) bool {
	if p.Agent == "" {
		return false
	}
	return b.delegateHides == nil || b.delegateHides(p, tile)
}

// agentAlerts is alerts as p's agent may read them: the workspace-wide
// ones, and a tile's only where the agent is let in.
func (b *Broker) agentAlerts(p auth.Principal, alerts []Alert) []Alert {
	if p.Agent == "" {
		return alerts
	}
	out := make([]Alert, 0, len(alerts))
	for _, a := range alerts {
		tile := a.Tile
		if tile == "" && a.Action != nil {
			tile = a.Action.Tile
		}
		if tile == "" && !a.System {
			continue // about something, and it doesn't say what
		}
		if tile != "" && b.agentHides(p, tile) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// agentTiles is a screen's tiles ([{path, …}], the shell's shape) without
// the ones p's agent isn't let into; an entry that names no tile goes too.
func (b *Broker) agentTiles(p auth.Principal, raw json.RawMessage) json.RawMessage {
	if p.Agent == "" || len(raw) == 0 {
		return raw
	}
	var rows []json.RawMessage
	_ = json.Unmarshal(raw, &rows)
	out := []json.RawMessage{}
	for _, row := range rows {
		var t struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(row, &t) == nil && t.Path != "" && !b.agentHides(p, t.Path) {
			out = append(out, row)
		}
	}
	bts, _ := json.Marshal(out)
	return bts
}

// agentDefaultScreen is the workspace's default screen ({tiles, …}) for
// p's agent.
func (b *Broker) agentDefaultScreen(p auth.Principal, raw json.RawMessage) json.RawMessage {
	if p.Agent == "" || len(raw) == 0 {
		return raw
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil || doc == nil {
		return nil
	}
	doc["tiles"] = b.agentTiles(p, doc["tiles"])
	if len(doc["tiles"]) == 0 {
		doc["tiles"] = json.RawMessage("[]")
	}
	bts, _ := json.Marshal(doc)
	return bts
}

// agentFolders is a sidebar folder set ([{id, name, items: [tile, …]}])
// without the items p's agent isn't let into.
func (b *Broker) agentFolders(p auth.Principal, raw json.RawMessage) json.RawMessage {
	if p.Agent == "" || len(raw) == 0 {
		return raw
	}
	var folders []map[string]json.RawMessage
	_ = json.Unmarshal(raw, &folders)
	out := []map[string]json.RawMessage{}
	for _, f := range folders {
		var items []json.RawMessage
		_ = json.Unmarshal(f["items"], &items)
		kept := []string{}
		for _, it := range items {
			var s string
			if json.Unmarshal(it, &s) == nil && s != "" && !b.agentHides(p, s) {
				kept = append(kept, s)
			}
		}
		f["items"], _ = json.Marshal(kept)
		out = append(out, f)
	}
	bts, _ := json.Marshal(out)
	return bts
}

// DelegationPolicy is the delegation.Policy xbind runs with: the orgs'
// agent rules (agentpolicy.go: AgentTileAllows, AgentSessionAllows — the
// tile's orgs' switch, model rule and harness list, and the workspace's
// bounds) and people's agent preferences, which startPrefs answers
// (agentprefs.Store.StartPrefs: the mode, network and tile-API access a
// session their agent starts opens with, and whether it may start one
// without asking). A nil startPrefs starts nothing unasked.
//
// It is installed by boot. Nothing else is a policy: a Server, Broker or
// terminal manager without one refuses every delegate call
// (delegation.NoPolicy), and delegation.OpenPolicy is the tests'.
func (b *Broker) DelegationPolicy(startPrefs func(user, harness string) (mode, net string, api, ok bool)) delegation.Policy {
	return orgAgentRules{b: b, startPrefs: startPrefs}
}

type orgAgentRules struct {
	b          *Broker
	startPrefs func(user, harness string) (mode, net string, api, ok bool)
}

var errNoAgentRules = errors.New("the workspace's agent rules aren't set up: nothing is delegated")

func (o orgAgentRules) TileAllows(s delegation.Subject) error {
	if o.b == nil {
		return errNoAgentRules
	}
	return o.b.AgentTileAllows(s.Holder, s.User, s.Tile, s.Model)
}

func (o orgAgentRules) SessionAllows(s delegation.Subject, provider string) error {
	if o.b == nil {
		return errNoAgentRules
	}
	return o.b.AgentSessionAllows(s.Holder, s.User, s.Tile, s.Model, provider)
}

func (o orgAgentRules) StartPrefs(user, _, provider string) (delegation.StartPrefs, bool) {
	if o.startPrefs == nil {
		return delegation.StartPrefs{}, false
	}
	mode, net, api, ok := o.startPrefs(user, provider)
	if !ok {
		return delegation.StartPrefs{}, false
	}
	return delegation.StartPrefs{Mode: mode, Net: net, API: api}, true
}
