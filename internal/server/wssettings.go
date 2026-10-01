package server

// The workspace settings an admin sets from the admin console's workspace
// tab that have no store of their own (D174): GET/PUT
// /api/xbin/workspace-settings, held by internal/wssettings in
// data/workspace-settings.json. Today one: baseAutoUpdate — a tile's
// terminal layer built on an older base image moves to the current base at
// its next session start (internal/term, claimLayer).

import (
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/wssettings"
)

func (s *Server) registerWorkspaceSettingsAPI() {
	s.RegisterAPI("GET /workspace-settings", s.apiWorkspaceSettingsGet)
	s.RegisterAPI("PUT /workspace-settings", s.apiWorkspaceSettingsPut)
}

// settingsView is the settings as they apply, with the reason when the
// file can't be read: then base auto-update is off (wssettings'
// BaseAutoUpdate) until it is fixed.
func settingsView(st wssettings.Settings, err error) map[string]any {
	v := map[string]any{"baseAutoUpdate": st.BaseAutoUpdate}
	if err != nil {
		v["baseAutoUpdate"] = false
		v["error"] = err.Error()
	}
	return v
}

// apiWorkspaceSettingsGet: any signed-in principal (nothing in it is
// secret; a terminal window learns the one it needs from /ws/term/env).
func (s *Server) apiWorkspaceSettingsGet(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		apiErr(w, http.StatusNotImplemented, "this xbind keeps no workspace settings")
		return
	}
	WriteJSON(w, http.StatusOK, settingsView(s.Settings.Load()))
}

// apiWorkspaceSettingsPut: admin (the admin tile through its xbin:admin
// grant). {baseAutoUpdate?: bool} — each present key replaces its setting,
// an absent one is left alone, an unknown one is refused; the file's other
// keys are kept. → the full view, which a `workspace-settings` hub event
// carries too. Audited like every admin write.
func (s *Server) apiWorkspaceSettingsPut(w http.ResponseWriter, r *http.Request) {
	if !(s.admin(r) || auth.PrincipalOf(r).IsAdmin()) {
		apiErr(w, http.StatusForbidden, "admin only — needs the xbin:admin capability (docs/auth.md)")
		return
	}
	if s.Settings == nil {
		apiErr(w, http.StatusNotImplemented, "this xbind keeps no workspace settings")
		return
	}
	var p wssettings.Patch
	if err := DecodeJSON(r, &p); err != nil || p.Empty() {
		apiErr(w, http.StatusBadRequest, "need {baseAutoUpdate: bool}")
		return
	}
	st, err := s.Settings.Apply(p)
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	view := settingsView(st, nil)
	if s.Hub != nil { // open terminal windows re-read what their chooser says (GET /ws/term/env)
		s.Hub.Publish(events.Event{Type: "workspace-settings", Data: view})
	}
	WriteJSON(w, http.StatusOK, view)
}
