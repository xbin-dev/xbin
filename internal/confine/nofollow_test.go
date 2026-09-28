package confine

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// followingCalls are the host-side calls rule C5 refuses inside a work tree,
// a resource mount, a materialized checkpoint or a quarantine: each follows
// a symlink (and an open blocks on a FIFO) wherever the tree's writer points
// it. fsutil.OpenBeneath/OpenIn, os.Lstat and os.RemoveAll don't follow and
// aren't listed.
var followingCalls = map[string]map[string]bool{
	"os":            {"Open": true, "OpenFile": true, "ReadFile": true, "ReadDir": true, "Stat": true, "Chmod": true, "Chown": true},
	"path/filepath": {"Walk": true, "WalkDir": true, "EvalSymlinks": true},
}

// nofollowScope is where C5 holds in Go code: the packages (every non-test
// file beneath a directory entry) and files that open those trees. An entry
// that doesn't exist yet is skipped, so the guard holds from its first line.
var nofollowScope = []string{
	"internal/checkpoint/",
	"internal/deployments/",
	"internal/registry/deployview.go",
	"internal/server/deployserve.go",
	"internal/broker/deploydata.go",
	"internal/broker/deployseed.go",
	"internal/broker/deployseed_copy.go",
	"internal/broker/backup_restore.go",
	"internal/runner/deploy.go",
}

// walkOK is the escape hatch: `// walk-ok: <why>` on the call's line or one
// of the two above (for example, an xbind-owned file under data/deployments).
var walkOK = regexp.MustCompile(`walk-ok:\s*\S`)

// covers D119g T2 — rule C5 in Go code: no host-side following open, stat,
// chmod, chown or walk inside a work tree, a resource mount, a materialized
// tree or a quarantine.
//
// The static half refuses every followingCalls use — a call, or a reference
// to the function, through any import name — in nofollowScope unless the
// line or the two above say `// walk-ok: <why>`. The behavioural half's
// tripwire is here and proven; it drives each operation in
// NofollowOperations through a hostile tree aimed at the tripwire's FIFO
// (capture today; materialize, the drift count, diff, GC, purge and restore
// join it as they land).
func TestNoFollowingHostWalks(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("guard", func(t *testing.T) { checkFollowingUsesMatcher(t) })

	for _, entry := range nofollowScope {
		t.Run(entry, func(t *testing.T) {
			files, err := nofollowScopeFiles(root, entry)
			if errors.Is(err, fs.ErrNotExist) {
				t.Skipf("%s doesn't exist yet: guarded from its first line", entry)
			}
			if err != nil {
				t.Fatal(err)
			}
			var bad []string
			fset := token.NewFileSet()
			for _, rel := range files {
				src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
				if err != nil {
					t.Fatal(err)
				}
				found, err := followingUses(fset, rel, src)
				if err != nil {
					t.Fatal(err)
				}
				for _, f := range found {
					bad = append(bad, rel+":"+strconv.Itoa(f.line)+": "+f.what)
				}
			}
			if len(bad) > 0 {
				t.Errorf("host-side following calls where C5 holds — open through fsutil.OpenBeneath/OpenIn, "+
					"use os.Lstat, run bulk work in confine, or say why the path is xbind's own with "+
					"`// walk-ok: <why>` on the line or the two above:\n  %s", strings.Join(bad, "\n  "))
			}
		})
	}

	t.Run("behaviour/tripwire", func(t *testing.T) { checkFIFOTripwire(t) })
	t.Run("behaviour/operations", func(t *testing.T) {
		if len(NofollowOperations) == 0 {
			t.Skip("no operation registered in NofollowOperations")
		}
		for _, op := range NofollowOperations {
			t.Run(op.Name, func(t *testing.T) {
				w := newFIFOTripwire(t)
				op.Run(t, w.path)
				if w.tripped.Load() {
					t.Fatalf("%s opened the FIFO outside the trees it was given", op.Name)
				}
			})
		}
	})
}

// NofollowOperation is one operation the behavioural half drives: Run builds
// its hostile trees (symlinks to fifo, FIFOs of their own) and runs the
// operation over them, failing on the operation's error; the half fails if
// the FIFO was opened. The packages that implement the operations import
// confine, so they register from package confine_test
// (nofollow_ops_test.go), which may import them.
type NofollowOperation struct {
	Name string
	Run  func(t *testing.T, fifo string)
}

// NofollowOperations is filled by package confine_test's init.
var NofollowOperations []NofollowOperation

// nofollowScopeFiles lists the non-test Go files (repo-relative, slash-
// separated) of one scope entry: a directory (recursively) or a file.
func nofollowScopeFiles(root, entry string) ([]string, error) {
	p := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(entry, "/")))
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return []string{strings.TrimSuffix(entry, "/")}, nil
	}
	var out []string
	err = filepath.WalkDir(p, func(q string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(q, ".go") || strings.HasSuffix(q, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(root, q)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out, err
}

type followingUse struct {
	line int
	what string
}

// followingUses returns the unannotated uses of followingCalls in one Go
// source file: selector expressions on an import of os or path/filepath
// (whatever its local name; not a local variable shadowing it), and dot
// imports of either, which would hide every call from this guard.
func followingUses(fset *token.FileSet, name string, src []byte) ([]followingUse, error) {
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(src), "\n")
	annotated := func(line int) bool {
		for l := line; l >= line-2 && l >= 1; l-- {
			if walkOK.MatchString(lines[l-1]) {
				return true
			}
		}
		return false
	}
	var out []followingUse
	local := map[string]string{} // import name → path
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		if followingCalls[p] == nil {
			continue
		}
		n := path.Base(p)
		if im.Name != nil {
			n = im.Name.Name
		}
		switch n {
		case "_":
		case ".":
			if line := fset.Position(im.Pos()).Line; !annotated(line) {
				out = append(out, followingUse{line, "dot import of " + p})
			}
		default:
			local[n] = p
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Obj != nil { // Obj is set for a local declaration, never for an import
			return true
		}
		if p, ok := local[id.Name]; ok && followingCalls[p][sel.Sel.Name] {
			if line := fset.Position(sel.Pos()).Line; !annotated(line) {
				out = append(out, followingUse{line, id.Name + "." + sel.Sel.Name})
			}
		}
		return true
	})
	return out, nil
}

// checkFollowingUsesMatcher proves the static half on a file written to
// break it: every line marked FLAG must be reported, and nothing else.
func checkFollowingUsesMatcher(t *testing.T) {
	src := `package x

import (
	"os"
	fp "path/filepath"
	. "path/filepath" // FLAG

	"github.com/xbin-dev/xbin/internal/fsutil"
)

func f(dir string) {
	_, _ = os.Open(dir) // FLAG
	_, _ = os.ReadFile(dir) // walk-ok: an xbind-owned record under data/deployments
	// walk-ok: the store's own config, which xbind writes
	_ = 1
	_, _ = os.Stat(dir)
	// walk-ok: three lines up is too far

	_ = 1
	_ = os.Chmod(dir, 0o755) // FLAG
	_ = fp.WalkDir(dir, nil) // FLAG
	_, _ = fp.EvalSymlinks(dir) // FLAG
	open := os.OpenFile // FLAG
	_ = open
	_, _ = os.Lstat(dir)
	_ = os.RemoveAll(dir)
	_, _ = fsutil.OpenBeneath(dir, "x")
	_, _ = fp.Abs(dir)
	_ = Walk(dir, nil)
	_, _ = os.ReadDir(dir); _ = os.Chown(dir, 0, 0) // FLAG FLAG
	{
		os := struct{ Open func(string) }{}
		os.Open(dir)
	}
}
`
	got, err := followingUses(token.NewFileSet(), "x.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]int{} // line → findings: one per FLAG in its comment
	for i, l := range strings.Split(src, "\n") {
		if _, c, ok := strings.Cut(l, "// FLAG"); ok {
			want[i+1] = 1 + strings.Count(c, "FLAG")
		}
	}
	have := map[int]int{}
	for _, u := range got {
		have[u.line]++
	}
	for l, n := range want {
		if have[l] != n {
			t.Errorf("line %d: %d finding(s), want %d (%v)", l, have[l], n, got)
		}
	}
	for l := range have {
		if want[l] == 0 {
			t.Errorf("line %d reported, but it's allowed (%v)", l, got)
		}
	}
}

// fifoTripwire is the behavioural half's detector: a FIFO outside every
// tree under test, polled with open(O_WRONLY|O_NONBLOCK), which succeeds
// only while something holds the FIFO open for reading (ENXIO otherwise). A
// following open of a symlink to it (os.Open, os.ReadFile, a walk that reads
// what it meets) blocks in open(2) holding it for reading; the probe's open
// lets it through (it reads EOF), so an operation under test never hangs,
// and the trip is recorded.
type fifoTripwire struct {
	path    string
	tripped atomic.Bool
	stop    chan struct{}
	done    chan struct{}
}

func newFIFOTripwire(t *testing.T) *fifoTripwire {
	t.Helper()
	w := &fifoTripwire{path: filepath.Join(t.TempDir(), "tripwire"), stop: make(chan struct{}), done: make(chan struct{})}
	if err := syscall.Mkfifo(w.path, 0o600); err != nil {
		t.Skipf("no FIFOs here: %v", err)
	}
	go func() {
		defer close(w.done)
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-tick.C:
			}
			if fd, err := syscall.Open(w.path, syscall.O_WRONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0); err == nil {
				w.tripped.Store(true) // before the close that lets the reader finish
				syscall.Close(fd)
			}
		}
	}()
	t.Cleanup(func() { close(w.stop); <-w.done })
	return w
}

// checkFIFOTripwire proves the detector: armed, it doesn't trip; a
// following read of a hostile symlink trips it (and returns); OpenBeneath
// on the same link refuses without touching the FIFO.
func checkFIFOTripwire(t *testing.T) {
	w := newFIFOTripwire(t)
	fd, err := syscall.Open(w.path, syscall.O_WRONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err == nil {
		syscall.Close(fd)
		t.Fatal("the FIFO opened for writing with no reader: the probe can't tell a follow from nothing")
	}
	if !errors.Is(err, syscall.ENXIO) {
		t.Fatalf("probe open: %v, want ENXIO", err)
	}

	tree := t.TempDir()
	if err := os.Symlink(w.path, filepath.Join(tree, "link.js")); err != nil {
		t.Fatal(err)
	}
	if f, err := fsutil.OpenBeneath(tree, "link.js"); err == nil {
		f.Close()
		t.Fatal("OpenBeneath followed a symlink out of its tree")
	}
	if w.tripped.Load() {
		t.Fatal("OpenBeneath's refusal tripped the wire")
	}

	read := make(chan error, 1)
	go func() { _, err := os.ReadFile(filepath.Join(tree, "link.js")); read <- err }()
	select {
	case err := <-read:
		if err != nil {
			t.Fatalf("the following read failed instead of blocking on the FIFO: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a following read of the FIFO never returned: the probe didn't let it through")
	}
	if !w.tripped.Load() {
		t.Fatal("a following read of a symlink to the FIFO didn't trip the wire")
	}
}
