//go:build linux && integration

// The leaves on the real cgroupfs. Needs a delegated cgroup that holds
// nothing but the test binary (xbind's own situation), so build it first:
//
//	go test -c -tags=integration -o /tmp/cg.test ./internal/cgroup/
//	systemd-run --user --scope -p Delegate=yes /tmp/cg.test -test.v -test.run OnCgroupfs
//
// The test binary doubles as the helpers the tests start into a leaf
// (TestMain). Skips elsewhere.
package cgroup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// helperEnv names the helper a re-exec of the test binary runs.
const helperEnv = "XBIN_CGROUP_TEST_HELPER"

// TestMain doubles as the helpers: "fork" forks at once and prints where
// the fork and it ran (/proc/self/cgroup, each); "hog" takes memory until
// something kills it.
func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "fork":
		child, err := exec.Command("cat", "/proc/self/cgroup").Output()
		if err != nil {
			fmt.Fprintln(os.Stderr, "fork:", err)
			os.Exit(2)
		}
		self, _ := os.ReadFile("/proc/self/cgroup")
		fmt.Printf("%s%s", child, self)
		os.Exit(0)
	case "hog":
		var held [][]byte
		for range 64 { // 1 GiB, every page touched
			b := make([]byte, 16<<20)
			for i := 0; i < len(b); i += 4096 {
				b[i] = 1
			}
			held = append(held, b)
		}
		fmt.Println("held", len(held))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestLeavesOnCgroupfs(t *testing.T) {
	m := New()
	if !m.Enabled() {
		t.Skip("no delegated cgroup (run the test binary under systemd-run --user --scope -p Delegate=yes)")
	}
	prefix := fmt.Sprintf("sbx-test%d-", os.Getpid())
	t.Cleanup(func() { _, _ = m.Sweep(prefix) })

	// a small tree that forks only once it is in the leaf
	start := func() (*exec.Cmd, func()) {
		cmd := exec.Command("sh", "-c", "read x; sleep 60 & sleep 60 & wait")
		in, _ := cmd.StdinPipe()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd, func() { _, _ = in.Write([]byte("go\n")) }
	}
	cmd, fork := start()
	name := prefix + "a"
	leaf, err := m.AddWith(name, cmd.Process.Pid, Limits{MemMax: 256 << 20, PidsMax: 64, CPUWeight: 100, CPUMax: 50000})
	if err != nil || leaf != name {
		t.Fatalf("AddWith = %q, %v", leaf, err)
	}
	fork()
	dir := m.leaf(name)
	for f, want := range map[string]string{
		"memory.max": "268435456", "memory.high": "234881024", "pids.max": "64", "cpu.max": "50000 100000",
	} {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		if got := strings.TrimSpace(string(b)); got != want {
			t.Errorf("%s = %q, want %q", f, got, want)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		pids, _ := m.Procs(name)
		if len(pids) == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the tree never showed up in the leaf: %v", pids)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !m.Populated(name) {
		t.Fatal("Populated = false with three processes in the leaf")
	}
	if _, err := m.AddWith(name, os.Getpid(), Limits{}); err == nil {
		t.Fatal("AddWith joined a populated leaf")
	}

	// Kill empties it even while the shell is an unreaped zombie of ours
	if err := m.Kill(name); err != nil {
		t.Fatal(err)
	}
	if m.Populated(name) {
		t.Fatal("populated after Kill")
	}
	if err := cmd.Wait(); err == nil || !strings.Contains(err.Error(), "killed") {
		t.Errorf("the shell ended with %v", err)
	}
	m.Remove(name)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Remove left the leaf: %v", err)
	}

	// Sweep kills and removes a stale, populated leaf
	cmd2, fork2 := start()
	if _, err := m.AddWith(prefix+"b", cmd2.Process.Pid, Limits{PidsMax: 16}); err != nil {
		t.Fatal(err)
	}
	fork2()
	swept, err := m.Sweep(prefix)
	if err != nil || len(swept) != 1 || swept[0] != prefix+"b" {
		t.Fatalf("Sweep = %v, %v", swept, err)
	}
	if _, err := os.Stat(m.leaf(prefix + "b")); !os.IsNotExist(err) {
		t.Fatal("Sweep left the leaf")
	}
	if err := cmd2.Wait(); err == nil {
		t.Error("the swept leaf's shell survived")
	}
}

// The tile sandboxes' parent on the real cgroupfs: a child started with
// UseCgroupFD into a prepared leaf is in it from its first instruction —
// a fork it makes at once lands there too — and the parent's memory.max
// binds a leaf whose own cap is far above it.
func TestParentOnCgroupfs(t *testing.T) {
	m := New()
	if !m.Enabled() {
		t.Skip("no delegated cgroup (run the test binary under systemd-run --user --scope -p Delegate=yes)")
	}
	name := fmt.Sprintf("tilesbx-test%d", os.Getpid())
	p, err := m.Parent(name, Limits{MemMax: 96 << 20, PidsMax: 128})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = p.Sweep("sbx-")
		m.Remove(name)
	})
	dir := m.leaf(name)
	for f, want := range map[string]string{"memory.max": "100663296", "pids.max": "128"} {
		if got := strings.TrimSpace(readStr(filepath.Join(dir, f))); got != want {
			t.Errorf("the parent's %s = %q, want %q", f, got, want)
		}
	}
	if sc := readStr(filepath.Join(dir, "cgroup.subtree_control")); !strings.Contains(sc, "memory") || !strings.Contains(sc, "pids") {
		t.Fatalf("the parent's subtree_control = %q", sc)
	}
	// no swap to hide in: the parent's cap is the memory its leaves have
	if err := writeExisting(filepath.Join(dir, "memory.swap.max"), "0"); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	start := func(helper, leaf string, l Limits) *exec.Cmd {
		t.Helper()
		f, got, err := p.Prepare(leaf, l)
		if err != nil || got != leaf || f == nil {
			t.Fatalf("Prepare = %v, %q, %v", f, got, err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), helperEnv+"="+helper)
		cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(f.Fd())}
		var out strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &out
		err = cmd.Start()
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		return cmd
	}

	// clone3 into the leaf: the helper and its first fork are both in it
	fork := start("fork", "sbx-fork", Limits{MemMax: 1 << 30, PidsMax: 64})
	if err := fork.Wait(); err != nil {
		t.Fatalf("the fork helper: %v: %s", err, fork.Stdout)
	}
	want := "0::" + strings.TrimPrefix(p.leaf("sbx-fork"), mount)
	lines := strings.Fields(fmt.Sprint(fork.Stdout))
	if len(lines) != 2 || lines[0] != want || lines[1] != want {
		t.Fatalf("the fork and the helper ran in %q, want both in %q", lines, want)
	}
	if !strings.HasPrefix(want, "0::"+strings.TrimPrefix(dir, mount)+"/") {
		t.Fatalf("the leaf %q isn't under the parent", want)
	}

	// the parent's 96 MiB binds a leaf capped at 1 GiB: the hog is killed,
	// counted in its leaf (oom_kill) with the limit hit in the parent (oom)
	hog := start("hog", "sbx-hog", Limits{MemMax: 1 << 30, MemHigh: -1})
	err = hog.Wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || !ee.Sys().(syscall.WaitStatus).Signaled() || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("the hog under a 96 MiB parent ended with %v: %s", err, hog.Stdout)
	}
	if n := p.OOMKills("sbx-hog"); n < 1 {
		t.Errorf("the hog's leaf OOMKills = %d", n)
	}
	if n := m.OOMKills(name); n < 1 {
		t.Errorf("the parent's OOMKills = %d", n)
	}
	leafEvents, parentEvents := filepath.Join(p.leaf("sbx-hog"), "memory.events"), filepath.Join(dir, "memory.events")
	if own, parent := eventCount(leafEvents, "oom"), eventCount(parentEvents, "oom"); own != 0 || parent < 1 {
		t.Errorf("oom events: the leaf's own %d (want 0), the parent's %d (want ≥ 1)", own, parent)
	}
	p.Remove("sbx-hog")
	p.Remove("sbx-fork")
}
