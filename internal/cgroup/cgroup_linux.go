//go:build linux

// Package cgroup attaches component backends to per-component cgroup v2 groups
// so buxond can report their memory/CPU/pids usage (plans/isolation.md). It is
// best-effort: it works when buxond's cgroup is delegated and writable (a
// systemd user service with Delegate=yes, or a container's own cgroup) and
// quietly disables itself otherwise.
package cgroup

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const mount = "/sys/fs/cgroup"

// Usage is a snapshot of one cgroup's resource accounting.
type Usage struct {
	MemCurrent  int64 `json:"memCurrent"`
	MemMax      int64 `json:"memMax"` // -1 = unlimited ("max")
	CPUUsec     int64 `json:"cpuUsec"`
	PidsCurrent int64 `json:"pidsCurrent"`
}

// Manager owns buxond's delegated cgroup subtree and per-component leaves.
type Manager struct {
	base    string // the delegated base cgroup dir
	enabled bool
}

// New sets up (if possible) a delegated subtree: buxond moves itself into a
// leaf so controllers can be enabled for sibling per-component groups. Returns
// a disabled Manager (safe no-op) when delegation isn't available.
func New() *Manager {
	m := &Manager{}
	rel, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return m
	}
	// "0::/path\n"
	line := strings.TrimSpace(string(rel))
	i := strings.Index(line, "::")
	if i < 0 {
		return m
	}
	m.base = filepath.Join(mount, line[i+2:])

	// Move buxond into an "init" leaf (cgroup v2 forbids enabling controllers on
	// a cgroup that has processes directly in it).
	initLeaf := filepath.Join(m.base, "init")
	if err := os.Mkdir(initLeaf, 0o755); err != nil && !os.IsExist(err) {
		return m // not writable / not delegated
	}
	if err := os.WriteFile(filepath.Join(initLeaf, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return m
	}
	// Enable the controllers we report on for children (ignore already-on).
	_ = os.WriteFile(filepath.Join(m.base, "cgroup.subtree_control"), []byte("+cpu +memory +pids"), 0o644)
	// Verify a controller is actually available to children.
	if sc, err := os.ReadFile(filepath.Join(m.base, "cgroup.subtree_control")); err == nil && strings.Contains(string(sc), "memory") {
		m.enabled = true
	}
	return m
}

// Enabled reports whether cgroup accounting is active.
func (m *Manager) Enabled() bool { return m != nil && m.enabled }

func (m *Manager) leaf(name string) string { return filepath.Join(m.base, "comp-"+name) }

// Add creates a per-component leaf and moves pid into it (no limits — pure
// accounting). Safe no-op when disabled.
func (m *Manager) Add(name string, pid int) {
	if !m.Enabled() {
		return
	}
	leaf := m.leaf(name)
	if err := os.Mkdir(leaf, 0o755); err != nil && !os.IsExist(err) {
		return
	}
	_ = os.WriteFile(filepath.Join(leaf, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0o644)
}

// Usage reads a component leaf's accounting; ok=false if disabled/absent.
func (m *Manager) Usage(name string) (Usage, bool) {
	if !m.Enabled() {
		return Usage{}, false
	}
	leaf := m.leaf(name)
	if _, err := os.Stat(leaf); err != nil {
		return Usage{}, false
	}
	u := Usage{MemMax: -1}
	u.MemCurrent = readInt(filepath.Join(leaf, "memory.current"))
	if s := strings.TrimSpace(readStr(filepath.Join(leaf, "memory.max"))); s != "" && s != "max" {
		u.MemMax, _ = strconv.ParseInt(s, 10, 64)
	}
	u.PidsCurrent = readInt(filepath.Join(leaf, "pids.current"))
	for _, ln := range strings.Split(readStr(filepath.Join(leaf, "cpu.stat")), "\n") {
		if v, ok := strings.CutPrefix(ln, "usage_usec "); ok {
			u.CPUUsec, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		}
	}
	return u, true
}

// Remove deletes a component leaf (best-effort; after the process has exited).
func (m *Manager) Remove(name string) {
	if m.Enabled() {
		_ = os.Remove(m.leaf(name))
	}
}

func readStr(p string) string { b, _ := os.ReadFile(p); return string(b) }

func readInt(p string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(readStr(p)), 10, 64)
	return n
}
