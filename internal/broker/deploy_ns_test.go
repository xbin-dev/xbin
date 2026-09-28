package broker

// The data namespaces' life (08-data §6.3, §6.4, §8.2, §9; 14-implementation
// WP-41): reset, deletion by the last claimant, the sweep of orphans, holds
// and the write gate, joins and dry runs. Built on deploy_fixture_test.go's
// workspace, whose apps/shop scope has two member tiles.

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/util"
)

// nsFx is deployBroker with the deployments plane's answers faked: deps
// lists each tile's deployments beyond main, primary each tile's primary
// ("" is main); stops records the stop callbacks.
type nsFx struct {
	t       *testing.T
	b       *Broker
	root    string
	deps    map[string][]string
	primary map[string]string
	stops   []string
}

func newNSFx(t *testing.T) *nsFx {
	t.Helper()
	b := deployBroker(t)
	f := &nsFx{t: t, b: b, root: b.Reg.Root, deps: map[string][]string{}, primary: map[string]string{}}
	b.DeploymentAnswers.PrimaryOf = func(tile string) string { return cmp.Or(f.primary[tile], util.MainDeployment) }
	b.DeploymentAnswers.DeploymentsOf = func(tile string) (string, []string) {
		return b.primaryOf(tile), append([]string{util.MainDeployment}, f.deps[tile]...)
	}
	if err := b.barrier.Init("ns-pass"); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *nsFx) stop(tile, dep string) { f.stops = append(f.stops, tile+"+"+dep) }

func allow(string) error { return nil }

func (f *nsFx) keys(scope, dep, name string) resKeys {
	f.t.Helper()
	k, err := f.b.resKeys(resTarget{Scope: scope, Name: name}, dep)
	if err != nil {
		f.t.Fatal(err)
	}
	return k
}

// putKV stores key=val in resource name of scope's namespace in dep, as the
// kv API does.
func (f *nsFx) putKV(scope, dep, name, key, val string) {
	f.t.Helper()
	k := f.keys(scope, dep, name)
	db, err := f.b.kvDB(k, true)
	if err != nil {
		f.t.Fatal(err)
	}
	stored, err := f.b.encodeKV(k.KVLabel, []byte(val))
	if err != nil {
		f.t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		bk, err := tx.CreateBucketIfNotExists([]byte(k.Bucket))
		if err != nil {
			return err
		}
		return bk.Put([]byte(key), stored)
	}); err != nil {
		f.t.Fatal(err)
	}
}

// getKV reads key back; "" when the namespace, bucket or key is missing.
func (f *nsFx) getKV(scope, dep, name, key string) string {
	f.t.Helper()
	k := f.keys(scope, dep, name)
	db, err := f.b.kvDB(k, false)
	if err != nil {
		f.t.Fatal(err)
	}
	var val []byte
	_ = kvView(db, func(tx *bolt.Tx) error {
		if bk := tx.Bucket([]byte(k.Bucket)); bk != nil {
			val = append(val, bk.Get([]byte(key))...)
		}
		return nil
	})
	if val == nil {
		return ""
	}
	plain, err := f.b.decodeKV(k.KVLabel, val)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(plain)
}

// volume lays out a file-backed resource's volume as resenc leaves it
// unmounted: a cipher directory with its config and a file, and the mount
// directory.
func (f *nsFx) volume(scope, dep, name string) (cipher, mount string) {
	f.t.Helper()
	k := f.keys(scope, dep, name)
	cipher, mount = f.b.resenc.CipherDir(k.DirKey, k.Name), f.b.resenc.MountDir(k.DirKey, k.Name)
	for _, d := range []string{cipher, mount} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			f.t.Fatal(err)
		}
	}
	for _, n := range []string{"gocryptfs.conf", "Xy3kq9"} {
		if err := os.WriteFile(filepath.Join(cipher, n), []byte("cipher"), 0o600); err != nil {
			f.t.Fatal(err)
		}
	}
	return cipher, mount
}

// vaultFile writes deployment dep of tile's vault file and returns its path.
func (f *nsFx) vaultFile(tile, dep string) string {
	f.t.Helper()
	p := filepath.Join(f.root, "data", "vault", util.CompKey(tile)+".json")
	if dep != util.MainDeployment {
		files, err := deploymentFiles(tile, dep)
		if err != nil {
			f.t.Fatal(err)
		}
		p = filepath.Join(f.root, filepath.FromSlash(files.Vault))
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"enc":1,"data":"AAAA"}`), 0o600); err != nil {
		f.t.Fatal(err)
	}
	return p
}

// nsRoot is scope's namespace root in dep beyond main.
func (f *nsFx) nsRoot(scope, dep string) string {
	return filepath.Join(f.root, "data", "resources-enc", deploymentsLevel, escS(scope), dep)
}

// meta reads a namespace's ns.json.
func (f *nsFx) meta(scope, dep string) nsMeta {
	f.t.Helper()
	m, ok, err := f.b.readNS(nsOf(scope, dep))
	if err != nil || !ok {
		f.t.Fatalf("ns.json of (%s, %s): ok %v, %v", scope, dep, ok, err)
	}
	return m
}

// setMeta writes a namespace's ns.json as an act's commit would, under its
// hold, making the namespace.
func (f *nsFx) setMeta(scope, dep string, fn func(*nsMeta)) {
	f.t.Helper()
	release, err := f.b.holdNS(nsOf(scope, dep), nsSeeding)
	if err != nil {
		f.t.Fatal(err)
	}
	defer release()
	if err := f.b.updateNS(nsOf(scope, dep), true, fn); err != nil {
		f.t.Fatal(err)
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func wantErr(t *testing.T, err error, status int, text string) {
	t.Helper()
	var de *deployments.Error
	if !errors.As(err, &de) || de.Status != status || !strings.Contains(de.Msg, text) {
		t.Fatalf("got %v (%T), want %d %q", err, err, status, text)
	}
}

// covers P14 P28 — reset empties only its target and removal deletes the
// namespace only with its last claimant, never main's; an orphaned one is
// listed to admins (15-test-plan §3.7; 08-data §9.1–§9.3). The per-tile half
// of a removal — vault, prefs, registrations, logs — is DropDeploymentFiles
// (deploy_keys_test.go), which runs here in §9.2's order; built artifacts
// are the runner's.
func TestResetAndRemoveScope(t *testing.T) {
	f := newNSFx(t)
	f.deps[fxShop], f.deps[fxShopAdmin], f.deps[fxCalendar] = []string{"dev"}, []string{"dev"}, []string{"dev"}

	f.putKV(fxShop, "", "orders", "o1", "main's order")
	f.putKV(fxShop, "dev", "orders", "o1", "dev's order")
	f.putKV(fxCalendar, "dev", "events", "e1", "calendar dev's event")
	f.putKV(fxCalendar, "", "events", "e1", "calendar main's event")
	devCipher, devMount := f.volume(fxShop, "dev", "files")
	calCipher, _ := f.volume(fxCalendar, "", "files")
	mainVault, devVault := f.vaultFile(fxShop, util.MainDeployment), f.vaultFile(fxShop, "dev")
	devKV := filepath.Join(f.nsRoot(fxShop, "dev"), "kv.db")

	// Reset (apps/shop, dev): both claimants' dev stop; only that namespace empties.
	claimants, err := f.b.ResetDeploymentData(fxShop, "dev", "user:ana", false, false, allow, f.stop)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{fxShop, fxShopAdmin}; !slices.Equal(claimants, want) {
		t.Errorf("claimants %v, want %v", claimants, want)
	}
	if want := []string{fxShop + "+dev", fxShopAdmin + "+dev"}; !slices.Equal(f.stops, want) {
		t.Errorf("stopped %v, want %v", f.stops, want)
	}
	switch {
	case f.getKV(fxShop, "dev", "orders", "o1") != "":
		t.Error("dev's kv survived its reset")
	case exists(devKV), exists(devCipher), exists(devMount):
		t.Error("dev's kv file or volume survived its reset")
	case f.getKV(fxShop, "", "orders", "o1") != "main's order":
		t.Error("the reset reached main's kv")
	case f.getKV(fxCalendar, "dev", "events", "e1") != "calendar dev's event":
		t.Error("the reset reached another scope's dev")
	case !exists(calCipher):
		t.Error("the reset reached main's volume of another scope")
	case !exists(devVault) || !exists(mainVault):
		t.Error("a reset without vault removed a vault file")
	}
	m := f.meta(fxShop, "dev")
	if m.State != nsEmpty || !m.Reset || m.By != "user:ana" || m.Busy != "" || len(m.History) != 1 || m.History[0].Op != "reset" {
		t.Errorf("ns.json after the reset: %+v", m)
	}
	for _, tile := range []string{fxShop, fxShopAdmin} { // the state is the namespace's, shared (P28)
		if d := f.b.DeploymentData(tile, "dev"); d.State != nsEmpty || !d.Reset || d.By != "user:ana" {
			t.Errorf("%s's dev data: %+v", tile, d)
		}
	}
	if d := f.b.DeploymentData(fxShop, ""); d.State != nsOriginal {
		t.Errorf("main's data: %+v", d)
	}

	// vault:true empties dev's vault, never main's.
	if _, err := f.b.ResetDeploymentData(fxShop, "dev", "user:ana", true, false, allow, nil); err != nil {
		t.Fatal(err)
	}
	if exists(devVault) || !exists(mainVault) {
		t.Errorf("vault:true: dev's file there %v (want false), main's there %v (want true)", exists(devVault), exists(mainVault))
	}

	// The primary is never reset, nor main while any claimant serves it.
	if _, err := f.b.ResetDeploymentData(fxShop, "", "user:ana", false, false, allow, nil); err == nil {
		t.Fatal("reset main while it is the primary")
	} else {
		wantErr(t, err, http.StatusConflict, "main is the primary of apps/shop")
	}
	f.primary[fxShop] = "dev"
	_, err = f.b.ResetDeploymentData(fxShop, "", "user:ana", false, false, allow, nil)
	wantErr(t, err, http.StatusConflict, "main is the primary of apps/shop/admin")
	f.primary[fxShop] = ""

	// Removal: apps/shop drops dev while apps/shop/admin still claims it.
	f.putKV(fxShop, "dev", "orders", "o2", "dev's again")
	if err := f.b.DropDeploymentFiles(fxShop, "dev"); err != nil {
		t.Fatal(err)
	}
	if gone, err := f.b.DropDeploymentData(fxShop, "dev", false); err != nil || gone {
		t.Fatalf("removal with a sibling claimant: deleted %v, %v", gone, err)
	}
	f.deps[fxShop] = nil // §9.2 step 4
	if f.getKV(fxShop, "dev", "orders", "o2") != "dev's again" {
		t.Fatal("the sibling's namespace lost its data")
	}
	// The last claimant's removal deletes it; a dry run first says so and deletes nothing.
	if gone, err := f.b.DropDeploymentData(fxShopAdmin, "dev", true); err != nil || !gone || !exists(f.nsRoot(fxShop, "dev")) {
		t.Fatalf("dry run: would delete %v, %v; root there %v", gone, err, exists(f.nsRoot(fxShop, "dev")))
	}
	if gone, err := f.b.DropDeploymentData(fxShopAdmin, "dev", false); err != nil || !gone {
		t.Fatalf("last claimant's removal: deleted %v, %v", gone, err)
	}
	if exists(filepath.Dir(f.nsRoot(fxShop, "dev"))) || exists(filepath.Join(f.root, ".xbin", "resenc", deploymentsLevel, escS(fxShop))) {
		t.Error("the namespace, or its emptied parents, survived the last claimant")
	}
	if f.getKV(fxShop, "", "orders", "o1") != "main's order" || !exists(mainVault) {
		t.Error("a removal reached main's data or vault")
	}
	if gone, _ := f.b.DropDeploymentData(fxShop, util.MainDeployment, false); gone {
		t.Error("main's data was deleted")
	}

	// A namespace whose claimant went without a removal is orphaned, listed, kept.
	f.deps[fxCalendar] = nil
	f.b.SweepNamespaces()
	orphans := f.b.OrphanedNamespaces()
	if len(orphans) != 1 || orphans[0].Scope != fxCalendar || orphans[0].Deployment != "dev" || orphans[0].Deletes == "" {
		t.Fatalf("orphans: %+v", orphans)
	}
	if f.getKV(fxCalendar, "dev", "events", "e1") != "calendar dev's event" {
		t.Fatal("an orphan lost its data inside the grace period")
	}
	if err := f.b.DeleteOrphanedNamespace(fxCalendar, "dev"); err != nil || exists(f.nsRoot(fxCalendar, "dev")) {
		t.Fatalf("an admin's delete: %v; root there %v", err, exists(f.nsRoot(fxCalendar, "dev")))
	}
	if exists(filepath.Join(f.root, "data", "resources-enc", deploymentsLevel)) {
		t.Error("the .deployments level outlived its last namespace")
	}

	// main reset while main isn't the primary of a scope's only member: today's keys emptied.
	f.primary[fxCalendar], f.deps[fxCalendar] = "dev", []string{"dev"}
	if _, err := f.b.ResetDeploymentData(fxCalendar, "", "user:ana", false, false, allow, nil); err != nil {
		t.Fatal(err)
	}
	if f.getKV(fxCalendar, "", "events", "e1") != "" || exists(calCipher) {
		t.Error("main's reset left its kv or volume")
	}
	if f.getKV(fxShop, "", "orders", "o1") != "main's order" {
		t.Error("main's reset of one scope reached another's")
	}
	if d := f.b.DeploymentData(fxCalendar, ""); d.State != nsEmpty || !d.Reset {
		t.Errorf("main's data after its reset: %+v", d)
	}
	if !exists(filepath.Join(filepath.Dir(f.nsRoot(fxCalendar, "dev")), "main", nsMetaFile)) {
		t.Error("main's reset left no metadata beside the namespaces")
	}
}

// covers P28 T8 — resetting a shared (scope, name) namespace needs the reset
// level on every claimant, the refusal names the tile that blocks it, and
// nothing stops or changes until every claimant allows it (08-data §6.3,
// §14; 11-contract §1.14). The judgement is the deployments plane's own
// Authorize, as its reset passes it.
func TestSharedScopeResetNeedsEveryTile(t *testing.T) {
	f := newNSFx(t)
	b := f.b
	dormantRecord(t, f.root, fxShop, util.MainDeployment, false)
	dormantRecord(t, f.root, fxShopAdmin, util.MainDeployment, false)
	dp := &deployments.Plane{Root: f.root, OwnerRef: b.Users.Owner, IsAdmin: b.IsAdmin, MayManage: b.MayManageDeployments}
	if err := dp.Boot(); err != nil {
		t.Fatal(err)
	}
	b.DeploymentAnswers.PrimaryOf, b.DeploymentAnswers.DeploymentsOf = dp.Primary, dp.DeploymentsOf
	judge := func(p auth.Principal) func(string) error {
		return func(tile string) error {
			_, err := dp.Authorize(p, deployments.OpReset, deployments.Subject{Tile: tile, Deployment: "dev",
				Primary: dp.Primary(tile), Record: true})
			return err
		}
	}
	tom, ana := deployPerson(t, b, fxTerm), deployPerson(t, b, fxAdmin)
	if judge(tom)(fxShop) != nil || judge(tom)(fxShopAdmin) == nil {
		t.Fatalf("fixture: %s should have terminal on %s only", fxTerm, fxShop)
	}
	f.putKV(fxShop, "dev", "orders", "o1", "dev's order")

	blocked := `reset of apps/shop's "dev" data needs terminal-level access on apps/shop/admin too`
	agent := auth.Principal{Component: fxShop, Via: "terminal", UserID: fxAdmin, User: ana.User, Access: ana.Access}
	for name, p := range map[string]auth.Principal{"terminal-level person": tom, "the tile's own agent": agent} {
		for _, dry := range []bool{true, false} {
			_, err := b.ResetDeploymentData(fxShop, "dev", "user:x", false, dry, judge(p), f.stop)
			wantErr(t, err, http.StatusForbidden, blocked)
			if len(f.stops) != 0 || f.getKV(fxShop, "dev", "orders", "o1") != "dev's order" || b.busyAct(nsOf(fxShop, "dev")) != "" {
				t.Fatalf("%s (dry %v): a refused reset stopped %v or changed data", name, dry, f.stops)
			}
		}
	}

	// Busy is judged after authority: a claimant's refusal comes first.
	release, err := b.holdNS(nsOf(fxShop, "dev"), nsSeeding)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.ResetDeploymentData(fxShop, "dev", "user:tom", false, false, judge(tom), f.stop)
	wantErr(t, err, http.StatusForbidden, blocked)
	_, err = b.ResetDeploymentData(fxShop, "dev", "user:ana", false, false, judge(ana), f.stop)
	wantErr(t, err, http.StatusConflict, "dev's data is being seeded")
	release()

	claimants, err := b.ResetDeploymentData(fxShop, "dev", "user:ana", false, true, judge(ana), f.stop)
	if err != nil || len(claimants) != 2 || len(f.stops) != 0 {
		t.Fatalf("an admin's dry run: %v, %v; stopped %v", claimants, err, f.stops)
	}
	if _, err := b.ResetDeploymentData(fxShop, "dev", "user:ana", false, false, judge(ana), f.stop); err != nil {
		t.Fatal(err)
	}
	if want := []string{fxShop + "+dev", fxShopAdmin + "+dev"}; !slices.Equal(f.stops, want) {
		t.Errorf("stopped %v, want every claimant's dev %v", f.stops, want)
	}
	if f.getKV(fxShop, "dev", "orders", "o1") != "" {
		t.Error("the admin's reset left dev's data")
	}
}

// covers P28 T8 NP-08-4 — joining a (scope, name) namespace that holds data
// (seeded, restored or partial) is a tile manager's act in a person's own
// session; joining an empty one, or none, is anyone's who may add, and the
// answer says whose data it joins (08-data §6.2; 11-contract §1.5, §1.14).
func TestJoinSeededNamespaceManagerOnly(t *testing.T) {
	f := newNSFx(t)
	b := f.b
	f.deps[fxShop] = []string{"dev"}
	ana := deployPerson(t, b, fxAdmin)
	askers := map[string]auth.Principal{
		"manager":                       ana,
		"terminal-level person":         deployPerson(t, b, fxTerm),
		"a manager's terminal token":    {Component: fxShopAdmin, Via: "terminal", UserID: fxAdmin, User: ana.User, Access: ana.Access},
		"an element holding xbin admin": deployTilePrincipal(fxConsole, "instance", ""),
	}
	if j, err := b.JoinDeploymentData(fxShopAdmin, "dev", false); j != nil || err != nil {
		t.Fatalf("no namespace yet: %+v, %v", j, err)
	}
	f.putKV(fxShop, "dev", "orders", "o1", "synthetic") // written by dev's code: empty by its metadata
	if j, err := b.JoinDeploymentData(fxShopAdmin, "dev", false); err != nil || j == nil || j.State != nsEmpty || j.Scope != fxShop {
		t.Fatalf("an empty namespace: %+v, %v", j, err)
	}
	for _, st := range []struct{ state, failed, verb string }{
		{nsSeeded, "", "seeded"}, {nsRestored, "", "restored"}, {nsPartial, "seed", "partly seeded"}, {nsPartial, "restore", "partly restored"},
	} {
		f.setMeta(fxShop, "dev", func(m *nsMeta) {
			m.State, m.Failed, m.By, m.At, m.From = st.state, st.failed, "user:ana", "2026-09-27T10:12:03Z", "main"
		})
		for name, p := range askers {
			j, err := b.JoinDeploymentData(fxShopAdmin, "dev", b.MayManageDeployments(p, fxShopAdmin))
			if name == "manager" {
				if err != nil || j == nil || j.State != st.state || j.By != "user:ana" || j.At == "" {
					t.Errorf("%s joining %s data: %+v, %v", name, st.state, j, err)
				}
				continue
			}
			wantErr(t, err, http.StatusForbidden,
				`apps/shop's "dev" data was `+st.verb+` by user:ana 2026-09-27T10:12:03Z: joining it is a tile manager's act`)
		}
	}
	// A held namespace is busy for a join; a scope too long for namespaces can't have deployments.
	release, _ := b.holdNS(nsOf(fxShop, "dev"), nsResetting)
	_, err := b.JoinDeploymentData(fxShopAdmin, "dev", true)
	wantErr(t, err, http.StatusConflict, "dev's data is being reset")
	release()
	if j, err := b.JoinDeploymentData(fxShop, util.MainDeployment, false); j != nil || err != nil {
		t.Errorf("main is joined: %+v, %v", j, err)
	}
}

// covers P5 PO-7 — the broker's half of every dry run (add's join, reset,
// removal) and every read of a namespace's state creates nothing: no
// .deployments level, ns.json, kv file or mount directory, on a zero-state
// workspace and on one whose tile has deployments nothing wrote to yet; a
// real reset of a namespace never made makes none (08-data §3.7, §14). The
// seed's and restore's halves are their cards'; the plane's are its own.
func TestDataDryRunCreatesNothing(t *testing.T) {
	f := newNSFx(t)
	b := f.b
	snapshot := func() []string {
		var out []string
		_ = filepath.WalkDir(f.root, func(p string, d fs.DirEntry, err error) error {
			if info, e := d.Info(); err == nil && e == nil {
				rel, _ := filepath.Rel(f.root, p)
				entry := rel + " " + info.Mode().String()
				if info.Mode().IsRegular() {
					entry += " " + strconv.FormatInt(info.Size(), 10)
				}
				out = append(out, entry)
			}
			return nil
		})
		sort.Strings(out)
		return out
	}
	every := func() {
		t.Helper()
		_, _ = b.ResetDeploymentData(fxShop, "dev", "user:ana", true, true, allow, f.stop)
		_, _ = b.ResetDeploymentData(fxShop, "", "user:ana", true, true, allow, f.stop)
		_, _ = b.DropDeploymentData(fxShop, "dev", true)
		_, _ = b.JoinDeploymentData(fxShopAdmin, "dev", false)
		for _, tile := range []string{fxShop, fxShopAdmin, fxCalendar} {
			_ = b.DeploymentData(tile, "dev")
			_ = b.DeploymentData(tile, "")
			_ = b.nsStartBlocked(fxShop, "dev")
		}
		b.nsAvailable(httptest.NewRecorder(), fxShop, "dev")
		_ = b.OrphanedNamespaces()
		b.SweepNamespaces()
	}
	before := snapshot()
	every() // the zero state: no tile has a deployment beyond main
	f.deps[fxShop], f.deps[fxShopAdmin] = []string{"dev"}, []string{"dev"}
	every()
	if len(f.stops) != 0 {
		t.Errorf("a dry run stopped %v", f.stops)
	}
	if _, err := b.ResetDeploymentData(fxShop, "dev", "user:ana", false, false, allow, f.stop); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); !slices.Equal(before, after) {
		t.Errorf("the workspace changed:\nbefore %v\nafter  %v", before, after)
	}
	for _, p := range []string{filepath.Join("data", "resources-enc", deploymentsLevel), filepath.Join(".xbin", "resenc", deploymentsLevel)} {
		if exists(filepath.Join(f.root, p)) {
			t.Errorf("%s exists", p)
		}
	}
}

// covers P14 K9 — a reset aborts when a volume stays mounted after its
// unmount (EBUSY): nothing is removed through the mount, the data and
// ns.json stay as they were, and the hold lifts (08-data §9.1 step 2, §14).
func TestResetAbortsWhileMounted(t *testing.T) {
	f := newNSFx(t)
	f.deps[fxShop] = []string{"dev"}
	f.putKV(fxShop, "dev", "orders", "o1", "dev's order")
	cipher, mount := f.volume(fxShop, "dev", "files")
	f.setMeta(fxShop, "dev", func(m *nsMeta) { m.State, m.By = nsSeeded, "user:ana" })

	// The device walk finds no mount in a plain tree, nor anything under a missing one.
	if pts, err := mountPointsUnder(filepath.Dir(filepath.Dir(mount))); err != nil || len(pts) != 0 {
		t.Fatalf("a plain tree's mount points: %v, %v", pts, err)
	}
	if pts, err := mountPointsUnder(filepath.Join(f.root, "no", "such")); err != nil || len(pts) != 0 {
		t.Fatalf("a missing tree's mount points: %v, %v", pts, err)
	}

	var probed []string
	nsMountPoints = func(dir string) ([]string, error) { // the volume never unmounts
		probed = append(probed, dir)
		if strings.HasPrefix(mount, dir) {
			return []string{mount}, nil
		}
		return nil, nil
	}
	t.Cleanup(func() { nsMountPoints = mountPointsUnder })
	_, err := f.b.ResetDeploymentData(fxShop, "dev", "user:ana", false, false, allow, f.stop)
	if err == nil || !strings.Contains(err.Error(), "still mounted") || len(probed) != 2 {
		t.Fatalf("reset over a busy mount: %v (probed %v)", err, probed)
	}
	if !exists(cipher) || !exists(mount) || f.getKV(fxShop, "dev", "orders", "o1") != "dev's order" {
		t.Error("the aborted reset removed data")
	}
	if m := f.meta(fxShop, "dev"); m.State != nsSeeded || m.Busy != "" || m.Reset {
		t.Errorf("ns.json after the aborted reset: %+v", m)
	}
	if act := f.b.busyAct(nsOf(fxShop, "dev")); act != "" {
		t.Errorf("the aborted reset left the namespace held by %q", act)
	}
	// Removal aborts alike, and leaves the rest to the sweep.
	f.deps[fxShop] = nil
	if gone, err := f.b.DropDeploymentData(fxShop, "dev", false); gone || err == nil || !exists(cipher) {
		t.Fatalf("removal over a busy mount: deleted %v, %v", gone, err)
	}
	if m := f.meta(fxShop, "dev"); m.Busy != nsRemoving {
		t.Errorf("the aborted removal isn't left to the sweep: %+v", m)
	}
	nsMountPoints = mountPointsUnder
	f.b.SweepNamespaces()
	if exists(f.nsRoot(fxShop, "dev")) {
		t.Error("the sweep didn't finish the removal")
	}
}

// covers NP-08-14 P28 — the sweep: a scope.json gone for a while orphans its
// namespaces and deletes nothing inside the grace period; a returning
// claimant clears the mark; past 14 days the orphan goes. An act a crash cut
// short becomes partial, and backends addressing it refuse to start; main's
// metadata is never collected; without the plane's answers nothing is
// orphaned (08-data §8.5, §9.3, §14).
func TestNamespaceGCGrace(t *testing.T) {
	f := newNSFx(t)
	b := f.b
	f.deps[fxShop], f.deps[fxShopAdmin] = []string{"dev"}, []string{"dev"}
	f.putKV(fxShop, "dev", "orders", "o1", "dev's order")
	scopeJSON := filepath.Join(f.root, fxShop, "scope.json")
	saved, err := os.ReadFile(scopeJSON)
	if err != nil {
		t.Fatal(err)
	}
	rescan := func() {
		t.Helper()
		if err := b.Reg.Rescan(); err != nil {
			t.Fatal(err)
		}
	}

	// Without the plane's answers, nothing is orphaned.
	answers := b.DeploymentAnswers
	b.DeploymentAnswers = DeploymentAnswers{}
	b.SweepNamespaces()
	if len(b.OrphanedNamespaces()) != 0 {
		t.Fatal("orphaned without the plane's answers")
	}
	b.DeploymentAnswers = answers

	if err := os.Remove(scopeJSON); err != nil { // an agent checks out an old branch
		t.Fatal(err)
	}
	rescan()
	b.SweepNamespaces()
	b.SweepNamespaces()
	if o := b.OrphanedNamespaces(); len(o) != 1 || o[0].Scope != fxShop {
		t.Fatalf("orphans after scope.json went: %+v", o)
	}
	if f.getKV(fxShop, "dev", "orders", "o1") != "dev's order" {
		t.Fatal("the grace period didn't keep the data")
	}
	if err := os.WriteFile(scopeJSON, saved, 0o644); err != nil {
		t.Fatal(err)
	}
	rescan()
	b.SweepNamespaces()
	if o := b.OrphanedNamespaces(); len(o) != 0 {
		t.Fatalf("a returning claimant left the mark: %+v", o)
	}
	if m := f.meta(fxShop, "dev"); len(m.History) != 2 || m.History[0].Op != "orphaned" || m.History[1].Op != "claimed" {
		t.Errorf("history: %+v", m.History)
	}

	// Past the grace period the orphan is deleted.
	f.deps[fxShop], f.deps[fxShopAdmin] = nil, nil
	b.SweepNamespaces()
	f.setMeta(fxShop, "dev", func(m *nsMeta) { m.Orphaned = nowStamp(time.Now().Add(-nsOrphanGrace + time.Hour)) })
	b.SweepNamespaces()
	if !exists(f.nsRoot(fxShop, "dev")) {
		t.Fatal("deleted an hour before the grace period ends")
	}
	f.setMeta(fxShop, "dev", func(m *nsMeta) { m.Orphaned = nowStamp(time.Now().Add(-nsOrphanGrace - time.Hour)) })
	b.SweepNamespaces()
	if exists(f.nsRoot(fxShop, "dev")) {
		t.Fatal("an orphan outlived its grace period")
	}

	// A crash mid-seed (and mid-reset) leaves partial; its backends don't start.
	f.deps[fxShop] = []string{"dev"}
	for _, c := range []struct{ busy, act string }{{nsSeeding, "seed"}, {nsResetting, "reset"}} {
		f.setMeta(fxShop, "dev", func(m *nsMeta) { *m = nsMeta{State: nsEmpty, Busy: c.busy} })
		if err := b.nsStartBlocked(fxShop, "dev"); err != nil {
			t.Fatalf("a mark without a hold refused a start before the sweep: %v", err)
		}
		b.SweepNamespaces()
		m := f.meta(fxShop, "dev")
		if m.State != nsPartial || m.Busy != "" || m.Failed != c.act || !strings.Contains(m.Error, "xbind stopped during the "+c.act) {
			t.Errorf("after a crash mid-%s: %+v", c.act, m)
		}
		want := c.act + " of apps/shop for dev failed at " + c.act + ": reset or seed again"
		if err := b.nsStartBlocked(fxShop, "dev"); err == nil || err.Error() != want {
			t.Errorf("start after a crash mid-%s: %v, want %q", c.act, err, want)
		}
		if d := b.DeploymentData(fxShop, "dev"); d.State != nsPartial {
			t.Errorf("data state: %+v", d)
		}
	}
	// A reset recovers it.
	if _, err := b.ResetDeploymentData(fxShop, "dev", "user:ana", false, false, allow, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.nsStartBlocked(fxShop, "dev"); err != nil {
		t.Errorf("after the reset: %v", err)
	}

	// main's metadata is never collected, whoever claims.
	f.deps[fxCalendar], f.primary[fxCalendar] = []string{"dev"}, "dev"
	if _, err := b.ResetDeploymentData(fxCalendar, "", "user:ana", false, false, allow, nil); err != nil {
		t.Fatal(err)
	}
	f.deps[fxCalendar], f.primary[fxCalendar] = nil, ""
	b.SweepNamespaces()
	if m := f.meta(fxCalendar, util.MainDeployment); m.Orphaned != "" || m.State != nsEmpty {
		t.Errorf("main's metadata swept: %+v", m)
	}
}

// covers P28 NP-08-6 — the hold and the write gate (08-data §8.2): a held
// namespace answers data-plane requests 503 with Retry-After, refuses starts
// and other acts (409 data busy) and reports busy in the state; API writes
// wait on the gate while an act holds it exclusively, and answer 503 past
// the wait; other namespaces never wait.
func TestNamespaceHoldAndWriteGate(t *testing.T) {
	f := newNSFx(t)
	b := f.b
	f.deps[fxShop] = []string{"dev"}
	shop := nsOf(fxShop, "dev")

	release, err := b.holdNS(shop, nsSeeding)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if b.nsAvailable(w, fxShop, "dev") || w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("a held namespace answered %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	var body map[string]string
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || body["error"] != "dev's data is being seeded" {
		t.Errorf("503 body %s", w.Body)
	}
	if !b.nsAvailable(httptest.NewRecorder(), fxShop, "") || !b.nsAvailable(httptest.NewRecorder(), fxCalendar, "dev") {
		t.Error("the hold reached another namespace")
	}
	if _, err := b.holdNS(shop, nsResetting); err == nil {
		t.Error("a second act held a held namespace")
	}
	if err := b.nsStartBlocked(fxShop, "dev"); err == nil {
		t.Error("a backend may start on a held namespace")
	}
	if d := b.DeploymentData(fxShop, "dev"); d.Busy != nsSeeding {
		t.Errorf("busy %q", d.Busy)
	}
	release()
	if !b.nsAvailable(httptest.NewRecorder(), fxShop, "dev") || b.DeploymentData(fxShop, "dev").Busy != "" {
		t.Fatal("the hold didn't lift")
	}
	// The sweep's brief hold keeps nothing off.
	sweep, _ := b.holdNS(shop, nsSweeping)
	if !b.nsAvailable(httptest.NewRecorder(), fxShop, "dev") || b.nsStartBlocked(fxShop, "dev") != nil {
		t.Error("the sweep's hold kept requests or starts off")
	}
	sweep()

	// The write gate.
	wait := nsWriteWait
	nsWriteWait = 50 * time.Millisecond
	t.Cleanup(func() { nsWriteWait = wait })
	g := b.nsTab().gate(shop)
	g.Lock()
	w = httptest.NewRecorder()
	if _, ok := b.nsWriting(w, fxShop, "dev"); ok || w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("a write past the wait: ok, %d", w.Code)
	}
	if done, ok := b.nsWriting(httptest.NewRecorder(), fxCalendar, "dev"); !ok {
		t.Fatal("another namespace's write waited")
	} else {
		done()
	}
	got := make(chan bool)
	nsWriteWait = 5 * time.Second
	go func() {
		done, ok := b.nsWriting(httptest.NewRecorder(), fxShop, "dev")
		if ok {
			done()
		}
		got <- ok
	}()
	time.Sleep(30 * time.Millisecond)
	g.Unlock()
	if !<-got {
		t.Fatal("a waiting write didn't pass once the gate opened")
	}
	// An exclusive holder waits out a write in flight.
	done, _ := b.nsWriting(httptest.NewRecorder(), fxShop, "dev")
	locked := make(chan struct{})
	go func() { g.Lock(); close(locked); g.Unlock() }()
	select {
	case <-locked:
		t.Fatal("the gate closed over a write in flight")
	case <-time.After(30 * time.Millisecond):
	}
	done()
	<-locked
}
