package main

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// stampedConv is a conversation of alice's at the global instance as origin
// makes one (a channel's, a schedule's thread…), with a user message when
// said, shared with members.
func stampedConv(t *testing.T, ag *Agent, origin, vis string, said bool, members ...string) int64 {
	t.Helper()
	id, err := ag.db.createRunStamped(origin+" thread", "{}", 0, statusIdle,
		runStamp{Owner: "alice", Visibility: vis, TeamRole: roleViewer, Origin: origin, OriginID: 1, TitleSrc: "origin"})
	if err != nil {
		t.Fatal(err)
	}
	if said {
		if _, err := ag.db.addMessage(&Message{RunID: id, Role: "user", Content: "hello"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range members {
		if _, err := ag.db.q.Exec(`INSERT INTO run_members (run_id, user, role, added_by, via, created) VALUES (?, ?, 'participant', 'alice', 'invite', ?)`, id, m, now()); err != nil {
			t.Fatal(err)
		}
	}
	ag.acl.flush(id)
	return id
}

// TestUnshareOnlyChats (the review of AF, finding 1): only a person's chat
// moves, and only on the act that took its last sharing away. An
// automation's thread at the global instance — a channel's group thread or
// unlinked DM, a schedule's, a trigger's — stays when made private or when
// its last member leaves; so does a chat nobody said anything in; and a
// PATCH that changes nothing about who shares it (the same visibility
// again, a team role on a private one) moves nothing.
func TestUnshareOnlyChats(t *testing.T) {
	ag, h := moveAgent(t)
	stubMail(t)
	patch := func(id int64, body string) {
		t.Helper()
		serveJSON(t, h, as("PATCH", fmt.Sprintf("/runs/%d", id), body, f5("alice", "read")), 200, nil)
	}
	stays := func(what string, id int64) {
		t.Helper()
		if st, _ := moveRow(ag, id); st != "" {
			t.Errorf("%s started moving (%q)", what, st)
		}
	}
	// a channel's group thread: its last member leaves; made private
	group := stampedConv(t, ag, "channel", visPrivate, true, "bob")
	serveJSON(t, h, as("DELETE", fmt.Sprintf("/runs/%d/members/bob", group), "", f5("bob", "read")), 200, nil)
	stays("a channel's thread its last member left", group)
	team := stampedConv(t, ag, "channel", visTeam, true)
	patch(team, `{"visibility":"private"}`)
	stays("a channel's team thread made private", team)
	// an unlinked DM (a channel's, private by default): its share settings saved unchanged
	dm := stampedConv(t, ag, "channel", visPrivate, true)
	patch(dm, `{"teamRole":"viewer"}`)
	patch(dm, `{"visibility":"private","teamRole":"participant"}`)
	stays("a channel's DM thread saved unchanged", dm)
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", dm), `{"text":"still mine"}`, f5("alice", "read")), 200, nil)
	// a schedule's and a trigger's thread made private
	for _, o := range []string{"schedule", "trigger", "watcher", "held"} {
		id := stampedConv(t, ag, o, visTeam, true)
		patch(id, `{"visibility":"private"}`)
		stays("a "+o+"'s thread made private", id)
	}
	// a chat nobody ever said anything in
	quietChat := stampedConv(t, ag, "chat", visTeam, false)
	patch(quietChat, `{"visibility":"private"}`)
	stays("a chat never said anything in", quietChat)
	// a private chat nobody is in: saving its settings again moves nothing
	mine := stampedConv(t, ag, "chat", visPrivate, true)
	patch(mine, `{"visibility":"private"}`)
	patch(mine, `{"teamRole":"participant"}`)
	stays("a private chat saved unchanged", mine)
	// …but one shared with the team made private does
	chat := stampedConv(t, ag, "chat", visTeam, true)
	patch(chat, `{"visibility":"private"}`)
	if st, _ := moveRow(ag, chat); st != "asked" {
		t.Fatalf("a team chat made private: %q", st)
	}
	quiet(t, ag)
}

// TestMoveWaitsAndRechecks (the review of AF, findings 2, 3 and 5): a moving
// conversation is let go only as it was read and at rest — done answers
// 412 (read it again) after any change, and while a sandbox command still
// runs the export waits; an automation's delivery into it is refused, while
// stopping it or deciding an approval isn't. One that left the shared space
// another way (a hosted conversation, say) is no move any more: its done is
// 409 and its row goes. A done cut short after the delete finishes.
func TestMoveWaitsAndRechecks(t *testing.T) {
	ag, h := moveAgent(t)
	stubMail(t)
	unshared := func() int64 {
		t.Helper()
		id := stampedConv(t, ag, "chat", visTeam, true)
		serveJSON(t, h, as("PATCH", fmt.Sprintf("/runs/%d", id), `{"visibility":"private"}`, f5("alice", "read")), 200, nil)
		if st, _ := moveRow(ag, id); st != "asked" {
			t.Fatalf("not moving: %q", st)
		}
		return id
	}
	x := unshared()
	// a command still running in one of its sandboxes: not read yet
	if _, err := ag.db.q.Exec(`INSERT INTO sandbox_jobs (root_id, job, run_id, tool_call_id, ref, command, created_ms, state) VALUES (?, 1, ?, 'c1', 'x', 'make', 1, 'running')`, x, x); err != nil {
		t.Fatal(err)
	}
	exportMove(t, h, ag, x, 409)
	_, _ = ag.db.q.Exec(`UPDATE sandbox_jobs SET state='done' WHERE root_id=?`, x)
	// read; then it changes (a turn's message): done asks for it again
	b := exportMove(t, h, ag, x, 200)
	if _, err := ag.db.addMessage(&Message{RunID: x, Role: "assistant", Content: "a late turn"}); err != nil {
		t.Fatal(err)
	}
	doneMove(t, h, x, partitionIDBase+1, b.Ticket, moveKeyOf(ag, x), f5("alice", "read"), 412)
	b = exportMove(t, h, ag, x, 200)
	if !hasMsg(&b.convBundle, "assistant", "a late turn") {
		t.Fatal("read again without the late turn")
	}
	// at work again after it was read: 412 too
	_, _ = ag.db.q.Exec(`UPDATE runs SET status='sleeping' WHERE id=?`, x)
	doneMove(t, h, x, partitionIDBase+1, b.Ticket, moveKeyOf(ag, x), f5("alice", "read"), 412)
	_, _ = ag.db.q.Exec(`UPDATE runs SET status='idle' WHERE id=?`, x)
	// an automation's delivery into it: refused; stopping it, deciding an approval: not by the move
	if _, _, err := ag.deliverInbound(inbound{Mode: "run", RunID: x, Stamp: runStamp{Owner: "alice", Origin: "schedule"}, Source: "schedule", Text: "tick"}); !errors.Is(err, errConvMoving) {
		t.Fatalf("a schedule's delivery into a moving conversation: %v", err)
	}
	for _, c := range []struct{ path, body string }{{"/cancel", ""}, {"/interrupt", ""}, {"/approve", `{"approve":true}`}} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, as("POST", fmt.Sprintf("/runs/%d%s", x, c.path), c.body, f5("alice", "read")))
		if strings.Contains(rec.Body.String(), "moving to its owner's own space") {
			t.Errorf("POST %s refused while moving: %d %s", c.path, rec.Code, rec.Body)
		}
	}
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/answer", x), `{"text":"yes"}`, f5("alice", "read")), 409, nil) // nothing waits for an answer
	b = exportMove(t, h, ag, x, 200)
	if m := doneMove(t, h, x, partitionIDBase+1, b.Ticket, moveKeyOf(ag, x), f5("alice", "read"), 200); m.State != "moved" {
		t.Fatalf("done at rest, unchanged: %+v", m)
	}

	// it left another way while moving (B2d's hosting takes a resting,
	// private conversation into team): no move any more
	y := unshared()
	b = exportMove(t, h, ag, y, 200)
	if err := ag.deleteRunTree(y); err != nil {
		t.Fatal(err)
	}
	doneMove(t, h, y, partitionIDBase+2, b.Ticket, moveKeyOf(ag, y), f5("alice", "read"), 409)
	if st, _ := moveRow(ag, y); st != "" {
		t.Fatalf("a conversation that left another way still moves: %q", st)
	}
	z := unshared()
	_ = ag.deleteRunTree(z)
	exportMove(t, h, ag, z, 404)
	if st, _ := moveRow(ag, z); st != "" {
		t.Fatalf("export of one that left another way: %q", st)
	}

	// a done cut short after the delete (state leaving): finished by the same done, refused for another
	w := unshared()
	b = exportMove(t, h, ag, w, 200)
	_, _ = ag.db.q.Exec(`UPDATE conv_moves SET state='leaving', to_id=? WHERE root=?`, partitionIDBase+7, w)
	_ = ag.deleteRunTree(w)
	doneMove(t, h, w, partitionIDBase+8, b.Ticket, moveKeyOf(ag, w), f5("alice", "read"), 409)
	if m := doneMove(t, h, w, partitionIDBase+7, b.Ticket, moveKeyOf(ag, w), f5("alice", "read"), 200); m.State != "moved" || m.To != partitionIDBase+7 {
		t.Fatalf("a done cut short, finished: %+v", m)
	}
	if st, to := moveRow(ag, w); st != "moved" || to != partitionIDBase+7 {
		t.Fatalf("its tombstone: %s %d", st, to)
	}
	quiet(t, ag)
}

// TestMoveCarries (the review of AF, finding 4): a move carries what is the
// conversation's alone — its notes, its owner's schedules reporting into it
// (gone from here at done), its owner's pin — and names what it leaves
// behind (a subagent's transcript, granted capabilities). Someone else's
// schedule into it stays.
func TestMoveCarries(t *testing.T) {
	ag, h := moveAgent(t)
	stubMail(t)
	x := stampedConv(t, ag, "chat", visTeam, true)
	if err := ag.db.memorySet(x, "plan", "ship friday"); err != nil {
		t.Fatal(err)
	}
	mine, _ := ag.db.createSchedule(&Schedule{Name: "standup", Cron: "0 9 * * *", Goal: "ask how it went", Owner: "alice", Visibility: visTeam,
		Mode: modeConversation, TargetRun: x, CreatedByRun: x})
	legacy, _ := ag.db.createSchedule(&Schedule{Name: "old", Cron: "0 9 * * *", Goal: "report", Mode: modeConversation, TargetRun: x})
	if _, err := ag.db.createRunStamped("helper", "{}", x, statusDone, runStamp{}); err != nil {
		t.Fatal(err)
	}
	_, _ = ag.db.q.Exec(`INSERT INTO run_grants (root_id, cap, granted_by, expires_ms, created_ms) VALUES (?, 'net', 'alice', 0, 1)`, x)
	ag.db.setUserState(x, "alice", func(s *userState) { s.PinnedAt = 4321 })
	serveJSON(t, h, as("PATCH", fmt.Sprintf("/runs/%d", x), `{"visibility":"private"}`, f5("alice", "read")), 200, nil)
	b := exportMove(t, h, ag, x, 200)
	if b.Memory["plan"] != "ship friday" || b.PinnedAt != 4321 {
		t.Fatalf("its notes, alice's pin: %+v %d", b.Memory, b.PinnedAt)
	}
	if len(b.Schedules) != 1 || b.Schedules[0].ID != mine || b.Schedules[0].Goal != "ask how it went" {
		t.Fatalf("the schedules that move: %+v", b.Schedules)
	}
	behind := strings.Join(b.Behind, "; ")
	if !strings.Contains(behind, "subagent's own transcript") || !strings.Contains(behind, "capabilities") {
		t.Fatalf("what stays behind: %q", behind)
	}
	doneMove(t, h, x, partitionIDBase+1, b.Ticket, moveKeyOf(ag, x), f5("alice", "read"), 200)
	var left []int64
	all, err := ag.db.listSchedules()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		left = append(left, s.ID)
	}
	if slices.Contains(left, mine) || !slices.Contains(left, legacy) {
		t.Fatalf("schedules left here: %v (alice's %d went with it, the legacy %d stays)", left, mine, legacy)
	}
	quiet(t, ag)
}
