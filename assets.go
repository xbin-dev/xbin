// Package buxon holds the embedded assets shipped inside the buxond binary:
// the core web elements + vendored frontend deps, the builder docs served at
// /docs/, and the workspace template used by `buxond init`.
package buxon

import (
	"embed"
	"io/fs"
)

//go:embed all:web all:docs all:workspace-template all:builtin-tiles
var assets embed.FS

func WebFS() fs.FS  { return mustSub("web") }
func DocsFS() fs.FS { return mustSub("docs") }

// TemplateFS is the workspace scaffold; "gitignore" is renamed to
// ".gitignore" at init time (embed skips dotfiles).
func TemplateFS() fs.FS { return mustSub("workspace-template") }

// BuiltinTilesFS is the curated, optional tile set (internal/builtins).
func BuiltinTilesFS() fs.FS { return mustSub("builtin-tiles") }

func mustSub(dir string) fs.FS {
	f, err := fs.Sub(assets, dir)
	if err != nil {
		panic(err)
	}
	return f
}
