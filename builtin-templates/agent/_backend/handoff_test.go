package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// mailRec is one mail the code sent (sendMail stubbed).
type mailRec struct {
	to, topic, source string
	data              json.RawMessage
}

type mailStub struct {
	mu   sync.Mutex
	sent []mailRec
	fail error
}

func stubMail(t *testing.T) *mailStub {
	t.Helper()
	s := &mailStub{}
	old := sendMail
	sendMail = func(_ context.Context, to, topic string, data any, source string) (string, error) {
		b, _ := json.Marshal(data)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.fail != nil {
			return "", s.fail
		}
		s.sent = append(s.sent, mailRec{to: to, topic: topic, source: source, data: b})
		return fmt.Sprintf("m%03d", len(s.sent)), nil
	}
	t.Cleanup(func() {
		waitMailIdle(t)
		for _, s := range []*sync.Mutex{&handoffSender.mu, &outboxMailer.mu} {
			s.Lock()
		}
		for _, tm := range []**time.Timer{&handoffSender.timer, &outboxMailer.timer} {
			if *tm != nil {
				(*tm).Stop()
				*tm = nil
			}
		}
		handoffSender.backoff, outboxMailer.backoff = 0, 0
		outboxMailer.mu.Unlock()
		handoffSender.mu.Unlock()
		sendMail = old
	})
	return s
}

func (s *mailStub) wait(t *testing.T, n int) []mailRec {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d mails", n), func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.sent) >= n
	})
	waitMailIdle(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mailRec(nil), s.sent...)
}

func (s *mailStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

// waitMailIdle waits until no sender pass runs (before a test swaps the
// process it plays).
func waitMailIdle(t *testing.T) {
	t.Helper()
	waitFor(t, "the mail senders to finish", func() bool {
		handoffSender.mu.Lock()
		a := handoffSender.running
		handoffSender.mu.Unlock()
		outboxMailer.mu.Lock()
		b := outboxMailer.running
		outboxMailer.mu.Unlock()
		return !a && !b
	})
}

// asPartition is a person's partition calling its own global instance
// (attributed to them by xbind, their role clamped).
func asPartition(user string) map[string]string {
	return map[string]string{"X-XBin-From": "apps/agent", "X-XBin-Role": "reader", "X-XBin-User": user,
		"X-XBin-User-Level": "read", "X-XBin-Partition": "user:" + user}
}

func serveAs(mux http.Handler, method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, as(method, target, body, hdr))
	return w
}

// TestLinkedDMHandoff: at a partitioned agent's global instance a DM from a
// chat account linked to alice is no conversation of global's: it is handed
// to her partition (with its file), which runs it as hers and mails the
// answer back; global posts it to the chat the DM came from — its own record
// says where — only when the mail is alice's, once however often it comes,
// and forgets it once the adapter acknowledged it.
func TestLinkedDMHandoff(t *testing.T) {
	setMode(t, modeGlobal, "")
	gAg, gMux := chanFixture(t)
	if err := gAg.db.addHandoffSchema(); err != nil {
		t.Fatal(err)
	}
	mail := stubMail(t)
	ch := helloAs(t, gMux, "apps/slack", "T1")
	claim(t, gMux, ch, map[string]any{"dm": map[string]any{"policy": "linked"}})
	_, _ = gAg.db.q.Exec(`INSERT INTO channel_peers (channel_id, peer_id, name, state, created, xbin_user, linked_at) VALUES (?, 'ho-uma', 'Uma', 'allowed', ?, 'alice', ?)`, ch, now(), now())
	_, _ = gAg.db.q.Exec(`INSERT INTO channel_files (id, channel_id, name, mime, size, content, blob, created) VALUES ('fx1', ?, 'notes.txt', 'text/plain', 8, 'line one', '', ?)`, ch, now())

	m := chMsg(ch, "dm", "D1", "ho-uma", "hello private")
	m["files"] = []string{"fx1"}
	if v := chPost(t, gMux, m); !v.Accepted || v.SessionKey != fmt.Sprintf("chan:%d:dm:ho-uma", ch) || v.RunID != 0 {
		t.Fatalf("the linked DM at global: %+v", v)
	}
	sent := mail.wait(t, 1)
	var h dmHandoff
	_ = json.Unmarshal(sent[0].data, &h)
	if sent[0].to != "user:alice" || sent[0].topic != topicDM || h.Text != "hello private" || h.Session != fmt.Sprintf("chan:%d:dm:ho-uma", ch) ||
		len(h.Files) != 1 || string(h.Files[0].Data) != "line one" || h.Staged != nil || h.Lane != "web" {
		t.Fatalf("the handoff mail: %+v %s", sent[0], sent[0].data)
	}
	var runs, staged int
	_ = gAg.db.q.QueryRow(`SELECT count(*) FROM runs`).Scan(&runs)
	_ = gAg.db.q.QueryRow(`SELECT count(*) FROM channel_files WHERE id='fx1'`).Scan(&staged)
	var state, payload, person string
	_ = gAg.db.q.QueryRow(`SELECT state, payload, person FROM handoffs WHERE id=?`, h.Handoff).Scan(&state, &payload, &person)
	if runs != 0 || staged != 0 || state != "mailed" || payload != "" || person != "alice" {
		t.Fatalf("global kept: %d runs, %d staged files, handoff %s/%q/%s", runs, staged, state, payload, person)
	}
	// /help is about the chat account: answered here, nothing handed off
	chPost(t, gMux, chMsg(ch, "dm", "D1", "ho-uma", "/help"))
	if n := outOfKind(gAg, ch, "notice"); len(n) != 1 || !strings.Contains(n[0].Body.Text, "Commands:") || mail.count() != 1 {
		t.Fatalf("/help at global: %+v, %d mails", n, mail.count())
	}

	// alice's partition takes it
	setMode(t, modeUser, "alice")
	kv := newMemKV()
	putConf(kv, "", `{}`)
	uAg := newTestAgent(t, newTestDB(t))
	useGlobalAgent(t, uAg)
	partitionConf(t, uAg, kv)
	if err := uAg.db.addHandoffSchema(); err != nil {
		t.Fatal(err)
	}
	fakeOf(uAg).on(lastUser("hello private"), say("hi alice"))
	fm := &fakeMail{items: []mailItem{
		{ID: "001", From: "user:bob", Topic: topicDM, Data: sent[0].data, At: time.Now()}, // only global hands a DM
		{ID: "002", From: "global", Topic: topicDM, Data: sent[0].data, At: time.Now()},
	}}
	useMail(t, fm)
	if c, err := uAg.pullMail(context.Background()); err != nil || c.Handled != 2 {
		t.Fatalf("alice's mailbox: %+v %v", c, err)
	}
	run, ok := uAg.db.sessionRun(h.Session)
	if !ok {
		t.Fatal("no DM conversation in alice's partition")
	}
	waitFor(t, "the answer", func() bool { return strings.Contains(transcript(uAg.db, run), "hi alice") })
	r, _ := uAg.db.getRun(run)
	if r.Owner != "alice" || r.Visibility != visPrivate || r.Origin != "channel" {
		t.Fatalf("the DM conversation: owner %q, %s, origin %s", r.Owner, r.Visibility, r.Origin)
	}
	if f, err := uAg.db.replFile(run, "notes.txt"); err != nil || f.Content != "line one" {
		t.Fatalf("the DM's file in alice's conversation: %v %+v", err, f)
	}
	sent = mail.wait(t, 2)
	var add outboxAddItem
	_ = json.Unmarshal(sent[1].data, &add)
	if sent[1].to != "global" || sent[1].topic != topicOutbox || add.Handoff != h.Handoff || add.Kind != "answer" || add.Text != "hi alice" || add.Key == "" {
		t.Fatalf("the reply mail: %+v %s", sent[1], sent[1].data)
	}
	if o := uAg.db.outRows(`WHERE state='delivered'`); len(o) != 1 {
		t.Fatalf("alice's outbox after mailing: %+v", uAg.db.outRows(``))
	}
	// mailed again by global (a crash before it noted the mail): taken once
	fm.items = append(fm.items, mailItem{ID: "003", From: "global", Topic: topicDM, Data: sent[0].data, At: time.Now()})
	if _, err := uAg.pullMail(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(uAg.db.inboxRows(`WHERE run_id=? AND kind='user'`, run)); n != 1 {
		t.Fatalf("the DM delivered %d times", n)
	}

	// global posts it — alice's, once; a forged or unknown one never
	waitQuiet(t, uAg)
	waitMailIdle(t)
	setMode(t, modeGlobal, "")
	useGlobalAgent(t, gAg)
	withFile, _ := json.Marshal(outboxAddItem{Handoff: h.Handoff, Key: "k-file", Kind: "answer", Text: "here",
		Files: []hoFile{{Name: "r.txt", Mime: "text/plain", Data: []byte("reply file")}}})
	unknown, _ := json.Marshal(outboxAddItem{Handoff: "hnope", Key: "k", Kind: "answer", Text: "lost"})
	forged := strings.Replace(string(sent[1].data), "hi alice", "forged by bob", 1)
	gm := &fakeMail{items: []mailItem{
		{ID: "101", From: "user:bob", Topic: topicOutbox, Data: json.RawMessage(forged)},
		{ID: "102", From: "user:alice", Topic: topicOutbox, Data: sent[1].data},
		{ID: "103", From: "user:alice", Topic: topicOutbox, Data: sent[1].data}, // the same reply again
		{ID: "104", From: "user:alice", Topic: topicOutbox, Data: unknown},
		{ID: "105", From: "global", Topic: topicOutbox, Data: sent[1].data},
		{ID: "106", From: "user:alice", Topic: topicOutbox, Data: withFile},
	}}
	useMail(t, gm)
	if c, err := gAg.pullMail(context.Background()); err != nil || c.Handled != 6 || len(gm.items) != 0 {
		t.Fatalf("global's mailbox: %+v %v (left %v)", c, err, gm.items)
	}
	answers := outOfKind(gAg, ch, "answer")
	if len(answers) != 2 || answers[0].Body.Text != "hi alice" || answers[1].Body.Text != "here" {
		t.Fatalf("posted: %+v", answers)
	}
	var addr struct{ Conversation, Type string }
	_ = json.Unmarshal(answers[0].Address, &addr)
	if addr.Conversation != "D1" || addr.Type != "dm" {
		t.Fatalf("the reply's address (global's record): %s", answers[0].Address)
	}
	// the reply's file, from where global staged it
	w := adapterCall(t, gMux, "apps/slack", "GET", fmt.Sprintf("/adapter/files/%d/0", answers[1].ID), nil)
	if w.Code != 200 || w.Body.String() != "reply file" {
		t.Fatalf("the reply's file: %d %s", w.Code, w.Body)
	}
	// acknowledged: its content goes, the row stays for the dedupe
	w = adapterCall(t, gMux, "apps/slack", "POST", "/adapter/ack", map[string]any{"acks": []map[string]any{
		{"id": answers[0].ID, "ok": true, "ref": "p1"}, {"id": answers[1].ID, "ok": false, "error": "the platform said no"}}})
	if w.Code != 200 {
		t.Fatalf("ack: %d %s", w.Code, w.Body)
	}
	// a failed one isn't sent again from the channel owner's view: its content is gone
	if w := callAs(t, gMux, asMgr, "POST", fmt.Sprintf("/channels/%d/outbox/%d/retry", ch, answers[1].ID), nil); w.Code != 404 {
		t.Fatalf("retrying alice's failed reply: %d %s", w.Code, w.Body)
	}
	var body, stagedLeft string
	_ = gAg.db.q.QueryRow(`SELECT body FROM outbox WHERE id=?`, answers[1].ID).Scan(&body)
	_ = gAg.db.q.QueryRow(`SELECT count(*) FROM channel_files`).Scan(&stagedLeft)
	if body != "{}" || stagedLeft != "0" {
		t.Fatalf("after the ack: body %s, %s staged files", body, stagedLeft)
	}
	gm.items = []mailItem{{ID: "107", From: "user:alice", Topic: topicOutbox, Data: sent[1].data}}
	_, _ = gAg.pullMail(context.Background())
	if n := len(outOfKind(gAg, ch, "answer")); n != 2 {
		t.Fatalf("a late retry after the ack posted again: %d answers", n)
	}
}

// TestHandoffRefused: a DM whose person xbind won't take mail for (gone, or
// no longer allowed to use the agent) isn't lost silently: the chat is told.
// A failure xbind may retry is tried again.
func TestHandoffRefused(t *testing.T) {
	setMode(t, modeGlobal, "")
	gAg, gMux := chanFixture(t)
	_ = gAg.db.addHandoffSchema()
	mail := stubMail(t)
	ch := helloAs(t, gMux, "apps/slack", "T1")
	claim(t, gMux, ch, map[string]any{"dm": map[string]any{"policy": "linked"}})
	_, _ = gAg.db.q.Exec(`INSERT INTO channel_peers (channel_id, peer_id, name, state, created, xbin_user, linked_at) VALUES (?, 'ho-gone', 'Uma', 'allowed', ?, 'gone', ?)`, ch, now(), now())
	mail.mu.Lock()
	mail.fail = fmt.Errorf("partition mail: HTTP 507: the inbox is full") // may retry
	mail.mu.Unlock()
	chPost(t, gMux, chMsg(ch, "dm", "D1", "ho-gone", "hello"))
	waitMailIdle(t)
	var state string
	var tries int
	_ = gAg.db.q.QueryRow(`SELECT state, tries FROM handoffs`).Scan(&state, &tries)
	if state != "queued" || tries < 1 {
		t.Fatalf("a failure xbind may retry: %s after %d tries", state, tries)
	}
	mail.mu.Lock()
	mail.fail = fmt.Errorf("%w: partition mail: HTTP 404: no such person here", errMailRefused)
	mail.mu.Unlock()
	kickHandoffs()
	waitFor(t, "the refusal", func() bool {
		_ = gAg.db.q.QueryRow(`SELECT state FROM handoffs`).Scan(&state)
		return state == "failed"
	})
	if n := outOfKind(gAg, ch, "notice"); len(n) != 1 || !strings.Contains(n[0].Body.Text, "couldn't be passed on") {
		t.Fatalf("the chat is told: %+v", n)
	}
}

// TestTriggerRegistry: the global instance keeps people's private triggers'
// names and matches: a private push trigger needs a prefix, and one that
// overlaps another owner's is refused; only its person, from their own
// partition, writes a row, which a manager may switch off (nothing else);
// a push it matches is handed to the person (with its source, for their
// ledger), once however often it comes.
func TestTriggerRegistry(t *testing.T) {
	setMode(t, modeGlobal, "")
	gAg, gMux := chanFixture(t)
	_ = gAg.db.addHandoffSchema()
	mail := stubMail(t)
	reg := func(user string, body string) *httptest.ResponseRecorder {
		return serveAs(gMux, "POST", "/triggers/registry", body, asPartition(user))
	}
	entry := func(name, match string) string {
		return fmt.Sprintf(`{"name":%q,"source":"push","sourceRef":"apps/webhooks","match":%q,"enabled":true,"maxPerHour":30}`, name, match)
	}
	if w := reg("alice", entry("a-deploys", "")); w.Code != 400 || !strings.Contains(w.Body.String(), "topic prefix") {
		t.Fatalf("an empty match: %d %s", w.Code, w.Body)
	}
	w := reg("alice", entry("a-deploys", "deploy/"))
	var row struct {
		ID   int64
		Host string
	}
	if json.Unmarshal(w.Body.Bytes(), &row); w.Code != 200 || row.Host != "user:alice" {
		t.Fatalf("alice registers: %d %s", w.Code, w.Body)
	}
	if w := reg("alice", entry("a-deploys", "deploy/")); w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"id":%d`, row.ID)) {
		t.Fatalf("registered again (a retry): %d %s", w.Code, w.Body)
	}
	if w := reg("alice", entry("a-deploys-prod", "deploy/prod")); w.Code != 200 {
		t.Fatalf("alice's own overlap: %d %s", w.Code, w.Body)
	}
	for name, body := range map[string]string{
		"under hers": entry("b1", "deploy/prod/x"),
		"over hers":  entry("b2", "dep"),
		"her name":   entry("a-deploys", "release/"),
		"everything": entry("b3", ""),
	} {
		if w := reg("bob", body); w.Code == 200 {
			t.Errorf("bob, %s: %d %s", name, w.Code, w.Body)
		}
	}
	// a "rename" of someone else's is a new row of bob's; hers stays
	if w := reg("bob", `{"name":"b4","prev":"a-deploys","source":"push","sourceRef":"apps/webhooks","match":"zz/","enabled":true}`); w.Code != 200 ||
		len(gAg.db.listTriggers(`WHERE name='a-deploys' AND owner='alice'`)) != 1 {
		t.Fatalf("bob renaming alice's: %d %s", w.Code, w.Body)
	}
	if w := reg("bob", entry("b-releases", "release/")); w.Code != 200 {
		t.Fatalf("bob's own prefix: %d %s", w.Code, w.Body)
	}
	if w := reg("bob", `{"name":"b-other","source":"push","sourceRef":"apps/github","match":"deploy/","enabled":true}`); w.Code != 200 {
		t.Fatalf("the same prefix on another source: %d %s", w.Code, w.Body)
	}
	for name, hdr := range map[string]map[string]string{
		"the owner token": {"X-XBin-From": "owner", "X-XBin-Role": "admin", "X-XBin-Owner": "1"},
		"alice's frame":   {"X-XBin-From": "apps/agent", "X-XBin-Role": "admin", "X-XBin-User": "alice", "X-XBin-User-Level": "write"},
		"another tile":    {"X-XBin-From": "apps/other", "X-XBin-Role": "admin"},
	} {
		if w := serveAs(gMux, "POST", "/triggers/registry", entry("x1", "x/"), hdr); w.Code == 200 {
			t.Errorf("%s registers a private trigger: %d", name, w.Code)
		}
	}

	// a push: handed to alice (her two triggers) and to nobody else
	w2, res := pushEvent(t, gMux, "apps/webhooks", map[string]any{"topic": "deploy/prod", "eventId": "ev1", "data": map[string]int{"n": 1}, "dataClass": "public"})
	if w2.Code != 200 || len(res) != 2 || !res[0].Accepted || !res[1].Accepted || res[0].RunID != 0 {
		t.Fatalf("the push: %d %+v", w2.Code, res)
	}
	sent := mail.wait(t, 2)
	for _, s := range sent {
		var e eventHandoff
		_ = json.Unmarshal(s.data, &e)
		if s.to != "user:alice" || s.topic != topicEvent || s.source != "apps/webhooks" || e.EventID != "ev1" || e.Topic != "deploy/prod" ||
			e.DataClass != "public" || !strings.HasPrefix(e.Trigger, "a-deploys") {
			t.Errorf("the event mail: %+v %s", s, s.data)
		}
	}
	if _, res := pushEvent(t, gMux, "apps/webhooks", map[string]any{"topic": "deploy/prod", "eventId": "ev1"}); len(res) != 2 || !res[0].Dup {
		t.Fatalf("the same delivery again: %+v", res)
	}
	var runs int
	_ = gAg.db.q.QueryRow(`SELECT count(*) FROM runs`).Scan(&runs)
	if time.Sleep(50 * time.Millisecond); mail.count() != 2 || runs != 0 {
		t.Fatalf("after the retry: %d mails, %d runs at global", mail.count(), runs)
	}
	// a manager switches it off (and changes nothing else); a halt holds pushes
	if w := callAs(t, gMux, asMgr, "PUT", fmt.Sprintf("/triggers/%d", row.ID), map[string]any{"goal": "steal"}); w.Code != 409 {
		t.Fatalf("editing a registry row at global: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, gMux, asMgr, "PUT", fmt.Sprintf("/triggers/%d", row.ID), map[string]any{"enabled": false}); w.Code != 200 {
		t.Fatalf("a manager switches it off: %d %s", w.Code, w.Body)
	}
	if _, res := pushEvent(t, gMux, "apps/webhooks", map[string]any{"topic": "deploy/x", "eventId": "ev2"}); len(res) != 1 || res[0].Reason != "disabled" {
		t.Fatalf("a push for the switched-off trigger: %+v", res)
	}
	_ = gAg.db.putSetting("halt", "1")
	if w, res := pushEvent(t, gMux, "apps/webhooks", map[string]any{"topic": "release/1", "eventId": "ev3"}); w.Code != 503 || res[0].Reason != "halted" {
		t.Fatalf("a push while halted: %d %+v", w.Code, res)
	}
	_ = gAg.db.putSetting("halt", "")
	// managers see the rows (oversight), not configs — there are none here
	var list struct{ Items []AutomationItem }
	_ = json.Unmarshal(callAs(t, gMux, asMgr, "GET", "/automations", nil).Body.Bytes(), &list)
	seen := 0
	for _, it := range list.Items {
		if it.Kind == "trigger" && it.Owner == "alice" {
			seen++
			if it.Access != "oversee" || it.Config != nil {
				t.Errorf("a manager's view of alice's trigger: %+v", it)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("a manager sees %d of alice's triggers", seen)
	}
	// deleting: its person only
	if w := serveAs(gMux, "DELETE", "/triggers/registry/a-deploys", "", asPartition("bob")); w.Code != 404 {
		t.Fatalf("bob deletes alice's row: %d", w.Code)
	}
	if w := serveAs(gMux, "DELETE", "/triggers/registry/a-deploys", "", asPartition("alice")); w.Code != 200 {
		t.Fatalf("alice deletes hers: %d %s", w.Code, w.Body)
	}
	// unpartitioned there's no registry
	setMode(t, modeLegacy, "")
	if w := reg("alice", entry("z", "z/")); w.Code != 404 {
		t.Fatalf("an unpartitioned agent's registry: %d", w.Code)
	}
}

// TestPrivateTriggerInPartition: a person's trigger lives in their
// partition — its registry row written at global first (a refusal there is
// the answer), renamed and deleted with it — and an event global hands them
// runs it here, once.
func TestPrivateTriggerInPartition(t *testing.T) {
	setMode(t, modeUser, "alice")
	kv := newMemKV()
	putConf(kv, "", `{}`)
	ag, mux := chanFixture(t)
	partitionConf(t, ag, kv)
	_ = ag.db.addHandoffSchema()
	fakeOf(ag).on(nil, say("summarised"))
	refuse := ""
	g := stubGlobalCalls(t, func(method, path string, _ []byte) (int, string) {
		if refuse != "" && method == "POST" {
			return 409, `{"error":` + strconvQuote(refuse) + `}`
		}
		return 200, `{"ok":true}`
	})
	body := map[string]any{"name": "a-deploys", "source": "push", "sourceRef": "apps/webhooks", "match": "deploy/",
		"goal": "summarise {{topic}}", "toolset": "web", "dataClass": "public"}
	refuse = "its topics overlap a trigger someone else has"
	if w := callAs(t, mux, asAlice, "POST", "/triggers", body); w.Code != 409 || !strings.Contains(w.Body.String(), "overlap") {
		t.Fatalf("a registration global refuses: %d %s", w.Code, w.Body)
	}
	if n := len(ag.db.listTriggers(``)); n != 0 {
		t.Fatalf("a refused trigger was kept: %d", n)
	}
	refuse = ""
	tr := mkTrigger(t, mux, asAlice, body)
	if c := g.got(); len(c) != 2 || !strings.HasPrefix(c[1], `POST /triggers/registry {"name":"a-deploys","source":"push","sourceRef":"apps/webhooks","match":"deploy/","enabled":true`) {
		t.Fatalf("the registration: %v", c)
	}
	if w := callAs(t, mux, asAlice, "POST", "/triggers", body); w.Code != 409 || len(g.got()) != 2 {
		t.Fatalf("a second trigger of the same name: %d, %d calls", w.Code, len(g.got()))
	}
	body["visibility"] = "team"
	body["name"] = "a-team"
	if w := callAs(t, mux, asAlice, "POST", "/triggers", body); w.Code != 409 {
		t.Fatalf("a team trigger in a partition: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asAlice, "PUT", fmt.Sprintf("/triggers/%d", tr.ID), map[string]any{"name": "deploys"}); w.Code != 200 {
		t.Fatalf("a rename: %d %s", w.Code, w.Body)
	}
	if c := g.got(); len(c) != 3 || !strings.Contains(c[2], `"name":"deploys","prev":"a-deploys"`) {
		t.Fatalf("the rename at global: %v", c)
	}
	if w := callAs(t, mux, asAlice, "PUT", fmt.Sprintf("/triggers/%d", tr.ID), map[string]any{"goal": "just the goal: {{topic}}"}); w.Code != 200 || len(g.got()) != 3 {
		t.Fatalf("an edit of its config only stays here: %d, %v", w.Code, g.got())
	}

	// an event handed over by global runs it, once; from anyone else, never
	ev, _ := json.Marshal(eventHandoff{Handoff: "h1", Trigger: "deploys", Source: "push", SourceRef: "apps/webhooks", EventID: "ev1",
		Topic: "deploy/prod", Data: json.RawMessage(`{"n":1}`), DataClass: "public"})
	fm := &fakeMail{items: []mailItem{
		{ID: "001", From: "user:bob", Topic: topicEvent, Data: ev},
		{ID: "002", From: "global", Topic: topicEvent, Data: ev},
		{ID: "003", From: "global", Topic: topicEvent, Data: ev}, // global mailed it twice
	}}
	useMail(t, fm)
	if c, err := ag.pullMail(context.Background()); err != nil || c.Handled != 3 {
		t.Fatalf("the mailbox: %+v %v", c, err)
	}
	evs := callAs(t, mux, asAlice, "GET", fmt.Sprintf("/triggers/%d/events", tr.ID), nil)
	var out struct{ Events []struct{ Accepted bool } }
	_ = json.Unmarshal(evs.Body.Bytes(), &out)
	var runs int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM runs WHERE origin='trigger' AND owner='alice'`).Scan(&runs)
	if len(out.Events) != 1 || !out.Events[0].Accepted || runs != 1 {
		t.Fatalf("the handed event ran %d times (%s)", runs, evs.Body)
	}
	waitQuiet(t, ag)

	// deleted here and at global
	if w := callAs(t, mux, asAlice, "DELETE", fmt.Sprintf("/triggers/%d", tr.ID), nil); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if c := g.got(); len(c) != 4 || c[3] != "DELETE /triggers/registry/deploys " {
		t.Fatalf("the delete at global: %q", c)
	}
}

// TestUsageSummaries: a person's partition mails its daily totals once a
// day; the global instance keeps them per person for its managers.
func TestUsageSummaries(t *testing.T) {
	setMode(t, modeUser, "alice")
	kv := newMemKV()
	putConf(kv, "", `{}`)
	uAg, _ := chanFixture(t)
	partitionConf(t, uAg, kv)
	mail := stubMail(t)
	day := time.Now().UTC().AddDate(0, 0, -2)
	for i := 0; i < 3; i++ {
		id, _ := uAg.db.createRun("x", "{}", 0)
		_, _ = uAg.db.q.Exec(`UPDATE runs SET created=?, llm_calls=2, prompt_tokens=100, completion_tokens=10 WHERE id=?`, day.Unix()+int64(i), id)
	}
	today, _ := uAg.db.createRun("today", "{}", 0) // not counted before the day is over
	_ = today
	uAg.sendUsage(context.Background())
	sent := mail.wait(t, 1)
	var u struct{ Days []usageDay }
	_ = json.Unmarshal(sent[0].data, &u)
	if sent[0].to != "global" || sent[0].topic != topicUsage || len(u.Days) != 1 || u.Days[0].Runs != 3 || u.Days[0].PromptTokens != 300 ||
		u.Days[0].Day != day.Format("2006-01-02") {
		t.Fatalf("the usage mail: %+v %s", sent[0], sent[0].data)
	}
	uAg.sendUsage(context.Background())
	if mail.count() != 1 {
		t.Fatal("the same days were mailed again")
	}

	setMode(t, modeGlobal, "")
	gAg, gMux := chanFixture(t)
	_ = gAg.db.addHandoffSchema()
	useMail(t, &fakeMail{items: []mailItem{
		{ID: "001", From: "user:alice", Topic: topicUsage, Data: sent[0].data},
		{ID: "002", From: "global", Topic: topicUsage, Data: sent[0].data},
	}})
	if c, err := gAg.pullMail(context.Background()); err != nil || c.Handled != 2 {
		t.Fatalf("global's mailbox: %+v %v", c, err)
	}
	w := callAs(t, gMux, asMgr, "GET", "/usage", nil)
	var got struct {
		People []struct {
			User  string
			Total usageDay
		}
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || len(got.People) != 1 || got.People[0].User != "alice" || got.People[0].Total.Runs != 3 || got.People[0].Total.LLMCalls != 6 {
		t.Fatalf("GET /usage: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, gMux, asAlice, "GET", "/usage", nil); w.Code != 403 {
		t.Fatalf("a reader's GET /usage: %d", w.Code)
	}
	setMode(t, modeLegacy, "")
	if w := callAs(t, gMux, asMgr, "GET", "/usage", nil); w.Code != 404 {
		t.Fatalf("GET /usage unpartitioned: %d", w.Code)
	}
}

// TestChannelsUnpartitionedUnchanged: an unpartitioned agent hands nothing
// off and grows no table: a linked DM is its own conversation, as ever.
func TestChannelsUnpartitionedUnchanged(t *testing.T) {
	ag, mux := chanFixture(t)
	mail := stubMail(t)
	fakeOf(ag).on(nil, say("hi"))
	ch := helloAs(t, mux, "apps/slack", "T1")
	claim(t, mux, ch, map[string]any{"dm": map[string]any{"policy": "linked"}})
	_, _ = ag.db.q.Exec(`INSERT INTO channel_peers (channel_id, peer_id, name, state, created, xbin_user, linked_at) VALUES (?, 'ho-legacy', 'Uma', 'allowed', ?, 'alice', ?)`, ch, now(), now())
	v := chPost(t, mux, chMsg(ch, "dm", "D1", "ho-legacy", "hello"))
	if !v.Accepted || v.RunID == 0 {
		t.Fatalf("a linked DM unpartitioned: %+v", v)
	}
	waitFor(t, "the answer", func() bool { return len(outOfKind(ag, ch, "answer")) == 1 })
	var tables int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('handoffs', 'usage_days')`).Scan(&tables)
	var cols int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM pragma_table_info('outbox') WHERE name='origin'`).Scan(&cols)
	if tables != 0 || cols != 0 || mail.count() != 0 {
		t.Fatalf("unpartitioned: %d new tables, %d new columns, %d mails", tables, cols, mail.count())
	}
}

// TestRepliesWaitWakePartition: a reply xbind hasn't taken yet is work that
// moves without the person — a stopping partition asks to start again.
func TestRepliesWaitWakePartition(t *testing.T) {
	setMode(t, modeUser, "alice")
	ag := newTestAgent(t, newTestDB(t))
	useGlobalAgent(t, ag)
	mail := stubMail(t)
	mail.mu.Lock()
	mail.fail = fmt.Errorf("partition mail: HTTP 507: the inbox is full")
	mail.mu.Unlock()
	if ag.db.userWake(time.Now()).runnable {
		t.Fatal("an idle partition is runnable")
	}
	_ = ag.db.Tx(func(t2 *DB) error {
		t2.outboxAdd(1, "chan:1:dm:u", 0, "answer", handoffAddr("h1"), "hi")
		return nil
	})
	waitMailIdle(t)
	if !ag.db.userWake(time.Now()).runnable {
		t.Fatal("a reply waiting to be mailed isn't work that wakes the partition")
	}
}

// TestTriggerOversightInPartition: a manager's own partition lists the
// other people's registry rows the global instance keeps (and only those),
// and switching one off there is forwarded to the global instance; its own
// triggers are numbered from 2^40, so the two never share an id.
func TestTriggerOversightInPartition(t *testing.T) {
	setMode(t, modeUser, "mgr")
	kv := newMemKV()
	putConf(kv, "", `{}`)
	ag, mux := chanFixture(t)
	partitionConf(t, ag, kv)
	_ = ag.db.addHandoffSchema()
	g := stubGlobalCalls(t, func(method, path string, _ []byte) (int, string) {
		if method == "GET" && path == "/automations" {
			return 200, `{"items":[{"kind":"trigger","id":5,"name":"bobs","owner":"bob","access":"oversee"},` +
				`{"kind":"trigger","id":6,"name":"team","owner":"","access":"owner"},{"kind":"channel","id":1,"access":"owner"}]}`
		}
		return 200, `{"ok":true}`
	})
	own := mkTrigger(t, mux, asMgr, map[string]any{"name": "mine", "source": "push", "sourceRef": "apps/webhooks", "match": "m/",
		"goal": "g", "toolset": "web", "dataClass": "public"})
	if own.ID < partitionIDBase {
		t.Fatalf("a partition's trigger id: %d (want ≥ 2^40)", own.ID)
	}
	var list struct{ Items []AutomationItem }
	_ = json.Unmarshal(callAs(t, mux, asMgr, "GET", "/automations", nil).Body.Bytes(), &list)
	var trig []string
	for _, it := range list.Items {
		if it.Kind == "trigger" {
			trig = append(trig, fmt.Sprintf("%s/%s", it.Name, it.Access))
		}
	}
	if strings.Join(trig, ",") != "bobs/oversee,mine/owner" {
		t.Fatalf("a manager's partition lists triggers %v", trig)
	}
	_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", "/automations", nil).Body.Bytes(), &list)
	for _, it := range list.Items {
		if it.Kind == "trigger" && it.Owner == "bob" {
			t.Fatalf("a reader's partition lists bob's trigger: %+v", it)
		}
	}
	calls := len(g.got())
	if w := callAs(t, mux, asMgr, "PUT", "/triggers/5", map[string]any{"enabled": false}); w.Code != 200 {
		t.Fatalf("switching bob's off from a manager's partition: %d %s", w.Code, w.Body)
	}
	if c := g.got(); len(c) != calls+1 || !strings.HasPrefix(c[calls], `PUT /triggers/5 {"enabled":false}`) {
		t.Fatalf("not forwarded: %v", c[calls:])
	}
}
