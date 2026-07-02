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

	"github.com/magik6k/buxon/internal/jsonc"
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
	Version     int      `json:"version,omitempty"`  // bump when the tile's files change (plans/builtin-updates.md)
	Changelog   string   `json:"changelog,omitempty"`
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

// TemplateEntry is a builtin template's catalog entry, derived from its
// buxon.json "template" block (plans/templates.md). Unlike tiles, templates
// carry no separate tile.json — the marker in buxon.json is the catalog.
type TemplateEntry struct {
	Name        string `json:"name"`        // embedded dir name = builtin id
	Title       string `json:"title"`       //
	Description string `json:"description"` //
	DefaultName string `json:"defaultName"` // suggested instance basename
	DefaultPath string `json:"-"`           // the template's authored path (for rewrite)
}

// TemplateSet is the embedded builtin template catalog.
type TemplateSet struct {
	fsys      fs.FS
	templates map[string]TemplateEntry
}

// LoadTemplates reads every builtin template's buxon.json "template" block from
// the embedded FS (rooted at the builtin-templates directory).
func LoadTemplates(fsys fs.FS) (*TemplateSet, error) {
	s := &TemplateSet{fsys: fsys, templates: map[string]TemplateEntry{}}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join(e.Name(), "buxon.json"))
		if err != nil {
			continue
		}
		var man struct {
			Template *struct {
				Title       string `json:"title"`
				Description string `json:"description"`
				DefaultName string `json:"defaultName"`
			} `json:"template"`
		}
		if err := jsonc.Unmarshal(b, &man); err != nil {
			return nil, fmt.Errorf("builtin-template %s/buxon.json: %w", e.Name(), err)
		}
		if man.Template == nil {
			continue // a dir without a template block isn't a template
		}
		name := man.Template.DefaultName
		if name == "" {
			name = e.Name()
		}
		s.templates[e.Name()] = TemplateEntry{
			Name:        e.Name(),
			Title:       man.Template.Title,
			Description: man.Template.Description,
			DefaultName: name,
			DefaultPath: "apps/" + name,
		}
	}
	return s, nil
}

// List returns builtin template entries sorted by name.
func (s *TemplateSet) List() []TemplateEntry {
	out := make([]TemplateEntry, 0, len(s.templates))
	for _, t := range s.templates {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *TemplateSet) Get(name string) (TemplateEntry, bool) {
	t, ok := s.templates[name]
	return t, ok
}

// Instantiate copies builtin template `name` into workspaceRoot at targetPath
// (empty = "apps/"+DefaultName), stripping the template marker so the copy is
// a normal, plugged-in component. Returns the installed path and files written.
func (s *TemplateSet) Instantiate(workspaceRoot, name, targetPath string) (string, []string, error) {
	t, ok := s.templates[name]
	if !ok {
		return "", nil, fmt.Errorf("no builtin template %q", name)
	}
	targetPath = strings.Trim(strings.TrimSpace(targetPath), "/")
	if targetPath == "" {
		targetPath = t.DefaultPath
	}
	written, err := CopyTree(s.fsys, name, workspaceRoot, targetPath, t.DefaultPath, true)
	if err != nil {
		return "", written, err
	}
	return targetPath, written, nil
}

// filesToSkip are catalog/dev artifacts never copied into a workspace.
func skip(rel string) bool {
	base := filepath.Base(rel)
	if base == "tile.json" {
		return true
	}
	for _, p := range strings.Split(rel, "/") {
		switch p {
		case ".claude", ".git", "deps", "data", "node_modules":
			return true
		}
		if strings.HasPrefix(p, ".") && p != "." {
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
	written, err := CopyTree(s.fsys, name, workspaceRoot, targetPath, m.DefaultPath, false)
	if err != nil {
		return "", written, err
	}
	return targetPath, written, nil
}

// CopyTree copies a component tree from srcFS (rooted at srcRoot) into
// workspaceRoot at targetPath — the shared copy-with-rewrite machinery behind
// both builtin-tile import and template instantiation.
//
// It rewrites the component's *own* authored path (defaultPath) to targetPath
// in text files so self-references (view <script src>, its scope's resource
// ids) point at the new location; cross-component references are left intact.
// A Go backend's manifest, shipped as go.mod.tile so go:embed bundles it (or a
// plain go.mod for a workspace source), is restored/rewritten with the unique
// target module path so two instances coexist in go.work. When stripTemplate
// is set, the buxon.json "template" block is removed so the copy is a normal,
// plugged-in component. Never overwrites an existing component.
func CopyTree(srcFS fs.FS, srcRoot, workspaceRoot, targetPath, defaultPath string, stripTemplate bool) ([]string, error) {
	targetPath = strings.Trim(strings.TrimSpace(targetPath), "/")
	if !util.ComponentPathOK(targetPath) {
		return nil, fmt.Errorf("invalid target path %q", targetPath)
	}
	dstRoot, _, err := util.SafeJoin(workspaceRoot, targetPath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dstRoot, "buxon.json")); err == nil {
		return nil, fmt.Errorf("%s already exists", targetPath)
	}
	files, err := RenderTree(srcFS, srcRoot, targetPath, defaultPath, stripTemplate)
	if err != nil {
		return nil, err
	}
	written, err := WriteTree(dstRoot, targetPath, files)
	return written, err
}

// RenderTree produces the *installed form* of a component tree from srcFS
// (rooted at srcRoot): rel path → bytes, with the same transforms CopyTree
// applies (go.mod.tile→go.mod + module path, own-path rewrite, optional
// template-block strip). Sharing this with the writer lets update detection
// compare the embedded source against a workspace copy byte-for-byte.
func RenderTree(srcFS fs.FS, srcRoot, targetPath, defaultPath string, stripTemplate bool) (map[string][]byte, error) {
	rename := defaultPath != "" && targetPath != defaultPath
	out := map[string][]byte{}
	err := fs.WalkDir(srcFS, srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, srcRoot)
		rel = strings.TrimPrefix(rel, "/")
		if skip(rel) {
			return nil
		}
		data, err := fs.ReadFile(srcFS, p)
		if err != nil {
			return err
		}
		switch {
		case rel == "go.mod.tile":
			rel = "go.mod"
			data = setModulePath(data, targetPath)
		case rel == "go.mod":
			data = setModulePath(data, targetPath)
		case rel == "buxon.json":
			if stripTemplate {
				data = stripTemplateBlock(data)
			}
			if rename {
				data = []byte(strings.ReplaceAll(string(data), defaultPath, targetPath))
			}
		default:
			if rename && textFile(rel) {
				data = []byte(strings.ReplaceAll(string(data), defaultPath, targetPath))
			}
		}
		out[rel] = data
		return nil
	})
	return out, err
}

// WriteTree writes a rendered file set under dstRoot. Returns the workspace-
// relative paths written (targetPath/<rel>).
func WriteTree(dstRoot, targetPath string, files map[string][]byte) ([]string, error) {
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	written := make([]string, 0, len(files))
	for _, rel := range rels {
		out := filepath.Join(dstRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return written, err
		}
		perm := os.FileMode(0o644)
		if rel == "backend/handler" {
			perm = 0o755
		}
		if err := os.WriteFile(out, files[rel], perm); err != nil {
			return written, err
		}
		written = append(written, targetPath+"/"+rel)
	}
	return written, nil
}

// stripTemplateBlock removes the top-level "template" key from a buxon.json so
// an instantiated copy is a normal, plugged-in component. Best-effort: on any
// parse failure the original bytes are returned unchanged.
func stripTemplateBlock(data []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(jsonc.Strip(data), &m) != nil {
		return data
	}
	if _, ok := m["template"]; !ok {
		return data
	}
	delete(m, "template")
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return data
	}
	return append(out, '\n')
}
