package term

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/layers"
)

// Base-image versioning for terminal sandbox layers (plans/component-env.md):
// thin wrappers over internal/layers, which pins terminal layers
// (.xbin/term/<key>) and tile sandboxes (.xbin/sbx) alike. A terminal's
// persistent overlay upper records apt installs and the dpkg/apt state
// copied up from the base it was built on, so each layer is stamped with
// that base and PINNED to it; "upgrading" a terminal to a newer base means
// discarding the upper (the existing reset action) — safe, because tile code
// and $HOME are bind mounts, not the overlay. Releasing the bases nothing
// pins is the boot's (internal/boot: layers.GC over layers.Pinned).

// baseVersion reads a rootfs's stamped base version ("v0" when unstamped).
func baseVersion(rootfs string) string { return layers.BaseVersion(rootfs) }

// resolveBase returns the rootfs dir serving base `version`: the current
// rootfs, or a preserved `<rootfs>-<version>` sibling; ok=false when it
// isn't installed.
func resolveBase(rootfs, version string) (string, bool) { return layers.ResolveBase(rootfs, version) }

// ensureLayerBase stamps a terminal layer with its base version on first use and
// returns it: a brand-new layer gets the current base; a pre-existing unstamped
// layer is the legacy base ("v0"). Idempotent.
func (m *Manager) ensureLayerBase(layer string) string {
	if s, err := layers.Read(layer); err == nil && s.Base != "" {
		return s.Base
	}
	ver := layers.BaseVersion(m.Rootfs) // brand-new layer → the current base
	if _, err := os.Stat(layer); err == nil {
		ver = layers.Legacy // pre-existing unstamped upper → the legacy base
	}
	_ = os.MkdirAll(layer, 0o755)
	_ = layers.Stamp(layer, layers.Stamps{Base: ver})
	return ver
}

// EnvStatus reports a component's persistent terminal layer before any
// terminal is open: whether one exists, and whether it was built on an older
// base image than the current rootfs (the terminal window's "base update").
func (m *Manager) EnvStatus(rel string) (exists, outdated bool) {
	key := termKey(rel)
	_, err := os.Stat(filepath.Join(m.Root, ".xbin", "term", key))
	return err == nil, m.layerOutdated(key)
}

// layerOutdated reports whether a held layer's base differs from the current
// rootfs (so the tile can offer an upgrade/reset).
func (m *Manager) layerOutdated(envKey string) bool {
	if envKey == "" || m.Rootfs == "" {
		return false
	}
	return layers.Outdated(filepath.Join(m.Root, ".xbin", "term", envKey), m.Rootfs)
}

// CheckBaseImages is the startup safety gate: it refuses to run if any existing
// terminal layer is pinned to a base image that isn't installed — stacking its
// upper on a different base would corrupt apt/dpkg state. Called once at boot
// when isolation is on. It gates on .xbin/term only: a tile sandbox whose base
// is gone fails its own start instead (plans/tile-sandbox-runtime.md §7).
func (m *Manager) CheckBaseImages() error {
	if m.Rootfs == "" {
		return nil
	}
	dir := filepath.Join(m.Root, ".xbin", "term")
	// view-* dirs are D40 per-session STAGED VIEWS, not env layers — a
	// daemon restart orphans any live sessions' views, and treating them
	// as layers pinned to a phantom base crash-looped a production boot
	// (2026-08-02). Sweep them here: anything present at boot is dead.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), "view-") {
				_ = os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}
	}
	ls, _ := layers.Check(m.Root, m.Rootfs)
	var bad []string
	for _, l := range ls {
		if l.Tree == layers.TreeTerm && l.Missing {
			bad = append(bad, l.Key+"→"+l.Base)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("terminal sandbox layers pinned to base images that aren't installed: %s.\n"+
			"A base upgrade must preserve the old base as %s-<version> (deploy/install.sh does this on upgrade).\n"+
			"Restore the missing base(s), or reset the affected terminal(s) by removing their layer dir under .xbin/term/",
			strings.Join(bad, ", "), m.Rootfs)
	}
	return nil
}
