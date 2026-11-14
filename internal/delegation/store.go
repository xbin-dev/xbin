package delegation

// store.go — what xbind keeps about delegation besides the audit log, all
// under <data>/delegation, 0600, xbind's own (no sandbox writes there, D78;
// no tile, the agent's component included, reads it):
//
//	workspace.json                 the workspace's switch
//	<hex(user)>/state.json         the person's pause and admin opt-in
//	<hex(user)>/requests.json      approval requests and their tickets (requests.go)
//	<hex(user)>/approvals.json     standing approvals (requests.go)
//	<hex(user)>/changesets/<id>.json   admin change sets (changeset.go)
//	<hex(user)>/audit-YYYY-MM.jsonl    the audit log (audit.go)
//
// Every file is small and read under one lock at each call: nothing is
// cached, so a hand edit or a restore is what the next request sees. A
// file that can't be parsed reads as the closed state (off, paused, no
// approvals) and is never overwritten by a read.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// Store is the delegation store. A nil *Store answers the closed state.
type Store struct {
	dir string
	now func() time.Time
	mu  sync.Mutex
	// Audit is the per-person audit log, under the same directory.
	Audit *Log
}

// OpenStore is the store under dir (created at the first write).
func OpenStore(dir string) *Store {
	return &Store{dir: dir, now: time.Now, Audit: Open(dir)}
}

// SetClock replaces the store's clock (tests).
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	s.now = now
	s.Audit.now = now
	s.mu.Unlock()
}

// readJSON reads path into v; ok false when the file is missing, and an
// error when it exists and can't be read or parsed.
func readJSON(path string, v any) (ok bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, err
	}
	return true, nil
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, append(b, '\n'), 0o600)
}

func (s *Store) userFile(user, name string) string {
	return filepath.Join(userDir(s.dir, user), name)
}

// ---- the workspace's switch ----

// Workspace is the workspace's delegation switch.
type Workspace struct {
	// Enabled: people's workspace agents may act for them. Off until an
	// admin turns it on.
	Enabled bool      `json:"enabled"`
	By      string    `json:"by,omitempty"`
	At      time.Time `json:"at,omitempty"`
}

// Workspace reads the switch: off when it was never set or can't be read.
func (s *Store) Workspace() Workspace {
	if s == nil {
		return Workspace{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var w Workspace
	if ok, err := readJSON(filepath.Join(s.dir, "workspace.json"), &w); !ok || err != nil {
		return Workspace{}
	}
	return w
}

// SetWorkspace writes the switch.
func (s *Store) SetWorkspace(enabled bool, by string) (Workspace, error) {
	if s == nil {
		return Workspace{}, errors.New("this xbind keeps no delegation state")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w := Workspace{Enabled: enabled, By: by, At: s.now().UTC().Truncate(time.Second)}
	return w, writeJSON(filepath.Join(s.dir, "workspace.json"), w)
}

// ---- a person's state ----

// State is a person's own switches.
type State struct {
	// Paused: the person paused their agent; it acts for them again when
	// they resume it.
	Paused bool `json:"paused"`
	// AdminChanges: the person, an admin, lets their agent read what change
	// sets cover and propose them (never apply them).
	AdminChanges bool      `json:"adminChanges"`
	Updated      time.Time `json:"updated,omitempty"`
}

// State reads user's state. A file that can't be read is the closed state:
// paused, no admin changes.
func (s *Store) State(user string) State {
	if s == nil || user == "" {
		return State{Paused: true}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked(user)
}

func (s *Store) stateLocked(user string) State {
	var st State
	if _, err := readJSON(s.userFile(user, "state.json"), &st); err != nil {
		return State{Paused: true}
	}
	return st
}

// SetState applies change to user's state and writes it.
func (s *Store) SetState(user string, change func(*State)) (State, error) {
	if s == nil || user == "" {
		return State{Paused: true}, errors.New("this xbind keeps no delegation state")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var st State
	if _, err := readJSON(s.userFile(user, "state.json"), &st); err != nil {
		st = State{} // an unreadable file is replaced by what the person now decides
	}
	change(&st)
	st.Updated = s.now().UTC().Truncate(time.Second)
	return st, writeJSON(s.userFile(user, "state.json"), st)
}
