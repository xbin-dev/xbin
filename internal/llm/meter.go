package llm

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
	sdkllm "github.com/xbin-dev/xbin/sdk/llm"
)

// meter.go — what was used (docs/llm.md "Usage"): counters per UTC day,
// person, component and model. Only counts: never a prompt, an answer or a
// model's output.
//
// The store is one JSON file per UTC month, data/llm/usage/<YYYY-MM>.json
// — {schema, month, rows: [{day, user, component, model, requests, …}]} —
// so a day is a row's key and a month is a file. Counting is in memory
// (one lock, two map writes); a file is written flushGap after its first
// unsaved count and when xbind stops, so a call never waits on the disk
// and a crash loses at most flushGap of counts. Files older than
// keepMonths go when a new month starts. A file this xbind can't read is
// left as it is: its month is counted in memory and never written over.

// Counters are one row's counts.
type Counters struct {
	Requests        int64   `json:"requests"`
	Failed          int64   `json:"failed,omitempty"` // of Requests: refused by the upstream, or broken off before any usage
	InputTokens     int64   `json:"inputTokens"`
	OutputTokens    int64   `json:"outputTokens"`
	CachedTokens    int64   `json:"cachedTokens,omitempty"`
	ReasoningTokens int64   `json:"reasoningTokens,omitempty"`
	Cost            float64 `json:"cost"`               // USD, of the requests whose model has a price
	Unpriced        int64   `json:"unpriced,omitempty"` // of Requests: used tokens on a model without a price
}

func (c *Counters) add(o Counters) {
	c.Requests += o.Requests
	c.Failed += o.Failed
	c.InputTokens += o.InputTokens
	c.OutputTokens += o.OutputTokens
	c.CachedTokens += o.CachedTokens
	c.ReasoningTokens += o.ReasoningTokens
	c.Cost += o.Cost
	c.Unpriced += o.Unpriced
}

// UsageRow is one row: who used which model on which day, and how much.
// User is the acting person ("owner": the root token; "": none — a tile's
// backend acting for nobody); Component the calling tile ("": a direct
// call); Model "<upstream>/<model>".
type UsageRow struct {
	Day       string `json:"day,omitempty"`
	Month     string `json:"month,omitempty"`
	User      string `json:"user"`
	Component string `json:"component"`
	Model     string `json:"model"`
	Counters
}

type rowKey struct{ day, user, component, model string }

type monthDoc struct {
	month  string
	rows   map[rowKey]*Counters
	dirty  bool
	broken bool // the file on disk is unreadable: never written over
}

const (
	usageSchema = 1
	flushGap    = 10 * time.Second
	keepMonths  = 24
	dayFmt      = "2006-01-02"
	monthFmt    = "2006-01"
	// OwnerUser is how the root token's calls are attributed.
	OwnerUser = "owner"
	totalKey  = "\x00total" // the workspace's window among the per-person ones
)

type meter struct {
	dir string // data/llm/usage
	log *slog.Logger

	mu       sync.Mutex
	months   map[string]*monthDoc
	win      map[string]*window // person → the current day's and month's use
	winDay   string
	winMonth string
	timer    *time.Timer
	closed   bool

	wmu sync.Mutex // one flush at a time, in order
}

func newMeter(dir string, log *slog.Logger) *meter {
	return &meter{dir: dir, log: log, months: map[string]*monthDoc{}, win: map[string]*window{}}
}

func (m *meter) path(month string) string { return filepath.Join(m.dir, month+".json") }

type usageFile struct {
	Schema int        `json:"schema"`
	Month  string     `json:"month"`
	Rows   []UsageRow `json:"rows"`
}

// doc is month's rows, read from its file the first time. m.mu held.
func (m *meter) doc(month string) *monthDoc {
	if d := m.months[month]; d != nil {
		return d
	}
	d := &monthDoc{month: month, rows: map[rowKey]*Counters{}}
	m.months[month] = d
	b, err := os.ReadFile(m.path(month))
	if os.IsNotExist(err) {
		return d
	}
	var f usageFile
	if err == nil {
		err = json.Unmarshal(b, &f)
	}
	if err == nil && f.Schema > usageSchema {
		err = fmt.Errorf("schema %d is a newer xbind's", f.Schema)
	}
	if err != nil {
		d.broken = true
		m.log.Error("llm: a usage file can't be read: its month is counted in memory only and the file is left as it is", "file", m.path(month), "err", err)
		return d
	}
	for _, r := range f.Rows {
		k := rowKey{r.Day, r.User, r.Component, r.Model}
		if d.rows[k] == nil {
			d.rows[k] = &Counters{}
		}
		d.rows[k].add(r.Counters)
	}
	return d
}

// roll moves the windows to now's day and month. m.mu held.
func (m *meter) roll(now time.Time) (day, month string) {
	now = now.UTC()
	day, month = now.Format(dayFmt), now.Format(monthFmt)
	switch {
	case month != m.winMonth:
		m.win, m.winMonth, m.winDay = map[string]*window{}, month, day
		for k, c := range m.doc(month).rows {
			m.addWindow(k.user, k.day == day, c.InputTokens+c.OutputTokens, c.Cost)
		}
		if _, err := os.Stat(m.path(month)); os.IsNotExist(err) {
			m.prune(now)
		}
	case day != m.winDay:
		m.winDay = day
		for _, w := range m.win {
			w.dayTokens, w.dayCost = 0, 0
		}
	}
	return day, month
}

func (m *meter) addWindow(user string, today bool, tokens int64, cost float64) {
	for _, k := range []string{user, totalKey} {
		if k == "" {
			continue // no acting person: the workspace's window only
		}
		w := m.win[k]
		if w == nil {
			w = &window{}
			m.win[k] = w
		}
		w.monthTokens += tokens
		w.monthCost += cost
		if today {
			w.dayTokens += tokens
			w.dayCost += cost
		}
	}
}

// prune removes the files of months older than keepMonths.
func (m *meter) prune(now time.Time) {
	oldest := now.AddDate(0, -keepMonths, 0).Format(monthFmt)
	entries, _ := os.ReadDir(m.dir)
	for _, e := range entries {
		month, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || len(month) != len(monthFmt) || month >= oldest {
			continue
		}
		if _, err := time.Parse(monthFmt, month); err == nil {
			_ = os.Remove(filepath.Join(m.dir, e.Name()))
			delete(m.months, month)
		}
	}
}

// record counts one finished call.
func (m *meter) record(now time.Time, user, component, model string, u sdkllm.Usage, cost float64, priced, failed bool) {
	c := Counters{Requests: 1, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CachedTokens: u.CachedTokens, ReasoningTokens: u.ReasoningTokens}
	switch {
	case failed:
		c.Failed = 1
	case priced:
		c.Cost = cost
	case !u.Zero():
		c.Unpriced = 1
	}
	m.mu.Lock()
	day, month := m.roll(now)
	d := m.doc(month)
	k := rowKey{day, user, component, model}
	if d.rows[k] == nil {
		d.rows[k] = &Counters{}
	}
	d.rows[k].add(c)
	d.dirty = true
	m.addWindow(user, true, c.InputTokens+c.OutputTokens, c.Cost)
	late := m.closed // a call that ended while xbind was stopping: no timer will come for it
	if m.timer == nil && !late {
		m.timer = time.AfterFunc(flushGap, m.flush)
	}
	m.mu.Unlock()
	if late {
		m.flush()
	}
}

// windows are user's use ("" = no person) and the workspace's, today and
// this month.
func (m *meter) windows(now time.Time, user string) (person, workspace window) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roll(now)
	if w := m.win[user]; w != nil && user != "" {
		person = *w
	}
	if w := m.win[totalKey]; w != nil {
		workspace = *w
	}
	return person, workspace
}

// flush writes every month with unsaved counts.
func (m *meter) flush() {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	type out struct {
		d   *monthDoc
		raw []byte
	}
	var outs []out
	m.mu.Lock()
	m.timer = nil
	for _, d := range m.months {
		if !d.dirty || d.broken {
			continue
		}
		f := usageFile{Schema: usageSchema, Month: d.month, Rows: rowsOf(d, nil)}
		if raw, err := json.MarshalIndent(f, "", " "); err == nil {
			outs = append(outs, out{d, append(raw, '\n')})
			d.dirty = false
		}
	}
	m.mu.Unlock()
	if len(outs) == 0 {
		return
	}
	err := os.MkdirAll(m.dir, 0o700)
	for _, o := range outs {
		if err == nil {
			err = fsutil.WriteFileAtomic(m.path(o.d.month), o.raw, 0o600)
		}
		if err != nil {
			m.log.Error("llm: saving the usage counters (kept in memory, tried again with the next call)", "month", o.d.month, "err", err)
			m.mu.Lock()
			o.d.dirty = true
			m.mu.Unlock()
		}
	}
}

func (m *meter) close() {
	m.mu.Lock()
	m.closed = true
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	m.mu.Unlock()
	m.flush()
}

// rowsOf is d's rows that keep passes (nil: all), sorted.
func rowsOf(d *monthDoc, keep func(rowKey) bool) []UsageRow {
	out := make([]UsageRow, 0, len(d.rows))
	for k, c := range d.rows {
		if keep == nil || keep(k) {
			out = append(out, UsageRow{Day: k.day, User: k.user, Component: k.component, Model: k.model, Counters: *c})
		}
	}
	slices.SortFunc(out, func(a, b UsageRow) int {
		return cmp.Or(cmp.Compare(a.Day, b.Day), cmp.Compare(a.User, b.User), cmp.Compare(a.Component, b.Component), cmp.Compare(a.Model, b.Model))
	})
	return out
}

// query returns the rows of the days from..to (inclusive, YYYY-MM-DD) that
// keep passes. byMonth folds each month's days into one row.
func (m *meter) query(from, to string, byMonth bool, keep func(user, component, model string) bool) []UsageRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []UsageRow
	start, err1 := time.Parse(dayFmt, from)
	end, err2 := time.Parse(dayFmt, to)
	if err1 != nil || err2 != nil {
		return nil
	}
	for t := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC); !t.After(end); t = t.AddDate(0, 1, 0) {
		month := t.Format(monthFmt)
		_, loaded := m.months[month]
		if !loaded {
			if _, err := os.Stat(m.path(month)); err != nil {
				continue // nothing was counted that month
			}
		}
		rows := rowsOf(m.doc(month), func(k rowKey) bool {
			return k.day >= from && k.day <= to && (keep == nil || keep(k.user, k.component, k.model))
		})
		if !byMonth {
			out = append(out, rows...)
			continue
		}
		folded := map[rowKey]*UsageRow{}
		var order []rowKey
		for _, r := range rows {
			k := rowKey{"", r.User, r.Component, r.Model}
			if folded[k] == nil {
				folded[k] = &UsageRow{Month: month, User: r.User, Component: r.Component, Model: r.Model}
				order = append(order, k)
			}
			folded[k].add(r.Counters)
		}
		for _, k := range order {
			out = append(out, *folded[k])
		}
	}
	return out
}
