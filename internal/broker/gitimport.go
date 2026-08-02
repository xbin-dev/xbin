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

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
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
	// Importing creates a tile at `path` — same authority as /create (create
	// patterns work; the confused-deputy clamp applies to attributed humans).
	if ok, msg := b.canCreateAt(auth.PrincipalOf(r), path, ""); !ok {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": msg, "docs": "/docs/auth.md"})
		return
	}
	if !util.ComponentPathOK(path) || util.ReservedTop[strings.Split(path, "/")[0]] {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad or reserved import path: " + path})
		return
	}
	if err := b.guardNewComponentTree(path); err != nil {
		server.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
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
	// It must actually be a xbin component.
	if !fileExists(filepath.Join(target, "xbin.json")) && !fileExists(filepath.Join(target, "index.html")) {
		_ = os.RemoveAll(target)
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "not a xbin component (no xbin.json or index.html at the repo root)"})
		return
	}

	// Make it visible so we can validate it (it already has .git + origin from
	// the clone, so EnsureComponentRepos leaves it alone).
	_ = b.Reg.Rescan()

	// Reject BEFORE provisioning/mounting: a `uses` that references a resource or
	// component that doesn't exist is a hard error, not a warning — most often a
	// tile hard-coding the scope it was authored under, which no longer matches
	// after an import-rename. Never let such a component into the workspace.
	warnings := b.unresolvedUses(path)
	for _, c := range b.Reg.Components() {
		if c.Path != path && hasPrefix(c.Path, path) {
			warnings = append(warnings, b.unresolvedUses(c.Path)...)
		}
	}
	if len(warnings) > 0 {
		_ = os.RemoveAll(target)
		_ = b.Reg.Rescan()
		server.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"error":    "import rejected: the component references things that don't exist — fix its xbin.json and re-import",
			"warnings": warnings,
		})
		return
	}

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
	owner, _ := b.resolveCreateOwner(auth.PrincipalOf(r), "") // D24: creator-owned (workspace-owned for admins)
	b.assignOwner(path, owner)
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"path": path, "remote": url, "ref": body.Ref, "pendingGrants": pending,
	})
}

// unresolvedUses returns human-readable descriptions of a component's `uses`
// targets that reference something that doesn't exist — a resource in no scope,
// or a component that isn't installed. These are otherwise silent (the grant/env
// is just skipped at spawn), which hides typos like a tile referencing the scope
// it was authored under before an import-rename (plans/vault-data.md).
func (b *Broker) unresolvedUses(comp string) []string {
	c, ok := b.Reg.Component(comp)
	if !ok {
		return nil
	}
	var out []string
	for _, u := range c.Manifest.Uses {
		t := u.Target
		switch {
		case strings.HasPrefix(t, "res:"):
			if _, res, ok := b.parseRes(t); !ok || res == nil {
				out = append(out, comp+": uses "+t+" — no such resource (typo, or a stale pre-rename scope?)")
			}
		case strings.HasPrefix(t, "net:"), strings.HasPrefix(t, "gpu:"), t == "code":
			// capability targets — always resolvable ("code" = read-all-source)
		case strings.HasPrefix(t, "code:"):
			if _, ok := b.Reg.Component(strings.TrimPrefix(t, "code:")); !ok {
				out = append(out, comp+": uses "+t+" — no such component to read source of")
			}
		default:
			name := t
			if i := strings.IndexByte(name, ':'); i >= 0 {
				name = name[:i]
			}
			if _, ok := b.Reg.Component(name); !ok {
				out = append(out, comp+": uses "+t+" — no such component")
			}
		}
	}
	return out
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
