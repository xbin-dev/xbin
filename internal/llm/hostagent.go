package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

// HostAgent is what the host's file says about workspace agents.
type HostAgent struct {
	Set     bool   // the file has the key: the switch is the host's
	Enabled bool   // agents on (true) or off
	Who     string // "" (admins choose), "admins" (each admin account has one), "everyone"
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
		Enabled *bool   `json:"enabled"`
		Who     *string `json:"who"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields() // a misspelled setting is an error, never a rule silently not applied
	if err := dec.Decode(&o); err != nil {
		return fmt.Errorf(`workspaceAgent: want false, true or {"enabled": bool, "who"?: "admins" | "everyone"}: %w`, err)
	}
	if o.Enabled == nil {
		return errors.New(`workspaceAgent: enabled is required ({"enabled": true, "who": "admins"})`)
	}
	*h = HostAgent{Set: true, Enabled: *o.Enabled}
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
