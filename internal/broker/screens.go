package broker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// Shared screens (D37). Personal screens live in each user's prefs; this is
// the WORKSPACE layer: a ws-admin-curated default screen every new user
// seeds from (replacing hand-editing root/index.html), and ORG SCREENS —
// layouts owned by an org, appearing as tabs for every member, with an
// `edit` knob choosing who may rearrange them:
//
//	admins  — org admins only (members view read-only)
//	write   — members holding org level ≥ write
//	members — any member
//
// Tiles arrays are the shell's own layout shape — opaque here beyond a size
// cap. Stored in data/screens.json (xbind-owned, atomic rename).

type orgScreen struct {
	ID    string          `json:"id"`
	Org   string          `json:"org"`
	Name  string          `json:"name"`
	Edit  string          `json:"edit"` // admins | write | members
	Tiles json.RawMessage `json:"tiles"`
}

type screensDoc struct {
	Default json.RawMessage `json:"default,omitempty"` // {tiles:[…]} — the seed screen
	Org     []orgScreen     `json:"org,omitempty"`
}

var screensMu sync.Mutex

func (b *Broker) screensPath() string {
	return filepath.Join(b.Reg.Root, "data", "screens.json")
}

func (b *Broker) screensRead() (screensDoc, error) {
	var d screensDoc
	bts, err := os.ReadFile(b.screensPath())
	if os.IsNotExist(err) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	return d, json.Unmarshal(bts, &d)
}

func (b *Broker) screensWrite(d screensDoc) error {
	bts, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	p := b.screensPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, bts, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (b *Broker) registerScreens(srv *server.Server) {
	srv.RegisterAPI("GET /screens", b.apiScreensGet)
	srv.RegisterAPI("PUT /screens/default", b.apiScreensDefaultPut)
	srv.RegisterAPI("PUT /screens/org", b.apiScreensOrgPut)
	srv.RegisterAPI("DELETE /screens/org", b.apiScreensOrgDelete)
}

// screenEditable: may this human rearrange the screen's tiles?
func screenEditable(s orgScreen, m users.OrgMembership) bool {
	if m.Suspended {
		return false
	}
	if m.Admin {
		return true
	}
	switch s.Edit {
	case "members":
		return true
	case "write":
		return m.Level == users.LevelWrite || m.Level == users.LevelTerminal
	}
	return false
}

// GET /screens — the workspace default (everyone) plus the caller's orgs'
// screens, each marked canEdit. ws-admins see every org screen.
func (b *Broker) apiScreensGet(w http.ResponseWriter, r *http.Request) {
	d, err := b.screensRead()
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	p := auth.PrincipalOf(r)
	memb := map[string]users.OrgMembership{}
	if b.Users != nil && p.User != nil {
		for _, m := range b.Users.UserOrgs(p.User.ID) {
			memb[m.ID] = m
		}
	}
	type view struct {
		orgScreen
		CanEdit bool `json:"canEdit"`
	}
	out := []view{}
	for _, s := range d.Org {
		if b.IsAdmin(p) {
			out = append(out, view{s, true})
			continue
		}
		if m, ok := memb[s.Org]; ok && !m.Suspended {
			out = append(out, view{s, screenEditable(s, m)})
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"default": d.Default, "org": out})
}

// PUT /screens/default {tiles} — the ws-admin-curated first screen new users
// seed from (root/index.html's <bx-frame> pins stay the fallback).
func (b *Broker) apiScreensDefaultPut(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	body, ok := readScreenBody(w, r)
	if !ok {
		return
	}
	screensMu.Lock()
	defer screensMu.Unlock()
	d, err := b.screensRead()
	if err == nil {
		d.Default = body
		err = b.screensWrite(d)
	}
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	b.Hub.Publish(events.Event{Type: "users"})
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// readScreenBody size-caps the opaque layout JSON (64K is ~hundreds of tiles).
func readScreenBody(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	var raw json.RawMessage
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := decodeJSON(r, &raw); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be JSON (≤64K)"})
		return nil, false
	}
	return raw, true
}

// PUT /screens/org — create or update an org screen. Org admins (and
// ws-admins) always; other members only per the EXISTING screen's edit knob,
// and then only its tiles — name/edit/org changes and creation are
// admin-plane acts.
func (b *Broker) apiScreensOrgPut(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body struct {
		ID    string          `json:"id"`
		Org   string          `json:"org"`
		Name  string          `json:"name"`
		Edit  string          `json:"edit"`
		Tiles json.RawMessage `json:"tiles"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := decodeJSON(r, &body); err != nil || len(body.Tiles) == 0 {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {id?, org, name?, edit?, tiles} (≤64K)"})
		return
	}
	p := auth.PrincipalOf(r)
	admin := b.IsAdmin(p)
	var m users.OrgMembership
	if !admin {
		if p.Component != "" || p.User == nil {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "org screens are managed by signed-in members"})
			return
		}
		found := false
		for _, om := range st.UserOrgs(p.User.ID) {
			if om.ID == body.Org {
				m, found = om, true
			}
		}
		if !found || m.Suspended {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of org " + body.Org})
			return
		}
	}
	if body.Edit != "" && body.Edit != "admins" && body.Edit != "write" && body.Edit != "members" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "edit must be admins|write|members"})
		return
	}
	if _, ok := st.Org(body.Org); !ok {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such org"})
		return
	}
	screensMu.Lock()
	defer screensMu.Unlock()
	d, err := b.screensRead()
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if body.ID == "" { // create: org admins / ws-admin only
		if !admin && !m.Admin {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "creating org screens needs an org admin"})
			return
		}
		if body.Name == "" {
			body.Name = body.Org
		}
		if body.Edit == "" {
			body.Edit = "admins"
		}
		d.Org = append(d.Org, orgScreen{
			ID: util.RandomToken(8), Org: body.Org, Name: body.Name, Edit: body.Edit, Tiles: body.Tiles,
		})
	} else {
		idx := -1
		for i, s := range d.Org {
			if s.ID == body.ID && s.Org == body.Org {
				idx = i
			}
		}
		if idx < 0 {
			server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such screen"})
			return
		}
		cur := d.Org[idx]
		metaChange := (body.Name != "" && body.Name != cur.Name) ||
			(body.Edit != "" && body.Edit != cur.Edit)
		if !admin && !m.Admin {
			if !screenEditable(cur, m) {
				server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "this screen is read-only for you (edit: " + cur.Edit + ")"})
				return
			}
			if metaChange {
				server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "renaming or changing who may edit needs an org admin"})
				return
			}
		}
		cur.Tiles = body.Tiles
		if admin || m.Admin {
			if body.Name != "" {
				cur.Name = body.Name
			}
			if body.Edit != "" {
				cur.Edit = body.Edit
			}
		}
		d.Org[idx] = cur
	}
	if err := b.screensWrite(d); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	b.Hub.Publish(events.Event{Type: "users"})
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// DELETE /screens/org {id, org} — org admins / ws-admin.
func (b *Broker) apiScreensOrgDelete(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body struct{ ID, Org string }
	if err := decodeJSON(r, &body); err != nil || body.ID == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {id, org}"})
		return
	}
	p := auth.PrincipalOf(r)
	if !b.IsAdmin(p) {
		ok := false
		if p.Component == "" && p.User != nil {
			for _, om := range st.UserOrgs(p.User.ID) {
				if om.ID == body.Org && om.Admin && !om.Suspended {
					ok = true
				}
			}
		}
		if !ok {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "deleting org screens needs an org admin"})
			return
		}
	}
	screensMu.Lock()
	defer screensMu.Unlock()
	d, err := b.screensRead()
	if err == nil {
		out := d.Org[:0]
		for _, s := range d.Org {
			if !(s.ID == body.ID && s.Org == body.Org) {
				out = append(out, s)
			}
		}
		d.Org = out
		err = b.screensWrite(d)
	}
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	b.Hub.Publish(events.Event{Type: "users"})
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
