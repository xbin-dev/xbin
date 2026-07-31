package broker

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/util"
)

// Template source repos (plans/agent-v2.md §template updates). The builtin-tile
// updater deliberately never touches template *instances* — they're meant to
// diverge after the copy, so a blind merge would clobber the builder's work.
// Instead we materialize each builtin template as a git repo, serve it
// read-only, and add it as a `template` remote to every instance: a builder
// pulls upstream fixes with `git fetch template && git merge`/`cherry-pick`,
// in control of what they adopt (the fork-upstream model).

func templateReposDir(root string) string { return filepath.Join(root, ".xbin", "template-repos") }

func templateNameOK(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// MaterializeTemplateRepos writes each builtin template's current files into a
// git repo (committing only when something changed) and refreshes the dumb-HTTP
// server info. Idempotent; safe to call at every startup. Best-effort per repo.
func (b *Broker) MaterializeTemplateRepos(tfs fs.FS) {
	entries, err := fs.ReadDir(tfs, ".")
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && templateNameOK(e.Name()) {
			_ = materializeTemplateRepo(b.Reg.Root, tfs, e.Name())
		}
	}
}

func materializeTemplateRepo(root string, tfs fs.FS, name string) error {
	dir := filepath.Join(templateReposDir(root), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Mirror the embedded files into the working tree.
	err := fs.WalkDir(tfs, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(name, p)
		data, rerr := fs.ReadFile(tfs, p)
		if rerr != nil {
			return rerr
		}
		out := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
	if err != nil {
		return err
	}
	if !isRepo(dir) {
		if _, err := runGitIn(dir, "init", "-q", "-b", "main"); err != nil {
			return err
		}
	}
	if _, err := runGitIn(dir, "add", "-A"); err != nil {
		return err
	}
	// Commit only when something is staged ("nothing to commit" exits non-zero
	// and is ignored) — so the repo accrues a snapshot per version.
	_, _ = runGitIn(dir,
		"-c", "user.email=xbin@localhost", "-c", "user.name=xbin",
		"commit", "-q", "-m", "template snapshot")
	_, _ = runGitIn(dir, "update-server-info") // enable dumb-HTTP fetch
	return nil
}

// AddTemplateRemote points an instance's repo at its builtin template's served
// repo as the `template` remote (idempotent). No-op if the instance isn't a repo.
func (b *Broker) AddTemplateRemote(instanceDir, name string) {
	if !templateNameOK(name) || !isRepo(instanceDir) {
		return
	}
	url := "http://xbin/api/xbin/templates/" + name + ".git"
	if _, err := runGitIn(instanceDir, "remote", "get-url", "template"); err == nil {
		_, _ = runGitIn(instanceDir, "remote", "set-url", "template", url)
		return
	}
	_, _ = runGitIn(instanceDir, "remote", "add", "template", url)
}

// serveTemplateRepo serves a template repo's git dir read-only over dumb HTTP
// (git falls back to it for fetch). Any authenticated principal: builtin
// template sources are embedded in the binary — identical in every install,
// no secrets — and tile terminals (tile-scoped tokens, not admin) fetch their
// `template` remote from here.
func (b *Broker) serveTemplateRepo(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSuffix(r.PathValue("repo"), ".git")
	if !templateNameOK(name) {
		http.Error(w, "bad template", http.StatusBadRequest)
		return
	}
	gitDir := filepath.Join(templateReposDir(b.Reg.Root), name, ".git")
	full, _, err := util.SafeJoin(gitDir, r.PathValue("rest"))
	if err != nil {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, full)
}
