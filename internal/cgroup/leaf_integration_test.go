//go:build linux && integration

// The leaves on the real cgroupfs. Needs a delegated cgroup that holds
// nothing but the test binary (xbind's own situation), so build it first:
//
//	go test -c -tags=integration -o /tmp/cg.test ./internal/cgroup/
//	systemd-run --user --scope -p Delegate=yes /tmp/cg.test -test.v -test.run OnCgroupfs
//
// Skips elsewhere.
package cgroup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
