package term

import (
	"errors"
	"fmt"
	"io/fs"
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
// auto-update on (D175, Manager.BaseAutoUpdate) a session's start does that
// itself, for a layer it holds that was built on another base (claimLayer;
// basemove.go). Releasing the bases nothing pins is the boot's
// (internal/boot: layers.GC over layers.Pinned).

// baseVersion reads a rootfs's stamped base version ("v0" when unstamped).
func baseVersion(rootfs string) string { return layers.BaseVersion(rootfs) }

// resolveBase returns the rootfs dir serving base `version`: the current
// rootfs, or a preserved `<rootfs>-<version>` sibling; ok=false when it
// isn't installed.
func resolveBase(rootfs, version string) (string, bool) { return layers.ResolveBase(rootfs, version) }

// layerBase is a terminal layer's base, stamped on first use: a brand-new
// layer is created and stamped with the current base cur; a pre-existing
// layer without a stamp predates them and is stamped the legacy base ("v0").
// A stamp that is there but can't be read is an error and is left as it
// is — never taken for a missing one, which would re-stamp the layer v0 and
// have base auto-update discard a layer that is on the current base — and
// so is a stamp that can't be written. A readable base stamp is kept
// whatever the overlay stamp beside it holds.
func (m *Manager) layerBase(layer, cur string) (string, error) {
	ver, err := layers.ReadBase(layer)
	if err != nil {
		return "", fmt.Errorf("its base stamp can't be read: %w", err)
	}
	if ver != "" {
		return ver, nil
	}
	fresh := false
	fi, err := os.Stat(layer)
	switch {
	case err == nil && fi.IsDir():
		ver = layers.Legacy // a pre-existing unstamped upper
	case err == nil:
		return "", fmt.Errorf("%s isn't a directory", layer)
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(layer, 0o755); err != nil {
			return "", err
		}
		ver, fresh = cur, true
	default:
		return "", err
	}
	if err := m.stamp(layer, layers.Stamps{Base: ver}); err != nil {
		if fresh {
			_ = os.Remove(layer) // empty: the next start makes it again
		}
		return "", fmt.Errorf("stamp its base: %w", err)
	}
	return ver, nil
}

// baseAutoUpdate is the workspace setting (D175): false when it isn't wired.
func (m *Manager) baseAutoUpdate() bool { return m.BaseAutoUpdate != nil && m.BaseAutoUpdate() }

// BaseAutoUpdateOn reports the workspace's base auto-update setting as the
// terminals apply it (GET /ws/term/env says it, so the window can say what
// the next session does instead of offering the update).
func (m *Manager) BaseAutoUpdateOn() bool { return m.baseAutoUpdate() }

// moveBase is the decision a session's start makes for the persistent layer
// it holds (claimLayer): move it to the current base — discard it, so the
// session builds a fresh one there — when base auto-update is on and the
// layer was built on another base. Otherwise it stays pinned to its own.
// Never onto an unstamped rootfs (current = Legacy): that is no base to move
// to — a dev rootfs, or one whose version couldn't be told apart from none.
// A layer another live session holds is never asked: the new session runs
// on an ephemeral upper, and the running one keeps its base until it ends.
func moveBase(auto bool, layerBase, current string) bool {
	return auto && current != layers.Legacy && layerBase != current
}

// layerClaim is what a session's start made of its tile's persistent layer.
type layerClaim struct {
	dir   string // .xbin/term/<key> (a person's: .xbin/term-part/<TK>/<pkey>)
	base  string // the rootfs dir to stack it on
	moved string // the base it was moved off ("" = it stayed)
}

// claimLayer takes the tile's persistent terminal layer for a session that
// is starting, and pins it: held=true when another live session holds it
// (the new one gets an ephemeral upper; nothing about the layer changes).
// A layer built on another base moves to the current one when base
// auto-update is on (moveBase, moveLayer) — before anything mounts it, so no
// session is yanked. What can't be read is never guessed at: a current base
// version or a layer stamp that can't be read fails the start, with the
// layer untouched, as does a move that can't complete (nothing half-made is
// mounted; the next start tries again). With auto-update off, a layer whose
// base isn't installed fails the start (reset it to rebuild). On an error
// the claim is released; a caller that can't use the claim releases it
// (releaseEnv).
func (m *Manager) claimLayer(envKey string) (c layerClaim, held bool, err error) {
	if !m.acquireEnv(envKey) {
		return layerClaim{}, true, nil
	}
	defer func() {
		if err != nil {
			m.releaseEnv(envKey)
			slog.Error("terminal layer: the session can't use it", "layer", envKey, "err", err)
		}
	}()
	c.dir = m.layerDir(envKey) // .xbin/term/<key>, or a person's .xbin/term-part/<TK>/<pkey>
	cur, err := m.currentBase()
	if err != nil {
		return layerClaim{}, false, fmt.Errorf("this terminal's layer can't be pinned: %w", err)
	}
	ver, err := m.layerBase(c.dir, cur)
	if err != nil {
		return layerClaim{}, false, fmt.Errorf("this terminal's layer can't be opened: %w — open the terminal again, or reset it", err)
	}
	if moveBase(m.baseAutoUpdate(), ver, cur) {
		if err := m.moveLayer(c.dir, cur); err != nil {
			return layerClaim{}, false, fmt.Errorf("this terminal's layer couldn't be moved to the new base image (%v) — open the terminal again, or reset it", err)
		}
		slog.Info("terminal layer moved to the current base image (base auto-update)", "layer", envKey, "from", ver, "to", cur)
		c.base, c.moved = m.Rootfs, ver
		return c, false, nil
	}
	if ver == cur {
		c.base = m.Rootfs
		return c, false, nil
	}
	// pin the upper to the base it was built on: not the current one (cur),
	// so a preserved sibling — never the rootfs on a version re-read as v0
	base, ok := resolveBase(m.Rootfs, ver)
	if !ok || base == m.Rootfs {
		// The base this layer was built on isn't installed — refuse rather
		// than corrupt its apt/dpkg state on a different base (the boot
		// logs every such layer: CheckBaseImages). Reset the terminal to upgrade.
		return layerClaim{}, false, fmt.Errorf("this terminal's base image %q is not installed — reset the terminal to rebuild on the current base", ver)
	}
	c.base = base
	return c, false, nil
}

// What a session says when its start moved the tile's layer to the current
// base; a shell prints it first, grey (baseMovedLine), an agent session logs
// it as a notice its Agent tab shows. What goes is everything the layer
// holds — all outside the workspace's bind mounts and $HOME, a VM
// terminal's whole disk — so the line names that, not only apt installs.
// A shell on a tile whose layer an agent session's start moved says the
// agent's line (baseMovedByAgentLine), once (takeMoveNote).
const (
	baseMovedWhat        = "everything outside the workspace files and $HOME was reset (installed packages, /etc, /var, /opt…; a VM terminal's whole disk)"
	baseMovedNote        = "xbin: this tile's terminal moved to the new base image — " + baseMovedWhat
	baseMovedByAgentNote = "xbin: an agent session's start moved this tile's terminal to the new base image — " + baseMovedWhat
	baseMovedLine        = "\x1b[90m" + baseMovedNote + "\x1b[0m\r\n"
	baseMovedByAgentLine = "\x1b[90m" + baseMovedByAgentNote + "\x1b[0m\r\n"
)

// EnvStatus reports a component's persistent terminal layer before any
// terminal is open: whether one exists, and whether it was built on an older
// base image than the current rootfs (the terminal window's "base update").
func (m *Manager) EnvStatus(rel string) (exists, outdated bool) {
	return m.envStatusOf(termKey(rel))
}

// envStatusOf is EnvStatus for layer key.
func (m *Manager) envStatusOf(key string) (exists, outdated bool) {
	_, err := os.Stat(m.layerDir(key))
	return err == nil, m.layerOutdated(key)
}

// layerOutdated reports whether a held layer's base differs from the current
// rootfs (so the tile can offer an upgrade/reset). A stamp or a current
// version that can't be read is not outdated: nothing to offer on a guess.
func (m *Manager) layerOutdated(envKey string) bool {
	if envKey == "" || m.Rootfs == "" {
		return false
	}
	cur, err := m.currentBase()
	if err != nil {
		return false
	}
	ver, err := layers.ReadBase(m.layerDir(envKey))
	return err == nil && ver != "" && ver != cur
}

// CheckBaseImages is the boot's look at the terminal layers (isolation on):
// it sweeps what a restart left — D40's staged views, layers moved off their
// base that the remover hadn't finished (basemove.go) — and logs every
// layer pinned to a base image that isn't installed, returning them
// ("key→base"). It no longer refuses to boot over one (D175): each session
// start refuses such a layer by itself (claimLayer: "not installed — reset"),
// or moves it to the current base while base auto-update is on, so no
// layer is ever stacked on another base, and a stale layer — a host move, a
// restore, a base deleted by hand — or turning auto-update off can't keep
// the whole workspace down. It looks at the terminal layers only — a tile's
// (.xbin/term) and a person's on a partitioned tile (.xbin/term-part,
// PD-22), which a session's start claims and moves alike: a tile sandbox
// whose base is gone fails its own start (plans/tile-sandbox-runtime.md §7).
func (m *Manager) CheckBaseImages() (missing []string) {
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
	m.sweepMoved()
	cur, cerr := m.currentBase()
	if cerr != nil {
		slog.Warn("the base image's version can't be read: terminal sessions fail to start until it can", "err", cerr)
	}
	auto := m.baseAutoUpdate()
	ls, _ := layers.Check(m.Root, m.Rootfs)
	for _, l := range ls {
		if (l.Tree != layers.TreeTerm && l.Tree != layers.TreeTermPart) || !l.Missing {
			continue
		}
		key := l.Key
		if l.Tree == layers.TreeTermPart {
			key = partLayerDir + "/" + key // a person's layer: its layer key (partLayerKey)
		}
		missing = append(missing, key+"→"+l.Base)
		if cerr == nil && moveBase(auto, l.Base, cur) {
			slog.Warn("a terminal layer's base image isn't installed: its next session moves it to the current base (base auto-update)", "layer", key, "base", l.Base)
			continue
		}
		slog.Warn("a terminal layer's base image isn't installed: its sessions refuse to start until it is reset (or base auto-update is on), or the base restored as "+m.Rootfs+"-<version>",
			"layer", key, "base", l.Base)
	}
	return missing
}
