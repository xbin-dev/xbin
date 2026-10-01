package broker

import (
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-17 PD-20 PD-43 — the identity plane's answers to the runner's
// hooks (partitionrunhooks.go): PartitionIdent gives a live person's pkey,
// minting their uid, and nothing else; ShouldRunPartition passes a live
// person's partition of a partitioned tile's primary only;
// PublishPartitionState publishes a `partitions` state event stamped with
// the partition, and nothing for anything but a user partition.
func TestPartitionRunHooks(t *testing.T) {
	w := partRouteWS(t)
	b := w.b
	pkey, uid, err := b.PartitionIdent("apps/pg", "user:alice")
	if err != nil || pkey != w.pkeyOf("alice") || !util.PartitionKeyOK(pkey) || uid == "" || uid != b.storedPartitionUID("alice") {
		t.Errorf("alice's ident: %q %q, %v", pkey, uid, err)
	}
	for _, c := range []struct{ tile, part, refused string }{
		{"apps/pg", "user:carol", "carol can't read apps/pg"},
		{"apps/pg", "user:dave", "disabled"},
		{"apps/pg", "user:erin", "erin no longer exists"},
		{"apps/pg", "global", "is no person's partition"},
		{"apps/pg", "user:Alice", "is no person's partition"},
	} {
		if got, gotUID, err := b.PartitionIdent(c.tile, c.part); err == nil || got != "" || gotUID != "" || !strings.Contains(err.Error(), c.refused) {
			t.Errorf("PartitionIdent(%s, %s) = %q %q, %v; want a refusal saying %q", c.tile, c.part, got, gotUID, err, c.refused)
		}
	}
	if u, _ := b.Users.Get("carol"); u.UID != "" {
		t.Error("a refused ident minted carol a uid")
	}
	_, bobUID, err := b.PartitionIdent("apps/pu", "user:bob")
	if err != nil {
		t.Fatal(err)
	}
	carolUID, err1 := b.mintPartitionUID("carol") // their own uids: the liveness gate refuses them, not the uid
	daveUID, err2 := b.mintPartitionUID("dave")
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	for _, c := range []struct {
		tile, dep, part, uid string
		want                 bool
	}{
		{"apps/pg", "main", "user:alice", uid, true},
		{"apps/pu", "main", "user:bob", bobUID, true},
		{"apps/pg", "main", "user:alice", "0123456789abcdef0123456789abcdef", false}, // another incarnation's (PD-20)
		{"apps/pg", "main", "user:alice", "", false},
		{"apps/pg", "dev", "user:alice", uid, false},
		{"apps/pg", "main", "user:carol", carolUID, false},
		{"apps/pg", "main", "user:dave", daveUID, false},
		{"apps/pg", "main", "global", uid, false},
		{"apps/x", "main", "user:alice", uid, false},
		{"apps/none", "main", "user:alice", uid, false},
	} {
		if got := b.ShouldRunPartition(c.tile, c.dep, c.part, c.uid); got != c.want {
			t.Errorf("ShouldRunPartition(%s, %s, %s, %q) = %v", c.tile, c.dep, c.part, c.uid, got)
		}
	}
	ch, cancel := b.Hub.Subscribe(func(e events.Event) bool { return e.Type == "partitions" })
	defer cancel()
	b.PublishPartitionState("apps/pg", "main", "global", "build-start", "")
	b.PublishPartitionState("apps/pg", "main", "user:alice", "build-error", "user:alice's instance: exit 1")
	select {
	case e := <-ch:
		data, _ := e.Data.(map[string]any)
		if e.Component != "apps/pg" || e.Partition != "user:alice" || data["op"] != "state" || data["event"] != "build-error" ||
			data["text"] != "user:alice's instance: exit 1" {
			t.Errorf("the state event: %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no partitions event")
	}
	select {
	case e := <-ch:
		t.Errorf("a second event: %+v", e)
	default:
	}
}

// covers PD-43 — the fillers of F4's seams: addressedPartitionKey is
// addressedPartition's answer, storedPartitionUID never mints, and
// mintPartitionUID mints once — adopting the uid the partition records
// carry (partitionAdoptUID) when there is one — so Route's partition id and
// F4's namespaces never disagree.
func TestPartitionUIDFillers(t *testing.T) {
	w := partRouteWS(t)
	b := w.b
	if got, err := b.addressedPartitionKey(frameOf("apps/pg", "alice"), "apps/pg"); got != "user:alice" || err != nil {
		t.Errorf("addressedPartitionKey: %q, %v", got, err)
	}
	if got := b.storedPartitionUID("alice"); got != "" {
		t.Errorf("a stored uid before any partition: %q", got)
	}
	const adopted = "0123456789abcdef0123456789abcdef"
	var asked time.Time
	withSeam(t, &partitionAdoptUID, func(_ *Broker, id string, created time.Time) (string, bool) {
		asked = created
		return adopted, id == "alice"
	})
	uid, err := b.mintPartitionUID("alice")
	u, _ := b.Users.Get("alice")
	if err != nil || uid != adopted || b.storedPartitionUID("alice") != adopted || asked.Unix() != u.Created {
		t.Errorf("alice's mint: %q, %v (asked from %v, created %d)", uid, err, asked, u.Created)
	}
	if d := b.Route(frameOf("apps/pg", "alice"), mustComp(t, b, "apps/pg"), ""); d.CallerPartitionID != util.PartitionKey("alice", adopted) {
		t.Errorf("Route's partition id %q isn't the adopted uid's", d.CallerPartitionID)
	}
	withSeam(t, &partitionAdoptUID, func(*Broker, string, time.Time) (string, bool) { return "", false })
	uid, err = b.mintPartitionUID("bob")
	if err != nil || len(uid) != 32 || uid == adopted {
		t.Errorf("bob's fresh mint: %q, %v", uid, err)
	}
	if again, _ := b.mintPartitionUID("bob"); again != uid {
		t.Errorf("a second mint changed bob's uid: %q → %q", uid, again)
	}
	if _, err := b.mintPartitionUID("erin"); err == nil {
		t.Error("a mint for nobody")
	}
}

// covers G2 PD-13 — a partition's bus events (02 §9, 04 §2): a tile of the
// scope, the root or a sibling, reaches its own partition's events there
// whatever the consent policy says (as the data plane reaches its own
// scope); another partitioned tile only through the cross-tile mapping (so,
// with the policy on, with consent); nothing reaches another person's. The
// publish goes through the data plane's stamp (publishPartitioned) and the
// rule is its busPartitionReaches, on the identity plane's real answers
// (the wired seams, no stand-ins).
func TestBusPartitionScope(t *testing.T) {
	w := partRouteWS(t)
	b := w.b
	w.write(map[string]string{
		"apps/sc/scope.json":     `{"resources":{"feed":{"type":"bus"}}}`,
		"apps/sc/xbin.json":      `{"runtime":"go","partition":["user"],"uses":[{"target":"res:apps/sc/feed","role":"writer"}]}`,
		"apps/sc/side/xbin.json": `{"runtime":"go","partition":["user"]}`,
	})
	w.rescan()
	if c := mustComp(t, b, "apps/sc/side"); c.Scope != "apps/sc" {
		t.Fatalf("apps/sc/side's scope is %q", c.Scope)
	}
	partRouteConsent(w, true)
	ch, cancel := b.Hub.Subscribe(func(e events.Event) bool { return e.Type == "bus" })
	defer cancel()
	alice := frameOf("apps/sc", "alice")
	alice.Role = "writer"
	if r := zeroDataCall(t, b.apiBusPublish, "POST", "", `{"resource":"res:apps/sc/feed","topic":"news","data":1}`, alice); r.Code != 200 {
		t.Fatalf("alice's publish: %d %s", r.Code, r.Body)
	}
	var e events.Event
	select {
	case e = <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("no bus event")
	}
	if e.Partition != "user:alice" || e.Topic != "res:apps/sc/feed/news" || e.Deployment != "" {
		t.Errorf("the partition's bus event: %+v", e)
	}
	rt, _ := b.resScope("res:apps/sc/feed")
	set, _ := b.declaredIn(rt.Scope, util.MainDeployment)
	reaches := func(p auth.Principal, e events.Event) bool {
		_, own, err := b.resNamespace(p, rt.Scope)
		return err == nil && b.busPartitionReaches(p, rt, set[rt.Name], own, e)
	}
	for _, c := range []struct {
		name string
		p    auth.Principal
		want bool
	}{
		{"alice's frame of the root", frameOf("apps/sc", "alice"), true},
		{"alice's frame of a sibling, the policy on", frameOf("apps/sc/side", "alice"), true},
		{"alice's partition's instance of a sibling", instanceOf("apps/sc/side", "user:alice"), true},
		{"bob's frame of a sibling", frameOf("apps/sc/side", "bob"), false},
		{"another partitioned tile as alice, no consent", instanceOf("apps/q", "user:alice"), false},
		{"a view-as frame", auth.Principal{Component: "apps/sc/side", UserID: "alice", Via: "frame", Impersonator: "bob"}, false},
	} {
		if got := reaches(c.p, e); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	withSeam(t, &partitionConsentHolds, func(_ *Broker, id, from, to string) bool { return id == "alice" && from == "apps/q" })
	if !reaches(instanceOf("apps/q", "user:alice"), e) {
		t.Error("another partitioned tile as alice, consented")
	}
	// a person's stamped event on a scope that no longer partitions (the
	// stamp outlived a switch) reaches no one; a global one stays today's
	w.write(map[string]string{"apps/other/scope.json": `{"resources":{"feed":{"type":"bus"}}}`, "apps/other/xbin.json": `{"runtime":"go"}`})
	w.rescan()
	ort, _ := b.resScope("res:apps/other/feed")
	oset, _ := b.declaredIn(ort.Scope, util.MainDeployment)
	other := frameOf("apps/other", "alice")
	if b.busPartitionReaches(other, ort, oset[ort.Name], true, events.Event{Type: "bus", Topic: "res:apps/other/feed/t", Partition: "user:alice"}) ||
		!b.busPartitionReaches(other, ort, oset[ort.Name], true, events.Event{Type: "bus", Topic: "res:apps/other/feed/t"}) {
		t.Error("an unpartitioned scope: a person's stamp must reach no one, an unstamped event everyone")
	}
}

// covers PD-43 — the merge guard of the uid adoption seam: once a store of
// people's partition records is registered (F4's partition namespaces),
// partitionAdoptUID must be F4's adoptablePartitionUID, or a person whose
// uid an older xbind dropped would be minted a new one on Route's path —
// a new pkey, their partitions orphaned.
func TestPartitionUIDAdoptionWired(t *testing.T) {
	base := map[string]bool{"lifecycle": true, "namespaces": true, "vault": true, "registrations": true}
	var records []string
	for _, s := range partitionStores {
		if !base[s.name] {
			records = append(records, s.name)
		}
	}
	if len(records) > 0 && partitionAdoptUID == nil {
		t.Errorf("stores %v keep people's partition records, but partitionAdoptUID is unset: "+
			"set it to (*Broker).adoptablePartitionUID (plans/partitions/records/F2.md)", records)
	}
}
