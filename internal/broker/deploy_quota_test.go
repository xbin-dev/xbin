package broker

// The disk monitor's tile-deployment rules (08-data §12; 07-runtime §2.5;
// 06-security T10 item 5; 14-implementation WP-44b): a quota bucket per data
// namespace beyond main, at the lowest limit its claimants set and never
// above the tile's ceiling; non-primary namespaces write-blocked first on
// low disk; the per-tile quota on checkpoint stores, view repositories,
// materialized trees and artifacts; reporting that keeps non-primary facts
// from readers. Built on newNSBroker (deploy_env_test.go).

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/util"
)

const gib = int64(1) << 30

// quietDisk replaces b's disk monitor with one whose scans only the test
// runs (no background loop), with quota as the tile's ceiling and the
// partition's free space read from *free (of 100 GiB).
func quietDisk(t *testing.T, b *Broker, quota int64, free *int64) *diskMon {
	t.Helper()
	b.disk.close()
	d := newDiskMon(b.Reg.Root, quota, b.scopeDiskUsage)
	d.free = func(string) (int64, int64) { return *free, 100 * gib }
	b.disk = d
	return d
}

// quotaRecords stands in for the plane's Lookup: each tile has a record
// whose deployments carry the diskGiB limits given (tile → deployment →
// GiB).
func quotaRecords(limits map[string]map[string]int64) func(string) deployments.Found {
	return func(tile string) deployments.Found {
		ds := map[string]*deployments.DeploymentRecord{util.MainDeployment: {}}
		for dep, v := range limits[tile] {
			ds[dep] = &deployments.DeploymentRecord{Limits: map[string]int64{deployments.LimitDiskGiB: v}}
		}
		return deployments.Found{State: deployments.RecordActive,
			Record: &deployments.Record{Tile: tile, Primary: util.MainDeployment, Deployments: ds}}
	}
}

// sparse makes a file of n bytes that occupies no blocks.
func sparse(t *testing.T, p string, n int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(n); err != nil {
		t.Fatal(err)
	}
}

// nsDataDir is the directory holding scope's namespace in dep: main's
// ciphertext tree, or the namespace root beyond it.
func nsDataDir(t *testing.T, b *Broker, scope, dep string) string {
	t.Helper()
	k, err := scopeKeys(scope, dep)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(b.Reg.Root, filepath.FromSlash(k.Enc))
}

func quotaKeyOf(t *testing.T, scope, dep string) string {
	t.Helper()
	k, err := scopeKeys(scope, dep)
	if err != nil {
		t.Fatal(err)
	}
	return k.Quota
}

// alertsFor is what GET /alerts answers p.
func alertsFor(t *testing.T, b *Broker, p auth.Principal) []Alert {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/xbin/alerts", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	b.apiAlerts(w, r)
	var out struct{ Alerts []Alert }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Alerts
}

func findAlert(as []Alert, match func(Alert) bool) *Alert {
	for i := range as {
		if match(as[i]) {
			return &as[i]
		}
	}
	return nil
}

// covers D127n D127q T10 SC-PRIMARY-FIRST — deployment data counts against the
// disk quota without ever blocking the primary: a namespace beyond main is a
// bucket of its own, measured as its root (its kv file included), blocked
// over the tile's ceiling or its manager's lower limit with 507 on its own
// writes only; low disk blocks non-primary namespaces first (main too, once
// it isn't P(S)), non-primary usage never moves a primary's fair share, and
// the alert names them first; no tile-visible figure (the System count,
// /tile-status, a reader's /alerts) reveals a non-primary namespace. The
// per-tile quota counts the checkpoint store, the view repository,
// materialized trees (the protected primary's included, never d/) and
// per-checkpoint artifacts with a walk that follows no link and blocks on no
// FIFO, alerts at 90%, and refuses new state when full until space is freed.
func TestDiskQuotaCountsDeploymentData(t *testing.T) {
	b, plane := newNSBroker(t)
	nsFakeVolumes(t, b) // unsealed, so the primary's kv write lands; no real gocryptfs mount outlives the test
	plane.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, calDevScope)})
	free := 50 * gib
	d := quietDisk(t, b, 8*gib, &free)
	limits := map[string]map[string]int64{}
	b.SetDeploymentQuota(quotaRecords(limits))
	mainKey, devKey := quotaKeyOf(t, fxCalendar, util.MainDeployment), quotaKeyOf(t, fxCalendar, "dev")
	mainDir, devDir := nsDataDir(t, b, fxCalendar, util.MainDeployment), nsDataDir(t, b, fxCalendar, "dev")
	admin, reader := deployPerson(t, b, fxAdmin), deployPerson(t, b, fxReader)
	reset := func() {
		t.Helper()
		for _, dir := range []string{mainDir, devDir, nsDataDir(t, b, fxShop, util.MainDeployment)} {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		}
		clear(limits)
	}

	t.Run("a bucket of its own", func(t *testing.T) {
		reset()
		sparse(t, filepath.Join(mainDir, "files", "c0"), gib)
		sparse(t, filepath.Join(devDir, "fs", "devonly", "c0"), 9*gib)
		sparse(t, filepath.Join(devDir, "kv.db"), 1<<20) // the namespace's kv file counts
		d.scan()
		if u, q, blocked := b.DeploymentDiskStatus(fxCalendar, "dev"); u != 9*gib+1<<20 || q != 8*gib || !blocked {
			t.Errorf("dev's namespace: usage %d, quota %d, blocked %v; want %d over the 8 GiB ceiling", u, q, blocked, 9*gib+1<<20)
		}
		if u, q, blocked := b.TileDiskStatus(fxCalendar); u != gib || q != 8*gib || blocked {
			t.Errorf("main's figures moved: usage %d, quota %d, blocked %v", u, q, blocked)
		}
		if st, body := nsKV(t, b, "PUT", deployTilePrincipal(fxCalendar, "instance", "dev"), "res:apps/calendar/devkv/k", "v"); st != http.StatusInsufficientStorage || !strings.Contains(body, "disk write blocked: over its 8.0GB quota") {
			t.Errorf("dev's write: %d %s, want 507", st, body)
		}
		if st, body := nsKV(t, b, "PUT", deployTilePrincipal(fxCalendar, "instance", ""), "res:apps/calendar/events/k", "v"); st != http.StatusOK {
			t.Errorf("the primary's write, beside a full non-primary namespace: %d %s", st, body)
		}
		a := findAlert(d.Alerts(), func(a Alert) bool { return a.Deployment == "dev" })
		switch {
		case a == nil:
			t.Fatalf("no alert names dev's namespace: %+v", d.Alerts())
		case a.System || a.Level != "crit" || a.Tile != devKey || !strings.Contains(a.Message, `apps/calendar's "dev" data is over its 8.0GB disk quota`):
			t.Errorf("dev's alert: %+v", *a)
		}
		if a := findAlert(d.Alerts(), func(a Alert) bool { return a.Kind == "blocking" }); a != nil {
			t.Errorf("a System count shows a non-primary block to every tile: %+v", *a)
		}
		if a := findAlert(alertsFor(t, b, reader), func(a Alert) bool { return a.Deployment != "" }); a != nil {
			t.Errorf("a reader of %s sees %+v", fxCalendar, *a)
		}
		if a := findAlert(alertsFor(t, b, admin), func(a Alert) bool { return a.Deployment == "dev" }); a == nil {
			t.Error("admins don't see dev's alert")
		}
		if a := findAlert(b.TileAlerts(fxCalendar), func(a Alert) bool { return a.Deployment != "" }); a != nil {
			t.Errorf("/tile-status shows %+v", *a)
		}
	})

	t.Run("a manager's lower limit", func(t *testing.T) {
		reset()
		sparse(t, filepath.Join(mainDir, "files", "c0"), 3*gib)
		sparse(t, filepath.Join(devDir, "fs", "devonly", "c0"), 3*gib/2)
		limits[fxCalendar] = map[string]int64{"dev": 1}
		d.scan()
		if u, q, blocked := b.DeploymentDiskStatus(fxCalendar, "dev"); q != gib || !blocked {
			t.Errorf("dev at %d under its 1 GiB limit: quota %d, blocked %v", u, q, blocked)
		}
		if r, _ := d.Blocked(devKey); r != "over its 1.0GB quota" {
			t.Errorf("dev's reason: %q", r)
		}
		if _, q, blocked := b.DeploymentDiskStatus(fxCalendar, util.MainDeployment); q != 8*gib || blocked {
			t.Errorf("dev's limit reached main: quota %d, blocked %v", q, blocked)
		}
		limits[fxCalendar] = map[string]int64{"dev": 100} // a hand edit above the ceiling
		d.scan()
		if _, q, blocked := b.DeploymentDiskStatus(fxCalendar, "dev"); q != 8*gib || blocked {
			t.Errorf("a limit above the ceiling: quota %d, blocked %v; want the 8 GiB ceiling", q, blocked)
		}
	})

	t.Run("low disk blocks non-primary namespaces first", func(t *testing.T) {
		reset()
		wide := quietDisk(t, b, 64*gib, &free) // room under the ceiling for a big non-primary namespace
		b.SetDeploymentQuota(quotaRecords(limits))
		t.Cleanup(func() { b.disk = d })
		sparse(t, filepath.Join(mainDir, "files", "c0"), gib)
		sparse(t, filepath.Join(nsDataDir(t, b, fxShop, util.MainDeployment), "orders", "c0"), 6*gib)
		sparse(t, filepath.Join(devDir, "fs", "devonly", "c0"), 20*gib)
		free = 5 * gib
		defer func() { free = 50 * gib }()
		wide.scan()
		if r, blocked := wide.Blocked(devKey); !blocked || !strings.Contains(r, "non-primary deployments' data is write-blocked first") {
			t.Errorf("dev on low disk: %q, blocked %v", r, blocked)
		}
		if _, blocked := wide.Blocked(mainKey); blocked {
			t.Error("the primary below the fair share is blocked")
		}
		// Counting dev's 20 GiB would lift the share to 9 GiB and spare
		// apps/shop's 6 GiB; the primaries' share alone is 5 GiB.
		if _, blocked := wide.Blocked(quotaKeyOf(t, fxShop, util.MainDeployment)); !blocked {
			t.Error("non-primary usage moved the primaries' fair share: apps/shop isn't blocked")
		}
		as := wide.Alerts()
		if len(as) < 3 || as[0].Kind != "disk-low" || as[1].Deployment != "dev" {
			t.Fatalf("the alerts, System first, then non-primary namespaces: %+v", as)
		}
		if strings.Contains(as[0].Message, "dev") {
			t.Errorf("the System alert names a non-primary deployment: %q", as[0].Message)
		}

		// A tiny namespace is blocked too; an empty one isn't.
		sparse(t, filepath.Join(devDir, "fs", "devonly", "c0"), 1024)
		wide.scan()
		if _, blocked := wide.Blocked(devKey); !blocked {
			t.Error("a 1 KiB non-primary namespace isn't blocked on low disk")
		}
		sparse(t, filepath.Join(devDir, "fs", "devonly", "c0"), 0)
		wide.scan()
		if _, blocked := wide.Blocked(devKey); blocked {
			t.Error("an empty non-primary namespace is blocked")
		}
	})

	t.Run("main is non-primary while dev is the primary", func(t *testing.T) {
		reset()
		plane.set(t, b, fxCalendar, "dev", map[string]string{util.MainDeployment: nsCheckpoint(t, calScope), "dev": ""})
		t.Cleanup(func() {
			plane.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, calDevScope)})
		})
		sparse(t, filepath.Join(mainDir, "files", "c0"), gib)
		sparse(t, filepath.Join(devDir, "fs", "files", "c0"), gib)
		free = 5 * gib
		defer func() { free = 50 * gib }()
		d.scan()
		if _, blocked := d.Blocked(mainKey); !blocked {
			t.Error("main, no longer the primary, isn't blocked first on low disk")
		}
		if _, blocked := d.Blocked(devKey); blocked {
			t.Error("dev, the primary, is blocked below the fair share")
		}
		a := findAlert(d.Alerts(), func(a Alert) bool { return a.Deployment == util.MainDeployment })
		if a == nil || a.Tile != "" || a.System {
			t.Fatalf("main's alert: %+v in %+v", a, d.Alerts())
		}
		if a := findAlert(b.TileAlerts(fxCalendar), func(a Alert) bool { return strings.Contains(a.Message, `"main" data`) }); a != nil {
			t.Errorf("/tile-status shows %+v", *a)
		}
		if a := findAlert(alertsFor(t, b, reader), func(a Alert) bool { return a.Deployment != "" }); a != nil {
			t.Errorf("a reader sees %+v", *a)
		}
	})

	t.Run("the System count counts primaries only", func(t *testing.T) {
		reset()
		sparse(t, filepath.Join(devDir, "fs", "devonly", "c0"), 9*gib)
		d.scan()
		if a := findAlert(d.Alerts(), func(a Alert) bool { return a.System }); a != nil {
			t.Errorf("a non-primary namespace over quota raised %+v", *a)
		}
		sparse(t, filepath.Join(mainDir, "files", "c0"), 9*gib)
		d.scan()
		a := findAlert(d.Alerts(), func(a Alert) bool { return a.Kind == "blocking" })
		if a == nil || !a.System || !strings.HasPrefix(a.Message, "1 tile(s) are over quota") {
			t.Errorf("the System count with main and dev over quota: %+v", d.Alerts())
		}
	})

	t.Run("the per-tile quota", func(t *testing.T) {
		reset()
		root, tk := b.Reg.Root, util.TileKey(fxCalendar)
		at := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
		tree := strings.Repeat("ab", 32)
		sparse(t, at("data/checkpoints/"+tk+".git/objects/pack/p.pack"), 4*gib)
		sparse(t, at("data/checkpoints/"+tk+".view.git/objects/pack/p.pack"), gib)
		sparse(t, at(".xbin/deploy/"+tk+"/"+tree+"/big"), 3*gib)
		sparse(t, at(".xbin/deploy/"+tk+"/.tmp-x1/part"), 100<<20)
		sparse(t, at(".xbin/deploy/"+tk+"/protected/build/"+tree+"/bin"), 200<<20)
		sparse(t, at(".xbin/deploy/"+tk+"/d/dev/backend.log"), 5*gib) // logs: not in the quota
		sparse(t, at(".xbin/build/"+util.CompKey(fxCalendar)+"/c/"+tree+"/bin"), 900<<20)
		// Hostile entries in a tree: a link to the 5 GiB log counts as a
		// link, a link to a directory is never entered, a FIFO never opened.
		link := at(".xbin/deploy/" + tk + "/" + tree + "/log")
		if err := os.Symlink(at(".xbin/deploy/"+tk+"/d/dev/backend.log"), link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(at(".xbin/deploy/"+tk+"/d"), at(".xbin/deploy/"+tk+"/"+tree+"/logs")); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(at(".xbin/deploy/"+tk+"/"+tree+"/pipe"), 0o600); err != nil {
			t.Fatal(err)
		}
		links := int64(len(at(".xbin/deploy/"+tk+"/d/dev/backend.log")) + len(at(".xbin/deploy/"+tk+"/d")))
		want := 8*gib + 1200<<20 + links
		scanned := make(chan struct{})
		go func() { d.scan(); close(scanned) }()
		select {
		case <-scanned:
		case <-time.After(30 * time.Second):
			t.Fatal("the scan blocked on the tree's FIFO")
		}
		d.mu.RLock()
		got, shop := d.stores[fxCalendar], d.stores[fxShop]
		d.mu.RUnlock()
		if got != want {
			t.Errorf("%s's deployment state: %d bytes, want %d", fxCalendar, got, want)
		}
		if shop != 0 {
			t.Errorf("a zero-state tile has deployment state: %d", shop)
		}
		a := findAlert(d.Alerts(), func(a Alert) bool { return a.Tile == fxCalendar })
		if a == nil || a.Level != "warn" || !strings.Contains(a.Message, "use 9.2GB of their 10.0GB quota") {
			t.Errorf("the 90%% alert: %+v", a)
		}
		if a := findAlert(alertsFor(t, b, reader), func(a Alert) bool { return a.Tile == fxCalendar }); a == nil {
			t.Error("the per-tile quota alert doesn't reach the tile's readers, as a scope's does")
		}
		if err := b.DeployStoreRoom(fxCalendar); err != nil {
			t.Errorf("room below the quota: %v", err)
		}

		sparse(t, at(".xbin/build/"+util.CompKey(fxCalendar)+"/c/"+strings.Repeat("cd", 20)+"/bin"), gib)
		d.scan()
		if a := findAlert(d.Alerts(), func(a Alert) bool { return a.Tile == fxCalendar }); a == nil || a.Level != "crit" {
			t.Errorf("the full quota's alert: %+v", a)
		}
		err := b.DeployStoreRoom(fxCalendar)
		var de *deployments.Error
		if !errors.As(err, &de) || de.Status != http.StatusInsufficientStorage || !strings.Contains(de.Msg, "of their 10.0GB quota") {
			t.Errorf("a capture into a full state: %v", err)
		}
		if err := os.RemoveAll(at("data/checkpoints/" + tk + ".view.git")); err != nil { // what a GC frees
			t.Fatal(err)
		}
		if err := b.DeployStoreRoom(fxCalendar); err != nil {
			t.Errorf("after space was freed: %v", err)
		}

		// A tree replaced at its path (removed, then materialized again) is
		// measured again; one left alone is not walked twice.
		if err := os.RemoveAll(at(".xbin/deploy/" + tk + "/" + tree)); err != nil {
			t.Fatal(err)
		}
		sparse(t, at(".xbin/deploy/"+tk+"/"+tree+"/big"), gib)
		d.scan()
		d.mu.RLock()
		got = d.stores[fxCalendar]
		d.mu.RUnlock()
		if want := 6*gib + 1200<<20; got != want { // the store, the new tree, the tmp and protected trees, both artifacts
			t.Errorf("after the tree was replaced: %d, want %d", got, want)
		}
	})

	t.Run("without the plane's records a scan is today's", func(t *testing.T) {
		reset()
		plain := quietDisk(t, b, 8*gib, &free)
		t.Cleanup(func() { b.disk = d })
		sparse(t, filepath.Join(devDir, "fs", "devonly", "c0"), 9*gib)
		plain.scan()
		if _, blocked := plain.Blocked(devKey); blocked || len(plain.Alerts()) != 0 {
			t.Errorf("dev blocked %v, alerts %+v, before SetDeploymentQuota", blocked, plain.Alerts())
		}
		if err := b.DeployStoreRoom(fxCalendar); err != nil {
			t.Error(err)
		}
	})
}

// covers D127n D127t — a shared (scope, name) namespace takes the lowest disk
// limit among its claimants' deployments of that name, never above the
// tile's ceiling; a claimant without one doesn't raise it, a non-claimant's
// doesn't count, and main's namespace keeps its own. The limit is set on the
// tile that roots the scope, as a positive number of GiB at most the ceiling
// (11-contract §1.7, §1.14).
func TestNamespaceLimitIsClaimantsMinimum(t *testing.T) {
	b, plane := newNSBroker(t)
	both := map[string]string{util.MainDeployment: "", "dev": ""}
	plane.set(t, b, fxShop, util.MainDeployment, both)
	plane.set(t, b, fxShopAdmin, util.MainDeployment, both)
	plane.set(t, b, fxCalendar, util.MainDeployment, both)
	free := 50 * gib
	d := quietDisk(t, b, 8*gib, &free)
	limits := map[string]map[string]int64{}
	b.SetDeploymentQuota(quotaRecords(limits))
	sparse(t, filepath.Join(nsDataDir(t, b, fxShop, "dev"), "kv.db"), 5*gib/2)
	quota := func(tile string) (int64, bool) {
		t.Helper()
		d.scan()
		_, q, blocked := b.DeploymentDiskStatus(tile, "dev")
		return q, blocked
	}

	limits[fxShop] = map[string]int64{"dev": 3}
	limits[fxShopAdmin] = map[string]int64{"dev": 2, util.MainDeployment: 1}
	limits[fxCalendar] = map[string]int64{"dev": 1} // another scope's
	for _, tile := range []string{fxShop, fxShopAdmin} {
		if q, blocked := quota(tile); q != 2*gib || !blocked {
			t.Errorf("seen from %s: quota %d, blocked %v; want the claimants' lowest, 2 GiB, and 2.5 GiB blocked", tile, q, blocked)
		}
	}
	if _, q, _ := b.DeploymentDiskStatus(fxShop, util.MainDeployment); q != gib {
		t.Errorf("main's namespace takes main's limits: %d", q)
	}

	delete(limits, fxShopAdmin)
	if q, blocked := quota(fxShop); q != 3*gib || blocked {
		t.Errorf("a claimant without a limit: quota %d, blocked %v; want 3 GiB", q, blocked)
	}
	if _, q, _ := b.DeploymentDiskStatus(fxShop, util.MainDeployment); q != 8*gib {
		t.Errorf("main's namespace without a limit: %d", q)
	}
	limits[fxShop] = map[string]int64{"dev": 100}
	if q, _ := quota(fxShop); q != 8*gib {
		t.Errorf("a limit above the ceiling: quota %d, want 8 GiB", q)
	}
	for _, c := range []struct {
		tile string
		gib  int64
		code int
		text string
	}{
		{fxShop, 3, 0, ""},
		{fxShop, 8, 0, ""},
		{fxShop, 9, http.StatusBadRequest, "diskGiB can't exceed the tile's ceiling (8)"},
		{fxShop, 0, http.StatusBadRequest, "positive"},
		{fxShopAdmin, 1, http.StatusConflict, `the quota of apps/shop's "dev" data is set on apps/shop`},
		{"apps/suite/app", 1, http.StatusConflict, `the quota of apps/suite's "dev" data is set on apps/suite — no tile roots it`},
		{fxChat, 1, http.StatusConflict, `the quota of the workspace's "dev" data is set on the workspace`},
		{"apps/nope", 1, http.StatusNotFound, "no such tile: apps/nope"},
	} {
		err := b.DiskLimitCheck(c.tile, "dev", c.gib)
		if c.code == 0 {
			if err != nil {
				t.Errorf("%s diskGiB %d: %v", c.tile, c.gib, err)
			}
			continue
		}
		wantErr(t, err, c.code, c.text)
	}
}

// covers D119c D127n — /runtime's resources per deployment (08-data §12 item 5):
// a scope with deployments lists main's namespace with what main's own code
// declares and each namespace beyond main with what its deployment's code
// declares (storage types only), each row naming its deployment, main's
// first; a scope whose root tile only has a record labels main's rows (the
// descriptive-field rule, 11-contract §0.2); a zero-state scope's rows are
// today's, with no deployment field. After a reassignment main's rows still
// follow main's code, not the primary's.
func TestResourceUsagePerDeployment(t *testing.T) {
	b, plane := newNSBroker(t)
	rows := func() map[string][]string { // scope → "id deployment", in order
		out := map[string][]string{}
		for _, ri := range b.ResourceUsage() {
			out[ri.Scope] = append(out[ri.Scope], ri.ID+" "+ri.Deployment)
		}
		return out
	}
	zero := rows()
	if len(zero[fxShop]) != 2 || len(zero[fxCalendar]) != 5 {
		t.Fatalf("the fixture's zero-state rows: %q", zero)
	}
	data, err := json.Marshal(b.ResourceUsage())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"deployment"`) {
		t.Fatalf("zero-state rows carry a deployment: %s", data)
	}

	plane.set(t, b, fxCalendar, util.MainDeployment, map[string]string{util.MainDeployment: "", "dev": nsCheckpoint(t, calDevScope)})
	got := rows()
	want := []string{
		"res:apps/calendar/bus main",
		"res:apps/calendar/db main", "res:apps/calendar/db dev",
		"res:apps/calendar/devkv dev", "res:apps/calendar/devonly dev", "res:apps/calendar/devpics dev",
		"res:apps/calendar/events main", "res:apps/calendar/events dev",
		"res:apps/calendar/files main", "res:apps/calendar/files dev",
		"res:apps/calendar/pics main", "res:apps/calendar/pics dev",
	}
	if strings.Join(got[fxCalendar], "|") != strings.Join(want, "|") {
		t.Errorf("apps/calendar's rows:\n got %q\nwant %q", got[fxCalendar], want)
	}
	if strings.Join(got[fxShop], "|") != strings.Join(zero[fxShop], "|") {
		t.Errorf("a zero-state scope's rows changed: %q, were %q", got[fxShop], zero[fxShop])
	}
	k, err := b.resKeys(resTarget{Scope: fxCalendar, Name: "db"}, "dev")
	if err != nil {
		t.Fatal(err)
	}
	sparse(t, filepath.Join(b.resenc.CipherDir(k.DirKey, k.Name), "c0"), 12345)
	for _, ri := range b.ResourceUsage() {
		if ri.ID == "res:apps/calendar/db" && ri.Deployment == "dev" && ri.Size != 12345 {
			t.Errorf("dev's sqlite measures %d, want its ciphertext's 12345", ri.Size)
		}
	}

	// apps/shop pauses live reload: a record holding main alone.
	b.DeploymentHooks.DeploymentSummary = func(tile string) (string, bool, bool, bool) {
		return util.MainDeployment, true, false, tile == fxShop
	}
	var shop []string
	for _, r := range zero[fxShop] {
		shop = append(shop, strings.TrimSuffix(r, " ")+" main")
	}
	if got := rows()[fxShop]; strings.Join(got, "|") != strings.Join(shop, "|") {
		t.Errorf("apps/shop with a record: %q, want %q", got, shop)
	}
	b.DeploymentHooks.DeploymentSummary = nil

	// Reassigned: dev, on the work tree, is the primary; main runs a
	// checkpoint declaring only events and db.
	nsWrite(t, b.Reg.Root, map[string]string{"apps/calendar/scope.json": calDevScope})
	plane.set(t, b, fxCalendar, "dev", map[string]string{
		util.MainDeployment: nsCheckpoint(t, `{"resources":{"events":{"type":"kv"},"db":{"type":"sqlite"}}}`), "dev": ""})
	var mains []string
	for _, r := range rows()[fxCalendar] {
		if strings.HasSuffix(r, " main") {
			mains = append(mains, r)
		}
	}
	if want := []string{"res:apps/calendar/db main", "res:apps/calendar/events main"}; strings.Join(mains, "|") != strings.Join(want, "|") {
		t.Errorf("main's rows after the reassignment: %q, want main's own code's %q", mains, want)
	}
}

// covers D127n — the tile's disk ceiling the plane's limits op reads is the
// scope quota: XBIN_LIMIT_DISK's, or the default without a disk monitor.
func TestDiskQuotaIsTheCeiling(t *testing.T) {
	if got := (&Broker{}).DiskQuota(); got != defaultQuotaBytes {
		t.Errorf("DiskQuota without a monitor = %d, want the default %d", got, int64(defaultQuotaBytes))
	}
	b := &Broker{disk: newDiskMon(t.TempDir(), 3<<30, nil)}
	if got := b.DiskQuota(); got != 3<<30 {
		t.Errorf("DiskQuota = %d, want the scope quota %d", got, int64(3<<30))
	}
}
