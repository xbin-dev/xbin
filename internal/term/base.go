package term

import (
	"fmt"
	"log/slog"
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
// and $HOME are bind mounts, not the overlay. With the workspace's base
// auto-update on (D173, Manager.BaseAutoUpdate) a session's start does that
// itself, for a layer it holds that was built on another base (claimLayer).
// Releasing the bases nothing pins is the boot's (internal/boot: layers.GC
// over layers.Pinned).

// baseVersion reads a rootfs's stamped base version ("v0" when unstamped).
func baseVersion(rootfs string) string { return layers.BaseVersion(rootfs) }

// resolveBase returns the rootfs dir serving base `version`: the current
// rootfs, or a preserved `<rootfs>-<version>` sibling; ok=false when it
// isn't installed.
func resolveBase(rootfs, version string) (string, bool) { return layers.ResolveBase(rootfs, version) }

// ensureLayerBase stamps a terminal layer with its base version on first use and
// returns it: a brand-new layer gets the current base; a pre-existing unstamped
// layer is the legacy base ("v0"). Idempotent. A readable base stamp is kept
// whatever the overlay stamp beside it holds: Read returns the base it read
// with the overlay's error, and a bad overlay stamp must not discard (and
// re-stamp over) the base the layer's upper was built on.
func (m *Manager) ensureLayerBase(layer string) string {
	if s, _ := layers.Read(layer); s.Base != "" {
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

// baseAutoUpdate is the workspace setting (D173): false when it isn't wired.
func (m *Manager) baseAutoUpdate() bool { return m.BaseAutoUpdate != nil && m.BaseAutoUpdate() }

// BaseAutoUpdateOn reports the workspace's base auto-update setting as the
// terminals apply it (GET /ws/term/env says it, so the window can say what
// the next session does instead of offering the update).
func (m *Manager) BaseAutoUpdateOn() bool { return m.baseAutoUpdate() }

// moveBase is the decision a session's start makes for the persistent layer
// it holds (claimLayer): move it to the current base — discard it, so the
// session builds a fresh one there — when base auto-update is on and the
// layer was built on another base. Otherwise it stays pinned to its own.
// A layer another live session holds is never asked: the new session runs
// on an ephemeral upper, and the running one keeps its base until it ends.
func moveBase(auto bool, layerBase, current string) bool {
	return auto && layerBase != current
}

// layerClaim is what a session's start made of its tile's persistent layer.
type layerClaim struct {
	dir   string // .xbin/term/<key>
	base  string // the rootfs dir to stack it on
	moved string // the base it was moved off ("" = it stayed)
}

// claimLayer takes the tile's persistent terminal layer for a session that
// is starting, and pins it: held=true when another live session holds it
// (the new one gets an ephemeral upper; nothing about the layer changes).
// A layer built on another base moves to the current one when base
// auto-update is on (moveBase): it is removed the way a reset removes it
// (confined) and stamped afresh — before anything mounts it, so no session
// is yanked. A removal that fails fails the start: what it left may be
// half a layer, which nothing mounts; the next start tries again. With
// auto-update off, a layer whose base isn't installed fails the start
// (reset it to rebuild). On an error the claim is released; a caller that
// can't use the claim releases it (releaseEnv).
func (m *Manager) claimLayer(envKey string) (c layerClaim, held bool, err error) {
	if !m.acquireEnv(envKey) {
		return layerClaim{}, true, nil
	}
	c.dir = filepath.Join(m.Root, ".xbin", "term", envKey)
	ver := m.ensureLayerBase(c.dir) // stamp on first use (new→current, legacy→v0)
	if cur := baseVersion(m.Rootfs); moveBase(m.baseAutoUpdate(), ver, cur) {
		if err := m.removeLayer(c.dir); err != nil {
			m.releaseEnv(envKey)
			slog.Error("terminal layer: moving it to the current base image failed", "layer", envKey, "base", ver, "err", err)
			return layerClaim{}, false, fmt.Errorf("this terminal's layer couldn't be moved to the new base image (%v) — open the terminal again, or reset it", err)
		}
		m.ensureLayerBase(c.dir) // a fresh layer: the current base
		slog.Info("terminal layer moved to the current base image (base auto-update)", "layer", envKey, "from", ver, "to", cur)
		c.base, c.moved = m.Rootfs, ver
		return c, false, nil
	}
	base, ok := resolveBase(m.Rootfs, ver) // pin the upper to the base it was built on
	if !ok {
		// The base this layer was built on isn't installed — refuse rather
		// than corrupt its apt/dpkg state on a different base (the startup
		// gate normally prevents reaching here). Reset the terminal to upgrade.
		m.releaseEnv(envKey)
		return layerClaim{}, false, fmt.Errorf("this terminal's base image %q is not installed — reset the terminal to rebuild on the current base", ver)
	}
	c.base = base
	return c, false, nil
}

// baseMovedNote is what a session says when its start moved the tile's
// layer to the current base; a shell prints it first, grey (baseMovedLine).
const (
	baseMovedNote = "xbin: this tile's terminal layer moved to the new base image — system changes (apt installs, /etc) were reset; your files and $HOME are kept"
	baseMovedLine = "\x1b[90m" + baseMovedNote + "\x1b[0m\r\n"
)

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
// when isolation is on. With base auto-update on (D173) such a layer is let
// through, logged: its next session discards it rather than stack it
// (claimLayer). It gates on .xbin/term only: a tile sandbox whose base is
// gone fails its own start instead (plans/tile-sandbox-runtime.md §7).
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
	auto := m.baseAutoUpdate()
	var bad []string
	for _, l := range ls {
		if l.Tree != layers.TreeTerm || !l.Missing {
			continue
		}
		if auto {
			slog.Warn("a terminal layer's base image isn't installed: its next session moves it to the current base (base auto-update)", "layer", l.Key, "base", l.Base)
			continue
		}
		bad = append(bad, l.Key+"→"+l.Base)
	}
	if len(bad) > 0 {
		return fmt.Errorf("terminal sandbox layers pinned to base images that aren't installed: %s.\n"+
			"A base upgrade must preserve the old base as %s-<version> (deploy/install.sh does this on upgrade).\n"+
			"Restore the missing base(s), reset the affected terminal(s) by removing their layer dir under .xbin/term/, "+
			"or turn base auto-update back on (\"baseAutoUpdate\": true in data/workspace-settings.json) so their next session rebuilds them on the current base",
			strings.Join(bad, ", "), m.Rootfs)
	}
	return nil
}
