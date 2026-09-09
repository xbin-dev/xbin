package broker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// Shared screens (D37) and shared sidebar folders (D55). Personal screens
// live in each user's prefs; this is the WORKSPACE layer, stored in
// data/screens.json (xbind-owned, atomic rename):
//
//   - a ws-admin-curated DEFAULT screen every new user seeds from (replacing
//     hand-editing root/index.html);
//   - ORG SCREENS — layouts owned by an org, appearing as tabs for every
//     member, with an `edit` knob choosing who may rearrange them:
//     admins — org admins only (members view read-only)
//     write   — members holding org level ≥ write
//     members — any member
//   - FOLDER SETS — the curated sidebar tree of one owner section, keyed
//     "ws" (workspace-owned tiles, ws-admins curate) or "org:<id>" (that
//     org's tiles, its admins curate); members read them.
//
// Org screens and folder sets carry a REVISION (D55): a tile/folder save
// names the revision it was based on and a stale one is refused with 409
// and the current document, so two people never silently clobber each
// other — the shell edits a local draft and publishes with an explicit
// "save and update for everyone". A tile write without `rev` is still
// accepted as a legacy overwrite (pre-D55 shell copies keep working).
// Rename/knob changes are admin-plane and don't bump the revision, so they
// never conflict with a member's draft. Layout JSON is the shell's own
// shape — opaque here beyond a size cap.

type orgScreen struct {
	ID    string          `json:"id"`
	Org   string          `json:"org"`
	Name  string          `json:"name"`
	Edit  string          `json:"edit"` // admins | write | members
	Tiles json.RawMessage `json:"tiles"`
	// Rev counts tile saves (1-based; legacy rows load as 1). UpdatedBy/At
	// stamp the last tile save — what the shell's "last saved by" shows.
	Rev       int    `json:"rev"`
	UpdatedBy string `json:"updatedBy,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// folderSet is one owner section's curated sidebar tree. Folders is the
// shell's shape ([{id,name,icon?,parent?,items:[tile path]}]); the server
// only checks it is a JSON array. Per-user state (open/closed) never lives
// here.
type folderSet struct {
	Folders   json.RawMessage `json:"folders"`
	Rev       int             `json:"rev"`
	UpdatedBy string          `json:"updatedBy,omitempty"`
	UpdatedAt string          `json:"updatedAt,omitempty"`
}

type screensDoc struct {
	Default json.RawMessage      `json:"default,omitempty"` // {tiles:[…]} — the seed screen
	Org     []orgScreen          `json:"org,omitempty"`
	Folders map[string]folderSet `json:"folders,omitempty"` // "ws" | "org:<id>"
}

const folderScopeWS = "ws"

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
	if err := json.Unmarshal(bts, &d); err != nil {
		return d, err
	}
	for i := range d.Org { // pre-D55 rows: revision 1
		if d.Org[i].Rev == 0 {
			d.Org[i].Rev = 1
		}
	}
	return d, nil
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
	return fsutil.WriteFileAtomic(p, bts, 0o644)
}

func (b *Broker) registerScreens(srv *server.Server) {
	srv.RegisterAPI("GET /screens", b.apiScreensGet)
	srv.RegisterAPI("PUT /screens/default", b.apiScreensDefaultPut)
	srv.RegisterAPI("PUT /screens/org", b.apiScreensOrgPut)
	srv.RegisterAPI("DELETE /screens/org", b.apiScreensOrgDelete)
	srv.RegisterAPI("PUT /screens/folders", b.apiScreensFoldersPut)
}

// stampOf names the saver for UpdatedBy/UpdatedAt: the user id, or "root"
// for the owner token.
func stampOf(p auth.Principal) (by, at string) {
	by = p.UserID
	if by == "" {
		by = "root"
	}
	return by, time.Now().UTC().Format(time.RFC3339)
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

// screenView is an org screen as the API returns it: the row plus whether
// the caller may edit its tiles.
type screenView struct {
	orgScreen
	CanEdit bool `json:"canEdit"`
}

type folderSetView struct {
	folderSet
	CanEdit bool `json:"canEdit"`
}

func folderViewOf(fs folderSet, canEdit bool) folderSetView {
	if len(fs.Folders) == 0 {
		fs.Folders = json.RawMessage("[]")
	}
	return folderSetView{fs, canEdit}
}

// membershipsOf maps org id → the caller's membership (empty for tokens and
// element principals).
func (b *Broker) membershipsOf(p auth.Principal) map[string]users.OrgMembership {
	memb := map[string]users.OrgMembership{}
	if b.Users != nil && p.User != nil {
		for _, m := range b.Users.UserOrgs(p.User.ID) {
			memb[m.ID] = m
		}
	}
	return memb
}

// GET /screens — the workspace default (everyone), the caller's orgs'
// screens (each marked canEdit) and the folder sets the caller may see:
// "ws" always, plus every org they belong to. ws-admins see every org.
func (b *Broker) apiScreensGet(w http.ResponseWriter, r *http.Request) {
	d, err := b.screensRead()
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p := auth.PrincipalOf(r)
	admin := b.IsAdmin(p)
	memb := b.membershipsOf(p)
	out := []screenView{}
	for _, s := range d.Org {
		if admin {
			out = append(out, screenView{s, true})
			continue
		}
		if m, ok := memb[s.Org]; ok && !m.Suspended {
			out = append(out, screenView{s, screenEditable(s, m)})
		}
	}
	folders := map[string]folderSetView{folderScopeWS: folderViewOf(d.Folders[folderScopeWS], admin)}
	if admin && b.Users != nil {
		for _, o := range b.Users.Orgs() {
			folders["org:"+o.ID] = folderViewOf(d.Folders["org:"+o.ID], true)
		}
	}
	for id, m := range memb {
		if m.Suspended {
			continue
		}
		if _, seen := folders["org:"+id]; !seen {
			folders["org:"+id] = folderViewOf(d.Folders["org:"+id], m.Admin)
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"default": d.Default, "org": out, "folders": folders})
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
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	b.Hub.Publish(events.Event{Type: "users"})
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// readScreenBody size-caps the opaque layout JSON (64K is ~hundreds of tiles).
func readScreenBody(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	var raw json.RawMessage
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := server.DecodeJSON(r, &raw); err != nil {
		server.WriteError(w, http.StatusBadRequest, "body must be JSON (≤64K)")
		return nil, false
	}
	return raw, true
}

// rawPresent: a JSON field that was sent and isn't null.
func rawPresent(m json.RawMessage) bool {
	return len(m) > 0 && strings.TrimSpace(string(m)) != "null"
}

func savedBy(by string) string {
	if by == "" {
		return "someone"
	}
	return by
}

// PUT /screens/org — create or update an org screen. Org admins (and
// ws-admins) always; other members only per the EXISTING screen's edit knob,
// and then only its tiles — name/edit changes and creation are admin-plane
// acts. A tile write carries the revision it was based on (D55): a stale
// one is refused with 409 + the current screen unless force:true; no rev at
// all is the legacy overwrite. A body without tiles is a meta-only edit.
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
		Rev   *int            `json:"rev"`
		Force bool            `json:"force"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := server.DecodeJSON(r, &body); err != nil || body.Org == "" {
		server.WriteError(w, http.StatusBadRequest, "need {id?, org, name?, edit?, tiles?, rev?, force?} (≤64K)")
		return
	}
	hasTiles := rawPresent(body.Tiles)
	p := auth.PrincipalOf(r)
	admin := b.IsAdmin(p)
	var m users.OrgMembership
	if !admin {
		if p.Component != "" || p.User == nil {
			server.WriteError(w, http.StatusForbidden, "org screens are managed by signed-in members")
			return
		}
		found := false
		for _, om := range st.UserOrgs(p.User.ID) {
			if om.ID == body.Org {
				m, found = om, true
			}
		}
		if !found || m.Suspended {
			server.WriteError(w, http.StatusForbidden, "not a member of org "+body.Org)
			return
		}
	}
	if body.Edit != "" && body.Edit != "admins" && body.Edit != "write" && body.Edit != "members" {
		server.WriteError(w, http.StatusBadRequest, "edit must be admins|write|members")
		return
	}
	if _, ok := st.Org(body.Org); !ok {
		server.WriteError(w, http.StatusNotFound, "no such org")
		return
	}
	screensMu.Lock()
	defer screensMu.Unlock()
	d, err := b.screensRead()
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	by, at := stampOf(p)
	var cur orgScreen
	if body.ID == "" { // create: org admins / ws-admin only
		if !admin && !m.Admin {
			server.WriteError(w, http.StatusForbidden, "creating org screens needs an org admin")
			return
		}
		if !hasTiles {
			server.WriteError(w, http.StatusBadRequest, "creating a screen needs tiles ([] for an empty one)")
			return
		}
		if body.Name == "" {
			body.Name = body.Org
		}
		if body.Edit == "" {
			body.Edit = "admins"
		}
		cur = orgScreen{
			ID: util.RandomToken(8), Org: body.Org, Name: body.Name, Edit: body.Edit, Tiles: body.Tiles,
			Rev: 1, UpdatedBy: by, UpdatedAt: at,
		}
		d.Org = append(d.Org, cur)
	} else {
		idx := -1
		for i, s := range d.Org {
			if s.ID == body.ID && s.Org == body.Org {
				idx = i
			}
		}
		if idx < 0 {
			server.WriteError(w, http.StatusNotFound, "no such screen")
			return
		}
		cur = d.Org[idx]
		metaChange := (body.Name != "" && body.Name != cur.Name) ||
			(body.Edit != "" && body.Edit != cur.Edit)
		if !hasTiles && !metaChange {
			server.WriteError(w, http.StatusBadRequest, "nothing to change: send tiles and/or name/edit")
			return
		}
		if !admin && !m.Admin {
			if hasTiles && !screenEditable(cur, m) {
				server.WriteError(w, http.StatusForbidden, "this screen is read-only for you (edit: "+cur.Edit+")")
				return
			}
			if metaChange {
				server.WriteError(w, http.StatusForbidden, "renaming or changing who may edit needs an org admin")
				return
			}
		}
		if hasTiles {
			if body.Rev != nil && *body.Rev != cur.Rev && !body.Force {
				canEdit := admin || screenEditable(cur, m)
				server.WriteJSON(w, http.StatusConflict, map[string]any{
					"error": fmt.Sprintf("stale revision: the screen is at rev %d (saved by %s) and you sent %d — reload it or resend with force:true",
						cur.Rev, savedBy(cur.UpdatedBy), *body.Rev),
					"rev":    cur.Rev,
					"screen": screenView{cur, canEdit},
				})
				return
			}
			cur.Tiles = body.Tiles
			cur.Rev++
			cur.UpdatedBy, cur.UpdatedAt = by, at
		}
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
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	b.Hub.Publish(events.Event{Type: "users"})
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"ok": true, "id": cur.ID, "rev": cur.Rev, "updatedBy": cur.UpdatedBy, "updatedAt": cur.UpdatedAt,
	})
}

// DELETE /screens/org {id, org} — org admins / ws-admin.
func (b *Broker) apiScreensOrgDelete(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body struct{ ID, Org string }
	if err := server.DecodeJSON(r, &body); err != nil || body.ID == "" {
		server.WriteError(w, http.StatusBadRequest, "need {id, org}")
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
			server.WriteError(w, http.StatusForbidden, "deleting org screens needs an org admin")
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
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	b.Hub.Publish(events.Event{Type: "users"})
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// PUT /screens/folders {scope, folders, rev, force?} — replace one owner
// section's curated sidebar tree (D55). "ws" is a ws-admin act; "org:<id>"
// needs that org's admin (ws-admins too). The revision rule is the org
// screens' one; the first save of a scope sends rev 0.
func (b *Broker) apiScreensFoldersPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scope   string          `json:"scope"`
		Folders json.RawMessage `json:"folders"`
		Rev     *int            `json:"rev"`
		Force   bool            `json:"force"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	bad := func() {
		server.WriteError(w, http.StatusBadRequest, "need {scope:'ws'|'org:<id>', folders:[…], rev} (≤64K)")
	}
	if err := server.DecodeJSON(r, &body); err != nil || body.Rev == nil || !rawPresent(body.Folders) {
		bad()
		return
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(body.Folders, &arr); err != nil {
		bad()
		return
	}
	p := auth.PrincipalOf(r)
	switch {
	case body.Scope == folderScopeWS:
		if !b.requireAdmin(w, r) {
			return
		}
	case strings.HasPrefix(body.Scope, "org:"):
		st := b.usersStore(w)
		if st == nil {
			return
		}
		org := strings.TrimPrefix(body.Scope, "org:")
		if _, ok := st.Org(org); !ok {
			server.WriteError(w, http.StatusNotFound, "no such org")
			return
		}
		if !b.IsAdmin(p) {
			ok := false
			if p.Component == "" && p.User != nil {
				for _, om := range st.UserOrgs(p.User.ID) {
					if om.ID == org && om.Admin && !om.Suspended {
						ok = true
					}
				}
			}
			if !ok {
				server.WriteError(w, http.StatusForbidden, "curating "+body.Scope+" folders needs an org admin")
				return
			}
		}
	default:
		bad()
		return
	}
	screensMu.Lock()
	defer screensMu.Unlock()
	d, err := b.screensRead()
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cur := d.Folders[body.Scope]
	if *body.Rev != cur.Rev && !body.Force {
		server.WriteJSON(w, http.StatusConflict, map[string]any{
			"error": fmt.Sprintf("stale revision: folders for %s are at rev %d (saved by %s) and you sent %d — reload or resend with force:true",
				body.Scope, cur.Rev, savedBy(cur.UpdatedBy), *body.Rev),
			"rev":     cur.Rev,
			"folders": folderViewOf(cur, true),
		})
		return
	}
	by, at := stampOf(p)
	next := folderSet{Folders: body.Folders, Rev: cur.Rev + 1, UpdatedBy: by, UpdatedAt: at}
	if d.Folders == nil {
		d.Folders = map[string]folderSet{}
	}
	d.Folders[body.Scope] = next
	if err := b.screensWrite(d); err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	b.Hub.Publish(events.Event{Type: "users"})
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"ok": true, "rev": next.Rev, "updatedBy": next.UpdatedBy, "updatedAt": next.UpdatedAt,
	})
}
