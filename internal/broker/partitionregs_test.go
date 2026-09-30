package broker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// partRegWS is the routing fixture (partRouteWS: the real identity plane)
// with registrations to make: apps/pu (people's partitions only) and apps/pg
// (people's and global) each declare a cron resource (beat), a bus of their
// own (feed) and a shared one (wall), and provide an http interface with
// instances; apps/x (unpartitioned) keeps a bus too.
func partRegWS(t *testing.T) *partWS {
	t.Helper()
	w := partRouteWS(t)
	scope := `{"resources":{"beat":{"type":"cron"},"feed":{"type":"bus"},"wall":{"type":"bus","shared":true}}}`
	uses := func(tile string) string {
		return `"uses":[{"target":"res:` + tile + `/beat","role":"writer"},{"target":"res:` + tile + `/feed","role":"writer"},` +
			`{"target":"res:` + tile + `/wall","role":"writer"}],"provides":{"mcp":{"kind":"http","instances":true}}`
	}
	w.write(map[string]string{
		"apps/pu/scope.json": scope,
		"apps/pu/xbin.json":  `{"runtime":"go","partition":["user"],` + uses("apps/pu") + `}`,
		"apps/pg/scope.json": scope,
		"apps/pg/xbin.json":  `{"runtime":"go","partition":["user","global"],` + uses("apps/pg") + `}`,
	})
	w.rescan()
	for _, tile := range []string{"apps/pu", "apps/pg"} {
		if st, _, _ := w.state(tile); st != registry.PartitionPartitioned {
			t.Fatalf("%s: %v, want partitioned", tile, st)
		}
	}
	return w
}

// a user partition's principals as the partition gate hands them over
func partInst(tile, user string) auth.Principal {
	return auth.Principal{Component: tile, Via: "instance", Partition: util.UserPartition(user)}
}

func partTerm(tile, user string) auth.Principal {
	return auth.Principal{Component: tile, UserID: user, Via: "terminal", Partition: util.UserPartition(user)}
}

// partDir is user's partition directory of tile (main).
func (w *partWS) partDir(tile, user string) string {
	return filepath.Join(w.root, "data", "partitions", util.TileKey(tile), "main", w.pkeyOf(user))
}

// covers PD-07 S19 C22 03§C — a person's partition's vault: alice's
// terminal writes alice's file (never the global vault), her backend alone
// reads its values, another person's partition sees none of her keys; an
// admin reaches only the global vault, `?partition=` is refused on a
// partitioned tile for everyone and ignored elsewhere; GET /vaults counts
// people's vaults, never their key names.
func TestPartitionVault(t *testing.T) {
	w := partRegWS(t)
	b := w.b
	vault := func(p auth.Principal, method, url, body string) (int, string) {
		t.Helper()
		rest := strings.TrimPrefix(url, "/vault/")
		rest, _, _ = strings.Cut(rest, "?")
		rec := call(t, map[string]http.HandlerFunc{"GET": b.apiVaultGet, "PUT": b.apiVaultPut, "DELETE": b.apiVaultDelete}[method],
			p, method, url, body, map[string]string{"rest": rest})
		return rec.Code, rec.Body.String()
	}
	if code, body := vault(partTerm("apps/pg", "alice"), "PUT", "/vault/apps/pg/token", `{"value":"alice-secret"}`); code != 200 {
		t.Fatalf("alice's terminal writes her vault: %d %s", code, body)
	}
	pk := w.pkeyOf("alice")
	file := filepath.Join(w.root, "data", "vault", ".partitions", util.TileKey("apps/pg"), "main", pk+".json")
	if !exists(file) || exists(b.vaultPath("apps/pg")) {
		t.Fatalf("alice's key: her file %v, the global vault %v", exists(file), exists(b.vaultPath("apps/pg")))
	}
	if !exists(filepath.Join(w.partDir("apps/pg", "alice"), partRecordFile)) {
		t.Error("alice's partition has no record after her first vault key")
	}
	if code, body := vault(partInst("apps/pg", "alice"), "GET", "/vault/apps/pg/token", ""); code != 200 || !strings.Contains(body, "alice-secret") {
		t.Errorf("alice's backend reads her value: %d %s", code, body)
	}
	if code, _ := vault(partTerm("apps/pg", "alice"), "GET", "/vault/apps/pg/token", ""); code != 403 {
		t.Errorf("alice's terminal read a value: %d", code)
	}
	if code, body := vault(partInst("apps/pg", "bob"), "GET", "/vault/apps/pg", ""); code != 200 || strings.Contains(body, "token") {
		t.Errorf("bob's partition lists: %d %s", code, body)
	}
	if code, _ := vault(partInst("apps/pg", "bob"), "GET", "/vault/apps/pg/token", ""); code != 404 {
		t.Errorf("bob's partition reads alice's key: %d", code)
	}
	// the global instance and an admin: the global vault only
	if code, body := vault(instanceOf("apps/pg", ""), "PUT", "/vault/apps/pg/g", `{"value":"global"}`); code != 200 {
		t.Fatalf("global writes: %d %s", code, body)
	}
	admin := personP(t, w, "bob")
	if code, body := vault(admin, "GET", "/vault/apps/pg", ""); code != 200 || strings.Contains(body, "token") || !strings.Contains(body, `"g"`) {
		t.Errorf("an admin lists: %d %s", code, body)
	}
	for _, p := range []auth.Principal{admin, partInst("apps/pg", "alice"), instanceOf("apps/pg", "")} {
		if code, body := vault(p, "GET", "/vault/apps/pg?partition=user:alice", ""); code != 400 || !strings.Contains(body, "only from inside it") {
			t.Errorf("?partition= on a partitioned tile by %s/%s: %d %s", p.Component, p.UserID, code, body)
		}
	}
	if code, body := vault(admin, "GET", "/vault/apps/x?partition=user:alice", ""); code != 200 {
		t.Errorf("?partition= on an unpartitioned tile is ignored: %d %s", code, body)
	}
	rec := call(t, b.apiVaults, admin, "GET", "/vaults", "", nil)
	var rows []struct {
		Component  string   `json:"component"`
		Keys       []string `json:"keys"`
		Partitions int      `json:"partitions"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &rows)
	i := slices.IndexFunc(rows, func(r struct {
		Component  string   `json:"component"`
		Keys       []string `json:"keys"`
		Partitions int      `json:"partitions"`
	}) bool {
		return r.Component == "apps/pg"
	})
	if i < 0 || rows[i].Partitions != 1 || slices.Contains(rows[i].Keys, "token") || strings.Contains(rec.Body.String(), "token") {
		t.Errorf("GET /vaults: %s", rec.Body)
	}
	// the last key gone, so is the file
	if code, _ := vault(partTerm("apps/pg", "alice"), "DELETE", "/vault/apps/pg/token", ""); code != 200 || exists(file) {
		t.Errorf("deleting alice's last key: %d, file %v", code, exists(file))
	}
}

// cronCalls records the cron dispatch's deliveries; reply answers each.
type cronCalls struct {
	mu    sync.Mutex
	got   []auth.Principal
	reply func(n int) (int, string)
}

func (c *cronCalls) install(b *Broker) {
	b.SetDispatch(func(p auth.Principal, comp, path string) (int, string) {
		c.mu.Lock()
		c.got = append(c.got, p)
		n := len(c.got)
		reply := c.reply
		c.mu.Unlock()
		if reply != nil {
			return reply(n)
		}
		return 200, "ok"
	})
}

func (c *cronCalls) take() []auth.Principal {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.got
	c.got = nil
	return out
}

// covers PD-20 C5 S15 03§D — a person's partition's cron jobs: stored in
// its own file (never data/cron-jobs.json), listed only to it, capped at 16
// and once a minute; a tick fires into the partition (the delivery
// principal carries it) while its person is live — not while disabled,
// again once re-enabled; a deferred start is retried with jitter until the
// next tick, and counted when it stays deferred; a restart loads it again.
func TestPartitionCron(t *testing.T) {
	w := partRegWS(t)
	b := w.b
	var calls cronCalls
	calls.install(b)
	alice := partInst("apps/pu", "alice")
	put := func(p auth.Principal, name, sched string) (int, string) {
		t.Helper()
		rec := call(t, b.apiCronPut, p, "PUT", "/cron/jobs",
			`{"name":"`+name+`","resource":"res:apps/pu/beat","schedule":"`+sched+`","path":"/tick"}`, nil)
		return rec.Code, rec.Body.String()
	}
	if code, body := put(alice, "j0", "@every 1m"); code != 200 {
		t.Fatalf("alice's job: %d %s", code, body)
	}
	if !exists(filepath.Join(w.partDir("apps/pu", "alice"), depCronFile)) {
		t.Fatal("alice's job isn't in her partition's cron.json")
	}
	if data, _ := os.ReadFile(filepath.Join(w.root, "data", "cron-jobs.json")); strings.Contains(string(data), "j0") {
		t.Errorf("a partition's job landed in today's store: %s", data)
	}
	if rows := b.cronJobsFor("apps/pu"); len(rows) != 0 { // a backup of the tile carries global's rows only (S15)
		t.Errorf("the tile's backup would carry a partition's job: %s", rows)
	}
	if code, body := put(alice, "fast", "@every 30s"); code != 400 || !strings.Contains(body, "once a minute") {
		t.Errorf("a job more often than once a minute: %d %s", code, body)
	}
	for i := 1; i < partCronCap; i++ {
		if code, body := put(alice, "j"+string(rune('a'+i)), "@every 5m"); code != 200 {
			t.Fatalf("job %d: %d %s", i, code, body)
		}
	}
	if code, body := put(alice, "one-too-many", "@every 5m"); code != 409 || !strings.Contains(body, "at most 16") {
		t.Errorf("the 17th job: %d %s", code, body)
	}
	list := func(p auth.Principal) string {
		return call(t, b.apiCronList, p, "GET", "/cron/jobs", "", nil).Body.String()
	}
	if got := list(alice); !strings.Contains(got, `"j0"`) {
		t.Errorf("alice's list: %s", got)
	}
	if got := list(partInst("apps/pu", "bob")); strings.Contains(got, `"j0"`) {
		t.Errorf("bob's partition lists alice's jobs: %s", got)
	}
	if got := list(personP(t, w, "bob")); strings.Contains(got, `"j0"`) {
		t.Errorf("an admin lists a person's partition's jobs: %s", got)
	}
	if rec := call(t, b.apiCronList, personP(t, w, "bob"), "GET", "/cron/jobs?component=apps/pu&partition=user:alice", "", nil); rec.Code != 400 {
		t.Errorf("?partition= on a partitioned tile's jobs: %d %s", rec.Code, rec.Body)
	}
	for n := 1; n < partCronCap; n++ { // keep one job: easier to count
		if rec := call(t, b.apiCronDelete, alice, "DELETE", "/cron/jobs/j"+string(rune('a'+n)), "", map[string]string{"name": "j" + string(rune('a'+n))}); rec.Code != 200 {
			t.Fatalf("delete: %d %s", rec.Code, rec.Body)
		}
	}

	tickAll(b)
	got := calls.take()
	if len(got) != 1 || got[0].Component != CronPrincipal || got[0].Partition != "user:alice" || got[0].Role != "writer" {
		t.Fatalf("the tick: %+v", got)
	}
	// disabled: no tick; enabled again: it fires, without registering again
	setDisabled := func(on bool) {
		u, _ := b.Users.Get("alice")
		u.Disabled = on
		if _, err := b.Users.Upsert(*u, ""); err != nil {
			t.Fatal(err)
		}
	}
	setDisabled(true)
	tickAll(b)
	if got := calls.take(); len(got) != 0 {
		t.Errorf("a disabled person's job fired: %+v", got)
	}
	setDisabled(false)
	tickAll(b)
	if got := calls.take(); len(got) != 1 {
		t.Errorf("re-enabled: %d ticks, want 1", len(got))
	}

	// deferred: retried with jitter within the period
	withSeam(t, &cronRetryDelay, func(left time.Duration) time.Duration { return 10 * time.Millisecond })
	deferred := 503
	calls.reply = func(n int) (int, string) {
		if n == 1 {
			return deferred, `{"error":"` + runner.ErrPartitionDeferred.Error() + `: 4 background starts"}`
		}
		return 200, "ok"
	}
	tickAll(b)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		calls.mu.Lock()
		n := len(calls.got)
		calls.mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond) // the retry's AfterFunc
	}
	if got := calls.take(); len(got) != 2 {
		t.Errorf("a deferred tick: %d deliveries, want 2 (the retry)", len(got))
	}
	// deferred until the next tick: missed, counted
	withSeam(t, &cronRetryDelay, func(time.Duration) time.Duration { return 0 })
	calls.reply = func(int) (int, string) {
		return 503, `{"error":"` + runner.ErrPartitionDeferred.Error() + `"}`
	}
	tickAll(b)
	calls.take()
	if c := b.PartitionRegistrations("apps/pu", "main", w.pkeyOf("alice")); c.MissedTicks != 1 || c.CronJobs != 1 {
		t.Errorf("counts: %+v", c)
	}
	calls.reply = nil

	// a restart loads the partition's job from its file: the broker's rows
	// forgotten, boot's load (SetDispatch) brings them back
	if n := b.dropPartRows("apps/pu", "main", w.pkeyOf("alice")); n != 1 {
		t.Fatalf("dropped %d rows, want 1", n)
	}
	calls.install(b)
	tickAll(b)
	if got := calls.take(); len(got) != 1 || got[0].Partition != "user:alice" {
		t.Errorf("after a restart: %+v", got)
	}
}

// covers PD-20 03§D 04§2 — a person's partition's bus subscriptions: an
// event of their own partition's bus reaches only their subscription (it
// may start it); the global instance's reaches only today's; a shared
// bus's reaches a partition only while it runs (a skipped one counted as
// dormantDrops); 16 at most.
func TestPartitionBusSubs(t *testing.T) {
	w := partRegWS(t)
	b := w.b
	deliveries := fakeBusDispatch(b, nil)
	sub := func(p auth.Principal, name, res string) (int, string) {
		t.Helper()
		rec := call(t, b.apiBusSubsPut, p, "PUT", "/bus/subscriptions", `{"name":"`+name+`","resource":"`+res+`","path":"/bus"}`, nil)
		return rec.Code, rec.Body.String()
	}
	alice, carol := partInst("apps/pg", "alice"), partInst("apps/pg", "bob")
	global := instanceOf("apps/pg", "")
	for _, p := range []auth.Principal{alice, carol, global} {
		for _, res := range []string{"feed", "wall"} {
			if code, body := sub(p, res, "res:apps/pg/"+res); code != 200 {
				t.Fatalf("%s subscribes to %s: %d %s", p.Partition, res, code, body)
			}
		}
	}
	if data, _ := os.ReadFile(filepath.Join(w.root, "data", "bus-subscriptions.json")); strings.Count(string(data), `"name"`) != 2 {
		t.Errorf("today's store holds more than global's two: %s", data)
	}
	if subs := b.bus.forComponent("apps/pg"); len(subs) != 2 { // a backup of the tile carries global's rows only (S15)
		t.Errorf("the tile's backup would carry %d subscriptions, want global's 2", len(subs))
	}
	publish := func(p auth.Principal, res string) {
		t.Helper()
		rec := call(t, b.apiBusPublish, p, "POST", "/bus/publish", `{"resource":"res:apps/pg/`+res+`","topic":"t","data":1}`, nil)
		if rec.Code != 200 {
			t.Fatalf("%s publishes on %s: %d %s", p.Partition, res, rec.Code, rec.Body)
		}
	}
	wait := func(n int) []busCall {
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
	publish(alice, "feed")
	if got := wait(1); got[0].p.Partition != "user:alice" || got[0].p.Component != BusPrincipal || got[0].comp != "apps/pg" {
		t.Errorf("alice's event: %+v", got)
	}
	publish(global, "feed")
	if got := wait(1); got[0].p.Partition != "" {
		t.Errorf("global's event reached a person's partition: %+v", got)
	}
	// a shared bus: people's partitions only while they run
	running := map[string]bool{}
	var mu sync.Mutex
	b.SetPartitionRunner(func(scope, pkey string) bool { mu.Lock(); defer mu.Unlock(); return running[pkey] }, nil, nil)
	publish(global, "wall")
	if got := wait(1); got[0].p.Partition != "" {
		t.Errorf("a shared event with no partition running: %+v", got)
	}
	mu.Lock()
	running[w.pkeyOf("alice")] = true
	mu.Unlock()
	publish(global, "wall")
	got := wait(2)
	parts := []string{string(got[0].p.Partition), string(got[1].p.Partition)}
	slices.Sort(parts)
	if parts[0] != "" || parts[1] != "user:alice" {
		t.Errorf("a shared event with alice's running: %q", parts)
	}
	if c := b.PartitionRegistrations("apps/pg", "main", w.pkeyOf("bob")); c.DormantDrops != 2 || c.BusSubscriptions != 2 {
		t.Errorf("bob's partition's counts: %+v", c)
	}
	for i := 2; i < partBusCap; i++ {
		if code, body := sub(alice, "s"+string(rune('a'+i)), "res:apps/pg/feed"); code != 200 {
			t.Fatalf("subscription %d: %d %s", i, code, body)
		}
	}
	if code, body := sub(alice, "one-too-many", "res:apps/pg/feed"); code != 409 || !strings.Contains(body, "at most 16") {
		t.Errorf("the 17th subscription: %d %s", code, body)
	}
	if rec := call(t, b.apiBusSubsList, alice, "GET", "/bus/subscriptions", "", nil); strings.Count(rec.Body.String(), `"name"`) != partBusCap {
		t.Errorf("alice's list: %s", rec.Body)
	}
}

// covers PD-21 — a person's partition's interface instances and ingress
// hosts are stored for it and answered with success, dormant: they never
// reach the workspace's maps, and its ingress routes are none.
func TestPartitionDormantRoutes(t *testing.T) {
	w := partRegWS(t)
	b := w.b
	alice := partInst("apps/pu", "alice")
	rec := call(t, b.apiIfaceInstancesSet, alice, "PUT", "/iface-instances", `{"instances":{"a":"/m/1"}}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"dormant":true`) {
		t.Fatalf("alice's interface instances: %d %s", rec.Code, rec.Body)
	}
	if len(b.Reg.Workspace().IfaceInstances["apps/pu"]) != 0 {
		t.Error("a partition's interface instance reached the workspace's map")
	}
	if !exists(filepath.Join(w.partDir("apps/pu", "alice"), depIfaceFile)) {
		t.Error("alice's interface instances aren't in her partition's file")
	}
	if c := b.PartitionRegistrations("apps/pu", "main", w.pkeyOf("alice")); c.IfaceInstances != 1 {
		t.Errorf("counts: %+v", c)
	}
	if hidden, err := b.routesHidden(alice); !hidden || err != nil {
		t.Errorf("a partition's ingress routes: hidden %v, %v", hidden, err)
	}
}

// covers PD-44 01§2.2 01§2.6 03§Tests(TestWipeHooks) — a person's
// partition's vault, registrations and record make the tile hold data, a
// dry run counts them, and a switch to unpartitioned leaves no trace of
// them in any store of these planes: files, the broker's rows, the vault.
func TestWipeHooksPartitionStores(t *testing.T) {
	w := partRegWS(t)
	b := w.b
	withSeam(t, &partitionIsolated, func() bool { return true })
	var calls cronCalls
	calls.install(b)
	fakeBusDispatch(b, nil)
	alice := partInst("apps/pu", "alice")
	for _, c := range []struct {
		h          http.HandlerFunc
		url, body  string
		path, name string
	}{
		{b.apiVaultPut, "/vault/apps/pu/k", `{"value":"v"}`, "rest", "apps/pu/k"},
		{b.apiCronPut, "/cron/jobs", `{"name":"j","resource":"res:apps/pu/beat","schedule":"@every 1m","path":"/t"}`, "", ""},
		{b.apiBusSubsPut, "/bus/subscriptions", `{"name":"s","resource":"res:apps/pu/feed","path":"/b"}`, "", ""},
		{b.apiIfaceInstancesSet, "/iface-instances", `{"instances":{"a":"/m"}}`, "", ""},
	} {
		pv := map[string]string{}
		if c.path != "" {
			pv[c.path] = c.name
		}
		if rec := call(t, c.h, alice, "PUT", c.url, c.body, pv); rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.url, rec.Code, rec.Body)
		}
	}
	ask := registry.PartitionAsk{Tile: "apps/pu", Scope: "apps/pu", RootsScope: true}
	mine := []string{"partition-vault", "partition-registrations", "partition-records"}
	for _, s := range partitionStores {
		if slices.Contains(mine, s.name) {
			if held, err := s.holds(b, ask); !held || err != nil {
				t.Errorf("the %s store: held %v, %v", s.name, held, err)
			}
		}
	}
	w.write(map[string]string{"apps/pu/xbin.json": strings.Replace(string(readFile(t, filepath.Join(w.root, "apps/pu/xbin.json"))), `"partition":["user"],`, "", 1)})
	w.rescan()
	if st, _, _ := w.state("apps/pu"); st != registry.PartitionPending {
		t.Fatalf("apps/pu is %s, want pending (it holds data)", st)
	}
	act := func(body string) string {
		t.Helper()
		rec := call(t, b.apiPartitionMode, auth.Principal{Owner: true}, "POST", "/partitions/mode", body, nil)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	dry := act(`{"tile":"apps/pu","act":"switch","from":{"user":true},"to":null,"dryRun":true}`)
	if !strings.Contains(dry, `"vaultKeys":1`) || !strings.Contains(dry, `"registrations":3`) || !exists(w.partDir("apps/pu", "alice")) {
		t.Errorf("the dry run: %s", dry)
	}
	act(`{"tile":"apps/pu","act":"switch","from":{"user":true},"to":null,"confirm":"apps/pu"}`)
	for _, s := range partitionStores {
		if slices.Contains(mine, s.name) {
			if held, err := s.holds(b, ask); held || err != nil {
				t.Errorf("after the switch the %s store holds data: %v", s.name, err)
			}
		}
	}
	if exists(w.partDir("apps/pu", "alice")) || exists(filepath.Join(w.root, "data", "vault", ".partitions", util.TileKey("apps/pu"))) {
		t.Error("alice's partition directory or vault survived the switch")
	}
	if !exists(filepath.Join(w.root, "data", "partitions", util.TileKey("apps/pu"), "mode.json")) {
		t.Error("the switch's wipe removed the mode record")
	}
	b.cron.mu.Lock()
	jobs := len(b.cron.part)
	b.cron.mu.Unlock()
	b.bus.mu.Lock()
	subs := len(b.bus.part)
	b.bus.mu.Unlock()
	if jobs+subs != 0 {
		t.Errorf("the broker still holds %d job(s) and %d subscription(s) of people's partitions", jobs, subs)
	}
}

// covers PD-43 PD-26 03§E — identity records across delete and recreate:
// an older xbind's rewrite of the users store drops alice's uid, and she
// adopts it back from her partition's record (never a new one), so her job
// fires again; deleted and made again, the new alice gets a new uid, the
// old partition is orphaned by the sweep and its job never fires as hers;
// a missing partition.json is rebuilt from the namespace's ns.json.
func TestPartitionRecordsReadopt(t *testing.T) {
	w := partRegWS(t)
	b := w.b
	var calls cronCalls
	calls.install(b)
	alice := partInst("apps/pu", "alice")
	rec := call(t, b.apiCronPut, alice, "PUT", "/cron/jobs", `{"name":"j","resource":"res:apps/pu/beat","schedule":"@every 1m","path":"/t"}`, nil)
	if rec.Code != 200 {
		t.Fatalf("alice's job: %d %s", rec.Code, rec.Body)
	}
	u, _ := b.Users.Get("alice")
	uid1, created := u.UID, u.Created
	dir := w.partDir("apps/pu", "alice")
	// the record was made after alice's record (by a second at least)
	setCreated := func(at time.Time) {
		r, ok, err := readPartitionRecordAt(dir)
		if !ok || err != nil {
			t.Fatalf("alice's record: %v %v", ok, err)
		}
		r.Created = at.UTC().Format(time.RFC3339Nano)
		if err := writePartitionRecordAt(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	setCreated(time.Unix(created, 0).Add(5 * time.Second))
	// an older xbind rewrites users.json without the uid
	dropUID := func() {
		p := filepath.Join(w.root, "data", "users.json")
		var doc map[string]any
		if err := json.Unmarshal(readFile(t, p), &doc); err != nil {
			t.Fatal(err)
		}
		for _, x := range doc["users"].([]any) {
			delete(x.(map[string]any), "uid")
		}
		data, _ := json.Marshal(doc)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := users.Open(filepath.Join(w.root, "data"))
		if err != nil {
			t.Fatal(err)
		}
		b.Users = st
	}
	dropUID()
	// the next boot's load of the registrations adopts it back at once, so
	// her job fires before she makes any request; a person without
	// partition records gets no uid at boot
	calls.install(b)
	if u, _ := b.Users.Get("alice"); u.UID != uid1 {
		t.Fatalf("re-adoption at boot: %q, want %q", u.UID, uid1)
	}
	if u, _ := b.Users.Get("carol"); u.UID != "" {
		t.Errorf("boot minted a uid for carol, who has no partition: %q", u.UID)
	}
	tickAll(b)
	if got := calls.take(); len(got) != 1 {
		t.Errorf("after re-adoption: %d ticks, want 1", len(got))
	}
	// the request path's minting answers the same
	if uid, err := b.mintPartitionUID("alice"); err != nil || uid != uid1 {
		t.Fatalf("re-adoption: %q %v, want %q", uid, err, uid1)
	}

	// deleted and made again: a new incarnation
	setCreated(time.Unix(created, 0).Add(-time.Hour))
	if _, err := b.Users.Delete("alice"); err != nil {
		t.Fatal(err)
	}
	b.PartitionUserDeleted("alice", uid1)
	if r, _, _ := readPartitionRecordAt(dir); r.State != partStateOrphaned || r.Reason != orphanUserDeleted {
		t.Errorf("the deleted person's record: %+v", r)
	}
	if _, err := b.Users.Upsert(users.User{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/*": users.LevelRead}}, "password1"); err != nil {
		t.Fatal(err)
	}
	if uid, err := b.mintPartitionUID("alice"); err != nil || uid == uid1 || uid == "" {
		t.Errorf("the new alice's uid: %q %v (the old one was %q)", uid, err, uid1)
	}
	tickAll(b)
	if got := calls.take(); len(got) != 0 {
		t.Errorf("the old alice's job fired for the new one: %+v", got)
	}
	// past the retention the sweep deletes the old partition, rows and all
	withSeam(t, &partitionRetention, time.Duration(0))
	b.sweepPartitionRecords(time.Now().Add(time.Minute))
	if exists(dir) {
		t.Error("the orphan survived the sweep past the retention")
	}
	b.cron.mu.Lock()
	n := len(b.cron.part)
	b.cron.mu.Unlock()
	if n != 0 {
		t.Errorf("the broker still holds %d job(s) of the swept partition", n)
	}

	// rebuilt from ns.json
	pk := w.pkeyOf("alice")
	u2, _ := b.Users.Get("alice")
	if err := b.notePartitionNS(partNS("apps/pg", "main", pk), nsPartition{User: "alice", UID: u2.UID}); err != nil {
		t.Fatal(err)
	}
	b.sweepPartitionRecords(time.Now())
	r, ok, err := readPartitionRecordAt(w.partDir("apps/pg", "alice"))
	if !ok || err != nil || !r.Rebuilt || r.UID != u2.UID || r.Tile != "apps/pg" {
		t.Errorf("the rebuilt record: %+v %v %v", r, ok, err)
	}
}
