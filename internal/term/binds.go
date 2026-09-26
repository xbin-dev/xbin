package term

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/util"
)

// scopedBinds builds a terminal's workspace binds (plans/runtime.md).
//
//   - A ROOT terminal (rel == "") is the owner plane: the whole workspace
//     read-write (edit anything, create components, workspace-level git).
//   - A COMPONENT terminal (rel != "") is isolated: the workspace is read-only
//     EXCEPT $HOME and this component's own directory (its code + its own .git
//     repo). So a rogue agent can only touch its component and $HOME — never
//     workspace state (xbin.json, AGENTS.md, go.work), runtime data (data/,
//     .xbin/), or other components. Commits work because each component is its
//     own git repo, writable inside the component dir even while the root is ro.
//
// Read-only ExtraBinds (e.g. the SDK) are appended last. rw binds go AFTER the
// ro root so they shadow it at their paths.
//
// hide lists component dirs (workspace-relative) to mask out entirely — the
// D17a source-visibility cut for non-admin users: tiles below read level
// aren't just unlisted in the UI, their source disappears from the terminal's
// filesystem too. nil for admins.
func scopedBinds(root, rel, homeDir string, extra []sandbox.Bind, hide []string) []sandbox.Bind {
	if rel == "" {
		return append([]sandbox.Bind{{Src: root, Dst: root}}, extra...) // owner plane: all rw (root terminals are disabled)
	}
	// Workspace read-only — so a tile terminal sees ALL tiles' source (needed to
	// integrate against another tile's API), but writes only its own dir + $HOME.
	// The bind is recursive (the only option in a rootless userns — see
	// sandbox.Bind), so the workspace's per-resource gocryptfs (resenc) submounts
	// are carried in; their contents are masked below, but their names stay in the
	// terminal's mountinfo. That's an accepted limitation (docs/isolation.md): a
	// terminal user can already `ls` every tile, so the mount-table names disclose
	// nothing new; truly hiding them needs resenc storage outside the workspace.
	binds := []sandbox.Bind{{Src: root, Dst: root, RO: true}}
	// ...and the platform's secrets and other users' data are masked out entirely:
	// .xbin (owner token + frame-token secret), data (vault, the encrypted
	// resource state, and users.json password hashes), and every OTHER user's
	// $HOME. Without these the read-only bind would re-grant owner (`cat
	// .xbin/token`), defeating the tile-scoped terminal token. Applies to every
	// terminal, including the owner's own tiles.
	binds = append(binds,
		sandbox.Bind{Dst: filepath.Join(root, ".xbin"), Mask: true, RO: true},
		sandbox.Bind{Dst: filepath.Join(root, "data"), Mask: true, RO: true},
		sandbox.Bind{Dst: filepath.Join(root, "homes"), Mask: true}, // rw: own $HOME nests below
	)
	if pathIsDir(homeDir) {
		binds = append(binds, sandbox.Bind{Src: homeDir, Dst: homeDir}) // this user's $HOME: read-write (nests over the homes mask)
	}
	comp := filepath.Join(root, filepath.FromSlash(rel))
	binds = append(binds, sandbox.Bind{Src: comp, Dst: comp}) // this component: read-write
	// D17a: mask unreadable tiles' source. Sealed (RO) covers — nothing may
	// nest back on top. Anything overlapping the session's own component is
	// skipped defensively (can't happen while levels stay monotone — terminal
	// implies read — but a mask over the shell's cwd must never win a race
	// against that invariant).
	for _, h := range hide {
		h = strings.Trim(filepath.ToSlash(h), "/")
		if h == "" || h == rel ||
			strings.HasPrefix(rel+"/", h+"/") || strings.HasPrefix(h+"/", rel+"/") {
			continue
		}
		binds = append(binds, sandbox.Bind{Dst: filepath.Join(root, filepath.FromSlash(h)), Mask: true, RO: true})
	}
	return append(binds, extra...)
}

func pathIsDir(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }

// stageView materializes a restricted session's workspace-root VIEW dir
// (D40) under .xbin/term/ (xbind-owned, never bound into any sandbox): the
// redacted root files, a CLAUDE.md → AGENTS.md symlink when AGENTS.md is
// present, an empty .xbin/ (bx locates the workspace by xbin.json + .xbin),
// and a pre-created mountpoint dir for every nested bind — the view is bound
// READ-ONLY at the workspace root, so mountpoints can't be created later.
// Caller removes the dir when the session ends.
func (m *Manager) stageView(rel, homeKey string, readable []string, rootFiles map[string][]byte) (string, error) {
	dir := filepath.Join(m.Root, ".xbin", "term", "view-"+util.RandomToken(8))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	fail := func(err error) (string, error) { os.RemoveAll(dir); return "", err }
	for name, content := range rootFiles {
		if filepath.Base(name) != name { // root files only — no path tricks
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			return fail(err)
		}
	}
	if _, ok := rootFiles["AGENTS.md"]; ok {
		if err := os.Symlink("AGENTS.md", filepath.Join(dir, "CLAUDE.md")); err != nil && !os.IsExist(err) {
			return fail(err)
		}
	}
	mountpoints := append([]string{".xbin", filepath.Join("homes", homeKey), rel}, readable...)
	for _, p := range mountpoints {
		p = strings.Trim(filepath.ToSlash(p), "/")
		if p == "" || strings.HasPrefix(p, "..") {
			continue
		}
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(p)), 0o755); err != nil {
			return fail(err)
		}
	}
	return dir, nil
}

// scopedBindsView is the D40 ALLOW-LIST plan for restricted terminals: the
// staged view dir READ-ONLY at the workspace root (redacted root files, and
// only the mountpoint dirs that need covering), then exactly the binds the
// principal is entitled to — each readable component RO, the session's own
// component RW, the user's $HOME RW — plus the read-only extras (SDK). No
// masks: unreadable tiles don't exist here, names included, and neither do
// .xbin/data contents or the resenc mount-table names the recursive
// real-root bind used to carry.
func scopedBindsView(root, rel, homeDir, viewDir string, readable []string, extra []sandbox.Bind) []sandbox.Bind {
	binds := []sandbox.Bind{{Src: viewDir, Dst: root, RO: true}}
	for _, r := range readable {
		r = strings.Trim(filepath.ToSlash(r), "/")
		if r == "" || r == rel {
			continue // own component binds RW below
		}
		src := filepath.Join(root, filepath.FromSlash(r))
		if pathIsDir(src) {
			binds = append(binds, sandbox.Bind{Src: src, Dst: src, RO: true})
		}
	}
	comp := filepath.Join(root, filepath.FromSlash(rel))
	binds = append(binds, sandbox.Bind{Src: comp, Dst: comp}) // own component: rw
	if pathIsDir(homeDir) {
		binds = append(binds, sandbox.Bind{Src: homeDir, Dst: homeDir}) // own $HOME: rw
	}
	return append(binds, extra...)
}
