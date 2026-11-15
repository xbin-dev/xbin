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
// The gate asks this store on every delegate call (the switches, a standing
// approval), so:
//
//   - each person has a lock of their own: one person's agent — a slow
//     disk, a big file, a flood of requests — never waits on, or holds up,
//     another's. The store's own lock guards only the map of people;
//   - a file is parsed once and kept; every call stats it (size and
//     mtime), so a hand edit or a restore is what the next call sees
//     without a read per call;
//   - a file that can't be parsed reads as the closed state (off, paused,
//     no approvals) and is never overwritten by a read.

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

	mu     sync.Mutex // people and ws only: never held across a file
	people map[string]*person
	wsMu   sync.Mutex
	ws     cached[Workspace]

	// Audit is the per-person audit log, under the same directory.
	Audit *Log
}

// person is one person's files, their cache and their lock.
type person struct {
	mu    sync.Mutex
	state cached[State]
	reqs  cached[requestsDoc]
	aps   cached[approvalsDoc]

	// change sets: a lock and a parsed-file cache of their own (changeset.go)
	setsMu sync.Mutex
	sets   map[string]*cachedSet
}

// OpenStore is the store under dir (created at the first write).
func OpenStore(dir string) *Store {
	return &Store{dir: dir, now: time.Now, people: map[string]*person{}, Audit: Open(dir)}
}

// SetClock replaces the store's clock (tests, before any use).
func (s *Store) SetClock(now func() time.Time) {
	s.now = now
	s.Audit.now = now
}

// lock returns user's state, locked: the caller unlocks it.
func (s *Store) lock(user string) *person {
	s.mu.Lock()
	p := s.people[user]
	if p == nil {
		p = &person{}
		s.people[user] = p
	}
	s.mu.Unlock()
	p.mu.Lock()
	return p
}

// cached is a parsed file with the stat it was parsed at.
type cached[T any] struct {
	v      T
	size   int64
	mtime  time.Time
	loaded bool
}

// load answers path's document: the cached one while the file's size and
// mtime haven't changed, else a fresh parse. A missing file is the zero
// document; a file that can't be read or parsed is an error.
func load[T any](c *cached[T], path string) (T, error) {
	var zero T
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		c.v, c.size, c.mtime, c.loaded = zero, -1, time.Time{}, true
		return zero, nil
	} else if err != nil {
		return zero, err
	}
	if c.loaded && c.size == fi.Size() && c.mtime.Equal(fi.ModTime()) {
		return c.v, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return zero, err
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		c.loaded = false
		return zero, err
	}
	c.v, c.size, c.mtime, c.loaded = v, fi.Size(), fi.ModTime(), true
	return v, nil
}

// save writes v to path and keeps it.
func save[T any](c *cached[T], path string, v T) error {
	c.loaded = false
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if fi, err := os.Stat(path); err == nil {
		c.v, c.size, c.mtime, c.loaded = v, fi.Size(), fi.ModTime(), true
	}
	return nil
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

// People lists the ids of everyone the store keeps something about.
func (s *Store) People() []string {
	if s == nil {
		return nil
	}
	ents, _ := os.ReadDir(s.dir)
	var out []string
	for _, e := range ents {
		if id, ok := userOfDir(e.Name()); ok && e.IsDir() {
			out = append(out, id)
		}
	}
	return out
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
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	w, err := load(&s.ws, filepath.Join(s.dir, "workspace.json"))
	if err != nil {
		return Workspace{}
	}
	return w
}

// SetWorkspace writes the switch.
func (s *Store) SetWorkspace(enabled bool, by string) (Workspace, error) {
	if s == nil {
		return Workspace{}, ErrNoStore
	}
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	w := Workspace{Enabled: enabled, By: by, At: s.now().UTC().Truncate(time.Second)}
	return w, save(&s.ws, filepath.Join(s.dir, "workspace.json"), w)
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
	p := s.lock(user)
	defer p.mu.Unlock()
	st, err := load(&p.state, s.userFile(user, "state.json"))
	if err != nil {
		return State{Paused: true}
	}
	return st
}

// SetState applies change to user's state and writes it.
func (s *Store) SetState(user string, change func(*State)) (State, error) {
	if s == nil || user == "" {
		return State{Paused: true}, ErrNoStore
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	st, err := load(&p.state, s.userFile(user, "state.json"))
	if err != nil {
		st = State{} // an unreadable file is replaced by what the person now decides
	}
	change(&st)
	st.Updated = s.now().UTC().Truncate(time.Second)
	return st, save(&p.state, s.userFile(user, "state.json"), st)
}
