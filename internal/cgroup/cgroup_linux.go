//go:build linux

// Package cgroup attaches component backends to per-component cgroup v2 groups
// so xbind can report their memory/CPU/pids usage (plans/isolation.md). It is
// best-effort: it works when xbind's cgroup is delegated and writable (a
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

// Limits are the per-component resource caps written to each leaf. A runaway
// tile then OOMs / is throttled / can't fork *alone* instead of taking the box
// down (plans/isolation.md). Zero fields are left at the cgroup default.
type Limits struct {
	MemMax    int64 // memory.max, bytes (0 = unlimited)
	PidsMax   int64 // pids.max (0 = unlimited)
	CPUWeight int64 // cpu.weight 1..10000 (0 = default 100). Fair share under
	//                  contention, full burst when idle — no hard cpu.max.
}

// Manager owns xbind's delegated cgroup subtree and per-component leaves.
type Manager struct {
	base    string // the delegated base cgroup dir
	enabled bool
	limits  Limits
}

// SetLimits installs the per-component caps applied to every leaf in Add.
func (m *Manager) SetLimits(l Limits) {
	if m != nil {
		m.limits = l
	}
}

// New sets up (if possible) a delegated subtree: xbind moves itself into a
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

	// Move xbind into an "init" leaf (cgroup v2 forbids enabling controllers on
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

// Add creates a per-component leaf, applies the caps, and moves pid into it.
// Limits are written before the pid joins so they bind the process's whole
// lifetime. Safe no-op when disabled.
func (m *Manager) Add(name string, pid int) {
	if !m.Enabled() {
		return
	}
	leaf := m.leaf(name)
	if err := os.Mkdir(leaf, 0o755); err != nil && !os.IsExist(err) {
		return
	}
	writeLimits(leaf, m.limits)
	_ = os.WriteFile(filepath.Join(leaf, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0o644)
}

// writeLimits applies l to a leaf (best-effort per file — a missing controller
// just leaves that cap at the default).
func writeLimits(leaf string, l Limits) {
	set := func(file, val string) { _ = os.WriteFile(filepath.Join(leaf, file), []byte(val), 0o644) }
	if l.MemMax > 0 {
		set("memory.max", strconv.FormatInt(l.MemMax, 10))
		// A soft ceiling a little under the hard one: reclaim pressure kicks in
		// before the OOM kill, so a gradual leak is throttled first.
		set("memory.high", strconv.FormatInt(l.MemMax-l.MemMax/8, 10))
	}
	if l.PidsMax > 0 {
		set("pids.max", strconv.FormatInt(l.PidsMax, 10))
	}
	if l.CPUWeight > 0 {
		set("cpu.weight", strconv.FormatInt(l.CPUWeight, 10)) // fair share; no cpu.max = burst when idle
	}
}

// AtLimit reads a leaf's memory/pids limit-hit counters (memory.events "max"
// + "oom", pids.events "max") — nonzero means the component has bumped a cap,
// which the alert monitor surfaces. ok=false if disabled/absent.
func (m *Manager) AtLimit(name string) (mem, pids int64, ok bool) {
	if !m.Enabled() {
		return 0, 0, false
	}
	leaf := m.leaf(name)
	if _, err := os.Stat(leaf); err != nil {
		return 0, 0, false
	}
	mem = eventCount(filepath.Join(leaf, "memory.events"), "max") + eventCount(filepath.Join(leaf, "memory.events"), "oom")
	pids = eventCount(filepath.Join(leaf, "pids.events"), "max")
	return mem, pids, true
}

// eventCount reads "<key> <n>" from a cgroup .events file.
func eventCount(path, key string) int64 {
	for _, ln := range strings.Split(readStr(path), "\n") {
		f := strings.Fields(ln)
		if len(f) == 2 && f[0] == key {
			n, _ := strconv.ParseInt(f[1], 10, 64)
			return n
		}
	}
	return 0
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

// Procs lists the pids currently in a leaf (the backend's whole process
// tree — cgroup membership is inherited). ok=false if disabled/absent.
func (m *Manager) Procs(name string) (pids []int, ok bool) {
	if !m.Enabled() {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(m.leaf(name), "cgroup.procs"))
	if err != nil {
		return nil, false
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if n, err := strconv.Atoi(strings.TrimSpace(ln)); err == nil {
			pids = append(pids, n)
		}
	}
	return pids, true
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
