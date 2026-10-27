package broker

// covers D203 — the disk monitor's half of tile limits (plans/tile-limits.md
// §5, §6): a scope's quota is the default raised by its decisions, in the
// scan, Status and every namespace's ceiling; a scope's main namespace held
// to its quota offers raising it (crit at the quota, quota-near from 90 %),
// a deployment's, a person's partition's or a claimant-lowered one doesn't;
// a scope newly at its quota is one hit; a raise and a kick lift the 507.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/limits"
	"github.com/xbin-dev/xbin/internal/util"
)

// fakeDiskTiles stands in for the store and the registry: grants by data
// key (bytes), the scopes (or the broker's, when b is set), and the hits.
type fakeDiskTiles struct {
	b      *Broker
	mu     sync.Mutex
	grants map[string]int64
	scopes map[string]scopeRoot
	hits   []string // "<key> <value GiB> <detail>"
}

func (f *fakeDiskTiles) diskGrant(key string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.grants[key]
}

func (f *fakeDiskTiles) grant(key string, n int64) {
	f.mu.Lock()
	f.grants[key] = n
	f.mu.Unlock()
}

func (f *fakeDiskTiles) diskScopes() map[string]scopeRoot {
	if f.b != nil {
		return f.b.diskScopes()
	}
	return f.scopes
}

func (f *fakeDiskTiles) diskHit(key string, h limits.Hit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits = append(f.hits, fmt.Sprintf("%s %d %s", key, h.Value, h.Detail))
}

func (f *fakeDiskTiles) takeHits() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.hits
	f.hits = nil
	return out
}

func alertOf(as []Alert, kind, tile string) *Alert {
	return findAlert(as, func(a Alert) bool { return a.Kind == kind && a.Tile == tile })
}

// covers D203 — quotaOf and the scan: the crit quota alert and the
// quota-near warning carry a raise action naming the scope and its root
// tile (none for the workspace scope), their text names the ways to raise
// it; quota-near isn't a block; a scope newly at its quota is one hit, and
// again only after it left it; Status follows a raise at once, the block
// at the next scan.
func TestDiskQuotaRaised(t *testing.T) {
	usage := map[string]int64{"apps~a": 9 * gib, "apps~b": 8*gib*9/10 + 1, "apps~c": gib, "workspace": 9 * gib}
	var mu sync.Mutex
	d := newDiskMon(t.TempDir(), 8*gib, func() map[string]int64 { mu.Lock(); defer mu.Unlock(); return usage })
	d.free = func(string) (int64, int64) { return 50 * gib, 100 * gib }
	f := &fakeDiskTiles{grants: map[string]int64{}, scopes: map[string]scopeRoot{
		"apps~a": {scope: "apps/a", tile: "apps/a"}, "apps~b": {scope: "apps/b"}, "apps~c": {scope: "apps/c", tile: "apps/c"},
	}}
	d.tiles = f
	if d.quotaOf("apps~a") != 8*gib {
		t.Fatalf("quotaOf without a grant: %d", d.quotaOf("apps~a"))
	}
	d.scan()

	a := alertOf(d.Alerts(), "quota", "apps~a")
	want := limits.Action{Action: "raise-limit", Tile: "apps/a", Scope: "apps/a", Kind: limits.DiskGiB, Unit: "GiB", Current: 8}
	switch {
	case a == nil:
		t.Fatalf("no quota alert for apps~a: %+v", d.Alerts())
	case a.Level != "crit" || a.Action == nil || fmt.Sprint(*a.Action) != fmt.Sprint(want):
		t.Errorf("apps~a's alert: %+v %+v, want crit with %+v", *a, a.Action, want)
	case !strings.HasSuffix(a.Message, "resource writes are blocked — raise it: the tile's ⚙ → Limits, /xbin/limits, or `bx limits raise apps/a disk`"):
		t.Errorf("apps~a's message: %q", a.Message)
	}
	near := alertOf(d.Alerts(), "quota-near", "apps~b")
	switch {
	case near == nil:
		t.Fatalf("no quota-near alert at 90%%: %+v", d.Alerts())
	case near.Level != "warn" || near.Action == nil || near.Action.Tile != "" || near.Action.Scope != "apps/b":
		t.Errorf("apps~b's warning: %+v %+v", *near, near.Action)
	case !strings.Contains(near.Message, "(90%)") || !strings.HasSuffix(near.Message, "an admin can raise it at /xbin/limits"):
		t.Errorf("apps~b's message: %q", near.Message)
	}
	if _, blocked := d.Blocked("apps~b"); blocked {
		t.Error("a scope near its quota is blocked")
	}
	if a := alertOf(d.Alerts(), "quota", "workspace"); a == nil || a.Action != nil {
		t.Errorf("the workspace scope's alert: %+v (no action: no tile roots it, no API path names it)", a)
	}
	if alertOf(d.Alerts(), "quota-near", "apps~c") != nil || alertOf(d.Alerts(), "quota", "apps~c") != nil {
		t.Error("a scope at an eighth of its quota is alerted")
	}
	if a := findAlert(d.Alerts(), func(a Alert) bool { return a.Kind == "blocking" }); a == nil || !strings.HasPrefix(a.Message, "2 tile(s)") {
		t.Errorf("the System count: %+v, want 2 (quota-near isn't a block)", a)
	}
	if got := f.takeHits(); len(got) != 1 || got[0] != "apps~a 8 the data quota is full (9.0GB of 8.0GB): resource writes are refused" {
		t.Errorf("hits: %q, want apps~a's alone", got)
	}
	d.scan()
	if got := f.takeHits(); len(got) != 0 {
		t.Errorf("a scope still at its quota is a hit again: %q", got)
	}

	// a raise: Status at once, the block at the next scan
	f.grant("apps~a", 10*gib)
	if u, q, blocked := d.Status("apps~a"); u != 9*gib || q != 10*gib || !blocked {
		t.Errorf("Status after the raise: %d %d %v, want the 10 GiB quota, still blocked until a scan", u, q, blocked)
	}
	d.scan()
	if _, blocked := d.Blocked("apps~a"); blocked {
		t.Error("a raised scope is still blocked after a scan")
	}
	if a := alertOf(d.Alerts(), "quota-near", "apps~a"); a == nil || a.Action.Current != 10 {
		t.Errorf("apps~a at 9 of 10 GiB: %+v", a)
	}
	f.grant("apps~a", 0) // reverted: full again, a new hit
	d.scan()
	if got := f.takeHits(); len(got) != 1 || !strings.HasPrefix(got[0], "apps~a 8 ") {
		t.Errorf("full again after a revert: %q", got)
	}
	f.grant("apps~a", 4*gib) // a grant below the default changes nothing
	if d.quotaOf("apps~a") != 8*gib {
		t.Errorf("a grant below the default: %d", d.quotaOf("apps~a"))
	}
}

// covers D203 — a raise and a kick lift the 507 now, not at the next 45 s
// tick: the scan loop rescans on a kick.
func TestDiskRaiseKickLiftsBlock(t *testing.T) {
	d := newDiskMon(t.TempDir(), 8*gib, func() map[string]int64 { return map[string]int64{"apps~a": 9 * gib} })
	d.free = func(string) (int64, int64) { return 50 * gib, 100 * gib }
	f := &fakeDiskTiles{grants: map[string]int64{}, scopes: map[string]scopeRoot{"apps~a": {scope: "apps/a", tile: "apps/a"}}}
	d.tiles = f
	go d.run()
	defer d.close()
	until := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: not within 10 s", what)
			}
		}
	}
	until("the first scan's block", func() bool { _, b := d.Blocked("apps~a"); return b })
	f.grant("apps~a", 12*gib)
	d.kick()
	until("the kicked scan's lift", func() bool { _, b := d.Blocked("apps~a"); return !b })
}

// covers D203 — on the deployments fixture: only a scope's main namespace,
// while it is its primary's and held to the scope's quota, offers a raise —
// not a deployment's namespace, not main under a claimant's lower limit,
// not a partition's or a non-primary main (raiseAction); a raised scope
// quota is every namespace's ceiling (DeploymentDiskStatus), the plane's
// diskGiB ceiling (DiskQuotaFor, DiskLimitCheck); a scope no tile roots
// names none.
func TestDiskActionsMainBucketsOnly(t *testing.T) {
	b, plane := newNSBroker(t)
	plane.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, calDevScope)})
	free := 50 * gib
	d := quietDisk(t, b, 8*gib, &free)
	f := &fakeDiskTiles{b: b, grants: map[string]int64{}}
	d.tiles = f
	lim := map[string]map[string]int64{}
	b.SetDeploymentQuota(quotaRecords(lim))
	mainKey, devKey := quotaKeyOf(t, fxCalendar, util.MainDeployment), quotaKeyOf(t, fxCalendar, "dev")
	sparse(t, filepath.Join(nsDataDir(t, b, fxCalendar, util.MainDeployment), "files", "c0"), 9*gib)
	sparse(t, filepath.Join(nsDataDir(t, b, fxCalendar, "dev"), "fs", "devonly", "c0"), 9*gib)
	sparse(t, filepath.Join(nsDataDir(t, b, "apps/suite", util.MainDeployment), "jobs", "c0"), 9*gib)
	d.scan()

	if a := alertOf(d.Alerts(), "quota", mainKey); a == nil || a.Action == nil || a.Action.Tile != fxCalendar || a.Action.Scope != fxCalendar {
		t.Errorf("main's alert: %+v, want a raise of %s's scope", a, fxCalendar)
	}
	if a := alertOf(d.Alerts(), "quota", devKey); a == nil || a.Action != nil {
		t.Errorf("dev's namespace's alert: %+v, want one without an action", a)
	}
	if a := alertOf(d.Alerts(), "quota", util.ScopeKey("apps/suite")); a == nil || a.Action == nil || a.Action.Tile != "" || a.Action.Scope != "apps/suite" {
		t.Errorf("a scope no tile roots: %+v", a)
	}
	if got := f.takeHits(); len(got) != 2 {
		t.Errorf("hits: %q, want main's and apps/suite's", got)
	}
	if err := b.DiskLimitCheck(fxCalendar, "dev", 15); err == nil {
		t.Error("diskGiB 15 under the 8 GiB ceiling passed")
	}

	// raised: every namespace's ceiling, and the plane's
	f.grant(mainKey, 20*gib)
	d.scan()
	if _, q, blocked := b.DeploymentDiskStatus(fxCalendar, "dev"); q != 20*gib || blocked {
		t.Errorf("dev under the raised scope: quota %d, blocked %v", q, blocked)
	}
	if _, q, blocked := b.TileDiskStatus(fxCalendar); q != 20*gib || blocked {
		t.Errorf("main under the raise: quota %d, blocked %v", q, blocked)
	}
	if q := b.DiskQuotaFor(fxCalendar); q != 20*gib {
		t.Errorf("DiskQuotaFor: %d", q)
	}
	if q := b.DiskQuotaFor(fxShop); q != 8*gib {
		t.Errorf("another scope's ceiling moved: %d", q)
	}
	if err := b.DiskLimitCheck(fxCalendar, "dev", 15); err != nil {
		t.Errorf("diskGiB 15 under the raised 20 GiB: %v", err)
	}

	// main under a claimant's lower limit: the scope's raise wouldn't lift it
	lim[fxCalendar] = map[string]int64{util.MainDeployment: 1}
	d.scan()
	if a := alertOf(d.Alerts(), "quota", mainKey); a == nil || a.Action != nil {
		t.Errorf("main over its claimant's 1 GiB: %+v, want no action", a)
	}

	for name, bk := range map[string]nsBucket{
		"a person's partition": {partition: true, alertDep: "main"},
		"a non-primary main":   {nonPrimary: true, alertDep: "main"},
	} {
		if a := raiseAction(mainKey, bk, 8*gib, b.diskScopes()); a != nil {
			t.Errorf("%s: %+v", name, a)
		}
	}
	if a := raiseAction(mainKey, nsBucket{}, 8*gib, b.diskScopes()); a == nil {
		t.Error("a primary main: no action")
	}
	if _, err := os.Stat(b.Reg.Root); err != nil {
		t.Fatal(err)
	}
}
