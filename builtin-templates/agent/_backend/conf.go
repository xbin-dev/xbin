// conf.go — a partitioned agent's tile-wide settings (API.md "Partitioned
// instances"; docs/partitions.md §Shared resources). The config (models, features,
// limits), the classes (D116), the halt switch and the shared skills live in
// the global instance's db, where managers change them — a manager in their
// own partition changes them through the global instance
// (partition_routes.go). The global instance mirrors them into the `conf`
// resource (kv, "shared": "read" in scope.json), which people's partitions
// read at each use and can't write. An unpartitioned instance never touches
// conf: the hooks below are nil there, and settings stay in its db as ever.
//
// conf's keys:
//
//	settings       {"config", "halt", "classes", "skills": <rev of the shared skills>, "at"}
//	skill:<name>   one shared skill (the Skill JSON)
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// mirroredSettings are the settings rows global mirrors and a person's
// partition reads from conf instead of its own db.
var mirroredSettings = map[string]bool{"config": true, "halt": true, "classes": true}

const (
	confSettingsKey = "settings"
	confSkillPrefix = "skill:"
)

// errSharedSetting refuses a person's partition writing a tile-wide setting:
// those are the global instance's (partition_routes.go forwards a manager's).
var errSharedSetting = errors.New("the agent's settings are kept by its shared instance: change them there")

// confSnap is the settings key.
type confSnap struct {
	Config  string `json:"config"`
	Halt    string `json:"halt"`
	Classes string `json:"classes"`
	Skills  string `json:"skills"` // a revision of the shared skills (skillsRev)
	At      int64  `json:"at"`
}

func (s confSnap) get(k string) string {
	switch k {
	case "config":
		return s.Config
	case "halt":
		return s.Halt
	case "classes":
		return s.Classes
	}
	return ""
}

// The mode's hooks: confOut is global's mirror, confIn a person's reader.
// Both nil in legacy mode (and in tests that don't set them).
var (
	confOut *confMirror
	confIn  *confReader
)

// confSetting answers a mirrored setting from conf in a person's partition.
func confSetting(k string) (string, bool) {
	if confIn == nil || !mirroredSettings[k] {
		return "", false
	}
	return confIn.setting(k), true
}

// confRefuses is the settings write hook: a person's partition may not write
// a mirrored setting.
func confRefuses(k string) error {
	if confIn != nil && mirroredSettings[k] {
		return errSharedSetting
	}
	return nil
}

// confWrote: global mirrors a mirrored setting once its write commits.
func confWrote(d *DB, k string) {
	if confOut != nil && mirroredSettings[k] {
		d.AfterCommit(confOut.kick)
	}
}

// confSkillsChanged is the skills write hook: global mirrors after the commit.
func confSkillsChanged(d *DB) {
	if confOut != nil {
		d.AfterCommit(confOut.kick)
	}
}

// confClassesRaw is the classes setting a person's partition last loaded.
var confClassesRaw atomic.Pointer[string]

// refreshConfClasses reloads the class cache when conf's classes changed: a
// manager's edit reaches every person's partition within confTTL.
func refreshConfClasses() {
	raw := confIn.setting("classes")
	if prev := confClassesRaw.Load(); prev != nil && *prev == raw && classStore.Load() != nil {
		return
	}
	var s classSettings
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &s)
	}
	classStore.Store(newClassState(s))
	confClassesRaw.Store(&raw)
}

// withShared is a person's skills and, beside them, the shared ones from conf
// — their own first where a name is both (a shared one saved after theirs).
func withShared(own, shared []*Skill) []*Skill {
	seen := map[string]bool{}
	for _, s := range own {
		seen[s.Name] = true
	}
	out := append([]*Skill(nil), own...)
	for _, s := range shared {
		if !seen[s.Name] {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// sharedSkill is a shared skill by name from conf; notFound when conf has
// none.
func sharedSkill(name string, notFound error) (*Skill, error) {
	for _, s := range confIn.sharedSkills() {
		if s.Name == name {
			c := *s
			return &c, nil
		}
	}
	return nil, notFound
}

// --- global: the mirror -------------------------------------------------------------

// confMirror writes global's tile-wide settings into conf: at start and after
// every change of one. A failed write is retried every confRetry until one
// succeeds (people's partitions keep the last good copy meanwhile).
type confMirror struct {
	kv kvStore
	db *DB

	mu      sync.Mutex // one sync at a time
	written map[string]string
	retry   *time.Timer
}

var confRetry = 30 * time.Second

func newConfMirror(kv kvStore, db *DB) *confMirror {
	return &confMirror{kv: kv, db: db, written: map[string]string{}}
}

// kick syncs now (the caller's goroutine, bounded by the calls' timeouts),
// arming a retry when it fails.
func (m *confMirror) kick() {
	if err := m.sync(context.Background()); err != nil {
		logf("conf mirror: %v — retrying in %v", err, confRetry)
		m.mu.Lock()
		if m.retry == nil {
			m.retry = time.AfterFunc(confRetry, func() {
				m.mu.Lock()
				m.retry = nil
				m.mu.Unlock()
				m.kick()
			})
		}
		m.mu.Unlock()
	}
}

// sync writes the shared skills (changed ones, removing the gone), then the
// settings key naming their revision — a reader that sees a new revision
// finds every skill of it already there.
func (m *confMirror) sync(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	skills := sharedSkillsOf(m.db)
	have, err := m.kv.List(ctx, confSkillPrefix)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, s := range skills {
		key := confSkillPrefix + s.Name
		want[key] = true
		raw, _ := json.Marshal(s)
		if m.written[key] == string(raw) {
			continue
		}
		if err := m.kv.Put(ctx, key, raw); err != nil {
			return err
		}
		m.written[key] = string(raw)
	}
	for _, key := range have {
		if !want[key] {
			if err := m.kv.Delete(ctx, key); err != nil {
				return err
			}
			delete(m.written, key)
		}
	}
	snap := confSnap{Config: m.db.getSetting("config"), Halt: m.db.getSetting("halt"),
		Classes: m.db.getSetting("classes"), Skills: skillsRev(skills), At: time.Now().UnixMilli()}
	raw, _ := json.Marshal(snap)
	return m.kv.Put(ctx, confSettingsKey, raw)
}

// sharedSkillsOf is db's shared skills (owner ""), by name.
func sharedSkillsOf(d *DB) []*Skill {
	all, _ := d.localSkills()
	var out []*Skill
	for _, s := range all {
		if s.Owner == "" {
			out = append(out, s)
		}
	}
	return out
}

// skillsRev names a set of shared skills: it changes whenever one is added,
// removed or edited.
func skillsRev(skills []*Skill) string {
	h := sha256.New()
	for _, s := range skills {
		raw, _ := json.Marshal(s)
		h.Write(raw)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// --- a person's partition: the reader ----------------------------------------------------

// confReader reads conf for a person's partition, caching it for confTTL so a
// busy turn doesn't ask xbind at every step; invalidate after a forwarded
// write. A read that fails keeps the last good copy; with none, the settings
// read as unset (the defaults) and the global instance is woken once in a
// while, since it writes conf when it starts.
type confReader struct {
	kv   kvStore
	wake func() // starts the global instance (F5 GET /health); nil in tests

	mu       sync.Mutex
	snap     confSnap
	at       time.Time
	have     bool
	skills   []*Skill
	skillRev string
	wokeAt   time.Time
}

var confTTL = 3 * time.Second

func newConfReader(kv kvStore, wake func()) *confReader { return &confReader{kv: kv, wake: wake} }

func (c *confReader) invalidate() {
	c.mu.Lock()
	c.at = time.Time{}
	c.mu.Unlock()
}

// current is the settings snapshot, fresh within confTTL.
func (c *confReader) current() confSnap {
	c.mu.Lock()
	if !c.at.IsZero() && time.Since(c.at) < confTTL {
		s := c.snap
		c.mu.Unlock()
		return s
	}
	c.mu.Unlock()
	raw, ok, err := c.kv.Get(context.Background(), confSettingsKey)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = time.Now()
	switch {
	case err != nil:
		logf("conf: %v (keeping the last settings read)", err)
	case !ok:
		c.snap, c.have = confSnap{}, false
		if c.wake != nil && time.Since(c.wokeAt) > 5*time.Minute {
			c.wokeAt = time.Now()
			go c.wake()
		}
	default:
		var s confSnap
		if json.Unmarshal(raw, &s) == nil {
			c.snap, c.have = s, true
		}
	}
	return c.snap
}

func (c *confReader) setting(k string) string { return c.current().get(k) }

// sharedSkills is the shared skills of the snapshot's revision (read again
// only when it changes).
func (c *confReader) sharedSkills() []*Skill {
	rev := c.current().Skills
	c.mu.Lock()
	if rev == c.skillRev {
		out := c.skills
		c.mu.Unlock()
		return out
	}
	c.mu.Unlock()
	ctx := context.Background()
	keys, err := c.kv.List(ctx, confSkillPrefix)
	if err != nil {
		logf("conf skills: %v (keeping the last read)", err)
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.skills
	}
	var out []*Skill
	for _, k := range keys {
		raw, ok, err := c.kv.Get(ctx, k)
		if err != nil || !ok {
			continue
		}
		var s Skill
		if json.Unmarshal(raw, &s) == nil && s.Name != "" {
			s.Owner = "" // conf holds shared skills only
			out = append(out, &s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	c.mu.Lock()
	c.skills, c.skillRev = out, rev
	c.mu.Unlock()
	return out
}
