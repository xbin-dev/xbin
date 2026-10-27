package broker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// Device limits (D204, internal/users/devicelimit.go): the workspace
// default and per-person overrides of how many app devices one person may
// have paired.

// deviceLimitBody is the device-limit part of a POST/PATCH /users body:
// maxDevices n (0–32) sets the person's override, null clears it, absent
// keeps it (D88 presence semantics).
type deviceLimitBody struct {
	MaxDevices json.RawMessage `json:"maxDevices"`
}

// parse reads maxDevices: set reports its presence, n is nil for null.
func (db deviceLimitBody) parse() (set bool, n *int, err error) {
	raw := bytes.TrimSpace(db.MaxDevices)
	if len(raw) == 0 {
		return false, nil, nil
	}
	if bytes.Equal(raw, []byte("null")) {
		return true, nil, nil
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil || v < 0 || v > users.MaxDevicesPerUser {
		return false, nil, fmt.Errorf("maxDevices: want 0–%d, or null for the workspace default", users.MaxDevicesPerUser)
	}
	return true, &v, nil
}

// checkAll validates the body's personal-plane and device-limit parts
// before anything is written.
func (ub *userBody) checkAll(st *users.Store) error {
	if err := ub.personalBody.check(st); err != nil {
		return err
	}
	_, _, err := ub.deviceLimitBody.parse()
	return err
}

// applyUserExtras writes the parts of a users request that live outside
// the row Upsert writes: the personal plane, then the device limit.
func (b *Broker) applyUserExtras(srv *server.Server, st *users.Store, u *users.User, ub *userBody, seeded bool) (*users.User, error) {
	u, err := b.applyPersonal(srv, st, u, ub.personalBody, seeded)
	if err != nil {
		return u, err
	}
	set, n, err := ub.deviceLimitBody.parse()
	if err != nil || !set {
		return u, err
	}
	nu, err := st.SetMaxDevices(u.ID, n)
	if err != nil {
		return u, err
	}
	return nu, nil
}

// deviceLimitsView is GET /devices/limits.
func deviceLimitsView(st *users.Store) map[string]any {
	atLimit, overrides := []users.DeviceLimitRow{}, []users.DeviceLimitRow{}
	for _, r := range st.DeviceLimitRows() {
		if r.Count >= r.Limit {
			atLimit = append(atLimit, r)
		}
		if r.Override != nil {
			overrides = append(overrides, r)
		}
	}
	return map[string]any{"default": st.DeviceLimit(), "max": users.MaxDevicesPerUser, "atLimit": atLimit, "overrides": overrides}
}

// apiDeviceLimits — GET /devices/limits: admin / xbin:users.
func (b *Broker) apiDeviceLimits(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	if st := b.usersStore(w); st != nil {
		server.WriteJSON(w, http.StatusOK, deviceLimitsView(st))
	}
}

// apiDeviceLimitsSet — PUT /devices/limits {default}: admin / xbin:users.
func (b *Broker) apiDeviceLimitsSet(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body struct {
		Default *int `json:"default"`
	}
	if err := server.DecodeJSON(r, &body); err != nil || body.Default == nil {
		server.WriteError(w, http.StatusBadRequest, fmt.Sprintf("need {default: 1–%d}", users.MaxDevicesPerUser), "/docs/auth.md")
		return
	}
	if err := st.SetDeviceLimit(*body.Default); err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error(), "/docs/auth.md")
		return
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, deviceLimitsView(st))
}
