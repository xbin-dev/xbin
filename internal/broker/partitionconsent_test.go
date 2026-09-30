package broker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// edgeFx is the routing fixture (partRouteWS) with the edges the matrix
// needs: apps/pg declares a per-partition kv (docs) and a shared one
// (board); apps/q holds grants on docs and board as well as its call
// grants; apps/rs, partitioned, holds only a grant on board; apps/pg's
// global instance holds grants on apps/x and apps/pu; apps/x on alice's
// personal tile. The prompts, pushes, stops and the clocks are recorded.
type edgeFx struct {
	*partWS
	mu      sync.Mutex
	pushes  []string
	stopped []string
	now     time.Time
	events  <-chan events.Event
}

func newEdgeFx(t *testing.T) *edgeFx {
	t.Helper()
	w := partRouteWS(t)
	w.write(map[string]string{
		"apps/pg/scope.json": `{"resources":{"docs":{"type":"kv"},"board":{"type":"kv","shared":true}}}`,
		"apps/q/xbin.json": `{"runtime":"go","partition":["user"],"uses":[{"target":"apps/pg","role":"reader"},{"target":"apps/pu","role":"reader"},` +
			`{"target":"apps/x","role":"reader"},{"target":"res:apps/pg/docs","role":"writer"},{"target":"res:apps/pg/board","role":"writer"}]}`,
		"apps/rs/xbin.json":       `{"runtime":"go","partition":["user"],"uses":[{"target":"res:apps/pg/board","role":"writer"}]}`,
		"apps/rs/backend/main.go": "package main\n",
	})
	w.rescan()
	if err := w.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants,
			registry.Grant{From: "apps/q", Target: "res:apps/pg/docs", Role: "writer"},
			registry.Grant{From: "apps/q", Target: "res:apps/pg/board", Role: "writer"},
			registry.Grant{From: "apps/rs", Target: "res:apps/pg/board", Role: "writer"},
			registry.Grant{From: "apps/pg", Target: "apps/x", Role: "reader"},
			registry.Grant{From: "apps/pg", Target: "apps/pu", Role: "reader"},
			registry.Grant{From: "apps/x", Target: "users/alice/mcp", Role: "reader"})
	}); err != nil {
		t.Fatal(err)
	}
	f := &edgeFx{partWS: w, now: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
	withSeam(t, &consentNow, func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now })
	withSeam(t, &ledgerNow, func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now })
	w.b.SetPartitionEdgeStop(func(tile, dep, part string) {
		f.mu.Lock()
		f.stopped = append(f.stopped, tile+"+"+dep+"/"+part)
		f.mu.Unlock()
	})
	w.b.SetPartitionPush(func(user, kind, title, body, link, collapse string) {
		f.mu.Lock()
		f.pushes = append(f.pushes, user+" "+kind+" "+link+" "+collapse)
		f.mu.Unlock()
	})
	ch, cancel := w.b.Hub.Subscribe(func(e events.Event) bool {
		_, other := e.Data.(audienceEvent) // the mode's and the notices' ops (F7b), not the consent plane's
		return e.Type == "partitions" && !other
	})
	t.Cleanup(cancel)
	f.events = ch
	return f
}

func (f *edgeFx) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func (f *edgeFx) pushed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pushes...)
}

func (f *edgeFx) route(p auth.Principal, target string) Decision {
	f.t.Helper()
	c, ok := f.b.Reg.Component(target)
	if !ok {
		f.t.Fatalf("no %s", target)
	}
	return f.b.Route(p, c, "")
}

// reach is a data-plane reach of res by p, authorized as a reader: the
// reached namespace's pkey, or the refusal.
func (f *edgeFx) reach(p auth.Principal, res string) (string, error) {
	ra, found, err := f.b.reachRes(p, res)
	if err == nil && !found {
		f.t.Fatalf("%s not found", res)
	}
	if err == nil {
		err = f.b.allowAt(p, ra, "reader")
	}
	return ra.pkey, err
}

// ledger is person user's egress ledger of tile as this xbind counts it:
// "kind target" → total over every day.
func (f *edgeFx) ledger(tile, user string) map[string]int64 {
	out := map[string]int64{}
	for _, d := range f.b.ledgerDocs() {
		if d.Tile != tile || d.User != user || !f.b.ledgerLive(d) {
			continue
		}
		for _, kinds := range d.Days {
			for kind, targets := range kinds {
				for target, n := range targets {
					out[kind+" "+target] += n
				}
			}
		}
	}
	return out
}

// covers PD-12 PD-13 PD-14 05§1 05§2 G1 — TestPartitionEdgeMatrix: the edge
// matrix of 05 §1 on the real identity plane, calls (Route) and data
// reaches (reachRes + allowAt) alike, in both settings of the workspace
// policy partitionConsent. Every cell still needs today's grant; a
// partitioned caller's person reaches the same person's partition of a
// partitioned callee only while they can read it — and, with the policy
// on, consented: refused without (a prompt, once a day, only for a
// granted edge), allowed with, refused again from the next call once
// revoked (the caller's instance of the person stopped), and a person
// recreated under the same id inherits nothing. Allowed cross-tile calls
// and reaches land in the caller partition's egress ledger.
func TestPartitionEdgeMatrix(t *testing.T) {
	f := newEdgeFx(t)
	b := f.b
	qAlice, qCarol, qDave := instanceOf("apps/q", "user:alice"), frameOf("apps/q", "carol"), instanceOf("apps/q", "user:dave")
	xInst, pgGlobal, root := instanceOf("apps/x", ""), instanceOf("apps/pg", ""), auth.Principal{Owner: true, Via: "bearer"}
	rsAlice := instanceOf("apps/rs", "user:alice")
	bobQ := personP(t, f.partWS, "bob") // an admin's frame of q may name a deployment
	bobQ.Component, bobQ.Via = "apps/q", "frame"
	pgComp, _ := b.Reg.Component("apps/pg")
	if st, _, _ := f.state("apps/rs"); st != registry.PartitionPartitioned {
		t.Fatalf("apps/rs: %v, want partitioned", st)
	}
	type cell struct {
		name          string
		p             auth.Principal
		target        string
		part, caller  string
		deny          string // a substring of the refusal, in both settings
		needsConsent  bool   // refused while the policy is on and alice hasn't consented
		consentDenial string
	}
	const qpg, qpu = "alice hasn't let apps/q use their apps/pg data", "alice hasn't let apps/q use their apps/pu data"
	cells := []cell{
		// a principal of a non-partitioned tile
		{name: "x → alice's personal tile (both unpartitioned: today)", p: xInst, target: "users/alice/mcp"},
		{name: "x → pg: its global instance", p: xInst, target: "apps/pg", part: "global"},
		{name: "x → pu: no global instance", p: xInst, target: "apps/pu", deny: "apps/pu is partitioned: only partitioned tiles reach its people's data, and it has no global instance"},
		{name: "x → q: no grant", p: xInst, target: "apps/q", deny: "apps/x is not granted access to apps/q"},
		// a user partition of a partitioned tile
		{name: "q as alice → x (a provider: headers only)", p: qAlice, target: "apps/x", caller: "user:alice"},
		{name: "q as alice → pg (the same person)", p: qAlice, target: "apps/pg", part: "user:alice", caller: "user:alice", needsConsent: true, consentDenial: qpg},
		{name: "q as alice → pu (no global: idem)", p: qAlice, target: "apps/pu", part: "user:alice", caller: "user:alice", needsConsent: true, consentDenial: qpu},
		{name: "q as carol → pg: she can't read it", p: qCarol, target: "apps/pg", deny: "carol can't read apps/pg"},
		{name: "q as dave → pg: disabled", p: qDave, target: "apps/pg", deny: "disabled"},
		{name: "q as alice → her personal tile: no grant", p: qAlice, target: "users/alice/mcp", deny: "apps/q is not granted access to users/alice/mcp"},
		// the tile's own principal: its own partition, no edge
		{name: "q as alice → q (a self-call)", p: qAlice, target: "apps/q", part: "user:alice", caller: "user:alice"},
		// view-as: a tile's frame an admin views as alice acts in no partition
		{name: "q's frame viewed as alice → pg", p: auth.Principal{Component: "apps/q", UserID: "alice", Via: "frame", Impersonator: "owner"},
			target: "apps/pg", deny: "view-as can't open it"},
		// the global instance of a partitioned tile
		{name: "pg's global → x", p: pgGlobal, target: "apps/x", caller: "global"},
		{name: "pg's global → pu: no global instance", p: pgGlobal, target: "apps/pu", deny: "it has no global instance"},
		// the root token
		{name: "root → pg: global", p: root, target: "apps/pg", part: "global", caller: "global"},
		{name: "root → pu", p: root, target: "apps/pu", deny: "sign in as a person"},
	}
	run := func(setting string, consented bool) {
		t.Helper()
		for _, c := range cells {
			d := f.route(c.p, c.target)
			deny := c.deny
			if c.needsConsent && b.Policies().PartitionConsent && !consented {
				deny = c.consentDenial
			}
			switch {
			case deny != "":
				if d.Deny == nil || !strings.Contains(d.Deny.Error(), deny) {
					t.Errorf("%s, %s: %+v; want a refusal saying %q", setting, c.name, d, deny)
				}
			case d.Deny != nil:
				t.Errorf("%s, %s: refused: %v", setting, c.name, d.Deny)
			case string(d.Partition) != c.part || string(d.CallerPartition) != c.caller:
				t.Errorf("%s, %s: partition %q caller %q; want %q %q", setting, c.name, d.Partition, d.CallerPartition, c.part, c.caller)
			}
		}
		// people and view-as (addressedPartition: people aren't routed by
		// grants): a person their own partition, if they can read the tile
		for _, c := range []struct {
			name string
			p    auth.Principal
			want string
			deny string
		}{
			{"alice in person", personP(t, f.partWS, "alice"), "user:alice", ""},
			{"bob, an admin: his own", personP(t, f.partWS, "bob"), "user:bob", ""},
			{"carol, no read", personP(t, f.partWS, "carol"), "", "carol can't read apps/pg"},
			{"view-as alice", auth.Principal{UserID: "alice", Via: "session", Impersonator: "owner"}, "", "view-as can't open it"},
		} {
			got, err := b.addressedPartition(c.p, "apps/pg")
			if c.deny != "" && (err == nil || !strings.Contains(err.Error(), c.deny)) || c.deny == "" && (err != nil || string(got) != c.want) {
				t.Errorf("%s, %s: %q %v", setting, c.name, got, err)
			}
		}
		// the data plane: alice's partition of q reaches her own namespace of
		// pg's per-partition kv, and the shared one at today's keys
		pk, err := f.reach(qAlice, "res:apps/pg/docs")
		switch want := f.pkeyOf("alice"); {
		case b.Policies().PartitionConsent && !consented:
			if err == nil || !strings.Contains(err.Error(), qpg) {
				t.Errorf("%s, alice's q → pg's docs: %q %v; want the consent refusal", setting, pk, err)
			}
		case err != nil || pk != want:
			t.Errorf("%s, alice's q → pg's docs: %q %v; want her namespace %q", setting, pk, err, want)
		}
		if _, err := f.reach(qCarol, "res:apps/pg/docs"); err == nil || !strings.Contains(err.Error(), "carol can't read apps/pg") {
			t.Errorf("%s, carol's q → pg's docs: %v", setting, err)
		}
		// a shared resource is no person's data: the grant and the person's
		// read access, never a consent — in both settings, for a tile with a
		// call grant (q) and one holding only the resource's (rs) alike
		for _, p := range []auth.Principal{qAlice, rsAlice} {
			if pk, err := f.reach(p, "res:apps/pg/board"); err != nil || pk != "" {
				t.Errorf("%s, %s → pg's shared board: %q %v; want today's keys", setting, describe(p), pk, err)
			}
		}
		if _, err := f.reach(qCarol, "res:apps/pg/board"); err == nil || !strings.Contains(err.Error(), "carol can't read apps/pg") {
			t.Errorf("%s, carol's q → pg's shared board: %v", setting, err)
		}
		// a deployment beyond pg's primary: its one instance, global — no
		// person's data, so no consent in either setting
		if d := f.b.Route(bobQ, pgComp, "dev"); d.Deny != nil || d.Partition != util.PartitionGlobal || d.CallerPartition != "user:bob" {
			t.Errorf("%s, bob's q frame → pg+dev: %+v", setting, d)
		}
	}

	// the policy off (the default): the grant and read access suffice
	run("off", false)
	if got := f.ledger("apps/q", "alice"); got["edge apps/pg"] != 2 || got["edge apps/pu"] != 1 || got["provider apps/x"] != 1 || len(got) != 3 {
		t.Errorf("alice's ledger of apps/q, the policy off: %v", got)
	}
	if got := f.ledger("apps/q", "carol"); len(got) != 0 {
		t.Errorf("carol's refused edges were counted: %v", got)
	}
	if got := f.ledger("apps/rs", "alice"); len(got) != 0 {
		t.Errorf("a shared resource's reach was counted as an edge: %v", got)
	}
	if got := f.ledger("apps/q", "bob"); got["provider apps/pg+dev"] != 1 || len(got) != 1 {
		t.Errorf("bob's call to pg's deployment beyond its primary: %v, want one provider row naming apps/pg+dev", got)
	}
	if n := len(f.pushed()); n != 0 {
		t.Errorf("the policy off prompted %d time(s)", n)
	}

	// the policy on, no consent: refused, and alice is asked once a day
	partRouteConsent(f.partWS, true)
	run("on, none", false)
	waitFor(t, func() bool { return len(f.pushed()) == 2 }, "the consent prompts (pg and pu)")
	for _, want := range []string{"alice tile.partition-consent xbin/partitions partition-consent:apps/q→apps/pg",
		"alice tile.partition-consent xbin/partitions partition-consent:apps/q→apps/pu"} {
		if !strings.Contains(strings.Join(f.pushed(), "\n"), want) {
			t.Errorf("no push %q in %q", want, f.pushed())
		}
	}
	ev := <-f.events
	if data, _ := ev.Data.(personEvent); ev.Partition != "user:alice" || data["op"] != "consent-needed" || data["from"] != "apps/q" || !data.PersonOnly() {
		t.Errorf("the prompt's event: %+v", ev)
	}
	run("on, none, again", false)
	if n := len(f.pushed()); n != 2 {
		t.Errorf("asked again within a day: %d pushes", n)
	}
	// a tile without a grant makes nobody consent: apps/pu holds none on pg
	if _, err := b.addressedPartition(instanceOf("apps/pu", "user:alice"), "apps/pg"); err == nil {
		t.Error("apps/pu reached alice's pg without her consent")
	}
	if n := len(f.pushed()); n != 2 {
		t.Errorf("an ungranted edge prompted: %q", f.pushed())
	}
	var asked struct {
		Asked []struct{ From, To string } `json:"asked"`
	}
	aliceP := personP(t, f.partWS, "alice")
	decode(t, call(t, b.apiConsentsList, aliceP, "GET", "/partitions/consents", "", nil), 200, &asked)
	if len(asked.Asked) != 2 || asked.Asked[0].To != "apps/pg" {
		t.Errorf("GET consents' asked: %+v", asked.Asked)
	}
	before := f.ledger("apps/q", "alice")

	// alice consents (to both), from her own session: allowed, and counted
	for _, to := range []string{"apps/pg", "apps/pu"} {
		if r := call(t, b.apiConsentsAdd, aliceP, "POST", "/partitions/consents", `{"from":"apps/q","to":"`+to+`"}`, nil); r.Code != 200 {
			t.Fatalf("alice consents to q → %s: %d %s", to, r.Code, r.Body)
		}
	}
	run("on, consented", true)
	if got := f.ledger("apps/q", "alice"); got["edge apps/pg"] != before["edge apps/pg"]+2 || got["edge apps/pu"] != before["edge apps/pu"]+1 {
		t.Errorf("consented edges counted %v (before %v)", got, before)
	}
	decode(t, call(t, b.apiConsentsList, aliceP, "GET", "/partitions/consents", "", nil), 200, &asked)
	if len(asked.Asked) != 0 {
		t.Errorf("GET consents still asks for %+v", asked.Asked)
	}

	// off → on → off keeps the records: read suffices while off, and her
	// consent applies again once the policy returns
	partRouteConsent(f.partWS, false)
	run("off again", true)
	partRouteConsent(f.partWS, true)
	run("on again", true)

	// revoked mid-run: the next call and reach are refused, and q's
	// instance of alice is stopped (its streams into pg go with it)
	if r := call(t, b.apiConsentsRevoke, aliceP, "DELETE", "/partitions/consents?from=apps/q&to=apps/pg", "", nil); r.Code != 200 {
		t.Fatalf("alice revokes q → pg: %d %s", r.Code, r.Body)
	}
	if d := f.route(qAlice, "apps/pg"); d.Deny == nil || d.Deny.Error() != qpg {
		t.Errorf("after the revocation: %+v", d)
	}
	if _, err := f.reach(qAlice, "res:apps/pg/docs"); err == nil {
		t.Error("after the revocation, alice's q still reaches her pg docs")
	}
	f.mu.Lock()
	stopped := strings.Join(f.stopped, ",")
	f.mu.Unlock()
	if stopped != "apps/q+main/user:alice" {
		t.Errorf("the revocation stopped %q", stopped)
	}
	if d := f.route(qAlice, "apps/pu"); d.Deny != nil {
		t.Errorf("revoking pg took pu too: %v", d.Deny)
	}

	// a person recreated under the same id inherits nothing
	st := b.Users
	if _, err := st.Delete("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Upsert(users.User{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/*": users.LevelRead, "users/*": users.LevelRead}}, "password1"); err != nil {
		t.Fatal(err)
	}
	if d := f.route(qAlice, "apps/pu"); d.Deny == nil || d.Deny.Error() != qpu {
		t.Errorf("the recreated alice inherited the old one's consent: %+v", d)
	}
	if got := f.ledger("apps/q", "alice"); len(got) != 0 {
		t.Errorf("the recreated alice sees the old one's ledger: %v", got)
	}
}

// covers PD-13 PD-29 02§8 — the consent routes are a person's own acts:
// tile code (a frame, an instance, a terminal or agent session), view-as and
// the root token are refused; a person consents only while the policy is
// on, only between partitioned tiles they can read, and may revoke in
// either setting.
func TestPartitionConsentPersonOnly(t *testing.T) {
	f := newEdgeFx(t)
	b := f.b
	body := `{"from":"apps/q","to":"apps/pg"}`
	alice := personP(t, f.partWS, "alice")
	for _, p := range []auth.Principal{
		frameOf("apps/q", "alice"),
		instanceOf("apps/q", "user:alice"),
		{Component: "apps/q", UserID: "alice", Via: "terminal"},
		{UserID: "alice", Via: "session", Impersonator: "owner"},
		{Owner: true, Via: "bearer"},
		{Component: "apps/x", Via: "instance"},
	} {
		for _, h := range []struct {
			method string
			fn     http.HandlerFunc
		}{{"GET", b.apiConsentsList}, {"POST", b.apiConsentsAdd}, {"DELETE", b.apiConsentsRevoke}, {"GET", b.apiPartitionLedger}} {
			if r := call(t, h.fn, p, h.method, "/partitions/consents", body, nil); r.Code != 403 || !strings.Contains(r.Body.String(), "a person's own act") {
				t.Errorf("%s %s by %s: %d %s", h.method, "consents/ledger", describe(p), r.Code, r.Body)
			}
		}
	}
	// the policy off: nothing to allow
	if r := call(t, b.apiConsentsAdd, alice, "POST", "/partitions/consents", body, nil); r.Code != 409 || !strings.Contains(r.Body.String(), "policy is off") {
		t.Errorf("a consent with the policy off: %d %s", r.Code, r.Body)
	}
	if _, err := os.Stat(b.consentsBase()); !os.IsNotExist(err) {
		t.Errorf("the policy off wrote consent records: %v", err)
	}
	partRouteConsent(f.partWS, true)
	for _, c := range []struct {
		p    auth.Principal
		body string
		code int
		says string
	}{
		{personP(t, f.partWS, "carol"), body, 403, "carol can't read apps/pg"},
		{alice, `{"from":"apps/q","to":"apps/x"}`, 409, "apps/x doesn't keep each person's data apart"},
		{alice, `{"from":"apps/x","to":"apps/pg"}`, 409, "apps/x doesn't keep each person's data apart"},
		{alice, `{"from":"apps/q","to":"apps/nope"}`, 404, "no tile apps/nope"},
		{alice, `{"from":"apps/q","to":"apps/q"}`, 400, "need {from, to}"},
		{alice, body, 200, `"from":"apps/q"`},
	} {
		if r := call(t, b.apiConsentsAdd, c.p, "POST", "/partitions/consents", c.body, nil); r.Code != c.code || !strings.Contains(r.Body.String(), c.says) {
			t.Errorf("POST %s by %s: %d %s; want %d saying %q", c.body, c.p.UserID, r.Code, r.Body, c.code, c.says)
		}
	}
	// the record: keyed by her uid, naming her and the credential used
	raw, err := os.ReadFile(b.consentPath(mustUID(t, b, "alice")))
	if err != nil {
		t.Fatal(err)
	}
	var doc consentDoc
	if err := json.Unmarshal(raw, &doc); err != nil || doc.User != "alice" || doc.Edges["apps/q→apps/pg"].Via != "session" {
		t.Errorf("the record: %s (%v)", raw, err)
	}
	// revoking works with the policy off too
	partRouteConsent(f.partWS, false)
	if r := call(t, b.apiConsentsRevoke, alice, "DELETE", "/partitions/consents", body, nil); r.Code != 200 || strings.Contains(r.Body.String(), `"from":"apps/q","to":"apps/pg"`) {
		t.Errorf("DELETE with the policy off: %d %s", r.Code, r.Body)
	}
}

// covers S1 PD-13 05§2 — the approval warning, in both settings: a pending
// grant of a partitioned tile on another partitioned tile (a call, or a
// per-partition resource) says whose data its code will reach; nothing for
// a shared resource, an unpartitioned caller or an unpartitioned target.
func TestPartitionGrantWarning(t *testing.T) {
	f := newEdgeFx(t)
	b := f.b
	f.write(map[string]string{
		"apps/r/xbin.json": `{"runtime":"go","partition":["user"],"uses":[{"target":"apps/pg","role":"reader"},` +
			`{"target":"res:apps/pg/docs","role":"reader"},{"target":"res:apps/pg/board","role":"reader"},{"target":"apps/x","role":"reader"}]}`,
		"apps/s/xbin.json": `{"runtime":"go","uses":[{"target":"apps/pg","role":"reader"}]}`,
	})
	f.rescan()
	warnings := func() map[string]string {
		out := map[string]string{}
		for _, pg := range b.Pending() {
			out[pg.From+" "+pg.Target] = pg.Warning
		}
		return out
	}
	off := "apps/r's code — and everyone who can change it — will be able to read and write the apps/pg data of every person who can read apps/pg"
	on := "apps/r's code — and everyone who can change it — will be able to read and write the apps/pg data of every person who allows it"
	for _, c := range []struct {
		setting bool
		want    string
	}{{false, off}, {true, on}} {
		partRouteConsent(f.partWS, c.setting)
		w := warnings()
		for key, want := range map[string]string{"apps/r apps/pg": c.want, "apps/r res:apps/pg/docs": c.want,
			"apps/r res:apps/pg/board": "", "apps/r apps/x": "", "apps/s apps/pg": ""} {
			if got, ok := w[key]; !ok || got != want {
				t.Errorf("policy %v, pending %s: warning %q (listed %v); want %q", c.setting, key, got, ok, want)
			}
		}
	}
}

// covers PD-46 06§6.1 — the ledger's surfaces: a person reads their own
// rows, a tile's managers and admins the tile's per-target totals, admins
// per-person totals; the edges between partitioned tiles, with the people
// who used and allowed each, are the admins' (the Policies tab's turn-on
// confirmation). Counts on an existing row are saved at most once a minute,
// and on Close; a switch that deletes everything takes the tile's ledgers
// and every consent naming it, a dry run or a global-only one nothing.
func TestPartitionLedger(t *testing.T) {
	f := newEdgeFx(t)
	b := f.b
	qAlice := instanceOf("apps/q", "user:alice")
	for range 3 {
		if d := f.route(qAlice, "apps/pg"); d.Deny != nil {
			t.Fatal(d.Deny)
		}
	}
	if d := f.route(instanceOf("apps/q", "user:bob"), "apps/pg"); d.Deny != nil {
		t.Fatal(d.Deny)
	}
	path := b.ledgerPath("apps/q", util.MainDeployment, f.pkeyOf("alice"))
	onDisk := func() int64 {
		d, err := readLedgerDoc(path)
		if err != nil || d == nil {
			t.Fatalf("alice's ledger on disk: %v", err)
		}
		return d.Days["2026-09-30"][LedgerEdge]["apps/pg"]
	}
	if n := onDisk(); n != 1 {
		t.Errorf("on disk after three calls within a minute: %d, want the first (a new row saves at once)", n)
	}
	f.advance(2 * time.Minute)
	f.route(qAlice, "apps/pg")
	if n := onDisk(); n != 4 {
		t.Errorf("on disk a minute later: %d, want 4", n)
	}
	f.route(qAlice, "apps/pg")
	b.flushLedgers()
	if n := onDisk(); n != 5 {
		t.Errorf("on disk after the flush: %d, want 5", n)
	}

	// the person's own rows; an admin's per-person totals
	var got struct {
		Rows   []ledgerRow `json:"rows"`
		Totals []ledgerRow `json:"totals"`
		People []ledgerRow `json:"people"`
	}
	decode(t, call(t, b.apiPartitionLedger, personP(t, f.partWS, "alice"), "GET", "/partitions/ledger", "", nil), 200, &got)
	if len(got.Rows) != 1 || got.Rows[0].Count != 5 || got.Rows[0].Tile != "apps/q" || got.Totals != nil || got.People != nil {
		t.Errorf("alice's ledger: %+v", got)
	}
	got.Rows, got.Totals, got.People = nil, nil, nil
	decode(t, call(t, b.apiPartitionLedger, personP(t, f.partWS, "carol"), "GET", "/partitions/ledger?tile=apps/q", "", nil), 200, &got)
	if len(got.Rows) != 0 || got.Totals != nil || got.People != nil {
		t.Errorf("carol (a reader of apps/q) sees %+v", got)
	}
	decode(t, call(t, b.apiPartitionLedger, personP(t, f.partWS, "bob"), "GET", "/partitions/ledger?tile=apps/q", "", nil), 200, &got)
	if len(got.Totals) != 1 || got.Totals[0].Count != 6 || got.Totals[0].People != 2 || len(got.People) != 2 {
		t.Errorf("bob (an admin) sees %+v", got)
	}

	// the edges, for admins: granted ones, with who used and allowed them
	partRouteConsent(f.partWS, true)
	if r := call(t, b.apiConsentsAdd, personP(t, f.partWS, "alice"), "POST", "/partitions/consents", `{"from":"apps/q","to":"apps/pg"}`, nil); r.Code != 200 {
		t.Fatal(r.Body)
	}
	var edges struct {
		Policy struct{ PartitionConsent bool } `json:"policy"`
		Edges  []partitionEdge                 `json:"edges"`
	}
	if r := call(t, b.apiPartitionEdges, personP(t, f.partWS, "alice"), "GET", "/partitions/edges", "", nil); r.Code != 403 {
		t.Errorf("alice reads the edges: %d", r.Code)
	}
	decode(t, call(t, b.apiPartitionEdges, personP(t, f.partWS, "bob"), "GET", "/partitions/edges?days=30", "", nil), 200, &edges)
	want := []partitionEdge{
		{From: "apps/pg", To: "apps/pu", Granted: true},
		{From: "apps/q", To: "apps/pg", Granted: true, People: 2, Calls: 6, Consented: 1},
		{From: "apps/q", To: "apps/pu", Granted: true},
	}
	if !edges.Policy.PartitionConsent || len(edges.Edges) != len(want) {
		t.Fatalf("the edges: %+v", edges)
	}
	for i := range want {
		if edges.Edges[i] != want[i] {
			t.Errorf("edge %d: %+v, want %+v", i, edges.Edges[i], want[i])
		}
	}
	f.advance(40 * 24 * time.Hour) // outside a 30-day window
	decode(t, call(t, b.apiPartitionEdges, personP(t, f.partWS, "bob"), "GET", "/partitions/edges", "", nil), 200, &edges)
	if edges.Edges[1].People != 0 || edges.Edges[1].Calls != 0 {
		t.Errorf("40 days later the window still counts: %+v", edges.Edges[1])
	}

	// the switch's wipe: a dry run and a global-only switch keep both
	for _, tgt := range []wipeTarget{{Tile: "apps/q", Kind: wipeEverything, DryRun: true}, {Tile: "apps/q", Kind: wipeGlobal}, {Tile: "apps/pg", Kind: wipeGlobal}} {
		sum := &wipeSummary{}
		if err := wipeLedgersHook(b, tgt, sum); err != nil {
			t.Fatal(err)
		}
		if err := wipeConsentsHook(b, tgt, sum); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path); err != nil || !b.consentHolds("alice", "apps/q", "apps/pg") {
		t.Fatalf("a dry or global-only wipe took the ledger (%v) or the consent", err)
	}
	// pg switches: consents naming it go; q switches: its ledgers go
	if err := wipeConsentsHook(b, wipeTarget{Tile: "apps/pg", Kind: wipeEverything}, &wipeSummary{}); err != nil {
		t.Fatal(err)
	}
	if b.consentHolds("alice", "apps/q", "apps/pg") {
		t.Error("pg's switch kept the consent naming it")
	}
	if err := wipeLedgersHook(b, wipeTarget{Tile: "apps/q", Kind: wipeEverything}, &wipeSummary{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("q's switch kept alice's ledger: %v", err)
	}
	if got := f.ledger("apps/q", "alice"); len(got) != 0 {
		t.Errorf("q's switch kept the counted ledger: %v", got)
	}
	if left, _ := filepath.Glob(filepath.Join(b.ledgerTileDir("apps/q"), "*", "*", ledgerFileName)); len(left) != 0 {
		t.Errorf("ledgers left: %v", left)
	}
}

// mustUID is id's uid in the users store.
func mustUID(t *testing.T, b *Broker, id string) string {
	t.Helper()
	u, ok := b.Users.Get(id)
	if !ok || u.UID == "" {
		t.Fatalf("%s has no uid", id)
	}
	return u.UID
}

// decode checks r's status and decodes its body into out.
func decode(t *testing.T, r interface {
	Result() *http.Response
}, code int, out any) {
	t.Helper()
	res := r.Result()
	defer res.Body.Close()
	if res.StatusCode != code {
		t.Fatalf("status %d, want %d", res.StatusCode, code)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}
