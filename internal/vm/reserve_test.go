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

func refusedWith(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !errors.Is(err, sbx.ErrRefused) || !strings.Contains(err.Error(), want) {
		t.Fatalf("want a refusal naming %q, got %v", want, err)
	}
}

// A tile sandbox's VM books against the tile sub-budget as well as the
// workspace's count and budget; other VMs never count against the
// sub-budget; a release gives back both, once.
func TestReserveTileSubBudget(t *testing.T) {
	m := &Manager{Root: t.TempDir()}
	if err := m.SetPolicy(Policy{Tiles: true, MemMiB: 1024, MaxVMs: 6, BudgetMiB: 4096, TilesBudgetMiB: 2048}); err != nil {
		t.Fatal(err)
	}
	t1, err := m.Reserve("apps/mgr", 1024, TileSandbox())
	if err != nil {
		t.Fatal(err)
	}
	t2, err := m.Reserve("apps/mgr", 1024, TileSandbox())
	if err != nil {
		t.Fatal(err)
	}
	// the sub-budget refuses, though the workspace's budget has room
	_, err = m.Reserve("apps/other-mgr", 512, TileSandbox())
	refusedWith(t, err, "VM memory budget for tile sandboxes (2048 MiB) is spent")
	if m.UsedTiles() != (Usage{2, 2048}) || m.Used() != (Usage{2, 2048}) {
		t.Fatalf("after a refusal: tiles %+v, all %+v", m.UsedTiles(), m.Used())
	}
	// a terminal or a backend isn't held to the sub-budget
	term, err := m.Reserve("apps/dev", 1024)
	if err != nil {
		t.Fatalf("a non-tile VM with the sub-budget spent: %v", err)
	}
	if m.UsedTiles() != (Usage{2, 2048}) || m.Used() != (Usage{3, 3072}) {
		t.Fatalf("a non-tile VM counted as a tile's: tiles %+v, all %+v", m.UsedTiles(), m.Used())
	}
	// every VM is booked to its tile, a tile sandbox's included
	if by := m.UsedBy(); by["apps/mgr"] != (Usage{2, 2048}) || by["apps/dev"] != (Usage{1, 1024}) {
		t.Fatalf("by owner: %+v", by)
	}
	// the workspace's budget still applies to a tile VM within its sub-budget
	t1()
	back, err := m.Reserve("apps/web", 2048)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Reserve("apps/mgr", 512, TileSandbox())
	refusedWith(t, err, "VM memory budget (4096 MiB) is spent")
	back()
	// release is idempotent, and gives back the sub-budget too
	t1()
	if m.UsedTiles() != (Usage{1, 1024}) || m.Used() != (Usage{2, 2048}) {
		t.Fatalf("after releases: tiles %+v, all %+v", m.UsedTiles(), m.Used())
	}
	t3, err := m.Reserve("apps/mgr", 1024, TileSandbox())
	if err != nil {
		t.Fatalf("the sub-budget wasn't given back: %v", err)
	}
	// ... and so does the workspace's count
	x, err := m.Reserve("apps/a", 256)
	if err != nil {
		t.Fatal(err)
	}
	y, err := m.Reserve("apps/b", 256)
	if err != nil {
		t.Fatal(err)
	}
	z, err := m.Reserve("apps/c", 256)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Reserve("apps/mgr", 1, TileSandbox())
	refusedWith(t, err, "VM limit (6 running)")
	for _, r := range []func(){t2, t3, term, x, y, z, t3} {
		r()
	}
	if m.UsedTiles() != (Usage{}) || m.Used() != (Usage{}) || len(m.UsedBy()) != 0 {
		t.Fatalf("not all released: tiles %+v, all %+v, by %+v", m.UsedTiles(), m.Used(), m.UsedBy())
	}
	var nilM *Manager
	if nilM.UsedTiles() != (Usage{}) {
		t.Fatal("nil manager")
	}
}

// tilesBudgetMiB: 0 is half the budget (the budget's own default included),
// it may not exceed the budget, and a hand-edited file that does is clamped.
func TestTilesBudgetDefaults(t *testing.T) {
	if p := (Policy{}).withDefaults(); p.TilesBudgetMiB != defaultMaxVMs*DefaultMemMiB/2 || p.Tiles || p.TilesEmulated {
		t.Fatalf("defaults: %+v", p)
	}
	if p := (Policy{BudgetMiB: 3000}).withDefaults(); p.TilesBudgetMiB != 1500 {
		t.Fatalf("half the budget: %+v", p)
	}
	if p := (Policy{BudgetMiB: 3000, TilesBudgetMiB: 3000}).withDefaults(); p.TilesBudgetMiB != 3000 {
		t.Fatalf("the whole budget: %+v", p)
	}
	if err := (Policy{BudgetMiB: 3000, TilesBudgetMiB: 3001}).Validate(); err == nil || !strings.Contains(err.Error(), "tilesBudgetMiB") {
		t.Fatalf("over the budget: %v", err)
	}
	if err := (Policy{TilesBudgetMiB: defaultMaxVMs*DefaultMemMiB + 1}).Validate(); err == nil {
		t.Fatal("over the default budget accepted")
	}
	if err := (Policy{TilesBudgetMiB: -1}).Validate(); err == nil {
		t.Fatal("negative accepted")
	}
	// a file written by hand, or before the budget was lowered by hand
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".xbin", "vm"), 0o755)
	os.WriteFile(filepath.Join(root, ".xbin", "vm", "policy.json"), []byte(`{"tiles": true, "budgetMiB": 1024, "tilesBudgetMiB": 4096}`), 0o644)
	m := &Manager{Root: root}
	if p := m.Policy(); p.TilesBudgetMiB != 1024 || !p.Tiles {
		t.Fatalf("clamped: %+v", p)
	}
	if _, err := m.Reserve("apps/mgr", 1025, TileSandbox()); err == nil {
		t.Fatal("admitted over the clamped sub-budget")
	}
}

// A policy file from before tile sandboxes has no "tiles": they follow
// "backends" (the installer's rule for a fresh policy: both on exactly where
// KVM is usable), the other switches and sizes kept; an admin's explicit
// "tiles": false stays off.
func TestPolicyFileWithoutTiles(t *testing.T) {
	for _, c := range []struct {
		file string
		want Policy
	}{
		{`{"terminals": true, "backends": true}`, Policy{Terminals: true, Backends: true, Tiles: true}},
		{`{"terminals": true, "backends": false, "memMiB": 4096}`, Policy{Terminals: true, MemMiB: 4096}},
		{`{"terminals": true, "backends": true, "tiles": false}`, Policy{Terminals: true, Backends: true}},
		{`{"tiles": true}`, Policy{Tiles: true}},
	} {
		root := t.TempDir()
		os.MkdirAll(filepath.Join(root, ".xbin", "vm"), 0o755)
		os.WriteFile(filepath.Join(root, ".xbin", "vm", "policy.json"), []byte(c.file), 0o644)
		m := &Manager{Root: root}
		if got := m.StoredPolicy(); got != c.want {
			t.Errorf("%s: stored %+v, want %+v", c.file, got, c.want)
		}
	}
	if got := (&Manager{Root: t.TempDir()}).StoredPolicy(); got != (Policy{}) {
		t.Errorf("no file: %+v, want everything off", got)
	}
}

// TileVMs: a tile sandbox runs in a VM only where VMs run, with tiles on,
// and under emulation only with tilesEmulated.
func TestTileVMs(t *testing.T) {
	var nilM *Manager
	if _, why := nilM.TileVMs(); !strings.Contains(why, "isolation") {
		t.Fatalf("nil manager: %q", why)
	}
	old := statusTTL
	t.Cleanup(func() { statusTTL = old })
	statusTTL = time.Hour
	fake := func(st Status) *Manager {
		m := &Manager{Root: t.TempDir()}
		m.pr = &probe{at: time.Now(), status: st}
		return m
	}
	m := fake(Status{Reason: "/dev/kvm is missing"})
	if err := m.SetPolicy(Policy{Tiles: true, TilesEmulated: true}); err != nil {
		t.Fatal(err)
	}
	if _, why := m.TileVMs(); !strings.Contains(why, "can't run here: /dev/kvm is missing") {
		t.Fatalf("unavailable: %q", why)
	}
	m = fake(Status{Available: true})
	if _, why := m.TileVMs(); why != "an admin hasn't enabled VM tile sandboxes (vm policy: tiles)" {
		t.Fatalf("tiles off: %q", why)
	}
	m.SetPolicy(Policy{Tiles: true})
	if st, why := m.TileVMs(); why != "" || st.Emulated {
		t.Fatalf("kvm: %+v %q", st, why)
	}
	m = fake(Status{Available: true, Emulated: true})
	m.SetPolicy(Policy{Tiles: true, Terminals: true, Backends: true})
	if _, why := m.TileVMs(); !strings.Contains(why, "tilesEmulated") {
		t.Fatalf("emulated, not allowed: %q", why)
	}
	m.SetPolicy(Policy{Tiles: true, TilesEmulated: true})
	if st, why := m.TileVMs(); why != "" || !st.Emulated {
		t.Fatalf("emulated, allowed: %+v %q", st, why)
	}
}
