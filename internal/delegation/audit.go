// Package delegation holds what xbind keeps about delegation — a person's
// workspace agent acting as them (internal/auth/delegate.go; plans/wsagent/
// s2-delegation.md): today the per-person audit log, next the admin change
// sets a delegate proposes.
package delegation

// audit.go — "what my agent did": one row per delegate call, per person.
//
//   - Where: <dir>/<hex(user id)>/audit-YYYY-MM.jsonl, appended, 0600 under
//     xbind's own data directory: no sandbox writes there (D78), and no tile
//     — the agent's component included — reads it.
//   - What: who, which sealed component, method, path WITHOUT its query,
//     the tile and the delegation class, the status, and the refusal's
//     reason. Never a body, a query or a header: a call's content stays in
//     the tile it went to.
//   - How much: every side effect and every refusal is a row. Successful
//     reads repeat (an agent polls), so one row per (method, path) a minute
//     is written; the rest are counted into the next row (N).
//   - How long: KeepMonths monthly files; older ones are removed when a new
//     month's file is first written.

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// KeepMonths is how many monthly files a person's log keeps.
const KeepMonths = 13

// readWindow coalesces a person's repeated successful reads of one path.
const readWindow = time.Minute

// Call is one row.
type Call struct {
	Time    time.Time `json:"ts"`
	User    string    `json:"user"`
	Agent   string    `json:"agent"`             // the sealed component that acted
	Method  string    `json:"method"`            //
	Path    string    `json:"path"`              // no query
	Tile    string    `json:"tile,omitempty"`    // the tile a call or a session is on, when the gate knows it
	Class   string    `json:"class"`             // the delegation class the gate judged it by
	Effect  bool      `json:"effect,omitempty"`  // a side effect (not a read)
	DryRun  bool      `json:"dryRun,omitempty"`  // asked what it would do; nothing ran
	Status  int       `json:"status"`            // the HTTP status answered
	Refused string    `json:"refused,omitempty"` // why the gate refused it, when it did
	N       int       `json:"n,omitempty"`       // reads of this path coalesced into this row, beyond itself
}

// Log is the store. A nil *Log records nothing.
type Log struct {
	dir string
	now func() time.Time

	mu    sync.Mutex
	reads map[string]*readMark // user \x00 method \x00 path → the window
	month map[string]string    // user → the month last written (prune once a month)
}

type readMark struct {
	at time.Time
	n  int
}

// Open is the log under dir (created at the first row).
func Open(dir string) *Log {
	return &Log{dir: dir, now: time.Now, reads: map[string]*readMark{}, month: map[string]string{}}
}

func userDir(dir, user string) string { return filepath.Join(dir, hex.EncodeToString([]byte(user))) }

const monthFmt = "2006-01"

// Append records c (its Time is set here when zero). A successful read that
// repeats one written within the last minute is counted, not written.
func (l *Log) Append(c Call) error {
	if l == nil || c.User == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if c.Time.IsZero() {
		c.Time = l.now().UTC()
	}
	if !c.Effect && c.Refused == "" && !c.DryRun && c.Status < 400 {
		key := c.User + "\x00" + c.Method + "\x00" + c.Path
		if m := l.reads[key]; m != nil && c.Time.Sub(m.at) < readWindow {
			m.n++
			return nil
		} else if m != nil {
			c.N = m.n
		}
		l.reads[key] = &readMark{at: c.Time}
		if len(l.reads) > 4096 { // forget the windows that closed
			for k, m := range l.reads {
				if c.Time.Sub(m.at) >= readWindow {
					delete(l.reads, k)
				}
			}
		}
	}
	dir := userDir(l.dir, c.User)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	month := c.Time.Format(monthFmt)
	if l.month[c.User] != month {
		l.month[c.User] = month
		prune(dir, c.Time)
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "audit-"+month+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return errors.Join(err, f.Close())
}

// prune removes monthly files older than KeepMonths before now.
func prune(dir string, now time.Time) {
	oldest := now.AddDate(0, -KeepMonths+1, 0).Format(monthFmt)
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		m, ok := strings.CutPrefix(strings.TrimSuffix(e.Name(), ".jsonl"), "audit-")
		if ok && len(m) == len(monthFmt) && m < oldest {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// Read answers user's rows, newest first: at most limit (0: 200) rows older
// than before (zero: now).
func (l *Log) Read(user string, before time.Time, limit int) ([]Call, error) {
	if l == nil || user == "" {
		return []Call{}, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	dir := userDir(l.dir, user)
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []Call{}, nil
	} else if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "audit-") && strings.HasSuffix(e.Name(), ".jsonl") {
			files = append(files, e.Name())
		}
	}
	slices.Sort(files)
	slices.Reverse(files) // newest month first
	out := []Call{}
	for _, name := range files {
		rows, err := readFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		for i := len(rows) - 1; i >= 0 && len(out) < limit; i-- {
			if before.IsZero() || rows[i].Time.Before(before) {
				out = append(out, rows[i])
			}
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func readFile(path string) ([]Call, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rows []Call
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var c Call
		if json.Unmarshal(sc.Bytes(), &c) == nil && c.User != "" { // a torn last line is skipped
			rows = append(rows, c)
		}
	}
	return rows, sc.Err()
}
