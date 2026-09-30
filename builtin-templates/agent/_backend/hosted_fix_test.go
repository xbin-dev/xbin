package main

// hosted_fix_test.go — the failure paths of hosted (non-secure)
// conversations: the tools a hosted run lacks, the audience changing while
// paused or mid-turn, the host leaving, forged live events, the two-phase
// move and continue, the host engine's wake-up, silent drafts, un-sharing,
// approvals, and fencing with real engine locks.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHostedRunLacksPartitionTools: a hosted run has no schedule, thread or
// skill tools — they would register cron jobs in the host's partition under
// ids team numbers like the partition's own, or read other people's hosted
// conversations and team's shared skills — and one called anyway is refused.
func TestHostedRunLacksPartitionTools(t *testing.T) {
	cfg := defaultConfig()
	lacks := []string{"schedule", "unschedule", "schedules_list", "schedule_inspect", "threads_list", "thread_inspect", "skills_list", "skill_view", "skill_manage"}
	own := toolNamesOf(runToolSpecs(cfg, &Run{ID: 7}, nil))
	if !own["schedule"] {
		t.Fatal("control: a partition's own run has no schedule tool — the check below proves nothing")
	}
	hosted := toolNamesOf(runToolSpecs(cfg, &Run{ID: teamIDBase + 7}, nil))
	for _, n := range lacks {
		if hosted[n] {
			t.Errorf("BUG: a hosted run is offered %s", n)
		}
		if err := hostedToolRefused(&Run{ID: teamIDBase + 7, RootID: teamIDBase + 1, Depth: 0}, n); err == nil {
			t.Errorf("BUG: a hosted run's %s isn't refused", n)
		}
	}
	if !hosted["memory_set"] || !hosted["finish"] {
		t.Fatal("a hosted run lost its core tools")
	}

	// the probe: alice's own schedule #1, then a hosted run's model calls schedule anyway
	ag, _, tdb, _ := hostAgent(t)
	tr := tdb.runs
	mine, err := ag.db.createSchedule(&Schedule{Name: "mine", Cron: "@every 1h", Goal: "my own", Owner: "alice", Mode: modeIsolated, Enabled: true})
	if err != nil || mine != 1 {
		t.Fatalf("alice's schedule: #%d %v", mine, err)
	}
	f := fakeOf(ag)
	f.on(lastUser("remind us"), callTools(tc("c1", "schedule", `{"cron":"@every 30m","goal":"ping the group"}`))).once()
	id := hostedRun(t, tr, "bob", map[string]string{"alice": roleParticipant}, "user:alice", "remind us hourly")
	ag.hostSnapshot(t, tr, id)
	ensureHostEngine()
	var res string
	waitFor(t, "the hosted run's schedule call answered", func() bool {
		_, res, _ = tr.toolResultRow(id, "c1")
		return res != "" && !isPlaceholder(res)
	})
	if !strings.Contains(res, "not available in a non-secure") {
		t.Fatalf("BUG: a hosted run's schedule call: %q", res)
	}
	var n int
	_ = tr.q.QueryRow(`SELECT count(*) FROM schedules`).Scan(&n)
	if n != 0 {
		t.Fatalf("BUG: team holds %d schedules", n)
	}
	if s, err := ag.db.getSchedule(1); err != nil || s.Goal != "my own" {
		t.Fatalf("alice's own schedule changed: %+v %v", s, err)
	}
	for _, c := range f.callsFor(id) {
		if toolNamesOf(c.Tools)["schedule"] {
			t.Fatal("BUG: the hosted run's model was offered schedule")
		}
	}
}

// addTeamMember adds a member straight into team (what global does), no ring.
func addTeamMember(t *testing.T, tr *DB, id int64, user, role string) {
	t.Helper()
	if _, err := tr.q.Exec(`INSERT INTO run_members (run_id, user, role, added_by, via, created) VALUES (?, ?, ?, 'bob', 'invite', ?)
		ON CONFLICT(run_id, user) DO UPDATE SET role=excluded.role`, id, user, role, now()); err != nil {
		t.Fatal(err)
	}
}

// said gives a team conversation a transcript (a hosted one always has one).
func said(t *testing.T, tr *DB, id int64, text string) int64 {
	t.Helper()
	for _, m := range []*Message{{RunID: id, Role: "user", Content: text}, {RunID: id, Role: "assistant", Content: "noted"}} {
		if _, err := tr.addMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func ringAudience(t *testing.T, ag *Agent, id int64) {
	t.Helper()
	data, _ := json.Marshal(hostedInput{Conversation: id, Signal: "audience"})
	if err := ag.db.Tx(func(d *DB) error {
		return handleHostedInputMail(context.Background(), d, mailItem{ID: fmt.Sprintf("m-%d", time.Now().UnixNano()), From: "global", Topic: topicHostedInput, Data: data})
	}); err != nil {
		t.Fatal(err)
	}
}

// TestHostedPausedAudienceChanges: while a hosted conversation waits for its
// host, a further change is what the host is asked about (the key they
// confirm follows it — no 409 loop), and an audience back within what they
// confirmed makes it active again by itself.
func TestHostedPausedAudienceChanges(t *testing.T) {
	ag, h, tdb, _ := hostAgent(t)
	tr := tdb.runs
	id := hostedRun(t, tr, "bob", map[string]string{"alice": roleParticipant}, "user:alice", "legit hello")
	ag.hostSnapshot(t, tr, id)
	ensureHostEngine()
	waitFor(t, "the first answer", func() bool { return answered(tr, id, "ok") })

	addTeamMember(t, tr, id, "carol", roleViewer)
	ringAudience(t, ag, id)
	var row *hostedRow
	waitFor(t, "paused for carol", func() bool { row, _ = ag.db.hostedRow(id); return row.State == hostPaused })
	first := row.PendingKey
	addTeamMember(t, tr, id, "dave", roleViewer)
	ringAudience(t, ag, id)
	waitFor(t, "the host asked about dave too", func() bool {
		row, _ = ag.db.hostedRow(id)
		return strings.Join(row.Pending, ",") == "carol,dave"
	})
	th, _ := tr.teamHost(id)
	if info := hostedInfo(th); fmt.Sprint(info["pending"]) != "[carol dave]" || info["pendingKey"] != row.PendingKey {
		t.Fatalf("the members' view: %v / %v (the host's key %s)", info["pending"], info["pendingKey"], row.PendingKey)
	}
	serveJSON(t, h, as("POST", fmt.Sprintf("/hosting/%d/confirm", id), fmt.Sprintf(`{"seen":%q}`, first), alicesFrame("read")), 409, nil)
	serveJSON(t, h, as("POST", fmt.Sprintf("/hosting/%d/confirm", id), fmt.Sprintf(`{"seen":%q}`, row.PendingKey), alicesFrame("read")), 200, nil)
	if row, _ = ag.db.hostedRow(id); row.State != hostActive || len(row.Snapshot.Members) != 3 {
		t.Fatalf("confirmed: %+v", row)
	}

	// widening, then narrowing back: active again, nothing to ask
	addTeamMember(t, tr, id, "erin", roleViewer)
	ringAudience(t, ag, id)
	waitFor(t, "paused for erin", func() bool { row, _ = ag.db.hostedRow(id); return row.State == hostPaused })
	if _, err := tr.q.Exec(`DELETE FROM run_members WHERE run_id=? AND user='erin'`, id); err != nil {
		t.Fatal(err)
	}
	ringAudience(t, ag, id)
	waitFor(t, "active again", func() bool { row, _ = ag.db.hostedRow(id); return row.State == hostActive })
	if th, _ := tr.teamHost(id); th.State != hostActive {
		t.Fatalf("team's note: %+v", th)
	}
	queueTeam(t, tr, id, "bob", "still there?")
	hostEngine.Load().Poke(id)
	waitFor(t, "it answers again", func() bool { return len(fakeOf(ag).callsFor(id)) >= 2 })
}

// TestHostRemovedDropsHosting: the owner removes the host from the
// conversation — the host's partition stops driving it (with its sandboxes,
// vault and tiles) and hosting drops, so the members may continue it.
func TestHostRemovedDropsHosting(t *testing.T) {
	ag, _, tdb, _ := hostAgent(t)
	tr := tdb.runs
	f := fakeOf(ag)
	f.on(lastUser("after"), say("SHOULD NOT RUN"))
	id := hostedRun(t, tr, "bob", map[string]string{"alice": roleParticipant, "carol": roleParticipant}, "user:alice", "legit hello")
	ag.hostSnapshot(t, tr, id)
	ensureHostEngine()
	waitFor(t, "the first answer", func() bool { return answered(tr, id, "ok") })
	if _, err := tr.q.Exec(`DELETE FROM run_members WHERE run_id=? AND user='alice'`, id); err != nil {
		t.Fatal(err)
	}
	queueTeam(t, tr, id, "carol", "after alice left")
	hostEngine.Load().Poke(id)
	waitFor(t, "hosting dropped", func() bool { row, _ := ag.db.hostedRow(id); return row.State == hostDropped })
	time.Sleep(100 * time.Millisecond)
	if answered(tr, id, "SHOULD NOT") {
		t.Fatal("BUG: the host's partition answered a conversation its person was removed from")
	}
	if th, _ := tr.teamHost(id); th.State != hostDropped || th.Reason != "left" {
		t.Fatalf("team's note: %+v", th)
	}
}

// TestHostedWidenMidTurn: someone is added while the host's model call is in
// flight and no ring arrives — the turn stops before its tool runs with the
// host's resources, and no further model step is made.
func TestHostedWidenMidTurn(t *testing.T) {
	ag, _, tdb, _ := hostAgent(t)
	tr := tdb.runs
	f := fakeOf(ag)
	f.on(lastUser("secret"), callTools(tc("c1", "memory_set", `{"key":"k","value":"from the host's resources"}`))).block("step1").once()
	id := hostedRun(t, tr, "bob", map[string]string{"alice": roleParticipant}, "user:alice", "secret work")
	ag.hostSnapshot(t, tr, id)
	ensureHostEngine()
	waitFor(t, "step 1 in flight", func() bool { return f.inFlight() == 1 })
	addTeamMember(t, tr, id, "mallory", roleViewer) // no ring: global died, or it is late
	f.release("step1")
	waitFor(t, "paused mid-turn", func() bool { row, _ := ag.db.hostedRow(id); return row.State == hostPaused })
	time.Sleep(150 * time.Millisecond)
	if mem, _ := tr.memory(id); mem["k"] != "" {
		t.Fatal("BUG: the tool ran with the host's resources for the wider audience")
	}
	if n := len(f.callsFor(id)); n != 1 {
		t.Fatalf("BUG: %d model steps (want the first only)", n)
	}
}

// TestHostedEventsChecked: the global instance publishes only what team
// holds — an event naming another conversation's run is refused, a draft of
// another conversation is never touched, and a durable event is re-read
// from team (a forged message isn't published; a real one is, as team has it).
func TestHostedEventsChecked(t *testing.T) {
	_, h := globalAgent(t)
	tdb := withTeam(t)
	tr := tdb.runs
	mine := hostedRun(t, tr, "alice", map[string]string{"bob": roleParticipant}, "user:alice", "alice hosts this")
	bobs := hostedRun(t, tr, "bob", map[string]string{"carol": roleParticipant}, "user:bob", "bob hosts this")
	post := func(who string, evs ...fwdEvent) map[string]int {
		b, _ := json.Marshal(map[string]any{"events": evs})
		var out map[string]int
		serveJSON(t, h, as("POST", "/hosted/events", string(b), f5(who, "read")), 200, &out)
		return out
	}
	carol := followAs(t, h, fmt.Sprintf("/stream?run=%d&deltas=1", bobs), f5("carol", "read"))
	bob := followAs(t, h, fmt.Sprintf("/stream?run=%d", mine), f5("bob", "read"))
	waitFor(t, "the streams say hello", func() bool {
		carol.mu.Lock()
		defer carol.mu.Unlock()
		bob.mu.Lock()
		defer bob.mu.Unlock()
		return len(carol.evs) > 0 && len(bob.evs) > 0
	})
	draftEv := func(run, root int64, s string) fwdEvent {
		return fwdEvent{Type: evText, Run: run, Root: root, Key: "text:" + itoa(run), Data: map[string]any{"text": s, "model": "m", "started": 5}}
	}
	post("bob", draftEv(bobs, bobs, "bob's own answer"))
	// alice, who hosts `mine`, names bob's run under her conversation
	if got := post("alice", draftEv(bobs, mine, "INJECTED BY ALICE"), fwdEvent{Type: evDraftEnd, Run: bobs, Root: mine}); got["refused"] != 2 {
		t.Fatalf("BUG: a run of another conversation: %v", got)
	}
	var v struct{ Drafts []draft }
	serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/view", bobs), "", f5("carol", "read")), 200, &v)
	if len(v.Drafts) != 1 || v.Drafts[0].Text != "bob's own answer" {
		t.Fatalf("BUG: carol's view of bob's conversation: %+v", v.Drafts)
	}
	// a forged message (no such row), and a real one with forged words
	if got := post("alice", fwdEvent{Type: evMessage, Run: mine, Root: mine, Data: map[string]any{"id": 999999, "role": "user", "content": "FORGED", "sender": "bob"}}); got["refused"] != 1 {
		t.Fatalf("BUG: a message team doesn't hold: %v", got)
	}
	msgs, _ := tr.messages(mine, false)
	real := msgs[len(msgs)-1]
	if got := post("alice", fwdEvent{Type: evMessage, Run: mine, Root: mine, Data: map[string]any{"id": real.ID, "role": "user", "content": "FORGED WORDS", "sender": "bob"}}); got["published"] != 1 {
		t.Fatalf("a message team holds: %v", got)
	}
	waitFor(t, "bob's stream to carry team's message", func() bool {
		bob.mu.Lock()
		defer bob.mu.Unlock()
		for _, ev := range bob.evs {
			if ev.Type == evMessage {
				return true
			}
		}
		return false
	})
	bob.mu.Lock()
	for _, ev := range bob.evs {
		if strings.Contains(fmt.Sprint(ev.Data), "FORGED") {
			bob.mu.Unlock()
			t.Fatalf("BUG: a forged event reached bob: %v", ev.Data)
		}
	}
	bob.mu.Unlock()
}

// TestHostedDraftsLetGo: a draft that hears nothing for hostedDraftTTL goes
// (with a draft.end); the host's hosted/changed mail drops the drafts and
// has the streams re-read the conversation.
func TestHostedDraftsLetGo(t *testing.T) {
	_, h := globalAgent(t)
	tdb := withTeam(t)
	old := hostedDraftTTL
	hostedDraftTTL = 100 * time.Millisecond
	t.Cleanup(func() { hostedDraftTTL = old })
	id := hostedRun(t, tdb.runs, "alice", map[string]string{"bob": roleParticipant}, "user:alice", "hello")
	post := func(evs ...fwdEvent) {
		b, _ := json.Marshal(map[string]any{"events": evs})
		serveJSON(t, h, as("POST", "/hosted/events", string(b), f5("alice", "read")), 200, nil)
	}
	drafts := func() []draft {
		var v struct{ Drafts []draft }
		serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/view", id), "", f5("bob", "read")), 200, &v)
		return v.Drafts
	}
	post(fwdEvent{Type: evText, Run: id, Root: id, Data: map[string]any{"text": "half an answer"}})
	if len(drafts()) != 1 {
		t.Fatal("no draft in flight")
	}
	waitFor(t, "the silent draft to go", func() bool { return len(drafts()) == 0 })

	hostedDraftTTL = time.Minute
	post(fwdEvent{Type: evText, Run: id, Root: id, Data: map[string]any{"text": "another"}})
	bob := followAs(t, h, fmt.Sprintf("/stream?run=%d", id), f5("bob", "read"))
	waitFor(t, "bob's stream says hello", func() bool { bob.mu.Lock(); defer bob.mu.Unlock(); return len(bob.evs) > 0 })
	data, _ := json.Marshal(map[string]any{"conversation": id})
	if err := agent.db.Tx(func(d *DB) error {
		return handleHostedChangedMail(context.Background(), d, mailItem{ID: "m1", From: "user:alice", Topic: topicHostedChanged, Data: data})
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bob's stream told to re-read", func() bool {
		bob.mu.Lock()
		defer bob.mu.Unlock()
		for _, ev := range bob.evs {
			if ev.Type == evReset {
				return true
			}
		}
		return false
	})
	if len(drafts()) != 0 {
		t.Fatal("BUG: a phantom draft after the host's changed mail")
	}
}

// TestHostingMoveTwoPhase: a person's partition records its intent before
// the global instance moves the conversation and acks after — a move that
// happened while the page was gone is taken up at the next start, one that
// didn't is forgotten, and a copy continued meanwhile isn't taken back.
func TestHostingMoveTwoPhase(t *testing.T) {
	ag, h, tdb, _ := hostAgent(t)
	tr := tdb.runs
	moved := hostedRun(t, tr, "bob", map[string]string{"alice": roleParticipant}, "user:alice", "the plan")
	if _, err := tr.q.Exec(`UPDATE team_hosts SET state='pending', moved_from=7 WHERE run_id=?`, moved); err != nil {
		t.Fatal(err)
	}
	acl, _ := tr.loadACL(moved)
	seen := audienceOf(acl)
	answer := fmt.Sprintf(`{"conversation":%d,"audience":%s}`, moved, seen.key())
	status := 503
	g := stubGlobalCalls(t, func(method, path string, body []byte) (int, string) {
		if status != 200 {
			return status, `{"error":"down"}`
		}
		return 200, answer
	})
	serveJSON(t, h, as("POST", "/hosting", `{"conversation":7}`, alicesFrame("read")), 400, nil) // no seen: no consent
	body := fmt.Sprintf(`{"conversation":7,"seen":%s}`, seen.key())
	serveJSON(t, h, as("POST", "/hosting", body, alicesFrame("read")), 502, nil)
	if !ag.db.hostingMovesWait() {
		t.Fatal("the intent didn't survive a failed call")
	}
	if len(g.got()) != 1 {
		t.Fatalf("calls: %v", g.got())
	}
	// the partition's next start: a lookup, never a move
	status = 200
	reconcileHostingMoves()
	if got := g.got(); len(got) != 2 || !strings.Contains(got[1], `"lookup":true`) {
		t.Fatalf("the reconcile asked: %v", got)
	}
	row, err := ag.db.hostedRow(moved)
	if err != nil || row.State != hostActive {
		t.Fatalf("taken up at the start: %+v %v", row, err)
	}
	if th, _ := tr.teamHost(moved); th.State != hostActive {
		t.Fatalf("not acked in team: %+v", th)
	}
	if ag.db.hostingMovesWait() {
		t.Fatal("the intent stayed")
	}

	// a lookup of a move that never happened: forgotten
	status = 404
	_, _ = ag.db.q.Exec(`INSERT INTO hosting_moves (from_id, seen, created) VALUES (8, ?, ?)`, seen.key(), now())
	reconcileHostingMoves()
	if ag.db.hostingMovesWait() {
		t.Fatal("an intent global never acted on stayed")
	}

	// continued without her meanwhile: not taken back
	other := hostedRun(t, tr, "bob", map[string]string{"alice": roleParticipant}, "user:alice", "another")
	_, _ = tr.q.Exec(`UPDATE team_hosts SET state='continued', continued_to=3 WHERE run_id=?`, other)
	answer = fmt.Sprintf(`{"conversation":%d,"audience":%s}`, other, seen.key())
	status = 200
	serveJSON(t, h, as("POST", "/hosting", `{"conversation":9,"seen":`+seen.key()+`}`, alicesFrame("read")), 409, nil)
	if row, _ := ag.db.hostedRow(other); row == nil || row.State != hostDropped {
		t.Fatalf("BUG: a continued conversation taken back: %+v", row)
	}
}

// TestHostedMoveIdempotent (the global instance): a second move of the same
// conversation by its host answers the copy there is and finishes deleting
// the original a stop left behind; another person can't; a pending copy
// whose original stayed is deleted once stale; one whose original is gone
// is "unclaimed" and continuable.
func TestHostedMoveIdempotent(t *testing.T) {
	ag, h := globalAgent(t)
	tdb := withTeam(t)
	tr := tdb.runs
	stubMail(t)
	quickWakes(t)
	var run Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"plan it","share":{"members":[{"user":"bob","role":"participant"}]}}`, f5("alice", "read")), 200, &run)
	waitStatus(t, ag.db, run.ID, statusIdle)
	// a stop between the copy and the delete: the copy is in team, the original here
	copyID := said(t, tr, hostedRun(t, tr, "alice", map[string]string{"bob": roleParticipant}, "user:alice", "plan it"), "plan it")
	_, _ = tr.q.Exec(`UPDATE team_hosts SET state='pending', moved_from=? WHERE run_id=?`, run.ID, copyID)
	serveJSON(t, h, as("POST", "/hosted", fmt.Sprintf(`{"conversation":%d,"lookup":true}`, run.ID), f5("bob", "read")), 409, nil)
	var got struct{ Conversation int64 }
	serveJSON(t, h, as("POST", "/hosted", fmt.Sprintf(`{"conversation":%d,"lookup":true}`, run.ID), f5("alice", "read")), 200, &got)
	if got.Conversation != copyID {
		t.Fatalf("the lookup answered #%d, want the copy #%d", got.Conversation, copyID)
	}
	if _, err := ag.db.getRun(run.ID); err == nil {
		t.Fatal("the original a stop left behind wasn't deleted")
	}
	serveJSON(t, h, as("POST", "/hosted", `{"conversation":424242,"lookup":true}`, f5("alice", "read")), 404, nil)

	// a stale pending copy whose original is still here: the original stays
	var other Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"another","share":{"members":[{"user":"bob","role":"participant"}]}}`, f5("alice", "read")), 200, &other)
	waitStatus(t, ag.db, other.ID, statusIdle)
	stale := hostedRun(t, tr, "alice", map[string]string{"bob": roleParticipant}, "user:alice", "another")
	_, _ = tr.q.Exec(`UPDATE team_hosts SET state='pending', moved_from=?, since=? WHERE run_id=?`, other.ID, now()-3600, stale)
	settleHostedMoves(teamView())
	if _, err := tr.getRun(stale); err == nil {
		t.Fatal("BUG: a copy whose move never finished stayed beside its original")
	}
	if _, err := ag.db.getRun(other.ID); err != nil {
		t.Fatal("the original went")
	}

	// a stale pending copy whose original is gone: unclaimed, continuable
	_, _ = tr.q.Exec(`UPDATE team_hosts SET since=? WHERE run_id=?`, now()-3600, copyID)
	th, _ := tr.teamHost(copyID)
	if info := hostedInfo(th); info["state"] != hostDropped || info["reason"] != "unclaimed" {
		t.Fatalf("an unclaimed copy: %v", info)
	}
	var back struct{ Conversation int64 }
	serveJSON(t, h, as("POST", fmt.Sprintf("/hosted/%d/continue", copyID), "", f5("bob", "read")), 200, &back)
	var again struct{ Conversation int64 }
	serveJSON(t, h, as("POST", fmt.Sprintf("/hosted/%d/continue", copyID), "", f5("alice", "read")), 200, &again)
	if again.Conversation != back.Conversation {
		t.Fatalf("BUG: a second continue made #%d beside #%d", again.Conversation, back.Conversation)
	}
	serveJSON(t, h, as("POST", fmt.Sprintf("/hosted/%d/continue", copyID), "", f5("dave", "read")), 404, nil)
	var n int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM runs WHERE parent_id=0 AND title LIKE 'plan it%'`).Scan(&n)
	if n != 1 {
		t.Fatalf("BUG: %d copies at global", n)
	}
}

// TestHostedContinueClaim: one continue at a time — a fresh claim answers
// 409, one a stopped process left is taken again.
func TestHostedContinueClaim(t *testing.T) {
	_, h := globalAgent(t)
	tdb := withTeam(t)
	tr := tdb.runs
	stubMail(t)
	quickWakes(t)
	id := said(t, tr, hostedRun(t, tr, "alice", map[string]string{"bob": roleParticipant}, "user:alice", "hello"), "hello")
	_, _ = tr.q.Exec(`UPDATE team_hosts SET state='continuing', reason='carol', since=? WHERE run_id=?`, now(), id)
	serveJSON(t, h, as("POST", fmt.Sprintf("/hosted/%d/continue", id), "", f5("bob", "read")), 409, nil)
	_, _ = tr.q.Exec(`UPDATE team_hosts SET since=? WHERE run_id=?`, now()-600, id)
	serveJSON(t, h, as("POST", fmt.Sprintf("/hosted/%d/continue", id), "", f5("bob", "read")), 200, nil)
}

// TestHostedUnshareAndDelete: the last member leaving a hosted conversation
// un-shares it — it leaves team for the global instance (a tombstone
// answers where) and its host is rung; deleting one rings the host too; a
// conversation moving to its owner's partition can't be hosted.
func TestHostedUnshareAndDelete(t *testing.T) {
	ag, h := globalAgent(t)
	tdb := withTeam(t)
	tr := tdb.runs
	mail := stubMail(t)
	quickWakes(t)
	id := said(t, tr, hostedRun(t, tr, "alice", map[string]string{"bob": roleParticipant}, "user:bob", "bob hosts alice's"), "bob hosts alice's")
	var out struct {
		MovedTo int64 `json:"movedTo"`
	}
	serveJSON(t, h, as("DELETE", fmt.Sprintf("/runs/%d/members/bob", id), "", f5("bob", "read")), 200, &out)
	if out.MovedTo <= 0 || hostedID(out.MovedTo) {
		t.Fatalf("un-shared: moved to #%d", out.MovedTo)
	}
	if a, err := ag.aclOf(out.MovedTo); err != nil || a.owner != "alice" || len(a.members) != 0 {
		t.Fatalf("the un-shared conversation at global: %+v %v", a, err)
	}
	if th, _ := tr.teamHost(id); th == nil || th.State != hostContinued || th.ContinuedTo != out.MovedTo {
		t.Fatalf("the tombstone: %+v", th)
	}
	sent := mail.wait(t, 1)
	if sent[0].to != "user:bob" || !strings.Contains(string(sent[0].data), `"signal":"audience"`) {
		t.Fatalf("the host's ring: %+v %s", sent[0], sent[0].data)
	}

	del := hostedRun(t, tr, "alice", map[string]string{"bob": roleParticipant}, "user:bob", "to delete")
	serveJSON(t, h, as("DELETE", fmt.Sprintf("/runs/%d", del), "", f5("alice", "read")), 200, nil)
	sent = mail.wait(t, 2)
	if sent[1].to != "user:bob" || !strings.Contains(string(sent[1].data), fmt.Sprint(del)) {
		t.Fatalf("BUG: deleting didn't ring the host: %+v", sent[1])
	}

	// the AF pack's move home (conv_moves 'asked'): not hostable meanwhile
	var run Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"x","share":{"members":[{"user":"bob","role":"participant"}]}}`, f5("alice", "read")), 200, &run)
	waitStatus(t, ag.db, run.ID, statusIdle)
	if _, err := ag.db.q.Exec(`CREATE TABLE IF NOT EXISTS conv_moves (root INTEGER PRIMARY KEY, owner TEXT, state TEXT, to_id INTEGER, created INTEGER, done INTEGER)`); err != nil {
		t.Fatal(err)
	}
	_, _ = ag.db.q.Exec(`INSERT INTO conv_moves (root, owner, state, created) VALUES (?, 'alice', 'asked', ?)`, run.ID, now())
	serveJSON(t, h, as("POST", "/hosted", fmt.Sprintf(`{"conversation":%d}`, run.ID), f5("alice", "read")), 409, nil)
}

// TestHostedApprovalsAndResume: a parked call in a hosted conversation runs
// with its host's resources — only the host approves it, anyone may deny;
// Retry (resume) works on a hosted run.
func TestHostedApprovalsAndResume(t *testing.T) {
	_, h := globalAgent(t)
	tdb := withTeam(t)
	tr := tdb.runs
	mail := stubMail(t)
	quickWakes(t)
	id := hostedRun(t, tr, "alice", map[string]string{"bob": roleParticipant, "carol": roleParticipant}, "user:bob", "hello")
	pend, _ := json.Marshal(pendingState{Kind: "approval", Park: "p1", ToolCalls: []toolCall{tc("c1", "xbin_call", `{}`)}})
	_, _ = tr.q.Exec(`UPDATE runs SET status='waiting_input', pending=? WHERE id=?`, string(pend), id)
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/approve", id), `{"approve":true,"park":"p1"}`, f5("carol", "read")), 403, nil)
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/approve", id), `{"approve":true,"park":"p1"}`, f5("bob", "read")), 200, nil)
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/approve", id), `{"approve":false,"park":"p1"}`, f5("carol", "read")), 200, nil)
	_, _ = tr.q.Exec(`UPDATE runs SET status='error', pending='' WHERE id=?`, id)
	before := len(tr.undelivered(id))
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/resume", id), `{}`, f5("carol", "read")), 200, nil)
	if n := len(tr.undelivered(id)); n != before+1 {
		t.Fatalf("resume queued %d", n-before)
	}
	mail.wait(t, 1)
}

// TestHostingConsentChecks: confirm needs what the host was shown; a pause
// times its own drop; a confirmation after 7 days doesn't bring it back.
func TestHostingConsentChecks(t *testing.T) {
	ag, h, tdb, _ := hostAgent(t)
	tr := tdb.runs
	id := hostedRun(t, tr, "bob", map[string]string{"alice": roleParticipant}, "user:alice", "legit hello")
	ag.hostSnapshot(t, tr, id)
	ensureHostEngine()
	waitFor(t, "the first answer", func() bool { return answered(tr, id, "ok") })
	addTeamMember(t, tr, id, "mallory", roleParticipant)
	ringAudience(t, ag, id)
	var row *hostedRow
	waitFor(t, "paused", func() bool { row, _ = ag.db.hostedRow(id); return row.State == hostPaused })
	serveJSON(t, h, as("POST", fmt.Sprintf("/hosting/%d/confirm", id), `{}`, alicesFrame("read")), 400, nil)
	serveJSON(t, h, as("POST", fmt.Sprintf("/hosting/%d/confirm", id), `{"seen":""}`, alicesFrame("read")), 400, nil)
	_, _ = ag.db.q.Exec(`UPDATE hosted SET paused_at=? WHERE conversation=?`, time.Now().Add(-hostPauseTTL-time.Hour).Unix(), id)
	serveJSON(t, h, as("POST", fmt.Sprintf("/hosting/%d/confirm", id), fmt.Sprintf(`{"seen":%q}`, row.PendingKey), alicesFrame("read")), 409, nil)
	if row, _ = ag.db.hostedRow(id); row.State != hostDropped {
		t.Fatalf("BUG: confirmed after the members were shown it dropped: %+v", row)
	}

	// a pause arms its own timer
	old := hostPauseTTL
	hostPauseTTL = 50 * time.Millisecond
	t.Cleanup(func() { hostPauseTTL = old })
	id2 := hostedRun(t, tr, "bob", map[string]string{"alice": roleParticipant}, "user:alice", "second hello")
	ag.hostSnapshot(t, tr, id2)
	hostEngine.Load().Poke(id2)
	waitFor(t, "the second answer", func() bool { return answered(tr, id2, "ok") })
	addTeamMember(t, tr, id2, "mallory", roleViewer)
	ringAudience(t, ag, id2)
	waitFor(t, "the pause's own timer drops it", func() bool { r, _ := ag.db.hostedRow(id2); return r.State == hostDropped })
}

// TestHostedWakeUp: the host engine's exit leaves a wake-up for what its
// active hosted conversations wait for — `wake` at a sleeping run's time
// (the earlier of its own and the partition's), `resume` for work now —
// and nothing for a paused one.
func TestHostedWakeUp(t *testing.T) {
	var mu sync.Mutex
	var jobs []map[string]any
	fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "PUT" && r.URL.Path == "/api/xbin/cron/jobs" {
			var j map[string]any
			_ = json.NewDecoder(r.Body).Decode(&j)
			jobs = append(jobs, j)
		}
		w.WriteHeader(200)
	}))
	take := func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		out := jobs
		jobs = nil
		return out
	}
	ag, _ := userAgent(t)
	if err := ag.db.addHostedSchema(); err != nil {
		t.Fatal(err)
	}
	tdb := withTeam(t)
	tr := tdb.runs
	id := hostedRun(t, tr, "bob", map[string]string{"alice": roleParticipant}, "user:alice", "check back later")
	_, _ = tr.q.Exec(`UPDATE inbox SET delivered_at=1 WHERE run_id=?`, id)
	ag.hostSnapshot(t, tr, id)
	host := &Agent{db: tr}
	wake := time.Now().Add(5 * time.Minute).Unix()
	_, _ = tr.q.Exec(`UPDATE runs SET status='sleeping', wake_at=? WHERE id=?`, wake, id)
	hostedWakeUp(host, tr)
	if j := take(); len(j) != 1 || j[0]["name"] != "wake" || j[0]["schedule"] != wakeSchedule(wake) {
		t.Fatalf("a hosted run sleeping 5 minutes: %v", j)
	}
	// the partition's own sleeping run wakes earlier: the shared `wake` job takes the earlier
	own, _ := ag.db.createRun("mine", "", 0)
	earlier := time.Now().Add(3 * time.Minute).Unix()
	_, _ = ag.db.q.Exec(`UPDATE runs SET status='sleeping', wake_at=? WHERE id=?`, earlier, own)
	hostedWakeUp(host, tr)
	if j := take(); len(j) != 1 || j[0]["schedule"] != wakeSchedule(earlier) {
		t.Fatalf("the earlier of the two: %v", j)
	}
	// a settled subagent its parent takes: resume
	child, _ := tr.createRun("child", "", id)
	_, _ = tr.q.Exec(`UPDATE runs SET status='awaiting', wake_at=0 WHERE id=?`, id)
	_, _ = tr.q.Exec(`UPDATE runs SET root_id=? WHERE id=?`, id, child)
	_, _ = tr.q.Exec(`INSERT INTO links (parent_id, child_id, tool_call_id, mode, state, delivered, created) VALUES (?, ?, 'c1', 'fg', 'done', 0, ?)`, id, child, now())
	hostedWakeUp(host, tr)
	if j := take(); len(j) != 1 || j[0]["name"] != "resume" {
		t.Fatalf("a settled subagent: %v", j)
	}
	// paused: nothing
	_, _ = ag.db.q.Exec(`UPDATE hosted SET state='paused' WHERE conversation=?`, id)
	hostedWakeUp(host, tr)
	if j := take(); len(j) != 0 {
		t.Fatalf("a paused conversation left %v", j)
	}
}

// TestHostEngineLocks: host engines take real per-host locks beside team —
// a successor of the same person waits on the lock until its predecessor
// lets go, then fences it; another person's engine takes its own at once.
func TestHostEngineLocks(t *testing.T) {
	ag, _ := userAgent(t)
	tdb := withTeam(t)
	open := func() *DB {
		db, err := openTeamSQL(tdb.path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return &DB{sql: db, q: db}
	}
	mk := func(db *DB, key string) *Engine {
		e := newEngine(db, &Agent{db: db, noGateway: true}, fakeOf(ag), tdb.path+".engine."+key)
		e.epochKey = "engine_epoch." + key
		e.scope = func(int64) bool { return false }
		return e
	}
	owned := func(e *Engine) bool { e.mu.Lock(); defer e.mu.Unlock(); return e.owned }
	nop := func(*DB) error { return nil }
	a1 := mk(open(), "u-alice")
	a1.Start()
	waitFor(t, "the first engine to own alice's hosting", func() bool { return owned(a1) })
	a2 := mk(open(), "u-alice")
	a2.Start()
	b := mk(open(), "u-bob")
	b.Start()
	waitFor(t, "bob's engine, at once", func() bool { return owned(b) })
	time.Sleep(100 * time.Millisecond)
	if owned(a2) {
		t.Fatal("BUG: alice's successor took over while her predecessor held the lock")
	}
	if err := a1.fenced(nop); err != nil {
		t.Fatalf("bob's takeover fenced alice's engine: %v", err)
	}
	a1.Shutdown(time.Second)
	waitFor(t, "the successor to take over", func() bool { return owned(a2) })
	if err := a1.fenced(nop); err != errFenced {
		t.Fatalf("the predecessor after its successor: %v", err)
	}
	t.Cleanup(func() { a2.Shutdown(time.Second); b.Shutdown(time.Second) })
	if tdb.runs.getSetting("engine_epoch.u-alice") != "2" || tdb.runs.getSetting("engine_epoch.u-bob") != "1" {
		t.Fatal("epochs: not per host")
	}
}
