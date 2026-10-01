// Package wssettings is the workspace settings an admin sets from the admin
// console's workspace tab that have no store of their own (D173):
// data/workspace-settings.json, a small xbind-owned JSON object. Each
// setting has a default for when its key is absent, so a missing file is
// every default. A write replaces only the keys it sets and keeps every
// other key the file holds (a newer xbind's), and an xbind that predates a
// key never reads it, so a downgrade ignores it and an upgrade back finds
// it as it was left. The file is outside the vault (readable while it is
// sealed) and under data/, which no sandbox mounts.
package wssettings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// The file's keys.
const (
	// KeyBaseAutoUpdate: a tile's terminal layer built on an older base
	// image moves to the current base at its next session start (D173).
	KeyBaseAutoUpdate = "baseAutoUpdate"
)

// Settings is the effective settings: the file's, defaults filled in.
type Settings struct {
	BaseAutoUpdate bool `json:"baseAutoUpdate"`
}

// Defaults is what a missing key means.
func Defaults() Settings { return Settings{BaseAutoUpdate: true} }

// Patch is a partial update: each present key replaces its setting, an
// absent one leaves it alone.
type Patch struct {
	BaseAutoUpdate *bool `json:"baseAutoUpdate"`
}

// Empty reports a patch that sets nothing.
func (p Patch) Empty() bool { return p.BaseAutoUpdate == nil }

// Store keeps the settings at one path.
type Store struct {
	path string
	mu   sync.Mutex
}

// New is the store at path (data/workspace-settings.json).
func New(path string) *Store { return &Store{path: path} }

// Path is the file the store keeps.
func (s *Store) Path() string { return s.path }

// Load reads the settings: a missing file, or a missing key, is the
// default. A file that isn't a JSON object, or a known key of the wrong
// type, is an error, with the defaults.
func (s *Store) Load() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.readLocked()
	if err != nil {
		return Defaults(), err
	}
	return decode(doc)
}

// BaseAutoUpdate is the base auto-update setting as a session's start
// reads it (the terminal manager's hook). A file that can't be read turns
// it off — a layer then keeps its base, as it did before the setting
// existed — rather than discard anything on a guess.
func (s *Store) BaseAutoUpdate() bool {
	st, err := s.Load()
	if err != nil {
		slog.Warn("workspace settings unreadable: terminals keep their base images until it is fixed", "file", s.path, "err", err)
		return false
	}
	return st.BaseAutoUpdate
}

// Apply sets what p names, keeps the rest of the file as it is, writes it
// atomically and returns the result. A file Load can't read is left alone
// and the error returned: rewriting it would lose what it holds.
func (s *Store) Apply(p Patch) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.readLocked()
	if err != nil {
		return Defaults(), err
	}
	if _, err := decode(doc); err != nil {
		return Defaults(), err
	}
	if p.BaseAutoUpdate != nil {
		doc[KeyBaseAutoUpdate], _ = json.Marshal(*p.BaseAutoUpdate)
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return Defaults(), err
	}
	if err := fsutil.WriteFileAtomicIn(s.path, append(b, '\n'), 0o644); err != nil {
		return Defaults(), err
	}
	return decode(doc)
}

// readLocked is the file's top-level object, every key raw; an empty map
// when there is no file.
func (s *Store) readLocked() (map[string]json.RawMessage, error) {
	doc := map[string]json.RawMessage{}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("data/%s isn't a JSON object (fix or remove it): %w", filepath.Base(s.path), err)
	}
	if doc == nil { // the file says null
		return nil, fmt.Errorf("data/%s isn't a JSON object (fix or remove it)", filepath.Base(s.path))
	}
	return doc, nil
}

// decode reads the known keys of doc over the defaults.
func decode(doc map[string]json.RawMessage) (Settings, error) {
	st := Defaults()
	if raw, ok := doc[KeyBaseAutoUpdate]; ok {
		if err := json.Unmarshal(raw, &st.BaseAutoUpdate); err != nil {
			return Defaults(), fmt.Errorf("workspace settings: %s must be true or false", KeyBaseAutoUpdate)
		}
	}
	return st, nil
}
