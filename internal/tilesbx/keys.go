package tilesbx

// keys.go — everything derived from a Key lives here: where its definitions
// and state are, its sandboxes' registry ids, cgroup leaves and archive
// keys, and the two hooks the deployment work fills (CodeRoot,
// ResourceMount). Every map, path, id and book of the runtime is keyed by
// one; nothing else builds these paths. keyOf is the one place a key is
// built from a caller (plans/tile-sandbox-runtime.md §1.4).

import (
	"fmt"
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// Key names one set of sandboxes: a tile's, in one of its deployments.
type Key struct {
	Tile       string // the manager tile's path
	Deployment string // "" = main (the only one until dev-lifecycle's deployments land)
}

// keyOf is the key a manager call names: its own tile's, in its own
// deployment. Every gate and ServeRuntime call it; nothing else reads
// p.Component to name sandboxes.
//
// Only main keys are built so far. A principal of a non-main deployment
// (auth.Principal.Deployment, "" or "main" being main) answers
// errDeployment (501) — never main's key, which would hand a dev deployment
// of the manager main's sandboxes, files and quota. Its own set (its
// definitions and state under its TileKey, its code and data namespace,
// books summed into the tile's) is the follow-up in
// plans/tile-sandbox-runtime.md §14.
//
// A person's partition of a partitioned tile (auth.Principal.Partition
// "user:<id>") has no sandboxes: they are the global instance's alone
// (plans/partitions PD-28; the API's partition gate refuses the routes
// first), so it answers not-allowed — never the tile's key.
func keyOf(p auth.Principal) (Key, error) {
	if p.Partition.IsUser() {
		return Key{}, refuse(RefNotAllowed, "a person's partition (%s) has no tile sandboxes: they are the global instance's", p.Partition)
	}
	k := keyFor(p.Component, p.Deployment)
	if !k.Main() {
		return Key{}, errDeployment()
	}
	return k, nil
}

// keyFor is tile's key in deployment dep, "" and "main" both naming main.
func keyFor(tile, dep string) Key {
	if dep == util.MainDeployment {
		dep = ""
	}
	return Key{Tile: tile, Deployment: dep}
}

// Main reports the main deployment's key.
func (k Key) Main() bool { return k.Deployment == "" }

// CK is the tile's component key (util.CompKey): short and path-safe.
func (k Key) CK() string { return util.CompKey(k.Tile) }

// errDeployment answers every path a non-main key would need until its
// deployment's layout exists (dev-lifecycle's TileKey), and every manager
// route a non-main deployment's principal calls.
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

// StateDir is one sandbox's state directory under StateRoot:
// <name>.<uid>. The uid, not the name, is the sandbox's identity: a
// sandbox deleted and created again under the same name gets another uid,
// so it never meets the old one's state (§1.3).
func (m *Manager) StateDir(k Key, d *Def) (string, error) {
	root, err := m.StateRoot(k)
	if err != nil {
		return "", err
	}
	if validName(d.Name) != nil || !validUID(d.UID) {
		return "", fmt.Errorf("tile sandboxes: no state dir for %q (uid %q)", d.Name, d.UID)
	}
	return filepath.Join(root, d.Name+"."+d.UID), nil
}

// CurDir is the sandbox's state proper, StateDir/cur: the one unit a
// restore swaps whole.
func (m *Manager) CurDir(k Key, d *Def) (string, error) {
	dir, err := m.StateDir(k, d)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cur"), nil
}

// TrashDir is where the key's deleted, reset and restored-over state waits
// for the confined remover: <state root>/.trash (entries <uid>[.<rand>]).
func (m *Manager) TrashDir(k Key) (string, error) {
	root, err := m.StateRoot(k)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".trash"), nil
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
// <CK>.sbx.<name>.<uid> (a deployment's needs its TileKey: "" until then).
func ArchiveKey(k Key, d *Def) string {
	if !k.Main() {
		return ""
	}
	return k.CK() + ".sbx." + d.Name + "." + d.UID
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
