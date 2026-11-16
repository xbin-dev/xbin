package server

// agenthost.go — workspace agents as the host sets them (D237;
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
//   - who "everyone": the workspace defaults name the agent (added if not);
//   - who "admins": every admin account with no agent and no entry of its
//     own gets one — at boot (the key first appearing), when the file
//     changes, and whenever people change (an admin made or made an admin:
//     a `users` event).
//
// What the host wrote is recorded (hostAgentRecord), and exactly that is
// taken back when the rule stops saying it: an entry it gave when its
// person stops being an admin or the key stops saying "admins", the
// defaults' entry it added when the key stops saying "everyone", and —
// when the key goes — the switch returns to the workspace's own setting
// from before the host first wrote it (off, if it had never been set). An
// entry a person or an admin made is never removed, lowered or
// overwritten; one the host gave that someone changed since is theirs.
//
// The host's file is looked at by the connector at most every 2 s, on its
// own reads; ApplyHostAgent runs when it noticed a change (llm.Options.
// HostChanged), at boot, and on every `users` event.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/users"
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
// /delegate/workspace, and puts the host's rule in force under it first:
// the host's check and the admin's write as one, so a reload of the host's
// file lands before both or after both — and a key the host removed is
// taken back before the admin writes, never over what they write. → unlock.
func (s *Server) hostAgentHold() func() {
	hostAgentMu.Lock()
	if s.Delegation != nil {
		s.applyHostAgentLocked()
	}
	return hostAgentMu.Unlock
}

// ApplyHostAgent puts the host's rule in force, and takes back what an
// earlier rule wrote that this one doesn't say; it changes nothing when
// all of it already is (so the `users` event its own writes cause ends
// there). The rule is read under the lock: an apply that began before the
// file changed never writes the old rule over the new one.
func (s *Server) ApplyHostAgent() {
	if s.Delegation == nil {
		return
	}
	if hostAgentLocking != nil {
		hostAgentLocking()
	}
	hostAgentMu.Lock()
	defer hostAgentMu.Unlock()
	s.applyHostAgentLocked()
}

// hostAgentRecord is what the host's rule wrote, kept so that exactly that
// is taken back when the rule stops saying it, and nothing else ever is
// (HostAgentFile; in memory alone without one):
type hostAgentRecord struct {
	// Given: the people the host gave an entry under "admins" — taken back
	// when they stop being admins or the key stops saying "admins", unless
	// someone changed the entry since (it is theirs then)
	Given []string `json:"given,omitempty"`
	// Everyone: the host added the defaults' everyone entry
	Everyone bool `json:"everyone,omitempty"`
	// Switch: the workspace's own switch from before the host first wrote it
	// — what it returns to when the key goes
	Switch *hostAgentSwitch `json:"switch,omitempty"`
}

type hostAgentSwitch struct {
	Enabled bool      `json:"enabled"`
	By      string    `json:"by,omitempty"`
	At      time.Time `json:"at,omitempty"`
}

// hostRecord loads the record (caller holds hostAgentMu).
func (s *Server) hostRecord() *hostAgentRecord {
	if s.hostRec != nil {
		return s.hostRec
	}
	s.hostRec = &hostAgentRecord{}
	if s.HostAgentFile != "" {
		if b, err := os.ReadFile(s.HostAgentFile); err == nil {
			if err := json.Unmarshal(b, s.hostRec); err != nil {
				slog.Error("workspace agents: what the host's rule wrote can't be read; it is taken as nothing", "file", s.HostAgentFile, "err", err)
				s.hostRec = &hostAgentRecord{}
			}
		}
	}
	return s.hostRec
}

func (s *Server) saveHostRecord(rec *hostAgentRecord) {
	if s.HostAgentFile == "" {
		return
	}
	b, _ := json.MarshalIndent(rec, "", " ")
	if err := os.MkdirAll(filepath.Dir(s.HostAgentFile), 0o755); err == nil {
		err = fsutil.WriteFileAtomic(s.HostAgentFile, append(b, '\n'), 0o600)
		if err == nil {
			return
		}
		slog.Error("workspace agents: what the host's rule wrote can't be saved", "file", s.HostAgentFile, "err", err)
	}
}

// setSwitch writes the workspace's switch for the host and tells everyone,
// as PUT /delegate/workspace does.
func (s *Server) setSwitch(on bool) bool {
	nw, err := s.Delegation.SetWorkspace(on, "host")
	if err != nil {
		slog.Error("workspace agents: the host's switch can't be written", "err", err)
		return false
	}
	slog.Info("workspace agents: the host's file sets the switch", "enabled", nw.Enabled)
	if !nw.Enabled {
		s.CutDelegateStreams("")
	}
	for _, u := range s.Delegation.People() {
		s.delegateEvent(u, "state", "")
		if !nw.Enabled {
			s.delegationStopped(u, false)
		}
	}
	return true
}

// applyHostAgentLocked is ApplyHostAgent's work (caller holds hostAgentMu).
func (s *Server) applyHostAgentLocked() {
	h := s.hostAgent()
	rec := s.hostRecord()
	saved := false
	// the switch: the host's value; its own value back when the key goes
	ws := s.Delegation.Workspace()
	switch {
	case h.Set && (ws.Enabled != h.Enabled || ws.At.IsZero()):
		if rec.Switch == nil {
			rec.Switch, saved = &hostAgentSwitch{Enabled: ws.Enabled, By: ws.By, At: ws.At}, true
		}
		s.setSwitch(h.Enabled)
	case !h.Set && rec.Switch != nil:
		own := rec.Switch
		rec.Switch, saved = nil, true
		if ws.Enabled != own.Enabled {
			s.setSwitch(own.Enabled) // (never set before the host: off)
		}
	}
	changed := s.applyHostWho(h, rec)
	if saved || changed != nil {
		s.saveHostRecord(rec)
	}
	if len(changed) == 0 {
		return
	}
	slog.Info("workspace agents: the host's file gives or takes back", "who", h.Who, "changed", len(changed))
	if s.PeopleChanged != nil {
		s.PeopleChanged() // access moved (agentwho.go)
	}
	for _, u := range changed {
		if u != "" {
			s.delegateEvent(u, "state", "") // their chrome follows at once, not at its next read
		}
	}
}

// applyHostWho gives what "everyone" or "admins" says and takes back what
// the host gave that the rule doesn't say now — only ever entries the
// record names, and never one someone changed since. → the people whose
// access moved ("" the defaults); nil when the record didn't change either.
func (s *Server) applyHostWho(h HostAgentRule, rec *hostAgentRecord) []string {
	holder := s.agentHolder()
	if holder == "" || s.Auth == nil || s.Auth.Users == nil {
		return nil
	}
	st := s.Auth.Users
	on := h.Set && h.Enabled
	var changed []string
	touched := false
	note := func(id string, err error) {
		touched = true
		if err != nil {
			slog.Error("workspace agents: who the host gives one to can't be written", "who", h.Who, "err", err)
			return
		}
		changed = append(changed, id)
	}
	// the defaults' everyone entry: added for "everyone", taken back after
	all, _ := st.SealedGrants(holder)
	switch {
	case on && h.Who == "everyone" && !all:
		rec.Everyone = true
		note("", st.SetSealedEveryone(holder, true))
	case !(on && h.Who == "everyone") && rec.Everyone:
		rec.Everyone, touched = false, true
		if all {
			note("", st.SetSealedEveryone(holder, false))
		}
	}
	// "admins": an entry for each admin with no agent and no entry of their own
	admins := on && h.Who == "admins"
	people := map[string]users.User{}
	for _, u := range st.List() {
		people[u.ID] = u
	}
	given := make([]string, 0, len(rec.Given))
	for _, id := range rec.Given {
		u, ok := people[id]
		switch {
		case !ok || u.Tiles[holder] != "read": // gone, or someone changed it since: not the host's any more
			touched = true
		case admins && u.IsAdmin():
			given = append(given, id)
		default: // no admin now, or the key stopped saying admins
			note(id, st.SetUserTile(id, holder, ""))
		}
	}
	if admins {
		for _, u := range people {
			if !u.IsAdmin() || u.Disabled || slices.Contains(given, u.ID) {
				continue
			}
			if _, own := u.Tiles[holder]; own || st.Given(u.ID, holder) {
				continue // an entry of their own (whatever its level), or one through the defaults or an org: left as it is
			}
			err := st.SetUserTile(u.ID, holder, "read")
			if err == nil {
				given = append(given, u.ID)
			}
			note(u.ID, err)
		}
	}
	sort.Strings(given)
	rec.Given = given
	if !touched {
		return nil
	}
	if changed == nil {
		changed = []string{}
	}
	return changed
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
