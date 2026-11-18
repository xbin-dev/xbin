// ws_xbin_standing.go — the two delegate routes of D246 on the wire
// (xbin's /docs/protocol/delegation.md §Where a tile stands and §A person's
// access): one read of where a tile stands, and whether an account of a
// name exists. Beside ws_xbin_http.go, which knows the rest of xbind's
// routes; an xbind without them answers errWsUnavailable (wsMissing), and
// the tools fall back to what they did before.
package main

import (
	"context"
	"net/http"
	"net/url"
)

// wsTileView is GET /delegate/tile's answer: where one tile stands, each
// part under the gate of the read it replaces.
type wsTileView struct {
	Tile           string           `json:"tile"`
	Title          string           `json:"title"`
	Kind           string           `json:"kind"`
	Runtime        string           `json:"runtime"`
	Orgs           []string         `json:"orgs"`
	Level          string           `json:"level"`
	Agent          string           `json:"agent"`
	AgentRequested string           `json:"agentRequested"`
	MCP            bool             `json:"mcp"`
	Owner          *wsTileViewOwner `json:"owner"`
	Report         *struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	} `json:"report"`
	Alerts  []string           `json:"alerts"`
	Backend *wsTileViewBackend `json:"backend"`
	Wiring  bool               `json:"wiring"`
	Unmet   bool               `json:"unmet"` // without Wiring: a required slot unbound or a use not granted
	Slots   []struct {
		Slot     string   `json:"slot"`
		Kind     string   `json:"kind"`
		Service  string   `json:"service"`
		Required bool     `json:"required"`
		Multi    bool     `json:"multi"`
		Bound    []string `json:"bound"`
	} `json:"slots"`
	Uses []struct {
		Target  string `json:"target"`
		Role    string `json:"role"`
		Granted bool   `json:"granted"`
		Pending bool   `json:"pending"`
	} `json:"uses"`
	Provides []struct {
		Slot    string `json:"slot"`
		Kind    string `json:"kind"`
		Service string `json:"service"`
		Role    string `json:"role"`
	} `json:"provides"`
	Exposes []struct {
		Slot  string   `json:"slot"`
		Kind  string   `json:"kind"`
		Paths []string `json:"paths"`
	} `json:"exposes"`
	Cron []struct {
		Name       string `json:"name"`
		Schedule   string `json:"schedule"`
		Path       string `json:"path"`
		Next       string `json:"next"`
		LastRun    string `json:"lastRun"`
		LastStatus int    `json:"lastStatus"`
		Dormant    bool   `json:"dormant"`
	} `json:"cron"`
	Log struct {
		Shown bool     `json:"shown"`
		Lines []string `json:"lines"`
		Why   string   `json:"why"`
	} `json:"log"`
	Sessions []struct {
		ID       string `json:"id"`
		By       string `json:"by"`
		Status   string `json:"status"`
		Mode     string `json:"mode"`
		Provider string `json:"provider"`
		Waits    int    `json:"waits"`
		Turn     int    `json:"turn"`
		Last     string `json:"last"`
	} `json:"sessions"`
}

type wsTileViewOwner struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type wsTileViewBackend struct {
	State    string `json:"state"`
	Error    string `json:"error"`
	Restarts int    `json:"restarts"`
	Retry    *struct {
		Attempt int    `json:"attempt"`
		Of      int    `json:"of"`
		At      string `json:"at"`
	} `json:"retry"`
	Flag      string `json:"flag"`
	Lifecycle string `json:"lifecycle"` // the tile isn't enabled: disabled, offloaded…
	Words     string `json:"words"`
}

// wsAccount is POST /delegate/accounts/lookup's answer.
type wsAccount struct {
	Name        string `json:"name"`
	Exists      *bool  `json:"exists"`
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Ambiguous   bool   `json:"ambiguous"`
	Unseen      bool   `json:"unseen"`
	Why         string `json:"why"`
}

// TileStanding is GET /delegate/tile?tile= for the conversation.
func (h wsHTTP) TileStanding(ctx context.Context, sid, tile string) (wsTileView, error) {
	var out wsTileView
	return out, h.get(ctx, sid, "/api/xbin/delegate/tile?tile="+url.QueryEscape(tile), &out)
}

// AccountLookup is POST /delegate/accounts/lookup {name}.
func (h wsHTTP) AccountLookup(ctx context.Context, sid, name string) (wsAccount, error) {
	var out wsAccount
	return out, h.json(ctx, sid, http.MethodPost, "/api/xbin/delegate/accounts/lookup", map[string]string{"name": name}, &out)
}
