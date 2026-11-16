package server

// delegatemcp.go — a person's workspace agent with MCP servers (D236;
// docs/protocol/delegation.md §Web and MCP). An MCP tile is one whose
// manifest provides an http interface of service "mcp" (its endpoint
// /api/<tile>/mcp).

import (
	"sort"

	"github.com/xbin-dev/xbin/internal/registry"
)

// mcpService is the interface service an MCP server provides.
const mcpService = "mcp"

// mcpTiles lists the tiles of this workspace that provide an MCP server,
// by path.
func (s *Server) mcpTiles() []*registry.Component {
	if s.Reg == nil {
		return nil
	}
	var out []*registry.Component
	for _, c := range s.Reg.Components() {
		if c.IsTemplate() || c.Sealed || isChrome(c.Path) {
			continue
		}
		for _, pr := range c.Manifest.Provides {
			if pr.Kind == "http" && pr.Service == mcpService && !pr.Instances {
				out = append(out, c)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// mcpTilesNone reports a workspace with no MCP tile at all.
func (s *Server) mcpTilesNone() bool { return len(s.mcpTiles()) == 0 }
