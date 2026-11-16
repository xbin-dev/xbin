// tools_view.go — GET /tools: what this instance would offer a conversation
// right now (API.md "Config, models, features" → GET /tools). For each class
// the caller may start a conversation in, the tools it gets — the built-ins
// its toolsets give and every MCP tool it may call — each with its source;
// and every MCP server a conversation of the caller's could use, where it
// comes from (a binding of the `mcp` slot, a person's personal bind, the
// legacy config list, a server connected by URL) and whether it answers now.
//
// It is how anyone who can read the tile — a person, or their workspace
// agent at this tile's "agent": "read" — tells that a binding reached the
// backend: "apps/dice is bound, it answers, the internal class offers
// roll_dice from it". A read: the bound servers are asked initialize and
// tools/list (as a conversation's discovery would), nothing is stored, and
// no header, token or credential in an address is ever in the answer —
// a server connected by URL is not asked at all (its sign-in may refresh a
// token): its state is the one its row keeps.
package main

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	routeTables = append(routeTables, func() []routeDef {
		return []routeDef{{"GET /tools", needAny, handleTools}} // a read, a workspace agent's too (agentReadOpen)
	})
}

// toolsProbeFresh is how long one server's probe answers GET /tools again
// before the server is asked anew (a page polling it doesn't hammer them).
const toolsProbeFresh = 10 * time.Second

type toolsProbe struct {
	at    time.Time
	ok    bool
	err   string
	tools []toolSpec
}

var (
	toolsProbeMu sync.Mutex
	toolsProbes  = map[string]toolsProbe{}
)

// mcpServerView is one MCP server in GET /tools.
type mcpServerView struct {
	Name     string   `json:"name"`               // how its tools are named: mcp:<name>:<tool>
	Via      string   `json:"via"`                // "slot" (the mcp slot's binding), "config" (config.mcp), "direct" (connected by URL)
	Slot     string   `json:"slot,omitempty"`     // via slot: "mcp"
	Tile     string   `json:"tile,omitempty"`     // via slot: the provider tile
	Instance string   `json:"instance,omitempty"` // via slot: the provider's instance, when it names one
	Personal bool     `json:"personal,omitempty"` // a person's own (a personal bind, a personal server): their conversations only
	Global   bool     `json:"global,omitempty"`   // via direct: reached through the global instance
	URL      string   `json:"url,omitempty"`      // via config: scheme, host and path — never credentials or a query
	State    string   `json:"state,omitempty"`    // via direct: the row's state (ready, signin, …)
	Probed   bool     `json:"probed"`             // asked now (initialize + tools/list)
	OK       bool     `json:"reachable"`          // it answered (probed), or its row is ready (direct)
	Error    string   `json:"error,omitempty"`    // why not
	Tools    []string `json:"tools"`              // what it offers: now, else as a conversation last listed it
	ListedAt int64    `json:"listedAt,omitempty"` // when a conversation's list of it was last refreshed (unix s)
	At       int64    `json:"checkedAt,omitempty"`
}

// toolView is one tool a class offers.
type toolView struct {
	Name   string `json:"name"`             // as the server names it (roll_dice); a built-in's own name
	Call   string `json:"call,omitempty"`   // an MCP tool: mcp:<server>:<tool>, as a conversation calls it
	Source string `json:"source"`           // "builtin" | "mcp"
	Server string `json:"server,omitempty"` // an MCP tool's server (mcpServerView.Name)
}

// handleTools — GET /tools →
//
//	{default, classes: [{id, name, tools: [toolView], mcp: [server names], note?}],
//	 mcp: [mcpServerView]}
func handleTools(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	ctx := callerPersonal(r) // a person's own binds and servers: for them only (iface_personal.go)
	if !partitioned() && c.kind == whoUser && c.viewedBy == "" {
		ctx = withMCPOwner(ctx, c.user) // an unpartitioned agent's personal servers are their owner's (mcp_direct.go)
	}
	cfg := parseConfig(agent.db.getSetting("config"))
	servers := allMCPServersIn(ctx, cfg)
	views := make([]mcpServerView, len(servers))
	lists := make([][]toolSpec, len(servers))
	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Add(1)
		go func(i int, srv MCPServer) {
			defer wg.Done()
			views[i], lists[i] = viewMCPServer(r.Context(), srv)
		}(i, srv)
	}
	wg.Wait()

	st := currentClasses()
	if !wsMode() {
		st = loadClasses(agent.db) // fresh: another process may have saved
	}
	classes := []map[string]any{}
	for _, cls := range st.list {
		if !cls.usableBy(c) {
			continue
		}
		ccfg := cfg
		ccfg.setClass(cls, true)
		var mcp []toolSpec
		names := []string{}
		for i, srv := range servers {
			if cls.allowsMCP(srv.Name) {
				names = append(names, srv.Name)
				mcp = append(mcp, lists[i]...)
			}
		}
		tools := []toolView{}
		for _, s := range toolSpecs(ccfg, 0, mcp) {
			tools = append(tools, toolOf(s.Function.Name, servers))
		}
		row := map[string]any{"id": cls.ID, "name": cls.Name, "tools": tools, "mcp": names}
		switch {
		case !cls.has(tsInternal) && len(servers) > 0:
			row["note"] = "this class has no internal toolset: it gets no MCP tools"
		case len(servers) > 0 && len(names) < len(servers):
			row["note"] = "this class's mcp list leaves some servers out"
		}
		classes = append(classes, row)
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"default": st.defaultFor(c).ID, "classes": classes, "mcp": views})
}

// toolOf is a tool spec's row: an MCP tool by its server, else a built-in.
func toolOf(name string, servers []MCPServer) toolView {
	if rest, ok := strings.CutPrefix(name, "mcp:"); ok {
		for _, s := range servers {
			if tool, ok := strings.CutPrefix(rest, s.Name+":"); ok {
				return toolView{Name: tool, Call: name, Source: "mcp", Server: s.Name}
			}
		}
	}
	return toolView{Name: name, Source: "builtin"}
}

// viewMCPServer describes srv and lists the tools a conversation would be
// offered from it: the probe's when it answered, else the list a
// conversation last cached.
func viewMCPServer(ctx context.Context, srv MCPServer) (mcpServerView, []toolSpec) {
	v := mcpServerView{Name: srv.Name, Personal: srv.personal, Tools: []string{}}
	cached, hasCache := agent.cachedMCPTools(srv)
	if hasCache {
		v.ListedAt = cached.At
	}
	list := cached.Tools
	switch {
	case srv.direct != nil || srv.viaGlobal:
		v.Via, v.Global = "direct", srv.viaGlobal
		if srv.direct != nil {
			v.State, v.Error = srv.direct.State, srv.direct.Error
			v.Personal = v.Personal || srv.direct.Scope == mcpPersonal
		}
		v.OK = v.State == "" || v.State == mcpReady
	default:
		if srv.gateway {
			v.Via, v.Slot = "slot", "mcp"
			v.Tile, v.Instance, _ = strings.Cut(srv.Name, "#")
		} else {
			v.Via, v.URL = "config", addrOnly(srv.URL)
		}
		p := probeServer(ctx, srv)
		v.Probed, v.OK, v.Error, v.At = true, p.ok, p.err, p.at.Unix()
		if p.ok {
			list = p.tools
		}
	}
	for _, t := range list {
		v.Tools = append(v.Tools, strings.TrimPrefix(t.Function.Name, "mcp:"+srv.Name+":"))
	}
	return v, list
}

// probeServer asks srv initialize and tools/list, bounded as a conversation's
// discovery is; an answer younger than toolsProbeFresh is reused. Nothing
// is stored but that answer, in memory.
func probeServer(ctx context.Context, srv MCPServer) toolsProbe {
	key := mcpCacheKey(srv)
	toolsProbeMu.Lock()
	if p, ok := toolsProbes[key]; ok && time.Since(p.at) < toolsProbeFresh {
		toolsProbeMu.Unlock()
		return p
	}
	toolsProbeMu.Unlock()
	pctx, cancel := context.WithTimeout(ctx, mcpDiscoverTimeout)
	defer cancel()
	tools, err := listMCPTools(pctx, srv)
	p := toolsProbe{at: time.Now(), ok: err == nil, tools: tools}
	if err != nil {
		p.err = clip(redactAddr(err.Error()), 500)
	}
	toolsProbeMu.Lock()
	for k, old := range toolsProbes { // what went stale goes
		if time.Since(old.at) > time.Minute {
			delete(toolsProbes, k)
		}
	}
	toolsProbes[key] = p
	toolsProbeMu.Unlock()
	return p
}

// addrOnly is an address without what can carry a credential: its user
// info, query and fragment.
func addrOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}

// anyAddr is an address quoted in an error message.
var anyAddr = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>]+`)

// redactAddr is msg with every address in it cut to addrOnly's (an error
// from the HTTP client quotes the URL it called, its query included).
func redactAddr(msg string) string {
	return anyAddr.ReplaceAllStringFunc(msg, func(a string) string {
		if short := addrOnly(a); short != "" {
			return short
		}
		return "‹an address›"
	})
}
