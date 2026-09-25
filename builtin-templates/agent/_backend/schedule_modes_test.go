package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func mkSchedule(t *testing.T, ag *Agent, s *Schedule) *Schedule {
	t.Helper()
	id, err := ag.db.createSchedule(s)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ag.db.getSchedule(id)
	return got
}

// Where a firing goes: a new run each time (its owner's, listed under the
// automation), one ongoing thread (reset starts it afresh), or into the
// conversation it reports to — as a message there, falling back to a run of
// its own when that conversation is gone.
func TestScheduleModes(t *testing.T) {
	ag, _ := accessFixture(t)
	f := fakeOf(ag)
	f.on(nil, say("done"))

	iso := mkSchedule(t, ag, &Schedule{Name: "digest", Cron: "@daily", Goal: "digest it", Owner: "alice", Visibility: visPrivate})
	ag.fireSchedule(iso)
	iso, _ = ag.db.getSchedule(iso.ID)
	r1, err := ag.db.getRun(iso.LastRunID)
	if err != nil || r1.Owner != "alice" || r1.Visibility != visPrivate || r1.Origin != "schedule" || r1.OriginID != iso.ID {
		t.Fatalf("isolated firing: %+v %v", r1, err)
	}
	waitFor(t, "the report", func() bool { r, _ := ag.db.getRun(r1.ID); return r.Status == statusIdle })
	if s, _ := ag.db.getSchedule(iso.ID); s.LastStatus != "ok" {
		t.Fatalf("last status: %q", s.LastStatus)
	}

	thread := mkSchedule(t, ag, &Schedule{Name: "standup", Cron: "@daily", Goal: "standup notes", Owner: "alice", Visibility: visPrivate, Mode: modePersistent})
	ag.fireSchedule(thread)
	first, _ := ag.db.getSchedule(thread.ID)
	waitFor(t, "the first standup", func() bool { r, _ := ag.db.getRun(first.LastRunID); return r.Status == statusIdle })
	ag.fireSchedule(thread)
	second, _ := ag.db.getSchedule(thread.ID)
	if second.LastRunID != first.LastRunID {
		t.Fatalf("a thread reuses its run: %d then %d", first.LastRunID, second.LastRunID)
	}
	waitFor(t, "two standups in one run", func() bool {
		var n int
		_ = ag.db.q.QueryRow(`SELECT count(*) FROM messages WHERE run_id=? AND role='user'`, first.LastRunID).Scan(&n)
		return n == 2
	})
	if err := automationKinds["schedule"].Reset(who{kind: whoUser, user: "alice", level: "read"}, thread.ID); err != nil {
		t.Fatal(err)
	}
	ag.fireSchedule(thread)
	third, _ := ag.db.getSchedule(thread.ID)
	if third.LastRunID == first.LastRunID {
		t.Fatal("after a reset the thread starts afresh")
	}

	// into a conversation
	chat := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	conv := mkSchedule(t, ag, &Schedule{Name: "remind", Cron: "@daily", Goal: "remind me to stretch", Owner: "alice", Visibility: visPrivate,
		Mode: modeConversation, TargetRun: chat})
	ag.fireSchedule(conv)
	waitFor(t, "the reminder in the chat", func() bool {
		var meta string
		_ = ag.db.q.QueryRow(`SELECT meta FROM messages WHERE run_id=? AND role='user' ORDER BY id DESC LIMIT 1`, chat).Scan(&meta)
		return strings.Contains(meta, `"origin":"schedule"`) && strings.Contains(meta, `"label":"remind"`)
	})
	// the chat is deleted: the next firing becomes a run of its own
	if _, err := ag.db.q.Exec(`DELETE FROM runs WHERE id=?`, chat); err != nil {
		t.Fatal(err)
	}
	ag.fireSchedule(conv)
	after, _ := ag.db.getSchedule(conv.ID)
	if after.Mode != modeIsolated || after.LastRunID == chat || !strings.Contains(after.LastStatus, "gone") {
		t.Fatalf("fallback: mode %q last run %d status %q", after.Mode, after.LastRunID, after.LastStatus)
	}
}

// The agent's schedule tool reports into its own conversation by default.
func TestScheduleToolDefaultsHere(t *testing.T) {
	ag, _ := accessFixture(t)
	chat := runAs(t, ag, runStamp{Owner: "bob", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	r, _ := ag.db.getRun(chat)
	out, err := ag.runTool(t.Context(), r, defaultConfig(), "schedule", map[string]any{"cron": "@daily", "goal": "check the build"})
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	_, _ = fmt.Sscanf(out, "scheduled #%d", &id)
	s, err := ag.db.getSchedule(id)
	if err != nil || s.Mode != modeConversation || s.TargetRun != chat || s.Owner != "bob" || s.CreatedByRun != chat {
		t.Fatalf("tool schedule: %+v %v (%s)", s, err, out)
	}
	out, _ = ag.runTool(t.Context(), r, defaultConfig(), "schedule", map[string]any{"cron": "@daily", "goal": "weekly report", "deliver": "new"})
	_, _ = fmt.Sscanf(out, "scheduled #%d", &id)
	if s, _ := ag.db.getSchedule(id); s.Mode != modeIsolated || s.TargetRun != 0 {
		t.Fatalf("deliver new: %+v", s)
	}
}

// The Automations list: yours and the team's, unread counts; a manager
// oversees others' private ones without their goal.
func TestAutomationsAPI(t *testing.T) {
	ag, mux := accessFixture(t)
	f := fakeOf(ag)
	f.on(nil, say("done"))
	_ = ag.db.putSetting("conv_epoch_ms", "1")
	s := mkSchedule(t, ag, &Schedule{Name: "alice digest", Cron: "@daily", Goal: "secret digest", Owner: "alice", Visibility: visPrivate})
	ag.fireSchedule(s)
	s, _ = ag.db.getSchedule(s.ID)
	waitFor(t, "the run", func() bool { r, _ := ag.db.getRun(s.LastRunID); return r.Status == statusIdle })
	list := func(c caller) []AutomationItem {
		var out struct{ Items []AutomationItem }
		_ = json.Unmarshal(callAs(t, mux, c, "GET", "/automations", nil).Body.Bytes(), &out)
		return out.Items
	}
	a := list(asAlice)
	if len(a) != 1 || a[0].Access != "owner" || a[0].Runs != 1 || a[0].Unread != 1 {
		t.Fatalf("alice's automations: %+v", a)
	}
	if b := list(asBob); len(b) != 0 {
		t.Fatalf("bob sees alice's private automation: %+v", b)
	}
	if m := list(asMgr); len(m) != 1 || m[0].Access != "oversee" || strings.Contains(m[0].Summary, "secret") || m[0].Runs != 0 {
		t.Fatalf("a manager's oversight: %+v", m)
	}
	var runs struct{ Items []map[string]any }
	_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", fmt.Sprintf("/automations/schedule/%d/runs", s.ID), nil).Body.Bytes(), &runs)
	if len(runs.Items) != 1 || runs.Items[0]["unread"] != true {
		t.Fatalf("its runs: %+v", runs.Items)
	}
	callAs(t, mux, asAlice, "POST", fmt.Sprintf("/automations/schedule/%d/read", s.ID), nil)
	if a := list(asAlice); a[0].Unread != 0 {
		t.Fatalf("read: %+v", a[0])
	}
	var sum map[string]int
	_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", "/automations?summary=1", nil).Body.Bytes(), &sum)
	if sum["count"] != 1 || sum["unread"] != 0 {
		t.Fatalf("summary: %v", sum)
	}
	// conversations don't list automation runs
	var conv convPage
	_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", "/conversations", nil).Body.Bytes(), &conv)
	if len(conv.Items) != 0 {
		t.Fatalf("an automation's run is not a conversation: %v", convIDs(conv.Items))
	}
}

func TestResetPolicies(t *testing.T) {
	at := time.Date(2026, 9, 26, 10, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		policy string
		lastIn time.Time
		stale  bool
	}{
		{"", at.Add(-100 * time.Hour), false},
		{"idle:3600", at.Add(-30 * time.Minute), false},
		{"idle:3600", at.Add(-2 * time.Hour), true},
		{"daily:4", at.Add(-2 * time.Hour), false},   // 08:00 today, after today's 04:00
		{"daily:4", at.Add(-7 * time.Hour), true},    // 03:00 today, before it
		{"daily:12", at.Add(-23 * time.Hour), true},  // yesterday 11:00, before yesterday's 12:00
		{"daily:12", at.Add(-21 * time.Hour), false}, // yesterday 13:00, after it
	} {
		if got := policyStale(tc.policy, tc.lastIn.Unix(), at); got != tc.stale {
			t.Errorf("%q, last in %s: stale=%v", tc.policy, tc.lastIn.Format("Jan 2 15:04"), got)
		}
	}
}
