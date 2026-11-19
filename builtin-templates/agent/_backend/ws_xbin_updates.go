// ws_xbin_updates.go — the delegate read of D248 on the wire (xbin's
// /docs/protocol/delegation.md §Updates): what has an update waiting, as
// the person's own update listings give it, under the conversation's
// gates, with the next step and xbind's brief. An xbind without it answers
// errWsUnavailable (wsMissing), and the tool says it can't tell.
package main

import (
	"context"
	"net/url"
)

// wsUpdateBrief is xbind's brief for a tile agent that applies an update:
// tile_agent_brief's fields, and the commands it names.
type wsUpdateBrief struct {
	Goal       string   `json:"goal"`
	DoneWhen   string   `json:"doneWhen"`
	LeaveAlone string   `json:"leaveAlone"`
	CheckBy    string   `json:"checkBy"`
	Known      string   `json:"known"`
	Commands   []string `json:"commands"`
}

// wsBuiltinUpdate is one builtin row of GET /delegate/updates.
type wsBuiltinUpdate struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"` // tile | scaffold
	Tile      string         `json:"tile"`
	Title     string         `json:"title"`
	From      int            `json:"from"`
	To        int            `json:"to"`
	Missing   bool           `json:"missing"`
	Adopted   bool           `json:"adopted"`
	Clean     int            `json:"clean"`
	Conflicts int            `json:"conflicts"`
	Edited    int            `json:"edited"`
	Level     string         `json:"level"`
	Agent     string         `json:"agent"`
	Proposal  int            `json:"proposal"`
	Next      string         `json:"next"` // propose | tile-agent | by-hand
	Why       string         `json:"why"`
	Brief     *wsUpdateBrief `json:"brief"`
}

// wsInstanceUpdate is one template instance row of GET /delegate/updates.
type wsInstanceUpdate struct {
	Tile     string         `json:"tile"`
	Title    string         `json:"title"`
	Template string         `json:"template"`
	Behind   int            `json:"behind"`
	Local    int            `json:"local"`
	Legacy   bool           `json:"legacy"`
	Level    string         `json:"level"`
	Agent    string         `json:"agent"`
	Next     string         `json:"next"`
	Why      string         `json:"why"`
	Brief    *wsUpdateBrief `json:"brief"`
}

// wsUpdates is GET /delegate/updates' answer.
type wsUpdates struct {
	Builtins  []wsBuiltinUpdate  `json:"builtins"`
	Instances []wsInstanceUpdate `json:"instances"`
	LeftOut   struct {
		HiddenByYou int `json:"hiddenByYou"`
		Blocked     int `json:"blocked"`
	} `json:"leftOut"`
}

// Updates is GET /delegate/updates[?tile=] for the conversation.
func (h wsHTTP) Updates(ctx context.Context, sid, tile string) (wsUpdates, error) {
	path := "/api/xbin/delegate/updates"
	if tile != "" {
		path += "?tile=" + url.QueryEscape(tile)
	}
	var out wsUpdates
	return out, h.get(ctx, sid, path, &out)
}
