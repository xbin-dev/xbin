package tilesbx

// keys.go — everything derived from a Key lives here: where its definitions
// and state are, its sandboxes' registry ids, cgroup leaves and archive
// keys, and the two hooks the deployment work fills (CodeRoot,
// ResourceMount). Every map, path, id and book of the runtime is keyed by
// one; nothing else builds these paths.

import (
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// Key names one set of sandboxes: a tile's, in one of its deployments.
type Key struct {
	Tile       string // the manager tile's path
	Deployment string // "" = main (the only one until dev-lifecycle's deployments land)
}

// KeyOf is the key a manager call names: its own tile's. It becomes
// Deployment: p.Deployment once principals carry a deployment.
func KeyOf(p auth.Principal) Key { return Key{Tile: p.Component} }

// Main reports the main deployment's key.
func (k Key) Main() bool { return k.Deployment == "" }

// CK is the tile's component key (util.CompKey): short and path-safe.
func (k Key) CK() string { return util.CompKey(k.Tile) }

// errDeployment answers every path a non-main key would need until its
// deployment's layout exists (dev-lifecycle's TileKey).
func errDeployment() *Error {
	return refuse(RefUnsupported, "sandboxes of a non-main deployment aren't supported yet")
}

// defsPath is the definitions file: data/sandboxes.json for main (keyed by
// tile inside); a deployment's own file later.
func (m *Manager) defsPath() string { return filepath.Join(m.root, "data", "sandboxes.json") }

// StateRoot is the key's state directory: .xbin/sbx/<CK>/ for main,
// .xbin/deploy/<TK>/d/<d>/sbx/ for a deployment (not yet).
func (m *Manager) StateRoot(k Key) (string, error) {
	if !k.Main() {
		return "", errDeployment()
	}
	return filepath.Join(m.root, ".xbin", "sbx", k.CK()), nil
}

// StateDir is one sandbox's state directory under StateRoot. name must have
// passed validName.
func (m *Manager) StateDir(k Key, name string) (string, error) {
	root, err := m.StateRoot(k)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

// RegistryID is the sandbox's id in the sandbox registry (internal/sbx):
// tile:<CK>:<name>, or tile+<d>:<CK>:<name> for a deployment's.
func RegistryID(k Key, name string) string {
	if k.Main() {
		return "tile:" + k.CK() + ":" + name
	}
	return "tile+" + k.Deployment + ":" + k.CK() + ":" + name
}

// Leaf is the running sandbox's cgroup leaf: sbx-<CK>-<name>, or
// sbx-<CK>+<d>-<name>. Every leaf starts "sbx-", which the boot sweep uses.
func Leaf(k Key, name string) string {
	if k.Main() {
		return "sbx-" + k.CK() + "-" + name
	}
	return "sbx-" + k.CK() + "+" + k.Deployment + "-" + name
}

// ArchiveKey is where an archived sandbox lives in the tile's archiver:
// <CK>.sbx.<name> (a deployment's needs its TileKey: "" until then).
func ArchiveKey(k Key, name string) string {
	if !k.Main() {
		return ""
	}
	return k.CK() + ".sbx." + name
}

// CodeRoot is what a {source:true} mount binds read-only: the tile's
// directory (a deployment's pinned code, later).
func (m *Manager) CodeRoot(k Key) (string, error) {
	if !k.Main() {
		return "", errDeployment()
	}
	return filepath.Join(m.root, filepath.FromSlash(k.Tile)), nil
}

// ResourceMount resolves a resource the key's tile would mount, in the
// key's data namespace (main's, until deployments have their own).
func (m *Manager) ResourceMount(k Key, res string) (MountSource, error) {
	if !k.Main() {
		return MountSource{}, errDeployment()
	}
	if m.deps.Mounts == nil {
		return MountSource{}, refuse(RefInvalid, "resource mounts aren't available here")
	}
	return m.deps.Mounts.ResourceMount(k.Tile, res)
}
