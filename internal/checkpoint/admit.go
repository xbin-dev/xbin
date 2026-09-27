package checkpoint

// admit.go — the caps and tree hygiene. Admission is the pure check between
// a capture's two confined runs (07-runtime §2.2, 06-security T2): it reads
// the listing run 1 printed (`git ls-tree -r -t -l -z <tree>`), never a blob,
// and refuses a tree that breaks a cap of §2.5 or tree hygiene: a path
// component that is .git in any case, "." or "..", a gitlink, or a mode other
// than a regular file, an executable, a symlink or a directory. A refused
// tree never reaches the store: its objects stay in the quarantine, which is
// removed. The stat-only estimate measures a work tree against the same caps,
// before a tile's first capture and for dry runs (§2.11).

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/xbin-dev/xbin/internal/confine"
)

// The rules a Refusal names.
const (
	RuleEntries     = "entries"
	RuleBytes       = "bytes"
	RuleLargestFile = "largest-file"
	RulePathLen     = "path-length"
	RuleDepth       = "depth"
	RuleHygiene     = "tree-hygiene"
	RuleUnreadable  = "unreadable"
	RuleEmbedded    = "embedded-repository"
)

// Refusal is a capture the store refused. The store is unchanged, and the
// deployment that asked stays on its previous code.
type Refusal struct {
	Tile   string
	Rule   string   // one of the Rule constants
	Detail string   // what broke the rule, with the limit
	Paths  []string // the largest top-level paths (caps), or the paths at fault (at most 20)
	Hint   string   // what to do about it
}

func (r *Refusal) Error() string {
	msg := "checkpoint of " + r.Tile + " refused: " + r.Detail
	if len(r.Paths) > 0 {
		msg += " — " + strings.Join(r.Paths, ", ")
	}
	if r.Hint != "" {
		msg += "; " + r.Hint
	}
	return msg
}

// Is makes errors.Is(err, ErrRefused) hold.
func (r *Refusal) Is(target error) bool { return target == ErrRefused }

// sizeHint follows every size cap's refusal (07-runtime §2.5): there is no
// exclude list, so large data moves out of the code.
const sizeHint = "large data belongs in a resource (per-deployment data) or outside the tile"

// maxPaths bounds the paths a refusal names.
const maxPaths = 20

// Stats measure one tree, or one work tree (Estimate), against Caps.
type Stats struct {
	Entries     int    // files, symlinks and directories
	Bytes       int64  // file (and symlink) content
	Largest     int64  // the largest file
	LargestPath string //
	Depth       int    // directory depth
	DeepestPath string //
	LongestPath string // the longest tile-relative path

	top map[string]*usage // by first path component
}

type usage struct {
	dir     bool
	entries int
	bytes   int64
}

// add counts one entry at tile-relative path p.
func (st *Stats) add(p string, dir bool, size int64) {
	st.Entries++
	parts := strings.Count(p, "/") + 1
	depth := parts
	if !dir {
		depth--
		st.Bytes += size
		if size > st.Largest || st.LargestPath == "" {
			st.Largest, st.LargestPath = size, p
		}
	}
	if depth > st.Depth {
		st.Depth, st.DeepestPath = depth, p
	}
	if len(p) > len(st.LongestPath) {
		st.LongestPath = p
	}
	name, _, nested := strings.Cut(p, "/")
	if st.top == nil {
		st.top = map[string]*usage{}
	}
	u := st.top[name]
	if u == nil {
		u = &usage{}
		st.top[name] = u
	}
	u.dir = u.dir || nested || dir
	u.entries++
	u.bytes += size
}

// Top names the n largest top-level paths, by bytes or by entries, each with
// its size: "node_modules/ (1.2 GiB, 40512 entries)".
func (st Stats) Top(n int, byBytes bool) []string {
	names := make([]string, 0, len(st.top))
	for name := range st.top {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := st.top[names[i]], st.top[names[j]]
		if byBytes && a.bytes != b.bytes {
			return a.bytes > b.bytes
		}
		if a.entries != b.entries {
			return a.entries > b.entries
		}
		return names[i] < names[j]
	})
	var out []string
	for _, name := range names[:min(n, len(names))] {
		u := st.top[name]
		label := name
		if u.dir {
			label += "/"
		}
		out = append(out, fmt.Sprintf("%s (%s, %d entries)", label, humanBytes(u.bytes), u.entries))
	}
	return out
}

// check refuses Stats that break a cap: the first broken one, in the order
// of 07-runtime §2.5's table.
func (c Caps) check(tile string, st Stats) *Refusal {
	size := func(rule, detail string, byBytes bool) *Refusal {
		return &Refusal{Tile: tile, Rule: rule, Detail: detail + "; largest at the top level", Paths: st.Top(5, byBytes), Hint: sizeHint}
	}
	switch {
	case c.Entries > 0 && st.Entries > c.Entries:
		return size(RuleEntries, fmt.Sprintf("%d entries, over the cap of %d", st.Entries, c.Entries), false)
	case c.Bytes > 0 && st.Bytes > c.Bytes:
		return size(RuleBytes, fmt.Sprintf("%s of file content, over the cap of %s", humanBytes(st.Bytes), humanBytes(c.Bytes)), true)
	case c.LargestFile > 0 && st.Largest > c.LargestFile:
		return size(RuleLargestFile, fmt.Sprintf("%s is %s, over the cap of %s for one file", shorten(st.LargestPath), humanBytes(st.Largest), humanBytes(c.LargestFile)), true)
	case c.PathLen > 0 && len(st.LongestPath) > c.PathLen:
		return &Refusal{Tile: tile, Rule: RulePathLen, Detail: fmt.Sprintf("a path of %d bytes, over the cap of %d", len(st.LongestPath), c.PathLen), Paths: []string{shorten(st.LongestPath)}}
	case c.Depth > 0 && st.Depth > c.Depth:
		return &Refusal{Tile: tile, Rule: RuleDepth, Detail: fmt.Sprintf("directories %d deep, over the cap of %d", st.Depth, c.Depth), Paths: []string{shorten(st.DeepestPath)}}
	}
	return nil
}

// admit checks one capture's listing: tree hygiene, then the caps. It is
// the whole of admission; V, the git view, needs no check of its own (its
// blobs are T's, its trees subsets of T's).
func admit(tile string, listing []byte, c Caps) (Stats, *Refusal) {
	var st Stats
	hygiene := func(detail, p string) (Stats, *Refusal) {
		return st, &Refusal{Tile: tile, Rule: RuleHygiene, Detail: detail, Paths: []string{shorten(p)}}
	}
	for len(listing) > 0 {
		var rec []byte
		if i := bytes.IndexByte(listing, 0); i >= 0 {
			rec, listing = listing[:i], listing[i+1:]
		} else {
			rec, listing = listing, nil
		}
		if len(rec) == 0 {
			continue
		}
		head, p, ok := bytes.Cut(rec, []byte{'\t'})
		f := strings.Fields(string(head))
		if !ok || len(f) != 4 || !fullID(f[2]) {
			return hygiene("an unreadable tree listing", string(rec))
		}
		mode, typ, path := f[0], f[1], string(p)
		if bad := badPath(path); bad != "" {
			return hygiene(bad, path)
		}
		switch {
		case mode == "160000":
			return hygiene("a gitlink (a directory with a repository of its own that could not be captured as files)", path)
		case mode == "040000" && typ == "tree":
			st.add(path, true, 0)
		case (mode == "100644" || mode == "100755" || mode == "120000") && typ == "blob":
			size, err := strconv.ParseInt(f[3], 10, 64)
			if err != nil || size < 0 {
				return hygiene("an unreadable tree listing", string(rec))
			}
			st.add(path, false, size)
		default:
			return hygiene("mode "+mode+" "+typ+", which a checkpoint never holds", path)
		}
	}
	if r := c.check(tile, st); r != nil {
		return st, r
	}
	return st, nil
}

// badPath says what is wrong with a tree path, or "" when nothing is: an
// empty, "." or ".." component, or a component that is .git in any case.
func badPath(p string) string {
	if p == "" {
		return "an empty path"
	}
	for _, part := range strings.Split(p, "/") {
		switch {
		case part == "":
			return "an empty path component"
		case part == "." || part == "..":
			return "a path component " + part
		case strings.EqualFold(part, ".git"):
			return "a path component " + part
		}
	}
	return ""
}

// shorten keeps a path readable in an error.
func shorten(p string) string {
	if len(p) > 160 {
		return p[:157] + "..."
	}
	return p
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GiB"
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MiB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " KiB"
	}
	return strconv.FormatInt(n, 10) + " B"
}

// ---- the stat-only estimate ----

// Estimate is the stat-only impact of checkpointing a work tree (07-runtime
// §2.11): what a capture would count, measured without capturing.
type Estimate struct {
	Stats
	Embedded []string // directories holding a repository of their own: captured as files, under isolation
	Special  []string // FIFOs, sockets and devices: no checkpoint holds them
}

// Check refuses what e measured if a capture of it would break c's caps.
func (e Estimate) Check(tile string, c Caps) error {
	if r := c.check(tile, e.Stats); r != nil {
		return r
	}
	return nil
}

// estimateScript walks the work tree without following a symlink and
// without reading a file; .git directories are named and not entered.
const estimateScript = `find -P "$1" -mindepth 1 \( -name .git -printf 'g%y %s %d %P\0' -prune \) -o -printf '%y %s %d %P\0'
`

const estimateMax = 256 << 20

// Estimate measures a work tree for a dry run of pausing live reload or
// adding a deployment, and before a tile's first capture: one confined walk
// with the work tree bound read-only and nothing writable. It needs no
// store, creates none, and doesn't count against the capture rate.
func (s *Store) Estimate(ctx context.Context, src Source) (Estimate, error) {
	if err := src.check(); err != nil {
		return Estimate{}, err
	}
	res, err := s.script(ctx, confine.Cmd{Dir: src.WorkTree, ReadOnlyDir: true, MaxOutput: estimateMax}, estimateScript, src.WorkTree)
	if err != nil {
		if _, exit := confine.ExitCode(err); exit && len(res.Stdout) > 0 {
			// the walk ran and met what it couldn't read
			var lines []string
			for _, l := range strings.Split(string(res.Stderr), "\n") {
				if l = strings.TrimSpace(l); l != "" {
					lines = append(lines, l)
				}
			}
			return Estimate{}, &Refusal{Tile: src.Tile, Rule: RuleUnreadable,
				Detail: "files or directories it can't read, which a pinned deployment would silently miss", Paths: firstPaths(lines)}
		}
		return Estimate{}, fmt.Errorf("measuring %s: %w", src.Tile, err)
	}
	if len(res.Stdout) >= estimateMax {
		return Estimate{}, &Refusal{Tile: src.Tile, Rule: RuleEntries, Detail: "the work tree's listing is over 256 MiB", Hint: sizeHint}
	}
	var e Estimate
	for _, rec := range bytes.Split(res.Stdout, []byte{0}) {
		f := strings.SplitN(string(rec), " ", 4)
		if len(f) != 4 || src.nested(f[3]) {
			continue
		}
		size, _ := strconv.ParseInt(f[1], 10, 64)
		switch typ, p := f[0], f[3]; {
		case strings.HasPrefix(typ, "g"):
			if p != ".git" {
				e.Embedded = append(e.Embedded, strings.TrimSuffix(p, "/.git"))
			}
		case typ == "d":
			e.add(p, true, 0)
		case typ == "f", typ == "l":
			e.add(p, false, size)
		default:
			e.Special = append(e.Special, p)
		}
	}
	return e, nil
}
