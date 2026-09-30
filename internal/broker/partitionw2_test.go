package broker

// partitionw2_test.go — the seams wave 2's integration wired between the
// packs (plans/partitions/records/W2-wire.md).

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// crossTileBusWS is partRegWS with apps/q (["user"]) granted apps/pg's
// per-person feed and its shared wall.
func crossTileBusWS(t *testing.T) *partWS {
	t.Helper()
	w := partRegWS(t)
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
	return w
}

// edgeCounts records what the one ledger seam is told.
type edgeCounts struct {
	mu   sync.Mutex
	rows []string
}

func (e *edgeCounts) install(t *testing.T) {
	withSeam(t, &partitionEdgeSeam, func(_ *Broker, user, from, to string) {
		e.mu.Lock()
		e.rows = append(e.rows, user+" "+from+"→"+to)
		e.mu.Unlock()
	})
}

func (e *edgeCounts) take() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.rows
	e.rows = nil
	return out
}

// covers PD-13 PD-20 05§1 05§2 03§D — a shared bus of another partitioned
// tile is no person's data (F10's rule for shared resources): a person's
// partition subscribes to it and gets its events on the tile's grant and
// its person's read access alone — with partitionConsent on and no
// consent too — while its per-person bus still needs the consent. Its
// unstamped events reach the subscription only while the partition runs
// (F5's rule), and a person who loses read gets nothing more: skipped at
// publish, refused at delivery. The ledger counts a reach of another
// tile's per-person bus (registration and each delivery), never a shared
// one's.
func TestPartitionBusSubsSharedReach(t *testing.T) {
	w := crossTileBusWS(t)
	b := w.b
	var edges edgeCounts
	edges.install(t)
	deliveries := fakeBusDispatch(b, nil)
	setRunning := partRunning(b)
	sub := func(p auth.Principal, name, res string) (int, string) {
		t.Helper()
		rec := call(t, b.apiBusSubsPut, p, "PUT", "/bus/subscriptions", `{"name":"`+name+`","resource":"res:apps/pg/`+res+`","path":"/bus"}`, nil)
		return rec.Code, rec.Body.String()
	}
	publish := func(p auth.Principal, res string) {
		t.Helper()
		if rec := call(t, b.apiBusPublish, p, "POST", "/bus/publish", `{"resource":"res:apps/pg/`+res+`","topic":"t","data":1}`, nil); rec.Code != 200 {
			t.Fatalf("%s publishes on %s: %d %s", p.Component, res, rec.Code, rec.Body)
		}
	}
	carolQ, aliceQ, alicePG, global := partInst("apps/q", "carol"), partInst("apps/q", "alice"), partInst("apps/pg", "alice"), instanceOf("apps/pg", "")

	partRouteConsent(w, true) // consent on, nobody consented
	// carol can't read apps/pg: not its shared bus either
	if code, body := sub(carolQ, "wall", "wall"); code != 403 || !strings.Contains(body, "carol can't read apps/pg") {
		t.Errorf("carol's partition subscribes to apps/pg's shared wall: %d %s", code, body)
	}
	// alice reads apps/pg: the shared wall needs no consent, the feed does
	if code, body := sub(aliceQ, "wall", "wall"); code != 200 {
		t.Fatalf("alice's partition subscribes to apps/pg's shared wall without consent: %d %s", code, body)
	}
	pk := w.pkeyOf("alice")
	if code, body := sub(aliceQ, "feed", "feed"); code != 403 || !strings.Contains(body, "alice hasn't let apps/q use their apps/pg data") {
		t.Errorf("alice's partition subscribes to apps/pg's feed without consent: %d %s", code, body)
	}
	if got := edges.take(); len(got) != 0 {
		t.Errorf("a shared bus's subscription counted in the ledger: %v", got)
	}
	if r := partSubRows(t, b, aliceQ)["wall"]; r.Dormant {
		t.Errorf("the shared wall's row without consent: %+v", r)
	}

	// the global instance's event on the shared wall: unstamped, it reaches
	// alice's partition only while it runs, never starting it
	publish(global, "wall")
	takeBus(t, deliveries, 0)
	if c := b.PartitionRegistrations("apps/q", "main", pk); c.DormantDrops != 1 {
		t.Errorf("a shared event while alice's apps/q partition is stopped: %+v, want one dormant drop", c)
	}
	setRunning("apps/q", pk, true)
	publish(global, "wall")
	if got := takeBus(t, deliveries, 1); got[0].comp != "apps/q" || got[0].p.Partition != "user:alice" {
		t.Errorf("a shared event while alice's partition runs: %+v", got)
	}
	publish(alicePG, "wall") // a person's partition's own publish on a shared bus: unstamped too
	if got := takeBus(t, deliveries, 1); got[0].comp != "apps/q" {
		t.Errorf("alice's apps/pg partition's shared event: %+v", got)
	}
	if got := edges.take(); len(got) != 0 {
		t.Errorf("shared deliveries counted in the ledger: %v", got)
	}

	// alice loses read on apps/pg: the shared wall's row goes dormant, an
	// event is skipped at publish, and one queued before is refused
	setTiles(t, b, "alice", map[string]string{"apps/q": "read"})
	if r := partSubRows(t, b, aliceQ)["wall"]; !r.Dormant {
		t.Errorf("the shared wall's row once alice can't read apps/pg: %+v", r)
	}
	publish(global, "wall")
	takeBus(t, deliveries, 0)
	snap := busSubSnap{sub: partSubRows(t, b, aliceQ)["wall"].busSub, part: "user:alice", pkey: pk}
	if out, why := b.bus.deliverPart(b.bus.dispatch, snap, busDelivery{Subscription: "wall", cold: true}); out != "refused" || !strings.Contains(why, "alice can't read apps/pg") {
		t.Errorf("a queued shared delivery once alice can't read apps/pg: %s %q", out, why)
	}
	setTiles(t, b, "alice", map[string]string{"apps/*": "read", "users/*": "read"})

	// consent given: the per-person feed's registration and each delivery
	// count one edge; the shared wall's still none
	withSeam(t, &partitionConsentHolds, func(_ *Broker, id, from, to string) bool {
		return id == "alice" && from == "apps/q" && to == "apps/pg"
	})
	if code, body := sub(aliceQ, "feed", "feed"); code != 200 {
		t.Fatalf("alice's partition subscribes to apps/pg's feed with consent: %d %s", code, body)
	}
	if got := edges.take(); len(got) != 1 || got[0] != "alice apps/q→apps/pg" {
		t.Errorf("the feed's registration in the ledger: %v", got)
	}
	publish(alicePG, "feed")
	if got := takeBus(t, deliveries, 1); got[0].comp != "apps/q" {
		t.Errorf("alice's feed event with consent: %+v", got)
	}
	publish(global, "wall")
	takeBus(t, deliveries, 1)
	if got := edges.take(); len(got) != 1 || got[0] != "alice apps/q→apps/pg" {
		t.Errorf("one feed delivery and one shared delivery in the ledger: %v", got)
	}
	// alice's own partition of apps/pg subscribing to its own feed: no edge
	if code, body := sub(alicePG, "own", "feed"); code != 200 {
		t.Fatalf("alice's apps/pg partition subscribes to its own feed: %d %s", code, body)
	}
	if got := edges.take(); len(got) != 0 {
		t.Errorf("an own-scope subscription counted in the ledger: %v", got)
	}
}

// covers 01§2.5 PD-20 03§C — a switch holds its tile until the rescan has
// settled the new record. Until then the registry still shows the old mode:
// on a tile that was running (here a switch after "keep") a person's
// partition registration or vault write released early would land in what
// the switch just wiped. The registry's change hooks run in that window
// (the registry still publishes the old mode), so they must see the hold;
// after the answer the tile runs.
func TestPartitionSwitchHoldsUntilSettled(t *testing.T) {
	f := newSwitchFx(t)
	f.pend(docsUserManifest)
	bob := principalFor(t, f.st, "bob")
	if code, out := f.act(bob, keepUser); code != 200 {
		t.Fatalf("keep: %d %v", code, out)
	}
	if why := f.b.PartitionHoldReason("apps/docs"); why != "" {
		t.Fatalf("after keep the tile runs: %q", why)
	}
	var mu sync.Mutex
	var seen []string
	f.b.Reg.OnPartitionChange(func(c *registry.Component, old, new registry.PartitionMode) {
		if c.Path != "apps/docs" {
			return
		}
		why := f.b.PartitionHoldReason("apps/docs")
		err := f.b.partPausedErr(partTarget{tile: "apps/docs", dep: "main", part: "user:dan"})
		mu.Lock()
		seen = append(seen, fmt.Sprintf("%s → %s: hold %q, a partition's write %v", old.State, new.State, why, err))
		mu.Unlock()
	})
	if code, out := f.act(bob, switchUser); code != 200 {
		t.Fatalf("switch: %d %v", code, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("the switch's rescan moved nothing")
	}
	for _, s := range seen {
		if strings.Contains(s, `hold ""`) || strings.Contains(s, "write <nil>") {
			t.Errorf("the switch's settle ran without its hold: %s", s)
		}
	}
	if st, _, _ := f.state("apps/docs"); st != registry.PartitionPartitioned {
		t.Errorf("after the switch: %v", st)
	}
	if why := f.b.PartitionHoldReason("apps/docs"); why != "" {
		t.Errorf("after the switch the hold stays: %q", why)
	}
}

// covers 01§2.6 PD-43 06§6.1 — a switch's wipe runs every data store's hook
// first, then the metadata: consents, ledgers, and the partitions' identity
// records last. A data store that fails stops the switch with its person's
// record, ledger and consent whole (a retry and a re-adoption still find
// them); the retry removes each person's directory whole, the ledger's file
// in it included, and leaves no level behind.
func TestPartitionWipeFailureKeepsMeta(t *testing.T) {
	w := partRegWS(t)
	b := w.b
	alice := partInst("apps/pu", "alice")
	if rec := call(t, b.apiCronPut, alice, "PUT", "/cron/jobs", `{"name":"j","resource":"res:apps/pu/beat","schedule":"@every 1m","path":"/t"}`, nil); rec.Code != 200 {
		t.Fatalf("alice's job: %d %s", rec.Code, rec.Body)
	}
	uid := b.storedPartitionUID("alice")
	dir := w.partDir("apps/pu", "alice")
	partitionEdgeSeam(b, "alice", "apps/pu", "apps/pg")
	b.flushLedgers()
	if _, err := b.editConsents("alice", uid, func(d *consentDoc) bool {
		d.Edges[consentKey("apps/pu", "apps/pg")] = consentEdge{At: time.Now()}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	consented := func() bool {
		d, err := b.consentDocOf(uid)
		_, ok := d.Edges[consentKey("apps/pu", "apps/pg")]
		return err == nil && d != nil && ok
	}
	for _, f := range []string{partRecordFile, ledgerFileName, depCronFile} {
		if !exists(filepath.Join(dir, f)) {
			t.Fatalf("before the switch: no %s in alice's partition directory", f)
		}
	}
	if !consented() {
		t.Fatal("before the switch: no consent")
	}
	old := wipeHooks
	t.Cleanup(func() { wipeHooks = old })
	registerWipeHook(wipeHook{name: "failing", wipe: func(b *Broker, t wipeTarget, sum *wipeSummary) error {
		if t.DryRun {
			return nil
		}
		return errFailingHook
	}})
	tgt := wipeTarget{Tile: "apps/pu", Scope: "apps/pu", RootsScope: true, From: registry.PartitionSpec{User: true},
		Kind: wipeEverything, By: "owner", At: time.Now()}
	if _, _, err := b.runSwitch(tgt); err == nil || !strings.Contains(err.Error(), "failing") {
		t.Fatalf("the switch with a failing data store: %v", err)
	}
	if exists(filepath.Join(dir, depCronFile)) {
		t.Error("the data stores before the failing one didn't run: alice's cron file is still there")
	}
	for _, f := range []string{partRecordFile, ledgerFileName} {
		if !exists(filepath.Join(dir, f)) {
			t.Errorf("a data store's failure took alice's %s", f)
		}
	}
	if !consented() {
		t.Error("a data store's failure took alice's consent")
	}
	wipeHooks = old
	if _, _, err := b.runSwitch(tgt); err != nil {
		t.Fatalf("the switch again: %v", err)
	}
	if exists(dir) || exists(filepath.Dir(dir)) {
		t.Errorf("after the switch alice's partition directory (or its level) is left: %v %v", exists(dir), exists(filepath.Dir(dir)))
	}
	if consented() {
		t.Error("after the switch alice's consent naming apps/pu stays")
	}
}

// covers PD-43 06§1 — F7a's termPartitionRecord seam is F5's record: a
// person's terminal opening on a partitioned tile writes their partition's
// partition.json (user, uid) on the primary, a non-primary target too; an
// unpartitioned tile gets none, and neither does a paused one.
func TestTermPartitionRecordWired(t *testing.T) {
	w := partFx(t)
	b := w.b
	realPartitionIdentity()
	alice := auth.Principal{UserID: "alice", Via: "session"}
	if _, err := b.TermPartition(alice, "apps/docs", ""); err != nil {
		t.Fatal(err)
	}
	uid := b.storedPartitionUID("alice")
	dir, err := b.partitionRecordDir("apps/docs", "main", util.PartitionKey("alice", uid))
	if err != nil {
		t.Fatal(err)
	}
	rec, ok, err := readPartitionRecordAt(dir)
	if err != nil || !ok || rec.User != "alice" || rec.UID != uid || rec.Tile != "apps/docs" || rec.State != partStateActive {
		t.Fatalf("alice's record after her terminal opened: %+v %v %v", rec, ok, err)
	}
	if _, err := b.TermPartition(alice, "apps/plain", ""); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(w.root, "data", partitionsDir, util.TileKey("apps/plain"))) {
		t.Error("an unpartitioned tile's terminal wrote a partition record")
	}
	carolUID, err := b.mintPartitionUID("carol")
	if err != nil {
		t.Fatal(err)
	}
	end, ok := b.beginSwitch("apps/docs", "is paused: switching")
	if !ok {
		t.Fatal("beginSwitch")
	}
	b.noteTermPartition("apps/docs", "carol", carolUID)
	end()
	cdir, _ := b.partitionRecordDir("apps/docs", "main", util.PartitionKey("carol", carolUID))
	if exists(cdir) {
		t.Error("a paused tile's terminal wrote a partition record")
	}
	b.noteTermPartition("apps/docs", "carol", carolUID)
	if _, ok, _ := readPartitionRecordAt(cdir); !ok {
		t.Error("carol's record once the tile runs again")
	}
}

// covers 05§2 S1 — approving a partitioned tile's grant on another
// partitioned tile's people's data answers the approval warning beside
// today's ok (for `bx grant` to print); a grant on a shared resource or an
// unpartitioned tile, and every revocation, answer today's body exactly.
func TestGrantApprovalWarning(t *testing.T) {
	f := newEdgeFx(t)
	b := f.b
	f.write(map[string]string{
		"apps/r/xbin.json": `{"runtime":"go","partition":["user"],"uses":[{"target":"apps/pg","role":"reader"},` +
			`{"target":"res:apps/pg/board","role":"reader"},{"target":"apps/x","role":"reader"}]}`,
	})
	f.rescan()
	owner := auth.Principal{Owner: true}
	grant := func(method, target string) (int, string) {
		t.Helper()
		h := b.apiGrantsAdd
		if method == "DELETE" {
			h = b.apiGrantsRevoke
		}
		rec := call(t, h, owner, method, "/grants", `{"from":"apps/r","target":"`+target+`","role":"reader"}`, nil)
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}
	warn := "apps/r's code — and everyone who can change it — will be able to read and write the apps/pg data of every person who can read apps/pg"
	if code, body := grant("POST", "apps/pg"); code != 200 || body != `{"ok":"true","warning":"`+warn+`"}` {
		t.Errorf("approving apps/r on apps/pg: %d %s", code, body)
	}
	for _, target := range []string{"res:apps/pg/board", "apps/x"} {
		if code, body := grant("POST", target); code != 200 || body != `{"ok":"true"}` {
			t.Errorf("approving apps/r on %s: %d %s, want today's answer", target, code, body)
		}
	}
	if code, body := grant("DELETE", "apps/pg"); code != 200 || body != `{"ok":"true"}` {
		t.Errorf("revoking apps/r on apps/pg: %d %s, want today's answer", code, body)
	}
}

// covers 05§2 05§3 06§6.1 PD-54 — a personal bind's calls go to a tile that
// isn't partitioned: no person's partition of another tile, so no consent
// is asked, the policy on or off; and the caller partition's ledger counts
// each as a provider call to the personal tile (never an edge).
func TestPersonalBindLedgerNoConsent(t *testing.T) {
	w, st, _ := pbindWS(t)
	b := w.b
	alice := principalFor(t, st, "alice")
	if rec := pbindCall(t, w, alice, "POST", pbindBody("apps/agent", "mcp", "users/alice/mcp")); rec.Code != 200 {
		t.Fatalf("alice's bind: %d %s", rec.Code, rec.Body.String())
	}
	target, _ := b.Reg.Component("users/alice/mcp")
	for _, on := range []bool{false, true} {
		partRouteConsent(w, on)
		if d := b.Route(instanceOf("apps/agent", "user:alice"), target, ""); d.Deny != nil || d.CallerPartition != "user:alice" {
			t.Errorf("policy %v: alice's partition through her personal bind: %+v", on, d)
		}
	}
	var provider, edge int64
	for _, d := range b.ledgerDocs() {
		if d.Tile != "apps/agent" || d.User != "alice" {
			continue
		}
		for _, kinds := range d.Days {
			provider += kinds[LedgerProvider]["users/alice/mcp"]
			edge += int64(len(kinds[LedgerEdge]))
		}
	}
	if provider != 2 || edge != 0 {
		t.Errorf("alice's apps/agent ledger: %d provider calls to users/alice/mcp, %d edge rows; want 2 and 0", provider, edge)
	}
}

// covers 05§6 02§5 PD-16 — a cron job or bus subscription of a partitioned
// tile whose delivery path carries ?xbin-partition (any value, an encoded
// key too) is refused when it is registered (400), not at every delivery
// (RouteGlobal's 403): a delivery acts in the partition it was registered
// for — a person's or the global instance's alike. On a tile that isn't
// partitioned the parameter is the backend's own, as today: registered.
func TestGlobalAddressRegRefused(t *testing.T) {
	w := partRegWS(t)
	b := w.b
	w.write(map[string]string{
		"apps/x/scope.json": `{"resources":{"beat":{"type":"cron"},"feed":{"type":"bus"}}}`,
		"apps/x/xbin.json":  `{"runtime":"go","uses":[{"target":"res:apps/x/beat","role":"writer"},{"target":"res:apps/x/feed","role":"writer"}]}`,
	})
	w.rescan()
	cron := func(p auth.Principal, tile, path string) (int, string) {
		t.Helper()
		rec := call(t, b.apiCronPut, p, "PUT", "/cron/jobs", `{"name":"j","resource":"res:`+tile+`/beat","schedule":"@every 1m","path":"`+path+`"}`, nil)
		return rec.Code, rec.Body.String()
	}
	bus := func(p auth.Principal, tile, path string) (int, string) {
		t.Helper()
		rec := call(t, b.apiBusSubsPut, p, "PUT", "/bus/subscriptions", `{"name":"s","resource":"res:`+tile+`/feed","path":"`+path+`"}`, nil)
		return rec.Code, rec.Body.String()
	}
	refusal := "delivery acts in the partition it was registered for: ?xbin-partition=global is for "
	for _, c := range []struct {
		name string
		put  func(auth.Principal, string, string) (int, string)
		p    auth.Principal
		tile string
		path string
		want int
	}{
		{"alice's partition's cron job", cron, partInst("apps/pu", "alice"), "apps/pu", "/t?xbin-partition=global", 400},
		{"the global instance's cron job, another value", cron, instanceOf("apps/pg", ""), "apps/pg", "/t?a=1&xbin-partition=user:bob", 400},
		{"alice's partition's subscription, an encoded key", bus, partInst("apps/pu", "alice"), "apps/pu", "/b?xbin%2Dpartition=global", 400},
		{"the global instance's subscription", bus, instanceOf("apps/pg", ""), "apps/pg", "/b?xbin-partition=global", 400},
		{"alice's partition's cron job without it", cron, partInst("apps/pu", "alice"), "apps/pu", "/t?x=1", 200},
		{"an unpartitioned tile's cron job", cron, instanceOf("apps/x", ""), "apps/x", "/t?xbin-partition=global", 200},
		{"an unpartitioned tile's subscription", bus, instanceOf("apps/x", ""), "apps/x", "/b?xbin-partition=global", 200},
	} {
		code, body := c.put(c.p, c.tile, c.path)
		if code != c.want || c.want == 400 && !strings.Contains(body, refusal+c.tile+"'s own frames") {
			t.Errorf("%s (%s): %d %s, want %d", c.name, c.path, code, body, c.want)
		}
	}
}
