package checkpoint

// gc.go — garbage collection and retention (07-runtime §2.8, 06-security
// L7, T10.9). One confined run on the store, under the tile's store lock:
//
//   - each deploy log keeps its newest 50 entries: a longer chain is rewritten
//     to a shorter one (same trees, messages and dates; the oldest kept entry
//     loses its parent);
//   - a checkpoint's retention root (and its git view) stays while the
//     checkpoint is one of keep's, among the last 20 successful entries of a
//     deploy log (roll-back targets), or younger than 24 hours; the rest are
//     deleted;
//   - unreachable objects older than an hour are pruned, and at most once a
//     day the store is repacked (repack -a -d drops what no ref reaches).
//
// Then, on the host, the materialized trees nothing keeps are removed with
// os.RemoveAll, which never follows a symlink (directories are 0755, so no
// chmod walk). A tree stays while it is one of keep's (a running
// generation binds it, or the record points at it), the current or previous
// checkpoint of a deployment (its deploy log's last two distinct successful
// entries), or Materialize handed it out within the last half hour (a deploy
// between materializing and starting, a build binding it). Materialized
// trees are derived and rebuildable; the store's checkpoints are what roll
// back needs. Go artifacts and env layers are the runner's.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
)

// The retention constants of 07-runtime §2.8 (v1).
const (
	logEntriesKept = 50               // deploy-log entries per deployment
	logOKKept      = 20               // successful entries whose checkpoints are roll-back targets
	youngKept      = 24 * time.Hour   // any checkpoint younger than this stays
	treesKept      = 2                // materialized trees per deployment: current and previous
	handoutGrace   = 30 * time.Minute // a tree Materialize returned this recently stays (a Go build runs up to 20 min)
	repackEvery    = 24 * time.Hour   // a full repack at most this often
	gcTime         = 10 * time.Minute // one GC, its confined run included
)

// repacked is when each store (by directory) was last fully repacked by
// this process: the first GC after boot repacks, then at most once a day.
var repacked sync.Map // store dir → time.Time

// gcScript is GC's one confined run, with the store bound read-write and
// nothing else. $KEEP holds keep's checkpoints (validated full ids, xbind's
// own); the logs add their roll-back targets to it. The counts are §2.8's
// constants. It prints "m <tree>" for each deployment's current and
// previous checkpoint, "x <tree>" for each checkpoint it deletes, and "r"
// when it repacked.
const gcScript = `S=$1 CUT=$2 REPACK=$3 ENTRIES=$4 OK=$5 TREES=$6
shift 6
KEEP=" $* "
set -f
g() { hg --git-dir="$S" "$@"; }
trim() {
	old=$(g rev-parse -q --verify "$1^{commit}") || return 0
	[ "$(g rev-list --first-parent --count "$old")" -gt "$ENTRIES" ] || return 0
	parent=
	for c in $(g rev-list --first-parent -n "$ENTRIES" --reverse "$old"); do
		msg=$(g cat-file commit "$c") || return 1
		{ read -r tree; read -r an; read -r ae; read -r ad; read -r cn; read -r ce; read -r cd; } <<EOF
$(g show -s --date=raw --format='%T%n%an%n%ae%n%ad%n%cn%n%ce%n%cd' "$c")
EOF
		new=$(printf '%s\n' "$msg" | sed '1,/^$/d' | (
			export GIT_AUTHOR_NAME="$an" GIT_AUTHOR_EMAIL="$ae" GIT_AUTHOR_DATE="@$ad"
			export GIT_COMMITTER_NAME="$cn" GIT_COMMITTER_EMAIL="$ce" GIT_COMMITTER_DATE="@$cd"
			g commit-tree "$tree" $parent -F -)) || return 1
		parent="-p $new"
	done
	g update-ref "$1" "$new" "$old"
}
unlock "$S"
for ref in $(g for-each-ref --format='%(refname)' refs/xbin/log/); do
	entries=$(g log --first-parent -n "$ENTRIES" --format='%T %(trailers:key=Xbin-Result,valueonly,separator=)' "$ref") || exit 60
	n=0 m=0 mine=" "
	while read -r tr res; do
		[ "$res" = ok ] || continue
		if [ $n -lt "$OK" ]; then KEEP="$KEEP$tr "; n=$((n+1)); fi
		case "$mine" in *" $tr "*) continue ;; esac
		if [ $m -lt "$TREES" ]; then mine="$mine$tr "; m=$((m+1)); echo "m $tr"; fi
	done <<EOF
$entries
EOF
	trim "$ref" || exit 61
done
TX= LIVE=" "
for line in $(g for-each-ref --format='%(refname:lstrip=3):%(committerdate:unix)' refs/xbin/checkpoints/); do
	t=${line%%:*} at=${line#*:}
	case "$KEEP" in *" $t "*) LIVE="$LIVE$t "; continue ;; esac
	if [ "${at:-0}" -ge "$CUT" ] 2>/dev/null; then LIVE="$LIVE$t "; continue; fi
	echo "x $t"
	TX="${TX}delete refs/xbin/checkpoints/$t
"
done
for t in $(g for-each-ref --format='%(refname:lstrip=3)' refs/xbin/views/); do
	case "$LIVE" in *" $t "*) ;; *) TX="${TX}delete refs/xbin/views/$t
" ;; esac
done
if [ -n "$TX" ]; then printf '%s' "$TX" | g update-ref --stdin || exit 62; fi
g reflog expire --expire=now --all || exit 63
g prune --expire=1.hour.ago || exit 64
if [ "$REPACK" = 1 ]; then g -c repack.writeBitmaps=false repack -a -d -q || exit 65; echo r; fi
`

var gcStages = map[int]string{
	60: "reading the deploy logs",
	61: "trimming a deploy log",
	62: "deleting checkpoints",
	63: "expiring reflogs",
	64: "pruning objects",
	65: "repacking",
}

// GC collects tile's checkpoint store and materialized trees (07-runtime
// §2.8): after each successful deploy of the tile, at boot, and on a
// deployment's removal. keep lists what must stay besides the store's own
// retention: the roots running generations bind, as Materialize returned
// them (Runner.RootsInUse; roots of other tiles are ignored), and full tree
// ids, such as the checkpoints the record points at. It is called before
// the store lock is taken, so it may take the runner's locks.
//
// A tile without a store has nothing to collect: its materialized trees
// stay (nothing can tell which ones its deployments run) and only killed
// extractions are swept. GC never creates a store or a directory. A failed
// run leaves every materialized tree in place.
func (s *Store) GC(ctx context.Context, tile string, keep func() []string) error {
	if !cleanRel(tile) || !singleLine(tile) {
		return fmt.Errorf("checkpoint: bad tile path %q", tile)
	}
	trees := s.TreesDir(tile)
	ids := map[string]bool{}
	if keep != nil {
		for _, k := range keep() {
			switch {
			case fullID(k):
				ids[k] = true
			case filepath.IsAbs(k) && filepath.Dir(filepath.Clean(k)) == trees && fullID(filepath.Base(k)):
				ids[filepath.Base(k)] = true
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, gcTime)
	defer cancel()
	release, err := s.acquire(ctx, tile)
	if err != nil {
		return fmt.Errorf("checkpoint GC of %s: %w", tile, err)
	}
	defer release()

	now := s.now()
	graced := func(root string) bool {
		v, ok := handedOut.Load(root)
		return ok && now.Sub(v.(time.Time)) < handoutGrace
	}
	handedOut.Range(func(k, _ any) bool {
		if root := k.(string); filepath.Dir(root) == trees {
			if graced(root) {
				ids[filepath.Base(root)] = true
			} else {
				handedOut.Delete(root)
			}
		}
		return true
	})
	if !s.Exists(tile) {
		if err := sweepTmp(trees); err != nil {
			return fmt.Errorf("checkpoint GC of %s: %w", tile, err)
		}
		return nil
	}
	stay, err := s.collect(ctx, tile, ids, now)
	if err != nil {
		return fmt.Errorf("checkpoint GC of %s: %w", tile, err)
	}
	if err := evict(trees, stay, graced); err != nil {
		return fmt.Errorf("checkpoint GC of %s: %w", tile, err)
	}
	return nil
}

// collect is the confined run on tile's store; it returns the trees whose
// materializations stay: keep's and each deployment's current and previous.
func (s *Store) collect(ctx context.Context, tile string, ids map[string]bool, now time.Time) (map[string]bool, error) {
	dir := s.Dir(tile)
	if err := pin(dir); err != nil {
		return nil, err
	}
	repack := "0"
	if last, ok := repacked.Load(dir); !ok || now.Sub(last.(time.Time)) >= repackEvery {
		repack = "1"
	}
	args := []string{dir, strconv.FormatInt(now.Add(-youngKept).Unix(), 10), repack,
		strconv.Itoa(logEntriesKept), strconv.Itoa(logOKKept), strconv.Itoa(treesKept)}
	stay := map[string]bool{}
	for id := range ids {
		args = append(args, id)
		stay[id] = true
	}
	sort.Strings(args[6:])
	res, err := s.script(ctx, confine.Cmd{Dir: dir}, gcScript, args...)
	s.forget(tile) // checkpoints may be gone whatever the outcome
	if err != nil {
		return nil, stageError(err, gcStages)
	}
	sc := bufio.NewScanner(bytes.NewReader(res.Stdout))
	for sc.Scan() {
		switch f := strings.Fields(sc.Text()); {
		case len(f) == 2 && f[0] == "m" && fullID(f[1]):
			stay[f[1]] = true
		case len(f) == 1 && f[0] == "r":
			repacked.Store(dir, now)
		}
	}
	return stay, sc.Err()
}

// evict removes the materialized trees under trees that stay doesn't name
// and that weren't handed out while the run went (graced, asked again just
// before each removal), and leftover extractions no run of this process
// holds. Only entries named by a full tree id or .tmp-* are touched (d/ is
// per-deployment state).
func evict(trees string, stay map[string]bool, graced func(root string) bool) error {
	names, err := dirNames(trees)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var first error
	for _, e := range names {
		name, p := e.Name(), filepath.Join(trees, e.Name())
		switch {
		case strings.HasPrefix(name, tmpPrefix):
			if _, busy := materializing.Load(p); busy {
				continue
			}
		case fullID(name) && !stay[name] && !graced(p):
			handedOut.Delete(p)
		default:
			continue
		}
		if err := os.RemoveAll(p); err != nil && first == nil {
			first = err
		}
	}
	return first
}
