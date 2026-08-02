package term

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Base-image versioning for terminal sandbox layers (plans/component-env.md).
//
// A terminal's persistent overlay upper (.xbin/term/<key>/upper) records apt
// installs and the dpkg/apt state copied up from the base rootfs it was built
// on. Stacking that upper on a DIFFERENT base merges new-base packages under an
// old dpkg status → apt breaks. So each layer is stamped with its base version
// and PINNED to it; "upgrading" a terminal to a newer base means discarding the
// upper (the existing reset action) — safe, because tile code and $HOME are
// bind mounts, not the overlay. The install upgrade preserves old bases as
// `<rootfs>-<version>` siblings so pinned layers keep resolving.

const baseVersionFile = "etc/xbin-base-version"

// baseVersion reads a rootfs's stamped base version, defaulting to "v0" for an
// unstamped (legacy, pre-versioning) base.
func baseVersion(rootfs string) string {
	if rootfs == "" {
		return "v0"
	}
	b, err := os.ReadFile(filepath.Join(rootfs, baseVersionFile))
	if err != nil {
		return "v0"
	}
	if v := strings.TrimSpace(string(b)); v != "" {
		return v
	}
	return "v0"
}

// resolveBase returns the rootfs dir serving base `version`: the current rootfs
// if it matches, else a preserved sibling `<rootfs>-<version>` (kept by the
// install upgrade). ok=false when that base isn't installed.
func resolveBase(rootfs, version string) (string, bool) {
	if version == baseVersion(rootfs) {
		return rootfs, true
	}
	sib := rootfs + "-" + version
	if fi, err := os.Stat(sib); err == nil && fi.IsDir() {
		return sib, true
	}
	return "", false
}

// ensureLayerBase stamps a terminal layer with its base version on first use and
// returns it: a brand-new layer gets the current base; a pre-existing unstamped
// layer is the legacy base ("v0"). Idempotent.
func (m *Manager) ensureLayerBase(layer string) string {
	stamp := filepath.Join(layer, "base")
	if b, err := os.ReadFile(stamp); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
	}
	ver := baseVersion(m.Rootfs) // brand-new layer → the current base
	if _, err := os.Stat(layer); err == nil {
		ver = "v0" // pre-existing unstamped upper → the legacy base
	}
	_ = os.MkdirAll(layer, 0o755)
	_ = os.WriteFile(stamp, []byte(ver+"\n"), 0o644)
	return ver
}

// layerOutdated reports whether a held layer's base differs from the current
// rootfs (so the tile can offer an upgrade/reset).
func (m *Manager) layerOutdated(envKey string) bool {
	if envKey == "" || m.Rootfs == "" {
		return false
	}
	b, err := os.ReadFile(filepath.Join(m.Root, ".xbin", "term", envKey, "base"))
	if err != nil {
		return false
	}
	v := strings.TrimSpace(string(b))
	return v != "" && v != baseVersion(m.Rootfs)
}

// pinnedBases returns the set of base versions still referenced by a terminal
// layer (unstamped layers count as "v0").
func (m *Manager) pinnedBases() map[string]bool {
	pinned := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(m.Root, ".xbin", "term"))
	if err != nil {
		return pinned
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ver := "v0"
		if b, err := os.ReadFile(filepath.Join(m.Root, ".xbin", "term", e.Name(), "base")); err == nil {
			if v := strings.TrimSpace(string(b)); v != "" {
				ver = v
			}
		}
		pinned[ver] = true
	}
	return pinned
}

// GCBaseImages removes preserved base images (`<rootfs>-<version>` siblings) that
// no terminal layer pins anymore — the "release" side of keeping old bases so
// they don't accumulate once every terminal has upgraded. Conservative: only
// touches dirs that carry a base-version stamp.
func (m *Manager) GCBaseImages() {
	if m.Rootfs == "" {
		return
	}
	pinned := m.pinnedBases()
	cur := baseVersion(m.Rootfs)
	parent, prefix := filepath.Dir(m.Rootfs), filepath.Base(m.Rootfs)+"-"
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		ver := strings.TrimPrefix(e.Name(), prefix)
		if ver == cur || pinned[ver] {
			continue
		}
		p := filepath.Join(parent, e.Name())
		if _, err := os.Stat(filepath.Join(p, baseVersionFile)); err != nil {
			continue // not a base image — leave it alone
		}
		slog.Info("releasing unreferenced base image", "path", p, "version", ver)
		_ = os.RemoveAll(p)
	}
}

// CheckBaseImages is the startup safety gate: it refuses to run if any existing
// terminal layer is pinned to a base image that isn't installed — stacking its
// upper on a different base would corrupt apt/dpkg state. Called once at boot
// when isolation is on.
func (m *Manager) CheckBaseImages() error {
	if m.Rootfs == "" {
		return nil
	}
	dir := filepath.Join(m.Root, ".xbin", "term")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // no terminal layers yet
	}
	var bad []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// view-* dirs are D40 per-session STAGED VIEWS, not env layers — a
		// daemon restart orphans any live sessions' views, and treating them
		// as layers pinned to a phantom base crash-looped a production boot
		// (2026-08-02). Sweep them here: anything present at boot is dead.
		if strings.HasPrefix(e.Name(), "view-") {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
			continue
		}
		ver := "v0"
		if b, err := os.ReadFile(filepath.Join(dir, e.Name(), "base")); err == nil {
			if v := strings.TrimSpace(string(b)); v != "" {
				ver = v
			}
		}
		if _, ok := resolveBase(m.Rootfs, ver); !ok {
			bad = append(bad, e.Name()+"→"+ver)
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
