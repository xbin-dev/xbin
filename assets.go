// Package xbin holds the embedded assets shipped inside the xbind binary:
// the core web elements + vendored frontend deps, the builder docs served at
// /docs/, and the workspace template used by `xbind init`.
package xbin

import (
	"embed"
	"io/fs"
)

//go:embed all:web all:docs all:workspace-template all:builtin-tiles all:builtin-templates
var assets embed.FS

func WebFS() fs.FS  { return mustSub("web") }
func DocsFS() fs.FS { return mustSub("docs") }

// TemplateFS is the workspace scaffold; "gitignore" is renamed to
// ".gitignore" at init time (embed skips dotfiles).
func TemplateFS() fs.FS { return mustSub("workspace-template") }

// BuiltinTilesFS is the curated, optional tile set (internal/builtins).
func BuiltinTilesFS() fs.FS { return mustSub("builtin-tiles") }

// BuiltinTemplatesFS is the curated builtin template catalog — clonable
// component blueprints instantiated into named copies (plans/templates.md).
func BuiltinTemplatesFS() fs.FS { return mustSub("builtin-templates") }

func mustSub(dir string) fs.FS {
	f, err := fs.Sub(assets, dir)
	if err != nil {
		panic(err)
	}
	return f
}
