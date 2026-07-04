package broker

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/magik6k/buxon/internal/server"
	"github.com/magik6k/buxon/internal/util"
)

// Install a component from a git remote (GitHub/GitLab/any git URL): each
// component is its own repo (plans/lifecycle.md), so importing one is a clone.
// The Tile Manager's "from git" tab inspects a URL (default branch + tags) then
// clones it in — the clone's origin remote makes it updatable via `git pull`.

// validGitURL allows only remote git URLs (https/http/git/ssh + scp-like
// user@host:path). It rejects local paths, file://, and git's dangerous ext::/
// fd:: transports, and anything starting with '-' (option injection — we also
// pass `--` before the URL as a second guard).
func validGitURL(u string) bool {
	if u == "" || strings.HasPrefix(u, "-") || strings.Contains(u, "\n") {
		return false
	}
	for _, s := range []string{"https://", "http://", "git://", "ssh://"} {
		if strings.HasPrefix(u, s) {
			return true
		}
	}
	// scp-like: user@host:path (no scheme, not an absolute/relative local path).
	if at := strings.IndexByte(u, '@'); at > 0 {
		rest := u[at+1:]
		if colon := strings.IndexByte(rest, ':'); colon > 0 && !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, ".") {
			return true
		}
	}
	return false
}

// repoNameFromURL derives a default component slug from a git URL's last segment.
func repoNameFromURL(u string) string {
	u = strings.TrimSuffix(strings.TrimRight(u, "/"), ".git")
	name := u
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		name = u[i+1:]
	}
	return util.Slugify(name)
}

func gitEnv() []string { return append(os.Environ(), "GIT_TERMINAL_PROMPT=0") }

// apiGitRemoteInfo (GET /git/remote-info?url=) inspects a remote without cloning:
// its default branch and tags (newest first), so the UI can offer versions.
func (b *Broker) apiGitRemoteInfo(w http.ResponseWriter, r *http.Request) {
	if !b.requireWriter(w, r) {
		return
	}
	url := strings.TrimSpace(r.URL.Query().Get("url"))
	if !validGitURL(url) {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "provide a git URL (https://…, git@…:…, ssh://…)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--symref", "--", url)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		server.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "cannot reach the repo: " + firstLine(string(out))})
		return
	}
	var defaultBranch string
	var tags []string
	for _, line := range strings.Split(string(out), "\n") {
		if s, ok := strings.CutPrefix(line, "ref: "); ok { // "ref: refs/heads/main\tHEAD"
			if b, _, _ := strings.Cut(s, "\t"); strings.HasPrefix(b, "refs/heads/") {
				defaultBranch = strings.TrimPrefix(b, "refs/heads/")
			}
			continue
		}
		_, ref, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if t, ok := strings.CutPrefix(ref, "refs/tags/"); ok {
			tags = append(tags, strings.TrimSuffix(t, "^{}")) // deref annotated tags
		}
	}
	tags = dedupSortTags(tags)
	server.WriteJSON(w, http.StatusOK, map[string]any{"defaultBranch": defaultBranch, "tags": tags, "remote": url})
}

// dedupSortTags removes duplicates (annotated-tag ^{} lines) and sorts newest
// first with a version-aware comparison so v1.10.0 > v1.9.0.
func dedupSortTags(tags []string) []string {
	seen := map[string]bool{}
	out := tags[:0]
	for _, t := range tags {
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return util.VersionLess(out[j], out[i]) })
	return out
}

// apiGitImport (POST /git/import {url, path?, ref?}) clones a remote component in.
func (b *Broker) apiGitImport(w http.ResponseWriter, r *http.Request) {
	if !b.requireWriter(w, r) {
		return
	}
	var body struct{ URL, Path, Ref string }
	if err := decodeJSON(r, &body); err != nil || !validGitURL(strings.TrimSpace(body.URL)) {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {url, path?, ref?} with a valid git URL"})
		return
	}
	url := strings.TrimSpace(body.URL)
	path := strings.Trim(strings.TrimSpace(body.Path), "/")
	if path == "" {
		path = "apps/" + repoNameFromURL(url)
	}
	if !util.ComponentPathOK(path) || util.ReservedTop[strings.Split(path, "/")[0]] {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad or reserved import path: " + path})
		return
	}
	target := filepath.Join(b.Reg.Root, filepath.FromSlash(path))
	if _, err := os.Stat(target); err == nil {
		server.WriteJSON(w, http.StatusConflict, map[string]string{"error": path + " already exists"})
		return
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	clone := exec.CommandContext(ctx, "git", "clone", "--", url, target)
	clone.Env = gitEnv()
	if out, err := clone.CombinedOutput(); err != nil {
		_ = os.RemoveAll(target)
		server.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "clone failed: " + firstLine(string(out))})
		return
	}
	if ref := strings.TrimSpace(body.Ref); ref != "" {
		if out, err := runGitIn(target, "checkout", "--quiet", ref); err != nil {
			_ = os.RemoveAll(target)
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "no such tag/branch " + ref + ": " + firstLine(err.Error()) + firstLine(out)})
			return
		}
	}
	// It must actually be a buxon component.
	if !fileExists(filepath.Join(target, "buxon.json")) && !fileExists(filepath.Join(target, "index.html")) {
		_ = os.RemoveAll(target)
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "not a buxon component (no buxon.json or index.html at the repo root)"})
		return
	}

	// Make it usable now (it already has .git + origin from the clone, so
	// EnsureComponentRepos leaves it alone).
	_ = b.Reg.Rescan()
	if b.OnStructureChange != nil {
		b.OnStructureChange()
	}
	b.Provision()

	var pending []registryGrantLite
	for _, g := range b.Pending() {
		if hasPrefix(g.From, path) {
			pending = append(pending, registryGrantLite{From: g.From, Target: g.Target, Role: g.Role})
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"path": path, "remote": url, "ref": body.Ref, "pendingGrants": pending,
	})
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
