// Package builtins is buxon's curated, optional tile set: tiles that ship
// embedded in the binary (this repo) but are NOT auto-installed — you import
// them into a workspace on demand (`bx tile import`, or the Tile Manager's
// Import tab). This is the first rung of the tile-sharing ladder; see
// plans/tile-sharing.md for how it extends to peer export/import and a
// community registry.
//
// Each tile is a directory under builtin-tiles/<name>/ carrying a tile.json
// catalog entry alongside its normal component files. Importing copies those
// files into the workspace at a target path (default = the tile's authored
// path), optionally rewriting the tile's own path so it can be installed
// under a different name.
package builtins

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/magik6k/buxon/internal/util"
)

// Meta is a tile.json catalog entry.
type Meta struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	DefaultPath string   `json:"defaultPath"`
	Tags        []string `json:"tags,omitempty"`
	Provides    []string `json:"provides,omitempty"` // component paths dependents rely on
	Requires    []string `json:"requires,omitempty"` // other builtin tile names
}

// Set is the embedded builtin tile catalog.
type Set struct {
	fsys  fs.FS
	tiles map[string]Meta
}

// Load reads every builtin tile's tile.json from the embedded FS (rooted at
// the builtin-tiles directory).
func Load(fsys fs.FS) (*Set, error) {
	s := &Set{fsys: fsys, tiles: map[string]Meta{}}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join(e.Name(), "tile.json"))
		if err != nil {
			continue // a dir without tile.json isn't a catalog tile
		}
		var m Meta
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("builtin %s/tile.json: %w", e.Name(), err)
		}
		if m.Name == "" {
			m.Name = e.Name()
		}
		if m.DefaultPath == "" {
			m.DefaultPath = "apps/" + m.Name
		}
		s.tiles[m.Name] = m
	}
	return s, nil
}

// List returns catalog entries sorted by name.
func (s *Set) List() []Meta {
	out := make([]Meta, 0, len(s.tiles))
	for _, m := range s.tiles {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Set) Get(name string) (Meta, bool) {
	m, ok := s.tiles[name]
	return m, ok
}

// filesToSkip are catalog/dev artifacts never copied into a workspace.
func skip(rel string) bool {
	base := filepath.Base(rel)
	if base == "tile.json" {
		return true
	}
	for _, p := range strings.Split(rel, "/") {
		if p == ".claude" || p == ".git" || strings.HasPrefix(p, ".") && p != "." {
			return true
		}
	}
	return false
}

// setModulePath rewrites the `module …` line of a go.mod to modPath, keeping
// the rest intact. Ensures each imported Go tile has a unique module path.
func setModulePath(gomod []byte, modPath string) []byte {
	lines := strings.Split(string(gomod), "\n")
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "module ") {
			lines[i] = "module " + modPath
			break
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// textFile reports whether a file's path should be treated as text for the
// default-path rewrite (used only when installing under a non-default path).
func textFile(rel string) bool {
	switch filepath.Ext(rel) {
	case ".html", ".js", ".css", ".json", ".go", ".md", ".mod", ".txt", ".jsonc":
		return true
	}
	return false
}

// Import copies builtin tile `name` into workspaceRoot at `targetPath`
// (empty = the tile's DefaultPath). If the target differs from the tile's
// authored path, occurrences of the authored path are rewritten in text
// files so the tile's own self-references (view script src, its scope's
// resource ids, etc.) point at the new location. Cross-tile references (to
// *other* tiles' paths) are left untouched. Never overwrites an existing
// component. Returns the installed path and the files written.
func (s *Set) Import(workspaceRoot, name, targetPath string) (string, []string, error) {
	m, ok := s.tiles[name]
	if !ok {
		return "", nil, fmt.Errorf("no builtin tile %q", name)
	}
	targetPath = strings.Trim(strings.TrimSpace(targetPath), "/")
	if targetPath == "" {
		targetPath = m.DefaultPath
	}
	if !util.ComponentPathOK(targetPath) {
		return "", nil, fmt.Errorf("invalid target path %q", targetPath)
	}
	dstRoot, _, err := util.SafeJoin(workspaceRoot, targetPath)
	if err != nil {
		return "", nil, err
	}
	if _, err := os.Stat(filepath.Join(dstRoot, "buxon.json")); err == nil {
		return "", nil, fmt.Errorf("%s already exists", targetPath)
	}

	rename := targetPath != m.DefaultPath

	var written []string
	err = fs.WalkDir(s.fsys, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, name+"/")
		if skip(rel) {
			return nil
		}
		data, err := fs.ReadFile(s.fsys, p)
		if err != nil {
			return err
		}
		// A Go backend's manifest ships as go.mod.tile so go:embed (which
		// skips nested modules) still bundles the tile; restore it on import
		// and set its module path to the (unique) target component path, so
		// importing the same tile twice doesn't collide in go.work.
		if rel == "go.mod.tile" {
			rel = "go.mod"
			data = setModulePath(data, targetPath)
		} else if rename && textFile(rel) {
			data = []byte(strings.ReplaceAll(string(data), m.DefaultPath, targetPath))
		}
		out := filepath.Join(dstRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if rel == "backend/handler" {
			perm = 0o755
		}
		if err := os.WriteFile(out, data, perm); err != nil {
			return err
		}
		written = append(written, targetPath+"/"+rel)
		return nil
	})
	if err != nil {
		return "", written, err
	}
	return targetPath, written, nil
}
