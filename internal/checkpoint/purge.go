package checkpoint

// purge.go — purging one checkpoint (05-model §2, §10; 06-security T20 and
// ledger L7; NP-06-16): a tile manager's act, which the deployments plane
// authorizes and refuses while any deployment runs the checkpoint. It is
// what GC never does: remove a checkpoint the retention still keeps (a
// roll-back target, one younger than a day) because its content must go,
// such as a secret captured with the work tree.
//
// One confined run on the store, with only the store bound read-write, under
// the store lock that captures, the deploy log and GC take:
//
//   - every deploy-log entry whose tree is the checkpoint is rewritten to name
//     none: the empty tree, and no Xbin-Checkpoint or Xbin-Feed trailer, as an
//     attempt that made no checkpoint is logged. The rest of the entry stays —
//     the attempt, who acted, when, its result, its subject line and the
//     Xbin-Previous of later entries, which are names, not content. A chain is
//     rewritten from its oldest entry, as GC trims one, and its ref moves only
//     from the head it had;
//   - its retention root and its git view are deleted;
//   - the store's persistent index goes: git keeps the objects an index names,
//     and it may name the checkpoint's blobs (the next capture re-hashes the
//     work tree, as after a refused one);
//   - reflogs are expired, the store is repacked (repack -a -d drops packed
//     objects nothing reaches) and loose objects nothing reaches are pruned at
//     once, not after GC's hour of grace: nothing else writes the store while
//     the lock is held.
//
// Then, on the host and still under the lock, its materialized tree goes
// (os.RemoveAll, which never follows a symlink out of it). Objects other
// checkpoints share stay theirs. Archives made earlier are out of reach.

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
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/util"
)

// ErrInUse: a purge named a checkpoint that its caller's keep names — a
// deployment runs it, a deploy of it hasn't finished, a running generation
// binds its tree. Nothing was changed (a 409).
var ErrInUse = errors.New("checkpoint in use")

// PurgeResult is what a purge removed.
type PurgeResult struct {
	Checkpoint Checkpoint // the purged checkpoint, as the store held it
	Entries    int        // deploy-log entries rewritten to name no checkpoint
	Logs       []string   // the deployments whose deploy log named it, sorted
}

// purgeTime bounds one purge, its confined run included (a full repack).
const purgeTime = 10 * time.Minute

// purgeScript is the purge's one confined run (L7), with the store bound
// read-write and nothing else. $T is a validated full tree id. It prints
// "e <n> <deployment>" for each deploy log it rewrote.
const purgeScript = `S=$1 T=$2
set -f
g() { hg --git-dir="$S" "$@"; }
unlock "$S"
g show-ref -q --verify "refs/xbin/checkpoints/$T" || exit 80
E=$(g hash-object -t tree -w --stdin </dev/null) || exit 81
rewrite() {
	old=$(g rev-parse -q --verify "$1^{commit}") || return 0
	hit=
	for tr in $(g log --first-parent --format=%T "$old"); do
		if [ "$tr" = "$T" ]; then hit=1; break; fi
	done
	[ -n "$hit" ] || return 0
	parent= n=0
	for c in $(g rev-list --first-parent --reverse "$old"); do
		{ read -r tree; read -r an; read -r ae; read -r ad; read -r cn; read -r ce; read -r cd; } <<EOF
$(g show -s --date=raw --format='%T%n%an%n%ae%n%ad%n%cn%n%ce%n%cd' "$c")
EOF
		msg=$(g cat-file commit "$c") || return 1
		msg=$(printf '%s\n' "$msg" | sed '1,/^$/d')
		if [ "$tree" = "$T" ]; then
			tree=$E n=$((n+1))
			msg=$(printf '%s\n' "$msg" | sed -e '/^Xbin-Checkpoint: /d' -e '/^Xbin-Feed: /d')
		fi
		new=$(printf '%s\n' "$msg" | (
			export GIT_AUTHOR_NAME="$an" GIT_AUTHOR_EMAIL="$ae" GIT_AUTHOR_DATE="@$ad"
			export GIT_COMMITTER_NAME="$cn" GIT_COMMITTER_EMAIL="$ce" GIT_COMMITTER_DATE="@$cd"
			g commit-tree "$tree" $parent -F -)) || return 1
		parent="-p $new"
	done
	g update-ref "$1" "$new" "$old" || return 1
	echo "e $n ${1#refs/xbin/log/}"
}
for ref in $(g for-each-ref --format='%(refname)' refs/xbin/log/); do
	rewrite "$ref" || exit 82
done
TX="delete refs/xbin/checkpoints/$T
"
if g show-ref -q --verify "refs/xbin/views/$T"; then
	TX="${TX}delete refs/xbin/views/$T
"
fi
printf '%s' "$TX" | g update-ref --stdin || exit 83
rm -f -- "$S/index" || exit 84
g reflog expire --expire=now --all || exit 85
g -c repack.writeBitmaps=false repack -a -d -q || exit 86
g prune --expire=now || exit 87
`

var purgeStages = map[int]string{
	81: "writing the empty tree",
	82: "rewriting a deploy log",
	83: "deleting the checkpoint's refs",
	84: "dropping the store's index",
	85: "expiring reflogs",
	86: "repacking",
	87: "pruning objects",
}

// Purge removes checkpoint tree (a full tree id) from tile's store: from
// every deploy log, its retention root and git view, its objects that no
// other ref reaches, and its materialized tree. keep lists what must stay,
// as GC's does — full tree ids, and the roots running generations bind as
// Materialize returned them (another tile's are ignored) — and is called
// before the store lock is taken; a purge of a checkpoint it names is
// refused with ErrInUse and changes nothing. The caller authorizes the act
// and holds whatever stops a deploy of the checkpoint from starting
// meanwhile (the plane's tile lock).
//
// Purge never creates a store: a tile without one gets ErrNoStore, a
// checkpoint the store doesn't hold ErrUnknownCheckpoint. A run that fails
// after the refs moved leaves the checkpoint gone and its objects to GC; the
// error names the step.
func (s *Store) Purge(ctx context.Context, tile, tree string, keep func() []string) (PurgeResult, error) {
	if !cleanRel(tile) || !singleLine(tile) {
		return PurgeResult{}, fmt.Errorf("checkpoint purge: bad tile path %q", tile)
	}
	if !fullID(tree) {
		return PurgeResult{}, &idError{fmt.Sprintf("%q is not a full checkpoint tree id", tree), ErrBadID}
	}
	if !s.Exists(tile) {
		return PurgeResult{}, fmt.Errorf("%s: %w", tile, ErrNoStore)
	}
	cp, err := s.Get(ctx, tile, tree) // before the lock: a cold list takes it
	if err != nil {
		return PurgeResult{}, err
	}
	trees := s.TreesDir(tile)
	root := filepath.Join(trees, tree)
	if keep != nil {
		for _, k := range keep() {
			if k == tree || filepath.IsAbs(k) && filepath.Clean(k) == root {
				return PurgeResult{}, fmt.Errorf("%s: checkpoint %s is in use: %w", tile, cp.ID, ErrInUse)
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, purgeTime)
	defer cancel()
	release, err := s.acquire(ctx, tile)
	if err != nil {
		return PurgeResult{}, fmt.Errorf("checkpoint purge of %s: %w", tile, err)
	}
	defer release()

	dir := s.Dir(tile)
	if err := pin(dir); err != nil {
		return PurgeResult{}, err
	}
	res, err := s.script(ctx, confine.Cmd{Dir: dir}, purgeScript, dir, tree)
	s.forget(tile) // the checkpoint may be gone whatever the outcome
	// The materialized tree is derived, and nothing keep names binds it:
	// it goes even when the run failed, since its refs may be gone already.
	handedOut.Delete(root)
	rmErr := os.RemoveAll(root)
	if err != nil {
		if code, ok := confine.ExitCode(err); ok && code == 80 {
			return PurgeResult{}, &idError{fmt.Sprintf("%s has no checkpoint %s", tile, cp.ID), ErrUnknownCheckpoint}
		}
		return PurgeResult{}, fmt.Errorf("checkpoint purge of %s: %w", tile, stageError(err, purgeStages))
	}
	repacked.Store(dir, s.now())
	out := PurgeResult{Checkpoint: cp}
	sc := bufio.NewScanner(bytes.NewReader(res.Stdout))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 || f[0] != "e" || !util.DeploymentNameOK(f[2]) {
			continue
		}
		if n, err := strconv.Atoi(f[1]); err == nil && n > 0 {
			out.Entries += n
			out.Logs = append(out.Logs, f[2])
		}
	}
	sort.Strings(out.Logs)
	if rmErr != nil {
		return out, fmt.Errorf("checkpoint purge of %s: removing its materialized tree: %w", tile, rmErr)
	}
	return out, nil
}
