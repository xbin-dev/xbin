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
)

// scm_events_test.go — E's fixture and the intake's tests: a project with
// one repo (acme/web) and tasks made straight in the store (no sandbox),
// K's fake provider bound, the agent halted so the pump delivers nothing
// and the worker runs no job (what a test checks is in project_queue,
// project_events and project_jobs), the scm loop off (tests make its passes)
// and scmClock the fixture's.

const (
	evSHA    = "9fceb02d0ae598e95dc970b74767f19372d61af8"
	evSHA2   = "1111111111111111111111111111111111111111"
	evSHA3   = "2222222222222222222222222222222222222222"
	evBranch = "xbin/k3x9qa/1-fix-login"
	evBot    = "acme-xbin[bot]"
)

// evSCM is the bound provider as the agent sees it: K's client to the fake
// provider, with hello's events.healthy the test's.
type evSCM struct {
	scmAPI
	healthy atomic.Bool
}

func (e *evSCM) Hello(ctx context.Context) (*scmHello, error) {
	h, err := e.scmAPI.Hello(ctx)
	if h == nil {
		return h, err
	}
	cp := *h
	cp.Events.Healthy = e.healthy.Load()
	cp.Events.PollMinMs = 0
	return &cp, err
}

type evFx struct {
	t    *testing.T
	ag   *Agent
	mux  *http.ServeMux
	scm  *fakeSCM
	api  *evSCM
	p    *Project
	seq  int
	hook atomic.Int32 // scmEventHooks calls
}

// newEvFx is the fixture at mode (modeUser: alice's partition, her
// partition id known), its project's policy as given.
func newEvFx(t *testing.T, mode agentMode, policy string) *evFx {
	t.Helper()
	user := ""
	if mode == modeUser {
		user = "alice"
	}
	setMode(t, mode, user)
	off := scmLoopOff.Load()
	scmLoopOff.Store(true)
	t.Cleanup(func() { scmLoopOff.Store(off) })
	fx := &evFx{t: t}
	oldClock := scmClockAt.Load()
	scmClockAt.Store(time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC).UnixMilli())
	t.Cleanup(func() { scmClockAt.Store(oldClock) })
	oldHooks := scmEventHooks
	scmEventHooks = append(append([]func(*DB, *scmEvent){}, oldHooks...), func(*DB, *scmEvent) { fx.hook.Add(1) })
	t.Cleanup(func() { scmEventHooks = oldHooks })

	fx.ag = newTestAgent(t, newTestDB(t))
	useGlobalAgent(t, fx.ag)
	if mode == modeUser {
		kv := newMemKV()
		putConf(kv, "1", `{}`) // halted: a person's partition reads the brake from conf
		partitionConf(t, fx.ag, kv)
		pidMu.Lock()
		pidSeen = alicePID
		pidMu.Unlock()
	}
	if mode != modeLegacy {
		if err := fx.ag.db.addHandoffSchema(); err != nil {
			t.Fatal(err)
		}
	}
	fx.mux = http.NewServeMux()
	adapterRoutes(fx.mux)
	fx.scm = newFakeSCM(t, "apps/scm-github")
	bindSCM(t, fx.scm)
	if mode == modeUser {
		fx.scm.SetSignedIn("octocat", 583231)
	}
	// K's client to this fixture's fake (not scmFor: an enclosing test's
	// fixture may have swapped it)
	fx.api = &evSCM{scmAPI: &scmConn{E: scmEndpoint{Provider: "apps/scm-github", URL: fx.scm.srv.URL}}}
	oldFor := scmFor
	scmFor = func(name string) (scmAPI, error) {
		if name == "apps/scm-github" {
			return fx.api, nil
		}
		return oldFor(name)
	}
	t.Cleanup(func() { scmFor = oldFor })
	_ = fx.ag.db.putSetting("halt", "1") // nothing delivered, no job run

	fx.p = &Project{Name: "Web", Slug: "web", Kind: projPersonal, Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer,
		SCM: "apps/scm-github", Host: "github.com", SandboxRef: "apps/cs|sb-1", Dir: "/work/web", Policy: json.RawMessage(orStr(policy, `{}`)),
		State: projActive}
	if err := fx.ag.db.insertProject(fx.p); err != nil {
		t.Fatal(err)
	}
	if err := fx.ag.db.insertRepo(&ProjectRepo{ProjectID: fx.p.ID, Slug: "web", Repo: "acme/web", URL: "https://github.com/acme/web.git",
		DefaultBranch: "main", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	_, _ = fx.ag.db.q.Exec(`INSERT INTO project_creds (project_id, sandbox_ref, host, identity, login, written_ms) VALUES (?, ?, 'github.com', 'bot', ?, ?)`,
		fx.p.ID, fx.p.SandboxRef, evBot, nowMs())
	return fx
}

// addTask makes task n on branch (remote: its pushed sha, "" none) with
// prs, and runs the refs hooks as the refs job would.
func (fx *evFx) addTask(n int64, branch, remote string, prs ...TaskPR) *ProjectTask {
	fx.t.Helper()
	d := fx.ag.db
	run, err := d.createRun(fmt.Sprintf("task %d", n), "{}", 0)
	if err != nil {
		fx.t.Fatal(err)
	}
	_, _ = d.q.Exec(`UPDATE runs SET origin=?, origin_id=?, owner='alice' WHERE id=?`, originProject, fx.p.ID, run)
	phase := phaseOpen
	for i := range prs {
		prs[i].Repo = orStr(prs[i].Repo, "acme/web")
		prs[i].State = orStr(prs[i].State, "open")
		prs[i].Checks = orStr(prs[i].Checks, "none")
		if prs[i].State == "open" {
			phase = phasePR
		}
	}
	b, _ := json.Marshal(prs)
	if _, err := d.q.Exec(`INSERT INTO project_tasks (project_id, n, run_id, title, slug, branch, repos, ws, phase, prs, created_by, created_ms, updated_ms)
		VALUES (?, ?, ?, ?, ?, ?, '["web"]', 'ready', ?, ?, 'alice', ?, ?)`, fx.p.ID, n, run, "fix login", "fix-login", branch, phase, string(b), nowMs(), nowMs()); err != nil {
		fx.t.Fatal(err)
	}
	k := fx.task(n)
	if err := d.putCheckout(ProjectCheckout{TaskID: k.ID, Repo: "web", Path: "/work/web/tasks/1-fix-login/web", Mode: coWorktree, State: "ready",
		RemoteSHA: remote}); err != nil {
		fx.t.Fatal(err)
	}
	fx.refs(k)
	return fx.task(n)
}

// refs runs the refs hooks for k (its row as stored).
func (fx *evFx) refs(k *ProjectTask) {
	_ = fx.ag.db.Tx(func(t *DB) error {
		cur, _ := t.taskByID(k.ID)
		for _, h := range projectRefsHooks {
			h(t, fx.p, cur)
		}
		return nil
	})
}

func (fx *evFx) task(n int64) *ProjectTask {
	fx.t.Helper()
	k, err := fx.ag.db.taskByN(fx.p.ID, n)
	if err != nil {
		fx.t.Fatal(err)
	}
	return k
}

// ev is an event v1 for task 1's branch, head and PR #42, by a member.
func (fx *evFx) ev(kind, action string, mod ...func(*scmEvent)) scmEvent {
	fx.seq++
	e := scmEvent{Protocol: 1, EventID: fmt.Sprintf("scm:github.com:d%d", fx.seq), For: "global", Kind: kind, Action: action,
		Repo: "acme/web", Ref: scmEventRef{Branch: evBranch, SHA: evSHA, PR: 42},
		Actor: scmActor{Login: "octo-dev", Association: "MEMBER"}, At: fx.now()}
	for _, m := range mod {
		m(&e)
	}
	return e
}

func (fx *evFx) deliver(e scmEvent) *httptest.ResponseRecorder {
	fx.t.Helper()
	return fx.scm.Deliver(fx.mux, e)
}

func (fx *evFx) mustTake(e scmEvent) {
	fx.t.Helper()
	if w := fx.deliver(e); w.Code != 200 {
		fx.t.Fatalf("delivering %s.%s: %d %s", e.Kind, e.Action, w.Code, w.Body)
	}
}

func (fx *evFx) pass() { scmPass(context.Background(), fx.ag.db) }

func (fx *evFx) advance(d time.Duration) { scmClockAt.Add(d.Milliseconds()) }

// now is the fixture's time (scmClock); setNow moves it.
func (fx *evFx) now() int64 { return scmClockAt.Load() }

func (fx *evFx) setNow(ms int64) { scmClockAt.Store(ms) }

func (fx *evFx) inputs() []taskInput { return fx.ag.db.queuedInputs(fx.p.ID, 0) }

func (fx *evFx) events(kind string) []ProjectEvent {
	var out []ProjectEvent
	for _, ev := range fx.ag.db.projectEvents(fx.p.ID, 0, 500) {
		if kind == "" || ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func (fx *evFx) jobs(kind string) []*ProjectJob {
	return fx.ag.db.jobsWhere(`WHERE project_id=? AND kind=?`, fx.p.ID, kind)
}

func (fx *evFx) pollRow(n int64, kind string) *scmPollRow {
	r, _ := scmPollRowOf(fx.ag.db, fx.p.ID, n, "acme/web", kind)
	return r
}

func pevText(ev ProjectEvent) string {
	var b struct{ Text string }
	_ = json.Unmarshal(ev.Body, &b)
	return b.Text
}

// --- the schema ---------------------------------------------------------------------

func TestScmEventsSchemaMigratesTwice(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.q.Exec(`INSERT INTO scm_seen (key, at) VALUES ('scm:github.com:x', 1)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := addSCMEventSchema(db); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if err := db.addFeatureSchemas(); err != nil {
			t.Fatalf("feature schemas, run %d: %v", i, err)
		}
	}
	var n int
	if err := db.q.QueryRow(`SELECT count(*) FROM scm_seen`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("scm_seen after migrating twice: %d %v", n, err)
	}
	for _, tbl := range scmEventTables {
		if err := db.q.QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n); err != nil {
			t.Errorf("%s: %v", tbl, err)
		}
	}
}

// A database the previous binary left opens with E's tables empty, its
// rows kept, and opens again unchanged.
func TestScmEventsSchemaOldDB(t *testing.T) {
	path := seedOldDB(t, oldHarnessRows)
	for i := 0; i < 2; i++ {
		db, err := openDB(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		for _, tbl := range scmEventTables {
			var n int
			if err := db.sql.QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n); err != nil || n != 0 {
				t.Fatalf("open %d: %s: %d %v", i, tbl, n, err)
			}
		}
		runs, err := db.queryRuns(`ORDER BY id`)
		if err != nil || len(runs) != 3 {
			t.Fatalf("open %d: the old runs: %v %v", i, runs, err)
		}
		db.sql.Close()
	}
}

// --- the intake ---------------------------------------------------------------------

// Mounted through adapterRouteTables behind adapterGuard: a bound provider
// holding only the channel role delivers; another channel-role adapter, a
// person's partition of the provider, a person through it, a call without
// the role are refused; the body's provider is ignored for the caller's.
func TestScmEventCallerMustBeProvider(t *testing.T) {
	fx := newEvFx(t, modeLegacy, "")
	fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
	found := false
	for _, tbl := range adapterRouteTables {
		_, found = tbl()["POST /adapter/scm/event"]
		if found {
			break
		}
	}
	if !found {
		t.Fatal("POST /adapter/scm/event isn't in adapterRouteTables")
	}
	push := fx.ev(scmKindPush, "pushed", func(e *scmEvent) {
		e.Ref.SHA = evSHA2
		e.SCM = scmEventSource{Provider: "apps/evil", Host: "github.com"} // ignored: the caller is the provider
	})
	if w := fx.deliver(push); w.Code != 200 || !strings.Contains(w.Body.String(), `"taken":true`) {
		t.Fatalf("the bound provider (channel role): %d %s", w.Code, w.Body)
	}
	if in := fx.inputs(); len(in) != 1 || !strings.Contains(in[0].Text, "pushed") {
		t.Fatalf("the push wasn't routed as the provider's: %+v", in)
	}
	b, _ := json.Marshal(fx.ev(scmKindPush, "pushed"))
	call := func(hdr map[string]string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/adapter/scm/event", strings.NewReader(string(body)))
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		fx.mux.ServeHTTP(w, r)
		return w
	}
	for name, hdr := range map[string]map[string]string{
		"another adapter":               {"X-XBin-From": "apps/telegram", "X-XBin-Role": "channel"},
		"the provider's partition":      {"X-XBin-From": "apps/scm-github", "X-XBin-Role": "channel", "X-XBin-Partition": "user:bob"},
		"a person through the provider": {"X-XBin-From": "apps/scm-github", "X-XBin-Role": "channel", "X-XBin-User": "bob", "X-XBin-User-Level": "write"},
		"no role":                       {"X-XBin-From": "apps/scm-github"},
	} {
		if w := call(hdr, b); w.Code != 403 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	ok := map[string]string{"X-XBin-From": "apps/scm-github", "X-XBin-Role": "channel"}
	bad, _ := json.Marshal(map[string]any{"protocol": 2, "eventId": "scm:x:1", "kind": "push"})
	if w := call(ok, bad); w.Code != 400 || !strings.Contains(w.Body.String(), `"refusal":"protocol"`) {
		t.Errorf("protocol 2: %d %s", w.Code, w.Body)
	}
	if w := call(ok, []byte(`{"protocol":1}`)); w.Code != 400 {
		t.Errorf("no eventId: %d %s", w.Code, w.Body)
	}
	big := `{"protocol":1,"eventId":"scm:x:2","kind":"push","summary":"` + strings.Repeat("x", scmEventMax) + `"}`
	if w := call(ok, []byte(big)); w.Code != 413 {
		t.Errorf("a body over 1 MiB: %d", w.Code)
	}
	// unpartitioned: no person's partition to hand it to
	if w := fx.deliver(fx.ev(scmKindPush, "pushed", func(e *scmEvent) { e.For = "user:bob" })); w.Code != 404 {
		t.Errorf("for user:bob unpartitioned: %d %s", w.Code, w.Body)
	}
	if fx.hook.Load() != 1 {
		t.Errorf("scmEventHooks ran %d times, want 1", fx.hook.Load())
	}
}

// A repeat of an eventId answers 200 and does nothing.
func TestScmEventDedupe(t *testing.T) {
	fx := newEvFx(t, modeLegacy, "")
	fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
	e := fx.ev(scmKindPush, "pushed", func(e *scmEvent) { e.Ref.SHA = evSHA2 })
	fx.mustTake(e)
	w := fx.deliver(e)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"duplicate":true`) {
		t.Fatalf("the repeat: %d %s", w.Code, w.Body)
	}
	if in, evs := fx.inputs(), fx.events(pevPush); len(in) != 1 || len(evs) != 1 || fx.hook.Load() != 1 {
		t.Fatalf("after the repeat: %d inputs, %d push events, %d hook calls", len(in), len(evs), fx.hook.Load())
	}
}

// One event reaches each `for` once with the same eventId: at a
// partitioned agent's global instance the copies for alice, bob and global
// are three deliveries (two hand-offs, one taken there), and only a repeat
// to the same `for` is a duplicate.
func TestScmEventDedupePerFor(t *testing.T) {
	gfx := newEvFx(t, modeGlobal, "")
	mail := stubMail(t)
	e := gfx.ev(scmKindPush, "pushed", func(e *scmEvent) { e.Ref.SHA = evSHA2 })
	copies := []scmEvent{e, e, e}
	copies[0].For, copies[0].ForPid = "user:alice", alicePID
	copies[1].For, copies[1].ForPid = "user:bob", "u-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	copies[2].For = "global"
	for _, c := range copies {
		if w := gfx.deliver(c); w.Code != 200 || strings.Contains(w.Body.String(), "duplicate") {
			t.Fatalf("the copy for %s: %d %s", c.For, w.Code, w.Body)
		}
	}
	sent := mail.wait(t, 2)
	to := map[string]bool{}
	for _, m := range sent {
		to[m.to] = true
	}
	if !to["user:alice"] || !to["user:bob"] || gfx.hook.Load() != 1 {
		t.Fatalf("handed to %v; hooks at global %d", to, gfx.hook.Load())
	}
	for _, c := range copies {
		if w := gfx.deliver(c); w.Code != 200 || !strings.Contains(w.Body.String(), `"duplicate":true`) {
			t.Fatalf("the repeat for %s: %d %s", c.For, w.Code, w.Body)
		}
	}
	waitMailIdle(t)
	if mail.count() != 2 || gfx.hook.Load() != 1 {
		t.Fatalf("after the repeats: %d mails, %d hooks", mail.count(), gfx.hook.Load())
	}
}

// At a partitioned agent's global instance an event for a person is mailed
// to their partition (handoff/scm, the provider its source) and nothing of
// it stays; there it is taken once, only from global.
func TestHandoffScmToPartition(t *testing.T) {
	gfx := newEvFx(t, modeGlobal, "")
	mail := stubMail(t)
	e := gfx.ev(scmKindPush, "pushed", func(e *scmEvent) {
		e.For, e.ForPid, e.Ref.SHA = "user:alice", alicePID, evSHA2
		e.SCM = scmEventSource{Provider: "apps/evil", Host: "github.com"}
	})
	gfx.mustTake(e)
	sent := mail.wait(t, 1)
	var got scmEvent
	_ = json.Unmarshal(sent[0].data, &got)
	if sent[0].to != "user:alice" || sent[0].topic != topicSCM || sent[0].source != "apps/scm-github" || got.EventID != e.EventID ||
		got.SCM.Provider != "apps/scm-github" || got.ForPid != alicePID {
		t.Fatalf("the mail: %+v %s", sent[0], sent[0].data)
	}
	if w := gfx.deliver(e); w.Code != 200 || mail.count() != 1 {
		t.Fatalf("the repeat at global: %d, %d mails", w.Code, mail.count())
	}
	var state, payload string
	_ = gfx.ag.db.q.QueryRow(`SELECT state, payload FROM handoffs WHERE kind=?`, scmHandoffKind).Scan(&state, &payload)
	if state != "mailed" || payload != "" || gfx.hook.Load() != 0 {
		t.Fatalf("global kept: %s %q; hooks ran %d times there", state, payload, gfx.hook.Load())
	}
	waitMailIdle(t)

	// alice's partition
	fx := newEvFx(t, modeUser, "")
	fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
	fm := &fakeMail{items: []mailItem{
		{ID: "001", From: "user:bob", Topic: topicSCM, Data: sent[0].data, At: time.Now()}, // only global hands it
		{ID: "002", From: "global", Topic: topicSCM, Data: sent[0].data, At: time.Now()},
		{ID: "003", From: "global", Topic: topicSCM, Data: sent[0].data, At: time.Now()}, // mailed twice
	}}
	useMail(t, fm)
	if c, err := fx.ag.pullMail(context.Background()); err != nil || c.Handled != 3 {
		t.Fatalf("alice's mailbox: %+v %v", c, err)
	}
	if in := fx.inputs(); len(in) != 1 || !strings.Contains(in[0].Text, "pushed "+sha7(evSHA2)) || fx.hook.Load() != 1 {
		t.Fatalf("in alice's partition: %+v, hooks %d", in, fx.hook.Load())
	}
	// her partition never takes a delivery itself
	if w := fx.deliver(fx.ev(scmKindPush, "pushed", func(e *scmEvent) { e.For, e.ForPid = "user:alice", alicePID })); w.Code != 404 {
		t.Fatalf("a delivery straight to a person's partition: %d %s", w.Code, w.Body)
	}
}

// A person's partition drops an event made for another partition id (an
// earlier person of the same id), or for none — counted.
func TestScmEventForPidMismatchDropped(t *testing.T) {
	fx := newEvFx(t, modeUser, "")
	fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
	item := func(id, pid string, sha string) mailItem {
		e := fx.ev(scmKindPush, "pushed", func(e *scmEvent) {
			e.EventID, e.For, e.ForPid, e.Ref.SHA = "scm:github.com:"+id, "user:alice", pid, sha
			e.SCM = scmEventSource{Provider: "apps/scm-github", Host: "github.com"}
		})
		b, _ := json.Marshal(e)
		return mailItem{ID: id, From: "global", Topic: topicSCM, Data: b, At: time.Now()}
	}
	before := scmDropped("for-pid")
	fm := &fakeMail{items: []mailItem{
		item("001", "u-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", evSHA2),
		item("002", "", evSHA2),
		item("003", alicePID, evSHA3),
	}}
	useMail(t, fm)
	if c, err := fx.ag.pullMail(context.Background()); err != nil || c.Handled != 3 {
		t.Fatalf("the mailbox: %+v %v", c, err)
	}
	in := fx.inputs()
	if len(in) != 1 || !strings.Contains(in[0].Text, sha7(evSHA3)) || scmDropped("for-pid")-before != 2 || fx.hook.Load() != 1 {
		t.Fatalf("inputs %+v; for-pid drops %d; hooks %d", in, scmDropped("for-pid")-before, fx.hook.Load())
	}
}
