package checkpoint

// materialize.go — a checkpoint as files (07-runtime §2.6, 06-security T2.4,
// ledger L3): one confined run extracts a checkpoint's tree with git's
// read-tree and checkout-index into a fresh .xbin/deploy/<TileKey>/.tmp-*/,
// with only that directory writable and the store bound read-only, and sets
// directories to 0755 and files to 0444, or 0555 where the exec bit is kept;
// xbind then renames the tree to .xbin/deploy/<TileKey>/<full tree id>/. A
// present tree is therefore complete. Never git archive: it honours
// export-* attributes, so its output is not the checkpoint.
//
// Directories stay owner-writable, so rm -rf .xbin and GC's os.RemoveAll
// work with no chmod walk; sandboxes see a tree read-only through the bind
// flag, not through host modes. xbind never opens, stats or walks inside a
// tree with a following call (C5): it Lstats, renames and removes.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/util"
)

// tmpPrefix names in-progress extractions under .xbin/deploy/<TileKey>/.
const tmpPrefix = ".tmp-"

var (
	// materializing holds the .tmp-* directories of extractions in flight
	// in this process: no sweep removes them.
	materializing sync.Map // tmp dir → struct{}
	// handedOut is when Materialize last returned each root: a deploy
	// between materializing and starting, or a build binding the tree, is
	// not yet a running generation, and GC leaves such a tree alone for
	// handoutGrace (gc.go).
	handedOut sync.Map // materialized root → time.Time
)

// materializeScript is the one confined run (L3). Dir is the fresh .tmp-*
// directory, the only thing writable; the tree goes into its tree/, the
// scratch index beside it, outside the target. Only a retained checkpoint
// is extracted: admission (§2.2) is what makes its tree safe to check out
// — no "..", no .git component, no gitlink — and checkout-index never
// writes through a symlink. The store's info/attributes outranks every
// in-tree .gitattributes, so the bytes are the checkpoint's.
const materializeScript = `S=$1 D=$2 T=$3
W="$D/tree"
g() { hg --git-dir="$S" "$@"; }
umask 022
g show-ref -q --verify "refs/xbin/checkpoints/$T" || exit 70
mkdir "$W" || exit 71
GIT_INDEX_FILE="$D/index"; export GIT_INDEX_FILE
g read-tree "$T" || exit 72
g --work-tree="$W" checkout-index -a -f || exit 73
find -P "$W" \( -type d -exec chmod 0755 {} + \) -o \( -type f -perm -u+x -exec chmod 0555 {} + \) -o \( -type f -exec chmod 0444 {} + \) || exit 74
`

var materializeStages = map[int]string{
	71: "starting the extraction",
	72: "reading the tree",
	73: "checking the files out",
	74: "setting the tree read-only",
}

// Materialize returns the host root of tile's checkpoint tree (its full
// tree id), .xbin/deploy/<TileKey>/<tree>/, materializing it first when
// missing: the fast path is one Lstat. The extraction is single-flight per
// tile (it holds the tile's store lock, so it never races a capture, GC or
// another extraction), bounded by Caps.Time (a caller waiting past it gets
// an error wrapping context.DeadlineExceeded, the contract's 503), and
// idempotent. A killed run leaves only a .tmp-* directory, swept before the
// tile's next extraction, by GC and by Sweep, never a partial tree.
//
// Only a checkpoint the tile's store retains is materialized
// (ErrUnknownCheckpoint otherwise); a tile without a store gets ErrNoStore
// and nothing is created. Consumers bind the root read-only (the static
// plane opens beneath it, the backend and build binds carry RO); xbind
// reads inside it only through fsutil.OpenBeneath (C5).
func (s *Store) Materialize(tile, tree string) (string, error) {
	if !cleanRel(tile) || !singleLine(tile) {
		return "", fmt.Errorf("checkpoint: bad tile path %q", tile)
	}
	if !fullID(tree) {
		return "", &idError{fmt.Sprintf("%q is not a full checkpoint tree id", tree), ErrBadID}
	}
	root := filepath.Join(s.TreesDir(tile), tree)
	if present(root) {
		s.handOut(root)
		return root, nil
	}
	if !s.Exists(tile) {
		return "", fmt.Errorf("%s: %w", tile, ErrNoStore)
	}
	ctx := context.Background()
	if t := s.Caps.Time; t > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t)
		defer cancel()
	}
	release, err := s.acquire(ctx, tile)
	if err != nil {
		return "", s.materializeErr(ctx, tile, tree, err)
	}
	defer release()
	if present(root) { // another caller's extraction finished while this one waited
		s.handOut(root)
		return root, nil
	}
	if err := s.materialize(ctx, tile, tree, root); err != nil {
		return "", s.materializeErr(ctx, tile, tree, err)
	}
	s.handOut(root)
	return root, nil
}

// materialize extracts tree to root; the caller holds tile's store lock.
func (s *Store) materialize(ctx context.Context, tile, tree, root string) error {
	dir := s.Dir(tile)
	if !s.Exists(tile) { // gone while this call waited for the lock
		return fmt.Errorf("%s: %w", tile, ErrNoStore)
	}
	if err := pin(dir); err != nil {
		return err
	}
	trees := s.TreesDir(tile)
	_ = sweepTmp(trees) // under the lock no extraction of this tile is in flight; a leftover it can't remove is harmless
	if exists(root) {
		// not a directory: never one of ours (a present tree is complete)
		if err := os.RemoveAll(root); err != nil {
			return fmt.Errorf("materializing: %w", err)
		}
	}
	if err := os.MkdirAll(trees, 0o755); err != nil {
		return fmt.Errorf("materializing: %w", err)
	}
	tmp := filepath.Join(trees, tmpPrefix+util.RandomToken(8))
	materializing.Store(tmp, struct{}{})
	defer materializing.Delete(tmp)
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return fmt.Errorf("materializing: %w", err)
	}
	defer os.RemoveAll(tmp) // the index, and the tree unless it moved
	_, err := s.script(ctx, confine.Cmd{Dir: tmp, Binds: []sandbox.Bind{confine.RO(dir)}}, materializeScript, dir, tmp, tree)
	if err != nil {
		if code, ok := confine.ExitCode(err); ok && code == 70 {
			return &idError{fmt.Sprintf("%s has no checkpoint %s", tile, tree), ErrUnknownCheckpoint}
		}
		return stageError(err, materializeStages)
	}
	out := filepath.Join(tmp, "tree")
	if !present(out) {
		return errors.New("the extraction left no tree")
	}
	if err := os.Rename(out, root); err != nil {
		if present(root) {
			return nil // complete, whoever put it there
		}
		return fmt.Errorf("materializing: %w", err)
	}
	return nil
}

// materializeErr names what failed, and says so when the time ran out.
func (s *Store) materializeErr(ctx context.Context, tile, tree string, err error) error {
	var ide *idError
	if errors.As(err, &ide) || errors.Is(err, ErrNoStore) {
		return err
	}
	if ctx.Err() != nil && !errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("%v: %w", err, context.DeadlineExceeded)
	}
	return fmt.Errorf("materializing checkpoint c:%s of %s: %w", tree[:7], tile, err)
}

func (s *Store) handOut(root string) { handedOut.Store(root, s.now()) }

// present reports whether p is a directory, not following a last symlink.
func present(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

// Sweep removes the .tmp-* extractions killed runs left under every tile's
// .xbin/deploy/<TileKey>/ (07-runtime §2.6: at boot), except those in
// flight in this process. Nothing is created; a workspace without
// .xbin/deploy has nothing to sweep.
func (s *Store) Sweep() error {
	base := filepath.Join(s.Root, ".xbin", "deploy")
	names, err := dirNames(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("sweeping materializations: %w", err)
	}
	var first error
	for _, e := range names {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			if err := sweepTmp(filepath.Join(base, e.Name())); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

// sweepTmp removes the .tmp-* entries of one tile's trees directory that no
// extraction of this process holds.
func sweepTmp(trees string) error {
	names, err := dirNames(trees)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var first error
	for _, e := range names {
		if !strings.HasPrefix(e.Name(), tmpPrefix) {
			continue
		}
		p := filepath.Join(trees, e.Name())
		if _, busy := materializing.Load(p); busy {
			continue
		}
		if err := os.RemoveAll(p); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// dirNames lists an xbind-owned directory under .xbin/deploy: opened
// beneath itself, the entries' types as the directory records them (a
// symlink stays a symlink), nothing inside them opened or followed (C5).
func dirNames(dir string) ([]os.DirEntry, error) {
	f, err := fsutil.OpenBeneath(dir, ".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}
