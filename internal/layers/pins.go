package layers

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// The trees of layers under a workspace:
//
//	.xbin/term/<key>/                             a tile's terminal layer (and VM disk)
//	.xbin/sbx/<CK>/<name>.<uid>/cur/              a tile sandbox's state (its stamps)
//	.xbin/sbx/<CK>/<name>.<uid>/snapshots/<sid>/  one of its snapshots
//
// A tile sandbox's state dir is named by its name and its uid (its identity:
// a re-created name gets a new one), and everything that is its state —
// stamps, upper or disk — sits in cur/, which a restore swaps whole
// (plans/tile-sandbox-runtime.md §1.3). A snapshot is a layer of its own: it
// keeps the base it was taken on after its sandbox is reset or rebased, and a
// clone or restore needs that base. `.xbin/sbx/<CK>/.trash` (what a delete,
// reset or restore put aside) and anything else not named `<name>.<uid>`
// isn't a layer and pins nothing. (A non-main deployment's
// `.xbin/deploy/<TK>/d/<d>/sbx/` joins here when its keys exist; §1.4.)
const (
	TreeTerm = "term"
	TreeSbx  = "sbx"
)

// CurDir is the dir in a tile sandbox's state dir that holds its state and
// stamps (what Pin and Stamp take).
const CurDir = "cur"

// SplitStateDir splits a tile sandbox's state dir name, `<name>.<uid>`, at
// its last "." into the sandbox's name and uid (12 lowercase hex). ok=false
// for anything else — `.trash`, a hidden or staging entry, a dir without a
// uid — which isn't a sandbox's state.
func SplitStateDir(dir string) (name, uid string, ok bool) {
	i := strings.LastIndexByte(dir, '.')
	if i <= 0 || strings.HasPrefix(dir, ".") {
		return "", "", false
	}
	name, uid = dir[:i], dir[i+1:]
	if len(uid) != 12 || strings.Trim(uid, "0123456789abcdef") != "" {
		return "", "", false
	}
	return name, uid, true
}

// Layer is one layer dir and what it pins.
type Layer struct {
	Tree     string `json:"tree"`               // TreeTerm | TreeSbx
	Dir      string `json:"dir"`                // the dir holding its stamps (a sandbox's cur/), absolute
	Key      string `json:"key"`                // the terminal key, or the tile's CK
	Sandbox  string `json:"sandbox,omitempty"`  // TreeSbx: the sandbox's name
	UID      string `json:"uid,omitempty"`      // TreeSbx: the sandbox's uid
	Snapshot string `json:"snapshot,omitempty"` // TreeSbx: a snapshot's id
	// Stamps.Base is the effective pin: a terminal layer without a stamp
	// predates them and pins Legacy; an unstamped sandbox dir hasn't
	// started yet and pins nothing ("").
	Stamps
	Err      string `json:"err,omitempty"`      // a stamp couldn't be read
	Resolved string `json:"resolved,omitempty"` // Check: the rootfs dir serving Base
	Missing  bool   `json:"missing,omitempty"`  // Check: Base isn't installed
	Outdated bool   `json:"outdated,omitempty"` // Check: Base isn't the current rootfs's
}

// List enumerates every layer under ws with its stamps. A layer whose stamp
// can't be read is listed with Err; an error return means a tree couldn't be
// read at all (so the list may be missing layers).
func List(ws string) ([]Layer, error) {
	var out []Layer
	var errs []error
	term := filepath.Join(ws, ".xbin", "term")
	keys, err := subdirs(term)
	if err != nil {
		errs = append(errs, err)
	}
	for _, k := range keys {
		// view-* are D40 per-session staged views, not layers.
		if strings.HasPrefix(k, "view-") {
			continue
		}
		l := read(Layer{Tree: TreeTerm, Dir: filepath.Join(term, k), Key: k})
		if l.Base == "" {
			l.Base = Legacy
		}
		out = append(out, l)
	}
	sbx := filepath.Join(ws, ".xbin", "sbx")
	cks, err := subdirs(sbx)
	if err != nil {
		errs = append(errs, err)
	}
	for _, ck := range cks {
		names, err := subdirs(filepath.Join(sbx, ck))
		if err != nil {
			errs = append(errs, err)
		}
		for _, n := range names {
			name, uid, ok := SplitStateDir(n)
			if !ok {
				continue // .trash, or not a sandbox's state: pins nothing
			}
			dir := filepath.Join(sbx, ck, n)
			l := Layer{Tree: TreeSbx, Dir: filepath.Join(dir, CurDir), Key: ck, Sandbox: name, UID: uid}
			// cur/ is xbind's: a missing one (never started) pins nothing,
			// and one swapped for a link or a file is refused, not followed.
			if err := dirOrNone(l.Dir); err != nil {
				l.Err = err.Error()
			} else {
				l = read(l)
			}
			out = append(out, l)
			snaps, err := subdirs(filepath.Join(dir, "snapshots"))
			if err != nil {
				errs = append(errs, err)
			}
			for _, sid := range snaps {
				out = append(out, read(Layer{Tree: TreeSbx, Dir: filepath.Join(dir, "snapshots", sid), Key: ck, Sandbox: name, UID: uid, Snapshot: sid}))
			}
		}
	}
	return out, errors.Join(errs...)
}

func read(l Layer) Layer {
	s, err := Read(l.Dir)
	l.Stamps = s
	if err != nil {
		l.Err = err.Error()
	}
	return l
}

// dirOrNone: dir is a directory (not a symlink to one) or doesn't exist.
func dirOrNone(dir string) error {
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !fi.IsDir():
		return fmt.Errorf("%s: not a directory", dir)
	}
	return nil
}

// subdirs lists dir's sub-directories (not symlinks to one); a missing dir
// has none. dir itself must not be a symlink: these are xbind's own dirs,
// and one swapped for a link is refused rather than walked.
func subdirs(dir string) ([]string, error) {
	if err := dirOrNone(dir); err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Check is List with each pin resolved against rootfs: a report per layer.
func Check(ws, rootfs string) ([]Layer, error) {
	ls, err := List(ws)
	if rootfs == "" {
		return ls, err
	}
	cur := BaseVersion(rootfs)
	for i := range ls {
		l := &ls[i]
		if l.Base == "" {
			continue
		}
		l.Resolved, _ = ResolveBase(rootfs, l.Base)
		l.Missing = l.Resolved == ""
		l.Outdated = l.Base != cur
	}
	return ls, err
}

// Pinned is the union of every base version still pinned under ws: the
// terminal layers' stamps, the tile sandboxes' (cur/) and their snapshots'
// stamps, and extra's — the base of every tile-sandbox definition, archived
// ones included (nil until the runtime has definitions). An error means a
// pin couldn't be read (a stamp, a tree, or extra's source: an unreadable
// definitions file), so the set may be short and nothing may be released on
// it.
func Pinned(ws string, extra func() ([]string, error)) (map[string]bool, error) {
	ls, err := List(ws)
	pins := map[string]bool{}
	var bad []string
	for _, l := range ls {
		if l.Err != "" {
			bad = append(bad, l.Err)
		}
		if l.Base != "" {
			pins[l.Base] = true
		}
	}
	if extra != nil {
		vs, xerr := extra()
		for _, v := range vs {
			if v != "" {
				pins[v] = true
			}
		}
		if xerr != nil {
			err = errors.Join(err, fmt.Errorf("the tile-sandbox definitions' bases: %w", xerr))
		}
	}
	if len(bad) > 0 {
		err = errors.Join(err, fmt.Errorf("unreadable layer stamps: %s", strings.Join(bad, "; ")))
	}
	return pins, err
}

// GC removes preserved base images (`<rootfs>-<version>` siblings) that
// pinned doesn't hold — the release side of keeping old bases, so they don't
// accumulate once every layer has moved on. Conservative: the current base
// stays, and only dirs that carry a base-version stamp are touched.
// pinned == nil means the pins are unknown (Pinned failed): nothing goes.
// Returns the dirs it removed.
func GC(rootfs string, pinned map[string]bool) []string {
	if rootfs == "" || pinned == nil {
		return nil
	}
	cur := BaseVersion(rootfs)
	parent, prefix := filepath.Dir(rootfs), filepath.Base(rootfs)+"-"
	ents, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		ver := strings.TrimPrefix(e.Name(), prefix)
		if ver == cur || pinned[ver] {
			continue
		}
		p := filepath.Join(parent, e.Name())
		if _, err := os.Stat(filepath.Join(p, VersionFile)); err != nil {
			continue // not a base image — leave it alone
		}
		slog.Info("releasing unreferenced base image", "path", p, "version", ver)
		if err := os.RemoveAll(p); err != nil {
			slog.Warn("release a base image", "path", p, "err", err)
			continue
		}
		out = append(out, p)
	}
	return out
}
