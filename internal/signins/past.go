package signins

// past.go — who COULD change a tile's code before (D245, the sticky
// condition of Source.NeedsTrust).
//
// "I trust this tile's source" is asked where a tile's code is not only
// the person's own NOW (trust.go). That forgot the past: someone holds
// write on a tile, leaves a hook in it, loses write — the tile is "only
// yours" again, needs no trust, and the next session runs their hook with
// the person's real home and saved sign-in. So xbind remembers, per tile
// IDENTITY, everyone it has seen able to change the tile's code — its
// owners (a person, an org) and the other people with write who aren't
// workspace admins — and a tile someone else could change before asks for
// each person's trust until that person answers the card.
//
// The record is xbind's: data/signins/tiles.json, never in the tile. It is
// written when the condition is observed — at the events that change who
// owns a tile or who can change it, at every rescan, and at every
// judgement of a tile (a session's start is the backstop). A tile that was
// only ever one party's gets no record: a one-person workspace never
// writes one. What happened before xbind kept this record is not known to
// it, and neither is a change made to its files while it was not running.

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// MaxPastParties bounds one tile's record.
const MaxPastParties = 200

// Past is the record of who could change tiles' code.
type Past struct {
	path string
	now  func() time.Time

	mu sync.Mutex
	// seen: the parties last observed on a tile that has no record yet —
	// in memory, so that the day a second party appears (or the one leaves)
	// the record starts with both
	seen map[string][]string
	file *pastFile // loaded on first use
}

type pastFile struct {
	V     int                  `json:"v"`
	Tiles map[string]*pastTile `json:"tiles"`
}

// pastTile is one tile identity's record: each party — "user:<id>" or
// "org:<id>" — and when it was last seen able to change the code (0: it
// can now).
type pastTile struct {
	Tile    string           `json:"tile"`
	Since   int64            `json:"since"`
	Parties map[string]int64 `json:"parties"`
}

// NewPast opens the record at path (made on its first write).
func NewPast(path string, now func() time.Time) *Past {
	if now == nil {
		now = time.Now
	}
	return &Past{path: path, now: now, seen: map[string][]string{}}
}

func (p *Past) load() *pastFile {
	if p.file != nil {
		return p.file
	}
	f := &pastFile{V: 1, Tiles: map[string]*pastTile{}}
	if b, err := os.ReadFile(p.path); err == nil {
		var got pastFile
		if json.Unmarshal(b, &got) == nil && got.Tiles != nil {
			f.Tiles = got.Tiles
		} else {
			slog.Error("signins: the record of who could change tiles is unreadable; it starts again", "path", p.path)
		}
	}
	p.file = f
	return f
}

// parties is who can change a tile's code now, as record keys: its owner
// when it is a person's or an org's (the workspace's own tiles are its
// admins', who are in every person's trust base) and editors — the other
// people with write who aren't workspace admins.
func parties(owner string, editors []string) []string {
	var out []string
	if owner != "" {
		out = append(out, owner)
	}
	for _, e := range editors {
		if ref := "user:" + e; e != "" && !slices.Contains(out, ref) {
			out = append(out, ref)
		}
	}
	sort.Strings(out)
	return out
}

// Observe notes that the tile with identity ident, at path tile, is owned
// by owner ("" the workspace, user:<id>, org:<id>) and can be changed by
// editors (account ids: write level or more, not workspace admins) — now.
// A tile only one party was ever seen on is remembered in memory alone.
func (p *Past) Observe(ident, tile, owner string, editors []string) {
	if p == nil || ident == "" {
		return
	}
	cur := parties(owner, editors)
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.load()
	now := p.now().UnixMilli()
	rec := f.Tiles[ident]
	changed := false
	if rec == nil {
		prev := p.seen[ident]
		left := slices.ContainsFunc(prev, func(x string) bool { return !slices.Contains(cur, x) })
		union := append([]string(nil), cur...)
		for _, x := range prev {
			if !slices.Contains(union, x) {
				union = append(union, x)
			}
		}
		if len(union) < 2 && !left {
			p.seen[ident] = cur // one party, or none: nothing to remember on disk
			return
		}
		rec = &pastTile{Tile: tile, Since: now, Parties: map[string]int64{}}
		for _, x := range prev {
			rec.Parties[x] = now // (set to 0 below when it still can)
		}
		f.Tiles[ident] = rec
		delete(p.seen, ident)
		changed = true
	}
	if rec.Tile != tile {
		rec.Tile, changed = tile, true
	}
	for _, x := range cur {
		if until, ok := rec.Parties[x]; (!ok && len(rec.Parties) < MaxPastParties) || (ok && until != 0) {
			rec.Parties[x], changed = 0, true
		}
	}
	for x, until := range rec.Parties {
		if until == 0 && !slices.Contains(cur, x) {
			rec.Parties[x], changed = now, true
		}
	}
	if !changed {
		return
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err == nil {
		err = fsutil.WriteFileAtomic(p.path, append(b, '\n'), 0o600)
		if err != nil {
			slog.Error("signins: the record of who could change tiles could not be written", "err", err)
		}
	}
}

// Others answers, for user, who else could change the tile's code before
// and can't now — account ids and "org:<id>", sorted — and when the last
// of them was last seen able to (Unix ms; 0 with none).
func (p *Past) Others(ident, user string) ([]string, int64) {
	if p == nil || ident == "" {
		return nil, 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	rec := p.load().Tiles[ident]
	if rec == nil {
		return nil, 0
	}
	var out []string
	var last int64
	for x, until := range rec.Parties {
		if until == 0 || x == "user:"+user {
			continue
		}
		out = append(out, strings.TrimPrefix(x, "user:"))
		last = max(last, until)
	}
	sort.Strings(out)
	return out, last
}

// Recorded reports how many tiles have a record (tests; an admin's count).
func (p *Past) Recorded() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.load().Tiles)
}
