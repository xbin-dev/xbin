package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ovStats is the fake provider's GET /scm/repo/stats (D216), served in
// front of the fake (scm_fake_test.go) by a server of its own: stats per
// repo, or every read answering 501 (unsupported).
type ovStats struct {
	mu          sync.Mutex
	stats       map[string]*scmRepoStats
	unsupported bool
	calls       int
}

func (s *ovStats) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// withStats puts f behind a server that also answers /scm/repo/stats, binds
// it as f's provider, and adds `stats` to f's caps when caps is set.
func withStats(t *testing.T, f *fakeSCM, caps bool) *ovStats {
	t.Helper()
	s := &ovStats{stats: map[string]*scmRepoStats{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/scm/repo/stats" {
			f.serve(w, r)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls++
		st := s.stats[r.URL.Query().Get("repo")]
		switch {
		case s.unsupported:
			fakeJSON(w, 501, &scmError{Status: 501, Refusal: scmRefUnsupported, Message: "no stats here"})
		case st == nil:
			fakeJSON(w, 404, &scmError{Status: 404, Refusal: scmRefNotFound, Message: "no such repo"})
		default:
			fakeJSON(w, 200, st)
		}
	}))
	t.Cleanup(srv.Close)
	f.mu.Lock()
	if caps {
		f.Caps = append(f.Caps, scmCapStats)
	}
	f.mu.Unlock()
	raw, _ := json.Marshal([]map[string]string{{"provider": f.Provider, "url": srv.URL + "/", "service": "scm"}})
	t.Setenv("XBIN_IFACE_SCM", string(raw))
	forgetSCMHellos()
	forgetOverviewRepos()
	t.Cleanup(forgetOverviewRepos)
	return s
}

func intp(n int) *int { return &n }

// overviewOf reads the overview as c (the status code, and the answer on 200).
func overviewOf(t *testing.T, mux *http.ServeMux, c caller, pid int64, rng string) (int, Overview) {
	t.Helper()
	path := fmt.Sprintf("/projects/%d/overview", pid)
	if rng != "" {
		path += "?range=" + rng
	}
	w := callAs(t, mux, c, "GET", path, nil)
	var ov Overview
	if w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &ov); err != nil {
			t.Fatalf("overview: %v %s", err, w.Body)
		}
	}
	return w.Code, ov
}

// ovRun makes a run under parent (a subagent, or a coding agent with
// provider) in status, updated at `updated` (unix s; 0 now).
func ovRun(t *testing.T, d *DB, parent int64, status, provider string, updated int64) int64 {
	t.Helper()
	cfg, eng := `{}`, ""
	if provider != "" {
		cfg, eng = fmt.Sprintf(`{"engine":"harness","harness":{"provider":%q}}`, provider), engineHarness
	}
	id, err := d.createRunStamped("sub", cfg, parent, status, runStamp{Engine: eng})
	if err != nil {
		t.Fatal(err)
	}
	if updated != 0 {
		if _, err := d.q.Exec(`UPDATE runs SET updated=? WHERE id=?`, updated, id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func ovExec(t *testing.T, d *DB, q string, args ...any) {
	t.Helper()
	if _, err := d.q.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// ovSeed is a project of alice's with nine tasks across the board, their
// subagents and coding agents, her coordinator and its subagent, an hour's
// usage, an event and the repo's stats.
type ovSeed struct {
	fx    *coordFix
	stats *ovStats
	coord *Run
	tasks map[int64]int64 // n → its run
	sub2  int64           // a running subagent of task 2
	hc1   int64           // task 1's coding agent
	at    time.Time       // the hour its usage is written in (usage_hourly's clock, fixed)
}

func newOvSeed(t *testing.T) *ovSeed {
	t.Helper()
	fx := newCoordFix(t)
	d := fx.ag.db
	s := &ovSeed{fx: fx, tasks: map[int64]int64{}, at: time.Now()}
	fixUsageClock(t, s.at)
	s.stats = withStats(t, fx.scm, true)
	s.stats.stats["acme/web"] = &scmRepoStats{Repo: "acme/web", URL: "https://github.com/acme/web", DefaultBranch: "main",
		OpenPulls: intp(3), OpenIssues: intp(14), MergedPulls: &scmMergedPulls{Days: 7, Count: 5},
		DefaultBranchChecks: &scmBranchChecks{SHA: "abc", State: "success", URL: "https://github.com/acme/web/actions/runs/9"}}
	for _, u := range []string{"carol", "dave"} {
		role := map[string]string{"carol": roleParticipant, "dave": roleViewer}[u]
		ovExec(t, d, `INSERT INTO project_members (project_id, user, role) VALUES (?, ?, ?)`, fx.p.ID, u, role)
	}
	ovExec(t, d, `UPDATE projects SET policy=? WHERE id=?`, `{"autoLabel":"agent","maxTasks":3}`, fx.p.ID)
	projACL.flush(fx.p.ID)
	s.coord = fx.coordinator(t, asAlice, fx.p.ID)
	now := time.Now().Unix()
	for n := int64(1); n <= 9; n++ {
		owner := "alice"
		if n == 9 {
			owner = "carol"
		}
		_, run := fx.addTask(t, fx.p, n, owner)
		s.tasks[n] = run.ID
	}
	run := func(n int64) int64 { return s.tasks[n] }
	// 1: working, driving Claude Code, one subagent running
	ovExec(t, d, `UPDATE runs SET status='running', turn_started=? WHERE id=?`, now-30, run(1))
	s.hc1 = ovRun(t, d, run(1), statusRunning, "claude", 0)
	ovRun(t, d, run(1), statusRunning, "", 0)
	// 2: working; subagents: two running, one waiting, one finished, one failed, one finished two days ago
	ovExec(t, d, `UPDATE runs SET status='running', turn_started=? WHERE id=?`, now-60, run(2))
	s.sub2 = ovRun(t, d, run(2), statusRunning, "", 0)
	ovRun(t, d, run(2), statusAwait, "", 0)
	ovRun(t, d, run(2), statusWaiting, "", 0)
	ovRun(t, d, run(2), statusDone, "", 0)
	ovRun(t, d, run(2), statusError, "", 0)
	ovRun(t, d, run(2), statusIdle, "", now-2*86400)
	// 3: waits for a person; 7: its turn failed
	ovExec(t, d, `UPDATE runs SET status='waiting_input' WHERE id=?`, run(3))
	ovExec(t, d, `UPDATE runs SET status='error' WHERE id=?`, run(7))
	// 4 (and carol's 9): a PR with CI pending; 5: a PR awaiting review; 6: merged; 8: queued
	pr := func(n, num int64, checks string) string {
		return fmt.Sprintf(`[{"repo":"acme/web","number":%d,"url":"https://github.com/acme/web/pull/%d","state":"open","headSha":"s%d","checks":%q}]`, num, num, n, checks)
	}
	ovExec(t, d, `UPDATE project_tasks SET phase='pr', prs=? WHERE project_id=? AND n=4`, pr(4, 41, "pending"), fx.p.ID)
	ovExec(t, d, `UPDATE project_tasks SET phase='pr', prs=? WHERE project_id=? AND n=5`, pr(5, 42, "success"), fx.p.ID)
	ovExec(t, d, `UPDATE project_tasks SET phase='pr', prs=? WHERE project_id=? AND n=9`, pr(9, 49, "pending"), fx.p.ID)
	ovExec(t, d, `UPDATE project_tasks SET phase='merged' WHERE project_id=? AND n=6`, fx.p.ID)
	ovExec(t, d, `UPDATE project_tasks SET ws='queued' WHERE project_id=? AND n=8`, fx.p.ID)
	// the coordinator made 1 and 2; it has a subagent running, answered once, and alice wrote to it
	ovExec(t, d, `UPDATE project_tasks SET from_run=? WHERE project_id=? AND n IN (1, 2)`, s.coord.ID, fx.p.ID)
	ovRun(t, d, s.coord.ID, statusRunning, "", 0)
	for _, m := range []*Message{
		{RunID: s.coord.ID, Role: "user", Content: "Triage the open bugs into tasks", Meta: json.RawMessage(`{"sender":"alice"}`)},
		{RunID: s.coord.ID, Role: "user", Content: "[project updates — …]", Meta: json.RawMessage(`{"origin":"project","originId":1}`)},
		{RunID: s.coord.ID, Role: "assistant", Content: "", ToolCalls: `[{"id":"c1"}]`, Meta: json.RawMessage(`{"model":"apps/llm|big-model"}`)},
		{RunID: s.coord.ID, Role: "assistant", Content: "Made two tasks.", Meta: json.RawMessage(`{"model":"apps/llm|big-model"}`)},
	} {
		if _, err := d.addMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	// an hour's usage: task 1 110, task 2's subagent 55, the coordinator 22; task 1's coding agent $1.25
	d.addRunCost(run(1), 100, 10)
	d.addRunCost(s.sub2, 50, 5)
	d.addRunCost(s.coord.ID, 20, 2)
	d.addHarnessCost(s.hc1, "", `{"used":1,"size":2,"cost":{"amount":0.5,"currency":"USD"}}`)
	d.addHarnessCost(s.hc1, `{"cost":{"amount":0.5,"currency":"USD"}}`, `{"cost":{"amount":1.25}}`)
	addProjectEventTx(t, d, fx.p.ID, 4, pevCIFailed, "CI failed on xbin/x/4@abc", false)
	return s
}

// The overview's shape and counts over a seeded project.
func TestProjectOverview(t *testing.T) {
	s := newOvSeed(t)
	fx := s.fx
	code, ov := overviewOf(t, fx.mux, asAlice, fx.p.ID, "")
	if code != 200 {
		t.Fatalf("GET overview: %d", code)
	}
	if ov.Range != "day" || ov.At == 0 {
		t.Errorf("range/at: %q %d", ov.Range, ov.At)
	}
	// the coordinator
	c := ov.Coordinator
	if c.Run == nil || c.Run.ID != s.coord.ID || c.State != "idle" {
		t.Errorf("coordinator: %+v run %+v", c, c.Run)
	}
	if c.Goal == nil || c.Goal.By != "you" || c.Goal.Text != "Triage the open bugs into tasks" {
		t.Errorf("goal before project_goal: %+v", c.Goal)
	}
	if c.TurnsToday != 1 || c.TasksMade7d != 2 || c.Model != "big-model" || c.TokensToday != 22 {
		t.Errorf("coordinator numbers: turns %d made %d model %q tokens %d", c.TurnsToday, c.TasksMade7d, c.Model, c.TokensToday)
	}
	var wakes []string
	for _, w := range c.WakesOn {
		wakes = append(wakes, fmt.Sprintf("%s:%d:%d:%s", w.Kind, w.N, w.PR, w.Label))
	}
	if got, want := strings.Join(wakes, ","), "ci:4:0:CI on #4,review:5:42:review on #42,issues:0:0:new issues,you:0:0:your message"; got != want {
		t.Errorf("wakesOn:\n got %s\nwant %s", got, want)
	}
	if e := c.LastEvent; e == nil || e.Kind != pevCIFailed || e.N != 4 || e.Label != "CI failed on #4" || e.AtMs == 0 {
		t.Errorf("lastEvent: %+v", e)
	}
	// the workers: 1, 2 working; 3, 7 need you; 4, 5, 9 idle (a PR); 8 queued; 6 done
	w := ov.Workers
	if w.Total != 9 || w.Working != 2 || w.NeedsYou != 2 || w.Idle != 3 || w.Queued != 1 || w.Done != 1 {
		t.Errorf("workers: %+v", w)
	}
	if w.Slots.Max != 3 {
		t.Errorf("slots: %+v", w.Slots)
	}
	if len(w.ByEngine) != 2 || w.ByEngine[0] != (OvEngine{Engine: "agent", Title: "agent", Count: 8}) ||
		w.ByEngine[1] != (OvEngine{Engine: "claude", Title: "Claude Code", Count: 1}) {
		t.Errorf("byEngine: %+v", w.ByEngine)
	}
	// subagents: task 1's one, task 2's two and the coordinator's run; one waits; one finished and one failed today
	sa := ov.Subagents
	if sa.Running != 4 || sa.Waiting != 1 || sa.Finished24h != 1 || sa.Failed24h != 1 {
		t.Errorf("subagents: %+v", sa)
	}
	if len(sa.By) != 3 || sa.By[0] != (OvSubBy{N: 2, Count: 2}) || sa.By[1] != (OvSubBy{N: 1, Count: 1}) || sa.By[2] != (OvSubBy{Coordinator: true, Count: 1}) {
		t.Errorf("subagents by: %+v", sa.By)
	}
	// tokens: this hour, by class; the coding agent's cost
	tk := ov.Tokens
	if len(tk.Buckets) != 24 {
		t.Fatalf("day buckets: %d", len(tk.Buckets))
	}
	if tk.Buckets[23].T != hourOf(time.UnixMilli(ov.At))*1000 {
		t.Errorf("the last bucket starts at %d, not the read's hour", tk.Buckets[23].T)
	}
	var spent *OvBucket // the seed's hour: the last bucket, or the one before when the hour turned since
	for i := range tk.Buckets {
		if tk.Buckets[i].T == hourOf(s.at)*1000 {
			spent = &tk.Buckets[i]
		}
	}
	if spent == nil || spent.Workers != 110 || spent.Subagents != 55 || spent.Coordinator != 22 {
		t.Errorf("the seed hour's bucket: %+v", spent)
	}
	if tk.Total != (OvClasses{Workers: 110, Coordinator: 22, Subagents: 55}) {
		t.Errorf("total: %+v", tk.Total)
	}
	if tk.Cost == nil || tk.Cost.Amount != 1.25 || tk.Cost.Currency != "USD" {
		t.Errorf("cost: %+v", tk.Cost)
	}
	// the cards
	if got := ov.Tasks["1"]; got.Engine != "claude" || got.Title != "Claude Code" || got.State != "working" || got.Subagents != 1 ||
		got.Tokens != 110 || got.TurnStartedMs == 0 {
		t.Errorf("task 1's card: %+v", got)
	}
	for n, want := range map[string]string{"2": "working", "3": "waiting", "4": "idle", "5": "idle", "6": "done", "7": "failed", "8": "queued", "9": "idle"} {
		if got := ov.Tasks[n]; got.State != want || got.Engine != "agent" {
			t.Errorf("task %s's card: %+v (want %s)", n, got, want)
		}
	}
	if got := ov.Tasks["2"]; got.Subagents != 2 || got.Tokens != 55 {
		t.Errorf("task 2's card: %+v", got)
	}
	// scm: the provider, its stats, the board's pull requests
	sc := ov.SCM
	if sc.Provider == nil || sc.Provider.Name != "scm-github" || sc.Provider.Title != "GitHub" || sc.Provider.Kind != "github" || !sc.Stats {
		t.Errorf("scm provider: %+v stats %v", sc.Provider, sc.Stats)
	}
	if len(sc.Repos) != 1 {
		t.Fatalf("repos: %+v", sc.Repos)
	}
	r := sc.Repos[0]
	if r.Repo != "acme/web" || r.URL != "https://github.com/acme/web" || r.DefaultBranch != "main" || *r.OpenPulls != 3 || *r.OpenIssues != 14 ||
		*r.Merged7d != 5 || r.Checks == nil || r.Checks.State != "success" || r.Err != "" {
		t.Errorf("repo row: %+v checks %+v", r, r.Checks)
	}
	if sc.Pulls != (OvPulls{Open: 3, Failing: 0, Review: 1}) {
		t.Errorf("pulls: %+v", sc.Pulls)
	}
	if ov.Sandbox != nil {
		t.Errorf("a project without a sandbox: %+v", ov.Sandbox)
	}
	// the week: 7 daily buckets, the same spend in the last
	code, wk := overviewOf(t, fx.mux, asAlice, fx.p.ID, "week")
	if code != 200 || wk.Range != "week" || len(wk.Tokens.Buckets) != 7 || wk.Tokens.Buckets[6].Workers != 110 || wk.Tokens.Total != tk.Total {
		t.Errorf("week: %d %q %+v", code, wk.Range, wk.Tokens)
	}
	if code, _ := overviewOf(t, fx.mux, asAlice, fx.p.ID, "month"); code != 400 {
		t.Errorf("range=month: %d", code)
	}
}

// Who may read it: a viewer reads the board but has no coordinator (and
// carol sees her own); someone outside it, someone viewing as another
// person and a component get 404.
func TestProjectOverviewAccess(t *testing.T) {
	s := newOvSeed(t)
	fx := s.fx
	code, ov := overviewOf(t, fx.mux, asDave, fx.p.ID, "")
	if code != 200 {
		t.Fatalf("a viewer: %d", code)
	}
	if ov.Coordinator.State != "none" || ov.Coordinator.Run != nil || ov.Coordinator.Goal != nil || ov.Coordinator.LastEvent != nil {
		t.Errorf("a viewer's coordinator: %+v", ov.Coordinator)
	}
	if len(ov.Coordinator.WakesOn) != 1 || ov.Coordinator.WakesOn[0].Kind != "you" {
		t.Errorf("a viewer's wakesOn: %+v", ov.Coordinator.WakesOn)
	}
	if ov.Workers.Total != 9 || ov.Tokens.Total.Coordinator != 0 || ov.Tokens.Total.Workers != 110 || ov.Subagents.Running != 3 {
		t.Errorf("a viewer sees the tasks, not alice's coordinator: workers %+v tokens %+v subagents %+v", ov.Workers, ov.Tokens.Total, ov.Subagents)
	}
	// carol: her own task's CI wakes her coordinator-to-be
	_, ov = overviewOf(t, fx.mux, asCarol, fx.p.ID, "")
	if w := ov.Coordinator.WakesOn; len(w) != 2 || w[0].Kind != "ci" || w[0].N != 9 || w[1].Kind != "you" {
		t.Errorf("carol's wakesOn: %+v", w)
	}
	for who, c := range map[string]caller{"bob": asBob, "viewAs": asViewAs, "element": asElement} {
		if code, _ := overviewOf(t, fx.mux, c, fx.p.ID, ""); code != 404 {
			t.Errorf("%s: %d (want 404)", who, code)
		}
	}
	// a team project's definition has no coordinator
	ovExec(t, fx.ag.db, `UPDATE projects SET kind='team' WHERE id=?`, fx.p.ID)
	if _, ov := overviewOf(t, fx.mux, asAlice, fx.p.ID, ""); ov.Coordinator.State != "none" || ov.Coordinator.Run != nil || len(ov.Coordinator.WakesOn) != 0 {
		t.Errorf("a team definition's coordinator: %+v", ov.Coordinator)
	}
}

// The chart's buckets: day — 24 hours, the last the current one; week — 7
// days of 24 hours ending with it; each bucket's t its start, oldest first.
func TestOverviewBuckets(t *testing.T) {
	fx := newCoordFix(t)
	d := fx.ag.db
	_, run := fx.addTask(t, fx.p, 1, "alice")
	now := time.Now()
	h := hourOf(now)
	end := h + 3600
	rows := map[int64]int64{ // hour → tokens
		h:                    1,    // day 23, week 6
		h - 23*3600:          10,   // day 0, week 6
		h - 24*3600:          100,  // week 5 only
		end - 7*86400:        1000, // week 0
		end - 7*86400 - 3600: 5000, // neither
	}
	for hr, n := range rows {
		ovExec(t, d, `INSERT INTO usage_hourly (run_id, hour, calls, prompt) VALUES (?, ?, 1, ?)`, run.ID, hr, n)
	}
	b := loadBoard(d, fx.p, who{kind: whoUser, user: "alice", level: "read"}, now)
	day := b.tokens("day")
	if len(day.Buckets) != 24 || day.Buckets[0].T != (h-23*3600)*1000 || day.Buckets[23].T != h*1000 {
		t.Fatalf("day buckets: %d, first %d last %d", len(day.Buckets), day.Buckets[0].T, day.Buckets[23].T)
	}
	if day.Buckets[23].Workers != 1 || day.Buckets[0].Workers != 10 || day.Total.Workers != 11 {
		t.Errorf("day: last %d first %d total %d", day.Buckets[23].Workers, day.Buckets[0].Workers, day.Total.Workers)
	}
	week := loadBoard(d, fx.p, who{kind: whoUser, user: "alice", level: "read"}, now).tokens("week")
	if len(week.Buckets) != 7 || week.Buckets[0].T != (end-7*86400)*1000 || week.Buckets[6].T != (end-86400)*1000 {
		t.Fatalf("week buckets: %d, first %d", len(week.Buckets), week.Buckets[0].T)
	}
	if week.Buckets[6].Workers != 11 || week.Buckets[5].Workers != 100 || week.Buckets[0].Workers != 1000 || week.Total.Workers != 1111 {
		t.Errorf("week: %+v total %+v", week.Buckets, week.Total)
	}
	if day.Cost != nil || week.Cost != nil {
		t.Errorf("a cost without any: %+v %+v", day.Cost, week.Cost)
	}
}
