package tilesbx

// workspace.go — what the rest of the workspace asks of the runtime
// (plans/tile-sandbox-runtime.md §9): the definitions a tile's backup
// carries and a restore merges back by uid, whether a tile's sandboxes
// hold state (offload refuses while they do, until archive/thaw carries
// them), and the leftovers of a removed tile. State itself never moves
// through a backup: a restore never touches .xbin/sbx.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/layers"
)

// Defs is tile's definitions as a backup carries them: each one as stored
// (its uid, base and bookkeeping included), by name. None: nil, and the
// backup is byte-identical to one of a tile without sandboxes.
func (m *Manager) Defs(tile string) []json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []json.RawMessage
	for _, d := range m.defs.list(Key{Tile: tile}) {
		if b, err := json.Marshal(d); err == nil {
			out = append(out, b)
		}
	}
	return out
}

// RestoreDefs merges a backup's definitions of tile into the store, by
// uid, each brought back stopped (the tile's sandboxes were stopped for
// the restore):
//
//   - a live definition with the same uid is replaced — it is the same
//     sandbox — and its state on disk is kept (the start checks its base);
//   - a backed-up definition whose name another uid holds now is skipped
//     and reported: the live sandbox and its state are never displaced, and
//     the backup never adopts another sandbox's state;
//   - one that pinned a base but has no state of its uid comes back error
//     ("restored without state"), never as a blank sandbox that would
//     silently start from nothing.
//
// Each is validated again first (its shape; the start re-checks its reach).
// skipped says what was left out and why.
func (m *Manager) RestoreDefs(tile string, raw []json.RawMessage) (skipped []string) {
	if len(raw) == 0 {
		return nil
	}
	k := Key{Tile: tile}
	var restored []*Def
	defer func() { // measured: their state on disk is the tile's again
		for _, d := range restored {
			m.measureSoon(k, d)
		}
	}()
	m.mu.Lock()
	defer m.mu.Unlock()
	lim := m.limitsFor(tile)
	for i, r := range raw {
		var d Def
		if err := json.Unmarshal(r, &d); err != nil {
			skipped = append(skipped, fmt.Sprintf("sandboxes[%d]: unreadable (%v)", i, err))
			continue
		}
		if err := m.checkRestored(&d, lim); err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", orUnnamed(d.Name), err))
			continue
		}
		if why := m.restoreOne(k, &d, lim); why != "" {
			skipped = append(skipped, fmt.Sprintf("%s (uid %s): %s", d.Name, d.UID, why))
			continue
		}
		restored = append(restored, &d)
	}
	return skipped
}

func orUnnamed(n string) string {
	if n == "" {
		return "a sandbox with no name"
	}
	return n
}

// restoreOne merges one validated definition; "" or why it was skipped.
// Callers hold m.mu.
func (m *Manager) restoreOne(k Key, d *Def, lim Limits) string {
	live, had := m.defs.get(k, d.Name)
	switch {
	case had && live.UID != d.UID:
		return fmt.Sprintf("the name is taken by another sandbox (uid %s): the live one is kept", live.UID)
	case had && m.live[k][d.Name] != nil && busyLocked(d.Name, m.live[k][d.Name]) != nil:
		return "a copy of its state runs (a clone, a snapshot or a restore): restore it again once it is done"
	case !had:
		for _, o := range m.defs.list(k) {
			if o.UID == d.UID {
				return fmt.Sprintf("its uid belongs to the live sandbox %q", o.Name)
			}
		}
		if n := m.defs.count(k); n >= lim.PerTile.Max {
			return fmt.Sprintf("the tile has %d sandboxes, its limit (sandboxes policy: perTile.max)", n)
		}
	}
	dir, err := m.StateDir(k, d)
	if err != nil {
		return err.Error()
	}
	_, err = os.Lstat(filepath.Join(dir, layers.CurDir))
	hasCur := err == nil
	if had {
		d.Version = max(d.Version, live.Version) + 1 // a manager's cached version is stale
		d.SnapSeq = max(d.SnapSeq, live.SnapSeq)     // snapshot ids are never handed out twice
		if hasCur {
			d.Base = live.Base // it mirrors the stamp of the state kept
		}
	}
	pending := d.Pending
	d.Pending = ""
	if err := m.defs.put(k, d); err != nil {
		return err.Error()
	}
	b := m.boxLocked(k, d.Name)
	if b.run != nil {
		return "" // still running: it keeps its launched definition until it stops
	}
	b.state, b.detail = StateStopped, "restored from a backup"
	switch {
	case pending != "":
		b.state, b.detail = StateError, "restored while its clone was still copying — delete it"
	case !hasCur && d.Base != "":
		b.state, b.detail = StateError, "restored without state — reset it to start from a fresh base"
	}
	return ""
}

// checkRestored validates a backed-up definition's shape — what a create
// would have refused — and resolves its sizes under the caps now. Its
// reach (mounts held, egress classes, users) is the start's to check.
func (m *Manager) checkRestored(d *Def, lim Limits) error {
	if err := validName(d.Name); err != nil {
		return err
	}
	if !validUID(d.UID) {
		return fmt.Errorf("no valid uid")
	}
	if d.Mode != ModeNamespace && d.Mode != ModeVM {
		return fmt.Errorf("mode %q isn't namespace or vm", d.Mode)
	}
	if d.MemMiB < 0 || d.VCPUs < 0 || d.DiskGiB < 0 || d.IdleStopMin < 0 {
		return fmt.Errorf("a negative size")
	}
	ps := lim.PerSandbox
	d.MemMiB = resolveSize(d.MemMiB, ps.MemMiB, minMemMiB, ps.MaxMemMiB)
	d.VCPUs = resolveSize(d.VCPUs, ps.VCPUs, 1, ps.MaxVCPUs)
	d.DiskGiB = resolveSize(d.DiskGiB, ps.DiskGiB, 1, ps.MaxDiskGiB)
	if d.Net.Egress == "" {
		d.Net.Egress = "none"
	}
	if slot, ok := strings.CutPrefix(d.Net.Egress, "class:"); d.Net.Egress != "none" && (!ok || slot == "") {
		return fmt.Errorf(`net.egress %q isn't "none" or "class:<slot>"`, d.Net.Egress)
	}
	if len(d.Mounts) > maxMounts {
		return fmt.Errorf("more than %d mounts", maxMounts)
	}
	seen := map[string]bool{}
	for i := range d.Mounts {
		mt := &d.Mounts[i]
		switch {
		case mt.Source == (mt.Res != ""):
			return fmt.Errorf("mounts[%d]: a mount is a res or the tile's source", i)
		case mt.Source:
			mt.Path, mt.RO = "", true
		default:
			if err := checkSubPath(mt.Path); err != nil {
				return fmt.Errorf("mounts[%d].path: %v", i, err)
			}
		}
		if err := checkAt(mt.At); err != nil || seen[mt.At] {
			return fmt.Errorf("mounts[%d].at: not a mount point it may have", i)
		}
		seen[mt.At] = true
	}
	if d.Defaults.Cwd != "" && checkAbs(d.Defaults.Cwd) != nil || d.Defaults.Shell != "" && checkAbs(d.Defaults.Shell) != nil {
		return fmt.Errorf("defaults.cwd and defaults.shell must be absolute, clean paths")
	}
	if err := checkEnv(d.Defaults.Env, "defaults.env"); err != nil {
		return err
	}
	if err := checkClientID(d.ClientID); err != nil {
		return err
	}
	return m.checkRest(Key{}, d, "labels", "for", "forUser", "idleStopMin")
}

// HasState counts tile's sandboxes that hold state — an upper or a disk
// (cur/), or a snapshot — and their bytes as last measured: offload
// refuses while any does (§9; archive/thaw will carry them). It reads the
// state root on disk, so a definitions file that can't be read hides
// nothing.
func (m *Manager) HasState(tile string) (n int, bytes int64) {
	k := Key{Tile: tile}
	root, err := m.StateRoot(k)
	if err != nil {
		return 0, 0
	}
	ents, _ := os.ReadDir(root)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range ents {
		name, uid, ok := layers.SplitStateDir(e.Name())
		if !ok || !e.IsDir() || !holdsState(filepath.Join(root, e.Name())) {
			continue
		}
		n++
		if d, ok := m.defs.get(k, name); ok && d.UID == uid {
			bytes += m.boxOf(k, name).diskBytes
		}
	}
	return n, bytes
}

// holdsState: a sandbox's state dir has a cur/ or a snapshot (xbind-made
// dirs: lstat'ed and listed, nothing inside them read).
func holdsState(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, layers.CurDir)); err == nil {
		return true
	}
	snaps, _ := os.ReadDir(filepath.Join(dir, "snapshots"))
	return len(snaps) > 0
}

// Leftovers counts the sandbox definitions of the tile at path and every
// tile under it, and their bytes as last measured: a tile created there
// would inherit them (the broker's pathLeftovers). They stay until an
// admin deletes them.
func (m *Manager) Leftovers(path string) (n int, bytes int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.defs.tiles() {
		if t != path && !strings.HasPrefix(t, path+"/") {
			continue
		}
		k := Key{Tile: t}
		for _, d := range m.defs.list(k) {
			n++
			bytes += m.boxOf(k, d.Name).diskBytes
		}
	}
	return n, bytes
}
