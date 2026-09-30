package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The AF × B2d interplay (W5-wire): a hosted (non-secure) conversation lives
// in team; un-sharing moves a person's chat at the global instance to their
// own partition (90 §I10). These pin how the two meet.

// convMoveMail is the conv/move mail sent to `to` for run, if any.
func convMoveMail(sent []mailRec, to string, run int64) bool {
	for _, m := range sent {
		var it moveItem
		if m.topic == topicMove && m.to == to && json.Unmarshal(m.data, &it) == nil && it.Run == run && it.Key != "" {
			return true
		}
	}
	return false
}

// TestHostedUnshareMovesHome: un-sharing a hosted conversation (its last
// member leaving; made private with nobody in it) ends hosting — back at the
// global instance as its owner's plain conversation — and from there it
// moves on to the owner's own partition like any un-shared chat: a
// conv/move mail to the owner, changes refused while it moves, its owner's
// partition can export it. The host is someone else (bob) in the first
// case: his resources leave with the sharing.
func TestHostedUnshareMovesHome(t *testing.T) {
	ag, h := moveAgent(t)
	tdb := withTeam(t)
	tr := tdb.runs
	mail := stubMail(t)
	quickWakes(t)

	// bob hosts alice's conversation, then leaves it
	id := said(t, tr, hostedRun(t, tr, "alice", map[string]string{"bob": roleParticipant}, "user:bob", "bob hosts alice's"), "bob hosts alice's")
	var out struct {
		MovedTo int64 `json:"movedTo"`
	}
	serveJSON(t, h, as("DELETE", fmt.Sprintf("/runs/%d/members/bob", id), "", f5("bob", "read")), 200, &out)
	back := out.MovedTo
	if back <= 0 || hostedID(back) {
		t.Fatalf("un-shared: moved to #%d", back)
	}
	if st, _ := moveRow(ag, back); st != "asked" {
		t.Fatalf("BUG: the un-shared conversation back at global isn't moving home: %q", st)
	}
	waitFor(t, "the conv/move mail to alice", func() bool {
		mail.mu.Lock()
		defer mail.mu.Unlock()
		return convMoveMail(mail.sent, "user:alice", back)
	})
	// while it moves: no changes, not hostable again, nothing copied into it
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", back), `{"text":"more"}`, f5("alice", "read")), 409, nil)
	rec := serveJSON(t, h, as("POST", "/hosted", fmt.Sprintf(`{"conversation":%d}`, back), f5("alice", "read")), 409, nil)
	if !strings.Contains(rec.Body.String(), "moving to its owner's own space") {
		t.Fatalf("POST /hosted on a moving conversation: %s", rec.Body)
	}
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/copyin", back), `{"files":[{"path":"a.txt","content":"x"}]}`, f5("alice", "read")), 409, nil)
	// …and alice's partition takes it: the export answers with a ticket
	if x := exportMove(t, h, ag, back, 200); x.Ticket == "" || len(x.Messages) == 0 {
		t.Fatalf("the export of the un-shared hosted conversation: ticket %q, %d messages", x.Ticket, len(x.Messages))
	}

	// alice hosts her own team-visible conversation and makes it private
	own := said(t, tr, hostedRun(t, tr, "alice", nil, "user:alice", "alice hosts her own"), "alice hosts her own")
	if _, err := tr.q.Exec(`UPDATE runs SET visibility='team' WHERE id=?`, own); err != nil {
		t.Fatal(err)
	}
	var item map[string]any
	serveJSON(t, h, as("PATCH", fmt.Sprintf("/runs/%d", own), `{"visibility":"private"}`, f5("alice", "read")), 200, &item)
	back2, _ := item["id"].(float64)
	if item["movedFrom"] != float64(own) || back2 <= 0 || hostedID(int64(back2)) {
		t.Fatalf("made private: %v", item)
	}
	if st, _ := moveRow(ag, int64(back2)); st != "asked" {
		t.Fatalf("BUG: made private, back at global, not moving home: %q", st)
	}
}

// TestHostedUnshareNotAPersons: a hosted conversation that isn't a person's
// (the owner token's) goes back to the global instance when un-shared and
// stays there, private — as an un-shared one of the owner token's does.
func TestHostedUnshareNotAPersons(t *testing.T) {
	ag, h := moveAgent(t)
	tdb := withTeam(t)
	tr := tdb.runs
	stubMail(t)
	quickWakes(t)
	id := said(t, tr, hostedRun(t, tr, "", map[string]string{"bob": roleParticipant}, "user:bob", "the token's"), "the token's")
	var out struct {
		MovedTo int64 `json:"movedTo"`
	}
	serveJSON(t, h, as("DELETE", fmt.Sprintf("/runs/%d/members/bob", id), "", f5("bob", "read")), 200, &out)
	if out.MovedTo <= 0 {
		t.Fatalf("un-shared: %+v", out)
	}
	if st, _ := moveRow(ag, out.MovedTo); st != "" {
		t.Fatalf("BUG: not a person's chat, yet moving: %q", st)
	}
}

// TestHostedMovingAtGlobal: a conversation moving out of the shared space
// ("asked", then "leaving") can't be taken into team (POST /hosted) nor
// copied into (POST /runs/{id}/copyin) — both are mounted outside
// globalRoute and ask movingRefused themselves; a move given up makes it
// hostable again.
func TestHostedMovingAtGlobal(t *testing.T) {
	ag, h := moveAgent(t)
	withTeam(t)
	stubMail(t)
	var run Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"x","share":{"members":[{"user":"bob","role":"participant"}]}}`, f5("alice", "read")), 200, &run)
	waitStatus(t, ag.db, run.ID, statusIdle)
	serveJSON(t, h, as("DELETE", fmt.Sprintf("/runs/%d/members/bob", run.ID), "", f5("alice", "read")), 200, nil)
	if st, _ := moveRow(ag, run.ID); st != "asked" {
		t.Fatalf("un-shared: %q", st)
	}
	for _, st := range []string{"asked", "leaving"} {
		_, _ = ag.db.q.Exec(`UPDATE conv_moves SET state=? WHERE root=?`, st, run.ID)
		serveJSON(t, h, as("POST", "/hosted", fmt.Sprintf(`{"conversation":%d}`, run.ID), f5("alice", "read")), 409, nil)
		serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/copyin", run.ID), `{"files":[{"path":"a.txt","content":"x"}]}`, f5("alice", "read")), 409, nil)
	}
	// given up: it stays here, private, and is alice's to host again
	_, _ = ag.db.q.Exec(`DELETE FROM conv_moves WHERE root=?`, run.ID)
	serveJSON(t, h, as("POST", "/hosted", fmt.Sprintf(`{"conversation":%d}`, run.ID), f5("alice", "read")), 200, nil)
}

// TestHostedNoChannelThreads: AF's staged handoff files belong to a channel's
// DMs (a person's partition) and replies; a channel's thread at the global
// instance can't be hosted, so no hosted conversation ever has a handoff.
func TestHostedNoChannelThreads(t *testing.T) {
	ag, h := moveAgent(t)
	withTeam(t)
	raw, _ := json.Marshal(defaultConfig())
	id, err := ag.db.createRunStamped("a group thread", string(raw), 0, statusIdle, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "channel"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.db.q.Exec(`INSERT INTO run_members (run_id, user, role, added_by, via, created) VALUES (?, 'bob', ?, 'alice', 'invite', ?)`, id, roleParticipant, now()); err != nil {
		t.Fatal(err)
	}
	rec := serveJSON(t, h, as("POST", "/hosted", fmt.Sprintf(`{"conversation":%d}`, id), f5("bob", "read")), 409, nil)
	if !strings.Contains(rec.Body.String(), "automation") {
		t.Fatalf("hosting a channel's thread: %s", rec.Body)
	}
}

// TestHostedExportAtGlobal: GET /runs/{id}/export on a hosted id at the
// global instance is team's (hostedRoute on homeRoutes): a member's "Copy
// to my own space" reads its transcript; anyone else 404.
func TestHostedExportAtGlobal(t *testing.T) {
	_, h := globalAgent(t)
	tdb := withTeam(t)
	id := said(t, tdb.runs, hostedRun(t, tdb.runs, "alice", map[string]string{"bob": roleViewer}, "user:alice", "the transcript"), "the transcript")
	var b convBundle
	serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/export", id), "", f5("bob", "read")), 200, &b)
	if len(b.Messages) == 0 || !strings.Contains(fmt.Sprint(b.Messages), "the transcript") {
		t.Fatalf("bob's export of the hosted conversation: %+v", b.Messages)
	}
	serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/export", id), "", f5("dave", "read")), 404, nil)
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/publish", id), `{}`, f5("alice", "read")), 409, nil)
}

// TestHostedRunEventNoMovedTo: the host's partition can't send the members'
// pages anywhere — a posted `run` event is re-read from team (B2d's
// allowlist), so a `deleted`/`movedTo` in it never reaches a stream; only
// the global instance's own delete (a continue, an un-share, AF's move)
// says where a conversation went.
func TestHostedRunEventNoMovedTo(t *testing.T) {
	_, h := globalAgent(t)
	tdb := withTeam(t)
	id := hostedRun(t, tdb.runs, "alice", map[string]string{"bob": roleParticipant}, "user:alice", "alice hosts")
	bob := followAs(t, h, fmt.Sprintf("/stream?run=%d", id), f5("bob", "read"))
	waitFor(t, "bob's stream says hello", func() bool {
		bob.mu.Lock()
		defer bob.mu.Unlock()
		return len(bob.evs) > 0
	})
	b, _ := json.Marshal(map[string]any{"events": []fwdEvent{{Type: evRun, Run: id, Root: id, Data: map[string]any{"id": id, "deleted": true, "movedTo": 4242}}}})
	serveJSON(t, h, as("POST", "/hosted/events", string(b), f5("alice", "read")), 200, nil)
	waitFor(t, "bob's stream to carry the run as team has it", func() bool {
		bob.mu.Lock()
		defer bob.mu.Unlock()
		for _, ev := range bob.evs {
			if ev.Type == evRun && ev.Run == id {
				return true
			}
		}
		return false
	})
	bob.mu.Lock()
	defer bob.mu.Unlock()
	for _, ev := range bob.evs {
		if s := fmt.Sprint(ev.Data); ev.Type == evRun && (strings.Contains(s, "movedTo") || strings.Contains(s, "deleted:true")) {
			t.Fatalf("BUG: the host's post moved bob's page: %v", ev.Data)
		}
	}
}
