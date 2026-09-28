// Package checkpoint is the checkpoint store (D119a): the code a tile's
// deployments run, kept as git trees in a private, xbind-owned bare
// repository, data/checkpoints/<TileKey>.git. A checkpoint is one tree —
// every file of the work tree except .git directories and nested
// components, gitignore not applying — named by its tree hash: "c:" and at
// least 7 hex digits in answers, the full hash in records, logs and
// directory names. Beside each checkpoint the store keeps its git view, the
// same tree minus what the tile's own ignore rules exclude, which is what
// the fetch remote serves (05-model §3). Neither is ever served from here.
//
// Only confined git reads or writes the store (D78, D119g). Every tool run
// goes through confine with the store bound read-write and the work tree
// read-only; the tile's .git is never the repository, the store's config and
// attributes are constants xbind writes, and no tile-controlled string
// reaches argv: pathspecs and path lists travel in files. xbind reads what
// the runs leave in a quarantine only through fsutil.OpenBeneath (C5).
//
// The store is lazy (D119c): a zero-state tile never has one. Only a committed
// opt-in creates it, a Capture with Create; a dry run (Estimate) and a diff
// never do.
//
// The files: store.go holds the Store, its paths, the per-tile lock, the
// capture rate, lazy creation, the hardened git invocation and checkpoint
// ids; capture.go the work-tree feed (two confined runs around admission);
// admit.go the caps, the pure admission check and the stat-only estimate.
// The rest of the store's surface hangs off the same Store from files of its
// own:
//
//	materialize.go  Materialize(tile, tree string) (root string, err error)
//	gc.go           GC(ctx, tile, keep func() []string) error
//	log.go          Log, AppendLog: the deploy log, refs/xbin/log/<name>
//	remote.go       the view repository (ViewDir) and ServeFetch
//	diff.go         Diff: --no-ext-diff --no-textconv, bounded per tile
//	drift.go        Drift: how far the work tree moved from a checkpoint
//
// Each takes the tile's store lock (acquire) around what it changes, runs
// its tools through Store.script with the hardened git (hg), and drops the
// id cache (forget) when it deletes checkpoints.
package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/util"
)

// Store is the workspace's checkpoint stores, one bare repository per tile
// that has opted in. Its methods take the tile path; the zero value is not
// usable, New builds one.
type Store struct {
	// Root is the workspace root: stores live under Root/data/checkpoints,
	// materialized trees under Root/.xbin/deploy.
	Root string
	// Caps bounds one checkpoint and the capture rate (07-runtime §2.5):
	// DefaultCaps, the v1 constants, which a workspace policy may raise.
	Caps Caps

	run func(context.Context, confine.Cmd) (confine.Result, error) // every tool run: confine.Run
	now func() time.Time

	mu     sync.Mutex
	tiles  map[string]*tileState // by tile path
	parent sync.Mutex            // data/checkpoints appears and goes with a store: create, and a failed opt-in's undo
}

// New returns the checkpoint stores of the workspace at root. It creates
// nothing: a store appears with the first committed opt-in of its tile.
func New(root string) *Store {
	return &Store{Root: root, Caps: DefaultCaps(), run: confine.Run, now: time.Now, tiles: map[string]*tileState{}}
}

// Caps are the limits of one capture (07-runtime §2.5, NP-07-7).
type Caps struct {
	Entries     int           // tree entries: files, symlinks and directories
	Bytes       int64         // file content
	LargestFile int64         // one file
	PathLen     int           // a tile-relative path, in bytes
	Depth       int           // directory depth
	Time        time.Duration // one capture, all its confined runs
	Burst       int           // captures of one tile allowed at once …
	Every       time.Duration // … then one per Every (drift counts and dry runs don't count)
}

// DefaultCaps are the v1 constants.
func DefaultCaps() Caps {
	return Caps{Entries: 200_000, Bytes: 2 << 30, LargestFile: 256 << 20, PathLen: 1024, Depth: 64,
		Time: 5 * time.Minute, Burst: 10, Every: 3 * time.Second}
}

// Checkpoint is one checkpoint of a tile (11-contract §1.1's Checkpoint).
type Checkpoint struct {
	ID   string    `json:"id"`   // "c:" and the shortest unique prefix of at least 7 digits, now
	Hash string    `json:"hash"` // the full tree id
	Feed string    `json:"feed"` // how it was made: "work-tree"
	At   time.Time `json:"at"`   // its first capture
	By   string    `json:"by"`   // who acted then
	// WorkTreeHead is the commit the tile's own repository had checked out
	// at the first capture: best effort and untrusted, "" when unknown.
	WorkTreeHead string `json:"-"`
	// WorkTreeBranch is the branch it had checked out then (the
	// Xbin-Work-Tree-Branch trailer, D131): "" when detached, unknown, or
	// the capture predates the trailer.
	WorkTreeBranch string `json:"-"`
}

// FeedWorkTree names checkpoints captured from the work tree.
const FeedWorkTree = "work-tree"

var (
	// ErrNoStore: the tile has no checkpoint store, and the call may not
	// create one (only a committed opt-in does, D119c).
	ErrNoStore = errors.New("the tile has no checkpoint store")
	// ErrRefused: a capture broke a cap, a tree-hygiene rule, or met a file
	// it can't read. The store is unchanged (*Refusal says why).
	ErrRefused = errors.New("checkpoint refused")
	// ErrRateLimited: the tile captured too often (*RateLimited, a 429).
	ErrRateLimited = errors.New("too many checkpoints")
	// ErrBadID: not "c:" and 7 to 64 lowercase hex digits (a 400).
	ErrBadID = errors.New("not a checkpoint id")
	// ErrUnknownCheckpoint: no checkpoint of the tile has that id (a 404).
	ErrUnknownCheckpoint = errors.New("no such checkpoint")
	// ErrAmbiguousID: several checkpoints of the tile share the prefix (a 409).
	ErrAmbiguousID = errors.New("ambiguous checkpoint id")
)

// RateLimited is a capture refused by the capture rate.
type RateLimited struct {
	Tile       string
	RetryAfter time.Duration
}

func (e *RateLimited) Error() string {
	return fmt.Sprintf("%s was checkpointed too often; retry in %ds", e.Tile, int((e.RetryAfter+time.Second-1)/time.Second))
}

// Is makes errors.Is(err, ErrRateLimited) hold.
func (e *RateLimited) Is(target error) bool { return target == ErrRateLimited }

// idError carries the contract's text (11-contract §1.14) and its kind.
type idError struct {
	msg  string
	kind error
}

func (e *idError) Error() string { return e.msg }
func (e *idError) Unwrap() error { return e.kind }

// tileState is what the Store keeps per tile. lock is taken by acquire; the
// other fields are guarded by Store.mu.
type tileState struct {
	lock   chan struct{}   // the store lock: one store operation per tile at a time
	tokens float64         // captures left in the burst
	filled time.Time       // when tokens was last topped up
	bg     bucket          // the background captures' own (CaptureRequest.Background)
	known  map[string]meta // the tile's checkpoints by full tree id; nil until read
}

// bucket is a second token bucket, at the same caps.
type bucket struct {
	tokens float64
	filled time.Time
}

// meta is what a checkpoint's retention commit records.
type meta struct {
	at     time.Time
	by     string
	feed   string
	head   string
	branch string
}

func (s *Store) state(tile string) *tileState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked(tile)
}

func (s *Store) stateLocked(tile string) *tileState {
	ts := s.tiles[tile]
	if ts == nil {
		ts = &tileState{lock: make(chan struct{}, 1)}
		s.tiles[tile] = ts
	}
	return ts
}

// acquire takes tile's store lock: every operation that writes the store —
// capture, the deploy log, GC, the view refresh — runs under it, so objects
// and refs move one operation at a time.
func (s *Store) acquire(ctx context.Context, tile string) (release func(), err error) {
	ts := s.state(tile)
	select {
	case ts.lock <- struct{}{}:
		return func() { <-ts.lock }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%s: waiting for its checkpoint store: %w", tile, ctx.Err())
	}
}

// take spends one capture of tile's rate: a burst of c.Burst, refilled at
// one per c.Every. When none is left it reports how long until one is.
func (s *Store) take(tile string, c Caps, background bool) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.stateLocked(tile)
	tokens, filled := &ts.tokens, &ts.filled
	if background {
		tokens, filled = &ts.bg.tokens, &ts.bg.filled
	}
	now := s.now()
	burst := float64(max(c.Burst, 1))
	switch {
	case filled.IsZero():
		*tokens = burst
	case c.Every > 0:
		*tokens = min(burst, *tokens+float64(now.Sub(*filled))/float64(c.Every))
	default:
		*tokens = burst
	}
	*filled = now
	if *tokens >= 1 {
		*tokens--
		return 0, true
	}
	return time.Duration((1 - *tokens) * float64(c.Every)), false
}

// ---- paths ----

// Dir is tile's store: Root/data/checkpoints/<TileKey>.git.
func (s *Store) Dir(tile string) string {
	return filepath.Join(s.Root, "data", "checkpoints", util.TileKey(tile)+".git")
}

// ViewDir is tile's view repository, the fetch remote's only source:
// Root/data/checkpoints/<TileKey>.view.git.
func (s *Store) ViewDir(tile string) string {
	return filepath.Join(s.Root, "data", "checkpoints", util.TileKey(tile)+".view.git")
}

// TreesDir holds tile's materialized checkpoints: Root/.xbin/deploy/<TileKey>.
func (s *Store) TreesDir(tile string) string {
	return filepath.Join(s.Root, ".xbin", "deploy", util.TileKey(tile))
}

// Exists reports whether tile has a store.
func (s *Store) Exists(tile string) bool {
	_, err := os.Lstat(filepath.Join(s.Dir(tile), "HEAD"))
	return err == nil
}

// ---- creation and the pinned files ----

// storeConfig is the store's whole config (07-runtime §2.1): no hooks,
// remotes, alternates or fsmonitor; xbind drives GC; objects and refs are
// fsync'd, since data/ is what backups carry (batch: one flush per capture
// rather than one per object).
const storeConfig = `[core]
	repositoryformatversion = 0
	filemode = true
	bare = true
	logAllRefUpdates = false
	excludesFile = /dev/null
	attributesFile = /dev/null
	fsync = objects,reference
	fsyncMethod = batch
[gc]
	auto = 0
`

// storeAttributes outranks every in-tree .gitattributes (gitattributes(5)):
// capture and materialization are byte-exact and no driver a tile names
// resolves (06-security T1).
const storeAttributes = "* -text -filter -ident -working-tree-encoding -export-subst -export-ignore !diff !merge\n"

// initScript makes an empty bare repository: no template, so no hooks, no
// description and no info/exclude. HEAD names deploy/main, dangling, as the
// contract draws the store (11-contract §10.3); nothing reads it.
const initScript = `S=$1
hg init -q --bare --template= -b deploy/main "$S" || exit 1
`

// create makes tile's store at dir with a confined git init, then pins its
// files. Only Capture with Create calls it.
func (s *Store) create(ctx context.Context, dir string) error {
	s.parent.Lock()
	err := os.MkdirAll(dir, 0o755)
	s.parent.Unlock()
	if err != nil {
		return fmt.Errorf("checkpoint store: %w", err)
	}
	if _, err := s.script(ctx, confine.Cmd{Dir: dir}, initScript, dir); err != nil {
		return fmt.Errorf("checkpoint store: git init: %w", err)
	}
	return pin(dir)
}

// uncreate removes a store a failed opt-in created, and data/checkpoints
// with it when no other store is there: a failed opt-in is no opt-in (D119c).
func (s *Store) uncreate(dir string) {
	s.parent.Lock()
	defer s.parent.Unlock()
	_ = os.RemoveAll(dir)
	_ = os.Remove(filepath.Dir(dir)) // only if empty
}

// pin writes the store's config and attributes from the constants when they
// differ, and removes what the store must never have: hooks, an exclude
// file, alternates. Nothing from a tile is ever copied into any of them.
func pin(dir string) error {
	for _, f := range []struct{ rel, body string }{{"config", storeConfig}, {"info/attributes", storeAttributes}} {
		p := filepath.Join(dir, filepath.FromSlash(f.rel))
		if b, err := os.ReadFile(p); err == nil && string(b) == f.body { // walk-ok: the store's own file, which only xbind writes
			continue
		}
		if err := fsutil.WriteFileAtomicIn(p, []byte(f.body), 0o644); err != nil {
			return fmt.Errorf("checkpoint store: %w", err)
		}
	}
	for _, rel := range []string{"hooks", "info/exclude", "objects/info/alternates", "objects/info/http-alternates", "description"} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if _, err := os.Lstat(p); err == nil {
			if err := os.RemoveAll(p); err != nil {
				return fmt.Errorf("checkpoint store: %w", err)
			}
		}
	}
	return nil
}

// ---- the hardened git ----

// gitConfig are the -c pairs every git run on a store carries: confine's own
// (internal/confine/git.go gitFlags) and 06-security T1's, plus quiet advice
// and no background maintenance.
var gitConfig = []string{
	"core.fsmonitor=false",
	"core.hooksPath=/dev/null",
	"core.untrackedCache=false",
	"safe.directory=*",
	"protocol.ext.allow=never",
	"commit.gpgsign=false",
	"tag.gpgsign=false",
	"core.pager=cat",
	"core.attributesFile=/dev/null",
	"core.excludesFile=/dev/null",
	"core.autocrlf=false",
	"core.symlinks=true",
	"core.fileMode=true",
	"core.ignoreCase=false",
	"core.precomposeUnicode=false",
	"core.protectNTFS=true",
	"core.protectHFS=true",
	"core.quotePath=false",
	"core.splitIndex=false",
	"gc.auto=0",
	"maintenance.auto=false",
	"submodule.recurse=false",
	"transfer.fsckObjects=true",
	"protocol.allow=never",
	"advice.addEmbeddedRepo=false",
}

// gitEnv is every store run's environment: confine's (no system or global
// config, never prompt, no optional locks, a fixed locale) and no system
// attributes file or replace refs either.
var gitEnv = []string{
	"GIT_CONFIG_NOSYSTEM=1",
	"GIT_CONFIG_GLOBAL=/dev/null",
	"GIT_ATTR_NOSYSTEM=1",
	"GIT_NO_REPLACE_OBJECTS=1",
	"GIT_TERMINAL_PROMPT=0",
	"GIT_OPTIONAL_LOCKS=0",
	"LC_ALL=C",
}

// preamble starts every store script. hg is the only way a script runs git:
// the hardened flags on every invocation (NP-07-9: several git commands in
// one confined run, each hardened). unlock drops the lock files a killed run
// left in a repository, inside the sandbox (07-runtime §2.1).
var preamble = func() string {
	var b strings.Builder
	b.WriteString("set -u\nhg() { git")
	for _, kv := range gitConfig {
		b.WriteString(" -c '" + kv + "'")
	}
	b.WriteString(` "$@"; }
unlock() { find "$1" -path "$1/objects" -prune -o -path "$1/quarantine" -prune -o -name '*.lock' -type f -exec rm -f {} + 2>/dev/null; }
`)
	return b.String()
}()

// script runs a store script confined: body is a constant of this package,
// args are xbind's own paths and validated ids, never tile strings. The
// run's timeout is what is left of ctx's deadline.
func (s *Store) script(ctx context.Context, c confine.Cmd, body string, args ...string) (confine.Result, error) {
	c.Argv = append([]string{"sh", "-c", preamble + body, "sh"}, args...)
	c.Env = append(append([]string(nil), gitEnv...), c.Env...)
	if dl, ok := ctx.Deadline(); ok {
		left := time.Until(dl)
		if left <= 0 {
			return confine.Result{}, context.DeadlineExceeded
		}
		c.Timeout = left
	}
	return s.run(ctx, c)
}

// ---- ids ----

var hexID = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// ParseID validates a checkpoint id from a client, "c:" and 7 to 64
// lowercase hex digits (11-contract §0.3), and returns its hex prefix.
func ParseID(id string) (string, error) {
	h, ok := strings.CutPrefix(id, "c:")
	if !ok || !hexID.MatchString(h) {
		return "", &idError{fmt.Sprintf("%q is not a checkpoint id (c: and at least 7 hex digits)", id), ErrBadID}
	}
	return h, nil
}

// fullID reports whether s is a full object id: 40 (SHA-1) or 64 (SHA-256)
// lowercase hex digits. Ids are checked before they reach any argv.
func fullID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// shortID is tree's id among known: "c:" and the shortest prefix of at
// least 7 digits that no other checkpoint shares.
func shortID(tree string, known map[string]meta) string {
	n := 7
	for other := range known {
		if other == tree {
			continue
		}
		l := 0
		for l < len(tree) && l < len(other) && tree[l] == other[l] {
			l++
		}
		n = max(n, l+1)
	}
	return "c:" + tree[:min(n, len(tree))]
}

func (m meta) checkpoint(tree string, known map[string]meta) Checkpoint {
	return Checkpoint{ID: shortID(tree, known), Hash: tree, Feed: m.feed, At: m.at, By: m.by, WorkTreeHead: m.head,
		WorkTreeBranch: m.branch}
}

// Resolve finds the checkpoint of tile an id names, only in tile's own
// store. An ambiguous prefix is refused with the contract's text.
func (s *Store) Resolve(ctx context.Context, tile, id string) (Checkpoint, error) {
	prefix, err := ParseID(id)
	if err != nil {
		return Checkpoint{}, err
	}
	known, err := s.known(ctx, tile)
	if err != nil {
		return Checkpoint{}, err
	}
	var hits []string
	for tree := range known {
		if strings.HasPrefix(tree, prefix) {
			hits = append(hits, tree)
		}
	}
	switch len(hits) {
	case 0:
		return Checkpoint{}, &idError{fmt.Sprintf("%s has no checkpoint %s", tile, id), ErrUnknownCheckpoint}
	case 1:
		return known[hits[0]].checkpoint(hits[0], known), nil
	}
	return Checkpoint{}, &idError{fmt.Sprintf("checkpoint id %s is ambiguous in %s; use more digits", id, tile), ErrAmbiguousID}
}

// Get is the checkpoint of tile whose full tree id is tree.
func (s *Store) Get(ctx context.Context, tile, tree string) (Checkpoint, error) {
	known, err := s.known(ctx, tile)
	if err != nil {
		return Checkpoint{}, err
	}
	m, ok := known[tree]
	if !ok {
		return Checkpoint{}, &idError{fmt.Sprintf("%s has no checkpoint %s", tile, tree), ErrUnknownCheckpoint}
	}
	return m.checkpoint(tree, known), nil
}

// List is every checkpoint tile's store holds, newest first.
func (s *Store) List(ctx context.Context, tile string) ([]Checkpoint, error) {
	known, err := s.known(ctx, tile)
	if err != nil {
		return nil, err
	}
	out := make([]Checkpoint, 0, len(known))
	for tree, m := range known {
		out = append(out, m.checkpoint(tree, known))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].Hash < out[j].Hash
	})
	return out, nil
}

// knownFormat prints one line per checkpoint of a store: its tree, then its
// retention commit's time and trailers, NUL-separated. Trailer values are
// single-line: xbind writes them.
const knownFormat = `%(refname:lstrip=3)%00%(committerdate:unix)%00%(trailers:key=Xbin-By,valueonly,separator=)%00%(trailers:key=Xbin-Feed,valueonly,separator=)%00%(trailers:key=Xbin-Work-Tree-Head,valueonly,separator=)%00%(trailers:key=Xbin-Work-Tree-Branch,valueonly,separator=)`

const knownScript = `hg --git-dir="$1" for-each-ref --format='` + knownFormat + `' refs/xbin/checkpoints/ || exit 1
`

// known is tile's checkpoints, read once with a confined for-each-ref and
// kept; a tile with no store has none, and asking creates nothing.
func (s *Store) known(ctx context.Context, tile string) (map[string]meta, error) {
	if k := s.cached(tile); k != nil {
		return k, nil
	}
	if !s.Exists(tile) {
		return map[string]meta{}, nil
	}
	release, err := s.acquire(ctx, tile)
	if err != nil {
		return nil, err
	}
	defer release()
	if k := s.cached(tile); k != nil {
		return k, nil
	}
	dir := s.Dir(tile)
	res, err := s.script(ctx, confine.Cmd{Dir: dir, ReadOnlyDir: true}, knownScript, dir)
	if err != nil {
		return nil, fmt.Errorf("%s: listing checkpoints: %w", tile, err)
	}
	k := parseKnown(res.Stdout)
	s.remember(tile, k)
	return k, nil
}

func (s *Store) cached(tile string) map[string]meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ts := s.tiles[tile]; ts != nil {
		return ts.known
	}
	return nil
}

func (s *Store) remember(tile string, k map[string]meta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateLocked(tile).known = k
}

// forget drops tile's cached checkpoint list: GC and purge call it after
// they delete checkpoints, restore after it rebuilds a store.
func (s *Store) forget(tile string) { s.remember(tile, nil) }

// parseKnown reads knownFormat's lines; a malformed one is skipped.
func parseKnown(out []byte) map[string]meta {
	k := map[string]meta{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\x00")
		if len(f) != 6 || !fullID(f[0]) {
			continue
		}
		m := meta{by: f[2], feed: f[3]}
		if sec, err := strconv.ParseInt(f[1], 10, 64); err == nil {
			m.at = time.Unix(sec, 0).UTC()
		}
		if fullID(f[4]) {
			m.head = f[4]
		}
		if BranchNameOK(f[5]) {
			m.branch = f[5]
		}
		k[f[0]] = m
	}
	return k
}

// ---- small helpers ----

// cleanRel reports whether p is a clean, relative, slash-separated path
// that stays beneath its root.
func cleanRel(p string) bool {
	return p != "" && p != "." && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "../") && p != ".." &&
		filepath.ToSlash(filepath.Clean(p)) == p && !strings.ContainsAny(p, "\\\x00")
}

// singleLine reports whether s may go into a commit trailer.
func singleLine(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// exists reports whether p exists (without following a last symlink).
func exists(p string) bool {
	_, err := os.Lstat(p)
	return !errors.Is(err, fs.ErrNotExist)
}
