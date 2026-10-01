package broker

// partitionmail_test.go — partition mail (plans/partitions/04 §3; PD-15):
// the sender table, inbox privacy, limits and expiry, the doorbell and its
// start rules, restart durability, and the drops (reset, user delete,
// switch).

import (
	"context"
	"encoding/json"
	"fmt"
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

const pgMailManifest = `{"runtime":"go","partition":["user","global"],"partitionMail":"/mailbox"}`

// mailWS is the routing fixture (partRouteWS: the real identity plane)
// with apps/pg declaring its doorbell at /mailbox.
func mailWS(t *testing.T) *partWS {
	t.Helper()
	w := partRouteWS(t)
	w.write(map[string]string{"apps/pg/xbin.json": pgMailManifest})
	w.rescan()
	if st, _, _ := w.state("apps/pg"); st != registry.PartitionPartitioned {
		t.Fatalf("apps/pg: %v", st)
	}
	return w
}

// The fixture's principals as the partition gate hands them over.
func mailFrame(tile, user string) auth.Principal {
	return auth.Principal{Component: tile, UserID: user, Via: "frame", Partition: util.UserPartition(user)}
}

func mailSend(t *testing.T, b *Broker, p auth.Principal, body string) (int, string) {
	t.Helper()
	rec := call(t, b.apiMailSend, p, "POST", "/partitions/mail", body, nil)
	return rec.Code, rec.Body.String()
}

// mailSendOK sends and answers the id.
func mailSendOK(t *testing.T, b *Broker, p auth.Principal, to, topic string, data any) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"to": to, "topic": topic, "data": data})
	code, body := mailSend(t, b, p, string(raw))
	if code != 200 {
		t.Fatalf("%s mails %s: %d %s", p.Component, to, code, body)
	}
	var out struct {
		OK bool   `json:"ok"`
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || !out.OK || out.ID == "" {
		t.Fatalf("send answer %s", body)
	}
	return out.ID
}

type mailPage struct {
	Items []mailItem `json:"items"`
	More  bool       `json:"more"`
}

func mailRead(t *testing.T, b *Broker, p auth.Principal, query string) (int, mailPage, string) {
	t.Helper()
	rec := call(t, b.apiMailList, p, "GET", "/partitions/mail"+query, "", nil)
	var pg mailPage
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &pg); err != nil {
			t.Fatalf("GET: %v %s", err, rec.Body)
		}
	}
	return rec.Code, pg, rec.Body.String()
}

// mailTopics reads p's inbox as "from topic" lines, failing on any error.
func mailTopics(t *testing.T, b *Broker, p auth.Principal) []string {
	t.Helper()
	code, pg, body := mailRead(t, b, p, "")
	if code != 200 {
		t.Fatalf("%+v reads its inbox: %d %s", p, code, body)
	}
	var out []string
	for _, it := range pg.Items {
		out = append(out, it.From+" "+it.Topic)
	}
	return out
}

func mailAckCall(t *testing.T, b *Broker, p auth.Principal, ids ...string) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"ids": ids})
	rec := call(t, b.apiMailAck, p, "POST", "/partitions/mail/ack", string(raw), nil)
	return rec.Code, rec.Body.String()
}

// covers PD-15 S4 04§3 — TestMailRules, the sender table: the tile's global
// instance mails any live reader of the tile (404 "no such person here" for
// anyone else, whatever the reason) or global; a person's partition —
// backend, frame, terminal — mails global only; admins, the root token,
// global's frames, view-as, another tile, a non-primary deployment and every
// delivery principal are refused. from is stamped by xbind whatever the
// body says.
func TestMailRules(t *testing.T) {
	w := mailWS(t)
	b := w.b
	global := instanceOf("apps/pg", "")
	aliceInst, aliceFrame, aliceTerm := partInst("apps/pg", "alice"), mailFrame("apps/pg", "alice"), partTerm("apps/pg", "alice")

	// the global instance → people and itself
	mailSendOK(t, b, global, "user:alice", "to-alice", map[string]string{"x": "1"})
	mailSendOK(t, b, global, "global", "to-self", nil)
	for _, to := range []string{"user:carol", "user:dave", "user:nobody"} { // can't read, disabled, doesn't exist
		if code, body := mailSend(t, b, global, `{"to":"`+to+`","topic":"t"}`); code != 404 || !strings.Contains(body, "no such person here") ||
			strings.Contains(body, "disabled") || strings.Contains(body, "read") {
			t.Errorf("global mails %s: %d %s", to, code, body)
		}
	}
	for _, to := range []string{"user:Alice", "org:x", "", "alice"} {
		if code, body := mailSend(t, b, global, `{"to":"`+to+`","topic":"t"}`); code != 400 {
			t.Errorf("global mails %q: %d %s", to, code, body)
		}
	}

	// a person's partition → global only; from stamped, whatever the body says
	for _, p := range []auth.Principal{aliceInst, aliceFrame, aliceTerm} {
		if code, body := mailSend(t, b, p, `{"to":"global","topic":"from-`+p.Via+`","from":"global","data":{"n":1}}`); code != 200 {
			t.Errorf("alice's %s mails global: %d %s", p.Via, code, body)
		}
		for _, to := range []string{"user:bob", "user:alice"} {
			if code, body := mailSend(t, b, p, `{"to":"`+to+`","topic":"t"}`); code != 403 || !strings.Contains(body, "mails only") {
				t.Errorf("alice's %s mails %s: %d %s", p.Via, to, code, body)
			}
		}
	}
	got := mailTopics(t, b, global)
	want := []string{"global to-self", "user:alice from-instance", "user:alice from-frame", "user:alice from-terminal"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("global's inbox %q, want %q", got, want)
	}

	// everyone else: nothing
	root := auth.Principal{Owner: true}
	outsiders := map[string]auth.Principal{
		"an admin (bob)":           personP(t, w, "bob"),
		"a person (alice)":         personP(t, w, "alice"),
		"the root token":           root,
		"global's frame":           {Component: "apps/pg", Via: "frame"},
		"a root terminal":          {Component: "apps/pg", Via: "terminal"},
		"view-as alice":            {Component: "apps/pg", UserID: "alice", Via: "frame", Impersonator: "bob"},
		"another tile's backend":   instanceOf("apps/q", ""),
		"another tile's frame":     mailFrame("apps/x", "alice"),
		"the dev deployment":       {Component: "apps/pg", Via: "instance", Deployment: "dev"},
		"a cron delivery":          {Component: CronPrincipal, Via: "cron", Partition: util.PartitionGlobal},
		"a bus delivery":           {Component: BusPrincipal, Via: "bus", Partition: util.UserPartition("alice")},
		"a mail doorbell":          mailPrincipal("main", util.UserPartition("alice")),
		"an unpartitioned tile":    instanceOf("apps/x", ""),
		"a tile without global":    partInst("apps/pu", "alice"),
		"another partitioned tile": partInst("apps/q", "alice"),
	}
	for name, p := range outsiders {
		for _, to := range []string{"user:alice", "global"} {
			if code, body := mailSend(t, b, p, `{"to":"`+to+`","topic":"forged"}`); code != 403 {
				t.Errorf("%s mails %s: %d %s", name, to, code, body)
			}
		}
	}
	// the tile's principals acting as global read its inbox, and send
	// nothing: sending as global is its backend's alone (04 §3)
	for _, p := range []auth.Principal{outsiders["global's frame"], outsiders["a root terminal"]} {
		if code, body := mailSend(t, b, p, `{"to":"user:alice","topic":"forged"}`); code != 403 || !strings.Contains(body, "only the global instance's backend") {
			t.Errorf("%s mails as global: %d %s", p.Via, code, body)
		}
	}
	if got := mailTopics(t, b, aliceInst); fmt.Sprint(got) != "[global to-alice]" {
		t.Errorf("alice's inbox after the outsiders: %q", got)
	}
}

// covers PD-15 G1 G2 04§3 — an inbox is its addressee's alone: alice's
// backend, frames and terminals read hers; bob's partition, the global
// instance and every admin credential never see it (403, or their own);
// an ack acts on the caller's own inbox only; admins get counts, never
// content. Paging, ttl expiry, the size limits and the pause.
func TestMailInboxPrivacy(t *testing.T) {
	w := mailWS(t)
	b := w.b
	global := instanceOf("apps/pg", "")
	aliceInst, bobInst := partInst("apps/pg", "alice"), partInst("apps/pg", "bob")
	aliceID := mailSendOK(t, b, global, "user:alice", "dm", "for alice's eyes")
	mailSendOK(t, b, global, "user:bob", "dm", "for bob")
	mailSendOK(t, b, aliceInst, "global", "reply", "alice to global")

	for _, p := range []auth.Principal{aliceInst, mailFrame("apps/pg", "alice"), partTerm("apps/pg", "alice")} {
		code, pg, body := mailRead(t, b, p, "")
		if code != 200 || len(pg.Items) != 1 || pg.Items[0].ID != aliceID || pg.Items[0].From != "global" ||
			string(pg.Items[0].Data) != `"for alice's eyes"` || pg.More {
			t.Errorf("alice's %s reads her inbox: %d %s", p.Via, code, body)
		}
	}
	if got := mailTopics(t, b, bobInst); fmt.Sprint(got) != "[global dm]" {
		t.Errorf("bob's inbox %q", got)
	}
	if code, pg, body := mailRead(t, b, bobInst, ""); code != 200 || strings.Contains(body, "alice") || len(pg.Items) != 1 {
		t.Errorf("bob's partition reads: %d %s", code, body)
	}
	if code, _, body := mailRead(t, b, global, ""); code != 200 || strings.Contains(body, "eyes") || !strings.Contains(body, "alice to global") {
		t.Errorf("global reads: %d %s", code, body)
	}
	for name, p := range map[string]auth.Principal{
		"admin bob":             personP(t, w, "bob"),
		"the root token":        {Owner: true},
		"view-as alice":         {Component: "apps/pg", UserID: "alice", Via: "frame", Impersonator: "bob"},
		"view-as global":        {Component: "apps/pg", Via: "frame", Impersonator: "owner"},
		"another tile":          instanceOf("apps/q", ""),
		"another tile's frame":  frameOf("apps/q", ""),
		"an unpartitioned tile": instanceOf("apps/x", ""),
		"global's dev frame":    {Component: "apps/pg", Via: "frame", Deployment: "dev"},
	} {
		if code, _, body := mailRead(t, b, p, ""); code != 403 {
			t.Errorf("%s reads mail: %d %s", name, code, body)
		}
		if code, body := mailAckCall(t, b, p, aliceID); code != 403 {
			t.Errorf("%s acks alice's mail: %d %s", name, code, body)
		}
	}
	// an ack is the caller's own inbox's: bob's ack of alice's id does nothing
	if code, body := mailAckCall(t, b, bobInst, aliceID); code != 200 {
		t.Fatalf("bob acks: %d %s", code, body)
	}
	if got := mailTopics(t, b, aliceInst); len(got) != 1 {
		t.Errorf("bob's ack took alice's item: %q", got)
	}
	// admins see counts, never content
	counts, err := b.PartitionMailCounts("apps/pg", "")
	if err != nil || counts[w.pkeyOf("alice")].Pending != 1 || counts["global"].Pending != 1 || counts[w.pkeyOf("bob")].Pending != 1 {
		t.Errorf("counts %+v %v", counts, err)
	}
	// a store on disk carries no plaintext once the vault seals it
	if err := b.barrier.Init("mail-pass"); err != nil {
		t.Fatal(err)
	}
	mailSendOK(t, b, global, "user:alice", "sealed", "a sealed secret")
	raw, err := os.ReadFile(filepath.Join(w.root, "data", "partitions", util.TileKey("apps/pg"), "main", "mail.db"))
	if err != nil || strings.Contains(string(raw), "a sealed secret") {
		t.Errorf("the store holds the plaintext (%v)", err)
	}
	if got := mailTopics(t, b, aliceInst); fmt.Sprint(got) != "[global dm global sealed]" {
		t.Errorf("alice's inbox, one item sealed: %q", got)
	}
	// ack deletes
	if code, body := mailAckCall(t, b, aliceInst, aliceID); code != 200 {
		t.Fatalf("alice acks: %d %s", code, body)
	}
	if got := mailTopics(t, b, aliceInst); fmt.Sprint(got) != "[global sealed]" {
		t.Errorf("after the ack: %q", got)
	}
	if code, _ := mailAckCall(t, b, aliceInst, "nope"); code != 400 {
		t.Errorf("a malformed id: %d", code)
	}

}

// covers 04§3 — TestMailGlobalInboxReaders (the owner's answer of
// 2026-09-30): the global instance's inbox is read and acknowledged by its
// backend and by the tile's principals acting as global — the owner token's
// frames, root terminals and agent sessions — on the primary; a person's
// frame, terminal or partition (an admin's included) reads only their own;
// they send nothing as global (TestMailRules).
func TestMailGlobalInboxReaders(t *testing.T) {
	w := mailWS(t)
	b := w.b
	global := instanceOf("apps/pg", "")
	mailSendOK(t, b, global, "global", "to-self", nil)
	mailSendOK(t, b, partInst("apps/pg", "alice"), "global", "from-alice", "alice to global")
	aliceID := mailSendOK(t, b, global, "user:alice", "dm", "for alice's eyes")
	readers := map[string]auth.Principal{
		"the owner token's frame": {Component: "apps/pg", Via: "frame", Gen: "o.gen"},
		"a root terminal":         {Component: "apps/pg", Via: "terminal"},
		"an agent session":        {Component: "apps/pg", Via: "terminal", Deployment: util.MainDeployment},
	}
	for name, p := range readers {
		got := mailTopics(t, b, p)
		if fmt.Sprint(got) != "[global to-self user:alice from-alice]" {
			t.Errorf("%s reads global's inbox: %q", name, got)
		}
		if code, body := mailAckCall(t, b, p, aliceID); code != 200 { // not its inbox's: nothing to do
			t.Errorf("%s acks alice's id: %d %s", name, code, body)
		}
	}
	if got := mailTopics(t, b, partInst("apps/pg", "alice")); fmt.Sprint(got) != "[global dm]" {
		t.Errorf("an ack from global's side took alice's item: %q", got)
	}
	// a person's credentials on the tile read their own inbox, never global's
	for name, p := range map[string]auth.Principal{
		"alice's frame":      mailFrame("apps/pg", "alice"),
		"alice's terminal":   partTerm("apps/pg", "alice"),
		"admin bob's frame":  mailFrame("apps/pg", "bob"),
		"admin bob's shell":  partTerm("apps/pg", "bob"),
		"bob's own instance": partInst("apps/pg", "bob"),
	} {
		code, _, body := mailRead(t, b, p, "")
		if code != 200 || strings.Contains(body, "to-self") || strings.Contains(body, "from-alice") {
			t.Errorf("%s reads: %d %s", name, code, body)
		}
	}
	// the owner token's frame acknowledges: gone for the backend too
	_, pg, _ := mailRead(t, b, global, "")
	if code, body := mailAckCall(t, b, readers["the owner token's frame"], pg.Items[0].ID); code != 200 {
		t.Fatalf("the owner token's frame acks: %d %s", code, body)
	}
	if got := mailTopics(t, b, global); fmt.Sprint(got) != "[user:alice from-alice]" {
		t.Errorf("global's inbox after the frame's ack: %q", got)
	}
}

// the limits: 1 MiB an item (413), an inbox's items and bytes (507 to the
// sender), the ttl range, paging; expired items are dropped and counted.
func TestMailLimitsAndExpiry(t *testing.T) {
	w := mailWS(t)
	b := w.b
	global, aliceInst := instanceOf("apps/pg", ""), partInst("apps/pg", "alice")
	big := strings.Repeat("x", mailItemMax)
	if code, body := mailSend(t, b, global, `{"to":"user:alice","topic":"big","data":"`+big+`"}`); code != 413 {
		t.Errorf("an item over 1 MiB: %d %.200s", code, body)
	}
	for _, ttl := range []string{"0", "-1", "2592001"} {
		if code, body := mailSend(t, b, global, `{"to":"user:alice","topic":"t","ttl":`+ttl+`}`); code != 400 {
			t.Errorf("ttl %s: %d %s", ttl, code, body)
		}
	}
	if code, body := mailSend(t, b, global, `{"to":"user:alice","topic":"a\u0000b"}`); code != 400 {
		t.Errorf("a topic with a control character: %d %s", code, body)
	}
	if k := knobs(); k.inboxItems != 1000 || k.inboxBytes != 64<<20 || fmt.Sprint(k.backoff) != "[1m0s 5m0s 30m0s 2h0m0s 6h0m0s]" {
		t.Errorf("the knobs %+v; 04 §3 says 1000 items, 64 MiB and 1 min, 5 min, 30 min, 2 h, then 6 h", k)
	}
	withMailKnobs(t, func(k *mailKnobs) { k.inboxItems = 3 })
	var ids []string
	for i := 0; i < 3; i++ {
		ids = append(ids, mailSendOK(t, b, global, "user:alice", fmt.Sprint("n", i), i))
	}
	if code, body := mailSend(t, b, global, `{"to":"user:alice","topic":"full"}`); code != 507 || !strings.Contains(body, "full") {
		t.Errorf("a full inbox: %d %s", code, body)
	}
	// paging, in arrival order
	code, pg, _ := mailRead(t, b, aliceInst, "?limit=2")
	if code != 200 || len(pg.Items) != 2 || !pg.More || pg.Items[0].ID != ids[0] || pg.Items[1].ID != ids[1] {
		t.Errorf("page 1: %d %+v", code, pg)
	}
	code, pg, _ = mailRead(t, b, aliceInst, "?limit=2&after="+ids[1])
	if code != 200 || len(pg.Items) != 1 || pg.More || pg.Items[0].ID != ids[2] {
		t.Errorf("page 2: %d %+v", code, pg)
	}
	if code, _, _ := mailRead(t, b, aliceInst, "?limit=0"); code != 400 {
		t.Errorf("limit 0: %d", code)
	}
	if code, _, _ := mailRead(t, b, aliceInst, "?after=zz"); code != 400 {
		t.Errorf("a malformed after: %d", code)
	}
	withMailKnobs(t, func(k *mailKnobs) { k.inboxBytes = 200 })
	if code, body := mailSend(t, b, global, `{"to":"user:alice","topic":"bytes","data":"`+strings.Repeat("y", 300)+`"}`); code != 507 {
		t.Errorf("an inbox over its bytes: %d %s", code, body)
	}
	withMailKnobs(t, func(*mailKnobs) {})

	// expiry: a 1-second item and the default ttl's, 8 days on
	mailSendOK(t, b, global, "global", "short", nil)
	if code, body := mailSend(t, b, global, `{"to":"user:alice","topic":"short","ttl":1}`); code != 200 {
		t.Fatalf("ttl 1: %d %s", code, body)
	}
	withMailKnobs(t, func(k *mailKnobs) { k.now = func() time.Time { return time.Now().Add(2 * time.Second) } })
	if got := mailTopics(t, b, aliceInst); len(got) != 3 {
		t.Errorf("after 2 s: %q, want the three 7-day items", got)
	}
	withMailKnobs(t, func(k *mailKnobs) { k.now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) } })
	if got := mailTopics(t, b, aliceInst); len(got) != 0 {
		t.Errorf("after 8 days: %q", got)
	}
	counts, _ := b.PartitionMailCounts("apps/pg", "main")
	if c := counts[w.pkeyOf("alice")]; c.Expired != 4 || c.Pending != 0 {
		t.Errorf("alice's counts after expiry %+v", c)
	}
}

// covers PD-44 01§2.3 — mail makes the tile hold data, so a manifest change
// is a request, not an auto flip; while it is pending every mail act answers
// 409; a switch removes the tile's whole store (a dry run only counts), and
// removing "global" deletes only global's inbox (H1).
func TestMailSwitchRemovesStore(t *testing.T) {
	w := mailWS(t)
	b := w.b
	global, aliceInst := instanceOf("apps/pg", ""), partInst("apps/pg", "alice")
	store := filepath.Join(w.root, "data", "partitions", util.TileKey("apps/pg"), "main", "mail.db")
	ask := registry.PartitionAsk{Tile: "apps/pg", Scope: "apps/pg", RootsScope: true}
	if held, _ := holdsPartitionMail(b, ask); held {
		t.Fatal("a tile without mail holds mail")
	}
	mailSendOK(t, b, global, "user:alice", "dm", 1)
	mailSendOK(t, b, aliceInst, "global", "up", 2)
	if held, err := holdsPartitionMail(b, ask); !held || err != nil {
		t.Fatalf("holds mail: %v %v", held, err)
	}

	// removing "global": its inbox only
	var sum wipeSummary
	if err := wipePartitionMail(b, wipeTarget{Tile: "apps/pg", Kind: wipeGlobal}, &sum); err != nil {
		t.Fatal(err)
	}
	if got := mailTopics(t, b, aliceInst); len(got) != 1 {
		t.Errorf("removing global took alice's inbox: %q", got)
	}
	if got := mailTopics(t, b, global); len(got) != 0 {
		t.Errorf("global's inbox after removing global: %q", got)
	}
	// a dry run counts; the switch's own flow removes the store. A person
	// is told only when their partition exists: bob, mailed by global but
	// never on the tile, had none to lose
	mailSendOK(t, b, global, "user:bob", "dm", 3)
	sum = wipeSummary{}
	if err := wipePartitionMail(b, wipeTarget{Tile: "apps/pg", Kind: wipeEverything, DryRun: true}, &sum); err != nil || sum.Bytes == 0 ||
		len(sum.People) != 0 {
		t.Errorf("dry run, no partition recorded: %+v %v", sum, err)
	}
	u, _ := b.Users.Get("alice")
	b.notePartitionStart("apps/pg", "main", "user:alice", w.pkeyOf("alice"), u.UID)
	sum = wipeSummary{}
	if err := wipePartitionMail(b, wipeTarget{Tile: "apps/pg", Kind: wipeEverything, DryRun: true}, &sum); err != nil || sum.Bytes == 0 ||
		fmt.Sprint(sum.People) != "[alice]" {
		t.Errorf("dry run: %+v %v", sum, err)
	}
	if _, err := os.Stat(store); err != nil {
		t.Fatal("the dry run removed the store")
	}

	old := partitionIsolated
	partitionIsolated = func() bool { return true }
	t.Cleanup(func() { partitionIsolated = old })
	w.write(map[string]string{"apps/pg/xbin.json": `{"runtime":"go"}`})
	w.rescan()
	if st, _, _ := w.state("apps/pg"); st != registry.PartitionPending {
		t.Fatalf("a manifest change on a tile holding mail: %v, want pending", st)
	}
	for name, code := range map[string]int{
		"send": call(t, b.apiMailSend, aliceInst, "POST", "/partitions/mail", `{"to":"global","topic":"t"}`, nil).Code,
		"read": call(t, b.apiMailList, aliceInst, "GET", "/partitions/mail", "", nil).Code,
		"ack":  call(t, b.apiMailAck, aliceInst, "POST", "/partitions/mail/ack", `{"ids":[]}`, nil).Code,
	} {
		if code != 409 {
			t.Errorf("%s while pending: %d", name, code)
		}
	}
	rec := call(t, b.apiPartitionMode, auth.Principal{Owner: true}, "POST", "/partitions/mode",
		`{"tile":"apps/pg","act":"switch","from":{"user":true,"global":true},"to":null,"confirm":"apps/pg"}`, nil)
	if rec.Code != 200 {
		t.Fatalf("switch: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Errorf("the mail store after the switch: %v", err)
	}
	if held, _ := holdsPartitionMail(b, ask); held {
		t.Error("the tile holds mail after the switch")
	}
}

// covers PD-26 PD-43 03§B.9 — a person's reset or purge (DropPartition)
// and their deletion remove their inbox and nobody else's; a person created
// again under the id never reads the old incarnation's mail; a new tile at
// a removed tile's path never reads its store (pathLeftovers lists it until
// then).
func TestMailDrops(t *testing.T) {
	w := mailWS(t)
	b := w.b
	global := instanceOf("apps/pg", "")
	mailSendOK(t, b, global, "user:alice", "a", 1)
	mailSendOK(t, b, global, "user:bob", "b", 1)
	mailSendOK(t, b, global, "global", "g", 1)
	if err := b.DropPartition("apps/pg", "main", w.pkeyOf("alice")); err != nil {
		t.Fatal(err)
	}
	if got := mailTopics(t, b, partInst("apps/pg", "alice")); len(got) != 0 {
		t.Errorf("alice's inbox after her reset: %q", got)
	}
	if got := mailTopics(t, b, partInst("apps/pg", "bob")); len(got) != 1 {
		t.Errorf("bob's inbox after alice's reset: %q", got)
	}
	// deletion: bob's inbox goes at once; a new bob reads nothing of it
	u, _ := b.Users.Get("bob")
	if _, err := b.Users.Delete("bob"); err != nil {
		t.Fatal(err)
	}
	b.PartitionUserDeleted("bob", u.UID)
	counts, _ := b.PartitionMailCounts("apps/pg", "")
	if _, ok := counts[util.PartitionKey("bob", u.UID)]; ok {
		t.Errorf("bob's inbox after his deletion: %+v", counts)
	}
	if len(counts) != 1 {
		t.Errorf("counts after the deletions: %+v (global's only)", counts)
	}
	if _, err := b.Users.Upsert(users.User{ID: "bob", Role: users.RoleAdmin}, "password1"); err != nil {
		t.Fatal(err)
	}
	if uid, err := b.mintPartitionUID("bob"); err != nil || uid == u.UID {
		t.Fatalf("the new bob's uid %q (%v), the old one's %q", uid, err, u.UID)
	}
	if got := mailTopics(t, b, partInst("apps/pg", "bob")); len(got) != 0 {
		t.Errorf("BUG: a new bob reads the old one's mail: %q", got)
	}
	mailSendOK(t, b, global, "user:bob", "hello-again", 1)
	if got := mailTopics(t, b, partInst("apps/pg", "bob")); fmt.Sprint(got) != "[global hello-again]" {
		t.Errorf("the new bob's inbox: %q", got)
	}

	// leftovers: a removed tile's store, until a tile is created there
	if err := os.Remove(filepath.Join(w.root, "apps/pg/xbin.json")); err != nil {
		t.Fatal(err)
	}
	w.rescan()
	gone := func(tile string) bool { _, ok := b.Reg.Component(tile); return tile == "apps/pg" && !ok }
	if left := b.mailLeftovers(gone); len(left) != 1 || !strings.Contains(left[0], "apps/pg's partition mail") {
		t.Errorf("leftovers %q", left)
	}
	b.mailTileCreated("apps/pg")
	if left := b.mailLeftovers(gone); len(left) != 0 {
		t.Errorf("leftovers after a tile is created there: %q", left)
	}
}

// ---- the doorbell ----

// withMailKnobs stands a set of knobs in for the test: the defaults, as
// set changes them.
func withMailKnobs(t *testing.T, set func(*mailKnobs)) {
	t.Helper()
	k := mailDefaults
	set(&k)
	prev := mailKnobsNow.Swap(&k)
	t.Cleanup(func() { mailKnobsNow.Store(prev) })
}

type bellCall struct {
	p          auth.Principal
	comp, path string
	body       map[string]any
}

// fakeMailDispatch records doorbells; answer, when set, runs in the
// delivery (an ack, say) and gives its status.
func fakeMailDispatch(t *testing.T, b *Broker, answer func(bellCall) int) chan bellCall {
	t.Helper()
	calls := make(chan bellCall, 64)
	ms := b.mail()
	ms.bellMu.Lock()
	ms.dispatch = func(_ context.Context, p auth.Principal, comp, path string, body []byte) (int, string) {
		c := bellCall{p: p, comp: comp, path: path}
		_ = json.Unmarshal(body, &c.body)
		calls <- c
		if answer != nil {
			return answer(c), ""
		}
		return 200, ""
	}
	ms.bellMu.Unlock()
	withMailKnobs(t, func(k *mailKnobs) {
		k.backoff, k.startDelay, k.bootDelay = []time.Duration{80 * time.Millisecond}, 10*time.Millisecond, 10*time.Millisecond
	})
	t.Cleanup(func() { b.forgetBellsOf("apps/pg") })
	return calls
}

func takeBell(t *testing.T, calls chan bellCall, what string) bellCall {
	t.Helper()
	select {
	case c := <-calls:
		return c
	case <-time.After(5 * time.Second):
		t.Fatalf("no doorbell: %s", what)
	}
	return bellCall{}
}

func noBell(t *testing.T, calls chan bellCall, what string) {
	t.Helper()
	select {
	case c := <-calls:
		t.Errorf("a doorbell %s: %+v", what, c)
	case <-time.After(300 * time.Millisecond):
	}
}

// covers PD-15 PD-18 PD-20 S1 S20 04§3 — the doorbell: a new item for a
// person whose partition never ran rings nothing (mail never makes a
// person's first instance); their partition's start rings it, as
// Principal{xbin/mail, writer, their partition} on the declared path; it
// re-rings with backoff until the items are acked, and a partition that
// has run before is rung (so started) at once; a disabled person's inbox
// waits; global's inbox rings the global instance; a tile without
// partitionMail rings nothing. Route sends a doorbell as a mail start.
func TestMailDoorbell(t *testing.T) {
	w := mailWS(t)
	b := w.b
	global, aliceInst := instanceOf("apps/pg", ""), partInst("apps/pg", "alice")
	calls := fakeMailDispatch(t, b, nil)

	mailSendOK(t, b, global, "user:alice", "dm", 1)
	noBell(t, calls, "for a partition that never ran")

	pk := w.pkeyOf("alice")
	u, _ := b.Users.Get("alice")
	b.notePartitionStart("apps/pg", "main", "user:alice", pk, u.UID) // her first start
	c := takeBell(t, calls, "at alice's start")
	want := auth.Principal{Component: "xbin/mail", Via: "mail", Role: "writer", Partition: "user:alice"}
	if c.p != want || c.comp != "apps/pg" || c.path != "/mailbox" || c.body["partition"] != "user:alice" || c.body["pending"] != float64(1) {
		t.Errorf("the doorbell: %+v", c)
	}
	takeBell(t, calls, "re-rung while unacked")
	// acked: it stops
	code, pg, _ := mailRead(t, b, aliceInst, "")
	if code != 200 || len(pg.Items) != 1 {
		t.Fatal(pg)
	}
	if code, _ := mailAckCall(t, b, aliceInst, pg.Items[0].ID); code != 200 {
		t.Fatal(code)
	}
	for len(calls) > 0 {
		<-calls
	}
	noBell(t, calls, "after the ack")

	// she has run before: a new item rings at once (the delivery starts her)
	mailSendOK(t, b, global, "user:alice", "dm2", 2)
	if c := takeBell(t, calls, "a new item"); c.body["pending"] != float64(1) {
		t.Errorf("%+v", c)
	}
	b.forgetBellsOf("apps/pg")
	for len(calls) > 0 {
		<-calls
	}

	// a disabled person's inbox waits
	u.Disabled = true
	if _, err := b.Users.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	mailSendOK(t, b, global, "global", "g", 1) // global's still rings
	if c := takeBell(t, calls, "global's inbox"); c.p.Partition != util.PartitionGlobal || c.body["partition"] != "global" {
		t.Errorf("global's doorbell: %+v", c)
	}
	b.forgetBellsOf("apps/pg")
	for len(calls) > 0 {
		<-calls
	}
	b.ringMail(mailBellKey{"apps/pg", "main", pk}, ringNew, time.Time{})
	noBell(t, calls, "for a disabled person")

	// a doorbell is a mail delivery to Route: a background start of class mail
	c0, _ := b.Reg.Component("apps/pg")
	u.Disabled = false
	if _, err := b.Users.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	if d := b.Route(mailPrincipal("main", util.UserPartition("alice")), c0, ""); d.Deny != nil || d.Delivery != "mail" ||
		d.Partition != "user:alice" || d.Role != "writer" {
		t.Errorf("Route of a doorbell: %+v", d)
	}

	// no partitionMail: its code polls; nothing rings
	b.forgetBellsOf("apps/pg")
	w.write(map[string]string{"apps/pg/xbin.json": `{"runtime":"go","partition":["user","global"]}`})
	w.rescan()
	for len(calls) > 0 {
		<-calls
	}
	mailSendOK(t, b, global, "global", "g2", 1)
	noBell(t, calls, "without partitionMail")
}

// covers 04§3 — durability: mail survives a restart of xbind between send
// and ack, and boot rings every inbox holding items.
func TestMailRestartDurable(t *testing.T) {
	w := mailWS(t)
	global := instanceOf("apps/pg", "")
	mailSendOK(t, w.b, global, "user:alice", "before", "the restart")
	mailSendOK(t, w.b, partInst("apps/pg", "alice"), "global", "up", 1)
	users := w.b.Users
	w.b.Close()

	reg := &registry.Registry{Root: w.root}
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	b2, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b2.Close)
	b2.AllowInsecureVault = true
	b2.Users = users
	b2.PrimaryOf, b2.DeploymentExists, b2.AddressedDeployment = w.b.PrimaryOf, w.b.DeploymentExists, w.b.AddressedDeployment
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if got := mailTopics(t, b2, partInst("apps/pg", "alice")); fmt.Sprint(got) != "[global before]" {
		t.Errorf("alice's inbox after the restart: %q", got)
	}
	calls := fakeMailDispatch(t, b2, nil)
	b2.loadMailBells()
	if c := takeBell(t, calls, "at boot"); c.p.Partition != util.PartitionGlobal {
		t.Errorf("boot rang %+v first; alice never ran, so only global's rings", c)
	}
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case c := <-calls:
			if c.p.Partition != util.PartitionGlobal {
				t.Errorf("boot rang alice's never-run partition: %+v", c)
			}
			continue
		case <-deadline:
		}
		break
	}
}

// covers 06§6.1 S10 — a private trigger's event: the global instance's
// mail to a person naming its source counts one "trigger" row in that
// person's ledger; nothing else counts, and never the content.
func TestMailTriggerLedger(t *testing.T) {
	w := mailWS(t)
	b := w.b
	var mu sync.Mutex
	var rows []string
	old := ledgerNow
	t.Cleanup(func() { ledgerNow = old })
	global := instanceOf("apps/pg", "")
	if code, body := mailSend(t, b, global, `{"to":"user:alice","topic":"handoff/event","data":{"secret":1},"source":"apps/webhooks#gh"}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := mailSend(t, b, partInst("apps/pg", "alice"), `{"to":"global","topic":"x","source":"ignored"}`); code != 200 {
		t.Fatal(code, body)
	}
	b.flushLedgers()
	for _, d := range b.ledgerDocs() {
		for _, kinds := range d.Days {
			for kind, targets := range kinds {
				for target, n := range targets {
					mu.Lock()
					rows = append(rows, fmt.Sprintf("%s %s %s %d", d.User, kind, target, n))
					mu.Unlock()
				}
			}
		}
	}
	if fmt.Sprint(rows) != "[alice trigger apps/webhooks#gh 1]" {
		t.Errorf("ledger rows %q", rows)
	}
	if code, body := mailSend(t, b, global, `{"to":"user:alice","topic":"t","source":"a\nb"}`); code != 400 {
		t.Errorf("a source with a newline: %d %s", code, body)
	}
}

// covers 01§2.2 01§2.6 — a mail store this xbind can't read holds data (the
// rule leans toward asking), and a switch that deletes everything removes
// it without failing.
func TestMailUnreadableStore(t *testing.T) {
	w := mailWS(t)
	b := w.b
	store := filepath.Join(w.root, "data", "partitions", util.TileKey("apps/pg"), "main", "mail.db")
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, []byte("not a bbolt file"), 0o600); err != nil {
		t.Fatal(err)
	}
	ask := registry.PartitionAsk{Tile: "apps/pg", Scope: "apps/pg", RootsScope: true}
	if held, err := holdsPartitionMail(b, ask); !held || err == nil {
		t.Errorf("an unreadable store: held %v, err %v", held, err)
	}
	var sum wipeSummary
	if err := wipePartitionMail(b, wipeTarget{Tile: "apps/pg", Kind: wipeEverything}, &sum); err != nil {
		t.Errorf("the switch's wipe of an unreadable store: %v", err)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Errorf("the unreadable store after the wipe: %v", err)
	}
	if held, err := holdsPartitionMail(b, ask); held || err != nil {
		t.Errorf("after the wipe: held %v, err %v", held, err)
	}
}
