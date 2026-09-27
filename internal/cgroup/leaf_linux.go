//go:build linux

package cgroup

// leaf_linux.go — leaves with their own limits and a lifecycle: the tile
// sandboxes' (plans/tile-sandbox-runtime.md §6.2). Parent makes the cgroup
// they all live in, Prepare (or AddWith) sizes a leaf per call, Kill empties
// it, Populated says whether it is empty, OOMKills counts what the OOM killer
// took, and Sweep clears the leaves a previous xbind left.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// killWait bounds how long Kill waits for a leaf to empty.
var killWait = 5 * time.Second

// rmdir removes a leaf. On cgroupfs a leaf's control files go with it, and
// a populated leaf answers EBUSY; the tests swap in a stand-in for a temp dir.
var rmdir = os.Remove

// AddWith creates the leaf name with exactly the limits l (not the shared
// ones SetLimits installs) and moves pid into it, limits first so they bind
// the process's whole life. It returns the leaf's name as the registry
// records it: "" when cgroups are off (the sandbox runs without limits).
//
// A leaf of the same name that a previous run left is removed first — it
// must be empty: a start never joins a leaf a dying orphan still holds.
func (m *Manager) AddWith(name string, pid int, l Limits) (string, error) {
	if !m.Enabled() {
		return "", nil
	}
	leaf, err := m.makeLeaf(name, l)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(leaf, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0o644); err != nil {
		_ = rmdir(leaf)
		return "", fmt.Errorf("cgroup %s: join: %w", name, err)
	}
	return name, nil
}

// Prepare creates the leaf name with exactly the limits l, as AddWith does
// (the same stale-leaf rule), but joins nothing: it returns the leaf's
// directory, open O_DIRECTORY, for a start straight into it —
// SysProcAttr.UseCgroupFD and CgroupFD, clone3's CLONE_INTO_CGROUP — so
// nothing the child runs or forks is ever outside the leaf, and the leaf's
// name as the registry records it. The caller closes the dir once the child
// has started. nil, "" when cgroups are off (the sandbox runs without
// limits).
func (m *Manager) Prepare(name string, l Limits) (*os.File, string, error) {
	if !m.Enabled() {
		return nil, "", nil
	}
	leaf, err := m.makeLeaf(name, l)
	if err != nil {
		return nil, "", err
	}
	dir, err := os.OpenFile(leaf, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		_ = rmdir(leaf)
		return nil, "", fmt.Errorf("cgroup %s: %w", name, err)
	}
	return dir, name, nil
}

// makeLeaf creates the leaf name with the limits l, first removing one a
// previous run left — which must be empty.
func (m *Manager) makeLeaf(name string, l Limits) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	leaf := m.leaf(name)
	if err := rmdir(leaf); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("cgroup %s: the previous leaf is still in use: %w", name, err)
	}
	if err := os.Mkdir(leaf, 0o755); err != nil {
		return "", fmt.Errorf("cgroup %s: %w", name, err)
	}
	writeLimits(leaf, l)
	return leaf, nil
}

// Parent makes the cgroup comp-<name> in m, the one cgroup that holds every
// tile sandbox's leaf (comp-tilesbx-<ws8>, plans/tile-sandbox-runtime.md
// §6.2): its memory.max and pids.max are l's (0 = no cap; a parent has no
// other limit) and it enables cpu, memory and pids for its children. It
// returns a Manager for inside it: that Manager's AddWith, Prepare, Kill,
// Populated, OOMKills, Remove, Sweep and Usage name leaves in the parent
// and never reach beside it, and its SetLimits re-writes the parent's
// limits (a policy change). A parent a previous xbind left is kept, leaves
// and all — the caller sweeps them. A disabled m gives a disabled Manager.
func (m *Manager) Parent(name string, l Limits) (*Manager, error) {
	if !m.Enabled() {
		return &Manager{parent: true}, nil
	}
	if err := checkName(name); err != nil {
		return nil, err
	}
	dir := m.leaf(name)
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("cgroup %s: %w", name, err)
	}
	if err := writeParentLimits(dir, l); err != nil {
		return nil, fmt.Errorf("cgroup %s: %w", name, err)
	}
	sc := filepath.Join(dir, "cgroup.subtree_control")
	if os.WriteFile(sc, []byte("+cpu +memory +pids"), 0o644) != nil {
		// one controller the base doesn't pass on (cpu, say) fails the lot
		for _, c := range []string{"+cpu", "+memory", "+pids"} {
			_ = os.WriteFile(sc, []byte(c), 0o644)
		}
	}
	if !strings.Contains(readStr(sc), "memory") {
		return nil, fmt.Errorf("cgroup %s: can't enable the memory controller for its leaves (a leftover process in it?)", name)
	}
	return &Manager{base: dir, enabled: true, parent: true}, nil
}

// writeParentLimits writes a parent's caps: memory.max, which is the point
// of a parent, and pids.max where the base passes the controller on. A zero
// is "max", so a policy change can lift a cap.
func writeParentLimits(dir string, l Limits) error {
	val := func(n int64) []byte {
		if n <= 0 {
			return []byte("max")
		}
		return []byte(strconv.FormatInt(n, 10))
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.max"), val(l.MemMax), 0o644); err != nil {
		return fmt.Errorf("memory.max: %w", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "pids.max"), val(l.PidsMax), 0o644)
	return nil
}

// OOMKills is how many processes the OOM killer took in a leaf, its subtree
// included (memory.events oom_kill) — whichever cgroup's limit was hit, the
// leaf's or its parent's. Read it before Remove. 0 when cgroups are off or
// the leaf doesn't exist.
func (m *Manager) OOMKills(name string) int64 {
	if !m.Enabled() || checkName(name) != nil {
		return 0
	}
	return eventCount(filepath.Join(m.leaf(name), "memory.events"), "oom_kill")
}

// Kill kills every process in a leaf and waits, at most killWait, until the
// leaf is empty. It writes cgroup.kill where the kernel has it (5.14+), which
// also catches a fork in flight; elsewhere it SIGKILLs each listed pid, again
// on every round while the leaf is populated. nil when cgroups are off or the
// leaf doesn't exist.
func (m *Manager) Kill(name string) error {
	if !m.Enabled() {
		return nil
	}
	if err := checkName(name); err != nil {
		return err
	}
	leaf := m.leaf(name)
	if _, err := os.Stat(leaf); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	killFile := writeExisting(filepath.Join(leaf, "cgroup.kill"), "1") == nil
	deadline := time.Now().Add(killWait)
	for delay := 2 * time.Millisecond; ; delay = min(2*delay, 100*time.Millisecond) {
		if !killFile {
			killProcs(leaf)
		}
		if !populated(leaf) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cgroup %s: still populated %s after the kill", name, killWait)
		}
		time.Sleep(delay)
	}
}

// Populated reports whether a leaf holds a live process (cgroup.events
// "populated 1"; a zombie doesn't count). false when cgroups are off or the
// leaf doesn't exist.
func (m *Manager) Populated(name string) bool {
	if !m.Enabled() || checkName(name) != nil {
		return false
	}
	return populated(m.leaf(name))
}

// Sweep kills and removes every leaf whose name starts with prefix: at boot,
// the leaves a previous xbind left (Sweep("sbx-") on the tile sandboxes'
// Parent — they died with it, or are dying; a parent's Sweep sees only its
// own leaves, never a backend's beside it). It returns the names it
// removed; a leaf that won't empty or go away is in err, and the others are
// swept regardless. The prefix must be non-empty: Sweep never clears every
// leaf.
func (m *Manager) Sweep(prefix string) ([]string, error) {
	if !m.Enabled() {
		return nil, nil
	}
	if prefix == "" || strings.ContainsAny(prefix, "/\x00") {
		return nil, fmt.Errorf("cgroup: bad sweep prefix %q", prefix)
	}
	ents, err := os.ReadDir(m.base)
	if err != nil {
		return nil, err
	}
	var swept []string
	var errs []error
	for _, e := range ents {
		name, ok := strings.CutPrefix(e.Name(), "comp-")
		if !ok || !e.IsDir() || !strings.HasPrefix(name, prefix) {
			continue
		}
		if err := m.Kill(name); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := rmdir(m.leaf(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("cgroup %s: %w", name, err))
			continue
		}
		swept = append(swept, name)
	}
	return swept, errors.Join(errs...)
}

// checkName: a leaf name is one path segment — it names a directory under
// xbind's base, never anything beside or above it.
func checkName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return fmt.Errorf("cgroup: bad leaf name %q", name)
	}
	return nil
}

// populated reads a leaf's cgroup.events.
func populated(leaf string) bool {
	return eventCount(filepath.Join(leaf, "cgroup.events"), "populated") > 0
}

// killProcs SIGKILLs every pid a leaf lists — never xbind itself or init,
// whatever the file says.
func killProcs(leaf string) {
	b, err := os.ReadFile(filepath.Join(leaf, "cgroup.procs"))
	if err != nil {
		return
	}
	self := os.Getpid()
	for _, ln := range strings.Split(string(b), "\n") {
		if pid, err := strconv.Atoi(strings.TrimSpace(ln)); err == nil && pid > 1 && pid != self {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// writeExisting writes to a file that must already exist: a control file
// the kernel may not have (cgroup.kill before 5.14) is never created.
func writeExisting(path, val string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(val)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
