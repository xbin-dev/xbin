package checkpoint

// capture.go — the work-tree feed (07-runtime §2.2): confined git over the
// store as a private GIT_DIR, with the tile as a read-only work tree (the D77
// technique), in two confined runs around admission.
//
// Run 1 adds the work tree to the store's persistent index (so only files
// whose stat data changed are re-hashed), writes the tree T, lists it for
// admission, and computes the git view V under a scratch index from the
// tile's own ignore rules. Its new objects land only in a per-run quarantine
// (GIT_OBJECT_DIRECTORY, the store as the alternate). Admission then checks
// the listing in xbind. Run 2, only for a tree the store doesn't hold yet,
// moves the quarantined objects into the store inside the sandbox and writes
// the retention and view commits. An unchanged tree costs run 1 alone.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/util"
)

// Source is the work tree a capture or an estimate reads.
type Source struct {
	Tile     string   // the tile path: the store's key and the Xbin-Tile trailer
	WorkTree string   // the tile's directory, absolute
	Nested   []string // tile-relative paths of the registered components nested in the tile: never in its checkpoints
}

func (src Source) check() error {
	if !cleanRel(src.Tile) || !singleLine(src.Tile) {
		return fmt.Errorf("checkpoint: bad tile path %q", src.Tile)
	}
	if !filepath.IsAbs(src.WorkTree) {
		return fmt.Errorf("checkpoint: %s: the work tree must be an absolute path", src.Tile)
	}
	for _, n := range src.Nested {
		if !cleanRel(n) {
			return fmt.Errorf("checkpoint: %s: bad nested component path %q", src.Tile, n)
		}
	}
	return nil
}

// nested reports whether tile-relative rel is a nested component or lies
// beneath one.
func (src Source) nested(rel string) bool {
	for _, n := range src.Nested {
		if rel == n || strings.HasPrefix(rel, n+"/") {
			return true
		}
	}
	return false
}

// CaptureRequest asks for a checkpoint of a work tree.
type CaptureRequest struct {
	Source
	By string // who acts: the principal's From(), the Xbin-By trailer
	// Create lets the capture create the tile's store: a committed opt-in
	// (pausing live reload, adding a deployment) sets it; nothing else may,
	// so a tile without a store gets ErrNoStore (D119c).
	Create bool
	// Background marks a capture xbind takes on its own — a save reaching a
	// live reload target with an assigned branch (D131) — which the tile's
	// rate limits in a bucket of its own, so it never spends the captures
	// people's requests use.
	Background bool
}

// Result is a capture's checkpoint.
type Result struct {
	Checkpoint
	New      bool     // this capture recorded the tree; false: the store held it already
	Warnings []string // what the work tree holds that no checkpoint can (FIFOs, sockets, devices)
	// Branch is the branch the tile's own repository had checked out
	// across this capture: HEAD read before the work tree was read and
	// again after, the same both times; "" for a detached or unreadable
	// HEAD, or one that moved while the capture ran (a checkout raced it).
	// It is this capture's, where Checkpoint.WorkTreeBranch is the tree's
	// first capture's (D131).
	Branch string
}

// Capture checkpoints a work tree into its tile's store (07-runtime §2.2):
// the tile's rate, then its store lock, then two confined runs around
// admission, all within Caps.Time. A refused capture (a *Refusal) leaves the
// store unchanged and drops the persistent index, so the next capture
// re-hashes everything; a refused first capture leaves no store at all.
func (s *Store) Capture(ctx context.Context, req CaptureRequest) (Result, error) {
	if err := req.check(); err != nil {
		return Result{}, err
	}
	if req.By == "" || !singleLine(req.By) {
		return Result{}, fmt.Errorf("checkpoint of %s: bad actor %q", req.Tile, req.By)
	}
	if !req.Create && !s.Exists(req.Tile) { // no capture at all: checked again under the lock
		return Result{}, fmt.Errorf("%s: %w", req.Tile, ErrNoStore)
	}
	c := s.Caps
	if wait, ok := s.take(req.Tile, c, req.Background); !ok {
		return Result{}, &RateLimited{Tile: req.Tile, RetryAfter: wait}
	}
	if c.Time > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Time)
		defer cancel()
	}
	release, err := s.acquire(ctx, req.Tile)
	if err != nil {
		return Result{}, err
	}
	defer release()

	dir := s.Dir(req.Tile)
	had := s.Exists(req.Tile)
	if !had && !req.Create {
		return Result{}, fmt.Errorf("%s: %w", req.Tile, ErrNoStore)
	}
	if !exists(filepath.Join(dir, "index")) {
		// the first capture (or the first after a refusal): refuse an
		// oversized tree from a stat-only walk, before hashing any of it
		est, err := s.Estimate(ctx, req.Source)
		if err != nil {
			return Result{}, err
		}
		if r := c.check(req.Tile, est.Stats); r != nil {
			return Result{}, r
		}
	}
	undo := func() {
		if !had {
			s.uncreate(dir)
		}
	}
	if !had {
		err = s.create(ctx, dir)
	} else {
		err = pin(dir)
	}
	if err != nil {
		undo()
		return Result{}, err
	}
	q, err := quarantine(dir)
	if err != nil {
		undo()
		return Result{}, err
	}
	defer os.RemoveAll(filepath.Join(dir, "quarantine"))
	res, err := s.capture(ctx, dir, q, req, c)
	if err != nil {
		_ = os.Remove(filepath.Join(dir, "index")) // it may name quarantined blobs
		undo()
		return Result{}, err
	}
	return res, nil
}

// quarantine starts this capture's quarantine under dir/quarantine. Any
// quarantine already there is a killed run's: its objects never reached the
// store, and the index may name them, so both go.
func quarantine(dir string) (string, error) {
	root := filepath.Join(dir, "quarantine")
	if exists(root) {
		if err := os.RemoveAll(root); err != nil {
			return "", fmt.Errorf("checkpoint store: %w", err)
		}
		_ = os.Remove(filepath.Join(dir, "index"))
	}
	q := filepath.Join(root, util.RandomToken(8))
	if err := os.MkdirAll(filepath.Join(q, "objects"), 0o755); err != nil {
		return "", fmt.Errorf("checkpoint store: %w", err)
	}
	return q, nil
}

func (s *Store) capture(ctx context.Context, dir, q string, req CaptureRequest, c Caps) (Result, error) {
	head, branch := workTreeHead(req.WorkTree), WorkTreeBranch(req.WorkTree)
	p, err := s.settledPass(ctx, dir, q, req.Source, nil, nil)
	if err != nil && indexBroken(err) {
		// a corrupt index, or one naming objects a killed run never
		// stored: re-hash everything, once
		_ = os.Remove(filepath.Join(dir, "index"))
		p, err = s.settledPass(ctx, dir, q, req.Source, nil, nil)
	}
	if err != nil {
		return Result{}, fmt.Errorf("checkpoint of %s: %w", req.Tile, err)
	}
	if links, unborn := p.gitlinks(), p.unborn(); len(links) > 0 || len(unborn) > 0 {
		// embedded repositories: git recorded a gitlink (or failed on an
		// unborn one), which would drop their files. Run 1 again with
		// each embedded .git masked, so git sees plain directories. From
		// then on the index holds their files and git recurses into them.
		if !confine.Isolated() {
			return Result{}, &Refusal{Tile: req.Tile, Rule: RuleEmbedded,
				Detail: "directories holding a repository of their own are captured as files only under isolation, and a checkpoint never drops them",
				Paths:  firstPaths(append(links, unborn...)), Hint: "run xbind with --isolate, or make them plain directories"}
		}
		masks, err := p.masks(req.Source, q)
		if err != nil {
			return Result{}, err
		}
		if p, err = s.settledPass(ctx, dir, q, req.Source, links, masks); err != nil {
			return Result{}, fmt.Errorf("checkpoint of %s: %w", req.Tile, err)
		}
	}
	if r := p.failures(req.Tile); r != nil {
		return Result{}, r
	}
	if WorkTreeBranch(req.WorkTree) != branch {
		branch = "" // HEAD moved while the work tree was read: no one branch
	}
	res := Result{Warnings: p.warnings(), Branch: branch}
	known := p.known
	if m, ok := known[p.tree]; ok && p.view == "" { // unchanged: admitted when recorded, and run 2 is skipped
		s.remember(req.Tile, known)
		res.Checkpoint = m.checkpoint(p.tree, known)
		return res, nil
	}
	if p.view == "" {
		return Result{}, fmt.Errorf("checkpoint of %s: %s is recorded but not listed", req.Tile, p.tree)
	}
	if _, r := admit(req.Tile, p.listing, c); r != nil {
		return Result{}, r
	}
	m := meta{at: s.now().UTC().Truncate(time.Second), by: req.By, feed: FeedWorkTree, head: head, branch: branch}
	known[p.tree] = m
	if err := s.record(ctx, dir, q, req.Tile, p.tree, p.view, m, shortID(p.tree, known)); err != nil {
		return Result{}, fmt.Errorf("checkpoint of %s: %w", req.Tile, err)
	}
	s.remember(req.Tile, known)
	res.Checkpoint, res.New = m.checkpoint(p.tree, known), true
	return res, nil
}

// captureScript is run 1 (and its re-run for embedded repositories). The
// tile's strings reach git only in files: $Q/rm (index entries to drop:
// nested components, gitlinks being re-added as files) and $Q/add (the
// pathspec: the whole tree minus the nested components). gitignore doesn't
// apply to a checkpoint (-f); it does to the git view (step 5). A tree the
// store holds already was admitted when it was recorded: run 1 prints T
// alone, with no listing and no view to compute (ls-tree -l reads every
// object's header, about half a second on 50 000 loose objects).
const captureScript = `S=$1 W=$2 Q=$3
g() { hg --git-dir="$S" --work-tree="$W" "$@"; }
v() { ( GIT_INDEX_FILE="$Q/view.index"; export GIT_INDEX_FILE; g "$@" ); }
unlock "$S"
if [ -s "$Q/rm" ]; then
	g rm --cached -r -q --ignore-unmatch --pathspec-from-file="$Q/rm" --pathspec-file-nul >/dev/null || exit 91
fi
g add -A -f --ignore-errors --pathspec-from-file="$Q/add" --pathspec-file-nul 2>"$Q/add.err"
if [ $? -gt 1 ]; then cat "$Q/add.err" >&2; exit 92; fi
T=$(g write-tree) || exit 93
find -P "$W" -mindepth 1 \( -name .git -printf 'g%y %P\0' -prune \) -o \( -type p -o -type s -o -type b -o -type c \) -printf 's%y %P\0' >"$Q/scan" 2>/dev/null
g for-each-ref --format='` + knownFormat + `' refs/xbin/checkpoints/ >"$Q/known" || exit 95
if g show-ref -q --verify "refs/xbin/checkpoints/$T"; then echo "$T"; exit 0; fi
g ls-tree -r -t -l -z "$T" >"$Q/listing" || exit 93
v read-tree "$T" || exit 94
v ls-files -z -c -i --exclude-standard >"$Q/ignored" || exit 94
v update-index -z --force-remove --stdin <"$Q/ignored" || exit 94
V=$(v write-tree) || exit 94
printf '%s %s\n' "$T" "$V"
`

var captureStages = map[int]string{
	91: "dropping nested components from the index",
	92: "adding the work tree",
	93: "writing the tree",
	94: "computing the git view",
	95: "listing checkpoints",
}

// pass is what one run 1 found.
type pass struct {
	tree, view string          // view: "" for a tree the store holds already
	addErr     string          // git add's stderr: unreadable files, embedded repositories
	scan       []scanned       // embedded .git entries and special files
	known      map[string]meta // the store's checkpoints before this capture
	listing    []byte          // ls-tree -r -t -l -z of tree
	src        Source
}

type scanned struct {
	git  bool   // a .git entry (else a special file)
	typ  byte   // find's %y: d, f, l, p, s, b, c
	path string // tile-relative
}

// vanishRetries bounds how often run 1 runs again after files vanished
// under git as it read the work tree. An editor saves by writing a
// temporary file and renaming it over the real one; git lists a directory
// before it stats and opens each entry, and meets the temporary file gone:
// a fatal "unable to stat", or "open(…)" and "unable to index file" under
// --ignore-errors. The work tree was being saved to, not unreadable, and
// the next run sees it settled; after the last one the failure stands.
const vanishRetries = 3

// settledPass is pass, run again while files vanish under it.
func (s *Store) settledPass(ctx context.Context, dir, q string, src Source, drop []string, masks []sandbox.Bind) (*pass, error) {
	for i := 0; ; i++ {
		p, err := s.pass(ctx, dir, q, src, drop, masks)
		if i == vanishRetries || !vanished(p, err) || ctx.Err() != nil {
			return p, err
		}
		time.Sleep(time.Duration(10*(i+1)) * time.Millisecond)
	}
}

// vanished reports a run 1 that met a file deleted while git read the work
// tree: git's ENOENT (strerror's capitalization, never Go's) in the add
// stage's failure or in the errors --ignore-errors let it pass.
func vanished(p *pass, err error) bool {
	const enoent = "No such file or directory"
	if err != nil {
		code, ok := confine.ExitCode(err)
		return ok && code == 92 && strings.Contains(err.Error(), enoent)
	}
	return p != nil && strings.Contains(p.addErr, enoent)
}

// pass runs run 1: drop is the gitlinks to re-add as files, masks cover
// their .git entries.
func (s *Store) pass(ctx context.Context, dir, q string, src Source, drop []string, masks []sandbox.Bind) (*pass, error) {
	var add, rm bytes.Buffer
	add.WriteString(":(top)\x00")
	for _, n := range src.Nested {
		add.WriteString(":(top,exclude,literal)" + n + "\x00")
		rm.WriteString(":(top,literal)" + n + "\x00")
	}
	for _, d := range drop {
		rm.WriteString(":(top,literal)" + d + "\x00")
	}
	if err := os.WriteFile(filepath.Join(q, "add"), add.Bytes(), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(q, "rm"), rm.Bytes(), 0o600); err != nil {
		return nil, err
	}
	res, err := s.script(ctx, confine.Cmd{
		Dir:   dir,
		Binds: append([]sandbox.Bind{confine.RO(src.WorkTree)}, masks...),
		Env: []string{"GIT_OBJECT_DIRECTORY=" + filepath.Join(q, "objects"),
			"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(dir, "objects")},
	}, captureScript, dir, src.WorkTree, q)
	if err != nil {
		return nil, stageError(err, captureStages)
	}
	ids := strings.Fields(string(res.Stdout))
	if len(ids) < 1 || len(ids) > 2 || !fullID(ids[0]) || len(ids) == 2 && !fullID(ids[1]) {
		return nil, fmt.Errorf("capture printed %q, not tree ids", bytes.TrimSpace(res.Stdout))
	}
	p := &pass{tree: ids[0], src: src}
	type output struct {
		name  string
		limit int64
		into  *[]byte
	}
	var addErr, scan, known []byte
	outputs := []output{{"add.err", 1 << 20, &addErr}, {"scan", 64 << 20, &scan}, {"known", 64 << 20, &known}}
	if len(ids) == 2 { // a tree to admit
		p.view = ids[1]
		outputs = append(outputs, output{"listing", 256 << 20, &p.listing})
	}
	for _, f := range outputs {
		b, err := readQuarantine(q, f.name, f.limit)
		if err != nil {
			if errors.Is(err, errTooLarge) && f.name == "listing" {
				return nil, &Refusal{Tile: src.Tile, Rule: RuleEntries, Detail: "the tree's listing is over 256 MiB", Hint: sizeHint}
			}
			return nil, err
		}
		*f.into = b
	}
	p.addErr, p.known = string(addErr), parseKnown(known)
	for _, rec := range bytes.Split(scan, []byte{0}) {
		if len(rec) < 4 || rec[2] != ' ' || (rec[0] != 'g' && rec[0] != 's') {
			continue
		}
		e := scanned{git: rec[0] == 'g', typ: rec[1], path: string(rec[3:])}
		if !src.nested(e.path) {
			p.scan = append(p.scan, e)
		}
	}
	return p, nil
}

var errTooLarge = errors.New("too large")

// readQuarantine reads a file a confined run left in the quarantine, beneath
// it (C5: never a following open).
func readQuarantine(q, name string, limit int64) ([]byte, error) {
	f, err := fsutil.OpenBeneath(q, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: %w", name, errTooLarge)
	}
	return b, nil
}

// gitlinks are the listing's embedded repositories (mode 160000).
func (p *pass) gitlinks() []string {
	var out []string
	for _, rec := range bytes.Split(p.listing, []byte{0}) {
		if bytes.HasPrefix(rec, []byte("160000 ")) {
			if _, path, ok := bytes.Cut(rec, []byte{'\t'}); ok {
				out = append(out, string(path))
			}
		}
	}
	return out
}

var unbornLine = regexp.MustCompile(`^error: '(.*)/' does not have a commit checked out$`)

// unborn are the embedded repositories git add failed on: no commit checked
// out, so not even a gitlink.
func (p *pass) unborn() []string {
	var out []string
	for _, line := range strings.Split(p.addErr, "\n") {
		if m := unbornLine.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

// masks hide every embedded .git from git in the re-run: a sealed empty
// tmpfs over a directory, an empty file over a gitfile. A .git that is a
// symlink is not masked (a mount would follow it); if git still takes it for
// a repository, admission refuses the gitlink it leaves.
func (p *pass) masks(src Source, q string) ([]sandbox.Bind, error) {
	var out []sandbox.Bind
	empty := filepath.Join(q, "empty")
	for _, e := range p.scan {
		if !e.git || e.path == ".git" || !cleanRel(e.path) {
			continue
		}
		abs := filepath.Join(src.WorkTree, filepath.FromSlash(e.path))
		switch e.typ {
		case 'd':
			out = append(out, confine.Mask(abs))
		case 'f':
			if !exists(empty) {
				if err := os.WriteFile(empty, nil, 0o444); err != nil {
					return nil, err
				}
			}
			out = append(out, sandbox.Bind{Src: empty, Dst: abs, RO: true})
		}
	}
	return out, nil
}

// failures are what git add couldn't read: fatal to a checkpoint, which
// would otherwise silently miss the files (07-runtime §2.2). Git skips
// sockets, FIFOs and devices silently; those are warnings.
func (p *pass) failures(tile string) *Refusal {
	unborn := map[string]bool{}
	var embedded, unreadable []string
	for _, u := range p.unborn() {
		unborn[u+"/"] = true
		embedded = append(embedded, u)
	}
	for _, line := range strings.Split(p.addErr, "\n") {
		if !strings.HasPrefix(line, "error: ") && !strings.HasPrefix(line, "fatal: ") && !strings.Contains(line, "could not open directory") {
			continue
		}
		if unbornLine.MatchString(line) {
			continue
		}
		path := quoted(line)
		if unborn[path] {
			continue // "unable to index file 'x/'", the unborn repository again
		}
		unreadable = append(unreadable, path)
	}
	switch {
	case len(unreadable) > 0:
		return &Refusal{Tile: tile, Rule: RuleUnreadable, Detail: "files or directories it can't read, which a pinned deployment would silently miss",
			Paths: firstPaths(unreadable), Hint: "make them readable, or move them out of the tile"}
	case len(embedded) > 0:
		return &Refusal{Tile: tile, Rule: RuleEmbedded, Detail: "embedded repositories that can't be captured as files", Paths: firstPaths(embedded),
			Hint: "make them plain directories"}
	}
	return nil
}

var quotedPath = regexp.MustCompile(`'([^']*)'|\("([^"]*)"\)`)

// quoted is the path a git error line names, or the line itself.
func quoted(line string) string {
	if m := quotedPath.FindStringSubmatch(line); m != nil {
		return m[1] + m[2]
	}
	return strings.TrimSpace(line)
}

// firstPaths dedupes and bounds the paths a refusal names.
func firstPaths(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		if p = strings.TrimSuffix(p, "/"); !seen[p] && len(out) < maxPaths {
			seen[p] = true
			out = append(out, shorten(p))
		}
	}
	sort.Strings(out)
	return out
}

// warnings name what the work tree holds that a checkpoint can't: git
// skips sockets, FIFOs and devices by type (07-runtime §2.4).
func (p *pass) warnings() []string {
	var special []string
	for _, e := range p.scan {
		if !e.git {
			special = append(special, e.path)
		}
	}
	if len(special) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("%d special files (FIFOs, sockets, devices) left out: %s", len(special), strings.Join(firstPaths(special), ", "))}
}

// recordScript is run 2: the quarantined objects move into the store inside
// the sandbox, a no-clobber rename each (a directory the store lacks moves
// whole; a symlink moves as a symlink), and whatever stays behind must be in
// the store already. Then the retention and view commits, both refs in one
// transaction.
const recordScript = `S=$1 Q=$2 T=$3 V=$4
g() { hg --git-dir="$S" "$@"; }
unlock "$S"
cd "$Q/objects" || exit 80
set --
for d in ??; do
	[ -d "$d" ] && [ ! -h "$d" ] || continue
	if [ -e "$S/objects/$d" ] || [ -h "$S/objects/$d" ]; then
		mv -n -t "$S/objects/$d" -- "$d"/* 2>/dev/null
	else
		set -- "$@" "$d"
	fi
done
if [ $# -gt 0 ]; then mv -n -t "$S/objects" -- "$@" 2>/dev/null; fi
if [ -d pack ] && [ ! -h pack ]; then
	mkdir -p "$S/objects/pack" || exit 80
	for p in pack/pack-*.pack; do
		[ -f "$p" ] || continue
		b=${p%.pack}
		for x in pack rev idx; do if [ -f "$b.$x" ]; then mv -n -- "$b.$x" "$S/objects/pack/" 2>/dev/null; fi; done
	done
fi
left=$(find . -mindepth 2 ! -type d \( -path './??/*' -o -path './pack/pack-*' \) -print | while IFS= read -r f; do
	[ -e "$S/objects/${f#./}" ] || [ -h "$S/objects/${f#./}" ] || printf ' %s' "${f#./}"
done)
if [ -n "$left" ]; then echo "objects that didn't reach the store:$left" >&2; exit 81; fi
cd "$S" || exit 80
C=$(g commit-tree -F "$Q/msg" "$T") || exit 82
VC=$(g commit-tree -F "$Q/view.msg" "$V") || exit 82
printf 'create refs/xbin/checkpoints/%s %s\nupdate refs/xbin/views/%s %s\n' "$T" "$C" "$T" "$VC" | g update-ref --stdin || exit 83
printf '%s %s\n' "$C" "$VC"
`

var recordStages = map[int]string{
	80: "moving objects into the store",
	81: "moving objects into the store",
	82: "writing the checkpoint commits",
	83: "writing the checkpoint refs",
}

// record is run 2 for tree (view: its git view; id: its short id now).
func (s *Store) record(ctx context.Context, dir, q, tile, tree, view string, m meta, id string) error {
	if !fullID(tree) || !fullID(view) {
		return fmt.Errorf("bad tree ids %q %q", tree, view)
	}
	msg, viewMsg := commitMessages(tile, tree, id, m)
	if err := os.WriteFile(filepath.Join(q, "msg"), []byte(msg), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(q, "view.msg"), []byte(viewMsg), 0o600); err != nil {
		return err
	}
	date := "@" + strconv.FormatInt(m.at.Unix(), 10) + " +0000"
	res, err := s.script(ctx, confine.Cmd{Dir: dir, Env: []string{
		"GIT_AUTHOR_NAME=xbin", "GIT_AUTHOR_EMAIL=xbin@localhost", "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=xbin", "GIT_COMMITTER_EMAIL=xbin@localhost", "GIT_COMMITTER_DATE=" + date,
	}}, recordScript, dir, q, tree, view)
	if err != nil {
		return stageError(err, recordStages)
	}
	if ids := strings.Fields(string(res.Stdout)); len(ids) != 2 || !fullID(ids[0]) || !fullID(ids[1]) {
		return fmt.Errorf("recording printed %q, not two commit ids", bytes.TrimSpace(res.Stdout))
	}
	return nil
}

// commitMessages are a checkpoint's retention and git-view commit messages
// (11-contract §10.3). The view's carries no Xbin-By: it is what a session
// fetches.
func commitMessages(tile, tree, id string, m meta) (string, string) {
	var c, v strings.Builder
	fmt.Fprintf(&c, "checkpoint of %s\n\nXbin-Tile: %s\nXbin-Feed: %s\nXbin-By: %s\nXbin-At: %s\n",
		tile, tile, m.feed, m.by, m.at.UTC().Format(time.RFC3339))
	fmt.Fprintf(&v, "checkpoint %s of %s (git view: ignored files left out)\n\nXbin-Tile: %s\nXbin-Checkpoint: %s\n",
		id, tile, tile, tree)
	if m.head != "" {
		c.WriteString("Xbin-Work-Tree-Head: " + m.head + "\n")
		v.WriteString("Xbin-Work-Tree-Head: " + m.head + "\n")
	}
	if m.branch != "" {
		c.WriteString("Xbin-Work-Tree-Branch: " + m.branch + "\n")
		v.WriteString("Xbin-Work-Tree-Branch: " + m.branch + "\n")
	}
	return c.String(), v.String()
}

// stageError names the step of a script that failed, with the tool's words.
func stageError(err error, stages map[int]string) error {
	if code, ok := confine.ExitCode(err); ok {
		if stage, ok := stages[code]; ok {
			return fmt.Errorf("%s: %w", stage, err)
		}
	}
	return err
}

// indexBroken reports a failure a fresh index fixes: git found the index
// corrupt, or it names an object the store doesn't have.
func indexBroken(err error) bool {
	msg := err.Error()
	for _, m := range []string{"index file corrupt", "bad signature", "index file smaller than expected", "bad index file", "invalid object", "unknown index entry format"} {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// ---- the work tree's HEAD ----

// workTreeHead is the commit the tile's own repository has checked out, for
// the Xbin-Work-Tree-Head trailer: .git/HEAD and the loose or packed ref it
// names, read beneath the work tree, 40 hex digits or "". Best effort and
// untrusted: git never runs on the tile's repository for it (06-security T1),
// and a gitfile is never followed out of the tile.
func workTreeHead(wt string) string {
	head, ok := readBeneath(wt, ".git/HEAD", 4<<10)
	if !ok {
		return ""
	}
	head = strings.TrimSpace(head)
	if len(head) == 40 && fullID(head) {
		return head // detached
	}
	ref, ok := strings.CutPrefix(head, "ref: ")
	if !ok || !refNameOK(ref) {
		return ""
	}
	if id, ok := readBeneath(wt, ".git/"+ref, 4<<10); ok {
		if id = strings.TrimSpace(id); len(id) == 40 && fullID(id) {
			return id
		}
		return ""
	}
	packed, _ := readBeneath(wt, ".git/packed-refs", 16<<20)
	for _, line := range strings.Split(packed, "\n") {
		if id, name, ok := strings.Cut(strings.TrimSpace(line), " "); ok && name == ref && len(id) == 40 && fullID(id) {
			return id
		}
	}
	return ""
}

// WorkTreeBranch is the branch the tile's own repository has checked out
// in work tree wt: .git/HEAD's "ref: refs/heads/<name>", read beneath the
// work tree like workTreeHead, with no git run (D131). "" for a detached
// HEAD, no repository, a gitfile, a name BranchNameOK refuses, or anything
// else unreadable: none of them is a branch. Best effort and untrusted, as
// the trailer is; an assigned branch only ever compares against it.
func WorkTreeBranch(wt string) string {
	head, ok := readBeneath(wt, ".git/HEAD", 4<<10)
	if !ok {
		return ""
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(head), "ref: ")
	if !ok || !refNameOK(ref) {
		return ""
	}
	name, ok := strings.CutPrefix(ref, "refs/heads/")
	if !ok || !BranchNameOK(name) {
		return ""
	}
	return name
}

// BranchNameOK reports a branch name xbind stores, compares and creates:
// what refNameOK takes beneath refs/heads/, never starting with "-" or "."
// (so no tool reads it as an option), at most 200 bytes.
func BranchNameOK(name string) bool {
	return name != "" && len(name) <= 200 && name != "HEAD" && !strings.HasPrefix(name, "-") &&
		!strings.HasPrefix(name, ".") && !strings.HasSuffix(name, ".") && refNameOK("refs/heads/"+name)
}

// BranchExists reports whether the tile's own repository in work tree wt
// has branch name: a loose ref or a packed-refs line, read beneath the work
// tree with no git run. Best effort: git switch -c is what refuses an
// existing name (D131); this lets a dry run say so first.
func BranchExists(wt, name string) bool {
	if !BranchNameOK(name) {
		return false
	}
	ref := "refs/heads/" + name
	if _, ok := readBeneath(wt, ".git/"+ref, 4<<10); ok {
		return true
	}
	packed, _ := readBeneath(wt, ".git/packed-refs", 16<<20)
	for _, line := range strings.Split(packed, "\n") {
		if _, n, ok := strings.Cut(strings.TrimSpace(line), " "); ok && n == ref {
			return true
		}
	}
	return false
}

var refName = regexp.MustCompile(`^refs/[A-Za-z0-9._+/-]+$`)

func refNameOK(ref string) bool {
	return len(ref) <= 1024 && refName.MatchString(ref) && !strings.Contains(ref, "..") && !strings.Contains(ref, "//") &&
		!strings.Contains(ref, "/.") && !strings.HasSuffix(ref, "/") && !strings.HasSuffix(ref, ".lock")
}

// readBeneath reads a small regular file beneath dir, never following a
// symlink out of it (C5).
func readBeneath(dir, rel string, limit int64) (string, bool) {
	f, err := fsutil.OpenBeneath(dir, rel)
	if err != nil {
		return "", false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return "", false
	}
	return string(b), true
}
