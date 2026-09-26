package push

import (
	"cmp"
	"crypto/rand"
	"encoding/json"
	"errors"
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
//
// A registration is bound to what made it, so it ends with it: one a
// device session made is that enrolled device's (DeviceID is the device
// login's id, enforced; removing the device or its session signing out
// drops it); one any other login made (the app before enrolling, a
// browser, the owner token) names that login's credential generation in
// Session and goes when the login does (logout, expiry, sign-out-
// everywhere, rotation) — a stolen session can't leave a registration
// behind that outlives it.
type Device struct {
	User      string   `json:"user"`
	DeviceID  string   `json:"deviceId"`
	Session   string   `json:"session,omitempty"` // "" = the enrolled device's own
	Handle    string   `json:"handle"`
	PublicKey string   `json:"publicKey"` // base64url X25519
	Kinds     []string `json:"kinds,omitempty"`
	Created   int64    `json:"created"`
	Updated   int64    `json:"updated"`
	LastSent  int64    `json:"lastSent,omitempty"`
	// Bound is the relay epoch (Service.epoch) the handle last delivered
	// under: the relay bound it to that relay workspace, so under another
	// one it can never deliver again.
	Bound string `json:"bound,omitempty"`
	// RelayErr is the relay's refusal of this handle (handle_bound,
	// handle_unknown), made under the relay epoch ErrEpoch.
	RelayErr string `json:"relayError,omitempty"`
	ErrEpoch string `json:"errEpoch,omitempty"`
	// StartHandle is the relay's Live Activity handle for the app's
	// push-to-start token, and Activities the Live Activities the device
	// shows for agent sessions (activity.go). Both hang off Handle at the
	// relay: a new Handle drops them.
	StartHandle string     `json:"startHandle,omitempty"`
	Activities  []Activity `json:"activities,omitempty"`
}

// clone copies d with slices of its own (a copy handed out of the store
// must not share what the store changes in place).
func (d Device) clone() Device {
	d.Kinds = slices.Clone(d.Kinds)
	d.Activities = slices.Clone(d.Activities)
	return d
}

// stale reports a registration whose handle cannot deliver under the relay
// epoch: skipped until the app registers a fresh handle (needsNewHandle).
func (d *Device) stale(epoch string) bool {
	return (d.RelayErr != "" && d.ErrEpoch == epoch) || (d.Bound != "" && d.Bound != epoch)
}

// Prefs are one user's push preferences across their devices.
type Prefs struct {
	MutedTiles []string `json:"mutedTiles"`
}

// RelayConfig is the workspace's relay opt-in, set by an admin. Turning
// push off keeps it (Off): the key identifies this workspace at the relay,
// and every handle the apps registered is bound to it.
type RelayConfig struct {
	URL         string `json:"url"`
	Key         string `json:"key"`
	WorkspaceID string `json:"workspaceId,omitempty"` // the relay's id for us
	Set         int64  `json:"set"`
	By          string `json:"by,omitempty"`
	Off         bool   `json:"off,omitempty"`
}

type fileState struct {
	Version   int               `json:"version"`
	Workspace string            `json:"workspace"` // this workspace's push id (Payload.WS)
	Devices   []*Device         `json:"devices"`
	Prefs     map[string]*Prefs `json:"prefs,omitempty"`
	Relay     *RelayConfig      `json:"relay,omitempty"`
}

// maxDevicesPerUser bounds registrations; the least recently updated one
// goes when a user registers more. Every note fans out to each of them —
// a relay post apiece, charged to the person's budgets per post — so it is
// kept to what one person carries.
const maxDevicesPerUser = 10

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

// errDeviceBound: a login that is not the device's own session tried to
// take over an enrolled device's registration.
var errDeviceBound = errors.New("that deviceId is an enrolled device's registration: it registers from its own device session")

// upsert registers or refreshes a device. A handle belongs to one
// registration: another one holding it (the app signed in as someone else)
// is replaced. An enrolled device's registration (Session "") is its
// device session's alone. start, when set, replaces the push-to-start
// handle ("" removes it); a new handle drops it and the Live Activities
// (they hang off the old one at the relay).
func (s *store) upsert(d Device, now int64, start *string) (Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.st.Devices {
		if x.User == d.User && x.DeviceID == d.DeviceID && x.Session == "" && d.Session != "" {
			return Device{}, errDeviceBound
		}
	}
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
	if cur.Handle != d.Handle { // a fresh handle: nothing the relay said about the old one holds
		cur.Bound, cur.RelayErr, cur.ErrEpoch = "", "", ""
		cur.StartHandle, cur.Activities = "", nil
	}
	if start != nil {
		cur.StartHandle = *start
	}
	cur.Handle, cur.PublicKey, cur.Kinds, cur.Updated, cur.Session = d.Handle, d.PublicKey, d.Kinds, now, d.Session
	return cur.clone(), s.saveLocked()
}

// removeIf drops the registrations drop picks (dead logins' — the caller's
// check must not take s.mu); returns how many went.
func (s *store) removeIf(drop func(Device) bool) int {
	s.mu.Lock()
	cands := make([]Device, 0, len(s.st.Devices))
	for _, x := range s.st.Devices {
		cands = append(cands, x.clone())
	}
	s.mu.Unlock()
	var gone []Device
	for _, d := range cands {
		if drop(d) {
			gone = append(gone, d)
		}
	}
	if len(gone) == 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.st.Devices)
	s.st.Devices = slices.DeleteFunc(s.st.Devices, func(x *Device) bool {
		return slices.ContainsFunc(gone, func(g Device) bool {
			return g.User == x.User && g.DeviceID == x.DeviceID && g.Session == x.Session && g.Handle == x.Handle
		})
	})
	n -= len(s.st.Devices)
	if n > 0 {
		_ = s.saveLocked()
	}
	return n
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

// removeUser drops every registration of a user, and their preferences
// too when prefs is set. Returns how many registrations went.
func (s *store) removeUser(user string, prefs bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.st.Devices)
	s.st.Devices = slices.DeleteFunc(s.st.Devices, func(x *Device) bool { return x.User == user })
	n -= len(s.st.Devices)
	_, hadPrefs := s.st.Prefs[user]
	if prefs {
		delete(s.st.Prefs, user)
	}
	if n > 0 || (prefs && hadPrefs) {
		_ = s.saveLocked()
	}
	return n
}

// removeOlder drops a user's registrations made before since (a unix
// time): they belong to an earlier account that had the same id.
func (s *store) removeOlder(user string, since int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.st.Devices)
	s.st.Devices = slices.DeleteFunc(s.st.Devices, func(x *Device) bool { return x.User == user && x.Created < since })
	if len(s.st.Devices) != n {
		_ = s.saveLocked()
	}
}

// all returns copies of every registration (the admin listing).
func (s *store) all() []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Device, 0, len(s.st.Devices))
	for _, x := range s.st.Devices {
		out = append(out, x.clone())
	}
	return out
}

// devices returns copies of a user's registrations.
func (s *store) devices(user string) []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Device
	for _, x := range s.st.Devices {
		if x.User == user {
			out = append(out, x.clone())
		}
	}
	return out
}

func (s *store) hasDevices(user string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.st.Devices {
		if x.User == user {
			return true
		}
	}
	return false
}

func (s *store) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.st.Devices)
}

// markSent records a delivery under a relay epoch: the time rides the next
// write; a new binding (or a cleared refusal) is written now.
func (s *store) markSent(user, deviceID, handle, epoch string, now int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.st.Devices {
		if x.User == user && x.DeviceID == deviceID && x.Handle == handle {
			x.LastSent = now
			if x.Bound != epoch || x.RelayErr != "" {
				x.Bound, x.RelayErr, x.ErrEpoch = epoch, "", ""
				_ = s.saveLocked()
			}
		}
	}
}

// markRefused records the relay's refusal of a handle under an epoch;
// false when the registration moved on meanwhile.
func (s *store) markRefused(user, deviceID, handle, code, epoch string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.st.Devices {
		if x.User == user && x.DeviceID == deviceID && x.Handle == handle {
			x.RelayErr, x.ErrEpoch = code, epoch
			_ = s.saveLocked()
			return true
		}
	}
	return false
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
