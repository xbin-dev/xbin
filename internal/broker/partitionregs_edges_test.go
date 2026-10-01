package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/proxy"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/util"
)

// takeBus waits for exactly n bus deliveries (and no extra one).
func takeBus(t *testing.T, deliveries chan busCall, n int) []busCall {
	t.Helper()
	var out []busCall
	deadline := time.After(3 * time.Second)
	for len(out) < n {
		select {
		case c := <-deliveries:
			out = append(out, c)
		case <-deadline:
			t.Fatalf("%d deliveries, want %d: %+v", len(out), n, out)
		}
	}
	select {
	case c := <-deliveries:
		t.Fatalf("an extra delivery: %+v (had %+v)", c, out)
	case <-time.After(100 * time.Millisecond):
	}
	return out
}

// setTiles gives person id the tile levels tiles.
func setTiles(t *testing.T, b *Broker, id string, tiles map[string]string) {
	t.Helper()
	u, ok := b.Users.Get(id)
	if !ok {
		t.Fatalf("no %s", id)
	}
	u.Tiles = tiles
	if _, err := b.Users.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
}

// partRunning makes the broker's view of the runner answer running for
// the "<tile> <pkey>" pairs the returned setter marks.
func partRunning(b *Broker) func(tile, pkey string, on bool) {
	var mu sync.Mutex
	running := map[string]bool{}
	b.SetPartitionRunner(func(tile, pkey string) bool {
		mu.Lock()
		defer mu.Unlock()
		return running[tile+" "+pkey]
	}, nil, nil)
	return func(tile, pkey string, on bool) {
		mu.Lock()
		running[tile+" "+pkey] = on
		mu.Unlock()
	}
}

// partSubRows is GET /bus/subscriptions as p lists it.
func partSubRows(t *testing.T, b *Broker, p auth.Principal) map[string]busSubView {
	t.Helper()
	rec := call(t, b.apiBusSubsList, p, "GET", "/bus/subscriptions", "", nil)
	var body struct {
		Subscriptions []busSubView `json:"subscriptions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	out := map[string]busSubView{}
	for _, r := range body.Subscriptions {
		out[r.Name] = r
	}
	return out
}

// covers PD-13 PD-20 03§D 05§1 05§2 — a person's partition subscribing to
// another partitioned tile's bus is a reach of that tile by the partition:
// carol can't read apps/pg, so her partition of apps/q can't subscribe to
// its buses (as it can't publish on them); with partitionConsent on and no
// consent, alice's partition can't either, and one registered before gets
// nothing (skipped at publish and at delivery, listed dormant) until she
// consents. A person's event starts only the partition whose own tile
// published it: another tile's code acting for her reaches her partition's
// subscription only while it runs (dormantDrops).
func TestPartitionBusSubsCrossTile(t *testing.T) {
	w := partRegWS(t)
	b := w.b
	grants := `[{"from":"apps/x","target":"apps/pg","role":"reader"},{"from":"apps/x","target":"apps/pu","role":"reader"},
		{"from":"apps/q","target":"apps/pg","role":"reader"},{"from":"apps/q","target":"apps/pu","role":"reader"},
		{"from":"apps/q","target":"apps/x","role":"reader"},
		{"from":"apps/q","target":"res:apps/pg/feed","role":"writer"},{"from":"apps/q","target":"res:apps/pg/wall","role":"writer"}]`
	w.write(map[string]string{
		"xbin.json": `{"schema":1,"grants":` + grants + `}`,
		"apps/q/xbin.json": `{"runtime":"go","partition":["user"],"uses":[{"target":"apps/pg","role":"reader"},{"target":"apps/pu","role":"reader"},` +
			`{"target":"apps/x","role":"reader"},{"target":"res:apps/pg/feed","role":"writer"},{"target":"res:apps/pg/wall","role":"writer"}]}`,
	})
	w.rescan()
	if st, _, _ := w.state("apps/q"); st != registry.PartitionPartitioned {
		t.Fatalf("apps/q: %v", st)
	}
	deliveries := fakeBusDispatch(b, nil)
	setRunning := partRunning(b)
	sub := func(p auth.Principal, name, res string) (int, string) {
		t.Helper()
		rec := call(t, b.apiBusSubsPut, p, "PUT", "/bus/subscriptions", `{"name":"`+name+`","resource":"res:apps/pg/`+res+`","path":"/bus"}`, nil)
		return rec.Code, rec.Body.String()
	}
	publish := func(p auth.Principal, res string) (int, string) {
		t.Helper()
		rec := call(t, b.apiBusPublish, p, "POST", "/bus/publish", `{"resource":"res:apps/pg/`+res+`","topic":"t","data":1}`, nil)
		return rec.Code, rec.Body.String()
	}
	carolQ, aliceQ, alicePG := partInst("apps/q", "carol"), partInst("apps/q", "alice"), partInst("apps/pg", "alice")

	// carol can't read apps/pg: neither publish nor subscribe
	if code, body := publish(carolQ, "wall"); code != 403 || !strings.Contains(body, "carol can't read apps/pg") {
		t.Errorf("carol's partition of apps/q publishes on apps/pg's wall: %d %s", code, body)
	}
	for _, res := range []string{"feed", "wall"} {
		if code, body := sub(carolQ, res, res); code != 403 || !strings.Contains(body, "carol can't read apps/pg") {
			t.Errorf("carol's partition of apps/q subscribes to apps/pg's %s: %d %s", res, code, body)
		}
	}
	// alice reads apps/pg: her partition of apps/q subscribes to both, and
	// her partition of apps/pg to its own feed
	for _, c := range []struct {
		p    auth.Principal
		name string
	}{{aliceQ, "feed"}, {aliceQ, "wall"}, {alicePG, "feed"}} {
		if code, body := sub(c.p, c.name, c.name); code != 200 {
			t.Fatalf("%s subscribes to apps/pg's %s: %d %s", c.p.Component, c.name, code, body)
		}
	}
	pk := w.pkeyOf("alice")

	// nothing runs: alice's partition of apps/q publishing on apps/pg's feed
	// starts her apps/q partition's subscription (its own tile published)
	// and never her apps/pg partition's (another tile's code for her)
	if code, body := publish(aliceQ, "feed"); code != 200 {
		t.Fatalf("alice's apps/q partition publishes: %d %s", code, body)
	}
	if got := takeBus(t, deliveries, 1); got[0].comp != "apps/q" || got[0].p.Partition != "user:alice" {
		t.Errorf("alice's apps/q event: %+v", got)
	}
	// and the other way round
	if code, body := publish(alicePG, "feed"); code != 200 {
		t.Fatalf("alice's apps/pg partition publishes: %d %s", code, body)
	}
	if got := takeBus(t, deliveries, 1); got[0].comp != "apps/pg" || got[0].p.Partition != "user:alice" {
		t.Errorf("alice's apps/pg event: %+v", got)
	}
	for _, tile := range []string{"apps/pg", "apps/q"} {
		if c := b.PartitionRegistrations(tile, "main", pk); c.DormantDrops != 1 {
			t.Errorf("%s: alice's partition's counts %+v, want one dormant drop", tile, c)
		}
	}
	// alice's apps/q partition running: her apps/pg partition's events and
	// the global instance's shared ones reach it
	setRunning("apps/q", pk, true)
	publish(alicePG, "feed")
	got := takeBus(t, deliveries, 2)
	comps := []string{got[0].comp, got[1].comp}
	slices.Sort(comps)
	if !slices.Equal(comps, []string{"apps/pg", "apps/q"}) {
		t.Errorf("alice's apps/pg event with apps/q running: %+v", got)
	}
	if code, body := publish(instanceOf("apps/pg", ""), "wall"); code != 200 {
		t.Fatalf("global publishes on wall: %d %s", code, body)
	}
	if got := takeBus(t, deliveries, 1); got[0].comp != "apps/q" || got[0].p.Partition != "user:alice" {
		t.Errorf("a shared event with alice's apps/q partition running: %+v", got)
	}

	// partitionConsent on, no consent: alice's partition of apps/q can't
	// subscribe, and her earlier subscriptions get nothing, listed dormant
	partRouteConsent(w, true)
	if code, body := sub(aliceQ, "feed2", "feed"); code != 403 || !strings.Contains(body, "alice hasn't let apps/q use their apps/pg data") {
		t.Errorf("a subscription without consent: %d %s", code, body)
	}
	publish(alicePG, "feed")
	if got := takeBus(t, deliveries, 1); got[0].comp != "apps/pg" {
		t.Errorf("without consent: %+v", got)
	}
	rows := partSubRows(t, b, aliceQ)
	if r := rows["feed"]; !r.Dormant || r.DormantEvents != 1 {
		t.Errorf("alice's apps/q feed row without consent: %+v", r)
	}
	if r := partSubRows(t, b, alicePG)["feed"]; r.Dormant {
		t.Errorf("alice's own apps/pg subscription needs no consent: %+v", r)
	}
	// a delivery queued before the consent went is refused at delivery too
	snap := busSubSnap{sub: rows["feed"].busSub, part: "user:alice", pkey: pk}
	if out, why := b.bus.deliverPart(b.bus.dispatch, snap, busDelivery{Subscription: "feed", cold: true}); out != "refused" ||
		!strings.Contains(why, "hasn't let") {
		t.Errorf("a queued delivery without consent: %s %q", out, why)
	}
	// alice consents: it flows again
	withSeam(t, &partitionConsentHolds, func(_ *Broker, id, from, to string) bool {
		return id == "alice" && from == "apps/q" && to == "apps/pg"
	})
	if code, body := sub(aliceQ, "feed2", "feed"); code != 200 {
		t.Errorf("a subscription with consent: %d %s", code, body)
	}
	publish(alicePG, "feed")
	if got := takeBus(t, deliveries, 3); len(got) != 3 {
		t.Errorf("with consent: %+v", got)
	}
}

// covers PD-20 03§D — a person who loses read on the tile: their
// partition's jobs stop ticking and its subscriptions list dormant; given
// read again, both resume without registering again.
func TestPartitionRegsLostRead(t *testing.T) {
	w := partRegWS(t)
	b := w.b
	var calls cronCalls
	calls.install(b)
	fakeBusDispatch(b, nil)
	alice := partInst("apps/pu", "alice")
	if rec := call(t, b.apiCronPut, alice, "PUT", "/cron/jobs", `{"name":"j","resource":"res:apps/pu/beat","schedule":"@every 1m","path":"/t"}`, nil); rec.Code != 200 {
		t.Fatalf("alice's job: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, b.apiBusSubsPut, alice, "PUT", "/bus/subscriptions", `{"name":"s","resource":"res:apps/pu/feed","path":"/b"}`, nil); rec.Code != 200 {
		t.Fatalf("alice's subscription: %d %s", rec.Code, rec.Body)
	}
	setTiles(t, b, "alice", map[string]string{"apps/pg": "read"})
	tickAll(b)
	if got := calls.take(); len(got) != 0 {
		t.Errorf("a job of a person who can't read the tile ticked: %+v", got)
	}
	if r := partSubRows(t, b, alice)["s"]; !r.Dormant {
		t.Errorf("the subscription of a person who can't read the tile: %+v", r)
	}
	setTiles(t, b, "alice", map[string]string{"apps/*": "read"})
	tickAll(b)
	if got := calls.take(); len(got) != 1 || got[0].Partition != "user:alice" {
		t.Errorf("read again: %+v", got)
	}
	if r := partSubRows(t, b, alice)["s"]; r.Dormant {
		t.Errorf("read again, the subscription: %+v", r)
	}
}

// covers PD-26 03§E — a removed tile orphans its people's partitions
// (tile-removed), and its return reclaims them; an orphan past the
// retention is never deleted while its tile is paused (pending), and is
// once a manager's decision lifts the pause.
func TestPartitionRecordsTileReturns(t *testing.T) {
	w := partRegWS(t)
	b := w.b
	var calls cronCalls
	calls.install(b)
	alice := partInst("apps/pu", "alice")
	if rec := call(t, b.apiCronPut, alice, "PUT", "/cron/jobs", `{"name":"j","resource":"res:apps/pu/beat","schedule":"@every 1m","path":"/t"}`, nil); rec.Code != 200 {
		t.Fatalf("alice's job: %d %s", rec.Code, rec.Body)
	}
	dir := w.partDir("apps/pu", "alice")
	manifest := string(readFile(t, filepath.Join(w.root, "apps/pu/xbin.json")))
	scope := string(readFile(t, filepath.Join(w.root, "apps/pu/scope.json")))
	if err := os.RemoveAll(filepath.Join(w.root, "apps/pu")); err != nil {
		t.Fatal(err)
	}
	w.rescan()
	b.sweepPartitionRecords(time.Now())
	if r, _, _ := readPartitionRecordAt(dir); r.State != partStateOrphaned || r.Reason != orphanTileRemoved {
		t.Fatalf("the removed tile's partition: %+v", r)
	}
	w.write(map[string]string{"apps/pu/xbin.json": manifest, "apps/pu/scope.json": scope, "apps/pu/backend/main.go": "package main\n"})
	w.rescan()
	b.sweepPartitionRecords(time.Now())
	if r, _, _ := readPartitionRecordAt(dir); r.State != partStateActive || r.Reason != "" {
		t.Fatalf("the returned tile's partition: %+v", r)
	}

	// orphaned long ago, the tile pending: kept
	b.markPartitionRecord(partitionDirOf{dir: dir}, orphanUserDeleted, nowStamp(time.Now().Add(-48*time.Hour)))
	w.write(map[string]string{"apps/pu/xbin.json": strings.Replace(manifest, `"partition":["user"],`, "", 1)})
	w.rescan()
	if st, _, _ := w.state("apps/pu"); st != registry.PartitionPending {
		t.Fatalf("apps/pu is %s, want pending", st)
	}
	withSeam(t, &partitionRetention, time.Hour)
	b.sweepPartitionRecords(time.Now())
	if !exists(dir) {
		t.Fatal("an orphan of a paused tile was deleted")
	}
	// the manifest asks for people's partitions again: the pause lifts, the
	// orphan goes
	w.write(map[string]string{"apps/pu/xbin.json": manifest})
	w.rescan()
	if st, _, _ := w.state("apps/pu"); st != registry.PartitionPartitioned {
		t.Fatalf("apps/pu is %s, want partitioned", st)
	}
	b.sweepPartitionRecords(time.Now())
	if exists(dir) {
		t.Error("the orphan outlived the retention once its tile ran again")
	}
}

// deferringParts is a proxy.PartitionRunner whose every start is deferred,
// as boot's adapter answers the runner's ErrPartitionDeferred.
type deferringParts struct {
	mu    sync.Mutex
	asked []string
}

func (d *deferringParts) EnsurePartition(_ context.Context, c *registry.Component, dep, part string, class proxy.PartitionStart) (proxy.PartitionGen, error) {
	d.mu.Lock()
	d.asked = append(d.asked, fmt.Sprintf("%s %s %s %d", c.Path, dep, part, class))
	d.mu.Unlock()
	return nil, sbx.Refuse(fmt.Errorf("%w: 4 background starts of people's partitions already run", runner.ErrPartitionDeferred))
}

func (d *deferringParts) TrackPartition(tile, dep, part string, passive bool) func() {
	return func() {}
}

// covers 03§D 03§A.5 — a tick whose start the runner defers, through the
// real proxy (DispatchViaProxy): the proxy's 503 is recognized, the tick
// is tried again (a background start both times), and one still deferred
// when the next tick is due is counted as missed.
func TestPartitionCronDeferredViaProxy(t *testing.T) {
	w := partRegWS(t)
	b := w.b
	parts := &deferringParts{}
	px := &proxy.Proxy{Reg: b.Reg, Hub: b.Hub, Partitions: parts}
	px.Route = func(p auth.Principal, c *registry.Component, q string) proxy.Decision {
		return proxy.Decision(b.Route(p, c, q))
	}
	var mu sync.Mutex
	var answers []int
	dispatch := DispatchViaProxy(px)
	b.SetDispatch(func(p auth.Principal, comp, path string) (int, string) {
		code, body := dispatch(p, comp, path)
		mu.Lock()
		answers = append(answers, code)
		mu.Unlock()
		if !partDeferred(code, body) {
			t.Errorf("the proxy's answer to a deferred start isn't recognized: %d %s", code, body)
		}
		return code, body
	})
	tries := 0
	withSeam(t, &cronRetryDelay, func(time.Duration) time.Duration {
		mu.Lock()
		defer mu.Unlock()
		tries++
		if tries == 1 {
			return 10 * time.Millisecond // tried again once
		}
		return 0 // then the next tick is due: missed
	})
	alice := partInst("apps/pu", "alice")
	if rec := call(t, b.apiCronPut, alice, "PUT", "/cron/jobs", `{"name":"j","resource":"res:apps/pu/beat","schedule":"@every 1m","path":"/t"}`, nil); rec.Code != 200 {
		t.Fatalf("alice's job: %d %s", rec.Code, rec.Body)
	}
	tickAll(b)
	deadline := time.Now().Add(5 * time.Second)
	for b.PartitionRegistrations("apps/pu", "main", w.pkeyOf("alice")).MissedTicks == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the deferred tick was never counted as missed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	parts.mu.Lock()
	asked := slices.Clone(parts.asked)
	parts.mu.Unlock()
	want := fmt.Sprintf("apps/pu main user:alice %d", proxy.StartBackground)
	if len(asked) != 2 || asked[0] != want || asked[1] != want {
		t.Errorf("the runner was asked %q, want twice %q", asked, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(answers, []int{http.StatusServiceUnavailable, http.StatusServiceUnavailable}) {
		t.Errorf("the proxy answered %v", answers)
	}
}

// covers 01§2.2 03§C — a person's partition vault leaves nothing behind:
// the last key deleted, or the partition dropped, removes the file and the
// levels it emptied, so the tile no longer "holds data"; a plain vault
// (--insecure-vault) is sealed when the barrier is first initialized,
// keeping whose it is.
func TestPartitionVaultLeftovers(t *testing.T) {
	w := partRegWS(t)
	b := w.b
	term := partTerm("apps/pg", "alice")
	put := func(key, value string) {
		t.Helper()
		if rec := call(t, b.apiVaultPut, term, "PUT", "/vault/apps/pg/"+key, `{"value":"`+value+`"}`, map[string]string{"rest": "apps/pg/" + key}); rec.Code != 200 {
			t.Fatalf("put %s: %d %s", key, rec.Code, rec.Body)
		}
	}
	ask := registry.PartitionAsk{Tile: "apps/pg", Scope: "apps/pg", RootsScope: true}
	holds := func() bool {
		held, err := holdsPartitionVault(b, ask)
		if err != nil {
			t.Fatal(err)
		}
		return held
	}
	put("k", "v")
	if !holds() {
		t.Fatal("a partition vault with a key doesn't hold data")
	}
	if rec := call(t, b.apiVaultDelete, term, "DELETE", "/vault/apps/pg/k", "", map[string]string{"rest": "apps/pg/k"}); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if holds() || exists(b.partVaultDir("apps/pg")) {
		t.Errorf("the last key deleted: holds %v, the tile's vault level left %v", holds(), exists(b.partVaultDir("apps/pg")))
	}
	put("k", "v")
	if err := b.DropPartition("apps/pg", "main", w.pkeyOf("alice")); err != nil {
		t.Fatal(err)
	}
	if holds() || exists(b.partVaultDir("apps/pg")) {
		t.Errorf("the partition dropped: holds %v, the tile's vault level left %v", holds(), exists(b.partVaultDir("apps/pg")))
	}
	if held, err := holdsPartitionRecords(b, ask); held || err != nil {
		t.Errorf("the partition dropped: records hold %v, %v", held, err)
	}
	// an empty level (an older build's leftover) holds nothing either
	if err := os.MkdirAll(filepath.Join(b.partVaultDir("apps/pg"), "main"), 0o700); err != nil {
		t.Fatal(err)
	}
	if holds() {
		t.Error("an empty deployment level holds data")
	}

	// sealed at the barrier's first init
	put("secret", "hunter2")
	file, _ := b.partVaultPath("apps/pg", "main", w.pkeyOf("alice"))
	if raw := readFile(t, file); !strings.Contains(string(raw), "hunter2") {
		t.Fatalf("an --insecure-vault partition vault isn't plain: %s", raw)
	}
	initBarrier(t, b)
	raw := readFile(t, file)
	var doc partVaultDoc
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Enc != 1 || doc.Plain != nil || strings.Contains(string(raw), "hunter2") ||
		doc.Tile != "apps/pg" || doc.Deployment != "main" || doc.Partition != "user:alice" {
		t.Fatalf("the partition vault after the barrier's init: %s", raw)
	}
	rec := call(t, b.apiVaultGet, partInst("apps/pg", "alice"), "GET", "/vault/apps/pg/secret", "", map[string]string{"rest": "apps/pg/secret"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hunter2") {
		t.Errorf("alice's backend reads the sealed value: %d %s", rec.Code, rec.Body)
	}
}

// covers 03§D PD-20 — editing one of a partition's jobs leaves the others
// as they were: the same schedule entry and missed-tick count.
func TestPartitionCronEditKeeps(t *testing.T) {
	w := partRegWS(t)
	b := w.b
	var calls cronCalls
	calls.install(b)
	alice := partInst("apps/pu", "alice")
	put := func(name, sched string) {
		t.Helper()
		if rec := call(t, b.apiCronPut, alice, "PUT", "/cron/jobs", `{"name":"`+name+`","resource":"res:apps/pu/beat","schedule":"`+sched+`","path":"/t"}`, nil); rec.Code != 200 {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	put("a", "@every 5m")
	put("b", "@every 5m")
	pk := w.pkeyOf("alice")
	key := partPrefix("apps/pu", "main", pk) + "a"
	b.cron.mu.Lock()
	pj := b.cron.part[key]
	pj.missed = 3
	entry := pj.entry
	b.cron.mu.Unlock()
	put("b", "@every 7m") // b changes
	put("c", "@every 5m") // c comes
	b.cron.mu.Lock()
	still := b.cron.part[key]
	b.cron.mu.Unlock()
	if still != pj || still.entry != entry || still.missed != 3 {
		t.Errorf("editing b and adding c rebuilt a: %+v (was entry %d, 3 missed)", still, entry)
	}
	if c := b.PartitionRegistrations("apps/pu", "main", pk); c.CronJobs != 3 || c.MissedTicks != 3 {
		t.Errorf("counts: %+v", c)
	}
	if n := len(b.cron.sched.Entries()); n != 3 {
		t.Errorf("%d schedule entries, want 3", n)
	}
}

// covers 01§2.3 01§2.5 03§D — while a tile is paused (pending) a person's
// partition's registrations and vault can't change (409), so nothing
// written during a switch's wipe brings the partition back.
func TestPartitionPausedRefusesWrites(t *testing.T) {
	w := partRegWS(t)
	b := w.b
	var calls cronCalls
	calls.install(b)
	fakeBusDispatch(b, nil)
	alice := partInst("apps/pu", "alice")
	if rec := call(t, b.apiCronPut, alice, "PUT", "/cron/jobs", `{"name":"j","resource":"res:apps/pu/beat","schedule":"@every 1m","path":"/t"}`, nil); rec.Code != 200 {
		t.Fatalf("alice's job: %d %s", rec.Code, rec.Body)
	}
	manifest := string(readFile(t, filepath.Join(w.root, "apps/pu/xbin.json")))
	w.write(map[string]string{"apps/pu/xbin.json": strings.Replace(manifest, `"partition":["user"],`, "", 1)})
	w.rescan()
	if st, _, _ := w.state("apps/pu"); st != registry.PartitionPending {
		t.Fatalf("apps/pu is %s, want pending", st)
	}
	for _, c := range []struct {
		h                  http.HandlerFunc
		method, url, body  string
		pathKey, pathValue string
	}{
		{b.apiCronPut, "PUT", "/cron/jobs", `{"name":"j2","resource":"res:apps/pu/beat","schedule":"@every 1m","path":"/t"}`, "", ""},
		{b.apiCronDelete, "DELETE", "/cron/jobs/j", "", "name", "j"},
		{b.apiBusSubsPut, "PUT", "/bus/subscriptions", `{"name":"s","resource":"res:apps/pu/feed","path":"/b"}`, "", ""},
		{b.apiVaultPut, "PUT", "/vault/apps/pu/k", `{"value":"v"}`, "rest", "apps/pu/k"},
		{b.apiIfaceInstancesSet, "PUT", "/iface-instances", `{"instances":{"a":"/m"}}`, "", ""},
	} {
		pv := map[string]string{}
		if c.pathKey != "" {
			pv[c.pathKey] = c.pathValue
		}
		if rec := call(t, c.h, alice, c.method, c.url, c.body, pv); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "can't change meanwhile") {
			t.Errorf("%s %s while pending: %d %s", c.method, c.url, rec.Code, rec.Body)
		}
	}
	if data := readFile(t, filepath.Join(w.partDir("apps/pu", "alice"), depCronFile)); !strings.Contains(string(data), `"j"`) || strings.Contains(string(data), `"j2"`) {
		t.Errorf("alice's jobs changed while pending: %s", data)
	}
}

// covers PD-24 03§E — a person's partition's crash metadata survives a
// restart of xbind in its record: each exit the runner's crash watch saw
// (lastExit, restarts) and its breaker (crashLoop), which a start clears;
// an exit never makes a record.
func TestPartitionExitRecorded(t *testing.T) {
	w := partRegWS(t)
	b := w.b
	alice := partInst("apps/pu", "alice")
	if rec := call(t, b.apiCronPut, alice, "PUT", "/cron/jobs", `{"name":"j","resource":"res:apps/pu/beat","schedule":"@every 1m","path":"/t"}`, nil); rec.Code != 200 {
		t.Fatalf("alice's job: %d %s", rec.Code, rec.Body)
	}
	pk := w.pkeyOf("alice")
	dir := w.partDir("apps/pu", "alice")
	b.NotePartitionExit("apps/pu", "main", "user:alice", pk, false)
	b.NotePartitionExit("apps/pu", "main", "user:alice", pk, true)
	r, _, _ := readPartitionRecordAt(dir)
	if r.Restarts != 2 || !r.CrashLoop || r.LastExit == "" {
		t.Errorf("after two exits: %+v", r)
	}
	u, _ := b.Users.Get("alice")
	b.notePartitionStart("apps/pu", "main", "user:alice", pk, u.UID)
	if r, _, _ := readPartitionRecordAt(dir); r.CrashLoop || r.Restarts != 2 || r.LastStarted == "" {
		t.Errorf("after a start: %+v", r)
	}
	bob, _ := b.mintPartitionUID("bob")
	b.NotePartitionExit("apps/pu", "main", "user:bob", util.PartitionKey("bob", bob), false)
	if exists(w.partDir("apps/pu", "bob")) {
		t.Error("an exit made a partition record")
	}
}
