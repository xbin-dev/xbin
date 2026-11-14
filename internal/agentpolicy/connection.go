package agentpolicy

import (
	"fmt"
	"slices"
	"sort"
)

// State is what a tile or an org is to a workspace agent session.
type State string

const (
	// Connected: in the session's scope and allowed with its model.
	Connected State = "connected"
	// Hidden: the person left it out of the session's scope.
	Hidden State = "hidden"
	// Blocked: a rule of the workspace or of one of the tile's orgs refuses
	// it (Reason says which and why) — whatever the scope.
	Blocked State = "blocked"
	// NoAccess: the scope names it, but the person can't read it (or it
	// doesn't exist: the two aren't told apart).
	NoAccess State = "no-access"
)

// Org is one org as the connection view needs it.
type Org struct {
	ID, Name string
	Rules    Rules
}

// Tile is one tile the person can read: the org that owns it ("" for a
// tile no org owns) and every org it falls under — the owner and the orgs
// it is shared into.
type Tile struct {
	Path, Name string
	Owner      string
	Orgs       []string
}

// Scope is what a person lets one workspace agent session see: orgs
// (every tile under them; WorkspaceID for the tiles in no org) and single
// tiles. The empty scope is everything they can see.
type Scope struct {
	Orgs  []string `json:"orgs"`
	Tiles []string `json:"tiles"`
}

// Empty reports the default scope: everything.
func (s Scope) Empty() bool { return len(s.Orgs) == 0 && len(s.Tiles) == 0 }

// Input is what Connection reads: the workspace's bounds, the orgs the
// person has anything to do with (a member, or one of their readable
// tiles falls under it) and the tiles they can read — never a tile or an
// org they can't see: a scope entry that matches none of these is
// answered no-access, existing or not.
type Input struct {
	Bounds Rules
	Orgs   []Org
	Tiles  []Tile
	Scope  Scope
	Model  *ModelRef
}

// OrgState is one org's row. Tiles counts the tiles listed under it and
// Connected how many of those are connected (fewer than Tiles: partly
// connected).
type OrgState struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"` // org | workspace (the tiles in no org)
	State     State  `json:"state"`
	Reason    string `json:"reason,omitempty"`
	Rule      string `json:"rule,omitempty"`
	Tiles     int    `json:"tiles"`
	Connected int    `json:"connected"`
	Rules     *Rules `json:"rules,omitempty"` // the org's own rules, when it has any
}

// TileState is one tile's row: Org is the group it is listed under (its
// owning org, else the first org it is shared into, else WorkspaceID),
// Orgs every org whose rules apply when there is more than one, By the
// layer that blocked it, Harnesses what an agent session there may run.
type TileState struct {
	Tile      string   `json:"tile"`
	Name      string   `json:"name,omitempty"`
	Org       string   `json:"org"`
	Orgs      []string `json:"orgs,omitempty"`
	State     State    `json:"state"`
	Reason    string   `json:"reason,omitempty"`
	By        string   `json:"by,omitempty"`
	Rule      string   `json:"rule,omitempty"`
	Harnesses Allow    `json:"harnesses"`
}

// Counts are the totals a chip shows. Tiles and Orgs count what is
// connected; NotConnected is every other tile (Hidden + Blocked +
// NoAccess).
type Counts struct {
	Tiles        int    `json:"tiles"`
	Orgs         int    `json:"orgs"`
	NotConnected int    `json:"notConnected"`
	Hidden       int    `json:"hidden"`
	Blocked      int    `json:"blocked"`
	NoAccess     int    `json:"noAccess"`
	Summary      string `json:"summary"`
}

// View is the connection state of one session: who is hooked up and who
// is not, and why.
type View struct {
	Model  *ModelRef   `json:"model,omitempty"`
	Scope  Scope       `json:"scope"`
	Bounds Rules       `json:"bounds"`
	Orgs   []OrgState  `json:"orgs"`
	Tiles  []TileState `json:"tiles"`
	Counts Counts      `json:"counts"`
}

// layersOf is the layers tile falls under: the bounds, then its orgs.
func layersOf(in Input, orgs map[string]Org, t Tile) []Layer {
	ls := []Layer{{Rules: in.Bounds}}
	for _, id := range t.Orgs {
		if o, ok := orgs[id]; ok {
			ls = append(ls, Layer{ID: o.ID, Name: o.Name, Rules: o.Rules})
		}
	}
	return ls
}

// groupOf is the org a tile is listed under.
func groupOf(t Tile) string {
	switch {
	case t.Owner != "":
		return t.Owner
	case len(t.Orgs) > 0:
		return t.Orgs[0]
	}
	return WorkspaceID
}

// inScope: the scope names the tile, or one of the orgs it falls under.
func inScope(s Scope, t Tile) bool {
	if s.Empty() || slices.Contains(s.Tiles, t.Path) {
		return true
	}
	if len(t.Orgs) == 0 {
		return slices.Contains(s.Orgs, WorkspaceID)
	}
	return slices.ContainsFunc(t.Orgs, func(id string) bool { return slices.Contains(s.Orgs, id) })
}

// Connection is the view for in: every org and tile with its state, and
// the totals. A blocked tile reads blocked even when the scope leaves it
// out — choosing it would not connect it.
func Connection(in Input) View {
	orgs := map[string]Org{}
	for _, o := range in.Orgs {
		orgs[o.ID] = o
	}
	scope := Scope{Orgs: normList(in.Scope.Orgs), Tiles: normList(in.Scope.Tiles)}
	v := View{Model: in.Model, Scope: scope, Bounds: in.Bounds, Orgs: []OrgState{}, Tiles: []TileState{}}

	rows := map[string]*OrgState{}
	row := func(id string) *OrgState {
		if r := rows[id]; r != nil {
			return r
		}
		r := &OrgState{ID: id, Name: id, Kind: "org", State: Connected}
		if id == WorkspaceID {
			r.Name, r.Kind = "Not in an org", "workspace"
		} else if o, ok := orgs[id]; ok {
			r.Name = o.Name
			if !o.Rules.IsZero() {
				rules := o.Rules
				r.Rules = &rules
			}
		}
		rows[id] = r
		return r
	}
	for _, o := range in.Orgs {
		row(o.ID)
	}

	tiles := slices.Clone(in.Tiles)
	sort.Slice(tiles, func(i, j int) bool { return tiles[i].Path < tiles[j].Path })
	known := map[string]bool{}
	inScopeTiles := map[string]int{} // group → its tiles the scope takes in
	for _, t := range tiles {
		t.Orgs = normList(t.Orgs)
		known[t.Path] = true
		layers := layersOf(in, orgs, t)
		ts := TileState{Tile: t.Path, Name: t.Name, Org: groupOf(t), State: Connected, Harnesses: Harnesses(layers)}
		if len(t.Orgs) > 1 {
			ts.Orgs = t.Orgs
		}
		g := row(ts.Org)
		g.Tiles++
		sel := inScope(scope, t)
		if sel {
			inScopeTiles[ts.Org]++
		}
		if d := Check(layers, Ask{WorkspaceAgent: true, Model: v.Model}); !d.Allowed {
			ts.State, ts.Reason, ts.By, ts.Rule = Blocked, d.Reason, d.By, d.Rule
		} else if !sel {
			ts.State = Hidden
		} else {
			g.Connected++
		}
		v.Tiles = append(v.Tiles, ts)
	}
	// what the scope names that the person can't see
	for _, p := range scope.Tiles {
		if !known[p] {
			v.Tiles = append(v.Tiles, TileState{Tile: p, Org: "", State: NoAccess, Reason: "You don't have access to this tile, or it doesn't exist"})
		}
	}
	for _, id := range scope.Orgs {
		if _, ok := orgs[id]; !ok && id != WorkspaceID && rows[id] == nil {
			r := row(id)
			r.State, r.Reason = NoAccess, "You aren't in this org and can see none of its tiles, or it doesn't exist"
		}
	}

	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { // orgs by id, the tiles in no org last
		if (ids[i] == WorkspaceID) != (ids[j] == WorkspaceID) {
			return ids[j] == WorkspaceID
		}
		return ids[i] < ids[j]
	})
	for _, id := range ids {
		r := rows[id]
		if r.State != NoAccess {
			// the org's own answer: its rules and the bounds, whatever its tiles
			layers := []Layer{{Rules: in.Bounds}}
			if o, ok := orgs[id]; ok {
				layers = append(layers, Layer{ID: o.ID, Name: o.Name, Rules: o.Rules})
			}
			switch d := Check(layers, Ask{WorkspaceAgent: true, Model: v.Model}); {
			case !d.Allowed:
				r.State, r.Reason, r.Rule = Blocked, d.Reason, d.Rule
			case !scope.Empty() && !slices.Contains(scope.Orgs, id) && inScopeTiles[id] == 0:
				r.State = Hidden
			}
		}
		if r.State == Connected && r.Kind == "org" && r.Connected > 0 {
			v.Counts.Orgs++
		}
		v.Orgs = append(v.Orgs, *r)
	}
	for _, t := range v.Tiles {
		switch t.State {
		case Connected:
			v.Counts.Tiles++
		case Hidden:
			v.Counts.Hidden++
		case Blocked:
			v.Counts.Blocked++
		case NoAccess:
			v.Counts.NoAccess++
		}
	}
	v.Counts.NotConnected = v.Counts.Hidden + v.Counts.Blocked + v.Counts.NoAccess
	v.Counts.Summary = summary(v.Counts)
	return v
}

func plural(n int, one string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %ss", n, one)
}

// summary: "14 tiles · 3 orgs connected · 6 not connected".
func summary(c Counts) string {
	s := plural(c.Tiles, "tile")
	if c.Orgs > 0 {
		s += " · " + plural(c.Orgs, "org")
	}
	s += " connected"
	if c.NotConnected > 0 {
		s += fmt.Sprintf(" · %d not connected", c.NotConnected)
	}
	return s
}

// Change is what a model change would do: the tiles it would connect and
// the ones it would disconnect (each with the state and reason it would
// have), and the totals after it.
type Change struct {
	Model      *ModelRef   `json:"model,omitempty"`
	Connect    []TileState `json:"connect"`
	Disconnect []TileState `json:"disconnect"`
	Counts     Counts      `json:"counts"`
}

// WhatIf compares in's view with the one the same scope would have with
// model to.
func WhatIf(in Input, to *ModelRef) Change {
	now := Connection(in)
	in.Model = to
	then := Connection(in)
	was := map[string]State{}
	for _, t := range now.Tiles {
		was[t.Tile] = t.State
	}
	ch := Change{Model: to, Connect: []TileState{}, Disconnect: []TileState{}, Counts: then.Counts}
	for _, t := range then.Tiles {
		switch {
		case t.State == Connected && was[t.Tile] != Connected:
			ch.Connect = append(ch.Connect, t)
		case t.State != Connected && was[t.Tile] == Connected:
			ch.Disconnect = append(ch.Disconnect, t)
		}
	}
	return ch
}
