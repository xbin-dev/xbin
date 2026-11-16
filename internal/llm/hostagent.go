package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xbin-dev/xbin/internal/delegation"
)

// hostagent.go — the workspace agent's key in the host's file (D237;
// docs/llm.md "The host's file", docs/workspace-agent.md "Set by your
// host"): the deployment file is read here, so its one key that isn't the
// connector's is read here too, and validated with the rest — a file that
// misspells it stops the boot, a changed one that does is refused and the
// last good one stays in force. xbind puts it in force in
// internal/server/agenthost.go.
//
//	"workspaceAgent": false                       agents locked off
//	"workspaceAgent": true                        locked on; admins choose who
//	"workspaceAgent": {"enabled": true, "who": "admins" | "everyone"}
//	"workspaceAgent": {"enabled": false}          the same as false
//	"workspaceAgent": {"web": {"mode": "read", "allow": [...], "deny": [...]},
//	                   "mcp": {"mode": "tiles"}}   the web and MCP policy (D239):
//	                                              a mode is a preset and a ceiling an admin
//	                                              may narrow (internal/delegation/hostfile.go);
//	                                              without "enabled" the switch stays the admins'

// HostAgent is what the host's file says about workspace agents.
type HostAgent struct {
	Set     bool   // the file sets the switch: it is the host's
	Enabled bool   // agents on (true) or off
	Who     string // "" (admins choose), "admins" (each admin account has one), "everyone"
	// Web, MCP: the host's web and MCP policy for people's agents (D239;
	// nil: unset) — validated as an admin's would be
	Web *delegation.WebPolicy
	MCP *delegation.MCPPolicy
}

// Who values the host's file may name.
const (
	HostWhoAdmins   = "admins"
	HostWhoEveryone = "everyone"
)

// UnmarshalJSON reads the key strictly: a bool, or an object whose every
// field is one of these. null is "not set".
func (h *HostAgent) UnmarshalJSON(b []byte) error {
	*h = HostAgent{}
	b = bytes.TrimSpace(b)
	switch string(b) {
	case "null":
		return nil
	case "true", "false":
		*h = HostAgent{Set: true, Enabled: string(b) == "true"}
		return nil
	}
	var o struct {
		Enabled *bool                 `json:"enabled"`
		Who     *string               `json:"who"`
		Web     *delegation.WebPolicy `json:"web"`
		MCP     *delegation.MCPPolicy `json:"mcp"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields() // a misspelled setting is an error, never a rule silently not applied
	if err := dec.Decode(&o); err != nil {
		return fmt.Errorf(`workspaceAgent: want false, true or {"enabled": bool, "who"?: "admins" | "everyone", "web"?: {"mode", "allow", "deny"}, "mcp"?: {"mode"}}: %w`, err)
	}
	if o.Web != nil {
		w, err := o.Web.Normalized()
		if err != nil {
			return fmt.Errorf("workspaceAgent: web: %w", err)
		}
		h.Web = &w
	}
	if o.MCP != nil {
		m, err := o.MCP.Normalized()
		if err != nil {
			return fmt.Errorf("workspaceAgent: mcp: %w", err)
		}
		h.MCP = &m
	}
	if o.Enabled == nil {
		if o.Who != nil || (o.Web == nil && o.MCP == nil) {
			return errors.New(`workspaceAgent: enabled is required ({"enabled": true, "who": "admins"})`)
		}
		return nil // the web and MCP policy alone: the switch stays the admins'
	}
	h.Set, h.Enabled = true, *o.Enabled
	if o.Who == nil {
		return nil
	}
	switch *o.Who {
	case HostWhoAdmins, HostWhoEveryone:
	default:
		return fmt.Errorf(`workspaceAgent: who is "admins" or "everyone", not %q`, *o.Who)
	}
	if !*o.Enabled {
		return errors.New(`workspaceAgent: who names who gets an agent, so it goes with "enabled": true`)
	}
	h.Who = *o.Who
	return nil
}

// HostWorkspaceAgent is the workspace agent's key in the host's file in
// force now (the file looked at again when it is due); Set false when no
// file is in force or it has no such key.
func (s *Service) HostWorkspaceAgent() HostAgent {
	s.current() // the file's reload, when it is due
	h, _ := s.host.current()
	if h == nil {
		return HostAgent{}
	}
	return h.WorkspaceAgent
}

// HostAgentSignIns is the host's file's word on saved coding-agent
// sign-ins (its agentSignIns key, D230): set false when no file is in force
// or it doesn't name the key — the switch is the admins' then.
func (s *Service) HostAgentSignIns() (set, enabled bool) {
	s.current() // the file's reload, when it is due
	h, _ := s.host.current()
	if h == nil || h.AgentSignIns == nil {
		return false, false
	}
	return true, *h.AgentSignIns
}
