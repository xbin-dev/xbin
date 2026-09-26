package broker

import (
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// Devices API (docs/auth.md §Device login): a signed-in user's enrolled app
// devices, and revocation — their own, or anyone's for admin / xbin:users.
// Enrollment and the login routes themselves are the server's
// (internal/server/devicelogin.go). The stored public key never leaves here.

func (b *Broker) registerDevices(srv *server.Server) {
	srv.RegisterAPI("GET /devices", b.apiDevicesMine)
	srv.RegisterAPI("DELETE /devices/{id}", func(w http.ResponseWriter, r *http.Request) { b.apiDeviceRevoke(srv, w, r) })
	srv.RegisterAPI("GET /users/{id}/devices", b.apiUserDevices)
}

// deviceView is a device as the APIs show it.
type deviceView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform,omitempty"`
	Origin   string `json:"origin"`
	Created  int64  `json:"created"`
	LastUsed int64  `json:"lastUsed,omitempty"`
	LastIP   string `json:"lastIP,omitempty"`
	Current  bool   `json:"current,omitempty"` // the device behind the calling session
}

func deviceViews(ds []users.Device, current string) []deviceView {
	out := make([]deviceView, 0, len(ds))
	for _, d := range ds {
		out = append(out, deviceView{ID: d.ID, Name: d.Name, Platform: d.Platform, Origin: d.Origin,
			Created: d.Created, LastUsed: d.LastUsed, LastIP: d.LastIP, Current: current != "" && d.ID == current})
	}
	return out
}

// apiDevicesMine — GET /devices: the caller's own devices (a human user —
// browser or app session; tiles and the bootstrap token have none).
func (b *Broker) apiDevicesMine(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if p.Component != "" || p.User == nil {
		server.WriteError(w, http.StatusForbidden, "devices belong to user accounts — sign in with yours", "/docs/auth.md")
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"devices": deviceViews(st.Devices(p.User.ID), p.DeviceID)})
}

// apiUserDevices — GET /users/{id}/devices: admin / xbin:users.
func (b *Broker) apiUserDevices(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	u, ok := st.Get(r.PathValue("id"))
	if !ok {
		server.WriteError(w, http.StatusNotFound, "no such user")
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"devices": deviceViews(st.Devices(u.ID), "")})
}

// apiDeviceRevoke — DELETE /devices/{id}: the device's own user, or admin /
// xbin:users. Removes the key and ends every session it opened — and with
// them the frame tokens those sessions minted — and its push registration
// (OnDeviceRemoved).
func (b *Broker) apiDeviceRevoke(srv *server.Server, w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	id := r.PathValue("id")
	owner, _, ok := st.FindDevice(id)
	p := auth.PrincipalOf(r)
	own := ok && p.Component == "" && p.User != nil && p.User.ID == owner
	if !ok || (!own && !b.canManageUsers(p)) {
		server.WriteError(w, http.StatusNotFound, "no such device", "/docs/auth.md")
		return
	}
	if _, _, err := st.RemoveDevice(id); err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	n := 0
	if srv != nil && srv.Auth != nil {
		n = srv.Auth.DropDeviceSessions(id)
	}
	b.deviceRemoved(owner, id)
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "user": owner, "dropped": n})
}

// removeUserDevices removes a user's devices — all of them, or all but keep
// (the caller's own) — and ends every session each one opened. Returns how
// many went.
func (b *Broker) removeUserDevices(srv *server.Server, st *users.Store, uid, keep string) (int, error) {
	ids, err := st.RemoveDevices(uid, keep)
	for _, id := range ids {
		if srv != nil && srv.Auth != nil {
			srv.Auth.DropDeviceSessions(id)
		}
		b.deviceRemoved(uid, id)
	}
	if len(ids) > 0 {
		b.usersEvent()
	}
	return len(ids), err
}

// signoutDevices is sign-out-everywhere's device half (DELETE
// /users/{id}/sessions): ?devices=1 removes every enrolled device too —
// otherwise they stay, and devicesLeft says how many can still sign in
// without a password. false: the removal failed (answered).
func (b *Broker) signoutDevices(srv *server.Server, st *users.Store, w http.ResponseWriter, r *http.Request, uid string, out map[string]any) bool {
	if r.URL.Query().Get("devices") == "1" {
		n, err := b.removeUserDevices(srv, st, uid, "")
		if err != nil {
			server.WriteError(w, http.StatusInternalServerError, err.Error())
			return false
		}
		out["devicesRemoved"] = n
	}
	out["devicesLeft"] = len(st.Devices(uid))
	return true
}

// deviceRemoved runs the OnDeviceRemoved hook.
func (b *Broker) deviceRemoved(userID, deviceID string) {
	if b.OnDeviceRemoved != nil {
		b.OnDeviceRemoved(userID, deviceID)
	}
}
