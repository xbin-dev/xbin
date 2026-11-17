package signins

// use.go — the one way a secret leaves the store (Use), the log of every
// such use, and the workspace's switch.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// Credential is a sign-in handed to a session start: its metadata and the
// secret. The secret goes into the coding agent's environment and nowhere
// else — never a log line, an event, an answer or a file.
type Credential struct {
	Signin
	Secret string
}

// UseFor says what a credential is taken for: the row of the use log.
type UseFor struct {
	Tile    string // the tile the session runs on
	Session string // the session's id
	Origin  string // "person": they opened it; "agent": their workspace agent did
}

// UseRow is one use, as the person's log keeps it.
type UseRow struct {
	At      int64  `json:"at"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Harness string `json:"harness"`
	Tile    string `json:"tile"`
	Session string `json:"session"`
	Origin  string `json:"origin"`
}

// usesKept is how many rows a person's use log keeps.
const usesKept = 500

// Peek is user's sign-in for harness as a start would take it — id, or
// their default when id is "" — without its secret and without a use:
// what a launcher says before anything starts. nil: they have none (or the
// one named is gone); the error is why none can be had at all.
func (s *Store) Peek(user, harness, id string) (*Signin, error) {
	if _, ok := s.person(user); !ok {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read(user)
	if err != nil {
		return nil, err
	}
	return pick(f.Signins, harness, id), nil
}

func pick(all []Signin, harness, id string) *Signin {
	for i := range all {
		sg := all[i]
		if sg.Harness != harness {
			continue
		}
		if (id == "" && sg.Default) || (id != "" && sg.ID == id) {
			return &sg
		}
	}
	return nil
}

// Use hands out user's sign-in for harness — id, or their default when id
// is "" — for one session start, and writes the use down. nil with a nil
// error: they have none. An error: there is one and it can't be had (the
// vault sealed, the switch off, expired, the account disabled) — the
// session starts without it and says why.
func (s *Store) Use(user, harness, id string, u UseFor) (*Credential, error) {
	if _, ok := s.person(user); !ok {
		return nil, refuse(403, "this account can't use saved sign-ins (it is disabled or gone)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read(user)
	if err != nil {
		return nil, err
	}
	sg := pick(f.Signins, harness, id)
	if sg == nil {
		return nil, nil
	}
	if !s.policy().Enabled {
		return nil, ErrOff
	}
	now := s.o.Now()
	if sg.Expired(now) {
		return nil, refuse(409, "your saved sign-in %s expired on %s: sign in again", sg.Name, time.UnixMilli(sg.ExpiresAt).UTC().Format("2006-01-02"))
	}
	m, err := s.secrets(f)
	if err != nil {
		return nil, err
	}
	secret := m[sg.ID]
	if secret == "" {
		return nil, refuse(409, "your saved sign-in %s holds no credential any more: remove it and sign in again", sg.Name)
	}
	for i := range f.Signins {
		if f.Signins[i].ID == sg.ID {
			f.Signins[i].LastUsedAt = now.UnixMilli()
			sg.LastUsedAt = now.UnixMilli()
		}
	}
	if err := s.write(f, nil); err != nil {
		return nil, err // a use that can't be written down doesn't happen
	}
	row := UseRow{At: now.UnixMilli(), ID: sg.ID, Name: sg.Name, Harness: harness, Tile: u.Tile, Session: u.Session, Origin: u.Origin}
	s.logUse(user, row)
	// the workspace's audit log: who, what, where — never the secret
	slog.Info("audit", "event", "signin.use", "user", user, "harness", harness, "signin", sg.ID, "name", sg.Name,
		"tile", u.Tile, "session", u.Session, "origin", u.Origin)
	return &Credential{Signin: *sg, Secret: secret}, nil
}

// logUse appends row to user's use log, cut to its newest rows when it
// grew (s.mu held). A log that can't be written is logged, not fatal: the
// audit line and lastUsedAt carry the use.
func (s *Store) logUse(user string, row UseRow) {
	path := s.usePath(user)
	b, _ := json.Marshal(row)
	rows := append(readUses(path), b)
	if len(rows) > usesKept {
		rows = rows[len(rows)-usesKept:]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		err = fsutil.WriteFileAtomic(path, append(bytes.Join(rows, []byte("\n")), '\n'), 0o600)
		if err != nil {
			slog.Warn("signins: the use log could not be written", "user", user, "err", err)
		}
	}
}

func readUses(path string) [][]byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var rows [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		if line := bytes.TrimSpace(sc.Bytes()); len(line) > 0 {
			rows = append(rows, append([]byte(nil), line...))
		}
	}
	return rows
}

// Uses is user's use log, newest first.
func (s *Store) Uses(user string) []UseRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := readUses(s.usePath(user))
	out := make([]UseRow, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		var r UseRow
		if json.Unmarshal(rows[i], &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

// ---- the workspace's switch -----------------------------------------------------

// Policy is the workspace's word on saved sign-ins.
type Policy struct {
	// Enabled: people may save sign-ins and have them used. On by default.
	Enabled bool `json:"enabled"`
	// Locked: the host's file sets it (Reason says so); a write is refused.
	Locked bool   `json:"locked,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Trust: whether people may trust a tile whose source isn't only their
	// own (trust.go).
	Trust TrustPolicy `json:"trust"`
}

// HostLockReason is what a held switch says — the host's other settings'
// words.
const HostLockReason = "set by your host"

// PolicyPath is where the admins' switch is kept.
func (s *Store) PolicyPath() string { return filepath.Join(s.o.Dir, "policy.json") }

// Policy is the switch in force: the host's when its file sets it, else
// the admins' (on when nobody set it; on, too, when the file can't be
// read — the switch is a convenience an admin turns off, and a broken file
// never silently changes what people have).
func (s *Store) Policy() Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policy()
}

func (s *Store) policy() Policy {
	f := s.policyFile()
	p := Policy{Enabled: true, Trust: TrustPolicy{Allowed: true}}
	if f.Enabled != nil {
		p.Enabled = *f.Enabled
	}
	if f.Trust != nil {
		p.Trust.Allowed = *f.Trust
	}
	if s.o.Host != nil {
		if set, on := s.o.Host(); set {
			p.Enabled, p.Locked, p.Reason = on, true, HostLockReason
		}
	}
	if s.o.HostTrust != nil {
		if set, on := s.o.HostTrust(); set {
			p.Trust = TrustPolicy{Allowed: on, Locked: true, Reason: HostLockReason}
		}
	}
	return p
}

// SetPolicy writes the admins' switch. Refused (409) while the host holds it.
func (s *Store) SetPolicy(enabled bool) (Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur := s.policy(); cur.Locked {
		return cur, refuse(409, "whether people may save coding-agent sign-ins is set by your host")
	}
	f := s.policyFile()
	f.Enabled = &enabled
	if err := s.writePolicy(f); err != nil {
		return Policy{}, err
	}
	return s.policy(), nil
}
