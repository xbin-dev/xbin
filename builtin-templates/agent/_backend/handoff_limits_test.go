package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestPrivateAutomationsAtGlobal: a person's call from their own partition
// to the global instance (xbin.fetch(…, {partition: 'global'})) can't keep a
// private automation there — so no one can pass the registry's rules by
// making a private push trigger at global that quietly takes everyone's
// pushes. A switch stays theirs; the tile's own principals are unchanged.
func TestPrivateAutomationsAtGlobal(t *testing.T) {
	setMode(t, modeGlobal, "")
	gAg, gMux := chanFixture(t)
	_ = gAg.db.addHandoffSchema()
	mail := stubMail(t)
	if w := serveAs(gMux, "POST", "/triggers/registry",
		`{"name":"a-deploys","source":"push","sourceRef":"apps/webhooks","match":"deploy/","enabled":true,"maxPerHour":30}`, asPartition("alice")); w.Code != 200 {
		t.Fatalf("alice registers: %d %s", w.Code, w.Body)
	}
	trig := func(name, vis, match string) string {
		return fmt.Sprintf(`{"name":%q,"source":"push","sourceRef":"apps/webhooks","match":%q,"goal":"read {{topic}}","toolset":"web","dataClass":"public","visibility":%q}`,
			name, match, vis)
	}
	for name, body := range map[string]string{
		"a catch-all":     trig("b-all", "private", ""),
		"over alice's":    trig("b-prod", "private", "deploy/prod"),
		"private, unsaid": trig("b-zz", "", "zz/"),
	} {
		if w := serveAs(gMux, "POST", "/triggers", body, asPartition("bob")); w.Code != 409 || !strings.Contains(w.Body.String(), "your own space") {
			t.Errorf("bob's %s at global: %d %s", name, w.Code, w.Body)
		}
	}
	if n := len(gAg.db.listTriggers(`WHERE owner='bob'`)); n != 0 {
		t.Fatalf("bob has %d triggers at global", n)
	}
	if w := serveAs(gMux, "POST", "/triggers", trig("b-team", "team", "team/"), asPartition("bob")); w.Code != 200 {
		t.Fatalf("a team trigger at global (the shared space's): %d %s", w.Code, w.Body)
	}
	// alice's push goes to alice, and runs nothing at global
	if _, res := pushEvent(t, gMux, "apps/webhooks", map[string]any{"topic": "deploy/prod", "eventId": "ev1", "data": map[string]string{"secret": "alice's"}}); len(res) != 1 || !res[0].Accepted {
		t.Fatalf("alice's push: %+v", res)
	}
	if sent := mail.wait(t, 1); sent[0].to != "user:alice" {
		t.Fatalf("the push went to %s", sent[0].to)
	}
	var runs int
	_ = gAg.db.q.QueryRow(`SELECT count(*) FROM runs`).Scan(&runs)
	if runs != 0 {
		t.Fatalf("a push for alice's trigger started %d runs at global", runs)
	}
	// schedules: a private one is made in their own space
	if w := serveAs(gMux, "POST", "/schedules", `{"name":"s","cron":"@every 1h","goal":"g"}`, asPartition("bob")); w.Code != 409 {
		t.Fatalf("bob's private schedule at global: %d %s", w.Code, w.Body)
	}
	// bob's private trigger and schedule from before the agent was
	// partitioned: switched from his partition, never edited there
	old := Trigger{Name: "b-old", Source: "push", SourceRef: "apps/other", Match: "o/", Goal: "g", Mode: "isolated", Toolset: "private",
		DataClass: "private", Owner: "bob", Visibility: visPrivate, Enabled: true, MaxPerHour: 30}
	if err := gAg.db.saveTrigger(&old); err != nil {
		t.Fatal(err)
	}
	sid, _ := gAg.db.createSchedule(&Schedule{Name: "s-old", Cron: "@every 1h", Goal: "g", Owner: "bob", Visibility: visPrivate})
	for path, body := range map[string]string{fmt.Sprintf("/triggers/%d", old.ID): `{"goal":"steal"}`, fmt.Sprintf("/schedules/%d", sid): `{"goal":"steal"}`} {
		if w := serveAs(gMux, "PUT", path, body, asPartition("bob")); w.Code != 409 {
			t.Errorf("bob edits his old %s at global: %d %s", path, w.Code, w.Body)
		}
		if w := serveAs(gMux, "PUT", path, `{"enabled":false}`, asPartition("bob")); w.Code != 200 {
			t.Errorf("bob switches his old %s off: %d %s", path, w.Code, w.Body)
		}
	}
	// the tile's own principals keep what they had
	if w := callAs(t, gMux, asMgr, "POST", "/triggers", json.RawMessage(trig("m-own", "private", "m/"))); w.Code != 200 {
		t.Fatalf("a manager's own private trigger at global, from its frame: %d %s", w.Code, w.Body)
	}
}

// TestHandoffPerPersonBackoff: one person's full inbox holds back only their
// handoffs (in order, behind the one that waits) — everyone else's are
// mailed; what waited longer than handoffQueueTTL is given up (the chat
// told, the content and staged file gone); a queued DM's staged file
// outlives the upload prune.
func TestHandoffPerPersonBackoff(t *testing.T) {
	setMode(t, modeGlobal, "")
	gAg, gMux := chanFixture(t)
	_ = gAg.db.addHandoffSchema()
	mail := stubMail(t)
	var carolFull atomic.Bool
	carolFull.Store(true)
	inner := sendMail
	sendMail = func(ctx context.Context, to, topic string, data any, source string) (string, error) {
		if to == "user:carol" && carolFull.Load() {
			return "", fmt.Errorf("partition mail: HTTP 507: the inbox of user:carol is full")
		}
		return inner(ctx, to, topic, data, source)
	}
	ch := helloAs(t, gMux, "apps/slack", "T1")
	claim(t, gMux, ch, map[string]any{"dm": map[string]any{"policy": "linked"}})
	for _, p := range []string{"carol", "bob"} {
		_, _ = gAg.db.q.Exec(`INSERT INTO channel_peers (channel_id, peer_id, name, state, created, xbin_user, linked_at) VALUES (?, ?, ?, 'allowed', ?, ?, ?)`,
			ch, "ho-"+p, p, now(), p, now())
		gAg.db.markRan(p)
	}
	chPost(t, gMux, chMsg(ch, "dm", "Dc", "ho-carol", "carol one"))
	chPost(t, gMux, chMsg(ch, "dm", "Db", "ho-bob", "bob one"))
	mail.wait(t, 1)
	chPost(t, gMux, chMsg(ch, "dm", "Dc", "ho-carol", "carol two")) // waits behind her first
	chPost(t, gMux, chMsg(ch, "dm", "Db", "ho-bob", "bob two"))
	sent := mail.wait(t, 2)
	for i, s := range sent {
		if s.to != "user:bob" {
			t.Fatalf("mail %d went to %s while carol's inbox is full", i, s.to)
		}
	}
	var queued, tries int
	_ = gAg.db.q.QueryRow(`SELECT count(*), max(tries) FROM handoffs WHERE person='carol' AND state='queued'`).Scan(&queued, &tries)
	if queued != 2 || tries < 1 {
		t.Fatalf("carol's handoffs: %d queued, tries %d", queued, tries)
	}
	carolFull.Store(false)
	sent = mail.wait(t, 4)
	var order []string
	for _, s := range sent[2:] {
		var h dmHandoff
		_ = json.Unmarshal(s.data, &h)
		order = append(order, s.to+":"+h.Text)
	}
	if strings.Join(order, ",") != "user:carol:carol one,user:carol:carol two" {
		t.Fatalf("carol's handoffs, once her inbox took them: %v", order)
	}

	// a queued DM's staged file outlives the upload prune; past the queue's
	// life the DM is given up — the file goes, the chat is told
	carolFull.Store(true)
	_, _ = gAg.db.q.Exec(`INSERT INTO channel_files (id, channel_id, name, mime, size, content, blob, created) VALUES
		('fheld', ?, 'held.txt', 'text/plain', 4, 'held', '', ?), ('fstale', ?, 'stale.txt', 'text/plain', 5, 'stale', '', ?)`,
		ch, now()-2*86400, ch, now()-2*86400)
	payload, _ := json.Marshal(dmHandoff{Handoff: "hold", Channel: ch, Session: "chan:x", Text: "old", Staged: []string{"fheld"}})
	_, _ = gAg.db.q.Exec(`INSERT INTO handoffs (id, kind, person, channel_id, peer_id, session_key, address, created, payload, next_try)
		VALUES ('hold', 'dm', 'carol', ?, 'ho-carol', 'chan:x', '{"conversation":"Dc","type":"dm"}', ?, ?, ?)`, ch, now()-86400, string(payload), now()+3600)
	up := httptest.NewRequest("POST", fmt.Sprintf("/adapter/files?channelId=%d&name=n.txt", ch), strings.NewReader("new"))
	up.Header.Set("X-XBin-From", "apps/slack")
	up.Header.Set("X-XBin-Role", "admin")
	upw := httptest.NewRecorder()
	if gMux.ServeHTTP(upw, up); upw.Code != 200 {
		t.Fatalf("an upload: %d %s", upw.Code, upw.Body)
	}
	var files []string
	rows, _ := gAg.db.q.Query(`SELECT id FROM channel_files WHERE id IN ('fheld','fstale') ORDER BY id`)
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		files = append(files, id)
	}
	rows.Close()
	if strings.Join(files, ",") != "fheld" {
		t.Fatalf("after the upload prune: %v (want the queued DM's file only)", files)
	}
	_, _ = gAg.db.q.Exec(`UPDATE handoffs SET created=? WHERE id='hold'`, now()-handoffQueueTTL-1)
	kickHandoffs()
	var state, left string
	waitFor(t, "the old handoff given up", func() bool {
		_ = gAg.db.q.QueryRow(`SELECT state, payload FROM handoffs WHERE id='hold'`).Scan(&state, &left)
		return state == "failed"
	})
	waitMailIdle(t)
	var n int
	_ = gAg.db.q.QueryRow(`SELECT count(*) FROM channel_files WHERE id='fheld'`).Scan(&n)
	told := false
	for _, o := range outOfKind(gAg, ch, "notice") {
		told = told || strings.Contains(o.Body.Text, "waited 7 days")
	}
	if left != "" || n != 0 || !told {
		t.Fatalf("given up: payload %q, staged file %d, chat told %v", left, n, told)
	}
}

// TestFirstDMNotice: a DM from a chat account linked to someone whose
// partition has never run gets a notice — once — since nothing answers it
// until they open the agent; a partition says hello when it first starts,
// and then no notice is sent.
func TestFirstDMNotice(t *testing.T) {
	setMode(t, modeGlobal, "")
	gAg, gMux := chanFixture(t)
	_ = gAg.db.addHandoffSchema()
	mail := stubMail(t)
	ch := helloAs(t, gMux, "apps/slack", "T1")
	claim(t, gMux, ch, map[string]any{"dm": map[string]any{"policy": "linked"}})
	_, _ = gAg.db.q.Exec(`INSERT INTO channel_peers (channel_id, peer_id, name, state, created, xbin_user, linked_at) VALUES (?, 'ho-dave', 'Dave', 'allowed', ?, 'dave', ?)`, ch, now(), now())
	notices := func() int {
		n := 0
		for _, o := range outOfKind(gAg, ch, "notice") {
			if strings.Contains(o.Body.Text, "open the agent once") {
				n++
			}
		}
		return n
	}
	chPost(t, gMux, chMsg(ch, "dm", "Dd", "ho-dave", "first"))
	chPost(t, gMux, chMsg(ch, "dm", "Dd", "ho-dave", "second"))
	if mail.wait(t, 2); notices() != 1 {
		t.Fatalf("the first-DM notice: %d", notices())
	}
	useMail(t, &fakeMail{items: []mailItem{
		{ID: "001", From: "global", Topic: topicHello, Data: json.RawMessage(`{}`)}, // only a person's partition
		{ID: "002", From: "user:dave", Topic: topicHello, Data: json.RawMessage(`{}`)},
	}})
	if c, err := gAg.pullMail(context.Background()); err != nil || c.Handled != 2 {
		t.Fatalf("hello: %+v %v", c, err)
	}
	var seen int
	_ = gAg.db.q.QueryRow(`SELECT seen FROM partition_people WHERE person='dave'`).Scan(&seen)
	if seen != 1 {
		t.Fatal("dave's hello wasn't recorded")
	}
	var ghosts int
	_ = gAg.db.q.QueryRow(`SELECT count(*) FROM partition_people WHERE person NOT IN ('dave')`).Scan(&ghosts)
	if ghosts != 0 {
		t.Fatalf("%d other rows", ghosts)
	}
	_, _ = gAg.db.q.Exec(`UPDATE partition_people SET noticed=0`) // were he told again, it would show
	chPost(t, gMux, chMsg(ch, "dm", "Dd", "ho-dave", "third"))
	if mail.wait(t, 3); notices() != 1 {
		t.Fatalf("a notice once dave's partition ran: %d", notices())
	}
	waitMailIdle(t)

	// a person's partition says hello once
	setMode(t, modeUser, "dave")
	uAg := newTestAgent(t, newTestDB(t))
	useGlobalAgent(t, uAg)
	uAg.sayHello(context.Background())
	uAg.sayHello(context.Background())
	hellos := 0
	for _, s := range mail.wait(t, 4) {
		if s.topic == topicHello && s.to == "global" {
			hellos++
		}
	}
	if hellos != 1 {
		t.Fatalf("%d hellos", hellos)
	}
}

// TestHandoffsWaitWakeGlobal: a global instance stopping with handoffs still
// to mail leaves its resume job.
func TestHandoffsWaitWakeGlobal(t *testing.T) {
	var mu sync.Mutex
	var jobs []string
	fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && r.URL.Path == "/api/xbin/cron/jobs" {
			var j map[string]any
			_ = json.NewDecoder(r.Body).Decode(&j)
			mu.Lock()
			jobs = append(jobs, fmt.Sprint(j["name"]))
			mu.Unlock()
		}
		w.WriteHeader(200)
	}))
	t.Setenv("XBIN_COMPONENT", "apps/agent")
	setMode(t, modeGlobal, "")
	d := newTestDB(t)
	_ = d.addHandoffSchema()
	ag := &Agent{db: d}
	ag.leaveWakeUp(d)
	if d.handoffsWait() || len(jobs) != 0 {
		t.Fatalf("an idle global instance: waits %v, jobs %v", d.handoffsWait(), jobs)
	}
	_ = d.queueHandoff("event", "alice", "h1", eventHandoff{Handoff: "h1"}, func(*handoffRow) {})
	ag.leaveWakeUp(d)
	mu.Lock()
	defer mu.Unlock()
	if !d.handoffsWait() || strings.Join(jobs, ",") != "resume" {
		t.Fatalf("a queued handoff: waits %v, jobs %v", d.handoffsWait(), jobs)
	}
}

// TestHandedReplyLimits: global posts a person's reply only while its chat
// account is still linked to them, and only for a handoff young enough; a
// reply's file is served only for its own row's channel; the dedupe key
// outlives the outbox's week.
func TestHandedReplyLimits(t *testing.T) {
	setMode(t, modeGlobal, "")
	gAg, gMux := chanFixture(t)
	_ = gAg.db.addHandoffSchema()
	stubMail(t)
	ch := helloAs(t, gMux, "apps/slack", "T1")
	claim(t, gMux, ch, map[string]any{"dm": map[string]any{"policy": "linked"}})
	_, _ = gAg.db.q.Exec(`INSERT INTO channel_peers (channel_id, peer_id, name, state, created, xbin_user, linked_at) VALUES (?, 'ho-uma', 'Uma', 'allowed', ?, 'alice', ?)`, ch, now(), now())
	hand := func(id string, age int64) {
		_, _ = gAg.db.q.Exec(`INSERT INTO handoffs (id, kind, person, channel_id, peer_id, session_key, address, created, state)
			VALUES (?, 'dm', 'alice', ?, 'ho-uma', 'chan:x', '{"conversation":"D1","type":"dm"}', ?, 'mailed')`, id, ch, now()-age)
	}
	hand("hnew", 60)
	hand("hold", handoffMaxAge+60)
	reply := func(id, h, key, text string) mailItem {
		b, _ := json.Marshal(outboxAddItem{Handoff: h, Key: key, Kind: "answer", Text: text})
		return mailItem{ID: id, From: "user:alice", Topic: topicOutbox, Data: b}
	}
	useMail(t, &fakeMail{items: []mailItem{reply("1", "hold", "k1", "too late"), reply("2", "hnew", "k2", "fine")}})
	_, _ = gAg.pullMail(context.Background())
	if a := outOfKind(gAg, ch, "answer"); len(a) != 1 || a[0].Body.Text != "fine" {
		t.Fatalf("posted: %+v", a)
	}
	// the dedupe key outlives the outbox's week (acked, 8 days old: kept)
	_, _ = gAg.db.q.Exec(`UPDATE outbox SET state='delivered', created=? WHERE origin<>''`, now()-8*86400)
	_ = gAg.db.Tx(func(t2 *DB) error { t2.outboxAdd(ch, "", 0, "notice", "{}", "anything"); return nil }) // runs the week's prune
	useMail(t, &fakeMail{items: []mailItem{reply("3", "hnew", "k2", "fine")}})
	_, _ = gAg.pullMail(context.Background())
	if a := outOfKind(gAg, ch, "answer"); len(a) != 1 {
		t.Fatalf("a retry 8 days later posted again: %d answers", len(a))
	}
	// unlinked (or linked to someone else): nothing more is posted
	_, _ = gAg.db.q.Exec(`UPDATE channel_peers SET xbin_user='bob' WHERE peer_id='ho-uma'`)
	useMail(t, &fakeMail{items: []mailItem{reply("4", "hnew", "k4", "after the relink")}})
	_, _ = gAg.pullMail(context.Background())
	if a := outOfKind(gAg, ch, "answer"); len(a) != 1 {
		t.Fatalf("a reply after the chat account was relinked: %d answers", len(a))
	}

	// a staged file is served for its own row only: not a global run's
	// session file named like one, not another channel's
	_, _ = gAg.db.q.Exec(`INSERT INTO channel_files (id, channel_id, name, mime, size, content, blob, created) VALUES ('rother', 99, 'x.txt', 'text/plain', 5, 'other', '', ?)`, now())
	body, _ := json.Marshal(outBody{Text: "t", Files: []outFile{{Name: "x.txt", Path: stagedPrefix + "rother"}}})
	res, _ := gAg.db.q.Exec(`INSERT INTO outbox (channel_id, session_key, run_id, kind, address, body, created, origin) VALUES (?, 'k', 0, 'answer', '{}', ?, ?, 'user:alice/kx')`, ch, string(body), now())
	oid, _ := res.LastInsertId()
	if w := adapterCall(t, gMux, "apps/slack", "GET", fmt.Sprintf("/adapter/files/%d/0", oid), nil); w.Code != 404 || strings.Contains(w.Body.String(), "other") {
		t.Fatalf("another channel's staged file: %d %s", w.Code, w.Body)
	}
	res, _ = gAg.db.q.Exec(`INSERT INTO outbox (channel_id, session_key, run_id, kind, address, body, created) VALUES (?, 'k', 7, 'answer', '{}', ?, ?)`, ch, string(body), now())
	oid, _ = res.LastInsertId()
	if w := adapterCall(t, gMux, "apps/slack", "GET", fmt.Sprintf("/adapter/files/%d/0", oid), nil); w.Code != 404 || strings.Contains(w.Body.String(), "other") {
		t.Fatalf("a run's file named like a staged one: %d %s", w.Code, w.Body)
	}
}

// TestRegistryActivityAtGlobal: global keeps a registry row's events only
// for its dedupe window, shows no activity of it, and refuses its events
// and tests (they are its person's).
func TestRegistryActivityAtGlobal(t *testing.T) {
	setMode(t, modeGlobal, "")
	gAg, gMux := chanFixture(t)
	_ = gAg.db.addHandoffSchema()
	stubMail(t)
	w := serveAs(gMux, "POST", "/triggers/registry", `{"name":"a-deploys","source":"push","sourceRef":"apps/webhooks","match":"deploy/","enabled":true,"maxPerHour":30}`, asPartition("alice"))
	var row struct{ ID int64 }
	if json.Unmarshal(w.Body.Bytes(), &row); w.Code != 200 {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	_, _ = gAg.db.q.Exec(`INSERT INTO trigger_events (trigger_id, event_id, source, topic, created) VALUES (?, 'push:old', 'push', 'deploy/x', ?)`, row.ID, now()-2*86400)
	pushEvent(t, gMux, "apps/webhooks", map[string]any{"topic": "deploy/prod", "eventId": "ev1"})
	var events int
	var last int64
	_ = gAg.db.q.QueryRow(`SELECT count(*) FROM trigger_events WHERE trigger_id=?`, row.ID).Scan(&events)
	_ = gAg.db.q.QueryRow(`SELECT last_event FROM triggers WHERE id=?`, row.ID).Scan(&last)
	if events != 1 || last != 0 {
		t.Fatalf("global keeps %d events of alice's trigger, last_event %d", events, last)
	}
	var list struct{ Items []AutomationItem }
	_ = json.Unmarshal(callAs(t, gMux, asMgr, "GET", "/automations", nil).Body.Bytes(), &list)
	for _, it := range list.Items {
		if it.Kind == "trigger" && it.ID == row.ID && (it.LastRunAt != 0 || it.LastStatus != "" || it.Access != "oversee") {
			t.Fatalf("a manager's view of alice's row: %+v", it)
		}
	}
	for _, c := range []struct{ method, path string }{{"GET", "/triggers/%d/events"}, {"POST", "/triggers/%d/test"}} {
		if w := callAs(t, gMux, asSystem, c.method, fmt.Sprintf(c.path, row.ID), map[string]any{}); w.Code != 409 {
			t.Errorf("%s %s on alice's row, as the tile: %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
}

// TestGlobalListingShared: a person's partition reads the global
// instance's automations once for a listing (channels and oversight both
// come from it), again after a write it forwarded there.
func TestGlobalListingShared(t *testing.T) {
	setMode(t, modeUser, "mgr")
	kv := newMemKV()
	putConf(kv, "", `{}`)
	ag, mux := chanFixture(t)
	partitionConf(t, ag, kv)
	_ = ag.db.addHandoffSchema()
	g := stubGlobalCalls(t, func(method, path string, _ []byte) (int, string) {
		if method == "GET" && path == "/automations" {
			return 200, `{"items":[{"kind":"trigger","id":5,"name":"bobs","owner":"bob","access":"oversee"},{"kind":"channel","id":1,"access":"owner"}]}`
		}
		return 200, `{"ok":true}`
	})
	reads := func() int {
		n := 0
		for _, c := range g.got() {
			if strings.HasPrefix(c, "GET /automations") {
				n++
			}
		}
		return n
	}
	callAs(t, mux, asMgr, "GET", "/automations", nil)
	callAs(t, mux, asMgr, "GET", "/automations?summary=1", nil)
	if reads() != 1 {
		t.Fatalf("a listing and a badge read global's %d times", reads())
	}
	noteGlobalWrite() // what callGlobal does after a write
	callAs(t, mux, asMgr, "GET", "/automations", nil)
	if reads() != 2 {
		t.Fatalf("after a write: %d reads", reads())
	}
}

// TestMailFilesPrepared: a mailed reply's binary file is stored before the
// handler's transaction (never inside it), kept when the handler keeps it,
// and deleted when it doesn't.
func TestMailFilesPrepared(t *testing.T) {
	setMode(t, modeGlobal, "")
	gAg, _ := chanFixture(t)
	_ = gAg.db.addHandoffSchema()
	bin := []byte{0, 1, 2, 3, 0xff}
	data, _ := json.Marshal(outboxAddItem{Handoff: "h", Key: "k", Kind: "answer", Text: "t",
		Files: []hoFile{{Name: "a.txt", Mime: "text/plain", Data: []byte("text")}, {Name: "b.bin", Mime: "application/octet-stream", Data: bin}}})
	it := mailItem{ID: "m1", From: "user:alice", Topic: topicOutbox, Data: data}
	ctx, settle := prepareMail(context.Background(), it)
	p, _ := ctx.Value(preparedKey{}).(*preparedFiles)
	if p == nil || len(p.blobs) != 1 || p.blobs[1] == "" {
		t.Fatalf("prepared: %+v", p)
	}
	if b, err := gAg.blobs.Get(context.Background(), p.blobs[1]); err != nil || string(b) != string(bin) {
		t.Fatalf("the object before the transaction: %v", err)
	}
	if path, err := preparedBlob(ctx, 1); err != nil || path != p.blobs[1] {
		t.Fatalf("preparedBlob: %q %v", path, err)
	}
	settle(true)
	if _, err := gAg.blobs.Get(context.Background(), p.blobs[1]); err != nil {
		t.Fatalf("a kept object was deleted: %v", err)
	}
	ctx, settle = prepareMail(context.Background(), mailItem{ID: "m2", From: "user:alice", Topic: topicOutbox, Data: data})
	p2, _ := ctx.Value(preparedKey{}).(*preparedFiles)
	settle(false) // its transaction failed
	if _, err := gAg.blobs.Get(context.Background(), p2.blobs[1]); err == nil {
		t.Fatal("the object of a failed transaction stays")
	}
	if _, settle := prepareMail(context.Background(), mailItem{ID: "m3", From: "global", Topic: topicOutbox, Data: data}); settle == nil {
		t.Fatal("no settle step")
	}
}
