package runner

// goversions.go — what D166 changed in the versions a Go tile links, and
// the fewest go.mod lines that keep what it had (the upgrade check's pure
// part; goversionscheck.go runs it). Under the workspace's shared go.work
// the go command chose every module's version over every tile's go.mod at
// once (MVS), so a tile linked the highest version any tile's graph reached
// — partly by accident: one tile's indirect requirement could un-prune
// another module's dependencies for everyone. Built with its own go.mod
// (D166), each tile links what its own graph selects, which is lower where
// another tile lifted it. Nothing fails; the binary just links older code.
// The check lists both module sets of a tile's entry and, where its own is
// lower, finds the require lines that restore the old set: greedily, the
// line restoring the most first, verified by listing again.

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/xbin-dev/xbin/internal/deps"
)

// listFormat is `go list -deps -f`'s template: each package's module, its
// version, and its replacement. A standard-library package prints nothing.
const listFormat = `{{with .Module}}{{.Path}} {{.Version}}{{with .Replace}} => {{.Path}} {{.Version}}{{end}}{{end}}`

// modVer is one module a build links: Version "" for a workspace module
// (a main module), Replace its replacement ("path version" or a directory).
type modVer struct {
	Path, Version, Replace string
}

// String is the module as listFormat printed it, trimmed.
func (m modVer) String() string {
	s := strings.TrimSpace(m.Path + " " + m.Version)
	if m.Replace != "" {
		s += " => " + m.Replace
	}
	return s
}

// comparable reports whether m's version says what code it is: a published
// version, not replaced. A workspace module and a replaced one are what the
// build workspace chooses (D166's replace rule), not what MVS selects.
func (m modVer) comparable() bool { return m.Version != "" && m.Replace == "" }

// parseModList reads `go list -deps -f listFormat`'s output: each module
// once, by path.
func parseModList(out []byte) []modVer {
	seen := map[string]bool{}
	var mods []modVer
	for _, line := range strings.Split(string(out), "\n") {
		if m, ok := parseModVer(line); ok && !seen[m.Path] {
			seen[m.Path] = true
			mods = append(mods, m)
		}
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mods
}

// parseModVer reads one line of listFormat's output (or modVer.String's).
func parseModVer(line string) (modVer, bool) {
	left, right, replaced := strings.Cut(line, " => ")
	f := strings.Fields(left)
	if len(f) == 0 || len(f) > 2 {
		return modVer{}, false
	}
	m := modVer{Path: f[0]}
	if len(f) == 2 {
		m.Version = f[1]
	}
	if replaced {
		m.Replace = strings.TrimSpace(right)
		if m.Replace == "" {
			m.Replace = "?"
		}
	}
	return m, true
}

// VersionChange is one module a tile's own build links differently from the
// shared go.work's: at a lower version (Had and Now), no longer (Now ""),
// or newly (Had "").
type VersionChange struct {
	Module string `json:"module"`
	Had    string `json:"had,omitempty"`
	Now    string `json:"now,omitempty"`
}

// diffVersions is what the tile's own build (now) links lower than, apart
// from, or besides what it linked under the shared go.work (had). Modules
// a side doesn't version — a workspace module, a replaced one — and higher
// versions aren't changes the require lines restore: a replace is the
// build workspace's choice (D166), and a higher version is the tile's own.
func diffVersions(had, now []modVer) []VersionChange {
	nowBy := map[string]modVer{}
	for _, m := range now {
		nowBy[m.Path] = m
	}
	hadBy := map[string]modVer{}
	var out []VersionChange
	for _, h := range had {
		hadBy[h.Path] = h
		if !h.comparable() {
			continue
		}
		n, ok := nowBy[h.Path]
		switch {
		case !ok:
			out = append(out, VersionChange{Module: h.Path, Had: h.Version})
		case n.comparable() && compareSemver(n.Version, h.Version) < 0:
			out = append(out, VersionChange{Module: h.Path, Had: h.Version, Now: n.Version})
		}
	}
	for _, n := range now {
		if _, ok := hadBy[n.Path]; !ok && n.comparable() {
			out = append(out, VersionChange{Module: n.Path, Now: n.Version})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Module < out[j].Module })
	return out
}

// pinsFor are the raw differing lines: each module of changes that the
// tile's own build links lower or no longer, at the version it had.
func pinsFor(changes []VersionChange) []deps.Pin {
	var out []deps.Pin
	for _, c := range changes {
		if c.Had != "" {
			out = append(out, deps.Pin{Path: c.Module, Version: c.Had})
		}
	}
	return out
}

// modLister lists the modules the tile's entry links in its own build
// workspace with pins required besides its go.mod.
type modLister func(pins []deps.Pin) ([]modVer, error)

// minimalPins finds the fewest require lines that undo changes, what the
// tile's own build links differently from what it had; measure is what a
// list of its own build still changes. Greedily: each round lists the build
// with each candidate added to the lines chosen so far and keeps the one
// that leaves the fewest changes (one leaving none ends the round), until
// none are left; then each chosen line the others make redundant is
// dropped. Candidates are the raw differing lines (pinsFor), the tile's
// direct requirements first — its own dependency usually lifts the rest
// (one `modernc.org/sqlite` line restores its libc, mathutil, memory, x/sys
// and x/exp). At most budget lists run; when they run out, or no candidate
// changes anything, the raw differing lines are the answer (minimal=false).
func minimalPins(changes []VersionChange, measure func([]modVer) []VersionChange, direct map[string]bool, list modLister, budget int) (pins []deps.Pin, minimal bool) {
	raw := pinsFor(changes)
	if len(raw) == 0 {
		return nil, true
	}
	cands := slices.Clone(raw)
	sort.SliceStable(cands, func(i, j int) bool { return direct[cands[i].Path] && !direct[cands[j].Path] })
	runs := 0
	try := func(p []deps.Pin) ([]VersionChange, bool) {
		if runs >= budget {
			return nil, false
		}
		runs++
		mods, err := list(p)
		if err != nil {
			return nil, false
		}
		return measure(mods), true
	}
	// done: every module it had is back at its version (what it newly links
	// goes with the versions that pulled it in)
	done := func(d []VersionChange) bool { return len(pinsFor(d)) == 0 }
	cur := changes
	var chosen []deps.Pin
	for !done(cur) {
		pending := map[string]string{} // what still differs: module → the version it had
		for _, c := range cur {
			if c.Had != "" {
				pending[c.Module] = c.Had
			}
		}
		best, bestLeft := -1, len(cur)
		var bestDiff []VersionChange
		for i, c := range cands {
			if pending[c.Path] != c.Version {
				continue // restored already
			}
			d, ok := try(append(slices.Clone(chosen), c))
			if !ok {
				if runs >= budget {
					break
				}
				continue
			}
			if len(d) < bestLeft {
				best, bestLeft, bestDiff = i, len(d), d
			}
			if done(d) {
				best, bestDiff = i, d
				break
			}
		}
		if best < 0 {
			return raw, false // no line changes anything, or the budget ran out
		}
		chosen = append(chosen, cands[best])
		cur = bestDiff
	}
	// a line chosen early may be redundant once a later one is in
	for i := len(chosen) - 1; i >= 0 && len(chosen) > 1; i-- {
		without := slices.Delete(slices.Clone(chosen), i, i+1)
		if d, ok := try(without); ok && done(d) {
			chosen = without
		}
	}
	return chosen, true
}

// compareSemver orders two module versions as semver does (the go
// command's order): numbers numerically, a pre-release below its release,
// pre-release identifiers numeric below alphanumeric; build metadata (and
// +incompatible) ignored. A version that doesn't parse sorts lowest.
func compareSemver(a, b string) int {
	pa, okA := parseSemver(a)
	pb, okB := parseSemver(b)
	switch {
	case !okA && !okB:
		return strings.Compare(a, b)
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for i := range 3 {
		if c := compareNum(pa.nums[i], pb.nums[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(pa.pre) == 0 && len(pb.pre) == 0:
		return 0
	case len(pa.pre) == 0:
		return 1
	case len(pb.pre) == 0:
		return -1
	}
	for i := 0; i < len(pa.pre) && i < len(pb.pre); i++ {
		x, y := pa.pre[i], pb.pre[i]
		xn, yn := isNum(x), isNum(y)
		var c int
		switch {
		case xn && yn:
			c = compareNum(x, y)
		case xn:
			c = -1
		case yn:
			c = 1
		default:
			c = strings.Compare(x, y)
		}
		if c != 0 {
			return c
		}
	}
	return compareNum(strconv.Itoa(len(pa.pre)), strconv.Itoa(len(pb.pre)))
}

type semver struct {
	nums [3]string
	pre  []string
}

func parseSemver(v string) (semver, bool) {
	var s semver
	if !strings.HasPrefix(v, "v") {
		return s, false
	}
	v, _, _ = strings.Cut(v[1:], "+")
	core, pre, hasPre := strings.Cut(v, "-")
	f := strings.Split(core, ".")
	if len(f) != 3 {
		return s, false
	}
	for i, n := range f {
		if !isNum(n) {
			return s, false
		}
		s.nums[i] = n
	}
	if hasPre {
		s.pre = strings.Split(pre, ".")
	}
	return s, true
}

func isNum(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// compareNum compares two decimal digit strings by value, any length.
func compareNum(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

// requireLines are pins as the go.mod lines the alert says to add.
func requireLines(pins []deps.Pin) []string {
	out := make([]string, len(pins))
	for i, p := range pins {
		out[i] = p.Path + " " + p.Version
	}
	return out
}

// GoVersionsAffected is one tile the alert names: the lines its go.mod
// needs to keep what it had.
type GoVersionsAffected struct {
	Tile    string
	Require []string // "path version"
}

// goVersionsMessage is the admin alert's text for the tiles it names (at
// least one): the lines to add to each tile's go.mod, tiles needing the
// same lines named together. since is the xbind version the change came
// with.
func goVersionsMessage(since string, tiles []GoVersionsAffected) string {
	if since == "" {
		since = "this xbind"
	}
	why := "(each Go tile now builds with its own go.mod's versions)"
	if len(tiles) == 1 {
		t := tiles[0]
		return fmt.Sprintf("%s builds with older dependency versions since %s %s: add %s to %s's go.mod to keep what it had",
			t.Tile, since, why, andList(quoteRequires(t.Require)), t.Tile)
	}
	type group struct {
		lines []string
		tiles []string
	}
	var groups []*group
	byLines := map[string]*group{}
	for _, t := range tiles {
		k := strings.Join(t.Require, "\n")
		g := byLines[k]
		if g == nil {
			g = &group{lines: t.Require}
			byLines[k] = g
			groups = append(groups, g)
		}
		g.tiles = append(g.tiles, t.Tile)
	}
	sort.SliceStable(groups, func(i, j int) bool { return len(groups[i].tiles) > len(groups[j].tiles) })
	const shown = 6
	var parts []string
	for i, g := range groups {
		if i == shown {
			rest := 0
			for _, h := range groups[shown:] {
				rest += len(h.tiles)
			}
			parts = append(parts, fmt.Sprintf("%d more tile(s): bx doctor lists them", rest))
			break
		}
		where := g.tiles[0] + "'s go.mod"
		if len(g.tiles) > 1 {
			where = "the go.mod of " + andList(g.tiles)
		}
		parts = append(parts, fmt.Sprintf("add %s to %s", andList(quoteRequires(g.lines)), where))
	}
	return fmt.Sprintf("%d Go tiles build with older dependency versions since %s %s: %s, to keep what each had",
		len(tiles), since, why, strings.Join(parts, "; "))
}

func quoteRequires(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = "`require " + l + "`"
	}
	return out
}

// andList joins "a", "a and b", "a, b and c".
func andList(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}
