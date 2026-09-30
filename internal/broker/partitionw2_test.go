package broker

// partitionw2_test.go — the seams wave 2's integration wired between the
// packs (plans/partitions/records/W2-wire.md).

import (
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
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
