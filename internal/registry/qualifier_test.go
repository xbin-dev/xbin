package registry

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/util"
)

// fakeDeployments is a DeploymentLookup over tile → record; a tile without
// one is zero-state: main alone, its primary.
type fakeDeployments map[string]fakeRecord

type fakeRecord struct {
	primary string
	names   []string
}

func (f fakeDeployments) HasRecord(tile string) bool {
	_, ok := f[tile]
	return ok
}

func (f fakeDeployments) HasDeployment(tile, name string) bool {
	rec, ok := f[tile]
	if !ok {
		return name == util.MainDeployment
	}
	return slices.Contains(rec.names, name)
}

func (f fakeDeployments) Primary(tile string) string {
	if rec, ok := f[tile]; ok {
		return rec.primary
	}
	return util.MainDeployment
}

// countingLookup counts the questions ResolveRef asks beyond the primary.
type countingLookup struct {
	DeploymentLookup
	asked int
}

func (c *countingLookup) HasRecord(tile string) bool {
	c.asked++
	return c.DeploymentLookup.HasRecord(tile)
}

func (c *countingLookup) HasDeployment(tile, name string) bool {
	c.asked++
	return c.DeploymentLookup.HasDeployment(tile, name)
}

// qualifierWorkspace is the workspace every qualifier row resolves in, and
// the records its tiles have.
func qualifierWorkspace(t *testing.T) (*Registry, fakeDeployments) {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"apps/crm/index.html":         `crm`,
		"apps/crm/a+b.txt":            `a file whose name holds a plus`,
		"apps/crm/widgets/index.html": `a nested tile, zero-state`,
		"apps/crm+x/notes.txt":        `a directory on disk, not a component`,
		"apps/crm+y":                  `a file on disk`,
		"apps/crm+old/index.html":     `a tile made before crm had "old"`,
		"apps/crm+gone/index.html":    `a tile whose directory goes before the next rescan`,
		"apps/flip/index.html":        `a tile whose primary is dev`,
		"apps/shop/index.html":        `zero-state`,
		"apps/shop/admin/index.html":  `a nested tile with a record`,
		"apps/c+d/index.html":         `a legacy tile whose name holds a plus`,
		"apps/c++/index.html":         `a tile named c++`,
		"site/index.html":             `a zero-state parent tile`,
		"site/blog/index.html":        `its child, with a record`,
	})
	if err := os.Symlink("nowhere", filepath.Join(root, "apps", "crm+z")); err != nil {
		t.Fatal(err)
	}
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "apps", "crm+gone")); err != nil {
		t.Fatal(err)
	}
	two := []string{"main", "dev"}
	return r, fakeDeployments{
		"apps/crm":        {primary: "main", names: []string{"main", "dev", "x", "y", "z", "old", "gone"}},
		"apps/flip":       {primary: "dev", names: two},
		"apps/shop/admin": {primary: "main", names: two},
		"apps/c+d":        {primary: "main", names: two},
		"apps/c++":        {primary: "main", names: two},
		"site/blog":       {primary: "main", names: two},
	}
}

// qualifierRow is one path and ResolveRef's answer for it. today marks a
// path that keeps Resolve's answer (dep is then the primary); errIs, when
// set, is the error the answer matches, and errText its text.
type qualifierRow struct {
	path      string
	today     bool
	comp, dep string
	rest      string
	errIs     error
	errText   string
}

var qualifierRows = []qualifierRow{
	// '+'-free paths: the fast path, today's answer.
	{path: "apps/crm/index.html", today: true, comp: "apps/crm", dep: "main", rest: "index.html"},
	{path: "apps/flip/", today: true, comp: "apps/flip", dep: "dev"},
	{path: "apps/nope/x", today: true, errIs: ErrNoComponent, errText: "no such tile: apps/nope/x"},
	// a '+' that qualifies nothing: today's answer.
	{path: "apps/crm/a+b.txt", today: true, comp: "apps/crm", dep: "main", rest: "a+b.txt"},
	{path: "apps/crm/x+dev/y", today: true, comp: "apps/crm", dep: "main", rest: "x+dev/y"},
	{path: "apps/crm+x/notes.txt", today: true, errIs: ErrNoComponent}, // a directory on disk at the full candidate
	{path: "apps/crm+y", today: true, errIs: ErrNoComponent},           // a file there
	{path: "apps/crm+z/x", today: true, errIs: ErrNoComponent},         // a dangling symlink there: Lstat, never followed
	{path: "apps/crm+old/app.js", today: true, comp: "apps/crm+old", dep: "main", rest: "app.js"},
	{path: "apps/crm+gone/app.js", today: true, comp: "apps/crm+gone", dep: "main", rest: "app.js"}, // registered, though no longer on disk
	{path: "apps/shop+dev/x", today: true, errIs: ErrNoComponent},                                   // a zero-state tile
	{path: "apps/shop+main/", today: true, errIs: ErrNoComponent},                                   // +main in the zero state
	{path: "apps/c+d/app.js", today: true, comp: "apps/c+d", dep: "main", rest: "app.js"},
	{path: "apps/c++/", today: true, comp: "apps/c++", dep: "main"},
	{path: "apps/crm+Dev/x", today: true, errIs: ErrNoComponent},                      // outside the name grammar
	{path: "apps/crm+1dev/x", today: true, errIs: ErrNoComponent},                     // a leading digit
	{path: "apps/crm+" + strings.Repeat("a", 25), today: true, errIs: ErrNoComponent}, // too long
	{path: "+dev/x", today: true, errIs: ErrNoComponent},
	{path: "site/blog+Dev/x", today: true, comp: "site", dep: "main", rest: "blog+Dev/x"},
	{path: "apps/crm/widgets+dev/", today: true, comp: "apps/crm", dep: "main", rest: "widgets+dev"}, // widgets has no record
	{path: "apps/crm+dev/../x", today: true, errIs: ErrNoComponent},                                  // not clean: never splits
	// qualified paths of tiles with a record.
	{path: "apps/crm+dev/app.js", comp: "apps/crm", dep: "dev", rest: "app.js"},
	{path: "apps/crm+dev", comp: "apps/crm", dep: "dev"},
	{path: "/apps/crm+dev/", comp: "apps/crm", dep: "dev"},
	{path: "apps/crm+dev/a+b.txt", comp: "apps/crm", dep: "dev", rest: "a+b.txt"},
	{path: "apps/crm+main/x", comp: "apps/crm", dep: "main", rest: "x"}, // the alias
	{path: "apps/flip+dev/", comp: "apps/flip", dep: "dev"},             // the alias of a reassigned primary
	{path: "apps/flip+main/", comp: "apps/flip", dep: "main"},           // main, no longer the primary
	{path: "apps/c+d+dev/app.js", comp: "apps/c+d", dep: "dev", rest: "app.js"},
	{path: "apps/c+++dev/x", comp: "apps/c++", dep: "dev", rest: "x"},
	{path: "apps/shop/admin+dev/x", comp: "apps/shop/admin", dep: "dev", rest: "x"},
	{path: "site/blog+dev/post.html", comp: "site/blog", dep: "dev", rest: "post.html"},
	// unknown names: 404, never a parent tile's file.
	{path: "apps/crm+nope/x", comp: "apps/crm", dep: "nope", errIs: util.ErrNoDeployment, errText: `apps/crm has no deployment "nope"`},
	{path: "site/blog+nope/x", comp: "site/blog", dep: "nope", errIs: util.ErrNoDeployment, errText: `site/blog has no deployment "nope"`},
	// a qualified path never enters a nested component.
	{path: "apps/crm+dev/widgets/sub/app.js", comp: "apps/crm", dep: "dev", errIs: ErrNestedTile,
		errText: "apps/crm/widgets is a tile of its own; its deployments are /c/apps/crm/widgets+<name>/"},
	{path: "apps/crm+main/widgets/", comp: "apps/crm", dep: "main", errIs: ErrNestedTile},
}

// covers D127j D119c PO-1 — ResolveRef, the deployment URL qualifier (11-contract
// §2.1, §2.2, §2.4). Today's resolution runs first and wins: a path without
// '+', a '+' in a file name, a registered component (even one whose
// directory went before the next rescan) or anything on disk (a directory, a
// file, a dangling symlink) at the full candidate "<tile>+<name>", a tile
// without a record ("+main" in the zero state included), a name outside the
// grammar, "c++", and a path that isn't clean all keep Resolve's answer.
// Only then does the last '+' of a segment name a deployment of a tile with
// a record: "apps/c+d+dev" is (apps/c+d, dev), "<tile>+<primary>" is the
// alias, a nested tile is qualified on its own segment. An unknown name is a
// 404 naming the tile, never a parent tile's file; a qualified path never
// enters a nested component.
func TestResolveDeploymentQualifier(t *testing.T) {
	r, deps := qualifierWorkspace(t)
	for _, row := range qualifierRows {
		t.Run(row.path, func(t *testing.T) {
			c, dep, qualified, rest, err := r.ResolveRef(row.path, deps)
			check := func(what string, got, want any) {
				t.Helper()
				if got != want {
					t.Errorf("%s = %v, want %v", what, got, want)
				}
			}
			check("qualified", qualified, !row.today)
			check("dep", dep, row.dep)
			check("rest", rest, row.rest)
			comp := ""
			if c != nil {
				comp = c.Path
			}
			check("component", comp, row.comp)
			switch {
			case row.errIs == nil && err != nil:
				t.Errorf("err = %v, want none", err)
			case row.errIs != nil && !errors.Is(err, row.errIs):
				t.Errorf("err = %v, want %v", err, row.errIs)
			case row.errText != "" && err != nil && err.Error() != row.errText:
				t.Errorf("err text = %q, want %q", err.Error(), row.errText)
			}
			if !row.today {
				return
			}
			base, baseRest, ok := r.Resolve(row.path)
			if ok != (c != nil) || ok && (base != c || baseRest != rest) {
				t.Errorf("today's answer is (%v, %q, %v); ResolveRef gave (%v, %q)", base, baseRest, ok, c, rest)
			}
		})
	}
}

// covers D119c PO-1 — in the zero state nothing splits: with no lookup, or one
// that knows no record, every row resolves exactly as Resolve does, main the
// primary, and a '+'-free path never asks about records at all (the fast
// path).
func TestResolveRefZeroState(t *testing.T) {
	r, deps := qualifierWorkspace(t)
	for _, d := range []DeploymentLookup{nil, fakeDeployments{}} {
		for _, row := range qualifierRows {
			c, dep, qualified, rest, err := r.ResolveRef(row.path, d)
			base, baseRest, ok := r.Resolve(row.path)
			switch {
			case qualified:
				t.Errorf("%s (lookup %T): qualified in the zero state", row.path, d)
			case ok != (c != nil) || ok && (base != c || baseRest != rest):
				t.Errorf("%s (lookup %T): (%v, %q), today (%v, %q, %v)", row.path, d, c, rest, base, baseRest, ok)
			case ok && (dep != util.MainDeployment || err != nil):
				t.Errorf("%s (lookup %T): dep %q err %v, want main and none", row.path, d, dep, err)
			case !ok && !errors.Is(err, ErrNoComponent):
				t.Errorf("%s (lookup %T): err %v, want ErrNoComponent", row.path, d, err)
			}
		}
	}
	counting := &countingLookup{DeploymentLookup: deps}
	for _, row := range qualifierRows {
		if !strings.Contains(row.path, "+") {
			r.ResolveRef(row.path, counting)
		}
	}
	if counting.asked != 0 {
		t.Errorf("'+'-free paths asked %d questions about records; the fast path asks none", counting.asked)
	}
}
