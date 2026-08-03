package runner

import (
	"os"
	"testing"
)

// The descendant walk drives the dev-mode (no cgroup delegation) sampler:
// a backend's whole process tree from one /proc scan.
func TestProcDescendants(t *testing.T) {
	children := map[int][]int{
		1:   {10, 20},
		10:  {11, 12},
		12:  {13},
		20:  {21},
		999: {1000}, // unrelated tree
	}
	got := procDescendants(children, 10)
	want := map[int]bool{10: true, 11: true, 12: true, 13: true}
	if len(got) != len(want) {
		t.Fatalf("descendants of 10 = %v, want the %d-node subtree", got, len(want))
	}
	for _, pid := range got {
		if !want[pid] {
			t.Fatalf("unexpected pid %d in %v", pid, got)
		}
	}
	// A leaf is just itself.
	if got := procDescendants(children, 21); len(got) != 1 || got[0] != 21 {
		t.Fatalf("descendants of leaf = %v", got)
	}
}

// /proc/<pid>/io parsing against our own process — after forcing some real
// read syscalls the counters must be nonzero and monotonic.
func TestAddProcIO(t *testing.T) {
	var a statsRaw
	addProcIO(&a, os.Getpid())
	if a.rch == 0 || a.sr == 0 {
		t.Skipf("io accounting unavailable here (rchar=%d syscr=%d)", a.rch, a.sr)
	}
	// Do some I/O, expect growth.
	for i := 0; i < 10; i++ {
		_, _ = os.ReadFile("/proc/self/status")
	}
	var b statsRaw
	addProcIO(&b, os.Getpid())
	if b.rch <= a.rch || b.sr <= a.sr {
		t.Fatalf("io counters did not grow: rchar %d→%d syscr %d→%d", a.rch, b.rch, a.sr, b.sr)
	}
}

// CPU + RSS readers must return sane values for our own process.
func TestProcCPUAndRSS(t *testing.T) {
	if procCPUJiffies(os.Getpid()) < 0 {
		t.Fatal("negative cpu jiffies")
	}
	rss := procRSSBytes(os.Getpid())
	if rss < 1<<20 { // a Go test binary is >1MB resident
		t.Fatalf("implausible RSS %d", rss)
	}
	// Nonexistent pid: zeros, no panic.
	if procCPUJiffies(1<<30) != 0 || procRSSBytes(1<<30) != 0 {
		t.Fatal("nonexistent pid should read as zero")
	}
}
