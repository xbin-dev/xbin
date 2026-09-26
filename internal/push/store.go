package push

import (
	"cmp"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// Device is one app install's registration for one user of this workspace:
// where to push (the relay handle) and whom to seal to (its X25519 key).
// Keyed by (User, DeviceID); DeviceID is the app's device id — the same
// string device login uses, nothing else is shared with it.
type Device struct {
	User      string   `json:"user"`
	DeviceID  string   `json:"deviceId"`
	Handle    string   `json:"handle"`
	PublicKey string   `json:"publicKey"` // base64url X25519
	Kinds     []string `json:"kinds,omitempty"`
	Created   int64    `json:"created"`
	Updated   int64    `json:"updated"`
	LastSent  int64    `json:"lastSent,omitempty"`
}

// Prefs are one user's push preferences across their devices.
type Prefs struct {
	MutedTiles []string `json:"mutedTiles"`
}

// RelayConfig is the workspace's relay opt-in, set by an admin.
type RelayConfig struct {
	URL         string `json:"url"`
	Key         string `json:"key"`
	WorkspaceID string `json:"workspaceId,omitempty"` // the relay's id for us, when xbind registered itself
	Set         int64  `json:"set"`
	By          string `json:"by,omitempty"`
}

type fileState struct {
	Version   int               `json:"version"`
	Workspace string            `json:"workspace"` // this workspace's push id (Payload.WS)
	Devices   []*Device         `json:"devices"`
	Prefs     map[string]*Prefs `json:"prefs,omitempty"`
	Relay     *RelayConfig      `json:"relay,omitempty"`
}

// maxDevicesPerUser bounds registrations; the least recently updated one
// goes when a user registers more.
const maxDevicesPerUser = 20

// store is data/push/push.json (mode 0600: it holds the relay key),
// rewritten atomically on every change.
type store struct {
	mu   sync.Mutex
	path string
	st   fileState
}

func openStore(dir string) (*store, error) {
	s := &store{path: filepath.Join(dir, "push.json"), st: fileState{Version: 1}}
	b, err := os.ReadFile(s.path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &s.st); err != nil {
			return nil, err
		}
	case !os.IsNotExist(err):
		return nil, err
	}
	if s.st.Prefs == nil {
		s.st.Prefs = map[string]*Prefs{}
	}
	if s.st.Workspace == "" {
		id := make([]byte, 12)
		if _, err := rand.Read(id); err != nil {
			return nil, err
		}
		s.st.Workspace = b64.EncodeToString(id)
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *store) saveLocked() error {
	b, err := json.MarshalIndent(&s.st, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(s.path, b, 0o600)
}

func (s *store) workspace() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Workspace
}

// upsert registers or refreshes a device. A handle belongs to one
// registration: another one holding it (the app signed in as someone else)
// is replaced.
func (s *store) upsert(d Device, now int64) (Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var cur *Device
	keep := s.st.Devices[:0]
	for _, x := range s.st.Devices {
		switch {
		case x.User == d.User && x.DeviceID == d.DeviceID:
			cur = x
		case x.Handle == d.Handle:
			continue // replaced
		}
		keep = append(keep, x)
	}
	s.st.Devices = keep
	if cur == nil {
		var mine []*Device
		for _, x := range s.st.Devices {
			if x.User == d.User {
				mine = append(mine, x)
			}
		}
		if len(mine) >= maxDevicesPerUser {
			// stalest first; ties (one second) in registration order
			slices.SortStableFunc(mine, func(a, b *Device) int { return cmp.Compare(a.Updated, b.Updated) })
			drop := mine[:len(mine)-maxDevicesPerUser+1]
			s.st.Devices = slices.DeleteFunc(s.st.Devices, func(x *Device) bool { return slices.Contains(drop, x) })
		}
		cur = &Device{User: d.User, DeviceID: d.DeviceID, Created: now}
		s.st.Devices = append(s.st.Devices, cur)
	}
	cur.Handle, cur.PublicKey, cur.Kinds, cur.Updated = d.Handle, d.PublicKey, d.Kinds, now
	return *cur, s.saveLocked()
}

// remove drops one registration; false when there was none.
func (s *store) remove(user, deviceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.st.Devices)
	s.st.Devices = slices.DeleteFunc(s.st.Devices, func(x *Device) bool { return x.User == user && x.DeviceID == deviceID })
	if len(s.st.Devices) == n {
		return false
	}
	_ = s.saveLocked()
	return true
}

// removeHandle drops the registration holding a handle the relay called
// dead, unless it was re-registered with another handle meanwhile.
func (s *store) removeHandle(user, deviceID, handle string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.st.Devices)
	s.st.Devices = slices.DeleteFunc(s.st.Devices, func(x *Device) bool {
		return x.User == user && x.DeviceID == deviceID && x.Handle == handle
	})
	if len(s.st.Devices) == n {
		return false
	}
	_ = s.saveLocked()
	return true
}

// removeUser drops every registration and preference of a user.
func (s *store) removeUser(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Devices = slices.DeleteFunc(s.st.Devices, func(x *Device) bool { return x.User == user })
	delete(s.st.Prefs, user)
	_ = s.saveLocked()
}

// devices returns copies of a user's registrations.
func (s *store) devices(user string) []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Device
	for _, x := range s.st.Devices {
		if x.User == user {
			out = append(out, *x)
		}
	}
	return out
}

func (s *store) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.st.Devices)
}

// markSent records a delivery (in memory; it rides the next write).
func (s *store) markSent(user, deviceID string, now int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.st.Devices {
		if x.User == user && x.DeviceID == deviceID {
			x.LastSent = now
		}
	}
}

func (s *store) prefs(user string) Prefs {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := Prefs{MutedTiles: []string{}}
	if x := s.st.Prefs[user]; x != nil {
		p.MutedTiles = append(p.MutedTiles, x.MutedTiles...)
	}
	return p
}

func (s *store) setPrefs(user string, p Prefs) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(p.MutedTiles) == 0 {
		delete(s.st.Prefs, user)
	} else {
		s.st.Prefs[user] = &p
	}
	return s.saveLocked()
}

func (s *store) muted(user, tile string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.st.Prefs[user]
	return p != nil && slices.Contains(p.MutedTiles, tile)
}

func (s *store) relay() *RelayConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Relay == nil {
		return nil
	}
	c := *s.st.Relay
	return &c
}

func (s *store) setRelay(c *RelayConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Relay = c
	return s.saveLocked()
}
