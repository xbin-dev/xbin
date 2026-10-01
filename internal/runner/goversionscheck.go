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
// (a tile's .xbin/build/<key>/bin), a few tiles at a time, never ahead of
// or in the way of a build; the state file under data/ is its done-marker.
// A workspace with no such build gets the marker at once. An admin can run
// it again (POST /go-build-versions/check). A tile's line goes away when its
// own go.mod catches up: each build of a tile the alert names lists the
// tile's own build again and compares it with what it had.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/deps"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// GoVersions is the upgrade check of D166: its state, its runs and the
// alert they make. Boot, then Start.
type GoVersions struct {
	Run     *Runner
	Path    string        // its state file: data/go-build-versions.json
	Version string        // the running xbind's version: the alert's "since"
	Delay   time.Duration // after boot, before the first pass starts
	// Parallel is how many tiles are checked at once (0 = 2); Budget how
	// many lists one tile's search for its lines may run (0 = 24).
	Parallel, Budget int

	// list stands in for Runner.listModules in tests; nil = it.
	list func(c *registry.Component, entry string, gowork []byte, pins []deps.Pin) ([]modVer, error)

	mu      sync.Mutex
	st      goVersionsState
	due     bool            // the first pass hasn't completed
	running bool            // a pass is running
	busy    map[string]bool // tiles being checked
	again   map[string]bool // tiles built again while being re-checked
	sem     chan struct{}
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
	Checked []string                   `json:"checked,omitempty"`
	Tiles   map[string]*goVersionsTile `json:"tiles,omitempty"`
	Errors  map[string]string          `json:"errors,omitempty"`
}

// goVersionsTile is one tile whose own build links older versions.
type goVersionsTile struct {
	Require   []string        `json:"require"` // the go.mod lines that keep what it had: "path version"
	Minimal   bool            `json:"minimal"` // false: the raw differing lines (the search gave up)
	Changes   []VersionChange `json:"changes"`
	Had       []string        `json:"had"` // what it linked under the shared go.work (modVer.String)
	CheckedAt time.Time       `json:"checkedAt"`
	Dismissed bool            `json:"dismissed,omitempty"`
}

// GoVersionsReport is GET /go-build-versions's answer (protocol.md).
type GoVersionsReport struct {
	Since     string             `json:"since,omitempty"`
	Done      bool               `json:"done"`
	Fresh     bool               `json:"fresh,omitempty"`
	Running   bool               `json:"running"`
	CheckedAt *time.Time         `json:"checkedAt,omitempty"`
	Tiles     []GoVersionsTile   `json:"tiles"`
	Errors    []GoVersionsFailed `json:"errors"`
}

// GoVersionsTile is a tile in the report.
type GoVersionsTile struct {
	Tile      string          `json:"tile"`
	Require   []string        `json:"require"`
	Minimal   bool            `json:"minimal"`
	Changes   []VersionChange `json:"changes"`
	Dismissed bool            `json:"dismissed"`
	CheckedAt time.Time       `json:"checkedAt"`
}

// GoVersionsFailed is a tile the check couldn't compare, and why.
type GoVersionsFailed struct {
	Tile  string `json:"tile"`
	Error string `json:"error"`
}

// GoVersionsAlertKind is the alert's kind in GET /alerts.
const GoVersionsAlertKind = "go-build-versions"

// ErrGoVersionsUnknownTile: Dismiss named a tile the check doesn't list.
var ErrGoVersionsUnknownTile = errors.New("no Go build versions alert for that tile")

// Boot reads the state file and decides whether the first pass is due: no
// state file (or one whose pass didn't complete) and a Go tile built by an
// earlier xbind. A workspace with no such build gets the done-marker at
// once: nothing it runs was built with the shared go.work. Call it before
// any build can run (the boot's registry step).
func (g *GoVersions) Boot() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.busy, g.again = map[string]bool{}, map[string]bool{}
	b, err := os.ReadFile(g.Path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &g.st); err != nil {
			slog.Warn("go build versions: the state file doesn't parse; checking again", "file", g.Path, "err", err)
			g.st = goVersionsState{}
		} else {
			g.due = !g.st.Done
			return nil
		}
	case !os.IsNotExist(err):
		return err
	}
	g.st = goVersionsState{Since: g.Version}
	if !goBuiltBefore(g.Run.Root, g.Run.components()) {
		g.st.Done, g.st.Fresh = true, true
		return g.saveLocked()
	}
	g.due = true
	return g.saveLocked() // since is this version, whenever the pass completes
}

// goBuiltBefore reports whether a Go tile of the workspace has a binary an
// earlier xbind built: .xbin/build/<key>/bin, a regular file.
func goBuiltBefore(root string, comps []*registry.Component) bool {
	for _, c := range comps {
		if workTreeView(c).Manifest.Runtime != "go" {
			continue
		}
		fi, err := os.Lstat(filepath.Join(root, ".xbin", "build", util.CompKey(c.Path), "bin"))
		if err == nil && fi.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// Start runs the first pass in the background when Boot found it due,
// after Delay.
func (g *GoVersions) Start() {
	g.mu.Lock()
	due := g.due
	g.mu.Unlock()
	if !due {
		return
	}
	go func() {
		time.Sleep(g.Delay)
		g.runAll(true)
	}()
}

// CheckAll runs the check again over every Go tile, in the background;
// false when a pass is already running.
func (g *GoVersions) CheckAll() bool {
	g.mu.Lock()
	running := g.running
	g.mu.Unlock()
	if running {
		return false
	}
	go g.runAll(false)
	return true
}

// runAll checks every Go tile, Parallel at a time: both module lists, and
// the lines for each tile whose own links older versions. resume skips the
// tiles an interrupted first pass checked. It completes the first pass.
func (g *GoVersions) runAll(resume bool) {
	g.mu.Lock()
	if g.running {
		g.mu.Unlock()
		return
	}
	g.running = true
	if !resume {
		g.st.Checked = nil
	}
	if g.st.Since == "" {
		g.st.Since = g.Version
	}
	done := map[string]bool{}
	for _, t := range g.st.Checked {
		done[t] = true
	}
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.running = false
		g.mu.Unlock()
	}()

	var tiles []*registry.Component
	for _, c := range g.Run.components() {
		if c = workTreeView(c); c.Manifest.Runtime == "go" && !done[c.Path] {
			tiles = append(tiles, c)
		}
	}
	slog.Info("go build versions: comparing each Go tile's own build with the shared go.work (D166)", "tiles", len(tiles))
	keep := done // what this pass leaves as it is: tiles an earlier part of it checked, and those being re-checked
	var wg sync.WaitGroup
	for _, c := range tiles {
		keep[c.Path] = true
		if !g.claim(c.Path) {
			continue // a re-check of it is running
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer g.release(c.Path)
			g.acquire()
			defer g.releaseSlot()
			e, err := g.checkTile(c)
			g.mu.Lock()
			g.record(c.Path, e, err)
			g.st.Checked = append(g.st.Checked, c.Path)
			if err := g.saveLocked(); err != nil {
				slog.Warn("go build versions: saving the state", "err", err)
			}
			g.mu.Unlock()
		}()
	}
	wg.Wait()
	g.mu.Lock()
	defer g.mu.Unlock()
	// a tile gone, or no longer a Go tile, has nothing to say
	for t := range g.st.Tiles {
		if !keep[t] {
			delete(g.st.Tiles, t)
		}
	}
	for t := range g.st.Errors {
		if !keep[t] {
			delete(g.st.Errors, t)
		}
	}
	g.st.Done, g.st.Fresh, g.st.Checked, g.st.CheckedAt = true, false, nil, time.Now().UTC()
	g.due = false
	if err := g.saveLocked(); err != nil {
		slog.Warn("go build versions: saving the state", "err", err)
	}
	slog.Info("go build versions: done", "affected", len(g.st.Tiles), "uncompared", len(g.st.Errors))
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

func (g *GoVersions) acquire() {
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
	sem <- struct{}{}
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

// Built is the runner's word that tile's work tree built: when the alert
// names the tile, its own build is listed again in the background and
// compared with what it had — the line goes away once its go.mod caught
// up. Never blocks the build.
func (g *GoVersions) Built(tile string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.initLocked()
	e := g.st.Tiles[tile]
	if e == nil {
		g.mu.Unlock()
		return
	}
	if g.busy[tile] {
		g.again[tile] = true
		g.mu.Unlock()
		return
	}
	g.busy[tile] = true
	had := slices.Clone(e.Had)
	g.mu.Unlock()
	go func() {
		defer g.release(tile)
		g.acquire()
		defer g.releaseSlot()
		e, err := g.recheck(tile, had)
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

// checkTile compares tile c's entry built with the shared go.work and with
// its own build workspace. nil, nil: it links nothing older (or holds no
// Go module).
func (g *GoVersions) checkTile(c *registry.Component) (*goVersionsTile, error) {
	r := g.Run
	entry := goEntry(c.Manifest)
	w, ok := r.buildWork(c, entry, "", nil)
	if !ok {
		return nil, nil
	}
	shared, err := deps.SharedWork(r.Reg, deps.SDKPath())
	if err != nil {
		return nil, fmt.Errorf("the workspace's go.work: %w", err)
	}
	had, err := g.listModules(c, entry, shared, nil)
	if err != nil {
		return nil, fmt.Errorf("built with the workspace's go.work: %w", err)
	}
	now, err := g.listModules(c, entry, w.GoWork, nil)
	if err != nil {
		return nil, fmt.Errorf("built with its own go.mod: %w", err)
	}
	measure := func(mods []modVer) []VersionChange { return diffVersions(had, mods) }
	return g.lines(c, entry, w, had, measure(now), measure)
}

// recheck lists tile's own build again and compares it with had, what it
// linked under the shared go.work when the check ran. Only the modules it
// links both ways count: its code may have changed since.
func (g *GoVersions) recheck(tile string, hadLines []string) (*goVersionsTile, error) {
	r := g.Run
	if r.Reg == nil {
		return nil, errors.New("no registry")
	}
	c, ok := r.Reg.Component(tile)
	if !ok {
		return nil, nil // gone
	}
	c = workTreeView(c)
	if c.Manifest.Runtime != "go" {
		return nil, nil
	}
	entry := goEntry(c.Manifest)
	w, ok := r.buildWork(c, entry, "", nil)
	if !ok {
		return nil, nil
	}
	var had []modVer
	for _, l := range hadLines {
		if m, ok := parseModVer(l); ok {
			had = append(had, m)
		}
	}
	now, err := g.listModules(c, entry, w.GoWork, nil)
	if err != nil {
		return nil, err
	}
	measure := func(mods []modVer) []VersionChange { return lowered(diffVersions(had, mods)) }
	return g.lines(c, entry, w, had, measure(now), measure)
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
func (g *GoVersions) lines(c *registry.Component, entry string, w deps.Work, had []modVer, changes []VersionChange, measure func([]modVer) []VersionChange) (*goVersionsTile, error) {
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
	list := func(pins []deps.Pin) ([]modVer, error) { return g.listModules(c, entry, w.GoWork, pins) }
	pins, minimal := minimalPins(changes, measure, direct, list, budget)
	e := &goVersionsTile{Require: requireLines(pins), Minimal: minimal, Changes: changes, CheckedAt: time.Now().UTC()}
	for _, m := range had {
		e.Had = append(e.Had, m.String())
	}
	return e, nil
}

// Dismiss hides the alert's line for tile ("" = every tile it names) until
// its lines change.
func (g *GoVersions) Dismiss(tile string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if tile != "" {
		e := g.st.Tiles[tile]
		if e == nil {
			return ErrGoVersionsUnknownTile
		}
		e.Dismissed = true
	} else {
		for _, e := range g.st.Tiles {
			e.Dismissed = true
		}
	}
	return g.saveLocked()
}

// Report is the check's latest result.
func (g *GoVersions) Report() GoVersionsReport {
	g.mu.Lock()
	defer g.mu.Unlock()
	rep := GoVersionsReport{Since: g.st.Since, Done: g.st.Done, Fresh: g.st.Fresh, Running: g.running,
		Tiles: []GoVersionsTile{}, Errors: []GoVersionsFailed{}}
	if !g.st.CheckedAt.IsZero() {
		at := g.st.CheckedAt
		rep.CheckedAt = &at
	}
	for t, e := range g.st.Tiles {
		rep.Tiles = append(rep.Tiles, GoVersionsTile{Tile: t, Require: e.Require, Minimal: e.Minimal,
			Changes: e.Changes, Dismissed: e.Dismissed, CheckedAt: e.CheckedAt})
	}
	sort.Slice(rep.Tiles, func(i, j int) bool { return rep.Tiles[i].Tile < rep.Tiles[j].Tile })
	for t, msg := range g.st.Errors {
		rep.Errors = append(rep.Errors, GoVersionsFailed{Tile: t, Error: msg})
	}
	sort.Slice(rep.Errors, func(i, j int) bool { return rep.Errors[i].Tile < rep.Errors[j].Tile })
	return rep
}

// Alert is the admin alert's message, naming every tile whose line isn't
// dismissed; ok=false when there is none.
func (g *GoVersions) Alert() (string, bool) {
	if g == nil {
		return "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	var affected []GoVersionsAffected
	for t, e := range g.st.Tiles {
		if !e.Dismissed && len(e.Require) > 0 {
			affected = append(affected, GoVersionsAffected{Tile: t, Require: e.Require})
		}
	}
	if len(affected) == 0 {
		return "", false
	}
	sort.Slice(affected, func(i, j int) bool { return affected[i].Tile < affected[j].Tile })
	return goVersionsMessage(g.st.Since, affected), true
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

// listModules is the check's list of c's modules (Runner.listModules).
func (g *GoVersions) listModules(c *registry.Component, entry string, gowork []byte, pins []deps.Pin) ([]modVer, error) {
	if g.list != nil {
		return g.list(c, entry, gowork, pins)
	}
	return g.Run.listModules(c, entry, gowork, pins)
}

// listModules lists the modules c's entry links built with the go.work
// gowork, and pins required besides the tile's go.mod (a module of their
// own the go.work uses: deps.PinGoMod): `go list -deps`, run as c's build
// runs — confined (D78: go reads the tile's go.mod and code as
// configuration) with the build's binds, caches, network and settings
// (goBuildCmd), or as the build runs with isolation off — writing only its
// own directory beside the tile's caches, versions/, where its go.work and
// go.work.sum live.
func (r *Runner) listModules(c *registry.Component, entry string, gowork []byte, pins []deps.Pin) ([]modVer, error) {
	gocache, _ := goCaches(r.Root, c.Path)
	cacheDir := filepath.Dir(gocache)
	vfd, err := openArtifacts(cacheDir, "versions") // bound into no build; the lists write in it
	if err != nil {
		return nil, err
	}
	defer unix.Close(vfd)
	dir := filepath.Join(cacheDir, "versions")
	content := gowork
	if len(pins) > 0 {
		goLine := deps.WorkGoLine(gowork)
		if goLine == "" {
			goLine = "1.18" // a go.work without one is go 1.18's
		}
		pfd, err := openArtifacts(dir, "pin")
		if err != nil {
			return nil, err
		}
		err = writeAt(pfd, "go.mod", deps.PinGoMod(goLine, pins))
		unix.Close(pfd)
		if err != nil {
			return nil, err
		}
		content = deps.WithUse(gowork, filepath.Join(dir, "pin"))
	}
	if err := writeAt(vfd, "go.work", content); err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstatat(vfd, "go.work.sum", &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		if seed := seedWorkSum(filepath.Join(cacheDir, "work"), "", r.Root); len(seed) > 0 {
			if err := writeAt(vfd, "go.work.sum", seed); err != nil {
				return nil, err
			}
		}
	}
	gw := filepath.Join(dir, "go.work")
	var cmd confine.Cmd
	if r.Isolate {
		var dirs []string
		cmd, dirs, err = r.goBuildCmd(c, entry, goBuild{outDir: dir})
		if err != nil {
			return nil, err
		}
		for _, d := range dirs {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return nil, err
			}
		}
		cmd.Argv = append([]string{cmd.Argv[0]}, listArgs(entry)...)
		cmd.Env = setEnv(cmd.Env, "GOWORK", gw)
		cmd.Env = setEnv(cmd.Env, "GOFLAGS", readonlyFlags(goFlags()))
	} else {
		// as the build runs with isolation off (build.go): xbind's go and
		// environment, the workspace's build cache — no sandbox exists
		cmd = confine.Cmd{Argv: append([]string{"go"}, listArgs(entry)...), Dir: c.Dir, ReadOnlyDir: true, Env: []string{
			"GOCACHE=" + filepath.Join(r.Root, ".xbin", "cache", "go-build"),
			"GOWORK=" + gw,
			"GOFLAGS=" + readonlyFlags(os.Getenv("GOFLAGS")),
		}}
	}
	cmd.Timeout, cmd.MaxOutput = 10*time.Minute, 4<<20
	res, err := confine.Run(context.Background(), cmd)
	if err != nil {
		msg := strings.TrimSpace(string(res.Stderr))
		if msg == "" {
			msg = err.Error()
		}
		if len(msg) > 2000 {
			msg = "…" + msg[len(msg)-2000:]
		}
		return nil, errors.New(msg)
	}
	return parseModList(res.Stdout), nil
}

// listArgs is `go list` of entry's packages and their dependencies' modules.
func listArgs(entry string) []string {
	return []string{"list", "-buildvcs=false", "-deps", "-f", listFormat, entry}
}

// readonlyFlags is GOFLAGS with -mod=readonly unless it sets -mod (a
// build's default either way: the list never edits a go.mod).
func readonlyFlags(flags string) string {
	if strings.Contains(flags, "-mod=") {
		return strings.TrimSpace(flags)
	}
	return strings.TrimSpace(flags + " -mod=readonly")
}

// setEnv sets k=v in env, replacing k's entries.
func setEnv(env []string, k, v string) []string {
	out := env[:0:0]
	for _, e := range env {
		if !strings.HasPrefix(e, k+"=") {
			out = append(out, e)
		}
	}
	return append(out, k+"="+v)
}
