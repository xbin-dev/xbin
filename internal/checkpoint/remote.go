package checkpoint

// remote.go — the checkpoint fetch remote (05-model §3, 07-runtime §2.7,
// 11-contract §1.12 and §10.3, 06-security T15a).
//
// It is served from the tile's view repository,
// data/checkpoints/<TileKey>.view.git, never from the store: a bare
// repository holding only refs/heads/deploy/<name> for each pinned
// deployment, pointing at the git view of the checkpoint it runs, and HEAD
// naming refs/heads/deploy/<primary> (dangling while the primary follows the
// work tree). It holds no other object, so no refs/xbin/* ref, no deploy-log
// entry, no full checkpoint tree and no ignored file is ever fetchable.
//
// RefreshView rebuilds it after every change to the pinned set, a pin, or the
// primary: one confined run (L6) fetches the pinned views from the store into
// a fresh repository and runs update-server-info there, xbind pins its files,
// and the fresh repository is renamed into place. It is derived: deleting it
// and refreshing gives the same refs. RemoveView removes it with the record.
//
// ServeFetch hands out dumb-HTTP files from an allow-list only, each opened
// beneath the view repository with every symlink refused, never through
// http.ServeFile (the template remote's opener, which follows symlinks and
// serves any file under the directory).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/util"
)

// Pins is what a tile's view repository shows.
type Pins struct {
	// Primary names the primary deployment: HEAD is a symbolic ref to
	// refs/heads/deploy/<Primary>, dangling unless Primary is pinned.
	Primary string
	// Trees maps each pinned deployment to the full tree id of the
	// checkpoint it runs. A deployment that follows the work tree is absent:
	// a stale ref would mislead someone branching from it.
	Trees map[string]string
}

func (p Pins) check() error {
	if !util.DeploymentNameOK(p.Primary) {
		return fmt.Errorf("view repository: bad primary %q", p.Primary)
	}
	for name, tree := range p.Trees {
		if !util.DeploymentNameOK(name) || !fullID(tree) {
			return fmt.Errorf("view repository: bad pin %q → %q", name, tree)
		}
	}
	return nil
}

// viewConfig is the view repository's whole config (11-contract §10.3).
const viewConfig = `[core]
	repositoryformatversion = 0
	filemode = true
	bare = true
	logAllRefUpdates = false
[gc]
	auto = 0
`

// viewTmp and viewOld are the refresh's neighbours of ViewDir: the fresh
// repository being built, and the previous one while it is swapped out.
// Both are removed before and after every refresh, under the store lock.
func (s *Store) viewTmp(tile string) string {
	return filepath.Join(s.Root, "data", "checkpoints", util.TileKey(tile)+".view.tmp")
}

func (s *Store) viewOld(tile string) string {
	return filepath.Join(s.Root, "data", "checkpoints", util.TileKey(tile)+".view.old")
}

// refreshScript builds a fresh view repository at $V from the store $S: an
// empty bare repository whose HEAD names deploy/<primary>; one fetch of the
// pinned git views, each refspec +refs/xbin/views/<tree>:refs/heads/deploy/
// <name> built by xbind from validated names and ids; update-server-info.
// Only this run fetches, and only over the file transport (06-security T1.3);
// the fetched objects stay one pack.
const refreshScript = `V=$1 S=$2 P=$3
shift 3
hg init -q --bare --template= -b "deploy/$P" "$V" || exit 70
if [ $# -gt 0 ]; then
	hg -c protocol.file.allow=always -c transfer.unpackLimit=1 -c fetch.unpackLimit=1 --git-dir="$V" \
		fetch -q --no-tags --no-write-fetch-head --no-recurse-submodules --no-auto-gc "$S" "$@" || exit 71
fi
hg --git-dir="$V" update-server-info || exit 72
`

var refreshStages = map[int]string{
	70: "creating the view repository",
	71: "fetching the pinned git views",
	72: "update-server-info",
}

// RefreshView rebuilds tile's view repository to show p (L6): the store
// lock, one confined run building a fresh repository beside it (the fresh
// one read-write, the store read-only), then a rename into place (swapIn).
// It needs the tile's store and never creates one (ErrNoStore); every pinned
// tree must be a checkpoint of that store. The view repository exists only
// while the tile has a record: the plane calls this after every committed
// change to which deployments are pinned, to what, or to the primary.
func (s *Store) RefreshView(ctx context.Context, tile string, p Pins) error {
	if !cleanRel(tile) || !singleLine(tile) {
		return fmt.Errorf("view repository: bad tile path %q", tile)
	}
	if err := p.check(); err != nil {
		return err
	}
	if !s.Exists(tile) {
		return fmt.Errorf("%s: %w", tile, ErrNoStore)
	}
	names := make([]string, 0, len(p.Trees))
	for name, tree := range p.Trees {
		if _, err := s.Get(ctx, tile, tree); err != nil {
			return fmt.Errorf("view repository of %s: %s: %w", tile, name, err)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	refspecs := make([]string, 0, len(names))
	for _, name := range names {
		refspecs = append(refspecs, "+refs/xbin/views/"+p.Trees[name]+":refs/heads/deploy/"+name)
	}

	release, err := s.acquire(ctx, tile)
	if err != nil {
		return err
	}
	defer release()
	tmp, old := s.viewTmp(tile), s.viewOld(tile)
	for _, d := range []string{tmp, old} {
		if err := os.RemoveAll(d); err != nil {
			return fmt.Errorf("view repository of %s: %w", tile, err)
		}
	}
	defer os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return fmt.Errorf("view repository of %s: %w", tile, err)
	}
	store := s.Dir(tile)
	args := append([]string{tmp, store, p.Primary}, refspecs...)
	if _, err := s.script(ctx, confine.Cmd{Dir: tmp, Binds: []sandbox.Bind{confine.RO(store)}}, refreshScript, args...); err != nil {
		return fmt.Errorf("view repository of %s: %w", tile, stageError(err, refreshStages))
	}
	if err := pinView(tmp); err != nil {
		return fmt.Errorf("view repository of %s: %w", tile, err)
	}
	return swapIn(tmp, s.ViewDir(tile), old)
}

// pinView writes the view repository's config from the constant and removes
// what it must never have: hooks, an exclude file, alternates, a description.
func pinView(dir string) error {
	if err := fsutil.WriteFileAtomicIn(filepath.Join(dir, "config"), []byte(viewConfig), 0o644); err != nil {
		return err
	}
	for _, rel := range []string{"hooks", "info/exclude", "objects/info/alternates", "objects/info/http-alternates", "description", "FETCH_HEAD"} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if _, err := os.Lstat(p); err == nil {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// swapIn renames the fresh repository tmp to dst, moving a previous one to
// old first and removing it after. A fetch in flight keeps reading the files
// it opened; one that starts between the two renames gets a 404 and retries.
func swapIn(tmp, dst, old string) error {
	had := exists(dst)
	if had {
		if err := os.Rename(dst, old); err != nil {
			return fmt.Errorf("view repository: %w", err)
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		if had {
			_ = os.Rename(old, dst)
		}
		return fmt.Errorf("view repository: %w", err)
	}
	_ = os.RemoveAll(old)
	return nil
}

// RemoveView removes tile's view repository, and a refresh's leftovers, under
// the store lock: opting out (the record goes) and a tile creation that finds
// a record at its path. The store stays.
func (s *Store) RemoveView(ctx context.Context, tile string) error {
	if !cleanRel(tile) || !singleLine(tile) {
		return fmt.Errorf("view repository: bad tile path %q", tile)
	}
	release, err := s.acquire(ctx, tile)
	if err != nil {
		return err
	}
	defer release()
	for _, d := range []string{s.ViewDir(tile), s.viewTmp(tile), s.viewOld(tile)} {
		if err := os.RemoveAll(d); err != nil {
			return fmt.Errorf("view repository of %s: %w", tile, err)
		}
	}
	return nil
}

// ---- serving ----

// ErrNotFetchable: the path isn't a file the checkpoint remote serves (off
// the allow-list, absent, a symlink, not a regular file), or the tile has no
// view repository. The route answers 404; nothing was written.
var ErrNotFetchable = errors.New("not a file of the checkpoint remote")

// fetchable is 11-contract §1.12's allow-list of dumb-HTTP files: HEAD,
// info/refs, objects/info/packs, packs and their indexes, loose objects
// (SHA-1 or SHA-256). Everything else of the repository — config, hooks/,
// description, packed-refs, refs/, objects/info/alternates and
// http-alternates — is never served.
var fetchable = regexp.MustCompile(`^(HEAD|info/refs|objects/info/packs|objects/pack/pack-([0-9a-f]{40}|[0-9a-f]{64})\.(pack|idx)|objects/[0-9a-f]{2}/([0-9a-f]{38}|[0-9a-f]{62}))$`)

// fetchType is the content type git's own dumb-HTTP server gives each kind.
func fetchType(rel string) string {
	switch {
	case strings.HasSuffix(rel, ".pack"):
		return "application/x-git-packed-objects"
	case strings.HasSuffix(rel, ".idx"):
		return "application/x-git-packed-objects-toc"
	case strings.HasPrefix(rel, "objects/") && rel != "objects/info/packs":
		return "application/x-git-loose-object"
	}
	return "text/plain; charset=utf-8"
}

// FetchPathOK reports whether rel is on the checkpoint remote's allow-list.
func FetchPathOK(rel string) bool { return fetchable.MatchString(rel) }

// openFetchable opens rel beneath tile's view repository for serving: on the
// allow-list, a regular file, and reached through no symlink at all — none is
// legitimate in a git directory (NP-06-18). The open resolves nothing: a
// path whose resolution differs from rel is refused, and the final open
// refuses any symlink met on the way (openat2 RESOLVE_NO_SYMLINKS on Linux),
// so a symlink swapped in meanwhile fails the open instead of winning.
func (s *Store) openFetchable(tile, rel string) (*os.File, os.FileInfo, error) {
	if !FetchPathOK(rel) || !cleanRel(tile) {
		return nil, nil, ErrNotFetchable
	}
	f, _, err := fsutil.OpenResolved(s.ViewDir(tile), rel, func(resolved string) bool { return resolved == rel })
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrNotFetchable, err)
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, nil, ErrNotFetchable
	}
	return f, fi, nil
}

// ServeFetch answers one request of the checkpoint remote for tile: rel is
// the git path after "<tile>.git/" (SplitFetchPath). The route authorizes
// first (the tile's own terminal and agent sessions, humans with at least
// write on the tile) and answers 404 for a tile without a record. A path the
// remote doesn't serve returns ErrNotFetchable with nothing written, for the
// route's 404. GET writes the file, HEAD only its headers; ?service= is
// ignored, so git falls back to the dumb protocol. The returned error after
// headers went out is the copy's.
func (s *Store) ServeFetch(w http.ResponseWriter, r *http.Request, tile, rel string) error {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return ErrNotFetchable
	}
	f, fi, err := s.openFetchable(tile, rel)
	if err != nil {
		return err
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", fetchType(rel))
	h.Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return nil
	}
	_, err = io.CopyN(w, f, fi.Size())
	return err
}

// SplitFetchPath splits the checkpoint route's {rest...} — decoded,
// "<tile>.git/<git path>" — at the last segment ending in ".git" whose prefix
// names a tile the remote serves (hasRecord: a registered component with a
// record), since a tile path may itself hold ".git"-suffixed segments
// (11-contract §1.12). The git path must be on the allow-list.
func SplitFetchPath(rest string, hasRecord func(tile string) bool) (tile, rel string, ok bool) {
	segs := strings.Split(rest, "/")
	for i := len(segs) - 2; i >= 0; i-- {
		name, isRepo := strings.CutSuffix(segs[i], ".git")
		if !isRepo || name == "" {
			continue
		}
		t := strings.Join(append(append([]string(nil), segs[:i]...), name), "/")
		r := strings.Join(segs[i+1:], "/")
		if cleanRel(t) && FetchPathOK(r) && hasRecord(t) {
			return t, r, true
		}
	}
	return "", "", false
}
