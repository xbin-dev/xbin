package term

import (
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// noTerminal is why p opens no terminal at rel ("" when it may): the path
// is no tile (sessionDir), or p has no terminal level there. err is
// SafeJoin's.
func (m *Manager) noTerminal(p auth.Principal, rel string, err error) string {
	if err == nil && rel != "" {
		if why := m.sessionDir(rel); why != "" {
			return why
		}
		if p.CanTerminalTile(rel) {
			return ""
		}
	}
	return "your account doesn't have terminal access to this tile"
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
