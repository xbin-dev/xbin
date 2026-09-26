package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// threadsWorld is alice's conversation A and what surrounds it:
//
//	S1 — a schedule A created (isolated): threads T1, T2 (mine)
//	S2 — alice's schedule made by hand: thread T3 (all)
//	S3 — bob's schedule: thread T4 (neither)
//	C2 — alice's other chat (all); B2 — bob's, alice added (all)
//	B1 — bob's, shared with the team (neither: the team pool)
//	B3 — bob's private (neither); H — alice's held draft (neither)
type threadsWorld struct {
	ag                                *Agent
	A, T1, T2, T3, T4, C2, B1, B2, B3 int64
	H                                 int64
	S1, S2, S3                        int64
}

func newThreadsWorld(t *testing.T) (*threadsWorld, *Agent) {
	ag, _ := accessFixture(t)
	return buildThreadsWorld(t, ag), ag
}

func buildThreadsWorld(t *testing.T, ag *Agent) *threadsWorld {
	t.Helper()
	w := &threadsWorld{ag: ag}
	mk := func(title string, st runStamp) int64 {
		cfg := defaultConfig()
		cfg.Features = map[string]bool{"titles": false, "streaming": false}
		r, err := ag.startRunOpts(runOpts{Title: title, Cfg: cfg, Hold: true, Stamp: st})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	private := func(owner, origin string) runStamp {
		return runStamp{Owner: owner, Visibility: visPrivate, TeamRole: roleViewer, Origin: origin}
	}
	w.A = mk("planning chat", private("alice", "chat"))
	sched := func(s *Schedule) int64 {
		id, err := ag.db.createSchedule(s)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	w.S1 = sched(&Schedule{Name: "digest", Cron: "@every 1h", Goal: "summarise the inbox", Owner: "alice", Visibility: visPrivate, Mode: modeIsolated, CreatedByRun: w.A})
	w.S2 = sched(&Schedule{Name: "backup check", Cron: "0 9 * * *", Goal: "check the backups", Owner: "alice", Visibility: visPrivate, Mode: modeIsolated})
	w.S3 = sched(&Schedule{Name: "bob's report", Cron: "0 8 * * *", Goal: "bob's secret report", Owner: "bob", Visibility: visPrivate, Mode: modeIsolated})
	fired := func(id int64, title string) int64 {
		s, _ := ag.db.getSchedule(id)
		return mk(title, s.stamp())
	}
	w.T1 = fired(w.S1, "digest monday")
	w.T2 = fired(w.S1, "digest tuesday")
	w.T3 = fired(w.S2, "backups fine")
	w.T4 = fired(w.S3, "bob report run")
	w.C2 = mk("holiday plans", private("alice", "chat"))
	w.B1 = mk("bob team notes", runStamp{Owner: "bob", Visibility: visTeam, TeamRole: roleViewer, Origin: "chat"})
	w.B2 = mk("bob and alice", private("bob", "chat"))
	if _, err := ag.db.q.Exec(`INSERT INTO run_members (run_id, user, role, created) VALUES (?, 'alice', 'viewer', 1)`, w.B2); err != nil {
		t.Fatal(err)
	}
	ag.acl.flush(w.B2)
	w.B3 = mk("bob private", private("bob", "chat"))
	w.H = mk("a held draft", private("alice", "held"))
	return w
}

// call runs a thread tool from conversation A (the grant given once when
// once is set).
func (w *threadsWorld) call(t *testing.T, cfg Config, once bool, name, args string) (string, error) {
	t.Helper()
	run, err := w.ag.db.getRun(w.A)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if once {
		ctx = withGrantOnce(ctx, capThreads)
	}
	return w.ag.runThreadTool(ctx, run, cfg, name, decodeArgs(args))
}

var idsRE = regexp.MustCompile(`(?m)^#(\d+) `)

func listedIDs(out string) []int64 {
	var ids []int64
	for _, m := range idsRE.FindAllStringSubmatch(out, -1) {
		var n int64
		fmt.Sscan(m[1], &n)
		ids = append(ids, n)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func sameIDs(t *testing.T, what string, got []int64, want ...int64) {
	t.Helper()
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

// Mine is this conversation's schedules and their threads; all is the
// owner's own and what was shared with them — never the team pool, never
// someone else's private thread, never a held draft.
func TestThreadToolsScopes(t *testing.T) {
	w, _ := newThreadsWorld(t)
	cfg := defaultConfig()

	out, err := w.call(t, cfg, false, "threads_list", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	sameIDs(t, "threads mine", listedIDs(out), w.T1, w.T2)

	if _, err := w.call(t, cfg, false, "threads_list", `{"scope":"all"}`); err == nil || !strings.Contains(err.Error(), "permission") {
		t.Fatalf("all without a grant: %v", err)
	}
	out, err = w.call(t, cfg, true, "threads_list", `{"scope":"all"}`)
	if err != nil {
		t.Fatal(err)
	}
	sameIDs(t, "threads all", listedIDs(out), w.A, w.T1, w.T2, w.T3, w.C2, w.B2)
	if !strings.Contains(out, "owner bob") || !strings.Contains(out, "this conversation") {
		t.Fatalf("all marks the conversation and others' owners:\n%s", out)
	}

	out, _ = w.call(t, cfg, false, "schedules_list", `{}`)
	sameIDs(t, "schedules mine", listedIDs(out), w.S1)
	if !strings.Contains(out, "a new thread each firing") || !strings.Contains(out, "summarise the inbox") {
		t.Fatalf("a schedule line:\n%s", out)
	}
	out, _ = w.call(t, cfg, true, "schedules_list", `{"scope":"all"}`)
	sameIDs(t, "schedules all", listedIDs(out), w.S1, w.S2)

	// inspecting: mine is free, the owner's needs the grant, the rest is missing
	for _, c := range []struct {
		name string
		id   int64
		want string // scope, or the error's start
	}{
		{"thread_inspect", w.T1, scopeMine}, {"thread_inspect", w.A, scopeMine},
		{"thread_inspect", w.C2, scopeAll}, {"thread_inspect", w.B2, scopeAll}, {"thread_inspect", w.T3, scopeAll},
		{"thread_inspect", w.B1, "no such thread"}, {"thread_inspect", w.B3, "no such thread"},
		{"thread_inspect", w.T4, "no such thread"}, {"thread_inspect", w.H, "no such thread"},
		{"thread_inspect", 9999, "no such thread"},
		{"schedule_inspect", w.S1, scopeMine}, {"schedule_inspect", w.S2, scopeAll}, {"schedule_inspect", w.S3, "no such schedule"},
	} {
		tcx, _ := w.ag.threadCtxOf(mustRun(t, w.ag, w.A), cfg)
		sc, err := w.ag.threadScope(tcx, c.name, map[string]any{"id": float64(c.id)})
		got := sc
		if err != nil {
			got = err.Error()
		}
		if !strings.HasPrefix(got, c.want) {
			t.Fatalf("%s #%d: %q, want %q", c.name, c.id, got, c.want)
		}
	}
	out, err = w.call(t, cfg, false, "schedule_inspect", fmt.Sprintf(`{"id":%d}`, w.S1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "by this conversation") || !strings.Contains(out, "digest monday") || !strings.Contains(out, "digest tuesday") {
		t.Fatalf("schedule_inspect:\n%s", out)
	}
	if _, err := w.call(t, cfg, false, "thread_inspect", fmt.Sprintf(`{"id":%d}`, w.C2)); err == nil {
		t.Fatal("another conversation without a grant")
	}
	if out, err = w.call(t, cfg, true, "thread_inspect", fmt.Sprintf(`{"id":%d}`, w.C2)); err != nil || !strings.Contains(out, "holiday plans") {
		t.Fatalf("with the grant: %v\n%s", err, out)
	}
}

func mustRun(t *testing.T, ag *Agent, id int64) *Run {
	t.Helper()
	r, err := ag.db.getRun(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A web-toolset run sees only web automations and never "all"; a chat
// channel's run and a conversation no person owns never "all" either.
func TestThreadToolsRefusals(t *testing.T) {
	w, ag := newThreadsWorld(t)
	web := defaultConfig()
	web.Toolset = "web"
	if out, _ := w.call(t, web, false, "schedules_list", `{}`); !strings.Contains(out, "no schedules") {
		t.Fatalf("web lane lists a private schedule:\n%s", out)
	}
	if out, _ := w.call(t, web, false, "threads_list", `{}`); !strings.Contains(out, "no threads") {
		t.Fatalf("web lane lists a private schedule's threads:\n%s", out)
	}
	for _, args := range []string{`{"scope":"all"}`, fmt.Sprintf(`{"id":%d}`, w.T1)} {
		name := "threads_list"
		if strings.Contains(args, "id") {
			name = "thread_inspect"
		}
		if _, err := w.call(t, web, true, name, args); err == nil || !strings.Contains(err.Error(), "web-toolset") {
			t.Fatalf("web lane %s %s: %v", name, args, err)
		}
	}
	// a web schedule made from a web conversation is its own
	wid, _ := ag.db.createSchedule(&Schedule{Name: "news", Cron: "@every 1h", Goal: "read the news", Owner: "alice", Toolset: "web", Mode: modeIsolated, CreatedByRun: w.A})
	if out, _ := w.call(t, web, false, "schedules_list", `{}`); fmt.Sprint(listedIDs(out)) != fmt.Sprint([]int64{wid}) {
		t.Fatalf("web lane, its own web schedule:\n%s", out)
	}
	ch := defaultConfig()
	ch.Channel = true
	if _, err := w.call(t, ch, true, "threads_list", `{"scope":"all"}`); err == nil || !strings.Contains(err.Error(), "chat channel") {
		t.Fatalf("a channel run: %v", err)
	}
	legacy, _ := ag.startRunOpts(runOpts{Title: "legacy", Cfg: defaultConfig(), Hold: true, Stamp: runStamp{Visibility: visTeam, TeamRole: roleParticipant}})
	tcx, _ := ag.threadCtxOf(legacy, defaultConfig())
	if _, err := ag.threadScope(tcx, "threads_list", map[string]any{"scope": "all"}); err == nil || !strings.Contains(err.Error(), "no person owns") {
		t.Fatalf("an unowned conversation: %v", err)
	}
	off := defaultConfig()
	off.Features = map[string]bool{"threads": false}
	if _, err := w.call(t, off, false, "threads_list", `{}`); err == nil {
		t.Fatal("the feature off")
	}
	if specs := toolSpecs(off, 0, nil); hasSpec(specs, "threads_list") {
		t.Fatal("offered with the feature off")
	}
	if specs := toolSpecs(defaultConfig(), 1, nil); hasSpec(specs, "threads_list") {
		t.Fatal("offered to a subagent")
	}
	if specs := toolSpecs(defaultConfig(), 0, nil); !hasSpec(specs, "thread_inspect") || !hasSpec(specs, "schedules_list") {
		t.Fatal("not offered at the top")
	}
}

func hasSpec(specs []toolSpec, name string) bool {
	for _, s := range specs {
		if s.Function.Name == name {
			return true
		}
	}
	return false
}

// Paging walks every thread once; filters narrow it.
func TestThreadToolsPagingAndFilters(t *testing.T) {
	w, ag := newThreadsWorld(t)
	cfg := defaultConfig()
	var seen []int64
	cursor := ""
	for page := 0; page < 10; page++ {
		args := `{"scope":"all","limit":2`
		if cursor != "" {
			args += `,"cursor":"` + cursor + `"`
		}
		out, err := w.call(t, cfg, true, "threads_list", args+`}`)
		if err != nil {
			t.Fatal(err)
		}
		ids := listedIDs(out)
		if len(ids) > 2 {
			t.Fatalf("page of %d", len(ids))
		}
		seen = append(seen, ids...)
		m := regexp.MustCompile(`more: cursor "([^"]+)"`).FindStringSubmatch(out)
		if m == nil {
			break
		}
		cursor = m[1]
	}
	sort.Slice(seen, func(i, j int) bool { return seen[i] < seen[j] })
	sameIDs(t, "paged", seen, w.A, w.T1, w.T2, w.T3, w.C2, w.B2)

	out, _ := w.call(t, cfg, true, "threads_list", `{"scope":"all","origin":"schedule"}`)
	sameIDs(t, "origin schedule", listedIDs(out), w.T1, w.T2, w.T3)
	out, _ = w.call(t, cfg, true, "threads_list", `{"scope":"all","q":"holiday"}`)
	sameIDs(t, "q by title", listedIDs(out), w.C2)
	// q also finds what was said
	if _, err := ag.db.addMessage(&Message{RunID: w.B2, Role: "user", Content: "the quarterly zeppelin budget"}); err != nil {
		t.Fatal(err)
	}
	out, _ = w.call(t, cfg, true, "threads_list", `{"scope":"all","q":"zeppelin"}`)
	sameIDs(t, "q by content", listedIDs(out), w.B2)
	if _, err := ag.db.q.Exec(`INSERT INTO run_user_state (run_id, user, archived_at) VALUES (?, 'alice', 5)`, w.C2); err != nil {
		t.Fatal(err)
	}
	out, _ = w.call(t, cfg, true, "threads_list", `{"scope":"all","archived":true}`)
	sameIDs(t, "archived", listedIDs(out), w.C2)
	out, _ = w.call(t, cfg, true, "threads_list", `{"scope":"all","archived":false,"origin":"chat"}`)
	sameIDs(t, "not archived chats", listedIDs(out), w.A, w.B2)
	out, _ = w.call(t, cfg, false, "schedules_list", `{"q":"inbox"}`)
	sameIDs(t, "schedules q", listedIDs(out), w.S1)
	out, _ = w.call(t, cfg, true, "schedules_list", `{"scope":"all","enabled":false}`)
	sameIDs(t, "schedules disabled", listedIDs(out))
}

// thread_inspect pages a transcript back by seq.
func TestThreadInspectPages(t *testing.T) {
	w, ag := newThreadsWorld(t)
	for i := 0; i < 7; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if _, err := ag.db.addMessage(&Message{RunID: w.T1, Role: role, Content: fmt.Sprintf("line %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := w.call(t, defaultConfig(), false, "thread_inspect", fmt.Sprintf(`{"id":%d,"limit":3}`, w.T1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "line 6") || strings.Contains(out, "line 3") || !strings.Contains(out, "older: thread_inspect") {
		t.Fatalf("the latest page:\n%s", out)
	}
	m := regexp.MustCompile(`before: (\d+)`).FindStringSubmatch(out)
	out, _ = w.call(t, defaultConfig(), false, "thread_inspect", fmt.Sprintf(`{"id":%d,"limit":3,"before":%s}`, w.T1, m[1]))
	if !strings.Contains(out, "line 3") || strings.Contains(out, "line 6") {
		t.Fatalf("the page before:\n%s", out)
	}
}

// The grant, end to end: a call reading "all" parks the step for the owner;
// a participant can't allow it; once runs it and keeps nothing; an hour
// keeps it until it expires or is revoked; deny answers the call.
func TestThreadGrantFlow(t *testing.T) {
	ag, mux := accessFixture(t)
	w := buildThreadsWorld(t, ag)
	if _, err := ag.db.q.Exec(`INSERT INTO run_members (run_id, user, role, created) VALUES (?, 'carol', 'participant', 1)`, w.A); err != nil {
		t.Fatal(err)
	}
	ag.acl.flush(w.A)
	f := fakeOf(ag)
	f.on(lastIs("tool", ""), say("done"))
	f.on(lastUser("everything"), callTools(tc("c1", "threads_list", `{"scope":"all"}`)))
	f.on(lastUser("mine"), callTools(tc("c1", "threads_list", `{}`)))

	parked := func() pendingState {
		t.Helper()
		waitStatus(t, ag.db, w.A, statusWaiting)
		return parsePending(mustRun(t, ag, w.A).Pending)
	}
	lastTool := func() string {
		msgs, _ := ag.db.messages(w.A, false)
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "tool" {
				return msgs[i].Content
			}
		}
		return ""
	}
	approve := func(c caller, body map[string]any) int {
		return callAs(t, mux, c, "POST", fmt.Sprintf("/runs/%d/approve", w.A), body).Code
	}

	// mine: no question asked
	send(t, ag, w.A, "show mine")
	waitQuiet(t, ag)
	if st := statusOf(ag.db, w.A); st == statusWaiting || !strings.Contains(lastTool(), "digest monday") {
		t.Fatalf("mine parked (%s) or missed: %s", st, lastTool())
	}

	send(t, ag, w.A, "show everything")
	if p := parked(); p.Kind != "approval" || p.Grant != capThreads {
		t.Fatalf("parked on %+v", p)
	}
	if got := approve(asCarol, map[string]any{"approve": true, "grant": "hour"}); got != 403 {
		t.Fatalf("a participant allows the owner's grant: %d", got)
	}
	if got := approve(asAlice, map[string]any{"approve": true, "grant": "forever"}); got != 400 {
		t.Fatalf("an unknown grant: %d", got)
	}
	var needs struct{ Items []map[string]any }
	_ = json.Unmarshal(callAs(t, mux, asCarol, "GET", "/needs", nil).Body.Bytes(), &needs)
	if len(needs.Items) != 0 {
		t.Fatalf("the grant is under carol's Needs you: %v", needs.Items)
	}
	_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", "/needs", nil).Body.Bytes(), &needs)
	if len(needs.Items) != 1 || needs.Items[0]["reason"] != "approval" {
		t.Fatalf("alice's Needs you: %v", needs.Items)
	}
	if got := approve(asAlice, map[string]any{"approve": true}); got != 200 { // once
		t.Fatalf("alice allows once: %d", got)
	}
	waitQuiet(t, ag)
	if !strings.Contains(lastTool(), "holiday plans") || ag.db.liveGrant(w.A, capThreads) {
		t.Fatalf("once: %q, kept=%v", lastTool(), ag.db.liveGrant(w.A, capThreads))
	}

	send(t, ag, w.A, "show everything again")
	parked()
	if got := approve(asAlice, map[string]any{"approve": true, "grant": "hour"}); got != 200 {
		t.Fatalf("alice allows for an hour: %d", got)
	}
	waitQuiet(t, ag)
	if !ag.db.liveGrant(w.A, capThreads) || !strings.Contains(lastTool(), "holiday plans") {
		t.Fatal("the hour grant")
	}
	var view struct {
		Run struct{ Grants []grantView }
	}
	_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/view", w.A), nil).Body.Bytes(), &view)
	if len(view.Run.Grants) != 1 || view.Run.Grants[0].Cap != capThreads || view.Run.Grants[0].GrantedBy != "alice" {
		t.Fatalf("the view's grants: %+v", view.Run.Grants)
	}
	send(t, ag, w.A, "everything once more")
	waitQuiet(t, ag)
	if statusOf(ag.db, w.A) == statusWaiting {
		t.Fatal("parked again within the hour")
	}

	// expired: asked again
	if _, err := ag.db.q.Exec(`UPDATE run_grants SET expires_ms=1 WHERE root_id=?`, w.A); err != nil {
		t.Fatal(err)
	}
	send(t, ag, w.A, "everything after it expired")
	parked()
	if got := approve(asCarol, map[string]any{"approve": false}); got != 200 { // anyone who steers may deny
		t.Fatalf("carol denies: %d", got)
	}
	waitQuiet(t, ag)
	if !strings.HasPrefix(lastTool(), "(denied") {
		t.Fatalf("denied: %q", lastTool())
	}

	// revoke: the owner only
	send(t, ag, w.A, "everything, for an hour")
	parked()
	approve(asAlice, map[string]any{"approve": true, "grant": "hour"})
	waitQuiet(t, ag)
	if got := callAs(t, mux, asCarol, "DELETE", fmt.Sprintf("/runs/%d/grants/threads", w.A), nil).Code; got == 200 {
		t.Fatal("carol revokes alice's grant")
	}
	if got := callAs(t, mux, asAlice, "DELETE", fmt.Sprintf("/runs/%d/grants/nope", w.A), nil).Code; got != 404 {
		t.Fatalf("an unknown grant: %d", got)
	}
	if got := callAs(t, mux, asAlice, "DELETE", fmt.Sprintf("/runs/%d/grants/threads", w.A), nil).Code; got != 200 || ag.db.liveGrant(w.A, capThreads) {
		t.Fatalf("alice revokes: %d", got)
	}
}
