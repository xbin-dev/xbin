package users

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Devices — the native app's hardware-bound credentials (plans/native.md §5,
// docs/auth.md §Device login). A device is a P-256 public key the app
// generated in its Secure Enclave, enrolled with a one-time code minted by a
// signed-in session of THIS user; it signs a server challenge to open a
// bearer session. Only the public key is stored. Devices live on the user
// row, so deleting the user removes them, and they are store-owned: a plain
// Upsert (the users API) never adds, drops or rewrites them.

// Device is one enrolled device on a user row.
type Device struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform,omitempty"`
	// PublicKey is the device key as SPKI DER, base64url without padding —
	// exactly what the app sent at enrollment (validated there as P-256).
	PublicKey string `json:"publicKey"`
	// Origin is the server origin the device signs into every login
	// challenge (scheme://host[:port]), fixed at enrollment — the origin of
	// the enrollment URL the app was given (docs/auth.md §Device login).
	Origin   string `json:"origin"`
	Created  int64  `json:"created"`
	LastUsed int64  `json:"lastUsed,omitempty"` // unix: last successful device login
	LastIP   string `json:"lastIP,omitempty"`   // client IP of that login
}

// MaxDevicesPerUser bounds a user row (enrollment refuses past it).
const MaxDevicesPerUser = 32

// Device field limits (enrollment input from the app).
const (
	maxDeviceName     = 64
	maxDevicePlatform = 32
)

// cleanDeviceText trims s, drops control characters and caps its length (in
// runes) — device names are shown in the shell and admin console.
func cleanDeviceText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max]))
	}
	return s
}

func newDeviceID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "dev-" + hex.EncodeToString(b), nil
}

// AddDevice enrolls a device on user id: assigns its id and creation time,
// normalises name/platform, and persists. The caller has validated the key
// and resolved the origin. A disabled or unknown user is refused.
func (s *Store) AddDevice(id string, d Device) (Device, error) {
	if d.PublicKey == "" || d.Origin == "" {
		return Device{}, fmt.Errorf("device needs a public key and an origin")
	}
	d.Name = cleanDeviceText(d.Name, maxDeviceName)
	d.Platform = strings.ToLower(cleanDeviceText(d.Platform, maxDevicePlatform))
	if d.Name == "" {
		d.Name = "device"
	}
	did, err := newDeviceID()
	if err != nil {
		return Device{}, err
	}
	d.ID, d.Created, d.LastUsed, d.LastIP = did, timeNow(), 0, ""
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID[normalizeID(id)]
	if u == nil || u.Disabled {
		return Device{}, fmt.Errorf("no such user %q, or the account is disabled", id)
	}
	if len(u.Devices) >= MaxDevicesPerUser {
		return Device{}, fmt.Errorf("too many devices (max %d) — remove one first", MaxDevicesPerUser)
	}
	nu := *u
	nu.Devices = append(append([]Device(nil), u.Devices...), d)
	s.byID[nu.ID] = &nu
	if err := s.persistLocked(); err != nil {
		return Device{}, err
	}
	return d, nil
}

// FindDevice resolves a device id to its owning user id and the device.
func (s *Store) FindDevice(deviceID string) (userID string, d Device, ok bool) {
	if deviceID == "" {
		return "", Device{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.byID {
		for _, dv := range u.Devices {
			if dv.ID == deviceID {
				return u.ID, dv, true
			}
		}
	}
	return "", Device{}, false
}

// Devices lists a user's devices, oldest first (a copy).
func (s *Store) Devices(id string) []Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.byID[normalizeID(id)]
	if u == nil {
		return []Device{}
	}
	out := append([]Device{}, u.Devices...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created < out[j].Created })
	return out
}

// RemoveDevice deletes a device by id wherever it is enrolled, returning the
// user it belonged to. ok=false: no such device.
func (s *Store) RemoveDevice(deviceID string) (userID string, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.byID {
		for i, dv := range u.Devices {
			if dv.ID != deviceID {
				continue
			}
			nu := *u
			nu.Devices = append(append([]Device(nil), u.Devices[:i]...), u.Devices[i+1:]...)
			if len(nu.Devices) == 0 {
				nu.Devices = nil
			}
			s.byID[nu.ID] = &nu
			return nu.ID, true, s.persistLocked()
		}
	}
	return "", false, nil
}

// TouchDevice stamps a successful device login (time + client IP). Like
// TouchLogin, a failure only logs upstream — it never blocks a login.
func (s *Store) TouchDevice(deviceID, ip string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.byID {
		for i, dv := range u.Devices {
			if dv.ID != deviceID {
				continue
			}
			nu := *u
			nu.Devices = append([]Device(nil), u.Devices...)
			nu.Devices[i].LastUsed, nu.Devices[i].LastIP = timeNow(), ip
			s.byID[nu.ID] = &nu
			return s.persistLocked()
		}
	}
	return fmt.Errorf("no such device %q", deviceID)
}
