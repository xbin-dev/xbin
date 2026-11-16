// Package delegation holds what xbind keeps about delegation — a person's
// workspace agent acting as them (internal/auth/delegate.go; plans/wsagent/
// s2-delegation.md): today the per-person audit log, next the admin change
// sets a delegate proposes.
package delegation

// audit.go — "what my agent did": the rows the delegate gate writes, per
// person.
//
//   - Where: <dir>/<hex(user id)>/audit-YYYY-MM.jsonl, appended, 0600 under
//     xbind's own data directory: no sandbox writes there (D78), and no tile
//     — the agent's component included — reads it.
//   - What: who, which sealed component, method, path WITHOUT its query,
//     the tile and the delegation class, the status, and the refusal's
//     reason; for an approved side effect, the ticket or standing approval
//     it ran under. Never a body, a query or a header: a call's content
//     stays in the tile it went to.
//   - Which calls: the gate decides (internal/server/delegate.go) — every
//     act, every tile call, every refusal, every read of code, logs and
//     admin state; not the listing reads a UI makes all day.
//   - How long: KeepMonths monthly files; older ones are removed when a new
//     month's file is first written.

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// KeepMonths is how many monthly files a person's log keeps.
const KeepMonths = 13

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
	Ticket  string    `json:"ticket,omitempty"`  // the approved request it ran under
	Session string    `json:"session,omitempty"` // the agent session (handle) the call ran under
	Under   string    `json:"under,omitempty"`   // …or the standing approval, or "preference" for a session started unasked
	Bytes   int64     `json:"bytes,omitempty"`   // what a web fetch read (D239)
	// Server and Tool: an MCP call's outside server ("ext:<id>") and tool —
	// never its arguments (D240)
	Server string `json:"server,omitempty"`
	Tool   string `json:"tool,omitempty"`
	// From and By are for a line an admin applied at another person's
	// request (a plan's "Ask an admin"): in the admin's log From is the
	// person who asked (Agent is THEIR agent, which proposed it); in the
	// asker's log By is the admin who applied it. Both empty otherwise.
	From string `json:"from,omitempty"`
	By   string `json:"by,omitempty"`
	// N: rows the gate left out before this one — refusals, dry runs and
	// reads beyond what it writes a minute (the gate's audit limit).
	N int `json:"n,omitempty"`
}

// Log is the store. A nil *Log records nothing. Rows are appended under a
// lock per person: one person's agent never holds up another's rows.
type Log struct {
	dir string
	now func() time.Time

	mu    sync.Mutex // users only
	users map[string]*logUser
}

type logUser struct {
	mu    sync.Mutex
	month string // the month last written (prune once a month)
}

// Open is the log under dir (created at the first row).
func Open(dir string) *Log {
	return &Log{dir: dir, now: time.Now, users: map[string]*logUser{}}
}

func (l *Log) lock(user string) *logUser {
	l.mu.Lock()
	u := l.users[user]
	if u == nil {
		u = &logUser{}
		l.users[user] = u
	}
	l.mu.Unlock()
	u.mu.Lock()
	return u
}

// MaxPath bounds a row's path: longer ones are cut (a path is the caller's
// to choose, and the log must not grow by it).
const MaxPath = 512

func userDir(dir, user string) string { return filepath.Join(dir, hex.EncodeToString([]byte(user))) }

// userOfDir is the person a directory under the store is for.
func userOfDir(name string) (string, bool) {
	b, err := hex.DecodeString(name)
	return string(b), err == nil && len(b) > 0
}

const monthFmt = "2006-01"

// Bounds of a row. Every string a row carries is cut to its bound when it
// is written, whoever handed it over (a tile path comes from a query or a
// request body; a refusal quotes the request): no row is longer than
// MaxRow on disk, so no row can make the file unreadable. MaxField bounds
// the names (agent, tile, class, ticket, under), MaxPath the path and the
// refusal. A user id is never cut — a cut id is another person's — so a
// row for one over MaxField is refused.
const (
	MaxField = 256
	MaxRow   = 4096
)

func bound(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// bounded is c with every field within its bound.
func bounded(c Call) Call {
	c.Agent, c.Tile, c.Class = bound(c.Agent, MaxField), bound(c.Tile, MaxField), bound(c.Class, 64)
	c.Ticket, c.Under, c.Method, c.Session = bound(c.Ticket, 64), bound(c.Under, 96), bound(c.Method, 16), bound(c.Session, 64)
	c.From, c.By = bound(c.From, MaxField), bound(c.By, MaxField)
	c.Path, c.Refused = bound(c.Path, MaxPath), bound(c.Refused, MaxPath)
	return c
}

// Append records c (its Time is set here when zero).
func (l *Log) Append(c Call) error {
	if l == nil || c.User == "" {
		return nil
	}
	if len(c.User) > MaxField {
		return fmt.Errorf("delegation audit: a user id of %d bytes", len(c.User))
	}
	c = bounded(c)
	u := l.lock(c.User)
	defer u.mu.Unlock()
	if c.Time.IsZero() {
		c.Time = l.now().UTC()
	}
	dir := userDir(l.dir, c.User)
	month := c.Time.Format(monthFmt)
	if u.month != month {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		u.month = month
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
	rows, _, err := l.ReadCounted(user, before, limit)
	return rows, err
}

// ReadCounted is Read, and how many lines of the files it read were no
// row: over-long, torn, not a row's JSON. Such a line is skipped, never an
// error — one bad line must not hide a month from its person.
func (l *Log) ReadCounted(user string, before time.Time, limit int) ([]Call, int, error) {
	if l == nil || user == "" {
		return []Call{}, 0, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	u := l.lock(user)
	defer u.mu.Unlock()
	dir := userDir(l.dir, user)
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []Call{}, 0, nil
	} else if err != nil {
		return nil, 0, err
	}
	var files []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "audit-") && strings.HasSuffix(e.Name(), ".jsonl") {
			files = append(files, e.Name())
		}
	}
	slices.Sort(files)
	slices.Reverse(files) // newest month first
	out, skipped := []Call{}, 0
	for _, name := range files {
		rows, bad, err := readFile(filepath.Join(dir, name))
		skipped += bad
		if err != nil {
			return nil, 0, err
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
	return out, skipped, nil
}

func readFile(path string) (rows []Call, skipped int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	rd := bufio.NewReaderSize(f, 64<<10)
	for {
		line, long, err := readLine(rd, MaxRow)
		if len(line) > 0 || long {
			var c Call
			switch {
			case long:
				skipped++ // written by something that didn't bound it
			case json.Unmarshal(line, &c) != nil || c.User == "" || c.Time.IsZero():
				skipped++ // torn, or not a row
			default:
				rows = append(rows, bounded(c))
			}
		}
		if err == io.EOF {
			return rows, skipped, nil
		}
		if err != nil {
			return rows, skipped, err
		}
	}
}

// readLine reads one line of at most max bytes; a longer one is read past
// and reported as long, with nothing of it kept.
func readLine(rd *bufio.Reader, max int) (line []byte, long bool, err error) {
	for {
		part, err := rd.ReadSlice('\n')
		if !long {
			if len(line)+len(part) > max+1 {
				long, line = true, nil
			} else {
				line = append(line, part...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return bytes.TrimRight(line, "\r\n"), long, err
	}
}
