package broker

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-13 01§2.6 — a consent file this xbind can't read (a newer
// schema left by a downgrade, a hand edit) counts as no consent and is
// never written over; it blocks only what it may concern: a switch of a
// tile whose path it holds fails, in the dry run already, while every
// other switch goes on; its person's POST and DELETE answer 409; /alerts
// names it to admins. A consent never names a tile whose path holds the
// arrow, and revoking says whether there was one.
func TestPartitionConsentUnreadable(t *testing.T) {
	f := newEdgeFx(t)
	b := f.b
	partRouteConsent(f.partWS, true)
	alice, bob := personP(t, f.partWS, "alice"), personP(t, f.partWS, "bob")
	if r := call(t, b.apiConsentsAdd, alice, "POST", "/partitions/consents", `{"from":"apps/q","to":"apps/pg"}`, nil); r.Code != 200 {
		t.Fatalf("alice consents: %d %s", r.Code, r.Body)
	}
	// bob's file, from a newer xbind, names apps/pu only
	bobUID, err := b.mintPartitionUID("bob")
	if err != nil {
		t.Fatal(err)
	}
	bobFile := b.consentPath(bobUID)
	if err := os.WriteFile(bobFile, []byte(`{"schema":2,"user":"bob","uid":"`+bobUID+`","edges":{"apps/q→apps/pu":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// a switch of apps/pg: bob's file can't name it — dry and real go on
	for _, dry := range []bool{true, false} {
		if err := wipeConsentsHook(b, wipeTarget{Tile: "apps/pg", Kind: wipeEverything, DryRun: dry}, &wipeSummary{}); err != nil {
			t.Errorf("switching apps/pg (dry %v) failed on bob's unrelated file: %v", dry, err)
		}
	}
	if b.consentHolds("alice", "apps/q", "apps/pg") {
		t.Error("pg's switch kept alice's consent naming it")
	}
	// a switch of apps/pu (or of apps/p, a prefix): the file may name pu
	for _, dry := range []bool{true, false} {
		err := wipeConsentsHook(b, wipeTarget{Tile: "apps/pu", Kind: wipeEverything, DryRun: dry}, &wipeSummary{})
		if err == nil || !strings.Contains(err.Error(), bobUID+".json") || !strings.Contains(err.Error(), "may name apps/pu") {
			t.Errorf("switching apps/pu (dry %v) with bob's unreadable file naming it: %v", dry, err)
		}
	}
	if err := wipeConsentsHook(b, wipeTarget{Tile: "apps/p", Kind: wipeEverything}, &wipeSummary{}); err != nil {
		t.Errorf("apps/p isn't named by a file that names apps/pu: %v", err)
	}
	if raw, _ := os.ReadFile(bobFile); !strings.Contains(string(raw), `"schema":2`) {
		t.Errorf("bob's file was written over: %s", raw)
	}
	// bob can't consent or revoke until an admin fixes it; admins see why
	for _, h := range []struct {
		method string
		fn     func() int
	}{
		{"POST", func() int {
			return call(t, b.apiConsentsAdd, bob, "POST", "/partitions/consents", `{"from":"apps/q","to":"apps/pg"}`, nil).Code
		}},
		{"DELETE", func() int {
			return call(t, b.apiConsentsRevoke, bob, "DELETE", "/partitions/consents", `{"from":"apps/q","to":"apps/pu"}`, nil).Code
		}},
	} {
		if code := h.fn(); code != 409 {
			t.Errorf("bob's %s with his file unreadable: %d, want 409", h.method, code)
		}
	}
	alerts := b.consentAlerts()
	if len(alerts) != 1 || alerts[0].Kind != "partition-consents" || !strings.Contains(alerts[0].Message, bobUID+".json (schema 2") {
		t.Errorf("the /alerts line: %+v", alerts)
	}
	if err := os.Remove(bobFile); err != nil {
		t.Fatal(err)
	}
	if alerts := b.consentAlerts(); len(alerts) != 0 {
		t.Errorf("the alert outlived the file: %+v", alerts)
	}

	// the arrow, and the revoke's answer
	if r := call(t, b.apiConsentsAdd, alice, "POST", "/partitions/consents", `{"from":"apps/q","to":"apps/p→g"}`, nil); r.Code != 400 {
		t.Errorf("a consent naming a path with the arrow: %d %s", r.Code, r.Body)
	}
	if b.consentHolds("alice", "apps/q→apps", "pg") {
		t.Error("a consent matched a path holding the arrow")
	}
	var ans struct{ Revoked bool }
	decode(t, call(t, b.apiConsentsRevoke, alice, "DELETE", "/partitions/consents?from=apps/q&to=apps/pu", "", nil), 200, &ans)
	if ans.Revoked {
		t.Error("revoking a consent alice never gave answers revoked")
	}
	if r := call(t, b.apiConsentsAdd, alice, "POST", "/partitions/consents", `{"from":"apps/q","to":"apps/pu"}`, nil); r.Code != 200 {
		t.Fatal(r.Body)
	}
	decode(t, call(t, b.apiConsentsRevoke, alice, "DELETE", "/partitions/consents?from=apps/q&to=apps/pu", "", nil), 200, &ans)
	if !ans.Revoked {
		t.Error("revoking alice's consent doesn't say so")
	}
}

// covers PD-46 S10 — a tile's totals for its writers and managers never
// name a person: a target that is someone's personal tile is "(a personal
// tile)", merged; the person's own rows and an admin's view keep it.
func TestPartitionLedgerPersonalTargets(t *testing.T) {
	f := newEdgeFx(t)
	b := f.b
	if _, err := b.Users.Upsert(users.User{ID: "wendy", Role: users.RoleUser, Tiles: map[string]string{"apps/*": users.LevelWrite}}, "password1"); err != nil {
		t.Fatal(err)
	}
	b.ledgerCount("apps/q", "alice", LedgerProvider, "users/alice/mcp")
	b.ledgerCount("apps/q", "alice", LedgerProvider, "apps/x")
	var got struct {
		Rows   []ledgerRow `json:"rows"`
		Totals []ledgerRow `json:"totals"`
	}
	decode(t, call(t, b.apiPartitionLedger, personP(t, f.partWS, "wendy"), "GET", "/partitions/ledger?tile=apps/q", "", nil), 200, &got)
	targets := map[string]int64{}
	for _, r := range got.Totals {
		targets[r.Target] = r.Count
	}
	if len(targets) != 2 || targets[ledgerPersonalTile] != 1 || targets["apps/x"] != 1 {
		t.Errorf("wendy (a writer of apps/q) sees totals %+v", got.Totals)
	}
	got.Totals = nil
	decode(t, call(t, b.apiPartitionLedger, personP(t, f.partWS, "bob"), "GET", "/partitions/ledger?tile=apps/q", "", nil), 200, &got)
	if s := strings.Join(func() (o []string) {
		for _, r := range got.Totals {
			o = append(o, r.Target)
		}
		return
	}(), ","); s != "apps/x,users/alice/mcp" {
		t.Errorf("bob (an admin) sees totals naming %q", s)
	}
	decode(t, call(t, b.apiPartitionLedger, personP(t, f.partWS, "alice"), "GET", "/partitions/ledger", "", nil), 200, &got)
	if len(got.Rows) != 2 {
		t.Errorf("alice's own rows: %+v", got.Rows)
	}
}

// covers PD-46 06§6.1 — counting never waits for a file: counts from many
// requests at once land, reads run meanwhile, a ledger nobody counted in
// for a while leaves memory once saved and is read again from its file,
// and nothing counted is lost (run with -race).
func TestPartitionLedgerConcurrent(t *testing.T) {
	f := newEdgeFx(t)
	b := f.b
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			who := []string{"alice", "bob"}[i%2]
			for j := range 50 {
				b.ledgerCount("apps/q", who, LedgerEdge, []string{"apps/pg", "apps/pu"}[j%2])
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 20 {
			_ = b.ledgerDocs()
			_ = b.partitionEdges(30)
		}
	}()
	wg.Wait()
	total := func() int64 {
		var n int64
		for _, d := range b.ledgerDocs() {
			for _, kinds := range d.Days {
				for _, c := range kinds[LedgerEdge] {
					n += c
				}
			}
		}
		return n
	}
	if n := total(); n != 400 {
		t.Fatalf("counted %d, want 400", n)
	}
	b.flushLedgers()
	path := b.ledgerPath("apps/q", util.MainDeployment, f.pkeyOf("alice"))
	if d, err := readLedgerDoc(path); err != nil || d.Days["2026-09-30"][LedgerEdge]["apps/pg"]+d.Days["2026-09-30"][LedgerEdge]["apps/pu"] != 200 {
		t.Fatalf("alice's ledger on disk after the flush: %+v %v", d, err)
	}
	// idle: saved ledgers leave memory at the next sweep, and are read again
	f.advance(ledgerIdle + time.Minute)
	b.ledgerCount("apps/q", "carol", LedgerProvider, "apps/x") // sweeps
	ls := b.ledgers()
	ls.mu.Lock()
	_, kept := ls.entries[path]
	n := len(ls.entries)
	ls.mu.Unlock()
	if kept || n != 1 {
		t.Errorf("after %v idle, alice's ledger is still in memory (%d entries)", ledgerIdle, n)
	}
	b.ledgerCount("apps/q", "alice", LedgerEdge, "apps/pg")
	b.flushLedgers()
	if n := total(); n != 401 {
		t.Errorf("after the reload: %d edges counted, want 401", n)
	}
	if d, _ := readLedgerDoc(path); d == nil || d.Days["2026-09-30"][LedgerEdge]["apps/pg"] != 101 {
		t.Errorf("alice's ledger after the reload: %+v", d)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
}
