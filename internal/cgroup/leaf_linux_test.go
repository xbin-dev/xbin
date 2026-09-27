//go:build linux

package cgroup

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeBase is a Manager over a temp dir standing in for xbind's delegated
// cgroup. rmdir behaves as on cgroupfs: a populated leaf is EBUSY, and the
// control files go with the leaf.
func fakeBase(t *testing.T) (*Manager, string) {
	t.Helper()
	base := t.TempDir()
	old := rmdir
	rmdir = func(p string) error {
		if _, err := os.Stat(p); err != nil {
			return err
		}
		if populated(p) {
			return syscall.EBUSY
		}
		return os.RemoveAll(p)
	}
	t.Cleanup(func() { rmdir = old })
	return &Manager{base: base, enabled: true}, base
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAddWithWritesTheLimits(t *testing.T) {
	m, base := fakeBase(t)
	// the shared caps SetLimits installs are not a tile sandbox's
	m.SetLimits(Limits{MemMax: 2 << 30, PidsMax: 512, CPUWeight: 100})

	// namespace mode: memMiB 1024 + 128, ⅞ high, the sandbox's pids, 2 vCPUs
	leaf, err := m.AddWith("sbx-apps~x-1234-build", 4242, Limits{MemMax: 1152 << 20, PidsMax: 4096, CPUWeight: 100, CPUMax: 2 * cpuPeriod})
	if err != nil || leaf != "sbx-apps~x-1234-build" {
		t.Fatalf("AddWith = %q, %v", leaf, err)
	}
	dir := filepath.Join(base, "comp-sbx-apps~x-1234-build")
	for f, want := range map[string]string{
		"memory.max": "1207959552", "memory.high": "1056964608", "pids.max": "4096",
		"cpu.weight": "100", "cpu.max": "200000 100000", "cgroup.procs": "4242",
	} {
		if got := read(t, filepath.Join(dir, f)); got != want {
			t.Errorf("%s = %q, want %q", f, got, want)
		}
	}

	// VM mode: no memory.high (the guest holds its memory by design)
	if _, err := m.AddWith("sbx-vm", 4343, Limits{MemMax: 704 << 20, MemHigh: -1, PidsMax: 512, CPUMax: 3 * cpuPeriod}); err != nil {
		t.Fatal(err)
	}
	vmDir := filepath.Join(base, "comp-sbx-vm")
	if _, err := os.Stat(filepath.Join(vmDir, "memory.high")); !os.IsNotExist(err) {
		t.Error("a VM leaf got a memory.high")
	}
	if got := read(t, filepath.Join(vmDir, "cpu.max")); got != "300000 100000" {
		t.Errorf("VM cpu.max = %q", got)
	}

	// cgroups off: no leaf, no error — the sandbox runs without limits
	if leaf, err := (&Manager{}).AddWith("sbx-x", 1, Limits{MemMax: 1 << 30}); leaf != "" || err != nil {
		t.Errorf("disabled AddWith = %q, %v", leaf, err)
	}
	// a name is one path segment
	for _, bad := range []string{"", ".", "..", "../init", "a/b"} {
		if _, err := m.AddWith(bad, 1, Limits{}); err == nil {
			t.Errorf("AddWith(%q) accepted", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "init", "cgroup.procs")); !os.IsNotExist(err) {
		t.Error("a bad name reached outside the comp- leaves")
	}
}

// A leaf a previous run left is replaced when empty — its old limits gone —
// and refused while a process still holds it.
func TestAddWithStaleLeaf(t *testing.T) {
	m, base := fakeBase(t)
	if _, err := m.AddWith("sbx-a", 1, Limits{MemMax: 1 << 30, CPUMax: cpuPeriod}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "comp-sbx-a")
	if _, err := m.AddWith("sbx-a", 2, Limits{PidsMax: 64}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cpu.max")); !os.IsNotExist(err) {
		t.Error("the previous run's cpu.max survived")
	}
	if got := read(t, filepath.Join(dir, "cgroup.procs")); got != "2" {
		t.Errorf("cgroup.procs = %q", got)
	}

	_ = os.WriteFile(filepath.Join(dir, "cgroup.events"), []byte("populated 1\nfrozen 0\n"), 0o644)
	if !m.Populated("sbx-a") {
		t.Fatal("Populated = false for a populated leaf")
	}
	if _, err := m.AddWith("sbx-a", 3, Limits{}); err == nil {
		t.Fatal("a start joined a leaf an orphan still holds")
	}
	if got := read(t, filepath.Join(dir, "cgroup.procs")); got != "2" {
		t.Errorf("the refused start touched the leaf: cgroup.procs = %q", got)
	}
	if m.Populated("sbx-none") || (&Manager{}).Populated("sbx-a") {
		t.Error("Populated = true for a missing leaf or with cgroups off")
	}
}

func TestKillWritesCgroupKill(t *testing.T) {
	m, base := fakeBase(t)
	dir := filepath.Join(base, "comp-sbx-k")
	_ = os.Mkdir(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "cgroup.kill"), nil, 0o644)
	if err := m.Kill("sbx-k"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "cgroup.kill")); got != "1" {
		t.Errorf("cgroup.kill = %q", got)
	}
	if err := m.Kill("sbx-missing"); err != nil {
		t.Errorf("Kill of a missing leaf: %v", err)
	}
}

// sleeper starts a process for a leaf to list, and reports its exit.
func sleeper(t *testing.T) (*exec.Cmd, chan error) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skip("no sleep:", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd, done
}

func killedBySignal(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL
}

// Without cgroup.kill (kernels before 5.14), Kill SIGKILLs every listed pid
// and waits for the leaf to report empty.
func TestKillFallback(t *testing.T) {
	m, base := fakeBase(t)
	cmd, done := sleeper(t)
	dir := filepath.Join(base, "comp-sbx-f")
	_ = os.Mkdir(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644)
	events := filepath.Join(dir, "cgroup.events")
	_ = os.WriteFile(events, []byte("populated 1\n"), 0o644)
	exited := make(chan error, 1)
	go func() {
		err := <-done
		_ = os.WriteFile(events, []byte("populated 0\n"), 0o644) // the kernel's view once it exits
		exited <- err
	}()

	if err := m.Kill("sbx-f"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if !killedBySignal(err) {
			t.Fatalf("the process ended with %v, want SIGKILL", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Kill returned before the leaf emptied")
	}
	if _, err := os.Stat(filepath.Join(dir, "cgroup.kill")); !os.IsNotExist(err) {
		t.Error("the fallback created a cgroup.kill")
	}

	// a leaf that never empties: an error after killWait, not a hang
	old := killWait
	killWait = 50 * time.Millisecond
	defer func() { killWait = old }()
	_ = os.WriteFile(events, []byte("populated 1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())+"\n1\n"), 0o644) // never signalled
	if err := m.Kill("sbx-f"); err == nil || !strings.Contains(err.Error(), "still populated") {
		t.Fatalf("Kill of a leaf that stays populated: %v", err)
	}
}

// Sweep clears the stale leaves under a prefix — killing what's left in
// them — and nothing else.
func TestSweep(t *testing.T) {
	m, base := fakeBase(t)
	cmd, done := sleeper(t)
	for _, d := range []string{"init", "comp-sbx-a", "comp-sbx-b", "comp-term-x", "comp-apps~x-1234", "comp-sbxnot"} {
		_ = os.Mkdir(filepath.Join(base, d), 0o755)
	}
	_ = os.WriteFile(filepath.Join(base, "comp-sbx-stray-file"), nil, 0o644) // not a leaf
	a := filepath.Join(base, "comp-sbx-a")
	_ = os.WriteFile(filepath.Join(a, "cgroup.procs"), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(a, "cgroup.events"), []byte("populated 1\n"), 0o644)
	go func() {
		err := <-done
		_ = os.WriteFile(filepath.Join(a, "cgroup.events"), []byte("populated 0\n"), 0o644)
		done <- err
	}()

	swept, err := m.Sweep("sbx-")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(swept)
	if !slices.Equal(swept, []string{"sbx-a", "sbx-b"}) {
		t.Errorf("swept %v", swept)
	}
	if err := <-done; !killedBySignal(err) {
		t.Errorf("the stale leaf's process ended with %v, want SIGKILL", err)
	}
	var left []string
	ents, _ := os.ReadDir(base)
	for _, e := range ents {
		left = append(left, e.Name())
	}
	if want := []string{"comp-apps~x-1234", "comp-sbx-stray-file", "comp-sbxnot", "comp-term-x", "init"}; !slices.Equal(left, want) {
		t.Errorf("left %v, want %v", left, want)
	}

	for _, bad := range []string{"", "a/b"} {
		if _, err := m.Sweep(bad); err == nil {
			t.Errorf("Sweep(%q) accepted", bad)
		}
	}
	if s, err := (&Manager{}).Sweep("sbx-"); s != nil || err != nil {
		t.Errorf("disabled Sweep = %v, %v", s, err)
	}
}
