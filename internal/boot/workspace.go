package boot

import (
	"bytes"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xbin-dev/xbin"
	"github.com/xbin-dev/xbin/internal/builtins"
)

// homeSkel maps per-user home dotfiles to their embedded template sources
// (the on-disk names carry the dot; go:embed sources can't).
var homeSkel = map[string]string{
	".zshrc":        "home/zshrc",
	".bashrc":       "home/bashrc",
	".bash_profile": "home/bash_profile",
}

// seedHomeSkeleton writes any missing skeleton dotfiles into a per-user home
// (term.Manager.SeedHome). Idempotent: user edits are never overwritten.
func seedHomeSkeleton(dir string) error {
	for dst, src := range homeSkel {
		p := filepath.Join(dir, dst)
		if _, err := os.Lstat(p); err == nil {
			continue
		}
		b, err := fs.ReadFile(xbin.TemplateFS(), src)
		if err != nil {
			continue
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// pristineHomeFile reports whether a legacy home/ file is byte-identical to the
// template skeleton — the home migration's both-forms check (a home/ recreated
// by an older xbind's backfill is safe to drop; anything else is not).
func pristineHomeFile(rel string, data []byte) bool {
	src, ok := homeSkel[rel]
	if !ok {
		return false
	}
	b, err := fs.ReadFile(xbin.TemplateFS(), src)
	return err == nil && bytes.Equal(b, data)
}

// templateRename maps template names to their real destinations (go:embed
// skips dotfiles, so the template stores them undotted).
var templateRename = map[string]string{
	"gitignore":         ".gitignore",
	"home/zshrc":        "home/.zshrc",
	"home/bashrc":       "home/.bashrc",
	"home/bash_profile": "home/.bash_profile",
}

// seedTemplateFile writes one template file into the workspace if (and only
// if) its destination doesn't exist yet.
func seedTemplateFile(dir, name string) error {
	dst := name
	if r, ok := templateRename[name]; ok {
		dst = r
	}
	out := filepath.Join(dir, filepath.FromSlash(dst))
	if _, err := os.Stat(out); err == nil {
		return nil // never overwrite
	}
	b, err := fs.ReadFile(xbin.TemplateFS(), name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(out, b, 0o644)
}

// InitWorkspace scaffolds a new workspace from the embedded template and
// (decision D2) makes it a git repo. Never overwrites existing files.
// `xbind init <dir>` and the auto-init of an empty mount both call it.
func InitWorkspace(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tpl := xbin.TemplateFS()
	err := fs.WalkDir(tpl, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return seedTemplateFile(dir, p)
	})
	if err != nil {
		return err
	}
	for _, sub := range []string{".xbin", "data", "home", "apps", "lib"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	// Agents discover builder guidance via either name; keep one source.
	if _, err := os.Lstat(filepath.Join(dir, "CLAUDE.md")); err != nil {
		_ = os.Symlink("AGENTS.md", filepath.Join(dir, "CLAUDE.md"))
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if git, err := exec.LookPath("git"); err == nil {
			cmd := exec.Command(git, "init", "-q")
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				slog.Warn("git init failed", "out", string(out))
			}
		}
	}
	// Record scaffold provenance so a future xbind can offer updates to these
	// components without clobbering the user's edits (plans/builtin-updates.md).
	if err := builtins.NewUpdater(dir, nil, xbin.TemplateFS()).RecordScaffoldSeed(); err != nil {
		slog.Warn("record scaffold provenance", "err", err)
	}
	return nil
}

// locateBx returns the directory containing the `bx` CLI so it can be put on
// terminals' PATH. Resolution order: XBIN_BIN override; next to the xbind
// binary (container /opt/xbin/bin, `make build` bin/); dev repo bin/; then
// whatever is already on xbind's PATH.
func locateBx(override string, dev bool) string {
	if override != "" {
		return override
	}
	if exe, err := os.Executable(); err == nil {
		if d := filepath.Dir(exe); isFile(filepath.Join(d, "bx")) {
			return d
		}
	}
	if dev {
		if src := devSourceDir(); src != "" && isFile(filepath.Join(src, "bin", "bx")) {
			return filepath.Join(src, "bin")
		}
	}
	if p, err := exec.LookPath("bx"); err == nil {
		return filepath.Dir(p)
	}
	return ""
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// extractFS materializes an embedded FS to a directory (idempotent overwrite).
func extractFS(src fs.FS, dst string) error {
	return fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out := filepath.Join(dst, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		b, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
}

// devSourceDir finds the repo root when running via `go run ./cmd/xbind`.
func devSourceDir() string {
	if _, err := os.Stat("web/bx-frame.js"); err == nil {
		wd, _ := os.Getwd()
		return wd
	}
	return ""
}

// parseBytes reads a byte count — a plain integer or a K/M/G/T suffix (e.g.
// "2G", "512M") — falling back to def on anything malformed (logged).
func parseBytes(k, v string, def int64) int64 {
	raw := v
	v = strings.TrimSpace(v)
	if v == "" {
		return def
	}
	mult := int64(1)
	switch v[len(v)-1] {
	case 'k', 'K':
		mult, v = 1<<10, v[:len(v)-1]
	case 'm', 'M':
		mult, v = 1<<20, v[:len(v)-1]
	case 'g', 'G':
		mult, v = 1<<30, v[:len(v)-1]
	case 't', 'T':
		mult, v = 1<<40, v[:len(v)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n <= 0 {
		slog.Warn("ignoring malformed byte-size env var", "key", k, "value", raw)
		return def
	}
	return n * mult
}
