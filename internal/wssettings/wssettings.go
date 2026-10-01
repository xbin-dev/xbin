// Package wssettings is the workspace settings: the workspace-wide switches
// an admin sets from the admin console's workspace → settings tab or `bx
// settings` (D175, D180). One xbind-owned JSON object,
// data/workspace-settings.json, holds them all, grouped by topic:
//
//   - terminals: baseAutoUpdate (D175) — a tile's terminal layer built on
//     an older base image moves to the current base at its next session
//     start. On by default.
//   - partitioned tiles (PD-55): partitionConsent — another partitioned
//     tile reaches a person's data only with their consent;
//     credentialResetConfirm — an admin-set credential for someone who
//     holds partitions waits for them. Both off by default.
//
// Each setting has a default for when its key is absent, so a missing (or
// empty) file is every default. A write replaces only the keys it sets and
// keeps every other key the file holds (a newer xbind's), and an xbind that
// predates a key never reads it, so a downgrade ignores it and an upgrade
// back finds it as it was left. The file is outside the vault (readable
// while it is sealed) and under data/, which no sandbox mounts. Each
// setting is read by its exact key.
//
// A setting whose value can't be read (the file unreadable or not a JSON
// object, a value not true or false) never does what a guess might undo:
// base auto-update reads off (a layer keeps its base: nothing discarded),
// and a partitioned tiles' switch — a protection — keeps the last value this
// store read, or is on when it has read none; a key spelled in another case
// makes a protection unreadable too (encoding/json would match it, and a
// write would put the right key beside it). Load says which and why; Apply
// never overwrites a file with such a problem.
//
// Before D180 the two partitioned tiles' switches had a file of their own,
// data/workspace-policies.json (v0.3.66). It stays readable for a
// downgrade: every write of a partitioned tiles' switch also writes both of
// them into it (keeping its other keys), and the settings file records the
// SHA-256 of the bytes it holds the switches of (policiesFileSha256). When
// that file is not the one recorded — the first read after the upgrade, or
// an older xbind (or a hand) changed it since — its switches are imported
// into the settings file on the read, once: idempotent, and what an older
// xbind set while it ran is what an upgrade back finds. A file to import that
// can't be read makes both switches unreadable until it is fixed or removed.
package wssettings

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// The file's keys.
const (
	// KeyBaseAutoUpdate: a tile's terminal layer built on an older base
	// image moves to the current base at its next session start (D175).
	KeyBaseAutoUpdate = "baseAutoUpdate"
	// KeyPartitionConsent: another partitioned tile reaches a person's data
	// only with that person's consent (PD-55; plans/partitions 05 §2).
	KeyPartitionConsent = "partitionConsent"
	// KeyCredentialResetConfirm: an admin-set credential for a person
	// holding partitions takes effect only after they confirm, or 24 h
	// after they were notified (PD-55; 06 §9).
	KeyCredentialResetConfirm = "credentialResetConfirm"
	// keyPoliciesSum: the SHA-256 (hex) of the data/workspace-policies.json
	// this file holds the switches of — as last imported or written (D180).
	keyPoliciesSum = "policiesFileSha256"
)

// The settings' groups: the admin console's sections, bx settings' headings.
const (
	GroupTerminals  = "terminals"
	GroupPartitions = "partitions"
)

// PoliciesFile is the partitioned tiles' switches' file before D180, beside
// the settings file; PoliciesSchema the schema written into it.
const (
	PoliciesFile   = "workspace-policies.json"
	PoliciesSchema = 1
)

// Settings is the effective settings: the file's, defaults filled in.
type Settings struct {
	BaseAutoUpdate         bool `json:"baseAutoUpdate"`
	PartitionConsent       bool `json:"partitionConsent"`
	CredentialResetConfirm bool `json:"credentialResetConfirm"`
}

// Defaults is what a missing key means.
func Defaults() Settings { return Settings{BaseAutoUpdate: true} }

// Patch is a partial update: each present key replaces its setting, an
// absent one leaves it alone.
type Patch struct {
	BaseAutoUpdate         *bool `json:"baseAutoUpdate"`
	PartitionConsent       *bool `json:"partitionConsent"`
	CredentialResetConfirm *bool `json:"credentialResetConfirm"`
}

// Setting describes one setting.
type Setting struct {
	Key     string // the file's and the wire's key
	Name    string // bx's name
	Group   string // GroupTerminals, GroupPartitions
	Default bool
	// Protection: a value that can't be read keeps the last value read, or
	// is on (never quietly off); otherwise it reads off.
	Protection bool
	val        func(*Settings) *bool
	set        func(*Patch) **bool
}

// Of is the setting's value in s.
func (st Setting) Of(s Settings) bool { return *st.val(&s) }

// In is the setting's value in p, nil when p leaves it alone.
func (st Setting) In(p Patch) *bool { return *st.set(&p) }

var all = []Setting{
	{Key: KeyBaseAutoUpdate, Name: "base-auto-update", Group: GroupTerminals, Default: true,
		val: func(s *Settings) *bool { return &s.BaseAutoUpdate }, set: func(p *Patch) **bool { return &p.BaseAutoUpdate }},
	{Key: KeyPartitionConsent, Name: "partition-consent", Group: GroupPartitions, Protection: true,
		val: func(s *Settings) *bool { return &s.PartitionConsent }, set: func(p *Patch) **bool { return &p.PartitionConsent }},
	{Key: KeyCredentialResetConfirm, Name: "credential-reset-confirm", Group: GroupPartitions, Protection: true,
		val: func(s *Settings) *bool { return &s.CredentialResetConfirm }, set: func(p *Patch) **bool { return &p.CredentialResetConfirm }},
}

// All is every setting, in the order the console and bx show them.
func All() []Setting { return append([]Setting(nil), all...) }

// Keys is the keys of the settings in group ("" for all).
func Keys(group string) []string {
	var out []string
	for _, st := range all {
		if group == "" || st.Group == group {
			out = append(out, st.Key)
		}
	}
	return out
}

// Empty reports a patch that sets nothing.
func (p Patch) Empty() bool { return len(p.Keys()) == 0 }

// Keys is the keys p sets, in settings order.
func (p Patch) Keys() []string {
	var out []string
	for _, st := range all {
		if st.In(p) != nil {
			out = append(out, st.Key)
		}
	}
	return out
}

// Problems is why some settings can't be read: key → reason, the file
// named (never the host's path). Empty when every setting reads.
type Problems map[string]string

// Of is the first problem among keys (in settings order; any setting when
// none is named), or nil.
func (p Problems) Of(keys ...string) error {
	for _, st := range all {
		if why, bad := p[st.Key]; bad && (len(keys) == 0 || contains(keys, st.Key)) {
			return errors.New(why)
		}
	}
	return nil
}

func contains(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

// Store keeps the settings at one path, and the pre-D180 policies file
// beside it.
type Store struct {
	path, legacy string

	mu                 sync.Mutex
	loaded             bool
	stamp, legacyStamp fileStamp
	cur                Settings
	probs              Problems
	last               map[string]bool // each protection's last value read
	logged             string          // the problems last logged
	importErr          string          // why the last import couldn't be saved
}

// New is the store at path (data/workspace-settings.json).
func New(path string) *Store {
	return &Store{path: path, legacy: filepath.Join(filepath.Dir(path), PoliciesFile)}
}

// Path is the file the store keeps.
func (s *Store) Path() string { return s.path }

// rel names a file of the store in messages: workspace-relative.
func rel(path string) string { return "data/" + filepath.Base(path) }

// Load is what xbind applies — each setting as the file says, its default
// when absent, its fail-safe value when it can't be read — and why the
// ones that can't be read can't. It reads the files again only when they
// changed on disk, and imports the policies file when it isn't the one the
// settings file holds the switches of.
func (s *Store) Load() (Settings, Problems) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, serr := stampFile(s.path)
	lst, lerr := stampFile(s.legacy)
	if serr == nil && lerr == nil && s.loaded && st == s.stamp && lst == s.legacyStamp {
		return s.cur, s.probs
	}
	d := s.readLocked()
	if serr != nil {
		d.fail("", fmt.Sprintf("%s: %v", rel(s.path), hostless(serr)))
	}
	if lerr != nil {
		d.fail(GroupPartitions, fmt.Sprintf("%s: %v", rel(s.legacy), hostless(lerr)))
	}
	saved := true
	if d.imported {
		if err := s.writeLocked(s.path, d.raw); err != nil {
			saved = false // applied for now; the next read tries again
			if err.Error() != s.importErr {
				slog.Warn("workspace settings: can't save the switches imported from the policies file", "file", s.path, "err", err)
			}
			s.importErr = err.Error()
		} else {
			s.importErr = ""
			slog.Info("workspace settings: imported the partitioned tiles' switches from the policies file (D180)", "file", s.legacy,
				KeyPartitionConsent, d.vals.PartitionConsent, KeyCredentialResetConfirm, d.vals.CredentialResetConfirm)
		}
	}
	cur := s.resolveLocked(d)
	if msg := fmt.Sprint(d.probs); len(d.probs) > 0 && msg != s.logged {
		slog.Warn("workspace settings: a setting can't be read; it takes its fail-safe value (base auto-update off, a partitioned tiles' switch its last value or on)",
			"file", s.path, "problems", msg, KeyBaseAutoUpdate, cur.BaseAutoUpdate,
			KeyPartitionConsent, cur.PartitionConsent, KeyCredentialResetConfirm, cur.CredentialResetConfirm)
		s.logged = msg
	} else if len(d.probs) == 0 {
		s.logged = ""
	}
	// an import just written changed the file: the next read stamps it
	s.loaded = serr == nil && lerr == nil && saved && !d.imported
	s.stamp, s.legacyStamp, s.cur, s.probs = st, lst, cur, d.probs
	return cur, d.probs
}

// BaseAutoUpdate is the base auto-update setting as a session's start
// reads it (the terminal manager's hook): off while it can't be read — a
// layer then keeps its base, as it did before the setting existed — rather
// than discard anything on a guess.
func (s *Store) BaseAutoUpdate() bool {
	st, _ := s.Load()
	return st.BaseAutoUpdate
}

// Apply sets what p names, keeps the rest of the file as it is, writes it
// atomically and returns the settings before and after. A file with a
// setting Load can't read is left alone and the error returned: rewriting
// it could lose what it holds. A partitioned tiles' switch is written into
// the policies file too, for a downgrade.
func (s *Store) Apply(p Patch) (old, cur Settings, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.readLocked()
	if err := d.probs.Of(); err != nil {
		return d.vals, d.vals, fmt.Errorf("%w — not overwritten; fix or remove it by hand", err)
	}
	old, cur = d.vals, d.vals
	mirror := false
	for _, st := range all {
		if v := st.In(p); v != nil {
			*st.val(&cur) = *v
			d.raw[st.Key] = json.RawMessage(fmt.Sprint(*v))
			mirror = mirror || st.Group == GroupPartitions
		}
	}
	if mirror {
		// the policies file first: once it is written, the settings file's
		// record of it is what makes the two agree (a failed write of the
		// settings file leaves the policies file to be imported again)
		sum, err := s.mirrorLocked(cur)
		if err != nil {
			return old, old, err
		}
		d.raw[keyPoliciesSum], _ = json.Marshal(sum)
	}
	defer func() { s.loaded = false }() // the next read stamps what was written
	if err := s.writeLocked(s.path, d.raw); err != nil {
		return old, old, err
	}
	for _, st := range all {
		if st.Protection {
			s.lastLocked()[st.Key] = st.Of(cur)
		}
	}
	return old, cur, nil
}

// doc is one read of the settings file (and the policies file it imports).
type doc struct {
	raw      map[string]json.RawMessage // every key; nil when the file can't be read
	vals     Settings                   // the settings read, defaults for the rest
	probs    Problems
	imported bool // raw took the policies file's switches: to be written back
}

// fail marks every setting of group ("" every setting) unreadable.
func (d *doc) fail(group, why string) {
	for _, st := range all {
		if group == "" || st.Group == group {
			d.probs[st.Key] = why
		}
	}
}

// readLocked reads the settings file and, when it reads cleanly, imports a
// policies file it doesn't hold the switches of yet.
func (s *Store) readLocked() doc {
	d := doc{vals: Defaults(), probs: Problems{}}
	raw, err := readObject(s.path)
	if err != nil {
		d.fail("", err.Error())
		return d
	}
	d.raw = raw
	for _, st := range all {
		if why := readBool(raw, st, &d.vals); why != "" {
			d.probs[st.Key] = fmt.Sprintf("%s: %s", rel(s.path), why)
		}
	}
	if len(d.probs) == 0 {
		s.importLocked(&d)
	}
	return d
}

// importLocked takes the policies file's switches into d when the settings
// file doesn't record holding them (its bytes' SHA-256).
func (s *Store) importLocked(d *doc) {
	b, err := os.ReadFile(s.legacy)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		d.fail(GroupPartitions, fmt.Sprintf("%s: %v", rel(s.legacy), hostless(err)))
		return
	}
	sum := sha256Hex(b)
	var held string
	if json.Unmarshal(d.raw[keyPoliciesSum], &held) == nil && held == sum {
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil || raw == nil { // empty included, as it always was
		d.fail(GroupPartitions, fmt.Sprintf("%s isn't a JSON object (fix or remove it)", rel(s.legacy)))
		return
	}
	vals := Settings{}
	for _, st := range all {
		if st.Group != GroupPartitions {
			continue
		}
		if why := readBool(raw, st, &vals); why != "" {
			d.fail(GroupPartitions, fmt.Sprintf("%s: %s", rel(s.legacy), why))
			return
		}
	}
	for _, st := range all {
		if st.Group == GroupPartitions {
			v := st.Of(vals)
			*st.val(&d.vals) = v
			d.raw[st.Key] = json.RawMessage(fmt.Sprint(v))
		}
	}
	d.raw[keyPoliciesSum], _ = json.Marshal(sum)
	d.imported = true
}

// mirrorLocked writes the partitioned tiles' switches of cur into the
// policies file, keeping its other keys and a newer schema number, and
// returns the SHA-256 of what it wrote.
func (s *Store) mirrorLocked(cur Settings) (string, error) {
	raw := map[string]json.RawMessage{}
	if b, err := os.ReadFile(s.legacy); err == nil && len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &raw); err != nil || raw == nil {
			raw = map[string]json.RawMessage{} // only reached for a file just imported or written: it reads
		}
	}
	var schema int
	if json.Unmarshal(raw["schema"], &schema) != nil || schema < PoliciesSchema {
		raw["schema"] = json.RawMessage(fmt.Sprint(PoliciesSchema))
	}
	for _, st := range all {
		if st.Group == GroupPartitions {
			raw[st.Key] = json.RawMessage(fmt.Sprint(st.Of(cur)))
		}
	}
	b, err := marshal(raw)
	if err != nil {
		return "", err
	}
	if err := fsutil.WriteFileAtomicIn(s.legacy, b, 0o644); err != nil {
		return "", fmt.Errorf("can't save %s: %w", rel(s.legacy), hostless(err))
	}
	return sha256Hex(b), nil
}

func (s *Store) writeLocked(path string, raw map[string]json.RawMessage) error {
	b, err := marshal(raw)
	if err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomicIn(path, b, 0o644); err != nil {
		return fmt.Errorf("can't save %s: %w", rel(path), hostless(err))
	}
	return nil
}

func (s *Store) lastLocked() map[string]bool {
	if s.last == nil {
		s.last = map[string]bool{}
	}
	return s.last
}

// resolveLocked is what xbind applies after a read: each setting as read,
// or, when it can't be read, its fail-safe value — off for base
// auto-update, the last value read (or on) for a protection.
func (s *Store) resolveLocked(d doc) Settings {
	out := d.vals
	last := s.lastLocked()
	for _, st := range all {
		_, bad := d.probs[st.Key]
		switch {
		case bad && st.Protection:
			v, seen := last[st.Key]
			*st.val(&out) = !seen || v
		case bad:
			*st.val(&out) = false
		case st.Protection:
			last[st.Key] = st.Of(out)
		}
	}
	return out
}

// readObject is the file's top-level object, every key raw; an empty map
// when there is no file or it is empty.
func readObject(path string) (map[string]json.RawMessage, error) {
	doc := map[string]json.RawMessage{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel(path), hostless(err))
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s isn't a JSON object (fix or remove it): %w", rel(path), err)
	}
	if doc == nil { // the file says null
		return nil, fmt.Errorf("%s isn't a JSON object (fix or remove it)", rel(path))
	}
	return doc, nil
}

// readBool reads st from raw into out by its exact key; the reason it
// can't, or "". An absent key (and, for a setting that isn't a protection,
// null) leaves the default; a protection's key spelled in another case is
// a problem.
func readBool(raw map[string]json.RawMessage, st Setting, out *Settings) string {
	if st.Protection {
		for k := range raw {
			if k != st.Key && strings.EqualFold(k, st.Key) {
				return fmt.Sprintf("the key %q should be spelled %q", k, st.Key)
			}
		}
	}
	v, ok := raw[st.Key]
	switch v = bytes.TrimSpace(v); {
	case !ok, !st.Protection && string(v) == "null":
	case string(v) == "true" || string(v) == "false":
		*st.val(out) = string(v) == "true"
	default:
		if len(v) > 40 {
			v = append(v[:40:40], "…"...)
		}
		return fmt.Sprintf("%s is %s, not true or false", st.Key, v)
	}
	return ""
}

func marshal(raw map[string]json.RawMessage) ([]byte, error) {
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// hostless drops the host path an *fs.PathError carries.
func hostless(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}
	return err
}

// fileStamp is what tells a changed file (a write of ours — a new inode —,
// a restore, a hand edit) apart from the one the cache holds.
type fileStamp struct {
	exists bool
	mod    int64 // mtime, ns
	size   int64
	ino    uint64
}

func stampFile(path string) (fileStamp, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileStamp{}, nil
	}
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{exists: true, mod: fi.ModTime().UnixNano(), size: fi.Size(), ino: inode(fi)}, nil
}
