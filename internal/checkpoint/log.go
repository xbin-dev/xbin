package checkpoint

// log.go — the deploy log (07-runtime §2.3, 11-contract §10.3): one commit
// chain per deployment at refs/xbin/log/<name> in the tile's store, one
// commit per finished attempt, failed ones included. Each entry's tree is the
// attempted checkpoint's (the empty tree when no checkpoint was made), its
// parent the deployment's previous entry, and the attempt is described in
// single-line trailers. The chain keeps every logged checkpoint reachable;
// GC trims it by rewriting the ref to a shorter chain. The log lives in the
// store, so no route serves it and the view repository never holds it.
//
// Only confined git writes and reads it (06-security L17): an append is one
// run under the store lock with the message on stdin, so no attempt's text
// ever reaches argv; a read is one read-only `git log` printing the trailers,
// parsed by git's own trailer parser.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/util"
)

// LogEntry is one finished deploy attempt as the log keeps it: 11-contract
// §1.1's DeployEntry with full tree ids (answers abbreviate them).
type LogEntry struct {
	ID              int64     // the record's deploy id, > 0
	Deployment      string    // the deployment the attempt moved; the chain it is in
	How             string    // one of LogHows
	From            string    // promote: the deployment the code came from
	Checkpoint      string    // the attempted checkpoint's full tree id; "" when no checkpoint was made
	Previous        string    // the checkpoint the deployment ran before; "" for the work tree
	Feed            string    // how the checkpoint was made: FeedWorkTree, or "" without one
	FollowsWorkTree bool      // resume and attach: the deployment follows the work tree afterwards
	By              string    // who acted: the principal's From()
	Via             string    // the principal's Via, verbatim
	Agent           bool      // an agent session's token acted
	Session         string    // the terminal or agent session that acted, if one did
	RequestedAt     time.Time //
	FinishedAt      time.Time // the entry's commit time
	Result          string    // LogOK, LogFailed or LogCancelled
	Error           string    // failed only: the first line, at most MaxLogError bytes
}

// The results a finished attempt has.
const (
	LogOK        = "ok"
	LogFailed    = "failed"
	LogCancelled = "cancelled"
)

// LogHows are the operations an entry names (11-contract §1.1's how).
var LogHows = []string{"deploy", "promote", "rollback", "reload-now", "resume", "pause", "attach", "add", "protect", "reassign", "restart"}

// MaxLogError bounds an entry's error text: its first line, in bytes.
const MaxLogError = 500

// The limits of one read (11-contract §1.10).
const (
	DefaultLogLimit = 50
	MaxLogLimit     = 200
)

// logReadMax bounds what one read prints; GC keeps 50 entries per deployment.
const logReadMax = 32 << 20

// emptyTree is the tree of an entry that has no checkpoint (a refused
// capture): git's empty tree, SHA-1 as the store is.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// ErrBadLogEntry: an entry AppendLog refuses to write (a field it can't keep).
var ErrBadLogEntry = errors.New("bad deploy log entry")

func howOK(how string) bool {
	for _, h := range LogHows {
		if h == how {
			return true
		}
	}
	return false
}

// check validates e before any of it reaches git: names by the deployment
// grammar, ids as full hex, every trailer value single-line.
func (e *LogEntry) check() error {
	bad := func(what string) error { return fmt.Errorf("%w: %s", ErrBadLogEntry, what) }
	switch {
	case e.ID <= 0:
		return bad("no deploy id")
	case !util.DeploymentNameOK(e.Deployment):
		return bad(fmt.Sprintf("deployment %q", e.Deployment))
	case !howOK(e.How):
		return bad(fmt.Sprintf("how %q", e.How))
	case e.From != "" && !util.DeploymentNameOK(e.From):
		return bad(fmt.Sprintf("from %q", e.From))
	case e.Checkpoint != "" && !fullID(e.Checkpoint):
		return bad(fmt.Sprintf("checkpoint %q", e.Checkpoint))
	case e.Previous != "" && !fullID(e.Previous):
		return bad(fmt.Sprintf("previous %q", e.Previous))
	case e.Result != LogOK && e.Result != LogFailed && e.Result != LogCancelled:
		return bad(fmt.Sprintf("result %q (a finished attempt is ok, failed or cancelled)", e.Result))
	case e.By == "":
		return bad("no actor")
	case e.FinishedAt.IsZero():
		return bad("no finish time")
	}
	for name, v := range map[string]string{"feed": e.Feed, "by": e.By, "via": e.Via, "session": e.Session} {
		if !trailerValueOK(v) {
			return bad(fmt.Sprintf("%s %q is not a single-line value", name, v))
		}
	}
	return nil
}

// trailerValueOK reports whether v can be a trailer value that reads back
// unchanged: no control character, no surrounding space.
func trailerValueOK(v string) bool {
	return singleLine(v) && strings.TrimSpace(v) == v && utf8.ValidString(v)
}

// errorLine is what an entry keeps of an attempt's error: its first line,
// control characters dropped, at most MaxLogError bytes on a rune boundary.
func errorLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
	if len(s) > MaxLogError {
		s = s[:MaxLogError]
		for !utf8.ValidString(s) { // a rune torn by the cut
			s = s[:len(s)-1]
		}
	}
	return strings.TrimSpace(s)
}

// logMessage is an entry's commit message (11-contract §10.3).
func logMessage(e LogEntry) string {
	var b strings.Builder
	b.WriteString(e.How)
	if e.Checkpoint != "" {
		b.WriteString(" c:" + e.Checkpoint[:7])
	}
	b.WriteString(" → " + e.Deployment + ": " + e.Result + "\n\n")
	tr := func(k, v string) { b.WriteString("Xbin-" + k + ": " + v + "\n") }
	opt := func(k, v string) {
		if v != "" {
			tr(k, v)
		}
	}
	tr("Deploy-Id", strconv.FormatInt(e.ID, 10))
	tr("Deployment", e.Deployment)
	tr("How", e.How)
	opt("From", e.From)
	opt("Checkpoint", e.Checkpoint)
	opt("Previous", e.Previous)
	opt("Feed", e.Feed)
	tr("Follows-Work-Tree", strconv.FormatBool(e.FollowsWorkTree))
	tr("By", e.By)
	opt("Via", e.Via)
	tr("Agent", strconv.FormatBool(e.Agent))
	opt("Session", e.Session)
	if !e.RequestedAt.IsZero() {
		tr("Requested-At", e.RequestedAt.UTC().Format(time.RFC3339))
	}
	tr("Result", e.Result)
	if e.Result == LogFailed {
		opt("Error", errorLine(e.Error))
	}
	return b.String()
}

// appendScript writes one entry: the tree must be in the store, the parent is
// the chain's head (none for the first entry), and the ref moves only from
// that head (a compare-and-set, though the store lock already serializes).
// The message arrives on stdin.
const appendScript = `S=$1 D=$2 T=$3
g() { hg --git-dir="$S" "$@"; }
unlock "$S"
if [ "$T" = ` + emptyTree + ` ]; then
	g hash-object -t tree -w --stdin </dev/null >/dev/null || exit 60
fi
g cat-file -e "$T^{tree}" || exit 60
P=$(g rev-parse -q --verify "refs/xbin/log/$D^{commit}")
if [ -n "$P" ]; then
	C=$(g commit-tree -p "$P" -F - "$T") || exit 61
else
	C=$(g commit-tree -F - "$T") || exit 61
fi
g update-ref "refs/xbin/log/$D" "$C" "$P" || exit 62
echo "$C"
`

var appendStages = map[int]string{
	60: "finding the attempted checkpoint",
	61: "writing the deploy log entry",
	62: "moving the deploy log",
}

// AppendLog writes one finished attempt to deployment e.Deployment's deploy
// log in tile's store, in one confined run under the store lock (L17). It
// never creates a store: a tile without one gets ErrNoStore. The attempted
// checkpoint must be in the store; an attempt that made none (a refused
// capture) logs the empty tree.
func (s *Store) AppendLog(ctx context.Context, tile string, e LogEntry) error {
	if !cleanRel(tile) || !singleLine(tile) {
		return fmt.Errorf("deploy log: bad tile path %q", tile)
	}
	if err := e.check(); err != nil {
		return err
	}
	if !s.Exists(tile) {
		return fmt.Errorf("%s: %w", tile, ErrNoStore)
	}
	release, err := s.acquire(ctx, tile)
	if err != nil {
		return err
	}
	defer release()
	dir := s.Dir(tile)
	tree := e.Checkpoint
	if tree == "" {
		tree = emptyTree
	}
	date := "@" + strconv.FormatInt(e.FinishedAt.Unix(), 10) + " +0000"
	res, err := s.script(ctx, confine.Cmd{Dir: dir, Stdin: strings.NewReader(logMessage(e)), Env: []string{
		"GIT_AUTHOR_NAME=xbin", "GIT_AUTHOR_EMAIL=xbin@localhost", "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=xbin", "GIT_COMMITTER_EMAIL=xbin@localhost", "GIT_COMMITTER_DATE=" + date,
	}}, appendScript, dir, e.Deployment, tree)
	if err != nil {
		return fmt.Errorf("%s: deploy log of %s: %w", tile, e.Deployment, stageError(err, appendStages))
	}
	if id := strings.TrimSpace(string(res.Stdout)); !fullID(id) {
		return fmt.Errorf("%s: deploy log of %s: the append printed %q, not a commit id", tile, e.Deployment, id)
	}
	return nil
}

// LogQuery selects entries of a tile's deploy log (11-contract §1.10).
type LogQuery struct {
	Deployment string // one deployment's entries; "" for every deployment's
	ID         int64  // > 0: that one entry (Before and Limit don't apply)
	Before     int64  // > 0: only entries with a smaller id
	Limit      int    // at most this many, newest first: 0 means DefaultLogLimit, capped at MaxLogLimit
}

// logFormat prints one entry per commit (-z separates commits): the ref it
// was reached from, its commit time, its tree and its trailers, unit-
// separated (no trailer value holds a control character).
const logFormat = `%S%x1f%ct%x1f%T%x1f%(trailers:only,unfold)`

// logScript reads one deployment's chain (D set) or every chain.
const logScript = `S=$1 D=$2
g() { hg --git-dir="$S" "$@"; }
if [ -n "$D" ]; then
	g show-ref -q --verify "refs/xbin/log/$D" || exit 0
	g log -z --source --first-parent --format='` + logFormat + `' "refs/xbin/log/$D" || exit 65
else
	g log -z --source --format='` + logFormat + `' --glob='refs/xbin/log/*' || exit 65
fi
`

// Log reads tile's deploy log with one confined, read-only `git log` (L17):
// the entries q selects, newest (highest id) first, and whether more match
// past the limit. A tile without a store has no log; reading it creates
// nothing. An entry that doesn't parse is skipped.
func (s *Store) Log(ctx context.Context, tile string, q LogQuery) ([]LogEntry, bool, error) {
	if !cleanRel(tile) || !singleLine(tile) {
		return nil, false, fmt.Errorf("deploy log: bad tile path %q", tile)
	}
	if q.Deployment != "" && !util.DeploymentNameOK(q.Deployment) {
		return nil, false, fmt.Errorf("%w: deployment %q", ErrBadLogEntry, q.Deployment)
	}
	if !s.Exists(tile) {
		return nil, false, nil
	}
	dir := s.Dir(tile)
	res, err := s.script(ctx, confine.Cmd{Dir: dir, ReadOnlyDir: true, MaxOutput: logReadMax + 1}, logScript, dir, q.Deployment)
	if err != nil {
		return nil, false, fmt.Errorf("%s: reading the deploy log: %w", tile, err)
	}
	if len(res.Stdout) > logReadMax {
		return nil, false, fmt.Errorf("%s: the deploy log is over %d MiB: GC trims it", tile, logReadMax>>20)
	}
	all := parseLog(res.Stdout)
	var out []LogEntry
	for _, e := range all {
		switch {
		case q.Deployment != "" && e.Deployment != q.Deployment:
		case q.ID > 0 && e.ID != q.ID:
		case q.ID == 0 && q.Before > 0 && e.ID >= q.Before:
		default:
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if q.ID > 0 {
		return out[:min(len(out), 1)], false, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLogLimit
	}
	limit = min(limit, MaxLogLimit)
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

// parseLog reads logFormat's records. An entry must sit in its own
// deployment's chain and carry an id, a known how and a result.
func parseLog(out []byte) []LogEntry {
	var entries []LogEntry
	for _, rec := range bytes.Split(out, []byte{0}) {
		f := strings.SplitN(strings.TrimLeft(string(rec), "\n"), "\x1f", 4)
		if len(f) != 4 {
			continue
		}
		ref, ok := strings.CutPrefix(f[0], "refs/xbin/log/")
		if !ok {
			continue
		}
		e := LogEntry{}
		if sec, err := strconv.ParseInt(f[1], 10, 64); err == nil {
			e.FinishedAt = time.Unix(sec, 0).UTC()
		}
		for _, line := range strings.Split(f[3], "\n") {
			k, v, ok := strings.Cut(line, ": ")
			if !ok {
				continue
			}
			k, ok = strings.CutPrefix(k, "Xbin-")
			if !ok {
				continue
			}
			v = strings.TrimSpace(v)
			switch k {
			case "Deploy-Id":
				e.ID, _ = strconv.ParseInt(v, 10, 64)
			case "Deployment":
				e.Deployment = v
			case "How":
				e.How = v
			case "From":
				e.From = v
			case "Checkpoint":
				e.Checkpoint = v
			case "Previous":
				e.Previous = v
			case "Feed":
				e.Feed = v
			case "Follows-Work-Tree":
				e.FollowsWorkTree = v == "true"
			case "By":
				e.By = v
			case "Via":
				e.Via = v
			case "Agent":
				e.Agent = v == "true"
			case "Session":
				e.Session = v
			case "Requested-At":
				e.RequestedAt, _ = time.Parse(time.RFC3339, v)
			case "Result":
				e.Result = v
			case "Error":
				e.Error = v
			}
		}
		if e.ID <= 0 || e.Deployment != ref || !howOK(e.How) || e.Result == "" ||
			e.Checkpoint != "" && (!fullID(e.Checkpoint) || e.Checkpoint != f[2]) || e.Previous != "" && !fullID(e.Previous) {
			continue
		}
		entries = append(entries, e)
	}
	return entries
}
