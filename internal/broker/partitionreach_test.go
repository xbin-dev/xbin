package broker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-04 PD-05 PD-13 PD-45 — the reach table of plans/partitions/03
// §B.2, cell by cell, with the consent policy off and on: a person's own
// partition for a partitioned resource, today's keys for a shared one
// (read-only for a user partition when "read") and for the global
// instance; another partitioned tile's user partition reaches the same
// person's namespace only if they can read the tile — and, with
// partitionConsent on, consented — and is counted in its ledger; an
// unpartitioned tile reaches today's (global's), or nothing without a
// global instance; view-as reaches nothing; a cron resource isn't
// partitioned. Which partition is the identity plane's one answer
// (addressedPartitionSeam, stood in by the fixture as F2 answers it).
func TestPartitionReachTable(t *testing.T) {
	w := partFx(t)
	b := w.b
	pk := func(user string) string { return util.PartitionKey(user, "uid-"+user) }
	var edges []string
	prevEdge := partitionEdgeSeam
	t.Cleanup(func() { partitionEdgeSeam = prevEdge })
	partitionEdgeSeam = func(b *Broker, userID, from, to string) {
		edges = append(edges, "user:"+userID+" "+from+"→"+to)
	}
	type cell struct {
		name     string
		p        auth.Principal
		target   string
		pkey     string // "" = today's keys
		readOnly bool
		err      string // a refusal's text, when refused
	}
	off := []cell{
		{"own partitioned", aliceDocs, "res:apps/docs/docs", pk("alice"), false, ""},
		{"another person's own", carolDocs, "res:apps/docs/docs", pk("carol"), false, ""},
		{"own read", aliceDocs, "res:apps/docs/pub", "", true, ""},
		{"own shared", aliceDocs, "res:apps/docs/board", "", false, ""},
		{"global partitioned", docsGlobal, "res:apps/docs/docs", "", false, ""},
		{"global read", docsGlobal, "res:apps/docs/pub", "", false, ""},
		{"root token", auth.Principal{Owner: true}, "res:apps/docs/docs", "", false, ""},
		{"cross-scope, can read", aliceAgent, "res:apps/docs/docs", pk("alice"), false, ""},
		{"cross-scope, can't read", bobAgent, "res:apps/docs/docs", "", false, "bob can't read apps/docs"},
		{"cross-scope, unpartitioned caller", plainTile, "res:apps/docs/docs", "", false, ""},
		{"cross-scope, unpartitioned caller, no global instance", plainTile, "res:apps/agent/mem", "", false, "it has no global instance"},
		{"an unpartitioned scope", plainTile, "res:apps/plain/notes", "", false, ""},
		{"view-as", viewAsAlice, "res:apps/docs/docs", "", false, "view-as can't open it"},
		{"cron", aliceDocs, "res:apps/docs/beat", "", false, ""},
		{"own scope of the caller, unrelated", aliceAgent, "res:apps/agent/mem", pk("alice"), false, ""},
	}
	check := func(policy string, cells []cell) {
		t.Helper()
		for _, c := range cells {
			ra, found, err := b.reachRes(c.p, c.target)
			switch {
			case !found:
				t.Errorf("%s %s: %s not found", policy, c.name, c.target)
			case c.err != "":
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Errorf("%s %s: err %v, want %q", policy, c.name, err, c.err)
				}
			case err != nil:
				t.Errorf("%s %s: %v", policy, c.name, err)
			case ra.pkey != c.pkey || ra.readOnly != c.readOnly:
				t.Errorf("%s %s: pkey %q readOnly %v, want %q %v", policy, c.name, ra.pkey, ra.readOnly, c.pkey, c.readOnly)
			case ra.dep != util.MainDeployment:
				t.Errorf("%s %s: deployment %q", policy, c.name, ra.dep)
			default:
				_ = b.allowAt(c.p, ra, "reader") // an authorized cross-scope reach is counted here
			}
		}
	}
	edgesAre := func(when string, want ...string) {
		t.Helper()
		if strings.Join(edges, ";") != strings.Join(want, ";") {
			t.Errorf("%s: the ledger counted %q, want %q", when, edges, want)
		}
		edges = nil
	}
	check("off", off)
	edgesAre("off", "user:alice apps/agent→apps/docs") // only the allowed cross-scope reach
	// a reach the grant doesn't allow (apps/agent holds none on box) isn't
	// counted: the ledger counts after authorization
	if ra, _, err := b.reachRes(aliceAgent, "res:apps/docs/box"); err != nil || ra.pkey != pk("alice") {
		t.Fatalf("alice's agent on box: %+v %v", ra, err)
	} else if err := b.allowAt(aliceAgent, ra, "reader"); err == nil {
		t.Fatal("apps/agent reached box without a grant")
	}
	edgesAre("off, not granted")

	// The policy on: the cross-scope edge needs alice's consent; nothing else
	// changes.
	w.setPartitionConsent(true)
	on := append([]cell(nil), off...)
	for i := range on {
		if on[i].name == "cross-scope, can read" {
			on[i].pkey, on[i].err = "", "alice hasn't let apps/agent use their apps/docs data"
		}
	}
	check("on", on)
	edgesAre("on")
	var asked []string
	partitionConsentStub = func(user, from, to string) bool {
		asked = append(asked, user+" "+from+"→"+to)
		return user == "alice"
	}
	for i := range on {
		if on[i].name == "cross-scope, can read" {
			on[i].pkey, on[i].err = pk("alice"), ""
		}
	}
	check("on, consented", on)
	if len(asked) != 1 || asked[0] != "alice apps/agent→apps/docs" {
		t.Errorf("consent asked %q", asked)
	}
	edgesAre("on, consented", "user:alice apps/agent→apps/docs")
	w.setPartitionConsent(false)

	// A write to a "read" resource by a user partition is refused (403 with
	// the reason); a read passes, and the global instance writes.
	ra, _, _ := b.reachRes(aliceDocs, "res:apps/docs/pub")
	if err := b.allowAt(aliceDocs, ra, "writer"); err == nil || err.Error() != "res:apps/docs/pub is read-only for people's partitions" {
		t.Errorf("alice writes pub: %v", err)
	}
	if err := b.allowAt(aliceDocs, ra, "reader"); err != nil {
		t.Errorf("alice reads pub: %v", err)
	}
	ra, _, _ = b.reachRes(docsGlobal, "res:apps/docs/pub")
	if err := b.allowAt(docsGlobal, ra, "writer"); err != nil {
		t.Errorf("global writes pub: %v", err)
	}

	// The seams unset: a partitioned scope refuses everyone rather than fall
	// back to today's keys, an unpartitioned caller too; an unpartitioned
	// scope's reach is untouched.
	addressedPartitionSeam = func(b *Broker, p auth.Principal, tile string) (string, error) {
		if c, ok := b.Reg.Component(tile); !ok || !partitionedNow(c) {
			return "", nil
		}
		return "", errPartitionUnwired
	}
	for _, p := range []auth.Principal{aliceDocs, docsGlobal, plainTile} {
		if _, _, err := b.reachRes(p, "res:apps/docs/docs"); err == nil {
			t.Errorf("an unwired identity plane let %s/%s reach a partitioned scope", p.Component, p.UserID)
		}
	}
	if ra, _, err := b.reachRes(plainTile, "res:apps/plain/notes"); err != nil || ra.pkey != "" || ra.part != "" {
		t.Errorf("an unpartitioned scope, unwired: %+v %v", ra, err)
	}
}

// covers PD-44 G3 — a paused partitioned scope (its code dropped the key
// while it holds data: pending) and one whose mode record can't be read
// refuse with 409, never falling back to today's keys.
func TestPartitionReachPaused(t *testing.T) {
	w := partFx(t)
	if code, body := nsKV(t, w.b, "PUT", aliceDocs, "res:apps/docs/docs/k", "v"); code != 200 {
		t.Fatalf("alice's first write: %d %s", code, body)
	}
	w.write(map[string]string{"apps/docs/xbin.json": strings.Replace(partFxFiles["apps/docs/xbin.json"], `"partition":["user","global"],`, "", 1)})
	w.rescan()
	if st, _, _ := w.state("apps/docs"); st != registry.PartitionPending {
		t.Fatalf("apps/docs is %s, want pending (it holds a partition's data)", st)
	}
	if code, body := nsKV(t, w.b, "GET", docsGlobal, "res:apps/docs/docs/k", ""); code != 409 || !strings.Contains(body, "is paused") {
		t.Errorf("a paused scope: %d %s", code, body)
	}

	// A mode record this xbind can't read: R is unknown, and nothing of the
	// scope is reached — kv, blob or bus, whoever asks — nor delivered.
	w = partFxWith(t, map[string]string{
		"data/partitions/" + util.TileKey("apps/docs") + "/mode.json": `{"schema": 1, "tile": "apps/docs", "mode": {"us` + "\n",
	})
	if c, _ := w.b.Reg.Component("apps/docs"); !c.PartitionRecordUnknown() {
		t.Fatal("apps/docs's corrupt record reads")
	}
	for _, p := range []auth.Principal{docsGlobal, aliceDocs, plainTile} {
		if code, body := nsKV(t, w.b, "GET", p, "res:apps/docs/docs/k", ""); code != 409 || !strings.Contains(body, "can't be read") {
			t.Errorf("%s/%s on an unreadable record: %d %s", p.Component, p.UserID, code, body)
		}
	}
	if code, body := nsKV(t, w.b, "GET", aliceDocs, "res:apps/docs/board/k", ""); code != 409 {
		t.Errorf("a shared kv on an unreadable record: %d %s", code, body)
	}
	if r := zeroDataCall(t, w.b.apiBusPublish, "POST", "", `{"resource":"res:apps/docs/wall","topic":"t"}`, docsGlobal); r.Code != 409 {
		t.Errorf("a bus publish on an unreadable record: %d %s", r.Code, r.Body)
	}
	ev := events.Event{Type: "bus", Topic: "res:apps/docs/bus/t", Partition: partGlobalKey}
	if w.b.busFilter(docsGlobal, ev) {
		t.Error("an event of a scope whose record can't be read was delivered")
	}
}

// covers PD-05 PD-45 S8 — the kv plane end to end: each person's partition
// has its own copy of a partitioned resource, the global instance its own
// at today's keys; a "read" resource is written by global and read by
// people (a person's write is 403); a shared one is one copy. The first
// write records whose the namespace is.
func TestPartitionKVRoundTrip(t *testing.T) {
	w := partFx(t)
	b := w.b
	put := func(p auth.Principal, rest, v string, want int) {
		t.Helper()
		if code, body := nsKV(t, b, "PUT", p, rest, v); code != want {
			t.Fatalf("PUT %s as %s/%s: %d %s, want %d", rest, p.Component, p.UserID, code, body, want)
		}
	}
	get := func(p auth.Principal, rest string) string {
		t.Helper()
		code, body := nsKV(t, b, "GET", p, rest, "")
		if code == 404 {
			return "-"
		}
		if code != 200 {
			t.Fatalf("GET %s as %s/%s: %d %s", rest, p.Component, p.UserID, code, body)
		}
		return body
	}
	put(aliceDocs, "res:apps/docs/docs/k", "alice's", 200)
	put(docsGlobal, "res:apps/docs/docs/k", "global's", 200)
	if a, c, g := get(aliceDocs, "res:apps/docs/docs/k"), get(carolDocs, "res:apps/docs/docs/k"), get(docsGlobal, "res:apps/docs/docs/k"); a != "alice's" || c != "-" || g != "global's" {
		t.Errorf("docs: alice %q, carol %q, global %q", a, c, g)
	}
	put(aliceDocs, "res:apps/docs/pub/k", "x", 403)
	put(docsGlobal, "res:apps/docs/pub/k", "public", 200)
	if a := get(aliceDocs, "res:apps/docs/pub/k"); a != "public" {
		t.Errorf("pub read by alice: %q", a)
	}
	put(carolDocs, "res:apps/docs/board/k", "carol's note", 200)
	if a := get(aliceDocs, "res:apps/docs/board/k"); a != "carol's note" {
		t.Errorf("board read by alice: %q", a)
	}
	// alice's partition of apps/agent reaches alice's apps/docs data.
	put(aliceAgent, "res:apps/docs/docs/from-agent", "via agent", 200)
	if a := get(aliceDocs, "res:apps/docs/docs/from-agent"); a != "via agent" {
		t.Errorf("alice's agent wrote into %q", a)
	}
	// The namespace names its person.
	m, ok, err := b.readNS(partNS("apps/docs", util.MainDeployment, util.PartitionKey("alice", "uid-alice")))
	if err != nil || !ok || m.Partition == nil || m.Partition.User != "alice" || m.Partition.UID != "uid-alice" || m.Partition.Tile != "apps/docs" {
		t.Fatalf("alice's ns.json: %+v %v %v", m, ok, err)
	}
	// Today's kv.db holds global's and the shared keys only.
	if main, _ := os.ReadFile(filepath.Join(w.root, "data", "kv.db")); strings.Contains(string(main), "alice") {
		t.Error("data/kv.db holds a partition's key")
	}
}

// covers PD-05 PD-45 S8 G2 — a partitioned scope's bus (02 §9, 04 §2): each
// publish on its own bus is stamped with the publisher's partition —
// global's too — and reaches only subscribers acting in that partition; an
// unstamped event there reaches no one; a shared bus is unstamped and
// reaches every reader; a person's publish on a "read" bus is 403; without
// the stamp wired (F2's field) a partitioned bus refuses publishes (503).
func TestPartitionBus(t *testing.T) {
	w := partFx(t)
	b := w.b
	ch, cancel := b.Hub.Subscribe(func(e events.Event) bool { return e.Type == "bus" })
	t.Cleanup(cancel)
	publish := func(p auth.Principal, res string, want int) events.Event {
		t.Helper()
		r := zeroDataCall(t, b.apiBusPublish, "POST", "", `{"resource":"`+res+`","topic":"t","data":1}`, p)
		if r.Code != want {
			t.Fatalf("%s/%s publishes on %s: %d %s, want %d", p.Component, p.UserID, res, r.Code, r.Body, want)
		}
		select {
		case ev := <-ch: // Publish is synchronous
			if want != 200 {
				t.Fatalf("a refused publish on %s went out: %+v", res, ev)
			}
			return ev
		default:
			if want == 200 {
				t.Fatalf("%s: no event", res)
			}
		}
		return events.Event{}
	}
	stamp := func(e events.Event) string { return busEventPartition(e) }
	reaches := func(e events.Event, ps ...auth.Principal) string {
		var out []string
		for _, p := range ps {
			if b.busFilter(p, e) {
				out = append(out, p.Component+"/"+p.UserID)
			}
		}
		return strings.Join(out, ",")
	}
	readers := []auth.Principal{aliceDocs, carolDocs, docsGlobal}

	// The global instance's event on the partitioned bus: stamped global,
	// never a person's frame's.
	ev := publish(docsGlobal, "res:apps/docs/bus", 200)
	if s := stamp(ev); s != partGlobalKey {
		t.Errorf("global's event stamped %q", s)
	}
	if r := reaches(ev, readers...); r != "apps/docs/" {
		t.Errorf("global's event reaches %q, want the global instance only", r)
	}
	// A person's: their partition's only.
	ev = publish(aliceDocs, "res:apps/docs/bus", 200)
	if s := stamp(ev); s != "user:alice" {
		t.Errorf("alice's event stamped %q", s)
	}
	if r := reaches(ev, readers...); r != "apps/docs/alice" {
		t.Errorf("alice's event reaches %q", r)
	}
	// An unstamped event on the partitioned bus reaches no one there.
	if r := reaches(events.Event{Type: "bus", Topic: "res:apps/docs/bus/t"}, readers...); r != "" {
		t.Errorf("an unstamped event reaches %q", r)
	}
	// A shared bus: unstamped, every reader.
	ev = publish(carolDocs, "res:apps/docs/wall", 200)
	if s := stamp(ev); s != "" {
		t.Errorf("a shared bus's event stamped %q", s)
	}
	if r := reaches(ev, readers...); r != "apps/docs/alice,apps/docs/carol,apps/docs/" {
		t.Errorf("a shared bus's event reaches %q", r)
	}
	// A "read" bus: a person's publish is 403; global's reaches everyone.
	r := zeroDataCall(t, b.apiBusPublish, "POST", "", `{"resource":"res:apps/docs/news","topic":"t"}`, aliceDocs)
	if r.Code != 403 || !strings.Contains(r.Body.String(), "res:apps/docs/news is read-only for people's partitions") {
		t.Errorf("alice publishes on news: %d %s", r.Code, r.Body)
	}
	if ev = publish(docsGlobal, "res:apps/docs/news", 200); reaches(ev, readers...) != "apps/docs/alice,apps/docs/carol,apps/docs/" {
		t.Errorf("a read bus's event reaches %q", reaches(ev, readers...))
	}

	// Unwired (F2's field not there yet): the partitioned bus refuses
	// publishes rather than deliver them unstamped, and delivers nothing; a
	// shared one is today's.
	stampBusPartition, busEventPartition = nil, nil
	publish(docsGlobal, "res:apps/docs/bus", 503)
	publish(aliceDocs, "res:apps/docs/bus", 503)
	publish(aliceDocs, "res:apps/docs/wall", 200)
	if r := reaches(events.Event{Type: "bus", Topic: "res:apps/docs/bus/t"}, readers...); r != "" {
		t.Errorf("unwired, an event on the partitioned bus reaches %q", r)
	}
}

// TestBusPartitionStampWired guards the merge with the identity plane
// (F2): once events.Event carries its Partition field, the bus stamp's
// seams must be pointed at it — or every partitioned bus keeps refusing
// publishes (503).
func TestBusPartitionStampWired(t *testing.T) {
	f, ok := reflect.TypeOf(events.Event{}).FieldByName("Partition")
	if !ok {
		t.Skip("events.Event has no Partition field yet (F2): partitioned buses answer 503 until it lands")
	}
	if stampBusPartition == nil || busEventPartition == nil {
		t.Fatal("events.Event.Partition exists: set stampBusPartition and busEventPartition to it (partitionbus.go, records/F4.md)")
	}
	var ev events.Event
	stampBusPartition(&ev, "user:alice")
	if v := reflect.ValueOf(ev).FieldByIndex(f.Index).String(); v != "user:alice" || busEventPartition(ev) != "user:alice" {
		t.Errorf("the stamp doesn't ride events.Event.Partition: %q, read back %q", v, busEventPartition(ev))
	}
}

// covers PD-04 PD-05 C7 — PartitionEnv: the global instance's is today's; a
// user partition's env is EnvFor's byte for byte, and its remap binds its
// own volumes at the canonical paths, a shared resource from today's volume
// (Shared, RO for "read"), and records whose the namespace is.
func TestPartitionEnv(t *testing.T) {
	w := partFx(t)
	b := w.b
	nsFakeVolumes(t, b)
	c, _ := b.Reg.Component("apps/docs")
	wantEnv, wantRemap := b.DeploymentEnv(c, util.MainDeployment)
	env, remap := b.PartitionEnv(c, "", "global")
	if strings.Join(env, "\n") != strings.Join(wantEnv, "\n") || remap != nil || wantRemap != nil {
		t.Errorf("global's env/remap: %v %v", env, remap)
	}
	env, remap = b.PartitionEnv(c, util.MainDeployment, "user:alice")
	if strings.Join(env, "\n") != strings.Join(b.EnvFor(c), "\n") {
		t.Errorf("alice's env differs from EnvFor's:\n%s", strings.Join(env, "\n"))
	}
	pkey := util.PartitionKey("alice", "uid-alice")
	own := func(name string) string {
		k, err := b.resKeysIn(resTarget{Scope: "apps/docs", Name: name}, util.MainDeployment, pkey)
		if err != nil {
			t.Fatal(err)
		}
		return b.resMount(k, false)
	}
	canon := func(name string) string { return b.fsResPath("apps/docs", name, false) }
	got, _ := json.Marshal(remap)
	for name, want := range map[string]struct {
		src          string
		shared, ro   bool
		expectInEnvs bool
	}{
		"files": {own("files"), false, false, true},
		"notes": {own("notes"), false, false, true},
		"team":  {canon("team"), true, false, true},
		"conf":  {canon("conf"), true, true, true},
	} {
		rb, ok := remap[canon(name)]
		if !ok || rb.Src != want.src || rb.Shared != want.shared || rb.RO != want.ro || rb.Omit {
			t.Errorf("%s: %+v (remap %s)", name, rb, got)
		}
	}
	if len(remap) != 4 {
		t.Errorf("remap has %d entries: %s", len(remap), got)
	}
	if m, ok, _ := b.readNS(partNS("apps/docs", util.MainDeployment, pkey)); !ok || m.Partition == nil || m.Partition.User != "alice" {
		t.Errorf("alice's namespace isn't recorded: %+v", m)
	}
	if reason := b.PartitionEncryptionHoldReason("apps/docs", "", "user:alice"); reason != "" {
		t.Errorf("alice's partition held: %s", reason)
	}
	// A data act on the primary's namespace, where the shared resources
	// are, holds people's partitions too; an act on another person's
	// partition doesn't.
	release, err := b.holdNS(nsOf("apps/docs", util.MainDeployment), nsRestoring)
	if err != nil {
		t.Fatal(err)
	}
	if reason := b.PartitionEncryptionHoldReason("apps/docs", "", "user:alice"); !strings.Contains(reason, "data operation") {
		t.Errorf("alice's partition during a restore of the shared data: %q", reason)
	}
	release()
	release, err = b.holdNS(partNS("apps/docs", util.MainDeployment, util.PartitionKey("carol", "uid-carol")), nsResetting)
	if err != nil {
		t.Fatal(err)
	}
	if reason := b.PartitionEncryptionHoldReason("apps/docs", "", "user:alice"); reason != "" {
		t.Errorf("alice's partition held by carol's reset: %q", reason)
	}
	if reason := b.PartitionEncryptionHoldReason("apps/docs", "", "user:carol"); !strings.Contains(reason, "data operation") {
		t.Errorf("carol's partition during its reset: %q", reason)
	}
	release()
	if reason := b.PartitionEncryptionHoldReason("apps/docs", "", "org:x"); reason == "" {
		t.Error("an unknown partition kind isn't held")
	}
}

// covers PD-05 PD-48 — the blob plane: a person's blob lands in their own
// partition's volume, mounted on first use (after its namespace is
// recorded), and no other partition or the global instance sees it.
func TestPartitionBlob(t *testing.T) {
	w := partFx(t)
	b := w.b
	nsFakeVolumes(t, b)
	call := func(h func(http.ResponseWriter, *http.Request), method string, p auth.Principal, rest, body string) (int, string) {
		t.Helper()
		r := zeroDataCall(t, h, method, rest, body, p)
		return r.Code, r.Body.String()
	}
	if code, body := call(b.apiBlobPut, "PUT", aliceDocs, "res:apps/docs/box/a.txt", "alice's blob"); code != 200 {
		t.Fatalf("PUT: %d %s", code, body)
	}
	if code, body := call(b.apiBlobGet, "GET", aliceDocs, "res:apps/docs/box/a.txt", ""); code != 200 || body != "alice's blob" {
		t.Errorf("alice's GET: %d %s", code, body)
	}
	for _, p := range []auth.Principal{carolDocs, docsGlobal} {
		if code, _ := call(b.apiBlobGet, "GET", p, "res:apps/docs/box/a.txt", ""); code != 404 {
			t.Errorf("%s/%s reads alice's blob: %d", p.Component, p.UserID, code)
		}
	}
	pkey := util.PartitionKey("alice", "uid-alice")
	k, _ := b.resKeysIn(resTarget{Scope: "apps/docs", Name: "box"}, util.MainDeployment, pkey)
	if data, err := os.ReadFile(filepath.Join(b.resMount(k, false), "a.txt")); err != nil || string(data) != "alice's blob" {
		t.Errorf("alice's volume holds %q %v", data, err)
	}
	if m, ok, _ := b.readNS(partNS("apps/docs", util.MainDeployment, pkey)); !ok || m.Partition == nil || m.Partition.User != "alice" {
		t.Errorf("alice's namespace isn't recorded: %+v", m)
	}
}
