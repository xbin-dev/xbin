package server

// mcpextpolicy.go — the outside MCP servers of a workspace's agent policy
// (mcp.mode "listed", D240; docs/protocol/delegation.md §Outside MCP
// servers) as an admin writes and reads them (PUT/GET /delegate/workspace's
// mcp.servers) and as a person's Preferences show them (GET
// /delegate/state's net.mcp.servers). What is judged at a call is
// mcpext.go's.

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/users"
)

// mcpWorkspaceIn is PUT /delegate/workspace's mcp.
type mcpWorkspaceIn struct {
	Mode    *string        `json:"mode"`
	Servers *[]mcpServerIn `json:"servers"`
}

// mcpServerIn is one outside server of an admin's write. The read-only
// members of a read (server, host, hasSecret, byHost) are taken and
// ignored, so a card can send back what it read.
type mcpServerIn struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	Auth           string   `json:"auth"`
	Header         string   `json:"header"`
	Note           string   `json:"note"`
	Orgs           []string `json:"orgs"`
	PlainHTTP      bool     `json:"plainHttp"`
	PrivateNetwork bool     `json:"privateNetwork"`
	TrustReadOnly  bool     `json:"trustReadOnly"`
	// Secret: absent keeps the stored one, "" removes it
	Secret *string `json:"secret"`

	Server    string `json:"server"`
	Host      string `json:"host"`
	HasSecret *bool  `json:"hasSecret"`
	ByHost    *bool  `json:"byHost"`
}

// orgExists reports an org of this workspace (true when xbind keeps no
// accounts: nothing to check against).
func (s *Server) orgExists(id string) bool {
	if s.Auth == nil || s.Auth.Users == nil {
		return true
	}
	return slices.ContainsFunc(s.Auth.Users.Orgs(), func(o users.Org) bool { return o.ID == id })
}

// personOrgs are the orgs user is an active member of.
func (s *Server) personOrgs(user string) []string {
	if s.Auth == nil || s.Auth.Users == nil {
		return nil
	}
	var out []string
	for _, o := range s.Auth.Users.Orgs() {
		if m, ok := o.Member(user); ok && !m.Suspended {
			out = append(out, o.ID)
		}
	}
	return out
}

// mcpServersPatch is the admin's list as delegation takes it, with what
// only the server can judge: the orgs exist, the address isn't one of the
// workspace's own (its tiles are reached as tiles).
func (s *Server) mcpServersPatch(in *[]mcpServerIn) (*[]delegation.MCPServerPatch, error) {
	if in == nil {
		return nil, nil
	}
	out := make([]delegation.MCPServerPatch, 0, len(*in))
	for _, m := range *in {
		for _, o := range m.Orgs {
			if !s.orgExists(strings.TrimSpace(o)) {
				return nil, errors.New("mcp.servers " + m.ID + ": no organisation " + o)
			}
		}
		if u, err := url.Parse(strings.TrimSpace(m.URL)); err == nil && s.ownHost(u.Hostname()) {
			return nil, errors.New("mcp.servers " + m.ID + ": " + u.Hostname() + " is this workspace's own address — an MCP tile of the workspace is used as a tile, not listed")
		}
		out = append(out, delegation.MCPServerPatch{Secret: m.Secret, MCPServer: delegation.MCPServer{ID: strings.TrimSpace(m.ID), Name: m.Name, URL: m.URL,
			Auth: m.Auth, Header: m.Header, Note: m.Note, Orgs: m.Orgs, PlainHTTP: m.PlainHTTP, PrivateNetwork: m.PrivateNetwork, TrustReadOnly: m.TrustReadOnly}})
	}
	return &out, nil
}

// netWriteError answers a refused write of the web and MCP policy: what
// the host's file holds (409 {settings}), a secret the vault can't take
// now (409 {refusal: "vault"}), anything else 400.
func netWriteError(w http.ResponseWriter, err error) {
	var lk *delegation.ErrHostLocked
	var sv *delegation.ErrSecretVault
	switch {
	case errors.As(err, &lk):
		WriteJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "settings": lk.Settings, "docs": delegationDocs})
	case errors.As(err, &sv):
		WriteJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "refusal": "vault", "docs": delegationDocs})
	default:
		WriteError(w, http.StatusBadRequest, err.Error(), delegationDocs)
	}
}

// mcpVisible are the outside servers in force that user sees (their orgs),
// while the mode in force is listed; nil otherwise.
func (s *Server) mcpVisible(user string) []delegation.MCPServer {
	if s.Delegation == nil || s.Delegation.NetPolicy().MCP.Mode != delegation.MCPListed {
		return nil
	}
	orgs := s.personOrgs(user)
	var out []delegation.MCPServer
	for _, m := range s.Delegation.MCPServers() {
		if m.Visible(orgs) {
			out = append(out, m)
		}
	}
	return out
}

// mcpServersForPerson is net.mcp.servers of GET /delegate/state: the
// outside servers the person's agent may use, by name and host.
func (s *Server) mcpServersForPerson(user string) []map[string]any {
	out := []map[string]any{}
	for _, m := range s.mcpVisible(user) {
		out = append(out, map[string]any{"server": m.Server(), "name": m.Name, "host": m.Host(), "trustReadOnly": m.TrustReadOnly})
	}
	return out
}

// mcpHostView is the host's file's mcp as the admin's view shows it.
func mcpHostView(m *delegation.MCPPolicy) map[string]any {
	out := map[string]any{}
	if m.Mode != "" {
		out["mode"] = m.Mode
	}
	if m.Servers != nil {
		list := *m.Servers
		views := delegation.Views(list)
		for i := range views {
			views[i].ByHost = true
		}
		out["servers"] = views
	}
	return out
}
