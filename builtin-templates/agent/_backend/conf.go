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
// conf is readable by everyone who can open the agent (their frame reads it
// through xbind's kv API), so it holds nothing a manager keeps from people:
// the config is mirrored as its viewers may see it, and a static MCP server
// with headers (they can carry tokens) is left out altogether — people's
// conversations don't use it; the global instance does.
//
// conf's keys:
//
//	halt           {"halt": "1" | "", "at"} — its own small key, written first
//	settings       {"config", "classes", "skills": <rev of the shared skills>, "at"}
//	skill:<name>   one shared skill (the Skill JSON)
//
// A person's partition fails closed: until it has read both keys once (conf
// not written yet, a kv error at start, conf missing from uses) the halt
// switch reads as on — brake.go parks the runs and looks again — so a
// manager's halt or classes never silently don't apply.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// mirroredSettings are the settings rows global mirrors and a person's
// partition reads from conf instead of its own db.
var mirroredSettings = map[string]bool{"config": true, "halt": true, "classes": true}

const (
	confHaltKey     = "halt"
	confSettingsKey = "settings"
	confSkillPrefix = "skill:"
	// confMaxValue is the most a conf value may hold (xbind's kv cuts values
	// at 1 MiB): a config or a shared skill bigger than this is refused when
	// saved in a partitioned agent (confTooBig).
	confMaxValue = 900 << 10
)

// errSharedSetting refuses a person's partition writing a tile-wide setting:
// those are the global instance's (partition_routes.go forwards a manager's).
var errSharedSetting = errors.New("the agent's settings are kept by its shared instance: change them there")

// confSnap is the settings key.
type confSnap struct {
	Config  string `json:"config"`
	Classes string `json:"classes"`
	Skills  string `json:"skills"` // a revision of the shared skills (skillsRev)
	At      int64  `json:"at"`
}

// confHalt is the halt key.
type confHalt struct {
	Halt string `json:"halt"`
	At   int64  `json:"at"`
}

// The mode's hooks: confOut is global's mirror, confIn() a person's reader.
// Both nil in legacy mode (and in tests that don't set them). The reader is
// published atomically: every engine's project worker reads it on each pass,
// and a reader is stored whole (its hooks set before Store).
var (
	confOut *confMirror
	confInP atomic.Pointer[confReader]
)

// confIn is a person's conf reader, nil outside a person's partition.
func confIn() *confReader { return confInP.Load() }

// confSetting answers a mirrored setting from conf in a person's partition;
// inTx: the caller holds a transaction, which never waits on the network.
func confSetting(k string, inTx bool) (string, bool) {
	if confIn() == nil || !mirroredSettings[k] {
		return "", false
	}
	return confIn().view(!inTx).get(k), true
}

// confRefuses is the settings write hook: a person's partition may not write
// a mirrored setting.
func confRefuses(k string) error {
	if confIn() != nil && mirroredSettings[k] {
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

// confTooBig: in a partitioned agent a tile-wide value (the config, a shared
// skill) must fit a conf value.
func confTooBig(raw []byte) bool { return partitioned() && len(raw) > confMaxValue }

// confClassesRaw is the classes setting a person's partition last loaded.
var confClassesRaw atomic.Pointer[string]

// refreshConfClasses reloads the class cache when conf's classes changed: a
// manager's edit reaches every person's partition within confTTL. It never
// waits on the network (classes are looked up inside transactions).
func refreshConfClasses() {
	raw := confIn().view(false).Classes
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
func sharedSkill(name string, block bool, notFound error) (*Skill, error) {
	for _, s := range confIn().sharedSkills(block) {
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

	mu       sync.Mutex // one sync at a time
	written  map[string]string
	withheld string // the static MCP servers last left out (logged when it changes)
	retry    *time.Timer
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

// sync writes the halt key first (a big config or a refused skill never
// holds the brake back), then the shared skills (changed ones, removing the
// gone), then the settings key naming their revision — a reader that sees a
// new revision finds every skill of it already there. A skill that can't be
// written is logged and left out of the revision, and the rest still goes
// out; the error it returns arms the retry.
func (m *confMirror) sync(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var errs []error
	now := time.Now().UnixMilli()
	halt, _ := json.Marshal(confHalt{Halt: m.db.getSetting("halt"), At: now})
	if err := m.put(ctx, confHaltKey, halt, false); err != nil {
		errs = append(errs, err)
	}
	var mirrored []*Skill
	have, err := m.kv.List(ctx, confSkillPrefix)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	want := map[string]bool{}
	for _, s := range sharedSkillsOf(m.db) {
		key := confSkillPrefix + s.Name
		want[key] = true
		raw, _ := json.Marshal(s)
		if err := m.put(ctx, key, raw, true); err != nil {
			logf("conf mirror: shared skill %q not mirrored: %v", s.Name, err)
			errs = append(errs, err)
			continue
		}
		mirrored = append(mirrored, s)
	}
	for _, key := range have {
		if !want[key] {
			if err := m.kv.Delete(ctx, key); err != nil {
				errs = append(errs, err)
				continue
			}
			delete(m.written, key)
		}
	}
	cfg, withheld := confConfig(m.db.getSetting("config"))
	if w := strings.Join(withheld, ", "); w != m.withheld {
		m.withheld = w
		if w != "" {
			logf("conf mirror: static MCP servers with headers stay with the shared instance (people's conversations don't get them): %s", w)
		}
	}
	snap, _ := json.Marshal(confSnap{Config: cfg, Classes: m.db.getSetting("classes"), Skills: skillsRev(mirrored), At: now})
	if err := m.put(ctx, confSettingsKey, snap, false); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// put writes key unless it already holds raw (cache: skills only — the
// settings and halt keys carry a time and are written at every sync).
func (m *confMirror) put(ctx context.Context, key string, raw []byte, cache bool) error {
	if cache && m.written[key] == string(raw) {
		return nil
	}
	if err := m.kv.Put(ctx, key, raw); err != nil {
		return err
	}
	if cache {
		m.written[key] = string(raw)
	}
	return nil
}

// confConfig is the config as conf carries it: as its viewers may see it
// (forView), and without the static MCP servers that have headers, whose
// names it returns. "" stays "" (the defaults).
func confConfig(raw string) (string, []string) {
	if raw == "" {
		return "", nil
	}
	cfg := parseConfig(raw)
	var keep []MCPServer
	var withheld []string
	for _, s := range cfg.MCP {
		if len(s.Headers) > 0 {
			withheld = append(withheld, s.Name)
			continue
		}
		keep = append(keep, s)
	}
	cfg.MCP = keep
	b, _ := json.Marshal(cfg.forView())
	return string(b), withheld
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

// confState is what a person's partition knows of conf.
type confState int

const (
	confPending confState = iota // not read yet (global hasn't written it, or the kv failed): fail closed
	confKnown                    // both keys read: the settings are global's
	confMissing                  // conf isn't in xbin.json's uses: fail closed for good
)

// confView is one read of conf as the partition uses it.
type confView struct {
	confSnap
	Halt  string
	State confState
}

// get is a mirrored setting: the halt reads as on while conf isn't known.
func (v confView) get(k string) string {
	switch k {
	case "config":
		return v.Config
	case "classes":
		return v.Classes
	case "halt":
		if v.State != confKnown {
			return "1"
		}
		return v.Halt
	}
	return ""
}

// confReader reads conf for a person's partition, caching it for confTTL so a
// busy turn doesn't ask xbind at every step; invalidate after a forwarded
// write. A read that fails keeps the last good copy; with none, the halt
// reads as on and the global instance is woken once in a while (it writes
// conf when it starts). A caller inside a transaction never waits: it gets
// the cached copy while a refresh runs beside it.
type confReader struct {
	kv      kvStore
	wake    func() // starts the global instance (F5 GET /health); nil in tests
	missing bool   // conf isn't granted (emptyKV): nothing will ever be read

	// onHaltOff runs (in its own goroutine) when the brake as this partition
	// reads it comes off — conf's halt lifted, or conf read for the first
	// time: the runs parked on it get their pass (brake.go).
	onHaltOff func()
	// parked: this process has runs waiting on the brake (brake.go's watch
	// stops when none do).
	parked func() bool

	mu       sync.Mutex
	snap     confSnap
	halt     string
	state    confState
	at       time.Time // the last read (good or not)
	fetching bool      // an async refresh is under way
	lastHalt string    // the brake as last read
	skills   []*Skill
	skillRev string
	wokeAt   time.Time
	watch    *time.Timer // brake.go's watch while runs are parked
	watchGap time.Duration
}

var confTTL = 3 * time.Second

func newConfReader(kv kvStore, wake func()) *confReader {
	_, missing := kv.(emptyKV)
	c := &confReader{kv: kv, wake: wake, missing: missing, lastHalt: "1"}
	if missing {
		c.state = confMissing
	}
	return c
}

func (c *confReader) invalidate() {
	c.mu.Lock()
	c.at = time.Time{}
	c.mu.Unlock()
}

func (c *confReader) viewLocked() confView {
	return confView{confSnap: c.snap, Halt: c.halt, State: c.state}
}

// view is conf fresh within confTTL — reading it now when stale and block,
// else the cached copy with a refresh started beside it.
func (c *confReader) view(block bool) confView {
	c.mu.Lock()
	if c.missing || (!c.at.IsZero() && time.Since(c.at) < confTTL) {
		v := c.viewLocked()
		c.mu.Unlock()
		return v
	}
	if !block {
		if !c.fetching {
			c.fetching = true
			go c.refresh()
		}
		v := c.viewLocked()
		c.mu.Unlock()
		return v
	}
	c.mu.Unlock()
	c.refresh()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.viewLocked()
}

// setting is one mirrored setting, read as a request outside a transaction
// reads it.
func (c *confReader) setting(k string) string { return c.view(true).get(k) }

// refresh reads both keys now.
func (c *confReader) refresh() {
	if c.missing {
		return
	}
	ctx := context.Background()
	hraw, hok, herr := c.kv.Get(ctx, confHaltKey)
	sraw, sok, serr := c.kv.Get(ctx, confSettingsKey)
	c.mu.Lock()
	c.at, c.fetching = time.Now(), false
	switch {
	case herr != nil || serr != nil:
		logf("conf: %v (keeping the last settings read)", errors.Join(herr, serr))
	case !hok || !sok:
		c.state = confPending // global hasn't written conf (yet, or since a wipe)
		if c.wake != nil && time.Since(c.wokeAt) > 5*time.Minute {
			c.wokeAt = time.Now()
			go c.wake()
		}
	default:
		var h confHalt
		var s confSnap
		if json.Unmarshal(hraw, &h) == nil && json.Unmarshal(sraw, &s) == nil {
			c.snap, c.halt, c.state = s, h.Halt, confKnown
		} else {
			logf("conf: unreadable settings (keeping the last read)")
		}
	}
	brake := c.viewLocked().get("halt")
	off := c.lastHalt == "1" && brake != "1"
	c.lastHalt = brake
	if off && c.watch != nil {
		c.watch.Stop()
		c.watch = nil
	}
	fire := off && c.onHaltOff != nil
	c.mu.Unlock()
	if fire {
		go c.onHaltOff()
	}
}

// sharedSkills is the shared skills of the snapshot's revision (read again
// only when it changes; inside a transaction, the last read).
func (c *confReader) sharedSkills(block bool) []*Skill {
	rev := c.view(block).Skills
	c.mu.Lock()
	if rev == c.skillRev || !block {
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
