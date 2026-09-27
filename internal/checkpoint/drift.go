package checkpoint

// drift.go — how far the work tree moved from a checkpoint (07-runtime §2.9,
// NP-13-12, 06-security L16): the count of files that differ, computed by a
// confined diff, never by counting watcher batches (the watcher drops batches
// under load). One confined run that changes nothing durable: it copies the
// persistent index into a scratch directory, runs capture's step 1 (git add
// -A -f, nested components excluded) against that copy with a scratch
// quarantine for new objects, then `git diff-index --cached` against the
// checkpoint's tree, and the scratch directory goes. The store is bound
// read-only, the scratch directory read-write, the work tree read-only.
//
// It creates no checkpoint and doesn't count against the capture rate. It
// runs on demand: the watcher's notice for a tile whose live reload is paused
// and a client's question both reach it through the plane, which debounces it
// to one run per tile every 2 s.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// driftScript counts the work tree's files that differ from tree $T. The
// tile's strings reach git only in files ($Q/add, $Q/rm), as in capture.
// The store's own index is only read: copied with its mtime, which git's
// racy-entry check compares file times against (a fresh one would make a
// file rewritten within the index's own tick look clean). git add sees only
// the scratch objects, so the blobs it hashes land there and it never
// freshens (touches) a store object it would find through an alternate;
// only diff-index, which writes nothing, reads the store's objects, for $T.
const driftScript = `S=$1 W=$2 Q=$3 T=$4
g() { hg --git-dir="$S" --work-tree="$W" "$@"; }
GIT_INDEX_FILE="$Q/index"
export GIT_INDEX_FILE
if [ -f "$S/index" ] && [ ! -h "$S/index" ]; then cp -p "$S/index" "$Q/index" || exit 76; fi
if [ -s "$Q/rm" ]; then
	g rm --cached -r -q --ignore-unmatch --pathspec-from-file="$Q/rm" --pathspec-file-nul >/dev/null || exit 77
fi
g add -A -f --ignore-errors --pathspec-from-file="$Q/add" --pathspec-file-nul 2>/dev/null
if [ $? -gt 1 ]; then exit 78; fi
( GIT_ALTERNATE_OBJECT_DIRECTORIES="$S/objects"; export GIT_ALTERNATE_OBJECT_DIRECTORIES
	g diff-index --cached --no-renames --name-only -z "$T" >"$Q/changed" ) || exit 79
tr -cd '\000' <"$Q/changed" | wc -c
`

var driftStages = map[int]string{
	76: "copying the index",
	77: "dropping nested components from the scratch index",
	78: "adding the work tree to the scratch index",
	79: "comparing with the checkpoint",
}

// driftDir is the drift count's scratch directory in tile's store: its index
// copy and its quarantine. Only one exists at a time (the store lock).
func driftDir(store string) string { return filepath.Join(store, "drift") }

// Drift counts the files of src's work tree that differ from tile's
// checkpoint tree — added, changed or removed — in one confined run that
// leaves the store, its index and the work tree as they were (L16). It
// needs the tile's store (ErrNoStore otherwise) and a checkpoint of it.
func (s *Store) Drift(ctx context.Context, src Source, tree string) (int, error) {
	if err := src.check(); err != nil {
		return 0, err
	}
	if !fullID(tree) {
		return 0, fmt.Errorf("drift of %s: %q is not a full tree id", src.Tile, tree)
	}
	if !s.Exists(src.Tile) {
		return 0, fmt.Errorf("%s: %w", src.Tile, ErrNoStore)
	}
	if _, err := s.Get(ctx, src.Tile, tree); err != nil {
		return 0, err
	}
	if s.Caps.Time > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.Caps.Time)
		defer cancel()
	}
	release, err := s.acquire(ctx, src.Tile)
	if err != nil {
		return 0, err
	}
	defer release()

	store := s.Dir(src.Tile)
	q := driftDir(store)
	if err := os.RemoveAll(q); err != nil { // a killed run's
		return 0, fmt.Errorf("drift of %s: %w", src.Tile, err)
	}
	defer os.RemoveAll(q)
	if err := os.MkdirAll(filepath.Join(q, "objects"), 0o755); err != nil {
		return 0, fmt.Errorf("drift of %s: %w", src.Tile, err)
	}
	var add, rm bytes.Buffer
	add.WriteString(":(top)\x00")
	for _, n := range src.Nested {
		add.WriteString(":(top,exclude,literal)" + n + "\x00")
		rm.WriteString(":(top,literal)" + n + "\x00")
	}
	if err := os.WriteFile(filepath.Join(q, "add"), add.Bytes(), 0o600); err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(q, "rm"), rm.Bytes(), 0o600); err != nil {
		return 0, err
	}
	res, err := s.script(ctx, confine.Cmd{
		Dir:         store,
		ReadOnlyDir: true,
		Binds:       []sandbox.Bind{confine.RW(q), confine.RO(src.WorkTree)},
		Env:         []string{"GIT_OBJECT_DIRECTORY=" + filepath.Join(q, "objects")},
	}, driftScript, store, src.WorkTree, q, tree)
	if err != nil {
		return 0, fmt.Errorf("drift of %s: %w", src.Tile, stageError(err, driftStages))
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("drift of %s: the count printed %q", src.Tile, bytes.TrimSpace(res.Stdout))
	}
	return n, nil
}
