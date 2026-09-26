package relay

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
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

	saved int64 // Used as last written to disk
}

// Workspace is one xbind installation that may push. Only a hash of its
// key is stored.
type Workspace struct {
	KeyHash string `json:"keyHash"` // hex sha256 of the key
	Created int64  `json:"created"`
	Used    int64  `json:"used,omitempty"` // last authenticated call (push or probe)

	saved int64
}

type state struct {
	Version int `json:"version"`
	// Seq is the last journal record this snapshot includes; replay skips
	// records at or below it.
	Seq        uint64                `json:"seq"`
	Handles    map[string]*Handle    `json:"handles"`
	Workspaces map[string]*Workspace `json:"workspaces"`
}

// Retention and capacity (Config fields; zero = these defaults).
const (
	DefaultMaxHandles         = 2_000_000
	DefaultMaxWorkspaces      = 200_000
	DefaultUnboundHandleTTL   = 30 * 24 * time.Hour  // a handle no workspace ever pushed to
	DefaultIdleHandleTTL      = 180 * 24 * time.Hour // a handle nothing pushed to for this long
	DefaultUnusedWorkspaceTTL = 30 * 24 * time.Hour  // a workspace key never used
	DefaultIdleWorkspaceTTL   = 180 * 24 * time.Hour // a workspace key not used (no push, no key check) for this long
)

type storeLimits struct {
	maxHandles, maxWorkspaces                           int
	unboundTTL, idleTTL, unusedWorkspace, idleWorkspace time.Duration
}

// store is the relay's whole state in memory, made durable by an
// append-only journal of small records (journal.go) that is folded into a
// snapshot now and then — so no request rewrites the whole state, and the
// lock the push path takes is never held across disk I/O. Usage timestamps
// are written at most once a day per entry.
type store struct {
	mu        sync.Mutex
	st        state
	byHash    map[string]string // key hash → workspace id
	seq       uint64            // last record handed out
	lim       storeLimits
	lastSweep time.Time

	j *journal // nil = memory only (tests)
}

var (
	errNoHandle     = errors.New("unknown handle")
	errHandleBound  = errors.New("handle belongs to another workspace")
	errBadWorkspace = errors.New("unknown workspace key")
	errFull         = errors.New("the relay is at capacity")
)

// maxHandlesPerToken bounds how many handles one device token may hold (one
// per workspace the app talks to); creating more evicts the least recently
// used.
const maxHandlesPerToken = 64

// usedEvery is how stale a persisted usage timestamp may get.
const usedEvery = 24 * time.Hour

// sweepEvery spaces the retention sweeps (a full store sweeps at most once
// a minute).
const sweepEvery = 10 * time.Minute

func openStore(path string, lim storeLimits) (*store, error) {
	s := &store{lim: lim, st: state{Version: 1}}
	if path != "" {
		j, st, err := openJournal(path)
		if err != nil {
			return nil, err
		}
		s.j, s.st = j, st
	}
	if s.st.Handles == nil {
		s.st.Handles = map[string]*Handle{}
	}
	if s.st.Workspaces == nil {
		s.st.Workspaces = map[string]*Workspace{}
	}
	s.st.Version = 1
	s.seq = s.st.Seq
	s.byHash = map[string]string{}
	for id, w := range s.st.Workspaces {
		s.byHash[w.KeyHash] = id
		w.saved = w.Used
	}
	for _, h := range s.st.Handles {
		h.saved = h.Used
	}
	if s.j != nil {
		s.j.snapshot, s.j.written = s.snapshot, s.written
	}
	return s, nil
}

// close folds the journal into the snapshot (usage timestamps included).
func (s *store) close() error {
	if s.j == nil {
		return nil
	}
	return s.j.close()
}

// snapshot deep-copies the state for a compaction (values, not pointers:
// the live entries keep changing).
func (s *store) snapshot() *state {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &state{Version: 1, Seq: s.seq, Handles: make(map[string]*Handle, len(s.st.Handles)),
		Workspaces: make(map[string]*Workspace, len(s.st.Workspaces))}
	for id, h := range s.st.Handles {
		v := *h
		c.Handles[id] = &v
	}
	for id, w := range s.st.Workspaces {
		v := *w
		c.Workspaces[id] = &v
	}
	return c
}

// written records that a snapshot reached the disk: its usage timestamps
// are durable now.
func (s *store) written(c *state) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, h := range c.Handles {
		if cur := s.st.Handles[id]; cur != nil && cur.saved < h.Used {
			cur.saved = h.Used
		}
	}
	for id, w := range c.Workspaces {
		if cur := s.st.Workspaces[id]; cur != nil && cur.saved < w.Used {
			cur.saved = w.Used
		}
	}
}

// Records (mu held): an entry's current value, or its deletion.

func (s *store) putH(id string) record {
	s.seq++
	h := s.st.Handles[id]
	h.saved = h.Used
	v := *h
	return record{Seq: s.seq, H: id, HV: &v}
}

func (s *store) delH(id string) record {
	s.seq++
	delete(s.st.Handles, id)
	return record{Seq: s.seq, H: id}
}

func (s *store) putW(id string) record {
	s.seq++
	w := s.st.Workspaces[id]
	w.saved = w.Used
	v := *w
	return record{Seq: s.seq, W: id, WV: &v}
}

func (s *store) delW(id string) record {
	s.seq++
	if w := s.st.Workspaces[id]; w != nil {
		delete(s.byHash, w.KeyHash)
	}
	delete(s.st.Workspaces, id)
	return record{Seq: s.seq, W: id}
}

// write makes records durable (called outside mu).
func (s *store) write(recs []record) error {
	if s.j == nil || len(recs) == 0 {
		return nil
	}
	return s.j.append(recs)
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

// sweepLocked drops what the retention rules call abandoned: handles no
// workspace ever pushed to, handles idle for long, workspace keys never
// used, and workspace keys unused for long (a key used once is not kept
// forever: xbind checks its key daily, so a live workspace never idles).
// full: the store is at capacity (sweep now, but at most once a minute
// under a flood).
func (s *store) sweepLocked(now time.Time, full bool) []record {
	since := now.Sub(s.lastSweep)
	if since < time.Minute || (!full && since < sweepEvery) {
		return nil
	}
	s.lastSweep = now
	var recs []record
	t := now.Unix()
	unbound, idle := int64(s.lim.unboundTTL/time.Second), int64(s.lim.idleTTL/time.Second)
	for id, h := range s.st.Handles {
		if (h.Workspace == "" && t-h.Created > unbound) || t-max(h.Used, h.Created) > idle {
			recs = append(recs, s.delH(id))
		}
	}
	unused, idleWS := int64(s.lim.unusedWorkspace/time.Second), int64(s.lim.idleWorkspace/time.Second)
	for id, w := range s.st.Workspaces {
		if (w.Used == 0 && t-w.Created > unused) || (w.Used != 0 && t-w.Used > idleWS) {
			recs = append(recs, s.delW(id))
		}
	}
	return recs
}

// newHandle stores a fresh handle for token.
func (s *store) newHandle(token, topic, env string, now time.Time) (string, error) {
	s.mu.Lock()
	recs := s.sweepLocked(now, false)
	if len(s.st.Handles) >= s.lim.maxHandles {
		recs = append(recs, s.sweepLocked(now, true)...)
		if len(s.st.Handles) >= s.lim.maxHandles {
			s.mu.Unlock()
			_ = s.write(recs)
			return "", errFull
		}
	}
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
			recs = append(recs, s.delH(id))
		}
	}
	id := randomID(16)
	s.st.Handles[id] = &Handle{Token: token, Topic: topic, Env: env, Created: now.Unix()}
	recs = append(recs, s.putH(id))
	s.mu.Unlock()
	return id, s.write(recs)
}

// repoint changes the device token behind an existing handle (the app's
// APNs token changed); the workspace binding stays.
func (s *store) repoint(id, token, topic, env string) error {
	s.mu.Lock()
	h := s.st.Handles[id]
	if h == nil {
		s.mu.Unlock()
		return errNoHandle
	}
	h.Token, h.Topic, h.Env = token, topic, env
	rec := s.putH(id)
	s.mu.Unlock()
	return s.write([]record{rec})
}

func (s *store) deleteHandle(id string) bool {
	s.mu.Lock()
	if _, ok := s.st.Handles[id]; !ok {
		s.mu.Unlock()
		return false
	}
	rec := s.delH(id)
	s.mu.Unlock()
	_ = s.write([]record{rec})
	return true
}

// dropToken removes every handle of a device token APNs called dead.
func (s *store) dropToken(token string) int {
	s.mu.Lock()
	var recs []record
	for id, h := range s.st.Handles {
		if h.Token == token {
			recs = append(recs, s.delH(id))
		}
	}
	s.mu.Unlock()
	_ = s.write(recs)
	return len(recs)
}

// newWorkspace mints a workspace id and key; only the key's hash is kept.
func (s *store) newWorkspace(now time.Time) (id, key string, err error) {
	s.mu.Lock()
	recs := s.sweepLocked(now, false)
	if len(s.st.Workspaces) >= s.lim.maxWorkspaces {
		recs = append(recs, s.sweepLocked(now, true)...)
		if len(s.st.Workspaces) >= s.lim.maxWorkspaces {
			s.mu.Unlock()
			_ = s.write(recs)
			return "", "", errFull
		}
	}
	id = randomID(12)
	key = "xbr_" + randomID(32)
	h := hashKey(key)
	s.st.Workspaces[id] = &Workspace{KeyHash: h, Created: now.Unix()}
	s.byHash[h] = id
	recs = append(recs, s.putW(id))
	s.mu.Unlock()
	return id, key, s.write(recs)
}

// workspaceFor resolves a key to its workspace id and marks it used.
func (s *store) workspaceFor(key string, now time.Time) (string, error) {
	if key == "" {
		return "", errBadWorkspace
	}
	h := hashKey(key)
	s.mu.Lock()
	id, ok := s.byHash[h]
	w := s.st.Workspaces[id]
	if !ok || w == nil || subtle.ConstantTimeCompare([]byte(w.KeyHash), []byte(h)) != 1 {
		s.mu.Unlock()
		return "", errBadWorkspace
	}
	w.Used = now.Unix()
	var recs []record
	if w.Used-w.saved > int64(usedEvery/time.Second) {
		recs = append(recs, s.putW(id))
	}
	s.mu.Unlock()
	_ = s.write(recs) // best-effort: a lost timestamp only ages the entry
	return id, nil
}

// target resolves a handle for a push from workspace ws, binding an unbound
// handle to it (first use). Returns a copy.
func (s *store) target(id, ws string, now time.Time) (Handle, error) {
	s.mu.Lock()
	h := s.st.Handles[id]
	if h == nil {
		s.mu.Unlock()
		return Handle{}, errNoHandle
	}
	bind := false
	switch h.Workspace {
	case ws:
	case "":
		h.Workspace, bind = ws, true
	default:
		s.mu.Unlock()
		return Handle{}, errHandleBound
	}
	h.Used = now.Unix()
	var recs []record
	if bind || h.Used-h.saved > int64(usedEvery/time.Second) {
		recs = append(recs, s.putH(id))
	}
	out := *h
	s.mu.Unlock()
	if err := s.write(recs); err != nil && bind {
		return Handle{}, err // the binding is durable before the first push goes out
	}
	return out, nil
}

func (s *store) counts() (handles, workspaces int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.st.Handles), len(s.st.Workspaces)
}
