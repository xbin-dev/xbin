package cgroup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteLimits(t *testing.T) {
	leaf := t.TempDir()
	writeLimits(leaf, Limits{MemMax: 2 << 30, PidsMax: 512, CPUWeight: 100})
	read := func(f string) string {
		b, _ := os.ReadFile(filepath.Join(leaf, f))
		return string(b)
	}
	if got := read("memory.max"); got != "2147483648" {
		t.Errorf("memory.max = %q", got)
	}
	// memory.high is a soft ceiling 1/8 under the hard max.
	if got := read("memory.high"); got != "1879048192" {
		t.Errorf("memory.high = %q, want 7/8 of max", got)
	}
	if got := read("pids.max"); got != "512" {
		t.Errorf("pids.max = %q", got)
	}
	if got := read("cpu.weight"); got != "100" {
		t.Errorf("cpu.weight = %q", got)
	}
	// No hard cpu.max — bursting stays allowed, fairness comes from the weight.
	if _, err := os.Stat(filepath.Join(leaf, "cpu.max")); !os.IsNotExist(err) {
		t.Error("cpu.max should not be written (no hard cap)")
	}
	// Zero fields leave the cgroup default (nothing written).
	leaf2 := t.TempDir()
	writeLimits(leaf2, Limits{})
	if _, err := os.Stat(filepath.Join(leaf2, "memory.max")); !os.IsNotExist(err) {
		t.Error("zero limits must write nothing")
	}
}
