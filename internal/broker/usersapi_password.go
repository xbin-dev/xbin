package broker

import (
	"fmt"
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
)

// Account passwords: the floor for passwords set through the API, and a
// person changing their own.

// minPasswordLen is the floor for account passwords set through the API. It's
// enforced here (not in users.Store) so the dev-seeded admin and unit tests,
// which set passwords through the store directly, aren't subject to UX policy.
const minPasswordLen = 8

// weakPassword returns a user-facing reason a password is rejected, or "".
func weakPassword(pw string) string {
	if len([]rune(pw)) < minPasswordLen {
		return fmt.Sprintf("password too short (min %d characters)", minPasswordLen)
	}
	return ""
}

// apiAccountPassword is the self-service password change (D38): a signed-in
// user rotates their OWN credential after proving the current one. Not
// admin-gated, not usable by elements. removeDevices:true also removes their
// app devices but the calling one (devicesapi.go).
func (b *Broker) apiAccountPassword(srv *server.Server, w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	p := auth.PrincipalOf(r)
	if p.Component != "" || p.User == nil {
		server.WriteError(w, http.StatusForbidden, "password change is for signed-in users (the bootstrap token has no password)")
		return
	}
	var body struct {
		Current, New  string
		RemoveDevices bool
	}
	if err := server.DecodeJSON(r, &body); err != nil || body.New == "" {
		server.WriteError(w, http.StatusBadRequest, "need {current, new}")
		return
	}
	if err := st.ChangePassword(p.User.ID, body.Current, body.New); err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	out := map[string]any{"ok": true}
	if body.RemoveDevices {
		out["devicesRemoved"], _ = b.removeUserDevices(srv, st, p.User.ID, p.DeviceID)
	}
	server.WriteJSON(w, http.StatusOK, out)
}
