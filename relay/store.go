package relay

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Handle is one APNs device token behind an opaque name. A workspace pushes
// to the handle, never to the token; the first workspace that pushes to a
// handle owns it (Workspace), so a handle the app gave one workspace cannot
// be used by another, and deleting it cuts that workspace off.
type Handle struct {
	Token     string `json:"token"` // APNs device token, lowercase hex
	Topic     string `json:"topic"` // the app's bundle id
	Env       string `json:"env"`   // production | development
	Workspace string `json:"workspace,omitempty"`
	Created   int64  `json:"created"`
	Used      int64  `json:"used,omitempty"`
}

// Workspace is one xbind installation that may push. Only a hash of its
// key is stored.
type Workspace struct {
	KeyHash string `json:"keyHash"` // hex sha256 of the key
	Created int64  `json:"created"`
	Used    int64  `json:"used,omitempty"`
}

type state struct {
	Version    int                   `json:"version"`
	Handles    map[string]*Handle    `json:"handles"`
	Workspaces map[string]*Workspace `json:"workspaces"`
}

// store is the relay's whole state: a JSON file rewritten atomically on
// every change that matters (handles and workspaces coming and going).
// Usage timestamps are kept in memory and ride along with the next write.
type store struct {
	mu     sync.Mutex
	path   string // "" = memory only
	st     state
	byHash map[string]string // key hash → workspace id
}

var (
	errNoHandle     = errors.New("unknown handle")
	errHandleBound  = errors.New("handle belongs to another workspace")
	errBadWorkspace = errors.New("unknown workspace key")
)

// maxHandlesPerToken bounds how many handles one device token may hold (one
// per workspace the app talks to); creating more evicts the least recently
// used.
const maxHandlesPerToken = 64

func openStore(path string) (*store, error) {
	s := &store{path: path, st: state{Version: 1, Handles: map[string]*Handle{}, Workspaces: map[string]*Workspace{}}}
	if path != "" {
		b, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := json.Unmarshal(b, &s.st); err != nil {
				return nil, fmt.Errorf("relay state %s: %w", path, err)
			}
		case !os.IsNotExist(err):
			return nil, err
		}
	}
	if s.st.Handles == nil {
		s.st.Handles = map[string]*Handle{}
	}
	if s.st.Workspaces == nil {
		s.st.Workspaces = map[string]*Workspace{}
	}
	s.byHash = map[string]string{}
	for id, w := range s.st.Workspaces {
		s.byHash[w.KeyHash] = id
	}
	return s, nil
}

// saveLocked writes the state file (temp file + rename). Callers hold mu.
func (s *store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(&s.st, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on a supported platform
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// newHandle stores a fresh handle for token.
func (s *store) newHandle(token, topic, env string, now time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var same []string
	for id, h := range s.st.Handles {
		if h.Token == token {
			same = append(same, id)
		}
	}
	if len(same) >= maxHandlesPerToken {
		sort.Slice(same, func(i, j int) bool {
			a, b := s.st.Handles[same[i]], s.st.Handles[same[j]]
			return max(a.Used, a.Created) < max(b.Used, b.Created)
		})
		for _, id := range same[:len(same)-maxHandlesPerToken+1] {
			delete(s.st.Handles, id)
		}
	}
	id := randomID(16)
	s.st.Handles[id] = &Handle{Token: token, Topic: topic, Env: env, Created: now.Unix()}
	return id, s.saveLocked()
}

// repoint changes the device token behind an existing handle (the app's
// APNs token changed); the workspace binding stays.
func (s *store) repoint(id, token, topic, env string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.st.Handles[id]
	if h == nil {
		return errNoHandle
	}
	h.Token, h.Topic, h.Env = token, topic, env
	return s.saveLocked()
}

func (s *store) deleteHandle(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.st.Handles[id]; !ok {
		return false
	}
	delete(s.st.Handles, id)
	_ = s.saveLocked()
	return true
}

// dropToken removes every handle of a device token APNs called dead.
func (s *store) dropToken(token string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, h := range s.st.Handles {
		if h.Token == token {
			delete(s.st.Handles, id)
			n++
		}
	}
	if n > 0 {
		_ = s.saveLocked()
	}
	return n
}

// newWorkspace mints a workspace id and key; only the key's hash is kept.
func (s *store) newWorkspace(now time.Time) (id, key string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id = randomID(12)
	key = "xbr_" + randomID(32)
	h := hashKey(key)
	s.st.Workspaces[id] = &Workspace{KeyHash: h, Created: now.Unix()}
	s.byHash[h] = id
	return id, key, s.saveLocked()
}

// workspaceFor resolves a key to its workspace id.
func (s *store) workspaceFor(key string, now time.Time) (string, error) {
	if key == "" {
		return "", errBadWorkspace
	}
	h := hashKey(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byHash[h]
	if !ok {
		return "", errBadWorkspace
	}
	w := s.st.Workspaces[id]
	if w == nil || subtle.ConstantTimeCompare([]byte(w.KeyHash), []byte(h)) != 1 {
		return "", errBadWorkspace
	}
	w.Used = now.Unix()
	return id, nil
}

// target resolves a handle for a push from workspace ws, binding an unbound
// handle to it (first use). Returns a copy.
func (s *store) target(id, ws string, now time.Time) (Handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.st.Handles[id]
	if h == nil {
		return Handle{}, errNoHandle
	}
	switch h.Workspace {
	case ws:
	case "":
		h.Workspace = ws
		if err := s.saveLocked(); err != nil {
			return Handle{}, err
		}
	default:
		return Handle{}, errHandleBound
	}
	h.Used = now.Unix()
	return *h, nil
}

func (s *store) counts() (handles, workspaces int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.st.Handles), len(s.st.Workspaces)
}
