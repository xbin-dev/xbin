package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/assetscan"
)

// cmdFix: `bx fix assets [<tile>] [--write] [--dir <path>]` — the owner-run
// codemod of strict tile asset gating (docs/auth.md §Tile asset gating):
// rewrites a tile's absolute /c/ URLs in HTML attributes, stylesheets,
// import maps and module imports to relative ones (same target in every
// mode), and lists what needs a human. Dry run unless --write.
func cmdFix(args []string) error {
	if len(args) == 0 || args[0] != "assets" {
		return errors.New("usage: bx fix assets [<tile>] [--write] [--dir <path>]")
	}
	var tile, dir string
	write := false
	for i := 1; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--write":
			write = true
		case a == "--dir":
			v, err := nextArg(args, &i)
			if err != nil {
				return err
			}
			dir = v
		case isFlag(a):
			return unknownFlag("fix assets", a, false)
		case tile == "":
			tile = strings.Trim(a, "/")
		default:
			return fmt.Errorf("unexpected argument %q", a)
		}
	}
	if tile == "" {
		tile = os.Getenv("XBIN_COMPONENT") // a tile terminal's own tile
	}
	if tile == "" {
		return errors.New("which tile? bx fix assets <tile> (in a tile terminal it defaults to that tile)")
	}
	root := workspaceRoot()
	if dir == "" {
		dir = tileDir(root, tile)
	}
	if dir == "" {
		return fmt.Errorf("can't find %s's files here — run inside the workspace (or its terminal), or pass --dir", tile)
	}
	opt := assetscan.Options{Root: root}
	if root == "" {
		opt.Owner = apiOwner()
	}
	changes, rep, err := assetscan.Plan(dir, tile, opt)
	if err != nil {
		return err
	}
	edits := 0
	for _, c := range changes {
		for _, e := range c.Edits {
			fmt.Printf("  %s:%d:%d  %s → %s\n", c.File, e.Line, e.Col, e.Ref, e.Fix)
			edits++
		}
	}
	manual := 0
	for _, f := range rep.Findings {
		if f.Fix != "" || f.Kind == assetscan.KindJSImport && f.Breaks == "" {
			continue
		}
		if manual == 0 {
			fmt.Println("needs a look (not rewritten):")
		}
		manual++
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d:%d", f.File, f.Line, f.Col)
		}
		ref := ""
		if f.Ref != "" {
			ref = " " + f.Ref
		}
		fmt.Printf("  %s  %s%s — %s\n", loc, f.Kind, ref, f.Note)
	}
	if rep.Truncated {
		fmt.Println("  (scan truncated — large tile; run again after fixing)")
	}
	switch {
	case edits == 0 && manual == 0:
		fmt.Printf("%s: no absolute /c/ references — ready for strict tile asset gating\n", tile)
		return nil
	case edits == 0:
		return nil
	case !write:
		fmt.Printf("dry run: %d reference(s) in %d file(s) would become relative — run with --write to apply\n", edits, len(changes))
		return nil
	}
	if err := assetscan.Apply(dir, changes); err != nil {
		return err
	}
	fmt.Printf("rewrote %d reference(s) in %d file(s)\n", edits, len(changes))
	return nil
}

// tileDir finds a tile's directory: under the workspace root when bx can see
// it, else — in the tile's own terminal — the nearest directory up from the
// working directory that holds an xbin.json.
func tileDir(root, tile string) string {
	if root != "" {
		d := filepath.Join(root, filepath.FromSlash(tile))
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
		return ""
	}
	if os.Getenv("XBIN_COMPONENT") != tile {
		return ""
	}
	cwd, _ := os.Getwd()
	for d := cwd; d != "/" && d != "."; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "xbin.json")); err == nil {
			return d
		}
	}
	return ""
}

// apiOwner maps /c/ paths to components by the registry's rule (longest
// registered prefix) using xbind's component list, when the workspace root
// isn't in view.
func apiOwner() func(string) string {
	comps, _ := components()
	return func(rel string) string {
		best := ""
		for _, c := range comps {
			if (rel == c.Path || strings.HasPrefix(rel, c.Path+"/")) && len(c.Path) > len(best) {
				best = c.Path
			}
		}
		if best == "" {
			best, _, _ = strings.Cut(rel, "/")
		}
		return best
	}
}

// doctorTileAssets: bx doctor's strict-asset-gating check, from xbind's tile
// asset report. Silent against an xbind without the report.
func doctorTileAssets(warn, ok func(string, ...any)) {
	var rep struct {
		Mode  string `json:"mode"`
		Tiles []struct {
			Component string         `json:"component"`
			Breaking  map[string]int `json:"breaking"`
		} `json:"tiles"`
	}
	if err := apiJSON("GET", "/api/xbin/tile-assets", nil, &rep); err != nil {
		return
	}
	mode := rep.Mode
	check := "tokens" // legacy: warn about what the coming enforcement refuses
	if mode == "origins" {
		check = "origins"
	}
	bad := 0
	for _, t := range rep.Tiles {
		n := t.Breaking[check]
		if n == 0 {
			continue
		}
		bad++
		when := "will be refused once strict tile asset gating is enforced (next release)"
		if mode != "legacy" {
			when = "are refused now (--tile-assets=" + mode + ")"
		}
		warn("%s: %d asset reference(s) %s — bx fix assets %s (/docs/elements.md#asset-urls)", t.Component, n, when, t.Component)
	}
	if bad == 0 {
		ok("tile assets: every sandboxed tile loads under strict asset gating (mode: %s)", mode)
	}
}
