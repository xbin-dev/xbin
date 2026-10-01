package runner

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/deps"
)

func TestParseModList(t *testing.T) {
	out := "example.com/b v1.1.0\n\nb \nexample.com/b v1.1.0\n" +
		"github.com/xbin-dev/xbin/sdk v0.0.0 => /opt/xbin/sdk \n" +
		"example.com/f v1.0.0 => example.com/fork v1.0.1\n" +
		"golang.org/x/sys v0.36.0\n"
	got := parseModList([]byte(out))
	want := []modVer{
		{Path: "b"},
		{Path: "example.com/b", Version: "v1.1.0"},
		{Path: "example.com/f", Version: "v1.0.0", Replace: "example.com/fork v1.0.1"},
		{Path: "github.com/xbin-dev/xbin/sdk", Version: "v0.0.0", Replace: "/opt/xbin/sdk"},
		{Path: "golang.org/x/sys", Version: "v0.36.0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseModList\n got %+v\nwant %+v", got, want)
	}
	for _, m := range want { // what the state file keeps reads back the same
		if back, ok := parseModVer(m.String()); !ok || back != m {
			t.Errorf("parseModVer(%q) = %+v", m.String(), back)
		}
	}
}

func TestCompareSemver(t *testing.T) {
	ordered := []string{
		"v0.0.0-20230101000000-aaaaaaaaaaaa",
		"v0.0.0-20250711185948-6ae5c78190dc",
		"v0.9.0",
		"v1.2.3-alpha",
		"v1.2.3-alpha.1",
		"v1.2.3-alpha.beta",
		"v1.2.3-beta.2",
		"v1.2.3-beta.11",
		"v1.2.3-rc.1",
		"v1.2.3",
		"v1.10.0",
		"v2.0.0+incompatible",
		"v10.0.0",
	}
	for i := range ordered {
		for j := range ordered {
			got := compareSemver(ordered[i], ordered[j])
			if (i < j && got >= 0) || (i > j && got <= 0) || (i == j && got != 0) {
				t.Errorf("compareSemver(%s, %s) = %d", ordered[i], ordered[j], got)
			}
		}
	}
	if compareSemver("v1.2.3+meta", "v1.2.3") != 0 {
		t.Error("build metadata counts")
	}
	if compareSemver("garbage", "v0.0.1") >= 0 {
		t.Error("an unparsable version sorts lowest")
	}
}

func TestDiffVersions(t *testing.T) {
	had := []modVer{
		{Path: "tile"}, // the workspace module: not compared
		{Path: "example.com/low", Version: "v1.2.0"},                        // lower now
		{Path: "example.com/same", Version: "v1.0.0"},                       // unchanged
		{Path: "example.com/gone", Version: "v0.3.0"},                       // no longer linked
		{Path: "example.com/up", Version: "v1.0.0"},                         // higher now: the tile's own
		{Path: "example.com/rep", Version: "v1.0.0", Replace: "/elsewhere"}, // another tile's replace (D166 drops it)
		{Path: "example.com/repnow", Version: "v1.5.0"},                     // replaced now: the tile's own replace
	}
	now := []modVer{
		{Path: "tile"},
		{Path: "example.com/low", Version: "v1.1.9"},
		{Path: "example.com/same", Version: "v1.0.0"},
		{Path: "example.com/up", Version: "v1.1.0"},
		{Path: "example.com/rep", Version: "v1.0.0"},
		{Path: "example.com/repnow", Version: "v1.0.0", Replace: "../fork"},
		{Path: "example.com/new", Version: "v0.1.0"}, // newly linked
	}
	want := []VersionChange{
		{Module: "example.com/gone", Had: "v0.3.0"},
		{Module: "example.com/low", Had: "v1.2.0", Now: "v1.1.9"},
		{Module: "example.com/new", Now: "v0.1.0"},
	}
	if got := diffVersions(had, now); !reflect.DeepEqual(got, want) {
		t.Fatalf("diffVersions\n got %+v\nwant %+v", got, want)
	}
	if got := lowered(want); !reflect.DeepEqual(got, want[1:2]) {
		t.Errorf("lowered %+v", got)
	}
	if got := pinsFor(want); !reflect.DeepEqual(got, []deps.Pin{{Path: "example.com/gone", Version: "v0.3.0"}, {Path: "example.com/low", Version: "v1.2.0"}}) {
		t.Errorf("pinsFor %+v", got)
	}
}

// mvs is a little module world for minimalPins: each module version's
// requirements, which are also what its packages import. A build lists
// the versions MVS selects over everything its roots reach (no pruning),
// linked from the tile's imports through the selected versions.
type mvs map[string][]string // "path@version" → "path@version"…

func (w mvs) list(roots, imports []string) []modVer {
	sel := map[string]string{}
	seen := map[string]bool{}
	for q := slices.Clone(roots); len(q) > 0; q = q[1:] {
		if seen[q[0]] {
			continue
		}
		seen[q[0]] = true
		p, v, _ := strings.Cut(q[0], "@")
		if cur, ok := sel[p]; !ok || compareSemver(v, cur) > 0 {
			sel[p] = v
		}
		q = append(q, w[q[0]]...)
	}
	linked := map[string]bool{}
	for q := slices.Clone(imports); len(q) > 0; q = q[1:] {
		if linked[q[0]] {
			continue
		}
		linked[q[0]] = true
		for _, r := range w[q[0]+"@"+sel[q[0]]] {
			p, _, _ := strings.Cut(r, "@")
			q = append(q, p)
		}
	}
	out := []modVer{{Path: "tile"}}
	for p := range linked {
		out = append(out, modVer{Path: p, Version: sel[p]})
	}
	slices.SortFunc(out, func(a, b modVer) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// search runs minimalPins on world w: the tile's own requirements own, the
// shared graph's (every tile's) shared, the tile's imports; it returns the
// lines, minimal, and how many lists the search ran.
func search(t *testing.T, w mvs, own, shared, imports []string, direct map[string]bool, budget int) ([]string, bool, int) {
	t.Helper()
	had := w.list(shared, imports)
	now := w.list(own, imports)
	changes := diffVersions(had, now)
	if len(changes) == 0 {
		t.Fatal("the fixture changes nothing")
	}
	lists := 0
	list := func(pins []deps.Pin) ([]modVer, error) {
		lists++
		roots := slices.Clone(own)
		for _, p := range pins {
			roots = append(roots, p.Path+"@"+p.Version)
		}
		return w.list(roots, imports), nil
	}
	measure := func(m []modVer) []VersionChange { return diffVersions(had, m) }
	pins, minimal := minimalPins(changes, measure, direct, list, budget)
	return requireLines(pins), minimal, lists
}

// The measured pattern of 20 of the owner's 25 affected tiles: a tile
// requiring modernc.org/sqlite v1.34.5 linked v1.39.1 (another tile's)
// with its libc, mathutil, memory, x/sys and x/exp under the shared
// go.work; one line restores all six.
func TestMinimalPinsSqlitePattern(t *testing.T) {
	w := mvs{
		"modernc.org/sqlite@v1.34.5": {"modernc.org/libc@v1.55.3", "modernc.org/mathutil@v1.6.0", "modernc.org/memory@v1.8.0", "golang.org/x/sys@v0.22.0"},
		"modernc.org/sqlite@v1.39.1": {"modernc.org/libc@v1.66.10", "modernc.org/mathutil@v1.7.1", "modernc.org/memory@v1.11.0", "golang.org/x/sys@v0.36.0"},
		"modernc.org/libc@v1.55.3":   {"golang.org/x/exp@v0.0.0-20231108232855-2478ac86f678", "golang.org/x/sys@v0.22.0", "modernc.org/mathutil@v1.6.0", "modernc.org/memory@v1.8.0"},
		"modernc.org/libc@v1.66.10":  {"golang.org/x/exp@v0.0.0-20250620022241-b7579e27df2b", "golang.org/x/sys@v0.36.0", "modernc.org/mathutil@v1.7.1", "modernc.org/memory@v1.11.0"},
		"modernc.org/memory@v1.11.0": {"golang.org/x/sys@v0.33.0"},
	}
	own := []string{"modernc.org/sqlite@v1.34.5", "modernc.org/libc@v1.55.3"}
	shared := append(slices.Clone(own), "modernc.org/sqlite@v1.39.1") // another tile's requirement
	lines, minimal, lists := search(t, w, own, shared, []string{"modernc.org/sqlite"},
		map[string]bool{"modernc.org/sqlite": true}, 24)
	if !minimal || !reflect.DeepEqual(lines, []string{"modernc.org/sqlite v1.39.1"}) {
		t.Fatalf("lines %q minimal %v", lines, minimal)
	}
	if lists != 1 { // the tile's direct requirement is tried first, and restores everything
		t.Errorf("%d lists, want 1", lists)
	}
}

// The accidental half of the shared versions: one tile's indirect x/tools
// line un-pruned libc's x/tools → x/net → x/crypto chain, so every tile
// linked that x/net and x/crypto. A tile requiring none of them directly
// needs the one line that lifts the rest: x/net's. (This world doesn't
// prune, so it checks the search's shape only; the go command's own
// pruning is TestConfinedGoVersionsUnprunedIndirect's.)
func TestMinimalPinsUnprunedIndirect(t *testing.T) {
	w := mvs{
		"golang.org/x/tools@v0.30.0": {"golang.org/x/net@v0.46.0"},
		"golang.org/x/net@v0.38.0":   {"golang.org/x/crypto@v0.36.0", "golang.org/x/text@v0.23.0"},
		"golang.org/x/net@v0.46.0":   {"golang.org/x/crypto@v0.43.0", "golang.org/x/text@v0.30.0"},
	}
	own := []string{"golang.org/x/net@v0.38.0"}
	shared := append(slices.Clone(own), "golang.org/x/tools@v0.30.0") // un-pruned by another tile's indirect line
	lines, minimal, _ := search(t, w, own, shared, []string{"golang.org/x/net", "golang.org/x/crypto"}, nil, 24)
	if !minimal || !reflect.DeepEqual(lines, []string{"golang.org/x/net v0.46.0"}) {
		t.Fatalf("lines %q minimal %v", lines, minimal)
	}
}

// Two independent lifts need two lines; a lower version that pulled in a
// module the old one doesn't (added) and one the old pulled in (dropped)
// are restored with them.
func TestMinimalPinsTwoLinesAddsDrops(t *testing.T) {
	w := mvs{
		"example.com/a@v1.0.0": {"example.com/old@v1.0.0"},
		"example.com/a@v1.1.0": {"example.com/fresh@v1.0.0"},
		"example.com/b@v1.0.0": nil,
		"example.com/b@v1.2.0": nil,
	}
	own := []string{"example.com/a@v1.0.0", "example.com/b@v1.0.0"}
	shared := append(slices.Clone(own), "example.com/a@v1.1.0", "example.com/b@v1.2.0")
	had := w.list(shared, []string{"example.com/a", "example.com/b"})
	now := w.list(own, []string{"example.com/a", "example.com/b"})
	ch := diffVersions(had, now)
	if !slices.ContainsFunc(ch, func(c VersionChange) bool { return c.Module == "example.com/fresh" && c.Now == "" }) ||
		!slices.ContainsFunc(ch, func(c VersionChange) bool { return c.Module == "example.com/old" && c.Had == "" }) {
		t.Fatalf("the fixture's changes: %+v", ch)
	}
	lines, minimal, _ := search(t, w, own, shared, []string{"example.com/a", "example.com/b"}, nil, 24)
	slices.Sort(lines)
	if !minimal || !reflect.DeepEqual(lines, []string{"example.com/a v1.1.0", "example.com/b v1.2.0"}) {
		t.Fatalf("lines %q minimal %v", lines, minimal)
	}
}

// When no line restores anything, or the lists run out, the answer is the
// raw differing lines.
func TestMinimalPinsFallback(t *testing.T) {
	changes := []VersionChange{
		{Module: "example.com/a", Had: "v1.1.0", Now: "v1.0.0"},
		{Module: "example.com/b", Had: "v1.2.0", Now: "v1.0.0"},
		{Module: "example.com/c", Now: "v0.1.0"},
	}
	raw := []string{"example.com/a v1.1.0", "example.com/b v1.2.0"}
	stuck := func([]modVer) []VersionChange { return changes } // nothing a line does shows
	lists := 0
	list := func([]deps.Pin) ([]modVer, error) { lists++; return nil, nil }
	pins, minimal := minimalPins(changes, stuck, nil, list, 24)
	if minimal || !reflect.DeepEqual(requireLines(pins), raw) {
		t.Errorf("stuck: %q %v", requireLines(pins), minimal)
	}
	lists = 0
	pins, minimal = minimalPins(changes, stuck, nil, list, 1)
	if minimal || !reflect.DeepEqual(requireLines(pins), raw) || lists != 1 {
		t.Errorf("budget: %q %v after %d lists", requireLines(pins), minimal, lists)
	}
}

func TestGoVersionsMessage(t *testing.T) {
	one := goVersionsMessage("v0.3.65", []GoVersionsAffected{{Tile: "apps/notes", Require: []string{"modernc.org/sqlite v1.39.1"}}})
	if want := "apps/notes builds with older dependency versions since v0.3.65 (each Go tile now builds with its own go.mod's versions): add `require modernc.org/sqlite v1.39.1` to apps/notes's go.mod to keep what it had"; one != want {
		t.Errorf("one tile:\n got %s\nwant %s", one, want)
	}
	two := goVersionsMessage("v0.3.65", []GoVersionsAffected{{Tile: "apps/x", Require: []string{"golang.org/x/crypto v0.43.0", "golang.org/x/text v0.30.0"}}})
	if !strings.Contains(two, "add `require golang.org/x/crypto v0.43.0` and `require golang.org/x/text v0.30.0` to apps/x's go.mod") {
		t.Errorf("two lines: %s", two)
	}
	many := goVersionsMessage("", []GoVersionsAffected{
		{Tile: "apps/a", Require: []string{"modernc.org/sqlite v1.39.1"}},
		{Tile: "apps/b", Require: []string{"golang.org/x/net v0.46.0"}},
		{Tile: "apps/c", Require: []string{"modernc.org/sqlite v1.39.1"}},
	})
	for _, s := range []string{
		"3 Go tiles build with older dependency versions since this xbind (each Go tile now builds with its own go.mod's versions): ",
		"add `require modernc.org/sqlite v1.39.1` to the go.mod of apps/a and apps/c; add `require golang.org/x/net v0.46.0` to apps/b's go.mod, to keep what each had",
	} {
		if !strings.Contains(many, s) {
			t.Errorf("several tiles: %q lacks %q", many, s)
		}
	}
}
