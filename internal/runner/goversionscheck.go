package runner

// goversionscheck.go — the upgrade check of D166 (G1). Each Go tile now
// builds with its own go.mod's versions; under the shared go.work it linked
// the highest version any tile's graph reached. Nothing fails, so nothing
// says so: this check lists, for every Go tile, what its entry links under
// the workspace's shared go.work (deps.SharedWork) and under its own build
// workspace (deps.BuildWork) — `go list -deps`, confined and with the
// build's own settings and caches, as the build runs (it is go on tile
// content, D78) — and where its own links lower versions, finds the fewest
// require lines that keep what it had (goversions.go). The result is an
// admin alert naming each such tile with its lines, `bx doctor`'s section,
// and GET /go-build-versions.
//
// It runs once on its own: in the background after the first boot of an
// xbind with it on a workspace that a Go build of an earlier xbind ran on
// (a tile's .xbin/build/<key>/bin, or a checkpoint artifact whose build.json
// records the shared go.work), a few tiles at a time, never ahead of or in
// the way of a build; the state file under data/ is its done-marker. A
// workspace with no such build gets the marker at once. The tiles it
// compares are the Go tiles the workspace had then — a tile added later
// never built with the shared go.work — and what each linked under it is
// kept, its baseline: an admin's re-check (POST /go-build-versions/check)
// compares each tile's own build with its baseline again, listing the
// shared go.work only for a tile that has none yet. A tile's line goes away
// when its own go.mod catches up: a build of a tile the alert names (work
// tree or checkpoint) lists the tile's own build again — when what decides
// its versions changed since it was checked — and compares it with its
// baseline. What it runs is goversionsgo.go's; what it says,
// goversionsreport.go's.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/deps"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
)

// GoVersions is the upgrade check of D166: its state, its runs and the
// alert they make. Boot, then Start; Stop ends what runs.
type GoVersions struct {
	Run     *Runner
	Path    string        // its state file: data/go-build-versions.json
	Version string        // the running xbind's version: the alert's "since"
	Delay   time.Duration // after boot, before the first pass starts
	// Parallel is how many tiles are checked at once (0 = 2); Budget how
	// many lists one tile's search for its lines may run (0 = 24).
	Parallel, Budget int

	// list and probe stand in for Runner.listModules and Runner.probeWork
	// in tests; nil = them.
	list  func(ctx context.Context, c *registry.Component, entry string, gowork []byte, pins []deps.Pin) ([]modVer, error)
	probe func(ctx context.Context, c *registry.Component, gowork []byte) error

	mu      sync.Mutex
	st      goVersionsState
	due     bool            // the first pass hasn't completed
	running bool            // a pass is running
	busy    map[string]bool // tiles being checked
	again   map[string]bool // tiles built again while being re-checked
	sem     chan struct{}
	ctx     context.Context // ends with Stop: every list stops with it
	cancel  context.CancelFunc
	stopped bool
	wg      sync.WaitGroup // every goroutine it started
}

// goVersionsState is the state file.
type goVersionsState struct {
	// Since is the xbind version whose first start found the change: the
	// first with D166 that ran on the workspace.
	Since string `json:"since"`
	// Done marks the first pass complete: it doesn't run on its own again.
	Done bool `json:"done"`
	// Fresh: no Go build of an earlier xbind was found; nothing compared.
	Fresh     bool      `json:"fresh,omitempty"`
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	// Checked are the tiles a pass that hasn't completed has checked: a
	// restart resumes after them.
	Checked []string `json:"checked,omitempty"`
	// Baseline is every tile the check compares — the Go tiles of the
	// workspace when the first pass became due — and what it linked under
	// the shared go.work; null until listed (the pass hasn't reached it, or
	// the list failed).
	Baseline map[string]*goVersionsBase `json:"baseline,omitempty"`
	Tiles    map[string]*goVersionsTile `json:"tiles,omitempty"`
	Errors   map[string]string          `json:"errors,omitempty"`
	// WorkspaceError: the shared go.work itself doesn't load (`go list -m`
	// refuses it), said once rather than for every tile; the tiles it left
	// without a baseline are listed again by the next pass.
	WorkspaceError string `json:"workspaceError,omitempty"`
}

// goVersionsBase is what a tile linked under the shared go.work.
type goVersionsBase struct {
	Had []string `json:"had"` // modVer.String
}

// goVersionsTile is one tile whose own build links older versions.
type goVersionsTile struct {
	Require   []string        `json:"require"` // the go.mod lines that keep what it had: "path version"
	Minimal   bool            `json:"minimal"` // false: the raw differing lines (the search gave up)
	Changes   []VersionChange `json:"changes"`
	CheckedAt time.Time       `json:"checkedAt"`
	Dismissed bool            `json:"dismissed,omitempty"`
	// Inputs is what decided its versions when it was checked (inputsOf):
	// a build with the same inputs re-checks nothing.
	Inputs string `json:"inputs,omitempty"`
}

// GoVersionsAlertKind is the alert's kind in GET /alerts.
const GoVersionsAlertKind = "go-build-versions"

var (
	// ErrGoVersionsUnknownTile: Dismiss named a tile the check doesn't list.
	ErrGoVersionsUnknownTile = errors.New("no Go build versions alert for that tile")
	// ErrGoVersionsNothing: CheckAll on a workspace none of whose Go tiles
	// built with the shared go.work (a fresh one: nothing to compare).
	ErrGoVersionsNothing = errors.New("no Go tile of this workspace built with the shared go.work: nothing to compare")
	errGoVersionsStopped = errors.New("xbind is stopping")
)

// Boot reads the state file and decides whether the first pass is due: no
// state file (or one whose pass didn't complete) and a Go tile built by an
// earlier xbind. A workspace with no such build gets the done-marker at
// once: nothing it runs was built with the shared go.work. The tiles the
// check compares are the Go tiles it has now. Call it before any build can
// run (the boot's registry step).
func (g *GoVersions) Boot() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()
	b, err := os.ReadFile(g.Path)
	switch {
	case err == nil:
		var st goVersionsState
		if err := json.Unmarshal(b, &st); err != nil {
			slog.Warn("go build versions: the state file doesn't parse; checking again", "file", g.Path, "err", err)
			break
		}
		g.st = st
		if !g.st.Done && g.st.Baseline == nil {
			g.st.Baseline = g.goTilesNow()
		}
		g.due = !g.st.Done
		return nil
	case !os.IsNotExist(err):
		return err
	}
	g.st = goVersionsState{Since: g.Version}
	if !g.Run.goBuiltBefore() {
		g.st.Done, g.st.Fresh = true, true
		return g.saveLocked()
	}
	g.st.Baseline = g.goTilesNow()
	g.due = true
	return g.saveLocked() // since is this version, whenever the pass completes
}

// goTilesNow is the baseline set: every Go tile of the workspace (its work
// tree's runtime), none listed yet.
func (g *GoVersions) goTilesNow() map[string]*goVersionsBase {
	set := map[string]*goVersionsBase{}
	for _, c := range g.Run.components() {
		if workTreeView(c).Manifest.Runtime == "go" {
			set[c.Path] = nil
		}
	}
	return set
}

// Start runs the first pass in the background when Boot found it due,
// after Delay.
func (g *GoVersions) Start() {
	g.mu.Lock()
	if !g.due || !g.spawnLocked() {
		g.mu.Unlock()
		return
	}
	ctx := g.ctxLocked()
	g.mu.Unlock()
	go func() {
		defer g.wg.Done()
		t := time.NewTimer(g.Delay)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if g.begin(true) { // not when an admin's pass completed it meanwhile
			g.pass(ctx, true)
		}
	}()
}

// Stop ends the check's passes and re-checks, and waits for them: a list
// that is running is killed, and what it didn't finish isn't recorded (a
// first pass resumes on the next boot).
func (g *GoVersions) Stop() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.stopped = true
	if g.cancel != nil {
		g.cancel()
	}
	g.mu.Unlock()
	g.wg.Wait()
}

// CheckAll runs the check again, in the background: each tile of the
// baseline set compared with what it linked under the shared go.work (that
// listed first for a tile it has no baseline of). false when a pass is
// already running; ErrGoVersionsNothing when there is nothing to compare.
func (g *GoVersions) CheckAll() (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case g.running:
		return false, nil
	case len(g.st.Baseline) == 0:
		return false, ErrGoVersionsNothing
	case !g.spawnLocked():
		return false, errGoVersionsStopped
	}
	g.running = true
	ctx := g.ctxLocked()
	go func() {
		defer g.wg.Done()
		g.pass(ctx, false)
	}()
	return true, nil
}

// begin claims the running pass; first: the automatic first pass, only
// while it is due.
func (g *GoVersions) begin(first bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running || (first && !g.due) {
		return false
	}
	g.running = true
	return true
}

// pass checks each tile of the baseline set, Parallel at a time (g.running
// claimed): what it linked under the shared go.work, when it has no
// baseline yet, what it links with its own, and the lines for each tile
// whose own links older versions. first resumes after the tiles an
// interrupted first pass checked. A pass that runs to its end completes
// the first pass; one Stop ends records nothing more.
func (g *GoVersions) pass(ctx context.Context, first bool) {
	defer func() {
		g.mu.Lock()
		g.running = false
		g.mu.Unlock()
	}()
	g.mu.Lock()
	if !first {
		g.st.Checked = nil
	}
	if g.st.Since == "" {
		g.st.Since = g.Version
	}
	skip := map[string]bool{}
	for _, t := range g.st.Checked {
		skip[t] = true
	}
	var names []string
	for t := range g.st.Baseline {
		if !skip[t] {
			names = append(names, t)
		}
	}
	g.mu.Unlock()
	sort.Strings(names)
	var tiles []*registry.Component
	for _, t := range names {
		if c, ok := g.goTile(t); ok {
			tiles = append(tiles, c)
		}
	}
	slog.Info("go build versions: comparing each Go tile's own build with the shared go.work (D166)", "tiles", len(tiles))
	probes := &sharedProbes{m: map[string]*sharedProbe{}}
	var wg sync.WaitGroup
	for _, c := range tiles {
		if !g.claim(c.Path) {
			continue // a re-check of it is running
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer g.release(c.Path)
			if g.acquire(ctx) != nil {
				return
			}
			e, err := g.compare(ctx, c, probes)
			g.releaseSlot()
			if ctx.Err() != nil {
				return // stopping: not compared
			}
			g.mu.Lock()
			defer g.mu.Unlock()
			if swe := (*sharedWorkError)(nil); errors.As(err, &swe) {
				delete(g.st.Errors, c.Path) // the workspace's, said once
			} else {
				g.record(c.Path, e, err)
			}
			g.st.Checked = append(g.st.Checked, c.Path)
			if err := g.saveLocked(); err != nil {
				slog.Warn("go build versions: saving the state", "err", err)
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked()
	g.st.WorkspaceError = probes.failed()
	g.st.Done, g.st.Checked, g.st.CheckedAt = true, nil, time.Now().UTC()
	g.due = false
	if err := g.saveLocked(); err != nil {
		slog.Warn("go build versions: saving the state", "err", err)
	}
	slog.Info("go build versions: done", "affected", len(g.st.Tiles), "uncompared", len(g.st.Errors), "workspaceError", g.st.WorkspaceError)
}

// pruneLocked drops what the state says of tiles that are gone (g.mu
// held): a tile no longer a Go tile has no line and no error, one the
// registry no longer has no baseline either.
func (g *GoVersions) pruneLocked() {
	if g.Run.Reg == nil {
		return
	}
	for t := range g.st.Tiles {
		if _, ok := g.goTile(t); !ok {
			delete(g.st.Tiles, t)
		}
	}
	for t := range g.st.Errors {
		if _, ok := g.goTile(t); !ok {
			delete(g.st.Errors, t)
		}
	}
	for t := range g.st.Baseline {
		if _, ok := g.Run.Reg.Component(t); !ok {
			delete(g.st.Baseline, t)
		}
	}
}

// goTile is tile as its work tree says, when it is a Go tile the registry
// has.
func (g *GoVersions) goTile(tile string) (*registry.Component, bool) {
	if g.Run.Reg == nil {
		return nil, false
	}
	c, ok := g.Run.Reg.Component(tile)
	if !ok {
		return nil, false
	}
	c = workTreeView(c)
	return c, c.Manifest.Runtime == "go"
}

// record stores a tile's result (g.mu held): an entry keeps its dismissal
// while its lines stay the same.
func (g *GoVersions) record(tile string, e *goVersionsTile, err error) {
	if err != nil {
		if g.st.Errors == nil {
			g.st.Errors = map[string]string{}
		}
		g.st.Errors[tile] = err.Error()
		return // what it said before stands
	}
	delete(g.st.Errors, tile)
	if e == nil {
		delete(g.st.Tiles, tile)
		return
	}
	if old := g.st.Tiles[tile]; old != nil && old.Dismissed && slices.Equal(old.Require, e.Require) {
		e.Dismissed = true
	}
	if g.st.Tiles == nil {
		g.st.Tiles = map[string]*goVersionsTile{}
	}
	g.st.Tiles[tile] = e
}

// spawnLocked counts a goroutine the check starts; false once it stopped
// (g.mu held).
func (g *GoVersions) spawnLocked() bool {
	if g.stopped {
		return false
	}
	g.wg.Add(1)
	return true
}

// ctxLocked is the check's context, which Stop ends (g.mu held).
func (g *GoVersions) ctxLocked() context.Context {
	if g.ctx == nil {
		g.ctx, g.cancel = context.WithCancel(context.Background())
	}
	return g.ctx
}

// acquire takes one of Parallel slots; an error once ctx ends.
func (g *GoVersions) acquire(ctx context.Context) error {
	g.mu.Lock()
	if g.sem == nil {
		n := g.Parallel
		if n <= 0 {
			n = 2
		}
		g.sem = make(chan struct{}, n)
	}
	sem := g.sem
	g.mu.Unlock()
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *GoVersions) releaseSlot() { <-g.sem }

// claim marks tile busy; false when it already is (a later build is noted
// in again for a re-check).
func (g *GoVersions) claim(tile string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()
	if g.busy[tile] {
		return false
	}
	g.busy[tile] = true
	return true
}

// initLocked makes the maps a GoVersions that wasn't booted lacks (g.mu held).
func (g *GoVersions) initLocked() {
	if g.busy == nil {
		g.busy, g.again = map[string]bool{}, map[string]bool{}
	}
}

func (g *GoVersions) release(tile string) {
	g.mu.Lock()
	delete(g.busy, tile)
	again := g.again[tile]
	delete(g.again, tile)
	g.mu.Unlock()
	if again {
		g.Built(tile)
	}
}

// Built is the runner's word that one of tile's builds succeeded — of its
// work tree or of a checkpoint: when the alert names the tile and what
// decides its versions (its build workspace's go.work and the go.mod of
// each module it uses) changed since it was checked, its own build is
// listed again in the background and compared with its baseline — the line
// goes away once its go.mod caught up. The work tree is what is listed:
// its go.mod is what the alert says to change, and what a pinned primary's
// next deployment builds. Never blocks the build.
func (g *GoVersions) Built(tile string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.initLocked()
	e, base := g.st.Tiles[tile], g.st.Baseline[tile]
	if e == nil || base == nil {
		g.mu.Unlock()
		return
	}
	if g.busy[tile] {
		g.again[tile] = true
		g.mu.Unlock()
		return
	}
	if !g.spawnLocked() {
		g.mu.Unlock()
		return
	}
	g.busy[tile] = true
	had, inputs, ctx := slices.Clone(base.Had), e.Inputs, g.ctxLocked()
	g.mu.Unlock()
	go func() {
		defer g.wg.Done()
		defer g.release(tile)
		e, same, err := g.recheck(ctx, tile, had, inputs)
		if same || ctx.Err() != nil {
			return
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if err != nil {
			slog.Debug("go build versions: re-checking a tile", "tile", tile, "err", err)
			return // its entry stands
		}
		if g.st.Tiles[tile] == nil {
			return // a pass dropped it meanwhile
		}
		g.record(tile, e, nil)
		if err := g.saveLocked(); err != nil {
			slog.Warn("go build versions: saving the state", "err", err)
		}
	}()
}

// compare compares tile c's own build with its baseline — listed first
// with the shared go.work when it has none, and then every change counts,
// as its code is the same both ways; against a stored baseline only the
// modules it links both ways do (its code may have changed since). nil,
// nil: it links nothing older (or holds no Go module). A *sharedWorkError:
// the shared go.work itself fails.
func (g *GoVersions) compare(ctx context.Context, c *registry.Component, probes *sharedProbes) (*goVersionsTile, error) {
	entry := goEntry(c.Manifest)
	w, ok := g.Run.buildWork(c, entry, "", nil)
	if !ok {
		return nil, nil
	}
	g.mu.Lock()
	base := g.st.Baseline[c.Path]
	var hadLines []string
	if base != nil {
		hadLines = slices.Clone(base.Had)
	}
	g.mu.Unlock()
	var had []modVer
	full := base == nil
	if full {
		var err error
		if had, err = g.listShared(ctx, c, entry, w, probes); err != nil {
			return nil, err
		}
		b := &goVersionsBase{Had: []string{}}
		for _, m := range had {
			b.Had = append(b.Had, m.String())
		}
		g.mu.Lock()
		if g.st.Baseline != nil {
			g.st.Baseline[c.Path] = b
		}
		if err := g.saveLocked(); err != nil {
			slog.Warn("go build versions: saving the state", "err", err)
		}
		g.mu.Unlock()
	} else {
		had = parseModVers(hadLines)
	}
	now, err := g.listModules(ctx, c, entry, w.GoWork, nil)
	if err != nil {
		return nil, fmt.Errorf("built with its own go.mod: %w", err)
	}
	measure := func(mods []modVer) []VersionChange { return lowered(diffVersions(had, mods)) }
	if full {
		measure = func(mods []modVer) []VersionChange { return diffVersions(had, mods) }
	}
	e, err := g.lines(ctx, c, entry, w, measure(now), measure)
	if e != nil {
		e.Inputs = g.inputsOf(entry, w)
	}
	return e, err
}

// recheck lists tile's own build again and compares it with had, what it
// linked under the shared go.work. Only the modules it links both ways
// count: its code may have changed since. same: nothing that decides its
// versions changed since inputs, and nothing was listed.
func (g *GoVersions) recheck(ctx context.Context, tile string, hadLines []string, inputs string) (e *goVersionsTile, same bool, err error) {
	c, ok := g.goTile(tile)
	if !ok {
		return nil, false, nil // gone, or no longer a Go tile
	}
	entry := goEntry(c.Manifest)
	w, ok := g.Run.buildWork(c, entry, "", nil)
	if !ok {
		return nil, false, nil
	}
	in := g.inputsOf(entry, w)
	if in == inputs {
		return nil, true, nil
	}
	if err := g.acquire(ctx); err != nil {
		return nil, false, err
	}
	defer g.releaseSlot()
	had := parseModVers(hadLines)
	now, err := g.listModules(ctx, c, entry, w.GoWork, nil)
	if err != nil {
		return nil, false, err
	}
	measure := func(mods []modVer) []VersionChange { return lowered(diffVersions(had, mods)) }
	e, err = g.lines(ctx, c, entry, w, measure(now), measure)
	if e != nil {
		e.Inputs = in
	}
	return e, false, err
}

// parseModVers reads modVer.String's lines back.
func parseModVers(lines []string) []modVer {
	var out []modVer
	for _, l := range lines {
		if m, ok := parseModVer(l); ok {
			out = append(out, m)
		}
	}
	return out
}

// lowered keeps the changes of modules linked both ways, at a lower version.
func lowered(changes []VersionChange) []VersionChange {
	var out []VersionChange
	for _, c := range changes {
		if c.Had != "" && c.Now != "" {
			out = append(out, c)
		}
	}
	return out
}

// lines is the entry for a tile whose own build changes what it links:
// the fewest require lines that undo what measure finds (minimalPins).
func (g *GoVersions) lines(ctx context.Context, c *registry.Component, entry string, w deps.Work, changes []VersionChange, measure func([]modVer) []VersionChange) (*goVersionsTile, error) {
	if len(pinsFor(changes)) == 0 {
		return nil, nil
	}
	var direct map[string]bool
	if len(w.Uses) > 0 {
		direct = deps.DirectRequires(w.Uses[0])
	}
	budget := g.Budget
	if budget <= 0 {
		budget = 24
	}
	list := func(pins []deps.Pin) ([]modVer, error) { return g.listModules(ctx, c, entry, w.GoWork, pins) }
	pins, minimal := minimalPins(changes, measure, direct, list, budget)
	req := requireLines(pins)
	slices.Sort(req) // by module: the same lines read (and group in the alert) the same, whatever order the search chose them in
	return &goVersionsTile{Require: req, Minimal: minimal, Changes: changes, CheckedAt: time.Now().UTC()}, nil
}

// saveLocked writes the state file (g.mu held).
func (g *GoVersions) saveLocked() error {
	b, err := json.MarshalIndent(g.st, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomicIn(g.Path, append(b, '\n'), 0o600)
}

// workTreeView is c as its work tree says: a pinned primary's registry
// entry carries its checkpoint's manifest, the work tree's beside it.
func workTreeView(c *registry.Component) *registry.Component {
	if c.WorkTree == nil {
		return c
	}
	v := *c
	v.Manifest = c.WorkTree.Manifest
	return &v
}
