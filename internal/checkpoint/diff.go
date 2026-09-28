package checkpoint

// diff.go — the diff for review (11-contract §1.11, 06-security L4 and
// T10.3): a git patch, or a per-file summary, between two checkpoints of one
// tile's store, a work-tree side being captured first (content-addressed,
// collectable). It runs `git diff-tree` confined over the store, read-only,
// with --no-ext-diff --no-textconv, so no driver the tile or anyone names
// runs; renames are detected, binary files named and not inlined.
//
// Its bounds: a patch is cut at 16 MiB (Truncated), a summary at 5 000 files,
// a diff at 30 s (ErrDiffTimeout, a 504). One diff runs per tile with one
// more waiting, and two run across xbind; a request past a tile's two answers
// 429 at once (ErrDiffBusy), so no tile holds the slots other tiles'
// confirmations need.
//
// A diff never creates a store: a tile without one has nothing to diff
// (ErrNothingToDiff, the 409), and no tool runs. The plane answers the same
// for a tile without a record whose store remains (an opt-out keeps it),
// before calling Diff.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
)

// The diff's bounds (vars so tests can shrink them).
var (
	diffMaxPatch = 16 << 20         // bytes of patch
	diffMaxFiles = 5000             // files of a summary
	diffTimeout  = 30 * time.Second // one diff, its capture included
)

const (
	diffPerTile     = 2 // one running, one waiting
	diffAcrossXbind = 2 // running at once, all tiles together
)

var (
	// ErrDiffBusy: the tile has a diff running and one waiting (a 429).
	ErrDiffBusy = errors.New("diff queue full")
	// ErrDiffTimeout: the diff took longer than 30 s (a 504).
	ErrDiffTimeout = errors.New("diff took too long")
	// ErrNothingToDiff: the tile runs its work tree and has no checkpoint
	// store, so there is nothing to diff (a 409); nothing was captured.
	ErrNothingToDiff = errors.New("nothing to diff")
	// ErrBadDiffPath: the path to narrow a diff to isn't a clean
	// tile-relative path (a 400).
	ErrBadDiffPath = errors.New("bad diff path")
)

// DiffSide is one side of a diff: a checkpoint of the tile's store by its
// full tree id, or the work tree, which the diff captures.
type DiffSide struct {
	Tree     string // a full tree id; "" with WorkTree
	WorkTree bool
}

// DiffRequest asks for a diff of one tile.
type DiffRequest struct {
	// Source is the tile; its work tree and nested components matter only
	// when a side is the work tree.
	Source
	From, To DiffSide
	By       string // who acts: a work-tree side's capture records it
	Path     string // "" or a clean tile-relative path: the diff of that path only
	Stat     bool   // the per-file summary instead of the patch
}

// DiffFile is one file of a summary (11-contract §1.11's files).
type DiffFile struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // A, M, D, R or T
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary,omitempty"`
}

// DiffResult is a diff: its resolved sides, for X-XBin-Checkpoint-From and
// -To (a work-tree side is the checkpoint it captured), and the patch or the
// summary.
type DiffResult struct {
	From, To  Checkpoint
	Patch     []byte     // !Stat: the patch, at most 16 MiB
	Files     []DiffFile // Stat: at most 5 000 files
	Truncated bool       // the patch or the summary was cut
}

// ---- the queue ----

// diffs is the diff queue: admissions per tile store (a running diff and a
// waiting one), a turn per tile, and the slots across xbind.
var diffs = struct {
	mu    sync.Mutex
	tiles map[string]*diffTile // by the tile's store directory
	run   chan struct{}
}{tiles: map[string]*diffTile{}, run: make(chan struct{}, diffAcrossXbind)}

type diffTile struct {
	admitted int
	turn     chan struct{}
}

// admitDiff takes one of key's two places, or refuses at once.
func admitDiff(key string) (*diffTile, bool) {
	diffs.mu.Lock()
	defer diffs.mu.Unlock()
	t := diffs.tiles[key]
	if t == nil {
		t = &diffTile{turn: make(chan struct{}, 1)}
		diffs.tiles[key] = t
	}
	if t.admitted >= diffPerTile {
		return nil, false
	}
	t.admitted++
	return t, true
}

func leaveDiff(key string, t *diffTile) {
	diffs.mu.Lock()
	defer diffs.mu.Unlock()
	if t.admitted--; t.admitted == 0 {
		delete(diffs.tiles, key)
	}
}

// queueDiff admits a diff of the tile whose store is key, then waits for the
// tile's turn and a slot across xbind; done gives both back.
func queueDiff(ctx context.Context, key, tile string) (done func(), err error) {
	t, ok := admitDiff(key)
	if !ok {
		return nil, &idError{fmt.Sprintf("%s already has a diff running and one waiting; retry shortly", tile), ErrDiffBusy}
	}
	select {
	case t.turn <- struct{}{}:
	case <-ctx.Done():
		leaveDiff(key, t)
		return nil, diffTimedOut(tile)
	}
	select {
	case diffs.run <- struct{}{}:
	case <-ctx.Done():
		<-t.turn
		leaveDiff(key, t)
		return nil, diffTimedOut(tile)
	}
	return func() { <-diffs.run; <-t.turn; leaveDiff(key, t) }, nil
}

func diffTimedOut(tile string) error {
	return &idError{fmt.Sprintf("the diff of %s took longer than %d s (narrow it with path or stat)", tile, int(diffTimeout/time.Second)), ErrDiffTimeout}
}

// ---- the diff ----

// diffScript prints the patch, or the summary (raw records with their
// statuses, then numstat records, NUL-separated), of tree $A → $B. $P, when
// set, narrows it to one literal path; it follows "--", so git reads it as a
// path whatever it holds.
const diffScript = `S=$1 A=$2 B=$3 STAT=$4 P=$5
set -- -r -M --no-ext-diff --no-textconv --no-color
if [ "$STAT" = 1 ]; then set -- "$@" -z --raw --numstat; else set -- "$@" -p; fi
set -- "$@" "$A" "$B"
if [ -n "$P" ]; then set -- "$@" -- ":(top,literal)$P"; fi
hg --git-dir="$S" diff-tree "$@" || exit 75
`

// Diff diffs one tile (11-contract §1.11): the queue, then a capture of each
// work-tree side (without Create: a tile without a store gets
// ErrNothingToDiff, and nothing runs), then one confined, read-only
// diff-tree over the store (L4), all within 30 s.
func (s *Store) Diff(ctx context.Context, req DiffRequest) (DiffResult, error) {
	tile := req.Tile
	if !cleanRel(tile) || !singleLine(tile) {
		return DiffResult{}, fmt.Errorf("diff: bad tile path %q", tile)
	}
	if req.Path != "" && (!cleanRel(req.Path) || !singleLine(req.Path) || len(req.Path) > s.Caps.PathLen && s.Caps.PathLen > 0) {
		return DiffResult{}, &idError{fmt.Sprintf("%q is not a clean tile-relative path", req.Path), ErrBadDiffPath}
	}
	for _, side := range []DiffSide{req.From, req.To} {
		if side.WorkTree == (side.Tree != "") || !side.WorkTree && !fullID(side.Tree) {
			return DiffResult{}, fmt.Errorf("diff of %s: a side is a full tree id or the work tree", tile)
		}
	}
	if !s.Exists(tile) {
		return DiffResult{}, &idError{fmt.Sprintf("%s has no deployments: its work tree is what runs, so there is nothing to diff", tile), ErrNothingToDiff}
	}
	ctx, cancel := context.WithTimeout(ctx, diffTimeout)
	defer cancel()
	done, err := queueDiff(ctx, s.Dir(tile), tile)
	if err != nil {
		return DiffResult{}, err
	}
	defer done()

	var res DiffResult
	var captured *Checkpoint
	for _, side := range []struct {
		DiffSide
		into *Checkpoint
	}{{req.From, &res.From}, {req.To, &res.To}} {
		var err error
		switch {
		case side.WorkTree && captured != nil:
			*side.into = *captured
		case side.WorkTree:
			var r Result
			r, err = s.Capture(ctx, CaptureRequest{Source: req.Source, By: req.By})
			if errors.Is(err, ErrNoStore) {
				return DiffResult{}, &idError{fmt.Sprintf("%s has no deployments: its work tree is what runs, so there is nothing to diff", tile), ErrNothingToDiff}
			}
			*side.into, captured = r.Checkpoint, &r.Checkpoint
		default:
			*side.into, err = s.Get(ctx, tile, side.Tree)
		}
		if err != nil {
			if ctx.Err() != nil {
				return DiffResult{}, diffTimedOut(tile)
			}
			return DiffResult{}, err
		}
	}

	stat := "0"
	limit := diffMaxPatch + 1
	if req.Stat {
		stat, limit = "1", 64<<20
	}
	dir := s.Dir(tile)
	out, err := s.script(ctx, confine.Cmd{Dir: dir, ReadOnlyDir: true, MaxOutput: limit},
		diffScript, dir, res.From.Hash, res.To.Hash, stat, req.Path)
	if err != nil {
		if ctx.Err() != nil {
			return DiffResult{}, diffTimedOut(tile)
		}
		return DiffResult{}, fmt.Errorf("diff of %s: %w", tile, err)
	}
	if !req.Stat {
		res.Patch = out.Stdout
		if len(res.Patch) > diffMaxPatch {
			res.Patch, res.Truncated = res.Patch[:diffMaxPatch], true
		}
		return res, nil
	}
	res.Files, res.Truncated = parseDiffStat(out.Stdout, diffMaxFiles)
	if len(out.Stdout) >= limit {
		res.Truncated = true
	}
	return res, nil
}

// parseDiffStat reads `diff-tree -z --raw --numstat`: every raw record (a
// ":<modes> <ids> <status>" field, then one path, or two for a rename), then
// as many numstat records ("<added>\t<removed>\t<path>", or with an empty
// path and the two names after it for a rename), in the same order. It keeps
// at most max files.
func parseDiffStat(out []byte, max int) ([]DiffFile, bool) {
	f := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	var files []DiffFile
	i := 0
	for i < len(f) && strings.HasPrefix(f[i], ":") {
		meta := strings.Fields(f[i])
		i++
		if len(meta) != 5 || i >= len(f) {
			break
		}
		status := meta[4][:1]
		path := f[i]
		i++
		if status == "R" || status == "C" {
			if i >= len(f) {
				break
			}
			path = f[i]
			i++
		}
		files = append(files, DiffFile{Path: path, Status: status})
	}
	for n := 0; n < len(files) && i < len(f); n++ {
		cols := strings.SplitN(f[i], "\t", 3)
		i++
		if len(cols) != 3 {
			break
		}
		if cols[2] == "" { // a rename: the two names follow
			i += 2
		}
		if cols[0] == "-" && cols[1] == "-" {
			files[n].Binary = true
			continue
		}
		files[n].Added, _ = strconv.Atoi(cols[0])
		files[n].Removed, _ = strconv.Atoi(cols[1])
	}
	if len(files) > max {
		return files[:max], true
	}
	return files, false
}
