package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp/acptest"
)

// Coding agents in a partitioned agent (harness_partition.go; 90 §I15):
// only in a person's own conversations.

const alicePID = "u-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // alicesFrame's partition id

// partitionManager is a manager's handler hearing every call as xbind stamps a
// person's partition's (the agent's calls in a test go straight to it) —
// while global is set, as the global instance's (no partition headers).
func partitionManager(user, pid string, global *atomic.Bool) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if global == nil || !global.Load() {
				r.Header.Set("X-XBin-Partition", "user:"+user)
				r.Header.Set("X-XBin-Partition-Id", pid)
			}
			h.ServeHTTP(w, r)
		})
	}
}

// atGlobal makes a sandbox as the global instance would (homed at the
// agent's non-personal identity), owned by user and shared with the agent's
// people — a person's partition sees it, marked shared.
func atGlobal(t *testing.T, global *atomic.Bool, provider, user, name string) *sbxSandbox {
	t.Helper()
	global.Store(true)
	defer global.Store(false)
	b := mkSandbox(t, provider, user, sbxCreate{Name: name})
	c, err := sbxDial(provider, user)
	if err != nil {
		t.Fatal(err)
	}
	if b, err = c.Patch(context.Background(), b.ID, sbxPatch{Shares: json.RawMessage(`[{"consumer":"apps/agent","users":"*"}]`)}); err != nil {
		t.Fatal(err)
	}
	invalidateSandboxCatalog()
	return b
}

// harnessPartition is alice's partition (conf known, in kv) with the fake
// coding agent in a sandbox homed there: a manager whose hello offers
// `partitions` (and stdio), hearing the agent as her partition.
func harnessPartition(t *testing.T) (*Agent, http.Handler, *sbxSandbox, *sbxTestManager, *memKV) {
	t.Helper()
	ag, h, box, m, kv, _ := harnessPartitionG(t)
	return ag, h, box, m, kv
}

// harnessPartitionG is harnessPartition with the switch that makes the
// manager hear the global instance instead (atGlobal), and the fake coding
// agent's flags.
func harnessPartitionG(t *testing.T, flags ...string) (*Agent, http.Handler, *sbxSandbox, *sbxTestManager, *memKV, *atomic.Bool) {
	t.Helper()
	setMode(t, modeUser, "alice")
	kv := newMemKV()
	confIn = newConfReader(kv, nil)
	putConf(kv, "", `{"config":`+strconvQuote(mustJSON(defaultConfig()))+`}`)
	confIn.refresh()
	ag, h := homeAgent(t)
	confIn.parked = ag.db.brakeParked
	confIn.onHaltOff = func() { ag.eng.recover() }
	if err := ag.db.addHandoffSchema(); err != nil { // the usage tables (startMode makes them)
		t.Fatal(err)
	}
	pidMu.Lock()
	pidSeen = alicePID
	pidMu.Unlock()
	global := &atomic.Bool{}
	m := bindSbxWith(t, partitionManager("alice", alicePID, global), "apps/cs")["apps/cs"]
	argv := acptest.Command(flags...)
	m.Harnesses = []fsbHarness{{ID: "fake", Title: "Fake agent (tests)", Argv: argv, Login: argv[0] + " acptest login"}}
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Egress: "internet"})
	if !homedHere(box) {
		t.Fatalf("the fixture's sandbox isn't homed in alice's partition: %+v", box.Owner)
	}
	forgetHarnessProbes()
	t.Cleanup(func() { settleHarnesses(ag.eng) }) // before the manager goes (cleanups run last first)
	return ag, h, box, m, kv, global
}

// askIn starts a coding agent's conversation in alice's partition.
func askIn(t *testing.T, h http.Handler, box *sbxSandbox, text string) *Run {
	t.Helper()
	body := mustJSON(map[string]any{"text": text, "class": "coding", "harness": map[string]any{"provider": "fake"},
		"sandbox": map[string]any{"ref": sandboxRef("apps/cs", box.ID)}})
	var run Run
	serveJSON(t, h, as("POST", "/ask", body, alicesFrame("read")), 200, &run)
	if run.Engine != engineHarness || run.ID < partitionIDBase {
		t.Fatalf("the run: %+v", run)
	}
	return &run
}

// holdCounter makes e's hold observable: the number of holds open.
func holdCounter(e *Engine) *atomic.Int32 {
	var open atomic.Int32
	e.hold.mu.Lock()
	e.hold.open = func(ctx context.Context) error {
		open.Add(1)
		<-ctx.Done()
		open.Add(-1)
		return nil
	}
	e.hold.mu.Unlock()
	return &open
}

// TestHarnessInPartition: a person's own coding agent works in their
// partition as unpartitioned — and resting, its adapter doesn't keep the
// partition up (A-S4): the partition leaves a `wake` at its idle reclaim's
// minute, never the @every 1m `resume` (A-M4), and the session counts in
// the person's usage totals (A-S6).
func TestHarnessInPartition(t *testing.T) {
	ag, h, box, _, _ := harnessPartition(t)
	open := holdCounter(ag.eng)
	run := askIn(t, h, box, "hello there")
	hwait(t, "the turn", turnOver(ag, run.ID))
	if !strings.Contains(fullText(ag.db, run.ID), "echo: hello there") {
		t.Fatalf("the turn: %s", transcript(ag.db, run.ID))
	}
	hs, _ := ag.db.harnessSession(run.ID)
	if hs == nil || hs.State != hsLive || ag.eng.harnessOf(run.ID) == nil {
		t.Fatalf("the adapter after its turn: %+v", hs)
	}
	hwait(t, "the hold let go while the adapter rests", func() bool { return open.Load() == 0 })

	// the wake-up a stopping partition leaves: the reclaim's minute
	now := time.Now()
	hs, _ = ag.db.harnessSession(run.ID) // as last active (a late event may have come)
	at := ag.db.userWake(now)
	want := time.UnixMilli(hs.LastActiveMs).Add(15 * time.Minute).Unix()
	if at.runnable || at.wake != want {
		t.Fatalf("an idle adapter's wake-up: %+v, want wake at %d", at, want)
	}
	if !ag.db.hasWork() {
		t.Fatal("control: unpartitioned, an adapter up is work (the resume job)")
	}

	// the usage totals count its session, no content
	var n int
	_ = ag.db.q.QueryRow(`SELECT harness_sessions FROM usage_daily WHERE day=?`, now.UTC().Format("2006-01-02")).Scan(&n)
	if n != 1 {
		t.Fatalf("the day's harness sessions: %d", n)
	}
	days := ag.db.usageDays("", now.UTC().AddDate(0, 0, 1).Truncate(24*time.Hour))
	if len(days) != 1 || days[0].HarnessSessions != 1 || days[0].Runs != 1 {
		t.Fatalf("the days a partition mails: %+v", days)
	}

	// a message: at work again, it holds; resting again, it lets go
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", run.ID), `{"text":"slow please"}`, alicesFrame("read")), 200, nil)
	hwait(t, "the hold while it works", func() bool { return open.Load() == 1 })
	hwait(t, "the second turn", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "tick 9")
	})
	hwait(t, "the hold let go again", func() bool { return open.Load() == 0 })
	_ = ag.db.q.QueryRow(`SELECT harness_sessions FROM usage_daily`).Scan(&n)
	if n != 1 {
		t.Fatalf("one adapter, two turns: %d sessions", n)
	}
}

// TestHarnessHoldUnpartitioned: unpartitioned, a coding agent's adapter
// keeps the hold while it is up, as ever (its idle reclaim is this
// process's).
func TestHarnessHoldUnpartitioned(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	open := holdCounter(ag.eng)
	run := askHarness(t, mux, box, "echo one")
	hwait(t, "the turn", turnOver(ag, run.ID))
	hwait(t, "the hold", func() bool { return open.Load() == 1 })
	time.Sleep(200 * time.Millisecond)
	if open.Load() != 1 {
		t.Fatal("unpartitioned, a resting adapter let the hold go")
	}
}

// TestHarnessReclaimAfterWake: a partition that stopped with an adapter
// resting comes back at the reclaim's minute (the wake above) — its
// takeover counts the reclaim from the adapter's last activity, not a whole
// idle time from now, so it stops the adapter at once (A-M4).
func TestHarnessReclaimAfterWake(t *testing.T) {
	harnessIdleTest = 30 * time.Minute
	t.Cleanup(func() { harnessIdleTest = 0 }) // after the engine settles (cleanups run last first)
	ag, h, box, _, _ := harnessPartition(t)
	run := askIn(t, h, box, "echo one")
	hwait(t, "the turn", turnOver(ag, run.ID))
	before, _ := ag.db.harnessSession(run.ID)
	ag.eng.Shutdown(2 * time.Second) // stopped: the adapter runs on in its sandbox
	if _, err := ag.db.q.Exec(`UPDATE harness_sessions SET last_active_ms=? WHERE run_id=?`,
		time.Now().Add(-31*time.Minute).UnixMilli(), run.ID); err != nil {
		t.Fatal(err)
	}
	b := successor(t, ag)
	hwait(t, "the reclaim at the takeover", func() bool {
		hs, _ := ag.db.harnessSession(run.ID)
		return hs.State == hsStopped && b.harnessOf(run.ID) == nil
	})
	if after, _ := ag.db.harnessSession(run.ID); after.Gen != before.Gen {
		t.Fatalf("after the reclaim: %+v", after)
	}
	if at := ag.db.userWake(time.Now()); at.runnable || at.wake != 0 {
		t.Fatalf("nothing rests any more, yet: %+v", at)
	}
}

// TestHarnessUserWake: what a person's partition leaves for its coding
// agents (A-M4) — a prompt on its way is work that moves without the person
// (resume), an adapter up with no turn one wake at its last activity plus
// the reclaim's time (never within the minute), and nothing for one waiting
// on a person, one signing in, or with the reclaim off.
func TestHarnessUserWake(t *testing.T) {
	setMode(t, modeUser, "alice")
	now := time.Unix(1_900_000_000, 0)
	d := newTestDB(t)
	id, _ := d.createRun("x", "", 0)
	_, _ = d.q.Exec(`UPDATE runs SET engine='harness', status='idle' WHERE id=?`, id)
	put := func(state, prompt string, last time.Time) {
		if err := d.putHarnessSession(&harnessSession{RunID: id, RootID: id, State: state, ExecID: "e1", PromptState: prompt,
			LastActiveMs: last.UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	put(hsLive, "", now.Add(-time.Minute))
	if got := d.userWake(now); got.runnable || got.wake != now.Add(14*time.Minute).Unix() {
		t.Fatalf("an idle adapter: %+v", got)
	}
	put(hsLive, "", now.Add(-time.Hour)) // its reclaim is overdue: the next minute, still a wake
	if got := d.userWake(now); got.runnable || got.wake != now.Unix()+61 {
		t.Fatalf("an overdue reclaim: %+v", got)
	}
	_, _ = d.q.Exec(`UPDATE runs SET status='sleeping', wake_at=? WHERE id=?`, now.Unix()+3600, id)
	if got := d.userWake(now); got.wake != now.Unix()+61 {
		t.Fatalf("the earlier of a sleep and a reclaim: %+v", got)
	}
	_, _ = d.q.Exec(`UPDATE runs SET status='idle', wake_at=0 WHERE id=?`, id)
	put(hsLive, "sending", now)
	if got := d.userWake(now); !got.runnable {
		t.Fatalf("a prompt on its way: %+v", got)
	}
	put(hsLive, "sent", now)
	_, _ = d.q.Exec(`UPDATE runs SET status='waiting_input' WHERE id=?`, id)
	if got := d.userWake(now); got.runnable || got.wake != 0 {
		t.Fatalf("parked on a person mid-turn: %+v", got)
	}
	put(hsLogin, "", now)
	if got := d.userWake(now); got.runnable || got.wake != 0 {
		t.Fatalf("signing in: %+v", got)
	}
	_, _ = d.q.Exec(`UPDATE runs SET status='idle' WHERE id=?`, id)
	put(hsStopped, "", now)
	if got := d.userWake(now); got.runnable || got.wake != 0 {
		t.Fatalf("a stopped adapter: %+v", got)
	}
	// a prompt marked on its way to an adapter no successor attaches (gone)
	// isn't work: it would bring the partition back every minute for good
	// (a takeover ends its turn: TestHarnessSendingGone)
	for _, st := range []string{hsStopped, hsLost, hsFailed} {
		put(st, "sending", now)
		if got := d.userWake(now); got.runnable || got.wake != 0 {
			t.Fatalf("a prompt marked on its way to a %s adapter: %+v", st, got)
		}
	}
	off := 0
	cfg := defaultConfig()
	cfg.HarnessIdleMin = &off
	_ = d.putSetting("config", mustJSON(cfg))
	put(hsLive, "", now)
	if got := d.userWake(now); got.runnable || got.wake != 0 {
		t.Fatalf("the reclaim off: %+v", got)
	}
}

// TestHarnessBrakeInPartition: a halt the global instance mirrors into
// conf reaches a person's coding agent mid-turn at its next event — the
// run is cancelled with why, the adapter stopped (A-M5) — and a pass under
// a known halt cancels a live turn that says nothing.
func TestHarnessBrakeInPartition(t *testing.T) {
	shorten(t, &confTTL, 0)
	ag, h, box, _, kv := harnessPartition(t)
	cfg := `{"config":` + strconvQuote(mustJSON(defaultConfig())) + `}`
	run := askIn(t, h, box, "slow")
	hwait(t, "the turn under way", func() bool { return strings.Contains(draftText(ag.eng, run.ID), "tick") })
	putConf(kv, "1", cfg)
	waitStatus(t, ag.db, run.ID, statusCanceled)
	if r, _ := ag.db.getRun(run.ID); !strings.Contains(r.Result, haltReason) {
		t.Fatalf("the cancel doesn't say why: %q", r.Result)
	}
	hwait(t, "the adapter stopped", func() bool {
		hs, _ := ag.db.harnessSession(run.ID)
		return hs.State == hsStopped && ag.eng.harnessOf(run.ID) == nil
	})
	if strings.Contains(fullText(ag.db, run.ID), "tick 9") {
		t.Fatal("the turn ran to its end under the halt")
	}

	// lifted: a new conversation's turn under way, then the halt again —
	// a turn that says nothing is reached all the same (brakeLook), no
	// event and no poke needed
	putConf(kv, "", cfg)
	confIn.refresh()
	quiet(t, ag)
	stall := askIn(t, h, box, "stall")
	hwait(t, "stalling", func() bool { return draftText(ag.eng, stall.ID) == "stalling" })
	putConf(kv, "1", cfg)
	waitStatus(t, ag.db, stall.ID, statusCanceled)
	if r, _ := ag.db.getRun(stall.ID); !strings.Contains(r.Result, haltReason) {
		t.Fatalf("the pass's cancel doesn't say why: %q", r.Result)
	}
}

// TestHarnessBrakeFailsClosed: until conf has been read, a person's
// partition parks a coding agent's prompt — no adapter starts — and looks
// again; once conf answers, it goes (A-M5).
func TestHarnessBrakeFailsClosed(t *testing.T) {
	shorten(t, &brakeWatchMin, 20*time.Millisecond)
	ag, h, box, m, kv := harnessPartition(t)
	kv.mu.Lock()
	kv.m = map[string][]byte{} // conf gone (a wipe): unread again
	kv.mu.Unlock()
	confIn.state, confIn.at = confPending, time.Time{}
	confIn.refresh()
	if v := confIn.view(true); v.State != confPending {
		t.Fatalf("conf: %+v", v)
	}
	execs := func() int { return m.count("POST", "/sandboxes/"+box.ID+"/execs") }
	before := execs()
	run := askIn(t, h, box, "early bird")
	time.Sleep(200 * time.Millisecond)
	if hs, _ := ag.db.harnessSession(run.ID); hs != nil && hs.ExecID != "" || execs() != before {
		t.Fatalf("an adapter started before conf was read: %+v", hs)
	}
	putConf(kv, "", `{"config":`+strconvQuote(mustJSON(defaultConfig()))+`}`)
	hwait(t, "the parked prompt's turn", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: early bird")
	})
}

// TestHarnessNotAtGlobal: the global instance never starts or drives a
// coding agent (A-M1): POST /ask and POST /runs with one answer 409, the
// catalog offers none, a sign-in answers 409, subagent_spawn has no
// `harness`, and a coding agent's run there (none can be made — this one is
// planted) fails its turn without an adapter.
func TestHarnessNotAtGlobal(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	ref := sandboxRef("apps/cs", box.ID)
	planted := askHarness(t, mux, box, "echo planted")
	hwait(t, "the planted turn", turnOver(ag, planted.ID))
	s := ag.eng.harnessOf(planted.ID)
	ag.eng.endHarness(context.Background(), planted.ID)
	select { // its consumer done: nothing of it reads the mode the test changes
	case <-s.done:
	case <-time.After(15 * time.Second):
		t.Fatal("the planted adapter's consumer runs on")
	}
	quiet(t, ag)
	setMode(t, modeGlobal, "")
	t.Cleanup(func() { quiet(t, ag) })
	for _, path := range []string{"/ask", "/runs"} {
		w := callAs(t, mux, asSystem, "POST", path, map[string]any{"text": "fix it", "goal": "fix it", "class": "coding",
			"harness": map[string]any{"provider": "fake"}, "sandbox": map[string]any{"ref": ref}})
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "own conversations") {
			t.Errorf("%s with harness at the global instance: %d %s", path, w.Code, w.Body)
		}
	}
	var cat struct{ Harnesses []hcEntry }
	w := callAs(t, mux, asSystem, "GET", "/harnesses", nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &cat) != nil || len(cat.Harnesses) == 0 {
		t.Fatalf("the catalog: %d %s", w.Code, w.Body)
	}
	for _, e := range cat.Harnesses {
		if e.Available || e.Reason != "shared-space" {
			t.Errorf("%s at the global instance: available %v, %q", e.ID, e.Available, e.Reason)
		}
	}
	w = callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/harness/authenticate", planted.ID), map[string]any{"method": "fake-api-key", "apiKey": "k"})
	if w.Code != http.StatusConflict {
		t.Errorf("a sign-in at the global instance: %d %s", w.Code, w.Body)
	}
	req := httptest.NewRequest("GET", fmt.Sprintf("/runs/%d/harness/terminal?login=1", planted.ID), nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("X-XBin-From", "apps/agent")
	req.Header.Set("X-XBin-Role", "admin")
	req.Header.Set("X-XBin-User", "alice")
	req.Header.Set("X-XBin-User-Level", "read")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "own conversations") {
		t.Errorf("the sign-in terminal at the global instance: %d %s", rec.Code, rec.Body)
	}

	// the spawn tool
	cfg := defaultConfig()
	b := SandboxBinding{Ref: ref, Name: "box", Egress: "internet", Harnesses: []string{"fake"}}
	cfg.Class, cfg.Sandbox, cfg.Attached = "coding", &b, []SandboxBinding{b}
	if props := spawnSpec(runToolSpecs(cfg, &Run{ID: 7}, nil)); props == nil || props["harness"] != nil {
		t.Fatalf("subagent_spawn at the global instance: %v", props)
	}
	if _, err := ag.eng.harnessSpawnOf(context.Background(), &turnState{run: &Run{ID: 7}, cfg: cfg}, map[string]any{"harness": "fake", "task": "x"}, nil); err == nil ||
		!strings.Contains(err.Error(), "own conversations") {
		t.Fatalf("a spawn with harness at the global instance: %v", err)
	}

	// the planted run: its message fails the turn, no adapter starts
	gen := func() int { hs, _ := ag.db.harnessSession(planted.ID); return hs.Gen }
	g := gen()
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", planted.ID), map[string]any{"text": "echo again"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	hwait(t, "the refusal", func() bool {
		r, err := ag.db.getRun(planted.ID)
		return err == nil && r.Status == statusError && strings.Contains(r.Result, "own conversations")
	})
	if gen() != g || strings.Contains(fullText(ag.db, planted.ID), "echo: echo again") {
		t.Fatal("a coding agent started at the global instance")
	}
}

// TestHarnessSpawnInPartition: in a person's partition the spawn tool offers
// coding agents in their own conversations, never in a hosted one's runs,
// which refuse one if asked anyway (A-M2).
func TestHarnessSpawnInPartition(t *testing.T) {
	setMode(t, modeUser, "alice")
	cfg := defaultConfig()
	b := SandboxBinding{Ref: "apps/cs|sb-1", Name: "box", Egress: "internet", Harnesses: []string{"fake"}}
	cfg.Class, cfg.Sandbox, cfg.Attached = "coding", &b, []SandboxBinding{b}
	if props := spawnSpec(runToolSpecs(cfg, &Run{ID: partitionIDBase + 7}, nil)); props == nil || props["harness"] == nil {
		t.Fatalf("control: her own conversation's spawn: %v", props)
	}
	hosted := &Run{ID: teamIDBase + 7}
	if props := spawnSpec(runToolSpecs(cfg, hosted, nil)); props == nil || props["harness"] != nil || props["harness_mode"] != nil {
		t.Fatalf("a hosted run's spawn offers a coding agent: %v", props)
	}
	e := &Engine{db: newTestDB(t)}
	if _, err := e.harnessSpawnOf(context.Background(), &turnState{run: hosted, cfg: cfg}, map[string]any{"harness": "fake", "task": "x"}, nil); err == nil ||
		!strings.Contains(err.Error(), "non-secure") {
		t.Fatalf("a hosted run's spawn with harness: %v", err)
	}
	if why := harnessBarred(hosted); why == "" {
		t.Fatal("the host's engine may start a coding agent in a hosted run")
	}
	setMode(t, modeLegacy, "")
	if why := harnessBarred(hosted); why != "" {
		t.Fatalf("unpartitioned: %q", why)
	}
	if why := hostedHarnessRefusal(hosted.ID, "apps/cs|sb-1", "box"); why != "" {
		t.Fatalf("unpartitioned, a sandbox refused to a team-range run: %q", why)
	}
}

// TestHarnessStaysHome: a coding agent's conversation doesn't move between
// homes (A-M3) — publishing it, exporting it (a copy), are 409, and so is a
// built-in conversation's while a coding agent it started still works; once
// that one rests the copy goes.
func TestHarnessStaysHome(t *testing.T) {
	ag, h := userAgent(t)
	g := stubGlobalCalls(t, func(method, path string, body []byte) (int, string) {
		return 200, `{"id":7,"title":"x","owner":"alice","visibility":"team"}`
	})
	raw := mustJSON(defaultConfig())
	root, err := ag.db.createRunStamped("coder", raw, 0, statusIdle, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = ag.db.q.Exec(`UPDATE runs SET engine='harness' WHERE id=?`, root)
	publish := func(id int64, want int) string {
		t.Helper()
		return serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/publish", id), `{"share":{"visibility":"team"}}`, alicesFrame("read")), want, nil).Body.String()
	}
	if body := publish(root, 409); !strings.Contains(body, "stays in the space") {
		t.Fatalf("publishing a coding agent's conversation: %s", body)
	}
	serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/export", root), "", alicesFrame("read")), 409, nil)
	if n := len(g.got()); n != 0 {
		t.Fatalf("the global instance was asked: %v", g.got())
	}

	parent, _ := ag.db.createRunStamped("parent", raw, 0, statusIdle, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"})
	_, _ = ag.db.q.Exec(`INSERT INTO messages (run_id, seq, role, content, created) VALUES (?, 1, 'user', 'hi', 1)`, parent)
	kid, _ := ag.db.createRun("fake: a task", raw, parent)
	_, _ = ag.db.q.Exec(`UPDATE runs SET engine='harness', status='running' WHERE id=?`, kid)
	last := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	if err := ag.db.putHarnessSession(&harnessSession{RunID: kid, RootID: parent, State: hsLive, ExecID: "e1", LastActiveMs: last.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	var refused struct {
		Error string
		Runs  []int64
	}
	if err := json.Unmarshal([]byte(publish(parent, 409)), &refused); err != nil || !strings.Contains(refused.Error, "still at work") ||
		!strings.Contains(refused.Error, fmt.Sprintf("run #%d", kid)) || len(refused.Runs) != 1 || refused.Runs[0] != kid {
		t.Fatalf("publishing while a coding agent works in it: %+v %v", refused, err)
	}
	// its turn over, its adapter still up (until its idle stop): said so,
	// with when
	_, _ = ag.db.q.Exec(`UPDATE runs SET status='idle' WHERE id=?`, kid)
	refused.Runs = nil
	if err := json.Unmarshal([]byte(publish(parent, 409)), &refused); err != nil || !strings.Contains(refused.Error, "has finished, but it is still running, idle") ||
		!strings.Contains(refused.Error, "idle stop at about 10:15 UTC") || len(refused.Runs) != 1 || refused.Runs[0] != kid {
		t.Fatalf("publishing while an idle coding agent is still up: %+v %v", refused, err)
	}
	_, _ = ag.db.q.Exec(`UPDATE harness_sessions SET state='stopped' WHERE run_id=?`, kid)
	publish(parent, 200)
	if n := len(g.got()); n != 1 {
		t.Fatalf("the copy once its coding agent rests: %v", g.got())
	}
}

// TestHarnessUnshareRefused: at the global instance (where none can start —
// this one is planted) un-sharing a coding agent's conversation answers 409
// and moves nothing, nor is it hosted (A-M3, A-M2).
func TestHarnessUnshareRefused(t *testing.T) {
	ag, h := moveAgent(t)
	withTeam(t)
	stubMail(t)
	var run Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"x","share":{"members":[{"user":"bob","role":"participant"}]}}`, f5("alice", "read")), 200, &run)
	waitStatus(t, ag.db, run.ID, statusIdle)
	_, _ = ag.db.q.Exec(`UPDATE runs SET engine='harness' WHERE id=?`, run.ID)
	rec := serveJSON(t, h, as("POST", "/hosted", fmt.Sprintf(`{"conversation":%d}`, run.ID), f5("alice", "read")), 409, nil)
	if !strings.Contains(rec.Body.String(), "stays in the space") {
		t.Fatalf("hosting a coding agent's conversation: %s", rec.Body)
	}
	rec = serveJSON(t, h, as("DELETE", fmt.Sprintf("/runs/%d/members/bob", run.ID), "", f5("alice", "read")), 409, nil)
	if !strings.Contains(rec.Body.String(), "stays in the space") {
		t.Fatalf("un-sharing it: %s", rec.Body)
	}
	if st, _ := moveRow(ag, run.ID); st != "" {
		t.Fatalf("a move was asked: %q", st)
	}
	var members int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM run_members WHERE run_id=?`, run.ID).Scan(&members)
	if members != 1 {
		t.Fatalf("the refused un-share changed its members: %d", members)
	}
	serveJSON(t, h, as("PATCH", fmt.Sprintf("/runs/%d", run.ID), `{"visibility":"private"}`, f5("alice", "read")), 200, nil) // still shared with bob: no move

	// shared with the team only: making it private is the un-share — 409,
	// saying what to do instead
	var team Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"y","share":{"visibility":"team"}}`, f5("alice", "read")), 200, &team)
	waitStatus(t, ag.db, team.ID, statusIdle)
	_, _ = ag.db.q.Exec(`UPDATE runs SET engine='harness' WHERE id=?`, team.ID)
	rec = serveJSON(t, h, as("PATCH", fmt.Sprintf("/runs/%d", team.ID), `{"visibility":"private"}`, f5("alice", "read")), 409, nil)
	if !strings.Contains(rec.Body.String(), "keep it shared, or delete it") {
		t.Fatalf("making it private: %s", rec.Body)
	}
	if r, _ := ag.db.getRun(team.ID); r.Visibility != visTeam {
		t.Fatalf("the refused un-share changed its visibility: %q", r.Visibility)
	}
	if st, _ := moveRow(ag, team.ID); st != "" {
		t.Fatalf("a move was asked: %q", st)
	}

	// a member's own leave is theirs: bob leaves; it stays here, with alice
	serveJSON(t, h, as("DELETE", fmt.Sprintf("/runs/%d/members/bob", run.ID), "", f5("bob", "read")), 200, nil)
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM run_members WHERE run_id=?`, run.ID).Scan(&members)
	if st, _ := moveRow(ag, run.ID); members != 0 || st != "" {
		t.Fatalf("bob's leave: %d members, move %q", members, st)
	}
	if r, err := ag.db.getRun(run.ID); err != nil || r.Owner != "alice" {
		t.Fatalf("after bob left: %+v %v", r, err)
	}

	// control: a built-in one's un-share moves it
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/members", run.ID), `{"user":"bob","role":"participant"}`, f5("alice", "read")), 200, nil)
	_, _ = ag.db.q.Exec(`UPDATE runs SET engine='' WHERE id=?`, run.ID)
	serveJSON(t, h, as("DELETE", fmt.Sprintf("/runs/%d/members/bob", run.ID), "", f5("alice", "read")), 200, nil)
	if st, _ := moveRow(ag, run.ID); st != "asked" {
		t.Fatalf("control: a built-in one's un-share moves it: %q", st)
	}
}

// TestHarnessRelayInPartition: in a person's partition the terminal relays
// check a coding agent's sandbox as every use does (A-S2) — a manager that
// can't keep people apart is refused (409), a sandbox not homed there too
// (403), before any terminal is asked for.
func TestHarnessRelayInPartition(t *testing.T) {
	setMode(t, modeUser, "alice")
	t.Setenv("XBIN_COMPONENT", "apps/agent")
	pidMu.Lock()
	pidSeen = alicePID
	pidMu.Unlock()
	global := &atomic.Bool{}
	ms := bindSbxWith(t, partitionManager("alice", alicePID, global), "apps/cs", "apps/old")
	ms["apps/old"].Caps = []string{"exec", "files", "tty"}
	alice := who{kind: whoUser, user: "alice"}
	deny := func(*sbxSandbox) string { return "no" }
	own := mkSandbox(t, "apps/cs", "alice", sbxCreate{})
	if _, _, _, err := personSandbox(context.Background(), alice, sandboxRef("apps/cs", own.ID), deny); err != nil {
		t.Fatalf("her own sandbox: %v", err)
	}
	// hers too, but homed at the global instance: its terminal is GET
	// /sandboxes/{ref}/terminal's, not a coding agent's relay
	other := atGlobal(t, global, "apps/cs", "alice", "team")
	_, _, _, err := personSandbox(context.Background(), alice, sandboxRef("apps/cs", other.ID), deny)
	if sbxRefusal(err) != "not-allowed" || !strings.Contains(err.Error(), "isn't a sandbox of your own space") {
		t.Fatalf("a sandbox not homed in her partition: %v", err)
	}
	forgetHellos()
	_, _, _, err = personSandbox(context.Background(), alice, sandboxRef("apps/old", "sb-x"), deny)
	if sbxRefusal(err) != "partitions" {
		t.Fatalf("an old manager in a person's partition: %v", err)
	}
	setMode(t, modeLegacy, "")
	forgetHellos()
	if _, _, _, err := personSandbox(context.Background(), alice, sandboxRef("apps/old", "sb-x"), deny); sbxRefusal(err) == "partitions" {
		t.Fatalf("unpartitioned, an old manager is refused: %v", err)
	}
}

// TestHarnessCatalogOldManager: in a person's partition a manager that
// can't keep people apart gives the coding agents it would offer
// `manager-error`, saying why (its hello is refused there).
func TestHarnessCatalogOldManager(t *testing.T) {
	setMode(t, modeUser, "alice")
	partAgent(t) // the agent the catalog reads (its settings)
	m := bindSbxWith(t, partitionManager("alice", alicePID, nil), "apps/old")["apps/old"]
	m.Caps = []string{"exec", "files", "tty", "stdio"}
	forgetHellos()
	for _, e := range harnessCatalog(who{kind: whoUser, user: "alice"}, harnessManagers(context.Background()), nil) {
		if e.ID == "claude" && (e.Available || e.Reason != "manager-error" || !strings.Contains(e.Why, `"partitions"`)) {
			t.Fatalf("claude beside an old manager, in a person's partition: %+v", e)
		}
	}
}

// TestSandboxRowsHomed: in a person's partition GET /sandboxes says of
// each row whether a conversation there may work in it (`homed`) and why
// not (`why`) — the page's coding-agent pick uses them (A-S3); the harness
// catalog keeps what it learned only for sandboxes homed there.
// Unpartitioned rows are unchanged.
func TestSandboxRowsHomed(t *testing.T) {
	ag, h, box, _, _, global := harnessPartitionG(t)
	team := atGlobal(t, global, "apps/cs", "alice", "team box")
	_ = ag.db.noteHarnessSeen(sandboxRef("apps/cs", box.ID), "fake", ptrBool(true), nil)
	_ = ag.db.noteHarnessSeen(sandboxRef("apps/cs", team.ID), "fake", ptrBool(true), nil)
	var list struct{ Sandboxes []map[string]any }
	serveJSON(t, h, as("GET", "/sandboxes", "", alicesFrame("read")), 200, &list)
	seen := map[string]map[string]any{}
	for _, s := range list.Sandboxes {
		seen[s["id"].(string)] = s
	}
	if s := seen[box.ID]; s == nil || s["homed"] != true || s["why"] != nil {
		t.Fatalf("her own sandbox's row: %v", s)
	}
	if s := seen[team.ID]; s == nil || s["homed"] != false || s["why"] != partitionBoxRefusal(team) ||
		!strings.Contains(fmt.Sprint(s["why"]), "isn't a sandbox of your own space") {
		t.Fatalf("a sandbox not homed here (its why: partitionBoxRefusal's words, W6-U's seam): %v", s)
	}
	var cat struct{ Harnesses []hcEntry }
	serveJSON(t, h, as("GET", "/harnesses", "", alicesFrame("read")), 200, &cat)
	for _, e := range cat.Harnesses {
		if e.ID != "fake" {
			continue
		}
		if _, ok := e.Sandboxes[sandboxRef("apps/cs", box.ID)]; !ok {
			t.Fatalf("the catalog lost her own sandbox: %v", e.Sandboxes)
		}
		if _, ok := e.Sandboxes[sandboxRef("apps/cs", team.ID)]; ok {
			t.Fatalf("the catalog keeps a sandbox not homed here: %v", e.Sandboxes)
		}
	}
	setMode(t, modeLegacy, "")
	if v := sandboxItem(sbxCatalogEntry{Ref: "apps/cs|x", Box: box}, sbxAccess{}, nil); v["homed"] != nil || v["why"] != nil {
		t.Fatalf("an unpartitioned row: %v", v)
	}
}

func ptrBool(b bool) *bool { return &b }

// TestHarnessUsageMail: the global instance keeps a person's coding-agent
// sessions with their daily totals, and GET /usage adds them up (A-S6).
func TestHarnessUsageMail(t *testing.T) {
	ag, h := globalAgent(t)
	if err := ag.db.addHandoffSchema(); err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	data, _ := json.Marshal(map[string]any{"days": []usageDay{{Day: day, Runs: 2, LLMCalls: 3, HarnessSessions: 4}}})
	if err := ag.db.Tx(func(t *DB) error {
		return handleUsageMail(context.Background(), t, mailItem{ID: "m1", From: "user:alice", Topic: topicUsage, Data: data})
	}); err != nil {
		t.Fatal(err)
	}
	var out struct {
		People []struct {
			User  string
			Days  []usageDay
			Total usageDay
		}
	}
	serveJSON(t, h, as("GET", "/usage", "", ownerToken), 200, &out)
	if len(out.People) != 1 || out.People[0].Total.HarnessSessions != 4 || out.People[0].Days[0].HarnessSessions != 4 {
		t.Fatalf("GET /usage: %+v", out)
	}
}
