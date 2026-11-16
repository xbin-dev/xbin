package term

import (
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/util"
)

// noTerminal is why p opens no terminal at rel ("" when it may), and the
// tile the session is on: the path is no tile (sessionTile), or p has no
// terminal level on the TILE it is, or lies in. err is SafeJoin's.
func (m *Manager) noTerminal(p auth.Principal, rel string, err error) (tile, why string) {
	if err == nil && rel != "" {
		if tile, why = m.sessionTile(rel); why != "" {
			return "", why
		}
		if p.CanTerminalTile(tile) {
			return tile, ""
		}
	}
	return "", noTerminalLevel
}

// noTerminalLevel is the refusal of a session on a tile the caller has no
// terminal level on.
const noTerminalLevel = "your account doesn't have terminal access to this tile"

// sessionTile is the tile a session whose directory is rel opens on, or why
// it opens on none (sessionDir). Every level, owner, org, policy, target,
// partition and listing of the session is that tile's (TileOf): a level
// looked up for the directory's own path would meet no exact entry, no
// owner and no org — only patterns — so `apps/*: terminal` beside an exact
// `apps/crm: none` opened a shell at apps/crm/src, and a personal pattern
// reached into an org's tile past D31. The directory stays where the
// session starts, inside that tile's sandbox.
func (m *Manager) sessionTile(rel string) (tile, why string) {
	if why = m.sessionDir(rel); why != "" {
		return "", why
	}
	tile, _ = m.TileOf(rel) // sessionDir found it, with a registry
	return tile, ""
}

// TileOf is the registered tile the workspace-relative path rel is, or lies
// in — TileAt, the registry's Resolve that boot installs — and ok; rel
// itself (trimmed) when the manager knows no registry. A path in no
// registered tile (nor a sealed component's) is not ok, and rel comes back
// as it was asked. A session's directory may lie inside its tile: every
// decision about the session is made on the tile this names, never on the
// directory's own path, which matches only patterns.
func (m *Manager) TileOf(rel string) (tile string, ok bool) {
	rel = strings.Trim(rel, "/")
	if m.TileAt == nil {
		return rel, rel != ""
	}
	if tile, ok = m.TileAt(rel); !ok || tile == "" {
		return rel, false
	}
	return tile, true
}

// onTile is the tile o's session is on (sessionTile, at the gate); rel, its
// directory, for an open that names none.
func (o openOpts) onTile(rel string) string {
	if o.tile != "" {
		return o.tile
	}
	return rel
}

// tileOf is the tile s is on, fixed at its start (sessionTile); a session
// made without one is on the tile its directory resolves to now.
func (m *Manager) tileOf(s *Session) string {
	if s.tile != "" {
		return s.tile
	}
	tile, _ := m.TileOf(s.Cwd)
	return tile
}

// onTileOrCwd is tileOf without the manager: the tile s was opened on, else
// its directory.
func (s *Session) onTileOrCwd() string {
	if s.tile != "" {
		return s.tile
	}
	return s.Cwd
}

// historyTile is the tile a past session ran on: the one its directory
// resolves to (TileOf); its directory itself once that tile is gone.
func (m *Manager) historyTile(meta HistoryMeta) string {
	tile, _ := m.TileOf(meta.Cwd)
	return tile
}

// dirWithoutLink reports whether root/rel is a directory reached from root
// without a symlink at any step (fsutil.OpenIn, as the code API and the /c/
// plane reach a tile's directory): a session's directory is bound
// read-write and decided on the tile it lies in, and a link inside a tile
// (apps/a/sub -> ../b) would make that another tile's directory.
func dirWithoutLink(root, rel string) bool {
	d, err := fsutil.OpenIn(root, rel, "")
	if err != nil {
		return false
	}
	defer d.Close()
	fi, err := d.Stat()
	return err == nil && fi.IsDir()
}

// termKey is the per-component key for a terminal's persistent layer.
func termKey(rel string) string {
	if rel == "" {
		return "_root"
	}
	return util.CompKey(rel)
}

// sessionDir says why a terminal or agent session can't open at the
// workspace-relative path rel ("" when it can): a session opens on a tile —
// a registered component, or a directory inside one.
//
// The session's directory is bound read-write into its sandbox, over the
// masks that hide xbind's own trees from every terminal (binds.go). The
// only gate on it used to be the caller's terminal level at that path, and
// an admin has that everywhere: cwd=.xbin let an admin's shell replace the
// owner token and the frame-token secret, cwd=data rewrite the users file
// and move the vault, cwd=homes write into anyone's home. (Reads there
// stayed refused by the sandbox; writes did not.) So the path is checked
// for what it is, before anyone's level is asked:
//
//   - it has the shape of a component path: no reserved top-level name
//     (.xbin, data, homes, vendor, xbin, …), no hidden or ".." segment;
//   - and it is, or lies in, a registered tile (TileAt: the registry's
//     Resolve, installed by boot), when the manager knows the registry.
func (m *Manager) sessionDir(rel string) string {
	const no = "a terminal opens on a tile: "
	if util.SealedPath(rel) { // no source and no dev sandbox (D227)
		return util.CanonPath(rel) + " is a system component, managed by xbin: it has no terminal"
	}
	if !util.ComponentPathOK(rel) {
		return no + rel + " is not one (xbind's own directories and hidden paths take no terminal)"
	}
	if m.TileAt != nil {
		if _, ok := m.TileAt(rel); !ok {
			return no + rel + " is not a registered tile, nor inside one"
		}
	}
	return ""
}
