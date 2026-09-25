package broker

import (
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// Workspace defaults (D27 + D52 + D88, ws-admin). defaultTiles: the live
// visibility baseline every user gets. newUsers: the seed copied onto every
// new account (tiles, terminal flags, org memberships, the personal-plane
// switches and sets; the deprecated canCreate patterns ride along inert,
// D82) — what SSO JIT provisioning lands with. tileCreation: any |
// org-only. personalDefaults: the live personal plane every user gets on
// top of their own sets (D88).

func (b *Broker) defaultsView(st *users.Store) map[string]any {
	return map[string]any{
		"defaultTiles":     st.DefaultTiles(),
		"newUsers":         st.NewUserDefaults(),
		"tileCreation":     st.TileCreation(),
		"personalDefaults": st.PersonalDefaults(),
	}
}

func (b *Broker) apiDefaultsGet(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	server.WriteJSON(w, http.StatusOK, b.defaultsView(st))
}

// apiDefaultsPut replaces whichever of the four settings are present
// (each is a whole-value replace, never a merge — an absent key leaves
// that setting alone, so the admin tile's per-card saves don't clobber
// each other).
func (b *Broker) apiDefaultsPut(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body struct {
		DefaultTiles map[string]string       `json:"defaultTiles"`
		NewUsers     *users.NewUserDefaults  `json:"newUsers"`
		TileCreation *string                 `json:"tileCreation"`
		Personal     *users.PersonalDefaults `json:"personalDefaults"`
	}
	if err := server.DecodeJSON(r, &body); err != nil || (body.DefaultTiles == nil && body.NewUsers == nil && body.TileCreation == nil && body.Personal == nil) {
		server.WriteError(w, http.StatusBadRequest, "need {defaultTiles?: {pattern: level}, newUsers?: {tiles, termApi, termNet, orgs:[{org, level, create}], noPersonalTiles, noTerminal, sets, netSets}, tileCreation?: any|org-only, personalDefaults?: {sets, netSets}}")
		return
	}
	if body.DefaultTiles != nil {
		if err := st.SetDefaultTiles(body.DefaultTiles); err != nil {
			server.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if body.NewUsers != nil {
		if err := st.SetNewUserDefaults(*body.NewUsers); err != nil {
			server.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if body.TileCreation != nil {
		if err := st.SetTileCreation(*body.TileCreation); err != nil {
			server.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if body.Personal != nil {
		before := st.PersonalDefaults()
		if err := st.SetPersonalDefaults(*body.Personal); err != nil {
			server.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.Join(before.NetSets, ",") != strings.Join(st.PersonalDefaults().NetSets, ",") {
			b.netSetsChanged("", nil, "personal-defaults") // every personal tile's default egress follows
		}
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, b.defaultsView(st))
}
