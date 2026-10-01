package server

// The workspace settings (D175, D180): the workspace-wide switches an admin
// sets from the admin console's workspace → settings tab or `bx settings`,
// held by internal/wssettings in data/workspace-settings.json, grouped by
// topic — terminals (baseAutoUpdate) and partitioned tiles
// (partitionConsent, credentialResetConfirm; PD-55).
//
//   - GET /workspace-settings: any signed-in principal reads the terminals'
//     settings; the partitioned tiles' switches only admins and people
//     (their session or device, or a terminal or agent session they drive)
//     — other tile code doesn't see their keys (PD-55).
//   - PUT /workspace-settings: admin; each present key replaces its
//     setting; audited with each setting's old→new; publishes
//     `workspace-settings` (and `policies` when a partitioned tiles' switch
//     is set, for clients older than D180).
//   - GET/PUT /workspace-policies: the partitioned tiles' switches as
//     v0.3.66 served them, before they joined the settings — an alias over
//     the same store, kept for older admin consoles, shells and bx.

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/wssettings"
)

func (s *Server) registerWorkspaceSettingsAPI() {
	s.RegisterAPI("GET /workspace-settings", s.apiWorkspaceSettingsGet)
	s.RegisterAPI("PUT /workspace-settings", s.apiWorkspaceSettingsPut)
	s.RegisterAPI("GET /workspace-policies", s.apiWorkspacePoliciesGet) // the alias (D180)
	s.RegisterAPI("PUT /workspace-policies", s.apiWorkspacePoliciesPut)
}

// settingsAdmin: the admin writes (the admin tile through its xbin:admin
// grant included).
func (s *Server) settingsAdmin(r *http.Request) bool {
	return s.admin(r) || auth.PrincipalOf(r).IsAdmin()
}

// ReadsPartitionSettings: the partitioned tiles' switches are for admins
// and a person through their own session or device, or a terminal or agent
// session they drive (both carry a terminal token: Via "terminal", the
// person in UserID) — `bx settings` runs in one. Other tile principals —
// frames, instances, cron and bus deliveries — don't read them (PD-55).
func ReadsPartitionSettings(p auth.Principal, admin bool) bool {
	return admin || p.UserID != "" && (p.Component == "" || p.Via == "terminal")
}

func (s *Server) readsPartitionSettings(r *http.Request) bool {
	return ReadsPartitionSettings(auth.PrincipalOf(r), s.settingsAdmin(r))
}

// settingsView is the settings as they apply: each key the caller may read,
// its value (a setting that can't be read: its fail-safe value), and why
// one can't be read — errors: {key: reason} (people get a generic reason;
// the detail is the admins'), and error, base auto-update's own, as D175
// gave it to every reader.
func settingsView(cur wssettings.Settings, probs wssettings.Problems, partitions, admin bool) map[string]any {
	v := map[string]any{}
	errs := map[string]string{}
	for _, st := range wssettings.All() {
		if st.Group == wssettings.GroupPartitions && !partitions {
			continue
		}
		v[st.Key] = st.Of(cur)
		if why, bad := probs[st.Key]; bad {
			if !admin {
				why = "can't be read; an admin must fix data/workspace-settings.json"
			}
			errs[st.Key] = why
		}
	}
	if why, bad := probs[wssettings.KeyBaseAutoUpdate]; bad {
		v["error"] = why
	}
	if len(errs) > 0 {
		v["errors"] = errs
	}
	return v
}

// apiWorkspaceSettingsGet: any signed-in principal (a terminal window learns
// the one it needs from /ws/term/env); the partitioned tiles' switches for
// people and admins.
func (s *Server) apiWorkspaceSettingsGet(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		apiErr(w, http.StatusNotImplemented, "this xbind keeps no workspace settings")
		return
	}
	cur, probs := s.Settings.Load()
	WriteJSON(w, http.StatusOK, settingsView(cur, probs, s.readsPartitionSettings(r), s.settingsAdmin(r)))
}

// apiWorkspaceSettingsPut: admin. {baseAutoUpdate?, partitionConsent?,
// credentialResetConfirm?: bool} — each present key replaces its setting,
// an absent one is left alone, an unknown one is refused; the file's other
// keys are kept. → the full view.
func (s *Server) apiWorkspaceSettingsPut(w http.ResponseWriter, r *http.Request) {
	if !s.settingsAdmin(r) {
		apiErr(w, http.StatusForbidden, "admin only — needs the xbin:admin capability (docs/auth.md)")
		return
	}
	if s.Settings == nil {
		apiErr(w, http.StatusNotImplemented, "this xbind keeps no workspace settings")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var p wssettings.Patch
	if err := DecodeJSON(r, &p); err != nil || p.Empty() {
		apiErr(w, http.StatusBadRequest, "need {baseAutoUpdate?, partitionConsent?, credentialResetConfirm?: bool} with at least one key")
		return
	}
	cur, ok := s.applySettings(w, r, p)
	if ok {
		WriteJSON(w, http.StatusOK, settingsView(cur, nil, true, true))
	}
}

// applySettings writes p, audits what it changed and publishes it; false
// when it answered the error.
func (s *Server) applySettings(w http.ResponseWriter, r *http.Request, p wssettings.Patch) (wssettings.Settings, bool) {
	old, cur, err := s.Settings.Apply(p)
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return cur, false
	}
	// The generic audit line names who and the status; this one what changed.
	pr := auth.PrincipalOf(r)
	args := []any{"who", pr.From(), "method", r.Method, "path", r.URL.Path, "status", http.StatusOK}
	if pr.Component != "" && pr.UserID != "" {
		args = append(args, "user", pr.UserID)
	}
	partitions := false
	for _, st := range wssettings.All() {
		args = append(args, st.Key, fmt.Sprintf("%t→%t", st.Of(old), st.Of(cur)))
		partitions = partitions || (st.Group == wssettings.GroupPartitions && st.In(p) != nil)
	}
	slog.Info("audit", args...)
	if s.Hub != nil {
		// Every socket hears it (tile frames too), so it carries what every
		// signed-in principal may read, and which keys the write set; a
		// client re-reads GET for the rest. Open terminal windows re-read
		// /ws/term/env on it.
		ev := settingsView(cur, nil, false, false)
		ev["changed"] = p.Keys()
		s.Hub.Publish(events.Event{Type: "workspace-settings", Data: ev})
		if partitions { // what clients older than D180 follow (PD-55)
			s.Hub.Publish(events.Event{Type: "policies"})
		}
	}
	return cur, true
}

// policiesView is the alias's shape: v0.3.66's GET /workspace-policies.
func policiesView(cur wssettings.Settings) map[string]any {
	return map[string]any{
		"schema":                             wssettings.PoliciesSchema,
		wssettings.KeyPartitionConsent:       cur.PartitionConsent,
		wssettings.KeyCredentialResetConfirm: cur.CredentialResetConfirm,
	}
}

// apiWorkspacePoliciesGet: the alias. People and admins; other tile
// principals 403. While a partitioned tiles' switch can't be read: 500 (the
// reason for admins).
func (s *Server) apiWorkspacePoliciesGet(w http.ResponseWriter, r *http.Request) {
	if !s.readsPartitionSettings(r) {
		WriteError(w, http.StatusForbidden, "the workspace policies are for people and admins, not tile code", "/docs/protocol.md")
		return
	}
	if s.Settings == nil {
		apiErr(w, http.StatusNotImplemented, "this xbind keeps no workspace settings")
		return
	}
	cur, probs := s.Settings.Load()
	if err := probs.Of(wssettings.Keys(wssettings.GroupPartitions)...); err != nil {
		msg := "the workspace policies can't be read; an admin must fix data/workspace-settings.json"
		if s.settingsAdmin(r) {
			msg = err.Error() + " — fix or remove the file by hand"
		}
		WriteError(w, http.StatusInternalServerError, msg, "/docs/protocol.md")
		return
	}
	WriteJSON(w, http.StatusOK, policiesView(cur))
}

// policiesPatch is the alias's PUT body: the partitioned tiles' switches
// only (baseAutoUpdate is 400 here, as it was).
type policiesPatch struct {
	PartitionConsent       *bool `json:"partitionConsent"`
	CredentialResetConfirm *bool `json:"credentialResetConfirm"`
}

// apiWorkspacePoliciesPut: the alias. Admin; {partitionConsent?,
// credentialResetConfirm?} → the alias's view.
func (s *Server) apiWorkspacePoliciesPut(w http.ResponseWriter, r *http.Request) {
	if !s.settingsAdmin(r) {
		WriteError(w, http.StatusForbidden, "admin only — needs the xbin:admin capability", "/docs/auth.md")
		return
	}
	if s.Settings == nil {
		apiErr(w, http.StatusNotImplemented, "this xbind keeps no workspace settings")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var pp policiesPatch
	if err := DecodeJSON(r, &pp); err != nil || pp == (policiesPatch{}) {
		WriteError(w, http.StatusBadRequest, "need {partitionConsent?: bool, credentialResetConfirm?: bool} with at least one key", "/docs/protocol.md")
		return
	}
	cur, ok := s.applySettings(w, r, wssettings.Patch{PartitionConsent: pp.PartitionConsent, CredentialResetConfirm: pp.CredentialResetConfirm})
	if ok {
		WriteJSON(w, http.StatusOK, policiesView(cur))
	}
}
