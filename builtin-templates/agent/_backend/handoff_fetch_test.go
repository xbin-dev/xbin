package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHandoffLargeFiles (90 §I11, 08 §5): a DM's file too large for the
// handoff's mail waits in the global instance's storage — the mail names
// it, the upload prune leaves it — and the person's partition fetches it
// (only they may) before taking the DM, then acknowledges, and global
// deletes it; one nobody fetched goes after fetchHeldTTL. A fetch that
// fails leaves the mail for the next pull; one gone is said in the text.
// The other way, a reply's file too large for its mail is staged at global
// first (once however often it is retried) and posted with the reply.
func TestHandoffLargeFiles(t *testing.T) {
	setMode(t, modeGlobal, "")
	gAg, gMux := chanFixture(t)
	if err := gAg.db.addHandoffSchema(); err != nil {
		t.Fatal(err)
	}
	mail := stubMail(t)
	ch := helloAs(t, gMux, "apps/slack", "T1")
	claim(t, gMux, ch, map[string]any{"dm": map[string]any{"policy": "linked"}})
	_, _ = gAg.db.q.Exec(`INSERT INTO channel_peers (channel_id, peer_id, name, state, created, xbin_user, linked_at) VALUES (?, 'ho-uma', 'Uma', 'allowed', ?, 'alice', ?)`, ch, now(), now())
	gAg.db.markRan("alice")
	big := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 200<<10) // 800 KiB: past the mail's 640
	_ = gAg.blobs.Put(context.Background(), "chanfiles/big", big, "image/png")
	_, _ = gAg.db.q.Exec(`INSERT INTO channel_files (id, channel_id, name, mime, size, content, blob, created) VALUES
		('fbig', ?, 'big.png', 'image/png', ?, '', 'chanfiles/big', ?), ('fsmall', ?, 'notes.txt', 'text/plain', 8, 'line one', '', ?)`,
		ch, len(big), now()-2*stagedTTL, ch, now())
	m := chMsg(ch, "dm", "D1", "ho-uma", "two files")
	m["files"] = []string{"fbig", "fsmall"}
	chPost(t, gMux, m)
	sent := mail.wait(t, 1)
	var h dmHandoff
	_ = json.Unmarshal(sent[0].data, &h)
	if len(h.Files) != 1 || h.Files[0].Name != "notes.txt" || len(h.Fetch) != 1 || h.Fetch[0].ID != "fbig" || h.Fetch[0].Size != len(big) ||
		strings.Contains(h.Text, "not passed on") || len(sent[0].data) > 1<<20 {
		t.Fatalf("the handoff: %d files, fetch %+v, text %q, %d bytes", len(h.Files), h.Fetch, h.Text, len(sent[0].data))
	}
	var n int
	_ = gAg.db.q.QueryRow(`SELECT count(*) FROM channel_files WHERE id IN ('fbig','fsmall')`).Scan(&n)
	if n != 1 {
		t.Fatalf("after mailing: %d of the two staged files (want the held one)", n)
	}
	// the upload prune leaves it (it is older than an unclaimed upload may be)
	if w := adapterCall(t, gMux, "apps/slack", "POST", fmt.Sprintf("/adapter/files?channelId=%d&name=x.txt", ch), "x"); w.Code != 200 {
		t.Fatalf("an upload: %d %s", w.Code, w.Body)
	}
	_ = gAg.db.q.QueryRow(`SELECT count(*) FROM channel_files WHERE id='fbig'`).Scan(&n)
	if n != 1 {
		t.Fatal("the upload prune took a file held for a fetch")
	}
	// only alice's partition fetches it
	path := "/handoffs/" + h.Handoff + "/files/fbig"
	for _, c := range []struct {
		hdr  map[string]string
		want int
	}{{asPartition("bob"), 404}, {ownerToken, 403}, {asPartition("alice"), 200}} {
		w := serveAs(gMux, "GET", path, "", c.hdr)
		if w.Code != c.want || (c.want == 200 && !bytes.Equal(w.Body.Bytes(), big)) {
			t.Fatalf("GET %s as %v: %d (%d bytes)", path, c.hdr["X-XBin-User"], w.Code, w.Body.Len())
		}
	}
	if w := serveAs(gMux, "GET", "/handoffs/"+h.Handoff+"/files/fsmall", "", asPartition("alice")); w.Code != 404 {
		t.Fatalf("a file not held for the handoff: %d", w.Code)
	}
	waitMailIdle(t)

	// alice's partition takes the DM with both files, then acknowledges
	setMode(t, modeUser, "alice")
	kv := newMemKV()
	putConf(kv, "", `{}`)
	uAg := newTestAgent(t, newTestDB(t))
	useGlobalAgent(t, uAg)
	partitionConf(t, uAg, kv)
	_ = uAg.db.addHandoffSchema()
	fakeOf(uAg).on(lastUser("two files"), say("got them"))
	var mu sync.Mutex
	fetchStatus := 500
	var gotAck []string
	oldExport, oldCall := exportAtGlobal, callGlobal
	t.Cleanup(func() { exportAtGlobal, callGlobal = oldExport, oldCall })
	exportAtGlobal = func(ctx context.Context, p string) (gwResp, error) {
		mu.Lock()
		defer mu.Unlock()
		if p != path {
			return gwResp{Status: 404, Body: []byte(`{"error":"no such file: fetched already, or gone"}`)}, nil
		}
		if fetchStatus != 200 {
			return gwResp{Status: fetchStatus, Body: []byte(`{"error":"no"}`)}, nil
		}
		return gwResp{Status: 200, Type: "image/png", Body: big}, nil // what global served alice above
	}
	callGlobal = func(_ context.Context, method, p string, body []byte, _ string) (gwResp, error) {
		mu.Lock()
		defer mu.Unlock()
		gotAck = append(gotAck, method+" "+p)
		return gwResp{Status: 200, Body: []byte(`{}`)}, nil
	}
	fm := &fakeMail{items: []mailItem{{ID: "001", From: "global", Topic: topicDM, Data: sent[0].data, At: time.Now()}}}
	useMail(t, fm)
	// global didn't answer: the mail stays, nothing taken
	if c, err := uAg.pullMail(context.Background()); err != nil || c.Left != 1 || c.Handled != 0 {
		t.Fatalf("a fetch that failed: %+v %v", c, err)
	}
	if _, ok := uAg.db.sessionRun(h.Session); ok {
		t.Fatal("taken without its file")
	}
	mu.Lock()
	fetchStatus = 200
	mu.Unlock()
	if c, err := uAg.pullMail(context.Background()); err != nil || c.Handled != 1 {
		t.Fatalf("the DM: %+v %v", c, err)
	}
	run, ok := uAg.db.sessionRun(h.Session)
	if !ok {
		t.Fatal("no DM conversation")
	}
	files, _ := uAg.db.replFiles(run)
	var gotBig bool
	for _, f := range files {
		if f.Binary && f.Bytes == len(big) {
			data, err := uAg.readBlob(context.Background(), f.Blob)
			gotBig = err == nil && bytes.Equal(data, big)
		}
	}
	if f, err := uAg.db.replFile(run, "notes.txt"); err != nil || f.Content != "line one" || !gotBig {
		t.Fatalf("the DM's files: %+v (big: %v)", files, gotBig)
	}
	waitFor(t, "the fetch acknowledged", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(strings.Join(gotAck, ","), "POST /handoffs/"+h.Handoff+"/fetched")
	})
	// a held file gone before it was fetched (past fetchHeldTTL): the DM goes, saying so
	lost := h
	lost.Handoff, lost.Text, lost.Files, lost.Fetch = "hlost", "third", nil, []hoHeld{{ID: "fgone", Name: "lost.png", Size: 900 << 10}}
	lostJSON, _ := json.Marshal(lost)
	fm.items = append(fm.items, mailItem{ID: "002", From: "global", Topic: topicDM, Data: lostJSON, At: time.Now()})
	if c, err := uAg.pullMail(context.Background()); err != nil || c.Handled != 1 {
		t.Fatalf("the DM whose file was gone: %+v %v", c, err)
	}
	waitFor(t, "the DM saying its file was gone", func() bool {
		var n int
		_ = uAg.db.q.QueryRow(`SELECT (SELECT count(*) FROM messages WHERE content LIKE '%gone before your space could fetch it: lost.png (900.0 KB)%')
			+ (SELECT count(*) FROM inbox WHERE body LIKE '%gone before your space could fetch it: lost.png (900.0 KB)%')`).Scan(&n)
		return n > 0
	})
	waitQuiet(t, uAg)
	waitMailIdle(t)

	// …and global deletes it
	setMode(t, modeGlobal, "")
	useGlobalAgent(t, gAg)
	if w := serveAs(gMux, "POST", "/handoffs/"+h.Handoff+"/fetched", "", asPartition("bob")); w.Code != 404 {
		t.Fatalf("bob acknowledges alice's: %d", w.Code)
	}
	if w := serveAs(gMux, "POST", "/handoffs/"+h.Handoff+"/fetched", "", asPartition("alice")); w.Code != 200 {
		t.Fatalf("the ack: %d %s", w.Code, w.Body)
	}
	_ = gAg.db.q.QueryRow(`SELECT count(*) FROM channel_files WHERE id='fbig'`).Scan(&n)
	if _, err := gAg.blobs.Get(context.Background(), "chanfiles/big"); n != 0 || err == nil {
		t.Fatalf("after the ack: %d rows, blob err %v", n, err)
	}
	if w := serveAs(gMux, "GET", path, "", asPartition("alice")); w.Code != 404 {
		t.Fatalf("the held file after the ack: %d", w.Code)
	}
	// a mail held back is mailed later: its held file's time starts again
	_, _ = gAg.db.q.Exec(`INSERT INTO handoff_files (file_id, handoff, person, created) VALUES ('flate', 'hlate', 'alice', ?)`, now()-fetchHeldTTL+60)
	if err := gAg.holdForFetch(&dmHandoff{Handoff: "hlate"}, "flate", "late.png", "image/png", 1); err != nil {
		t.Fatal(err)
	}
	var created int64
	_ = gAg.db.q.QueryRow(`SELECT created FROM handoff_files WHERE file_id='flate'`).Scan(&created)
	if created < now()-5 {
		t.Fatalf("mailed again, its file's time didn't start again: %d", now()-created)
	}
	// one nobody fetched goes after fetchHeldTTL
	_, _ = gAg.db.q.Exec(`INSERT INTO channel_files (id, channel_id, name, mime, size, content, blob, created) VALUES ('fold', ?, 'o', 'text/plain', 1, 'o', '', ?)`, ch, now())
	_, _ = gAg.db.q.Exec(`INSERT INTO handoff_files (file_id, handoff, person, created) VALUES ('fold', 'hx', 'alice', ?)`, now()-fetchHeldTTL-1)
	gAg.expireHeldFiles()
	_ = gAg.db.q.QueryRow(`SELECT count(*) FROM channel_files WHERE id='fold'`).Scan(&n)
	if n != 0 {
		t.Fatal("a file nobody fetched outlived fetchHeldTTL")
	}

	// a reply's file too large for its mail: staged at global first — only
	// by the handoff's person, once however often — then posted with it
	stageTo := func(handoff string, hdr map[string]string, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/handoffs/"+handoff+"/reply-files?key="+key+"&name=out.png&mime=image/png", bytes.NewReader(big))
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		gMux.ServeHTTP(w, r)
		return w
	}
	stage := func(hdr map[string]string, key string) *httptest.ResponseRecorder {
		return stageTo(h.Handoff, hdr, key)
	}
	const k1 = "00112233445566ff-1"
	if w := stage(asPartition("bob"), k1+"/0"); w.Code != 404 {
		t.Fatalf("bob stages into alice's reply: %d", w.Code)
	}
	if w := stage(asPartition("alice"), "k1/0"); w.Code != 400 {
		t.Fatalf("a key not a reply's: %d", w.Code)
	}
	var st struct{ ID string }
	for range 2 {
		w := stage(asPartition("alice"), k1+"/0")
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &st) != nil || st.ID != replyFileID("alice", k1+"/0") {
			t.Fatalf("staging: %d %s", w.Code, w.Body)
		}
	}
	_ = gAg.db.q.QueryRow(`SELECT count(*) FROM channel_files WHERE id=?`, st.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("staged %d times", n)
	}
	// bob stages a file for his own chat; alice's reply naming it doesn't take it
	_, _ = gAg.db.q.Exec(`INSERT INTO handoffs (id, kind, person, channel_id, created, state) VALUES ('hbob', 'dm', 'bob', ?, ?, 'mailed')`, ch, now())
	var bobs struct{ ID string }
	if w := stageTo("hbob", asPartition("bob"), k1+"/0"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &bobs) != nil || bobs.ID == st.ID {
		t.Fatalf("bob stages for his own chat: %d %s", w.Code, w.Body)
	}
	reply, _ := json.Marshal(outboxAddItem{Handoff: h.Handoff, Key: "k1", Kind: "answer", Text: "the chart", Staged: []string{st.ID, bobs.ID}})
	useMail(t, &fakeMail{items: []mailItem{{ID: "201", From: "user:alice", Topic: topicOutbox, Data: reply, At: time.Now()}}})
	if _, err := gAg.pullMail(context.Background()); err != nil {
		t.Fatal(err)
	}
	answers := outOfKind(gAg, ch, "answer")
	if len(answers) != 1 || len(answers[0].Body.Files) != 1 || answers[0].Body.Files[0].Bytes != len(big) {
		t.Fatalf("the reply with its staged file: %+v", answers)
	}
	if w := adapterCall(t, gMux, "apps/slack", "GET", fmt.Sprintf("/adapter/files/%d/0", answers[0].ID), nil); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), big) {
		t.Fatalf("the adapter downloads it: %d (%d bytes)", w.Code, w.Body.Len())
	}
	adapterCall(t, gMux, "apps/slack", "POST", "/adapter/ack", map[string]any{"acks": []map[string]any{{"id": answers[0].ID, "ok": true, "ref": "p1"}}})
	_ = gAg.db.q.QueryRow(`SELECT count(*) FROM channel_files WHERE id=?`, st.ID).Scan(&n)
	if n != 0 {
		t.Fatal("the staged reply file outlived its ack")
	}
	_ = gAg.db.q.QueryRow(`SELECT count(*) FROM handoff_files WHERE file_id=? AND person='bob'`, bobs.ID).Scan(&n)
	if n != 1 {
		t.Fatal("alice's reply took bob's staged file")
	}
	waitMailIdle(t)

	// what a person may stage and not send: per chat (413: sent without it),
	// in all (507: later); a chat too old to answer (410)
	oldPer, oldFiles, oldBytes := maxStagedPerHandoff, maxStagedFiles, maxStagedBytes
	t.Cleanup(func() { maxStagedPerHandoff, maxStagedFiles, maxStagedBytes = oldPer, oldFiles, oldBytes })
	maxStagedPerHandoff, maxStagedFiles, maxStagedBytes = 2, 3, 3*len(big)
	for i, want := range []int{200, 200, 413} {
		if w := stage(asPartition("alice"), fmt.Sprintf("%s/%d", k1, i+1)); w.Code != want {
			t.Fatalf("stage %d for one chat: %d %s (want %d)", i+1, w.Code, w.Body, want)
		}
	}
	_, _ = gAg.db.q.Exec(`INSERT INTO handoffs (id, kind, person, channel_id, created, state) VALUES ('h2', 'dm', 'alice', ?, ?, 'mailed'), ('hold', 'dm', 'alice', ?, ?, 'mailed')`,
		ch, now(), ch, now()-handoffMaxAge-1)
	if w := stageTo("h2", asPartition("alice"), k1+"/7"); w.Code != 200 {
		t.Fatalf("a third file, another chat: %d %s", w.Code, w.Body)
	}
	if w := stageTo("h2", asPartition("alice"), k1+"/8"); w.Code != 507 {
		t.Fatalf("past the person's share: %d %s", w.Code, w.Body)
	}
	maxStagedFiles = 10
	if w := stageTo("h2", asPartition("alice"), k1+"/8"); w.Code != 507 {
		t.Fatalf("past the person's bytes: %d %s", w.Code, w.Body)
	}
	maxStagedBytes = 10 * len(big)
	if w := stageTo("hold", asPartition("alice"), k1+"/9"); w.Code != 410 {
		t.Fatalf("a chat too old to answer: %d %s", w.Code, w.Body)
	}

	// the partition's side: a reply whose file doesn't fit is staged, then mailed naming it
	setMode(t, modeUser, "alice")
	useGlobalAgent(t, uAg)
	blob := newBlobPath(run)
	_ = uAg.blobs.Put(context.Background(), blob, big, "image/png")
	if _, err := uAg.db.replPutBinary(run, "chart.png", "image/png", len(big), blob); err != nil {
		t.Fatal(err)
	}
	var stagedCalls []string
	callGlobal = func(_ context.Context, method, p string, body []byte, _ string) (gwResp, error) {
		mu.Lock()
		defer mu.Unlock()
		stagedCalls = append(stagedCalls, method+" "+p)
		return gwResp{Status: 200, Body: []byte(`{"id":"rstaged1"}`)}, nil
	}
	before := mail.count()
	_ = uAg.db.Tx(func(t *DB) error {
		t.outboxAdd(ch, h.Session, run, "answer", handoffAddr(h.Handoff), "here's the chart", outFile{Name: "chart.png", Mime: "image/png", Path: "chart.png"})
		return nil
	})
	kickOutboxMail()
	all := mail.wait(t, before+1)
	var out outboxAddItem
	_ = json.Unmarshal(all[len(all)-1].data, &out)
	mu.Lock()
	calls := strings.Join(stagedCalls, ",")
	mu.Unlock()
	if out.Text != "here's the chart" || len(out.Files) != 0 || len(out.Staged) != 1 || out.Staged[0] != "rstaged1" ||
		!strings.Contains(calls, "PUT /handoffs/"+h.Handoff+"/reply-files?key=") {
		t.Fatalf("the reply's mail: %+v, calls %s", out, calls)
	}
	waitQuiet(t, uAg)
}
