package vm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/sbx"
)

// Reservations are charged to their tile; the global limits and their
// messages are unchanged, and a refusal says it is one.
func TestReserveAttribution(t *testing.T) {
	m := &Manager{Root: t.TempDir()}
	if err := m.SetPolicy(Policy{Terminals: true, MemMiB: 1024, MaxVMs: 3, BudgetMiB: 2048}); err != nil {
		t.Fatal(err)
	}
	r1, err := m.Reserve("apps/a", 1024)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := m.Reserve("apps/b", 512)
	if err != nil {
		t.Fatal(err)
	}
	by := m.UsedBy()
	if by["apps/a"] != (Usage{1, 1024}) || by["apps/b"] != (Usage{1, 512}) || m.Used() != (Usage{2, 1536}) {
		t.Fatalf("usage: %+v %+v", by, m.Used())
	}
	_, err = m.Reserve("apps/a", 1024)
	if err == nil || !errors.Is(err, sbx.ErrRefused) || !strings.Contains(err.Error(), "VM memory budget (2048 MiB) is spent") {
		t.Fatalf("budget refusal: %v", err)
	}
	r3, err := m.Reserve("apps/a", 256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reserve("apps/c", 1); err == nil || !errors.Is(err, sbx.ErrRefused) || !strings.Contains(err.Error(), "VM limit (3 running)") {
		t.Fatalf("count refusal: %v", err)
	}
	r1()
	r1() // harmless
	r3()
	if by := m.UsedBy(); len(by) != 1 || by["apps/b"] != (Usage{1, 512}) {
		t.Fatalf("after release: %+v", by)
	}
	r2()
	if len(m.UsedBy()) != 0 || m.Used() != (Usage{}) {
		t.Fatal("not all released")
	}
	var nilM *Manager
	if len(nilM.UsedBy()) != 0 || nilM.StoredPolicy() != (Policy{}) {
		t.Fatal("nil manager")
	}
}

// The editor saves what the admin set: zero sizes stay "the default".
func TestStoredPolicy(t *testing.T) {
	m := &Manager{Root: t.TempDir()}
	if err := m.SetPolicy(Policy{Backends: true, VCPUs: 4}); err != nil {
		t.Fatal(err)
	}
	fresh := &Manager{Root: m.Root}
	if got := fresh.StoredPolicy(); got != (Policy{Backends: true, VCPUs: 4}) {
		t.Fatalf("stored: %+v", got)
	}
	if got := fresh.Policy(); got.MemMiB != DefaultMemMiB || got.BudgetMiB != defaultMaxVMs*DefaultMemMiB || got.VCPUs != 4 {
		t.Fatalf("effective: %+v", got)
	}
}

// The probe is reused for statusTTL, then taken again; Health says what is
// missing.
func TestProbeCacheAndHealth(t *testing.T) {
	for _, env := range []string{"XBIN_VM_KERNEL", "XBIN_VM_AGENT", "XBIN_MKFS_EROFS", "XBIN_FIRECRACKER", "XBIN_QEMU", "XBIN_VHOST_VSOCK"} {
		t.Setenv(env, "/nonexistent/"+env)
	}
	old := statusTTL
	t.Cleanup(func() { statusTTL = old })
	statusTTL = time.Hour
	m := &Manager{Root: t.TempDir()}
	st := m.Status()
	if st.Available || !strings.Contains(st.Reason, "missing") {
		t.Fatalf("status: %+v", st)
	}
	first := m.pr.at
	m.Status()
	m.OverheadMiB()
	if m.pr.at != first {
		t.Fatal("re-probed within the TTL")
	}
	statusTTL = 0
	m.Status()
	if m.pr.at == first {
		t.Fatal("not re-probed after the TTL")
	}
	h := m.Health()
	if h.Available || h.Accel != "" || len(h.Missing) != 4 || !strings.Contains(strings.Join(h.Missing, "|"), "the bx CLI") {
		t.Fatalf("health: %+v", h)
	}
	var nilM *Manager
	if h := nilM.Health(); h.Available || !strings.Contains(h.Reason, "isolation") {
		t.Fatalf("nil health: %+v", h)
	}
}

// Disks are listed by stat alone, from the terminal layers and the tile
// sandboxes' cur/ (named by the state dir's `<name>.<uid>`): a sparse
// image's allocation is small, a symlink in a disk's or a cur/'s place is
// skipped, and so is a disk outside a `<name>.<uid>` dir.
func TestListDisks(t *testing.T) {
	root := t.TempDir()
	m := &Manager{Root: root}
	if err := m.SetPolicy(Policy{DiskGiB: 1}); err != nil {
		t.Fatal(err)
	}
	p, err := m.EnsureDisk(filepath.Join(root, ".xbin", "term", "apps~x"))
	if err != nil {
		t.Fatal(err)
	}
	evil := filepath.Join(root, ".xbin", "term", "apps~y", "vm")
	os.MkdirAll(evil, 0o700)
	os.Symlink(p, filepath.Join(evil, "disk.img"))
	sbx := filepath.Join(root, ".xbin", "sbx", "apps~m-1")
	sp, err := EnsureDiskAt(filepath.Join(sbx, "web.0123456789ab", "cur"), 2<<30)
	if err != nil {
		t.Fatal(err)
	}
	evil = filepath.Join(sbx, "evil.0123456789ab", "cur", "vm")
	os.MkdirAll(evil, 0o700)
	os.Symlink(sp, filepath.Join(evil, "disk.img"))
	os.MkdirAll(filepath.Join(sbx, "link.0123456789ab"), 0o700)
	os.Symlink(filepath.Join(sbx, "web.0123456789ab", "cur"), filepath.Join(sbx, "link.0123456789ab", "cur"))
	for _, d := range []string{"web", ".trash"} { // no uid; not a sandbox's
		if _, err := EnsureDiskAt(filepath.Join(sbx, d, "cur"), 1<<20); err != nil {
			t.Fatal(err)
		}
	}
	d := ListDisks(root)
	if len(d) != 2 || d[0].Kind != DiskTerminal || d[0].Key != "apps~x" || d[0].Sandbox != "" || d[0].Path != p ||
		d[0].ApparentBytes != 1<<30 || d[0].AllocatedBytes >= 1<<20 {
		t.Fatalf("disks: %+v", d)
	}
	if d[1].Kind != DiskTile || d[1].Key != "apps~m-1" || d[1].Sandbox != "web" || d[1].SandboxUID != "0123456789ab" || d[1].Path != sp ||
		d[1].ApparentBytes != 2<<30 || d[1].AllocatedBytes >= 1<<20 {
		t.Fatalf("tile sandbox disk: %+v", d[1])
	}
	// a person's layer on a partitioned tile (PD-22): keyed by the tile's
	// TileKey alone, never the person; a symlinked layer isn't followed
	pp, err := EnsureDiskAt(filepath.Join(root, ".xbin", "term-part", "00ff", "u-ana"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	os.Symlink(filepath.Join(root, ".xbin", "term-part", "00ff", "u-ana"), filepath.Join(root, ".xbin", "term-part", "00ff", "u-link"))
	d = ListDisks(root)
	if len(d) != 3 || d[1].Kind != DiskPersonTerminal || d[1].Key != "00ff" || d[1].Path != pp || d[1].Sandbox != "" {
		t.Fatalf("a person's terminal disk: %+v", d)
	}
}
