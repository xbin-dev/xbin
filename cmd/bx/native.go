package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Native UIs for agents (plans/native.md §16, docs/bx.md "Native UIs"):
//
//	bx native tree <tile>                 the rendered tree JSON (cheapest, diffable)
//	bx lint --native [tile…]              static checks + a headless run; coverage
//	bx preview --native <tile> --out f.png  the reference renderer's picture
//
// All three load the tile's runtime document (/c/<tile>/?native=1) in
// headless Chromium against the live backend — or a replayed fixture
// (--data) — and read what the tile rendered.

// moreCmds are top-level commands dispatched by cmdExtra (main.go's switch
// is at its size budget).
var moreCmds = map[string]func([]string) error{
	"native":  cmdNative,
	"lint":    cmdLint,
	"preview": cmdPreview,
}

const nativeUsage = `  bx native tree <tile> [--data d.json] the tile's rendered native tree (JSON)
  bx lint --native [tile…] [--static] [--json]
                                        check native UIs; no tile = the whole
                                        workspace, with its native coverage
  bx preview --native <tile> [--dark] [--size 390x844] [--large-text]
             [--data d.json] [--full] [--out shot.png]
                                        screenshot a native UI (reference renderer)
`

type nativeArgs struct {
	cmd       string // tree | lint | preview
	native    bool
	tiles     []string
	dark      bool
	width     int
	height    int
	largeText bool
	data      string
	steps     string
	out       string
	full      bool
	json      bool
	static    bool
	timeout   time.Duration
}

// flag sets per command: which flags each takes, and whether they take a value
var nativeFlags = map[string]map[string]bool{
	"tree":    {"--native": false, "--data": true, "--steps": true, "--timeout": true},
	"lint":    {"--native": false, "--json": false, "--static": false, "--timeout": true},
	"preview": {"--native": false, "--data": true, "--steps": true, "--timeout": true, "--dark": false, "--light": false, "--size": true, "--large-text": false, "--out": true, "-o": true, "--full": false},
}

func parseNativeArgs(cmd string, args []string) (nativeArgs, error) {
	a := nativeArgs{cmd: cmd, width: 390, height: 844, timeout: 30 * time.Second}
	flags := nativeFlags[cmd]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !isFlag(arg) {
			t, err := cleanTilePath(arg)
			if err != nil {
				return a, err
			}
			a.tiles = append(a.tiles, t)
			continue
		}
		name, val, inline := strings.Cut(arg, "=")
		takesVal, ok := flags[name]
		if !ok {
			return a, unknownFlag(cmd, name, false)
		}
		if takesVal && !inline {
			v, err := nextArg(args, &i)
			if err != nil {
				return a, err
			}
			val = v
		} else if !takesVal && inline {
			return a, fmt.Errorf("%s takes no value", name)
		}
		switch name {
		case "--native":
			a.native = true
		case "--json":
			a.json = true
		case "--static":
			a.static = true
		case "--dark":
			a.dark = true
		case "--light":
			a.dark = false
		case "--large-text":
			a.largeText = true
		case "--full":
			a.full = true
		case "--data":
			a.data = val
		case "--steps":
			a.steps = val
		case "--out", "-o":
			a.out = val
		case "--size":
			w, h, err := parseSize(val)
			if err != nil {
				return a, err
			}
			a.width, a.height = w, h
		case "--timeout":
			d, err := parseTimeout(val)
			if err != nil {
				return a, err
			}
			a.timeout = d
		}
	}
	switch cmd {
	case "lint", "preview":
		if !a.native {
			return a, fmt.Errorf("bx %s: only --native is supported so far — bx %s --native …", cmd, cmd)
		}
	}
	if cmd == "tree" || cmd == "preview" {
		if len(a.tiles) == 0 {
			if c := os.Getenv("XBIN_COMPONENT"); c != "" { // a tile's terminal: that tile
				a.tiles = []string{c}
			} else {
				return a, fmt.Errorf("bx %s: which tile? (bx %s <tile>)", nativeCmdName(cmd), nativeCmdName(cmd))
			}
		}
		if len(a.tiles) > 1 {
			return a, fmt.Errorf("bx %s: one tile at a time", nativeCmdName(cmd))
		}
	}
	return a, nil
}

func nativeCmdName(cmd string) string {
	switch cmd {
	case "tree":
		return "native tree"
	case "preview":
		return "preview --native"
	}
	return "lint --native"
}

// cleanTilePath accepts apps/x, /apps/x/, /c/apps/x/.
func cleanTilePath(s string) (string, error) {
	t := strings.Trim(strings.TrimSpace(s), "/")
	t = strings.TrimPrefix(t, "c/")
	if t == "" || t == "." {
		return "", fmt.Errorf("bad tile path %q", s)
	}
	for _, seg := range strings.Split(t, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("bad tile path %q", s)
		}
	}
	return t, nil
}

func parseSize(s string) (int, int, error) {
	ws, hs, ok := strings.Cut(strings.ToLower(s), "x")
	w, err1 := strconv.Atoi(ws)
	h, err2 := strconv.Atoi(hs)
	if !ok || err1 != nil || err2 != nil || w < 200 || h < 200 || w > 4000 || h > 4000 {
		return 0, 0, fmt.Errorf("--size wants WIDTHxHEIGHT in points, 200…4000 (e.g. 390x844), got %q", s)
	}
	return w, h, nil
}

// parseTimeout: a Go duration (45s, 2m) or plain seconds.
func parseTimeout(s string) (time.Duration, error) {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return time.Duration(n) * time.Second, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--timeout wants a duration (30s, 2m), got %q", s)
	}
	return d, nil
}

// loadFixture reads --data (a data.json, or a fixture directory holding one
// plus an optional steps.json) and --steps. A data file's own "steps" array
// is used when --steps is absent.
func loadFixture(data, steps string) (json.RawMessage, json.RawMessage, error) {
	var d, s json.RawMessage
	if data != "" {
		file := data
		if fi, err := os.Stat(data); err == nil && fi.IsDir() {
			file = filepath.Join(data, "data.json")
			if steps == "" {
				if _, err := os.Stat(filepath.Join(data, "steps.json")); err == nil {
					steps = filepath.Join(data, "steps.json")
				}
			}
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, nil, fmt.Errorf("--data: %v", err)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(b, &obj); err != nil {
			return nil, nil, fmt.Errorf("--data %s: not a JSON object: %v", file, err)
		}
		d = b
		if st, ok := obj["steps"]; ok && steps == "" {
			s = st
		}
	}
	if steps != "" {
		b, err := os.ReadFile(steps)
		if err != nil {
			return nil, nil, fmt.Errorf("--steps: %v", err)
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(b, &arr); err != nil {
			return nil, nil, fmt.Errorf("--steps %s: not a JSON array: %v", steps, err)
		}
		s = b
	}
	return d, s, nil
}

func nativeComponents() ([]nativeComp, error) {
	var out []nativeComp
	err := apiJSON("GET", "/api/xbin/components", nil, &out)
	return out, err
}

func compPaths(comps []nativeComp) []string {
	out := make([]string, len(comps))
	for i, c := range comps {
		out[i] = c.Path
	}
	return out
}

func (a nativeArgs) probeConfig(mode string, tiles []string) (probeConfig, error) {
	d, s, err := loadFixture(a.data, a.steps)
	if err != nil {
		return probeConfig{}, err
	}
	cfg := probeConfig{Mode: mode, Tiles: tiles, Width: a.width, Height: a.height, Scale: 2,
		Data: d, Steps: s, Timeout: a.timeout.Milliseconds(), Settle: 400, Full: a.full}
	if a.dark {
		cfg.Theme = "dark"
	} else if mode == "preview" {
		cfg.Theme = "light"
	}
	if a.largeText {
		cfg.Text = "large"
	}
	return cfg, nil
}

// printRuntimeNotes writes a run's errors and diagnostics to stderr.
func printRuntimeNotes(r *probeResult) {
	for _, f := range runtimeFindings(r) {
		if f.Level == "ok" || f.Level == "info" {
			continue
		}
		fmt.Fprintln(os.Stderr, formatFinding(f))
	}
	for _, u := range r.Unmatched {
		fmt.Fprintf(os.Stderr, "fixture: no route for %s (answered 404)\n", u)
	}
}

func runtimeFailed(r *probeResult) bool {
	for _, f := range runtimeFindings(r) {
		if f.Level == "error" {
			return true
		}
	}
	return false
}

// cmdNative: bx native tree <tile> [--data d.json] [--steps s.json] [--timeout 30s]
func cmdNative(args []string) error {
	if len(args) == 0 || args[0] != "tree" {
		return errors.New("usage: bx native tree <tile> [--data fixture.json] [--steps steps.json] (docs/bx.md)")
	}
	a, err := parseNativeArgs("tree", args[1:])
	if err != nil {
		return err
	}
	cfg, err := a.probeConfig("tree", a.tiles)
	if err != nil {
		return err
	}
	comps, err := nativeComponents()
	if err != nil {
		return err
	}
	res, err := runProbe(cfg, compPaths(comps))
	if err != nil {
		return err
	}
	r := &res[0]
	printRuntimeNotes(r)
	if r.LoadError != "" || len(r.Tree) == 0 {
		return fmt.Errorf("%s: no tree", r.Tile)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, r.Tree, "", " "); err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, _ = os.Stdout.Write(buf.Bytes())
	if runtimeFailed(r) {
		return fmt.Errorf("%s: the runtime reported errors (above)", r.Tile)
	}
	return nil
}

// cmdPreview: bx preview --native <tile> [--dark] [--size WxH] [--large-text]
// [--data d.json] [--steps s.json] [--full] [--out shot.png]
func cmdPreview(args []string) error {
	a, err := parseNativeArgs("preview", args)
	if err != nil {
		return err
	}
	tile := a.tiles[0]
	if a.out == "" {
		a.out = filepath.Join(os.TempDir(), "bx-preview-"+strings.ReplaceAll(tile, "/", "-")+".png")
	}
	out, err := filepath.Abs(a.out)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(filepath.Dir(out)); err != nil || !fi.IsDir() {
		return fmt.Errorf("--out %s: no such directory", filepath.Dir(out))
	}
	cfg, err := a.probeConfig("preview", a.tiles)
	if err != nil {
		return err
	}
	cfg.Out = out
	comps, err := nativeComponents()
	if err != nil {
		return err
	}
	res, err := runProbe(cfg, compPaths(comps))
	if err != nil {
		return err
	}
	r := &res[0]
	printRuntimeNotes(r)
	if r.Shot == "" {
		if r.LoadError != "" {
			return fmt.Errorf("%s: %s", tile, r.LoadError)
		}
		return fmt.Errorf("%s: no screenshot", tile)
	}
	scheme := "light"
	if a.dark {
		scheme = "dark"
	}
	text := ""
	if a.largeText {
		text = ", large text"
	}
	ms := ""
	if r.FirstTreeMs != nil {
		ms = fmt.Sprintf(", first tree in %.0f ms", *r.FirstTreeMs)
	}
	fmt.Printf("wrote %s (%dx%d @2x, %s%s; %d nodes%s)\n", out, a.width, a.height, scheme, text, r.Stats.Nodes, ms)
	if runtimeFailed(r) {
		return fmt.Errorf("%s: the runtime reported errors (above; the picture shows them in a red strip)", tile)
	}
	return nil
}

// tileLint is one tile's lint report.
type tileLint struct {
	Tile     string        `json:"tile"`
	Entry    string        `json:"entry,omitempty"`
	Findings []lintFinding `json:"findings"`
	Runtime  *probeResult  `json:"runtime,omitempty"`
}

func (t tileLint) worst() string {
	w := "ok"
	for _, f := range t.Findings {
		switch {
		case f.Level == "error":
			return "error"
		case f.Level == "warn":
			w = "warn"
		}
	}
	return w
}

// cmdLint: bx lint --native [tile…] [--static] [--json] [--timeout 30s]
func cmdLint(args []string) error {
	a, err := parseNativeArgs("lint", args)
	if err != nil {
		return err
	}
	comps, err := nativeComponents()
	if err != nil {
		return err
	}
	byPath := map[string]nativeComp{}
	for _, c := range comps {
		byPath[c.Path] = c
	}
	workspace := len(a.tiles) == 0
	var targets []nativeComp
	var reports []tileLint
	if workspace {
		for _, c := range comps {
			if !c.Chrome && !c.Template {
				targets = append(targets, c)
			}
		}
	} else {
		for _, t := range a.tiles {
			c, ok := byPath[t]
			if !ok {
				reports = append(reports, tileLint{Tile: t, Findings: []lintFinding{{Level: "error", Message: "no such tile (or not visible to this token)"}}})
				continue
			}
			targets = append(targets, c)
		}
	}
	var probe []string
	idx := map[string]int{}
	for _, c := range targets {
		rep := tileLint{Tile: c.Path, Entry: c.entry(), Findings: []lintFinding{}}
		if c.entry() != "" || !workspace { // the workspace sweep reads only native tiles
			rep.Findings = staticLint(xbindGet, c)
		}
		if c.entry() != "" {
			if rep.worst() != "error" {
				probe = append(probe, c.Path)
			} else if !a.static {
				rep.Findings = append(rep.Findings, lintFinding{Level: "info", Message: "headless run skipped until the errors above are fixed"})
			}
		}
		idx[c.Path] = len(reports)
		reports = append(reports, rep)
	}

	runtime := "chromium"
	if a.static {
		runtime = "static checks only (--static)"
	} else if len(probe) > 0 {
		cfg, err := a.probeConfig("lint", probe)
		if err != nil {
			return err
		}
		res, err := runProbe(cfg, compPaths(comps))
		var nb *errNoBrowser
		switch {
		case errors.As(err, &nb):
			runtime = "static checks only — " + nb.Error()
		case err != nil:
			return err
		default:
			for i := range res {
				r := res[i]
				rep := &reports[idx[r.Tile]]
				rep.Findings = append(rep.Findings, runtimeFindings(&r)...)
				r.Tree = nil // bx native tree prints trees; lint reports on them
				rep.Runtime = &r
			}
		}
	}

	sort.SliceStable(reports, func(i, j int) bool { return reports[i].Tile < reports[j].Tile })
	cov := coverage(reports, workspace)
	if a.json {
		b, _ := json.MarshalIndent(map[string]any{"tiles": reports, "coverage": cov, "runtime": runtime}, "", "  ")
		fmt.Println(string(b))
	} else {
		printLint(reports, cov, runtime, workspace)
	}
	for _, r := range reports {
		if r.worst() == "error" {
			return errors.New("native lint found errors")
		}
	}
	return nil
}

type nativeCoverage struct {
	Tiles   int      `json:"tiles"`
	Native  []string `json:"native"`
	Clean   []string `json:"clean"`
	Warn    []string `json:"warnings"`
	Errors  []string `json:"errors"`
	WebOnly []string `json:"webOnly"`
}

func coverage(reports []tileLint, workspace bool) nativeCoverage {
	cov := nativeCoverage{Tiles: len(reports), Native: []string{}, Clean: []string{}, Warn: []string{}, Errors: []string{}, WebOnly: []string{}}
	for _, r := range reports {
		if r.Entry == "" {
			cov.WebOnly = append(cov.WebOnly, r.Tile)
			continue
		}
		cov.Native = append(cov.Native, r.Tile)
		switch r.worst() {
		case "error":
			cov.Errors = append(cov.Errors, r.Tile)
		case "warn":
			cov.Warn = append(cov.Warn, r.Tile)
		default:
			cov.Clean = append(cov.Clean, r.Tile)
		}
	}
	return cov
}

func formatFinding(f lintFinding) string {
	where := ""
	if f.Where != "" {
		where = f.Where + "  "
	}
	return fmt.Sprintf("  %-5s  %s%s", f.Level, where, f.Message)
}

func printLint(reports []tileLint, cov nativeCoverage, runtime string, workspace bool) {
	for _, r := range reports {
		if workspace && r.Entry == "" {
			continue // listed in the coverage line
		}
		head := r.Tile
		if r.Entry != "" {
			head += "  (" + r.Entry + ")"
		}
		fmt.Println(head)
		for _, f := range r.Findings {
			fmt.Println(formatFinding(f))
		}
	}
	rendered := runtime == "chromium"
	if !rendered {
		fmt.Println("note: " + runtime)
	}
	if workspace || len(reports) > 1 {
		how := ""
		if !rendered {
			how = " (static checks only, not rendered)"
		}
		fmt.Printf("native coverage: %d of %d tiles have a native UI — %d clean, %d with warnings, %d with errors%s\n",
			len(cov.Native), cov.Tiles, len(cov.Clean), len(cov.Warn), len(cov.Errors), how)
		if workspace && len(cov.WebOnly) > 0 {
			fmt.Printf("web only: %s\n", strings.Join(cov.WebOnly, ", "))
		}
	}
}
