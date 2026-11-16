// ws_empty.go — the empty state of a workspace-agent session (API.md
// §Workspace mode; GET /ws/empty): what the agent can and can't do, with
// this session's own numbers, and starter requests built from the person's
// own tiles as they stand — a tile that reports a failure becomes a "Fix"
// starter. Computed from xbind's map of the session, with no model call:
// it is shown before the first message, and must cost nothing.
//
// A starter fills the composer and is never sent by itself; none proposes
// a destructive or an admin action. The words are the product's copy deck.
package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// wsStarter is one starter request. Blank: it has a ___ for the person to
// fill before sending.
type wsStarter struct {
	Kind  string `json:"kind"` // Ask | Fix | Change | New
	Text  string `json:"text"`
	Tile  string `json:"tile,omitempty"`
	Blank bool   `json:"blank,omitempty"`
}

const wsStartersMax = 4

// wsTroubled says what is wrong with t, in a clause ("" when nothing is).
func wsTroubled(t wsTile) string {
	rep := strings.ToLower(t.Report)
	switch {
	case t.Status == "failed" || t.Status == "error" || t.Status == "crashed":
		return "isn't running"
	case strings.HasPrefix(rep, "error") || strings.HasPrefix(rep, "warn"):
		_, msg, _ := strings.Cut(t.Report, ":")
		if msg = strings.TrimSpace(oneLineTitle(coordPlain(msg))); msg != "" {
			return "reports \"" + clip(msg, 80) + "\""
		}
		return "reports a problem"
	case len(t.Alerts) > 0:
		return "has a warning from xbin"
	}
	return ""
}

// wsStarters builds the starters from the session's tiles.
func wsStarters(tiles []wsTile) []wsStarter {
	usable := make([]wsTile, 0, len(tiles))
	for _, t := range tiles {
		if t.Agent != "off" { // a console's API is closed to agents: nothing to start there
			usable = append(usable, t)
		}
	}
	sort.Slice(usable, func(i, j int) bool { return usable[i].name() < usable[j].name() })
	name := func(t wsTile) string { return clip(oneLineTitle(coordPlain(t.name())), 60) }
	var out []wsStarter
	for _, t := range usable {
		if why := wsTroubled(t); why != "" && len(out) < 2 {
			out = append(out, wsStarter{Kind: "Fix", Tile: t.Path, Text: fmt.Sprintf("%s %s. Find out why.", name(t), why)})
		}
	}
	if len(usable) < 2 { // too little of their own to build from: the generic ones
		out = append(out, wsStarter{Kind: "Ask", Text: "What can you see in this workspace, and what state is it in?"},
			wsStarter{Kind: "New", Text: "Make a new tile for ___", Blank: true})
		return out
	}
	for _, t := range usable {
		if t.Running {
			out = append(out, wsStarter{Kind: "Ask", Tile: t.Path, Text: fmt.Sprintf("What is the tile agent in %s working on?", name(t))})
			break
		}
	}
	pick := func(skip string) wsTile {
		for _, t := range usable {
			if t.Path != skip && wsTroubled(t) == "" {
				return t
			}
		}
		return usable[0]
	}
	a := pick("")
	b := pick(a.Path)
	out = append(out,
		wsStarter{Kind: "Ask", Tile: a.Path, Text: fmt.Sprintf("What does %s do, and who can use it?", name(a))},
		wsStarter{Kind: "Change", Tile: b.Path, Text: fmt.Sprintf("Add ___ to %s", name(b)), Blank: true},
		wsStarter{Kind: "New", Text: "Make a new tile for ___", Blank: true})
	if len(out) > wsStartersMax {
		out = out[:wsStartersMax]
	}
	return out
}

// wsCanCant are the empty state's two lists: nothing in "can't" is a thing
// the agent can get done another way, and what the session's reach turns
// on or off is said with who can change it.
func wsCanCant(c wsConnection, reach wsReach) (can, cant []string) {
	can = []string{
		fmt.Sprintf("Look across your %d connected tiles", c.Counts.Tiles),
		"Change a tile's code — by briefing that tile's own agent",
		"Do things in a tile for you, once you approve",
		"Remember what you tell it",
	}
	switch reach.Web.State {
	case "on":
		can = append(can, "Read web pages")
	case "asks":
		can = append(can, "Read web pages you approve, one by one")
	}
	if reach.MCP.on() {
		can = append(can, "Use MCP tools from your tiles, once you approve each")
	}
	cant = []string{"Answer a tile agent's questions for you — those come to you", "Act without your approval where a change is involved"}
	if not := c.Counts.NotConnected; not > 0 {
		cant = append(cant, fmt.Sprintf("See the %d tiles that aren't connected", not))
	}
	if !reach.Web.on() {
		cant = append(cant, "Use the web — "+wsWhoTurnsOn(reach.Web, "web-person"))
	}
	if !reach.MCP.on() {
		cant = append(cant, "Use MCP tools — "+wsWhoTurnsOn(reach.MCP, "mcp-person"))
	}
	return can, cant
}

// wsWhoTurnsOn is who can turn a capability on, in the empty state's words.
func wsWhoTurnsOn(r wsReachOne, person string) string {
	switch {
	case r.Code == person:
		return "you can turn it on in Preferences"
	case r.Code == "web-org":
		return "this conversation holds data of an org that keeps it off the web; a new session can"
	case strings.Contains(r.Why, "whoever runs this xbin"):
		return "off for this workspace, set by whoever runs this xbin"
	}
	return "off for this workspace — an admin can turn it on"
}

func handleWsEmpty(w http.ResponseWriter, r *http.Request) {
	owner, ok := wsCaller(w, r)
	if !ok {
		return
	}
	s := wsQuerySession(r, owner)
	if s == nil {
		xbin.WriteError(w, http.StatusNotFound, "no session yet: POST /ws/sessions {main: true} makes Main")
		return
	}
	agent.wsRefresh(r.Context(), s, false)
	c := s.Conn.Connection
	not := c.Counts.NotConnected
	can, cant := wsCanCant(c, s.Conn.Reach)
	out := map[string]any{
		"heading": "Ask for an outcome",
		"line":    "The workspace agent plans the work, hands building to each tile's own agent, and asks you before it changes anything itself.",
		"can":     can, "cant": cant, "connected": c.Counts.Tiles, "notConnected": not, "summary": wsConnLine(c),
		"placeholder":   "Ask for an outcome…",
		"hint":          "Say what you want and how you'll know it's done. The agent works out the steps.",
		"startersTitle": "Try one — from your tiles",
	}
	// nobody — an admin included — has an agent that can act unless xbind
	// says so: a switch that is off (or no agent given) is the empty state's
	// first word, not a list of what it could do
	if o := wsOffNow(r.Context()); o.Code != "" {
		out["off"], out["line"], out["can"], out["starters"] = o.Code, o.switchWords(), []string{}, []wsStarter{}
		xbin.WriteJSON(w, http.StatusOK, out)
		return
	}
	var m wsMap
	if err := agent.wsWith(r.Context(), s, func(sid string) (err error) { m, err = wsX.Map(r.Context(), sid); return }); err != nil {
		out["starters"], out["startersError"] = []wsStarter{}, wsRefusal(err)
	} else {
		out["starters"] = wsStarters(m.Tiles)
	}
	xbin.WriteJSON(w, http.StatusOK, out)
}
