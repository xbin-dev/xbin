package broker

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/magik6k/buxon/internal/server"
	"github.com/magik6k/buxon/internal/util"
)

// Component code & git endpoints for the Admin console: browse a component's
// files and its git history/diffs. Admin-gated (buxon:admin, like the rest of
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

// runGit runs a git subcommand in the workspace repo, returning stdout (and,
// on failure, stderr as the error text).
func (b *Broker) runGit(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", b.Reg.Root}, args...)...)
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

func (b *Broker) hasRepo() bool {
	_, err := os.Stat(filepath.Join(b.Reg.Root, ".git"))
	return err == nil
}

// GET /code/tree?component=<path> — the component's files (flat, sorted).
func (b *Broker) apiCodeTree(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	comp, dir, ok := b.component(w, r)
	if !ok {
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
	if !b.requireAdmin(w, r) {
		return
	}
	_, dir, ok := b.component(w, r)
	if !ok {
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
	if !b.requireAdmin(w, r) {
		return
	}
	comp, _, ok := b.component(w, r)
	if !ok {
		return
	}
	if !b.hasRepo() {
		server.WriteJSON(w, http.StatusOK, map[string]any{"repo": false, "commits": []any{}})
		return
	}
	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	out, err := b.runGit("log", "--no-color", "-n", strconv.Itoa(limit),
		"--pretty=format:%H%x1f%h%x1f%an%x1f%aI%x1f%s", "--", comp)
	if err != nil {
		server.WriteJSON(w, http.StatusOK, map[string]any{"repo": true, "commits": []any{}, "note": err.Error()})
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
	server.WriteJSON(w, http.StatusOK, map[string]any{"repo": true, "commits": commits})
}

// GET /git/diff?component=<path>&rev=<hash> — a commit's diff scoped to the
// component, or (rev empty) the component's uncommitted changes vs HEAD.
func (b *Broker) apiGitDiff(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	comp, _, ok := b.component(w, r)
	if !ok {
		return
	}
	if !b.hasRepo() {
		server.WriteJSON(w, http.StatusOK, map[string]any{"repo": false, "diff": ""})
		return
	}
	rev := r.URL.Query().Get("rev")
	var out string
	var err error
	if rev == "" {
		// Uncommitted changes for this component (vs last commit).
		out, err = b.runGit("diff", "--no-color", "HEAD", "--", comp)
		if err != nil { // no commits yet
			out, err = b.runGit("diff", "--no-color", "--", comp)
		}
	} else {
		if !revRE.MatchString(rev) {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad revision"})
			return
		}
		out, err = b.runGit("show", "--no-color", rev, "--", comp)
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
