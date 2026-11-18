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
//
// Four more things the record knows (the review of D245):
//
//   - a WORKSPACE ADMIN who stops being one (demoted, disabled, deleted)
//     could have changed every tile: noted once, workspace-wide
//     (ObserveAdmins), and named on every tile's question ("was a
//     workspace admin until …") until each person answers. Admins are not
//     parties of a tile while they are admins — they are in everyone's
//     trust base — so a workspace with one account never records anything,
//     whatever that account owns or hands on;
//   - a tile whose IDENTITY changed (a restore, another disk, its
//     directory made again) is the tile at that path for this record: the
//     parties recorded for the path are carried over to the new identity.
//     Nothing is ever pruned;
//   - a record that is full (MaxPastParties) says "and others" (More): it
//     still asks;
//   - who can write a tile includes who can write a tile it lies in (the
//     caller's list of editors: broker/signinothers.go).

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

// MaxPastParties bounds one tile's record: past it the record says "and
// others" (pastTile.More), which still asks for trust.
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
	// admins: the workspace's admins as last observed (nil: not yet) — in
	// memory: only one who STOPS being an admin is written down
	admins []string
	looked bool
	file   *pastFile // loaded on first use
}

type pastFile struct {
	V     int                  `json:"v"`
	Tiles map[string]*pastTile `json:"tiles"`
	// Admins: accounts that were workspace admins and stopped being one —
	// when that was seen (Unix ms). They could change every tile.
	Admins map[string]int64 `json:"admins,omitempty"`
}

// pastTile is one tile identity's record: each party — "user:<id>" or
// "org:<id>" — and when it was last seen able to change the code (0: it
// can now).
type pastTile struct {
	Tile    string           `json:"tile"`
	Since   int64            `json:"since"`
	Parties map[string]int64 `json:"parties"`
	// More: there were more parties than the record holds.
	More bool `json:"more,omitempty"`
}

// PastOthers is what the record says of one tile, for one person.
type PastOthers struct {
	// Parties: everyone else who could change the tile's code before and
	// can't now — account ids and "org:<id>", sorted — the former admins
	// among them (Admins, a subset).
	Parties []string
	Admins  []string
	// Until: when the last of them was last seen able to (Unix ms).
	Until int64
	// More: and others the record had no room for.
	More bool
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
			f.Tiles, f.Admins = got.Tiles, got.Admins
		} else {
			slog.Error("signins: the record of who could change tiles is unreadable; it starts again", "path", p.path)
		}
	}
	p.file = f
	return f
}

func (p *Past) save(f *pastFile) {
	b, _ := json.MarshalIndent(f, "", "  ")
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err == nil {
		if err = fsutil.WriteFileAtomic(p.path, append(b, '\n'), 0o600); err != nil {
			slog.Error("signins: the record of who could change tiles could not be written", "err", err)
		}
	}
}

// parties is who can change a tile's code now, as record keys: its owner
// when it is an org's or a person's who is not a workspace admin (the
// caller passes "" for the workspace's own tiles and for an admin's: admins
// are in every person's trust base while they are admins) and editors —
// the other people with write who aren't workspace admins.
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
// by owner ("" the workspace or a workspace admin, user:<id>, org:<id>)
// and can be changed by editors (account ids: write level or more on it or
// on a tile it lies in, not workspace admins) — now. A tile only one
// party was ever seen on is remembered in memory alone.
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
		// the record of the tile that stood at this path under another
		// identity (a restore, another disk, the directory made again) is
		// this tile's: whoever could change that one may have left
		// something in what stands here now
		var carried *pastTile
		for id, old := range f.Tiles {
			if id == ident || old.Tile != tile {
				continue
			}
			if carried == nil {
				carried = &pastTile{Tile: tile, Since: old.Since, Parties: map[string]int64{}}
			}
			carried.Since, carried.More = min(carried.Since, old.Since), carried.More || old.More
			for x, until := range old.Parties {
				if until == 0 {
					until = now // able to when that record was last written
				}
				if len(carried.Parties) < MaxPastParties {
					carried.Parties[x] = max(carried.Parties[x], until)
				} else if _, ok := carried.Parties[x]; !ok {
					carried.More = true
				}
			}
		}
		if carried != nil {
			rec, changed = carried, true
			f.Tiles[ident] = rec
			delete(p.seen, ident)
		}
	}
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
		until, ok := rec.Parties[x]
		switch {
		case !ok && len(rec.Parties) >= MaxPastParties:
			if !rec.More {
				rec.More, changed = true, true // no room: "and others"
			}
		case !ok || until != 0:
			rec.Parties[x], changed = 0, true
		}
	}
	for x, until := range rec.Parties {
		if until == 0 && !slices.Contains(cur, x) {
			rec.Parties[x], changed = now, true
		}
	}
	if changed {
		p.save(f)
	}
}

// ObserveAdmins notes who the workspace's admins are now (enabled accounts
// with the admin role). One who was an admin when last observed and is not
// any more — demoted, disabled, deleted — could change every tile: written
// down once, workspace-wide. The first observation after a start is the
// baseline (what happened while xbind was not running is not known).
func (p *Past) ObserveAdmins(admins []string) {
	if p == nil {
		return
	}
	cur := append([]string(nil), admins...)
	sort.Strings(cur)
	p.mu.Lock()
	defer p.mu.Unlock()
	prev, looked := p.admins, p.looked
	p.admins, p.looked = cur, true
	if !looked {
		return
	}
	var f *pastFile
	now := p.now().UnixMilli()
	for _, id := range prev {
		if id == "" || slices.Contains(cur, id) {
			continue
		}
		if f == nil {
			f = p.load()
		}
		if f.Admins == nil {
			f.Admins = map[string]int64{}
		}
		f.Admins[id] = now
	}
	if f != nil {
		p.save(f)
	}
}

// Others answers, for user, who else could change the tile's code before
// and can't now: the tile's own past parties, and the accounts that were
// workspace admins and aren't any more (unless one can change the tile
// now: then it is among its editors).
func (p *Past) Others(ident, user string) PastOthers {
	var out PastOthers
	if p == nil || ident == "" {
		return out
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.load()
	rec := f.Tiles[ident]
	seen := map[string]bool{}
	if rec != nil {
		out.More = rec.More
		for x, until := range rec.Parties {
			id := strings.TrimPrefix(x, "user:")
			if until == 0 || x == "user:"+user || (id != x && slices.Contains(p.admins, id)) {
				continue // can now, themselves, or a workspace admin now: in the trust base
			}
			out.Parties, seen[id] = append(out.Parties, id), true
			out.Until = max(out.Until, until)
		}
	}
	for id, until := range f.Admins {
		if id == user || slices.Contains(p.admins, id) || (rec != nil && rec.Parties["user:"+id] == 0 && hasKey(rec.Parties, "user:"+id)) {
			continue // themselves, an admin again, or someone who can change this tile now
		}
		out.Admins = append(out.Admins, id)
		if !seen[id] {
			out.Parties = append(out.Parties, id)
		}
		out.Until = max(out.Until, until)
	}
	sort.Strings(out.Parties)
	sort.Strings(out.Admins)
	return out
}

func hasKey(m map[string]int64, k string) bool { _, ok := m[k]; return ok }

// Recorded reports how many tiles have a record, and how many former
// admins (tests; an admin's count).
func (p *Past) Recorded() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.load()
	return len(f.Tiles) + len(f.Admins)
}
