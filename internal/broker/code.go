package broker

import (
	"bytes"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/server"
	"github.com/magik6k/xbin/internal/util"
)

// Component code & git endpoints for the Admin console: browse a component's
// files and its git history/diffs. Admin-gated, OR readable by a component
// granted "code:<target>" (requireCodeRead) — like the rest of
// the console) — this exposes source and history across the whole workspace,
// which is owner-level. History is scoped to a component's path within the
// single workspace repo (decision D2); there are no per-component repos.

var revRE = regexp.MustCompile(`^[0-9a-fA-F]{4,40}$`)

const maxFileBytes = 512 << 10

func (b *Broker) registerCode(srv *server.Server) {
	srv.RegisterAPI("GET /code/tree", b.apiCodeTree)
	srv.RegisterAPI("GET /code/file", b.apiCodeFile)
	srv.RegisterAPI("GET /git/log", b.apiGitLog)
	srv.RegisterAPI("GET /git/diff", b.apiGitDiff)
	srv.RegisterAPI("GET /git/remote-info", b.apiGitRemoteInfo)
	srv.RegisterAPI("POST /git/import", b.apiGitImport)
	srv.RegisterAPI("GET /workspace/git", b.apiWorkspaceGit)
	srv.RegisterAPI("POST /workspace/commit", b.apiWorkspaceCommit)
}

// apiWorkspaceGit reports the core workspace repo's HEAD + how many paths are
// uncommitted — for the shell's "commit workspace" control. Admin only.
func (b *Broker) apiWorkspaceGit(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	root := b.Reg.Root
	if !isRepo(root) {
		server.WriteJSON(w, http.StatusOK, map[string]any{"repo": false})
		return
	}
	head := map[string]string{}
	if out, err := runGitIn(root, "log", "-1", "--no-color", "--pretty=format:%h%x1f%s%x1f%aI"); err == nil {
		if f := strings.Split(out, "\x1f"); len(f) == 3 {
			head = map[string]string{"short": f[0], "subject": f[1], "date": f[2]}
		}
	}
	dirty := 0
	if st, err := runGitIn(root, "status", "--porcelain"); err == nil {
		for _, l := range strings.Split(st, "\n") {
			if strings.TrimSpace(l) != "" {
				dirty++
			}
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"repo": true, "head": head, "dirty": dirty})
}

// apiWorkspaceCommit snapshots the core workspace repo (git add -A + commit).
// Component sub-repos ride along as pinned gitlinks. Admin only.
func (b *Broker) apiWorkspaceCommit(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	root := b.Reg.Root
	if !isRepo(root) {
		if _, err := runGitIn(root, "init", "-q"); err != nil {
			server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	var body struct {
		Message string `json:"message"`
	}
	_ = decodeJSON(r, &body)
	msg := strings.TrimSpace(body.Message)
	if msg == "" {
		msg = "workspace snapshot"
	}
	if st, _ := runGitIn(root, "status", "--porcelain"); strings.TrimSpace(st) == "" {
		server.WriteJSON(w, http.StatusOK, map[string]any{"committed": false, "note": "nothing to commit — the workspace is clean"})
		return
	}
	if _, err := runGitIn(root, "add", "-A"); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if _, err := runGitIn(root, "-c", "user.email=xbin@localhost", "-c", "user.name=xbin", "commit", "-q", "-m", msg); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	head := map[string]string{}
	if out, err := runGitIn(root, "log", "-1", "--no-color", "--pretty=format:%h%x1f%s"); err == nil {
		if f := strings.Split(out, "\x1f"); len(f) == 2 {
			head = map[string]string{"short": f[0], "subject": f[1]}
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"committed": true, "head": head})
}

// component validates the ?component= query param and returns its OS dir.
func (b *Broker) component(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	comp := strings.Trim(r.URL.Query().Get("component"), "/")
	if !util.ComponentPathOK(comp) {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad component path"})
		return "", "", false
	}
	dir := filepath.Join(b.Reg.Root, filepath.FromSlash(comp))
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such component"})
		return "", "", false
	}
	return comp, dir, true
}

// runGitIn runs a git subcommand in dir (a component's own repo — each component
// is its own repo, plans/lifecycle.md), returning stdout (and, on failure, stderr
// as the error text).
func runGitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), &gitError{msg}
	}
	return out.String(), nil
}

type gitError struct{ msg string }

func (e *gitError) Error() string { return e.msg }

func isRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// gitInitComponent makes a component directory its own git repo with a baseline
// commit, unless it already is one. Idempotent and non-destructive: a component
// terminal edits + commits inside this repo even though the workspace root is
// read-only to it (plans/runtime.md). The identity is only for the baseline
// commit; the owner/agents author later commits.
func gitInitComponent(dir string) error {
	if isRepo(dir) {
		return nil
	}
	if _, err := runGitIn(dir, "init", "-q", "-b", "main"); err != nil {
		return err
	}
	_, _ = runGitIn(dir, "add", "-A")
	_, _ = runGitIn(dir,
		"-c", "user.email=xbin@localhost", "-c", "user.name=xbin",
		"commit", "-q", "--allow-empty", "-m", "initial commit")
	return nil
}

// EnsureComponentRepos gives every component its own git repo (idempotent). Run
// at start and after structural changes so new/imported/instantiated components
// are versioned; existing ones are migrated on first sight.
func (b *Broker) EnsureComponentRepos() {
	for _, c := range b.Reg.Components() {
		if err := gitInitComponent(c.Dir); err != nil {
			slog.Debug("git init component", "path", c.Path, "err", err)
		}
	}
}

// componentRemote returns a component repo's origin URL, or "" if none/no repo.
func componentRemote(dir string) string {
	if !isRepo(dir) {
		return ""
	}
	out, err := runGitIn(dir, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// requireCodeRead gates the read-only source endpoints: admin sees any
// component, an element sees its own source, or another component's when
// granted the "code:<component>" capability (plans/auth.md). This is the
// runtime equivalent of what a workspace terminal already sees read-only.
func (b *Broker) requireCodeRead(w http.ResponseWriter, r *http.Request, comp string) bool {
	p := auth.PrincipalOf(r)
	if b.IsAdmin(p) || (p.Component != "" && p.Component == comp) {
		return true
	}
	if p.Component != "" {
		// "code" (bare) = read ANY component's source (tooling: linters, stats,
		// search); "code:<comp>" = just that one.
		for _, t := range []string{"code", "code:" + comp} {
			if role, ok := b.grantedRole(p.Component, t); ok && roleSatisfies(role, "reader", nil) {
				return true
			}
		}
	}
	server.WriteJSON(w, http.StatusForbidden, map[string]string{
		"error": "reading a component's source needs admin, or a `code:" + comp + "` grant (one component) / `code` grant (all) — declare uses {target: \"code:" + comp + "\", role: \"reader\"}",
		"docs":  "/docs/auth.md",
	})
	return false
}

// GET /code/tree?component=<path> — the component's files (flat, sorted).
func (b *Broker) apiCodeTree(w http.ResponseWriter, r *http.Request) {
	comp, dir, ok := b.component(w, r)
	if !ok {
		return
	}
	if !b.requireCodeRead(w, r, comp) {
		return
	}
	type file struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	var files []file
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != dir && (name == ".git" || name == "node_modules" || name == "data") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 { // don't follow deps/ symlinks
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		info, _ := d.Info()
		var sz int64
		if info != nil {
			sz = info.Size()
		}
		files = append(files, file{Path: filepath.ToSlash(rel), Size: sz})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	server.WriteJSON(w, http.StatusOK, map[string]any{"component": comp, "files": files})
}

// GET /code/file?component=<path>&file=<rel> — one file's current content.
func (b *Broker) apiCodeFile(w http.ResponseWriter, r *http.Request) {
	comp, dir, ok := b.component(w, r)
	if !ok {
		return
	}
	if !b.requireCodeRead(w, r, comp) {
		return
	}
	rel := r.URL.Query().Get("file")
	p, _, err := util.SafeJoin(dir, rel)
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad file path"})
		return
	}
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such file"})
		return
	}
	if fi.Size() > maxFileBytes {
		server.WriteJSON(w, http.StatusOK, map[string]any{"path": rel, "truncated": true, "size": fi.Size()})
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if bytes.IndexByte(firstN(data, 8<<10), 0) >= 0 {
		server.WriteJSON(w, http.StatusOK, map[string]any{"path": rel, "binary": true, "size": fi.Size()})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"path": rel, "content": string(data)})
}

// GET /git/log?component=<path>&limit=N — commits touching the component.
func (b *Broker) apiGitLog(w http.ResponseWriter, r *http.Request) {
	comp, dir, ok := b.component(w, r)
	if !ok {
		return
	}
	if !b.requireCodeRead(w, r, comp) {
		return
	}
	if !isRepo(dir) {
		server.WriteJSON(w, http.StatusOK, map[string]any{"repo": false, "commits": []any{}})
		return
	}
	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	// The component's own repo — its whole history is the component's history.
	out, err := runGitIn(dir, "log", "--no-color", "-n", strconv.Itoa(limit),
		"--pretty=format:%H%x1f%h%x1f%an%x1f%aI%x1f%s")
	if err != nil {
		server.WriteJSON(w, http.StatusOK, map[string]any{"repo": true, "commits": []any{}, "note": err.Error(), "remote": componentRemote(dir)})
		return
	}
	commits := []map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x1f")
		if len(f) != 5 {
			continue
		}
		commits = append(commits, map[string]string{
			"hash": f[0], "short": f[1], "author": f[2], "date": f[3], "subject": f[4],
		})
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"repo": true, "commits": commits, "remote": componentRemote(dir)})
}

// GET /git/diff?component=<path>&rev=<hash> — a commit's diff scoped to the
// component, or (rev empty) the component's uncommitted changes vs HEAD.
func (b *Broker) apiGitDiff(w http.ResponseWriter, r *http.Request) {
	comp, dir, ok := b.component(w, r)
	if !ok {
		return
	}
	if !b.requireCodeRead(w, r, comp) {
		return
	}
	if !isRepo(dir) {
		server.WriteJSON(w, http.StatusOK, map[string]any{"repo": false, "diff": ""})
		return
	}
	rev := r.URL.Query().Get("rev")
	var out string
	var err error
	if rev == "" {
		// Uncommitted changes in this component (vs last commit).
		out, err = runGitIn(dir, "diff", "--no-color", "HEAD")
		if err != nil { // no commits yet
			out, err = runGitIn(dir, "diff", "--no-color")
		}
	} else {
		if !revRE.MatchString(rev) {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad revision"})
			return
		}
		out, err = runGitIn(dir, "show", "--no-color", rev)
	}
	if err != nil {
		server.WriteJSON(w, http.StatusOK, map[string]any{"repo": true, "diff": "", "note": err.Error()})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"repo": true, "diff": out})
}

func firstN(b []byte, n int) []byte {
	if len(b) < n {
		return b
	}
	return b[:n]
}
