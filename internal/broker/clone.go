package broker

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// POST /api/xbin/clone — fork an existing component. Copies its directory
// (including .git, so the fork stays related to its source repo), rewrites
// references to the old path (the xbin.json self-scope `res:` uses plus any
// hardcoded occurrences in code — the same replace a human `sed` would do),
// and registers the copy as an independent component.
//
// Deliberately NOT copied: vault secrets (secrets don't fork) and resource
// DATA (kv/filesystem/sqlite/blob start empty — a fork is a new app).
// Cross-scope `uses` re-enter the owner-approval flow like any import
// (pendingGrants in the response). Same capability gate as /create.
func (b *Broker) apiClone(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := decodeJSON(r, &body); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {from, to}"})
		return
	}
	from := strings.Trim(strings.TrimSpace(body.From), "/")
	to := strings.Trim(strings.TrimSpace(body.To), "/")
	if from == "" || to == "" || from == to {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {from, to} with distinct paths"})
		return
	}
	// Cloning creates a tile at `to` — same authority as /create (a user's
	// create patterns work; the confused-deputy clamp applies) — and copies
	// the source, so a human must be able to READ `from` (otherwise a
	// manager-style tile is a source-exfiltration route).
	p := auth.PrincipalOf(r)
	if ok, msg := b.canCreateAt(p, to); !ok {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": msg, "docs": "/docs/auth.md"})
		return
	}
	if !b.attributedCanRead(p, from) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{
			"error": "cloning copies the source — your account has no read access to " + from, "docs": "/docs/auth.md",
		})
		return
	}
	src, ok := b.Reg.Component(from)
	if !ok {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such component: " + from})
		return
	}
	if !util.ComponentPathOK(to) || util.ReservedTop[strings.Split(to, "/")[0]] {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad or reserved clone path: " + to})
		return
	}
	// Nesting either way is a mess: not inside an existing component, not a
	// subtree containing one, and no nesting with the clone source.
	if err := b.guardNewComponentTree(to); err != nil {
		server.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if strings.HasPrefix(to+"/", from+"/") || strings.HasPrefix(from+"/", to+"/") {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "clone destination must not nest with the source"})
		return
	}
	target, _, err := util.SafeJoin(b.Reg.Root, to)
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if _, err := os.Stat(target); err == nil {
		server.WriteJSON(w, http.StatusConflict, map[string]string{"error": to + " already exists"})
		return
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	fail := func(code int, payload map[string]any) {
		_ = os.RemoveAll(target)
		_ = b.Reg.Rescan()
		server.WriteJSON(w, code, payload)
	}

	if err := copyTree(src.Dir, target); err != nil {
		fail(http.StatusInternalServerError, map[string]any{"error": "copy failed: " + err.Error()})
		return
	}
	rewritten, err := rewriteRefs(target, from, to)
	if err != nil {
		fail(http.StatusInternalServerError, map[string]any{"error": "rewrite failed: " + err.Error()})
		return
	}

	_ = b.Reg.Rescan()

	// Same hard gate as git import: a clone whose `uses` don't resolve (a
	// hardcoded path the rewrite couldn't reach, or a genuinely broken source)
	// never enters the workspace.
	warnings := b.unresolvedUses(to)
	for _, c := range b.Reg.Components() {
		if c.Path != to && hasPrefix(c.Path, to) {
			warnings = append(warnings, b.unresolvedUses(c.Path)...)
		}
	}
	if len(warnings) > 0 {
		fail(http.StatusBadRequest, map[string]any{
			"error":    "clone rejected: the copy references things that don't exist — fix the source's xbin.json and re-clone",
			"warnings": warnings,
		})
		return
	}

	// The fork keeps the source's repo/history (or gets a fresh repo if the
	// source never had one); commit the path rewrite so it starts clean.
	b.EnsureComponentRepos()
	if len(rewritten) > 0 && isRepo(target) {
		_, _ = runGitIn(target, "add", "-A")
		_, _ = runGitIn(target,
			"-c", "user.email=xbin@localhost", "-c", "user.name=xbin",
			"commit", "-q", "-m", "fork from "+from)
	}

	if b.OnStructureChange != nil {
		b.OnStructureChange()
	}
	b.Provision()

	var pending []registryGrantLite
	for _, g := range b.Pending() {
		if hasPrefix(g.From, to) {
			pending = append(pending, registryGrantLite{From: g.From, Target: g.Target, Role: g.Role})
		}
	}
	owner, _ := b.resolveCreateOwner(auth.PrincipalOf(r), "") // D24: creator-owned (workspace-owned for admins)
	b.assignOwner(to, owner)
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"path": to, "from": from, "rewritten": rewritten, "pendingGrants": pending,
	})
}

// copyTree copies src into dst (which must not exist): dirs, regular files
// (mode preserved), and symlinks (as links). Other node types are skipped.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			t, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(t, out)
		case d.IsDir():
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(out, info.Mode().Perm())
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			o, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(o, in); err != nil {
				o.Close()
				return err
			}
			return o.Close()
		default:
			return nil // sockets/devices — not part of a component
		}
	})
}

// rewriteRefs replaces whole-word occurrences of the old component path with
// the new one across the copied tree's text files, skipping .git and
// binaries. Returns the rewritten files (slash-relative, sorted).
func rewriteRefs(dir, from, to string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > 4<<20 {
			return nil // not a source file
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if bytes.IndexByte(raw[:min(len(raw), 8000)], 0) >= 0 {
			return nil // binary
		}
		next, changed := replaceWord(string(raw), from, to)
		if !changed {
			return nil
		}
		if err := os.WriteFile(p, []byte(next), info.Mode().Perm()); err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out, err
}

// replaceWord substitutes whole-word occurrences of from with to. "Word"
// boundaries are [A-Za-z0-9_-]: `apps/x` matches inside `res:apps/x/home`
// and `/api/apps/x/ws` (slashes delimit) but never inside `apps/x2` or
// `apps/x-y` — so a fork of apps/x can't corrupt references to siblings
// whose names merely extend it.
func replaceWord(s, from, to string) (string, bool) {
	isWord := func(c byte) bool {
		return c == '-' || c == '_' ||
			('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
	}
	var bld strings.Builder
	changed := false
	for i := 0; i < len(s); {
		j := strings.Index(s[i:], from)
		if j < 0 {
			bld.WriteString(s[i:])
			break
		}
		j += i
		end := j + len(from)
		bld.WriteString(s[i:j])
		if (j == 0 || !isWord(s[j-1])) && (end >= len(s) || !isWord(s[end])) {
			bld.WriteString(to)
			changed = true
		} else {
			bld.WriteString(from)
		}
		i = end
	}
	if !changed {
		return s, false
	}
	return bld.String(), true
}
