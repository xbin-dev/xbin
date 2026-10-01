package broker

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/server"
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
	// The manifest the repo carries now (nil: a new repo): its "template"
	// block is the one every later snapshot keeps (templaterepo_block.go).
	carried, _ := os.ReadFile(filepath.Join(dir, "xbin.json"))
	var manifest, served []byte // the embedded manifest, and as the repo serves it
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
		if rel == "xbin.json" {
			manifest = data
			data = repoManifest(data, carried)
			served = data
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
	// and is ignored) — so the repo accrues a snapshot per version — or when
	// the template block changed, which no file shows: then the snapshot
	// says so, empty if need be (templaterepo_block.go).
	last := ""
	if isRepo(dir) {
		msg, _ := runGitIn(dir, "log", "-1", "--format=%B")
		last = lastBlockHash(msg, carried)
	}
	cur := templateBlockHash(manifest)
	args := []string{"-c", "user.email=xbin@localhost", "-c", "user.name=xbin", "commit", "-q", "-m", "template snapshot"}
	changed := last != "" && cur != last
	if changed {
		args = append(args, "-m", templateBlockNote(manifest, servedPartition(served)))
	}
	if req := servedPartition(served); req != "" && servedPartition(carried) == "" {
		args = append(args, "-m", templateRequestNote(req)) // the first snapshot that asks (D177)
		changed = true
	}
	if cur != "" {
		args = append(args, "-m", templateBlockTrailer+cur)
	}
	if _, err := runGitIn(dir, args...); err != nil && changed {
		_, _ = runGitIn(dir, append(args, "--allow-empty")...)
	}
	_, _ = runGitIn(dir, "update-server-info") // enable dumb-HTTP fetch
	return nil
}

// SeedInstanceRepo gives a fresh builtin-template instance a git history that
// SHARES ANCESTRY with the template's served repo: main = the template's
// current snapshot, plus one commit holding the instantiate rewrites. That is
// what makes the documented upgrade path real — `git fetch template && git
// merge template/main` fast-forwards/merges cleanly, with the snapshot as the
// common ancestor. (Without this the instance got a fresh `git init` root
// from EnsureComponentRepos and every upstream merge refused with "unrelated
// histories".) Must run before EnsureComponentRepos first sees the instance;
// no-op if the dir is already a repo.
func (b *Broker) SeedInstanceRepo(instanceDir, name string) error {
	if !templateNameOK(name) || isRepo(instanceDir) {
		return nil
	}
	tpl := filepath.Join(templateReposDir(b.Reg.Root), name)
	if !isRepo(tpl) {
		return fmt.Errorf("template repo %q not materialized", name)
	}
	if _, err := runGitIn(instanceDir, "init", "-q", "-b", "main"); err != nil {
		return err
	}
	// Bring the snapshot commit in from the local template repo and point
	// main at it WITHOUT touching the working tree (which already holds the
	// rewritten files); the follow-up commit is then exactly the rewrites.
	if _, err := runGitWith(instanceDir, []sandbox.Bind{confine.RO(tpl)}, "fetch", "-q", tpl, "main"); err != nil {
		return err
	}
	if _, err := runGitIn(instanceDir, "update-ref", "refs/heads/main", "FETCH_HEAD"); err != nil {
		return err
	}
	if _, err := runGitIn(instanceDir, "reset", "-q", "--mixed", "HEAD"); err != nil {
		return err
	}
	if _, err := runGitIn(instanceDir, "add", "-A"); err != nil {
		return err
	}
	rel, _ := filepath.Rel(b.Reg.Root, instanceDir)
	_, err := runGitIn(instanceDir,
		"-c", "user.email=xbin@localhost", "-c", "user.name=xbin",
		"commit", "-q", "--allow-empty", "-m", "instantiate "+name+" as "+filepath.ToSlash(rel))
	return err
}

// AddTemplateRemote points an instance's repo at its builtin template's served
// repo as the `template` remote (idempotent), and names the manifest's merge
// driver there (templaterepo_driver.go). No-op if the instance isn't a repo.
func (b *Broker) AddTemplateRemote(instanceDir, name string) {
	if !templateNameOK(name) || !isRepo(instanceDir) {
		return
	}
	url := templateRemoteURL(name)
	if _, err := runGitIn(instanceDir, "remote", "get-url", "template"); err == nil {
		_, _ = runGitIn(instanceDir, "remote", "set-url", "template", url)
	} else {
		_, _ = runGitIn(instanceDir, "remote", "add", "template", url)
	}
	if tile, err := filepath.Rel(b.Reg.Root, instanceDir); err == nil {
		if err := b.ensureTemplateMergeDriver(instanceDir, filepath.ToSlash(tile)); err != nil {
			slog.Warn("template instance: the manifest's merge driver couldn't be set", "instance", tile, "err", err)
		}
	}
}

// TemplateInstanceUpdate is one instance whose builtin template has snapshots
// the instance hasn't merged yet.
type TemplateInstanceUpdate struct {
	Path     string `json:"path"`     // instance component path
	Template string `json:"template"` // builtin template name
	Head     string `json:"head"`     // template repo HEAD (short) it should merge up to
	Legacy   bool   `json:"legacy"`   // pre-seeding instance: unrelated history, one-time --allow-unrelated-histories merge
}

// apiTemplateUpdates GET /templates/updates — instances behind their
// template. All local git plumbing (no fetches): an instance is "behind"
// when the template repo's HEAD commit is not an ancestor of its HEAD —
// which also covers never-fetched instances (the object isn't there at
// all). Filtered to tiles the caller can read.
func (b *Broker) apiTemplateUpdates(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	type tplInfo struct{ head, root string }
	infos := map[string]tplInfo{} // template name → HEAD + root commit (cached per request)
	out := []TemplateInstanceUpdate{}
	for _, c := range b.Reg.Components() {
		if !isRepo(c.Dir) || !p.CanReadTile(c.Path) {
			continue
		}
		url, err := runGitIn(c.Dir, "remote", "get-url", "template")
		if err != nil {
			continue // no template remote — not an instance
		}
		name := strings.TrimSuffix(path.Base(strings.TrimSpace(url)), ".git")
		if !templateNameOK(name) {
			continue
		}
		info, ok := infos[name]
		if !ok {
			tpl := filepath.Join(templateReposDir(b.Reg.Root), name)
			h, herr := runGitIn(tpl, "rev-parse", "main")
			r, rerr := runGitIn(tpl, "rev-list", "--max-parents=0", "main")
			if herr != nil || rerr != nil {
				continue // template repo gone (stripped build) — nothing to offer
			}
			info = tplInfo{head: strings.TrimSpace(h), root: strings.TrimSpace(r)}
			infos[name] = info
		}
		if _, err := runGitIn(c.Dir, "merge-base", "--is-ancestor", info.head, "HEAD"); err == nil {
			continue // current snapshot already merged
		}
		// Legacy = instantiated before repo seeding existed: its history never
		// contained ANY template snapshot (root commit not an ancestor), so
		// the first merge needs --allow-unrelated-histories.
		_, aerr := runGitIn(c.Dir, "merge-base", "--is-ancestor", info.root, "HEAD")
		out = append(out, TemplateInstanceUpdate{
			Path: c.Path, Template: name, Head: info.head[:12], Legacy: aerr != nil,
		})
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"instances": out})
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
