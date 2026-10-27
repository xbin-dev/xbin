package users

import (
	"errors"
	"fmt"
	"sort"
)

// Device limits (D204, plans/app-tunnel.md): how many app devices one
// person may have paired. The workspace default (deviceLimit in users.json,
// 1 when unset) applies to everyone without an override (User.MaxDevices).
// Enforced when a device is added — never by removing one: devices already
// past a lowered limit stay (grandfathered) until their owner removes them.
// MaxDevicesPerUser caps both.

// DefaultDeviceLimit is the workspace default when none is set.
const DefaultDeviceLimit = 1

// DeviceLimitError is an enrollment refused at the limit.
type DeviceLimitError struct {
	Count, Limit int
}

func (e *DeviceLimitError) Error() string {
	noun := "devices"
	if e.Count == 1 {
		noun = "device"
	}
	return fmt.Sprintf("you have %d %s; this workspace allows %d per person — remove one first (an admin can raise your limit)", e.Count, noun, e.Limit)
}

// ErrTunnelKeyTaken is a device tunnel key already on another device.
var ErrTunnelKeyTaken = errors.New("that tunnel key belongs to another device — pair again")

// DeviceLimit is the workspace default.
func (s *Store) DeviceLimit() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deviceLimitLocked()
}

func (s *Store) deviceLimitLocked() int {
	if s.deviceLimit <= 0 {
		return DefaultDeviceLimit
	}
	return min(s.deviceLimit, MaxDevicesPerUser)
}

// SetDeviceLimit sets the workspace default, 1–MaxDevicesPerUser.
func (s *Store) SetDeviceLimit(n int) error {
	if n < 1 || n > MaxDevicesPerUser {
		return fmt.Errorf("device limit: want 1–%d", MaxDevicesPerUser)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deviceLimit = n
	return s.persistLocked()
}

// UserDeviceLimit is user id's effective limit: their override, else the
// workspace default — capped at MaxDevicesPerUser.
func (s *Store) UserDeviceLimit(id string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.userLimitLocked(s.byID[normalizeID(id)])
}

func (s *Store) userLimitLocked(u *User) int {
	if u != nil && u.MaxDevices != nil {
		return min(max(*u.MaxDevices, 0), MaxDevicesPerUser)
	}
	return s.deviceLimitLocked()
}

// SetMaxDevices sets (n) or clears (nil) user id's override, 0–
// MaxDevicesPerUser (0: no devices).
func (s *Store) SetMaxDevices(id string, n *int) (*User, error) {
	if n != nil && (*n < 0 || *n > MaxDevicesPerUser) {
		return nil, fmt.Errorf("maxDevices: want 0–%d, or null for the workspace default", MaxDevicesPerUser)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID[normalizeID(id)]
	if u == nil {
		return nil, fmt.Errorf("no such user %q", id)
	}
	nu := *u
	nu.MaxDevices = nil
	if n != nil {
		v := *n
		nu.MaxDevices = &v
	}
	s.byID[nu.ID] = &nu
	if err := s.persistLocked(); err != nil {
		return nil, err
	}
	c := nu
	return &c, nil
}

// DeviceLimitRow is one user at or over their limit, or with an override.
type DeviceLimitRow struct {
	User     string `json:"user"`
	Name     string `json:"name,omitempty"`
	Override *int   `json:"override,omitempty"`
	Limit    int    `json:"limit"`
	Count    int    `json:"count"`
}

// DeviceLimitRows lists every user with an override or at their limit
// (the admin console's view), sorted by id.
func (s *Store) DeviceLimitRows() []DeviceLimitRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []DeviceLimitRow
	for _, u := range s.sortedLocked() {
		lim := s.userLimitLocked(u)
		if u.MaxDevices == nil && len(u.Devices) < lim {
			continue
		}
		out = append(out, DeviceLimitRow{User: u.ID, Name: u.Name, Override: u.MaxDevices, Limit: lim, Count: len(u.Devices)})
	}
	return out
}

func (s *Store) sortedLocked() []*User {
	out := make([]*User, 0, len(s.byID))
	for _, u := range s.byID {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// tunnelKeyTakenLocked reports a TunnelKey on any device but except.
func (s *Store) tunnelKeyTakenLocked(key, except string) bool {
	if key == "" {
		return false
	}
	for _, u := range s.byID {
		for _, d := range u.Devices {
			if d.TunnelKey == key && d.ID != except {
				return true
			}
		}
	}
	return false
}

// SetDeviceTunnel registers a device's tunnel keys (the upgrade route, spec
// §9): its Noise public key (base64url) and iroh EndpointId (hex). The key
// must be unique across the store.
func (s *Store) SetDeviceTunnel(deviceID, tunnelKey, endpoint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tunnelKeyTakenLocked(tunnelKey, deviceID) {
		return ErrTunnelKeyTaken
	}
	for _, u := range s.byID {
		for i, dv := range u.Devices {
			if dv.ID != deviceID {
				continue
			}
			nu := *u
			nu.Devices = append([]Device(nil), u.Devices...)
			nu.Devices[i].TunnelKey, nu.Devices[i].Endpoint = tunnelKey, endpoint
			s.byID[nu.ID] = &nu
			return s.persistLocked()
		}
	}
	return fmt.Errorf("no such device %q", deviceID)
}

// TunnelDevice is a tunnel-paired device and its user.
type TunnelDevice struct {
	UserID   string
	Disabled bool
	Device   Device
}

// TunnelDevices lists every device with a tunnel key (the app tunnel's
// allowlist and its key index).
func (s *Store) TunnelDevices() []TunnelDevice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []TunnelDevice
	for _, u := range s.sortedLocked() {
		for _, d := range u.Devices {
			if d.TunnelKey != "" {
				out = append(out, TunnelDevice{UserID: u.ID, Disabled: u.Disabled, Device: d})
			}
		}
	}
	return out
}
