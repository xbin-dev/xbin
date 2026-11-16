package server

// agenthost.go — workspace agents as the host sets them (D236;
// docs/workspace-agent.md "Set by your host"): the deployment file the LLM
// connector reads (XBIN_LLM_CONFIG) may carry a "workspaceAgent" key beside
// the connector's settings (internal/llm/hostagent.go parses it), and
// whatever it sets is locked the way the connector's settings are — reads
// say so (locks: {enabled, who}: {locked, reason}), writes through the API
// are refused, and xbind keeps it in force:
//
//   - enabled: the workspace's switch is the host's value. Written to the
//     delegation store (by "host") when it differs, so every reader of the
//     switch — the delegate gate first — reads the host's;
//   - who "everyone": the workspace defaults name the agent;
//   - who "admins": the defaults' entry goes, and every admin account has
//     an entry of its own — given at boot (the key first appearing), when
//     the file changes, and whenever people change (an admin made or made
//     an admin: a `users` event).
//
// The host's file is looked at by the connector at most every 2 s, on its
// own reads; ApplyHostAgent runs when it noticed a change (llm.Options.
// HostChanged), at boot, and on every `users` event.

import (
	"log/slog"
	"net/http"
	"sync"
)

// HostAgentRule is the host's rule for workspace agents (Set false: none).
type HostAgentRule struct {
	Set     bool
	Enabled bool
	Who     string // "" (admins choose), "admins", "everyone"
}

// hostLockReason is what a locked control says, as the connector's do.
const hostLockReason = "set by your host"

var hostAgentMu sync.Mutex // one ApplyHostAgent at a time

// Test seams (nil in production): where the two writers of the host's rule
// can be made to meet.
var (
	hostAgentLocking func() // ApplyHostAgent, just before it takes hostAgentMu
	hostAgentChecked func() // PUT /delegate/workspace, between the host's check and its write
)

func (s *Server) hostAgent() HostAgentRule {
	if s.HostAgent == nil {
		return HostAgentRule{}
	}
	return s.HostAgent()
}

// agentLocks is the `locks` of GET /delegate/state and GET/PUT
// /delegate/workspace: the settings the host's file sets, nil when none.
func (s *Server) agentLocks() map[string]any {
	h := s.hostAgent()
	if !h.Set {
		return nil
	}
	lock := map[string]any{"locked": true, "reason": hostLockReason}
	out := map[string]any{"enabled": lock}
	if h.Who != "" {
		out["who"] = lock
	}
	return out
}

// hostRefuses answers a PUT /delegate/workspace that would change what the
// host sets ("" when it changes nothing of the host's).
func (s *Server) hostRefuses(enabled *bool, who *string) string {
	h := s.hostAgent()
	switch {
	case !h.Set:
		return ""
	case enabled != nil:
		return "workspace agents are " + map[bool]string{true: "on", false: "off"}[h.Enabled] + " as set by your host (the deployment's file, XBIN_LLM_CONFIG): it isn't changed here"
	case who != nil && h.Who != "":
		return "who gets a workspace agent is set by your host (" + h.Who + ", in the deployment's file): it isn't changed here"
	}
	return ""
}

// hostAgentHold takes the lock ApplyHostAgent takes, for PUT
// /delegate/workspace: the host's check and the admin's write as one, so a
// reload of the host's file lands before both or after both. → unlock.
func (s *Server) hostAgentHold() func() {
	hostAgentMu.Lock()
	return hostAgentMu.Unlock
}

// ApplyHostAgent puts the host's rule in force; it changes nothing when the
// rule already is (so the `users` event its own writes cause ends there).
// The rule is read under the lock: an apply that began before the file
// changed never writes the old rule over the new one.
func (s *Server) ApplyHostAgent() {
	if s.Delegation == nil {
		return
	}
	if hostAgentLocking != nil {
		hostAgentLocking()
	}
	hostAgentMu.Lock()
	defer hostAgentMu.Unlock()
	h := s.hostAgent()
	if !h.Set {
		return
	}
	if ws := s.Delegation.Workspace(); ws.Enabled != h.Enabled || ws.At.IsZero() {
		nw, err := s.Delegation.SetWorkspace(h.Enabled, "host")
		if err != nil {
			slog.Error("workspace agents: the host's switch can't be written", "err", err)
			return
		}
		slog.Info("workspace agents: the host's file sets the switch", "enabled", nw.Enabled)
		if !nw.Enabled {
			s.CutDelegateStreams("")
		}
		for _, u := range s.Delegation.People() { // as PUT /delegate/workspace tells them
			s.delegateEvent(u, "state", "")
			if !nw.Enabled {
				s.delegationStopped(u, false)
			}
		}
	}
	if h.Who == "" || !h.Enabled || s.Auth == nil || s.Auth.Users == nil {
		return
	}
	holder := s.agentHolder()
	if holder == "" {
		return
	}
	st := s.Auth.Users
	all, _ := st.SealedGrants(holder)
	changed := []string{}
	var err error
	switch h.Who {
	case "everyone":
		if !all {
			err = st.SetSealedEveryone(holder, true)
			changed = append(changed, "")
		}
	case "admins":
		if all {
			err = st.SetSealedEveryone(holder, false)
			changed = append(changed, "")
		}
		for _, u := range st.List() {
			if err != nil || !u.IsAdmin() || u.Disabled || u.Tiles[holder] == "read" {
				continue
			}
			if err = st.SetUserTile(u.ID, holder, "read"); err == nil {
				changed = append(changed, u.ID)
			}
		}
	}
	if err != nil {
		slog.Error("workspace agents: who the host gives one to can't be written", "who", h.Who, "err", err)
	}
	if len(changed) == 0 {
		return
	}
	slog.Info("workspace agents: the host's file gives them", "who", h.Who, "changed", len(changed))
	if s.PeopleChanged != nil {
		s.PeopleChanged() // access moved (agentwho.go)
	}
	for _, u := range changed {
		if u != "" {
			s.delegateEvent(u, "state", "") // their chrome shows the agent now, not at its next read
		}
	}
}

// hostAgentWrite is PUT /delegate/workspace's refusal of a write the host's
// file owns; true when it wrote the 409.
func (s *Server) hostAgentWrite(w http.ResponseWriter, enabled *bool, who *string) bool {
	if msg := s.hostRefuses(enabled, who); msg != "" {
		WriteError(w, http.StatusConflict, msg, "/docs/workspace-agent.md#set-by-your-host")
		return true
	}
	if hostAgentChecked != nil {
		hostAgentChecked()
	}
	return false
}
