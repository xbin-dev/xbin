package tilesbx

// cgroup.go — where tile sandboxes are counted (plans/tile-sandbox-runtime.md
// §6.2): one parent, comp-tilesbx-<ws8>/ under xbind's delegated cgroup,
// capped by the policy's total, and one leaf per running sandbox inside it,
// prepared with its limits before the sandbox's first process starts — and
// started straight into it (clone3 CLONE_INTO_CGROUP), so nothing it forks
// ever runs outside. Without delegation everything runs, without limits.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xbin-dev/xbin/internal/cgroup"
)

// leafOverheadMiB is what a namespace sandbox's leaf holds past the
// sandbox's own memory: its agent and fuse-overlayfs.
const leafOverheadMiB = 128

// ws8 is the first 8 hex of the sha256 of the workspace's absolute path:
// two xbinds sharing a delegated cgroup never touch each other's parent.
func ws8(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:4])
}

// parentName is the parent's name as cgroup.Parent takes it: on disk
// comp-tilesbx-<ws8>/.
func parentName(root string) string { return "tilesbx-" + ws8(root) }

// initCgroup makes the parent from xbind's delegated base (the boot's
// sweep then clears the leaves a previous xbind left in it, sweep.go). A
// parent that can't be made — a broken delegation, the memory controller
// unavailable — leaves the sandboxes running without limits; CgroupNote
// says why (the admin's health view).
func (m *Manager) initCgroup(base *cgroup.Manager) {
	if !base.Enabled() {
		return
	}
	p, err := base.Parent(parentName(m.root), m.totalLimits())
	if err != nil {
		m.cgNote = fmt.Sprintf("tile sandboxes run without cgroup limits: %v", err)
		slog.Warn("tile sandboxes: no cgroup parent; they run without limits", "err", err)
		return
	}
	m.cg = p
}

// CgroupNote is why tile sandboxes run without cgroup limits although
// xbind's cgroup is delegated ("" = they have them, or nothing does).
func (m *Manager) CgroupNote() string { return m.cgNote }

// Cgroup is the parent's Manager: where a tile sandbox's leaf (Entry.Leaf)
// is read — the base Manager can't see inside the parent (WP-8b). nil
// without one.
func (m *Manager) Cgroup() *cgroup.Manager { return m.cg }

// totalLimits is the parent's caps from the policy's total: memMiB 0 is ¾
// of the host's RAM.
func (m *Manager) totalLimits() cgroup.Limits {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cgroup.Limits{MemMax: int64(m.totalMemMiBLocked()) << 20, PidsMax: int64(m.policy.get().Effective().Total.Pids)}
}

// applyTotal re-writes the parent's caps after a policy change.
func (m *Manager) applyTotal() {
	if m.cg == nil {
		return
	}
	if err := m.cg.SetLimits(m.totalLimits()); err != nil {
		slog.Warn("tile sandboxes: the cgroup parent's limits", "err", err)
	}
}

// leafLimits is a namespace sandbox's leaf (§6.2): its memory plus the
// agent's and fuse-overlayfs's overhead as memory.max, and no swap past it
// (memMiB is a cap; the host's swap would otherwise extend it without
// bound); the policy's pids cap, its vCPUs as a hard cpu.max, the default
// weight. No memory.high: without swap, anonymous memory over it can't be
// reclaimed and the allocating task is throttled to a near stall instead of
// being OOM-killed — at memory.max the OOM killer takes a session first
// (oom_score_adj 500, §2.4), and the sandbox runs on.
func leafLimits(d *Def, lim Limits) cgroup.Limits {
	return cgroup.Limits{
		MemMax:    int64(d.MemMiB+leafOverheadMiB) << 20,
		MemHigh:   -1,
		NoSwap:    true,
		PidsMax:   int64(lim.PerSandbox.Pids),
		CPUWeight: 100,
		CPUMax:    int64(d.VCPUs) * 100000,
	}
}

// prepareLeaf makes a sandbox's leaf with its limits and opens it for a
// start straight into it: nil, "" without cgroups.
func (m *Manager) prepareLeaf(k Key, d *Def, l cgroup.Limits) (*os.File, string, error) {
	if m.cg == nil {
		return nil, "", nil
	}
	return m.cg.Prepare(Leaf(k, d.Name), l)
}

// hostMemBytes is the host's RAM (MemTotal), 0 when unknown (no cap).
func hostMemBytes() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			kib, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
			return kib << 10
		}
	}
	return 0
}
