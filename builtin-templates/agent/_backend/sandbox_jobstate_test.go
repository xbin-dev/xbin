package main

// Regression tests from the phase-1 review of the sandbox job engine and the
// coding tools: which job a bash call is, jobs in a sandbox the conversation
// no longer has, a start the manager never answered, and what sandbox_create
// asks for and makes.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// sbxTransport routes the agent's calls to its managers through rt (after
// bindSbx), which passes them on with next.
func sbxTransport(t *testing.T, rt func(r *http.Request, next http.RoundTripper) (*http.Response, error)) {
	t.Helper()
	old := sbxClient
	c := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) { return rt(r, http.DefaultTransport) })}
	sbxClient = func() *http.Client { return c }
	t.Cleanup(func() { sbxClient = old })
}

// rewriteBody replaces old with new in a response's body.
func rewriteBody(resp *http.Response, old, new string) *http.Response {
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	raw = bytes.ReplaceAll(raw, []byte(old), []byte(new))
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	resp.Header.Del("Content-Length")
	return resp
}

// A provider that names every call "call_0" (or none at all): each bash
// call is its own command, in every turn — never an earlier turn's job.
func TestBashRepeatedCallIDs(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, _, _ := sbxRun(t, ag, "ids", "none")
	f := fakeOf(ag)
	bash := func(cmd string) func(LLMRequest) (LLMReply, error) {
		return callTools(tc("call_0", "bash", fmt.Sprintf(`{"command":%q}`, cmd)))
	}
	f.on(forRun(r.ID, lastIs("tool", "one\n[exit")), bash("echo two"))
	f.on(forRun(r.ID, lastIs("tool", "two\n[exit")), say("done"))
	f.on(forRun(r.ID, lastIs("tool", "three\n[exit")), bash("echo four"))
	f.on(forRun(r.ID, lastIs("tool", "four\n[exit")), say("done"))
	f.on(forRun(r.ID, lastIs("user", "first")), bash("echo one"))
	f.on(forRun(r.ID, lastIs("user", "second")), bash("echo three"))
	send(t, ag, r.ID, "first")
	waitFor(t, "the first turn", func() bool { return strings.Count(transcript(ag.db, r.ID), "A:done") == 1 })
	waitQuiet(t, ag)
	send(t, ag, r.ID, "second")
	waitFor(t, "the second turn", func() bool { return strings.Count(transcript(ag.db, r.ID), "A:done") == 2 })
	got := fullText(ag.db, r.ID)
	for i, w := range []string{"one", "two", "three", "four"} {
		if !strings.Contains(got, fmt.Sprintf("%s\n[exit 0 · ", w)) || !strings.Contains(got, fmt.Sprintf("· job %d]", i+1)) {
			t.Fatalf("%q did not run as job %d:\n%s", "echo "+w, i+1, got)
		}
	}
	jobs := ag.db.jobList(r.ID, false, 10)
	if len(jobs) != 4 || jobs[0].Command != "echo four" || jobs[3].Command != "echo one" {
		t.Fatalf("the jobs: %d", len(jobs))
	}
	calls := map[string]bool{}
	for _, j := range jobs {
		if calls[j.Call] {
			t.Fatalf("two jobs of call %s", j.Call)
		}
		calls[j.Call] = true
	}
}

// uniqueCallIDs never hands out an id the run already used, even one it
// generated itself in an earlier turn.
func TestUniqueCallIDsAcrossTurns(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	id, _ := ag.db.createRun("t", "", 0)
	run, _ := ag.db.getRun(id)
	seen := map[string]bool{}
	for turn := 0; turn < 3; turn++ {
		for step := 0; step < 3; step++ {
			run.TurnSteps = step
			calls := ag.eng.uniqueCallIDs(run, []toolCall{tc("", "bash", "{}"), tc("call_0", "bash", "{}"), tc("call_0", "bash", "{}")})
			for _, c := range calls {
				if seen[c.ID] {
					t.Fatalf("turn %d step %d: %s again", turn, step, c.ID)
				}
				seen[c.ID] = true
				if _, err := ag.db.addMessage(&Message{RunID: id, Role: "tool", ToolCallID: c.ID, Content: "x"}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

// bash takes an earlier job of the same call only when it is the same
// request still going; anything else runs as a new job.
func TestBashReusesOnlyTheSameRequest(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, box := sbxRun(t, ag, "reuse", "none")
	conn, _ := sbxDial("apps/cs", "")
	if out := mustTool(t, ag, r, cfg, "c1", "bash", map[string]any{"command": "echo one"}); !strings.HasPrefix(out, "one\n[exit 0") || !strings.HasSuffix(out, "job 1]") {
		t.Fatalf("first: %q", out)
	}
	// the same call id, another command: it runs
	if out := mustTool(t, ag, r, cfg, "c1", "bash", map[string]any{"command": "echo two"}); !strings.HasPrefix(out, "two\n[exit 0") || !strings.HasSuffix(out, "job 2]") {
		t.Fatalf("another command under the same call: %q", out)
	}
	// the same request, already ended: it runs again
	if out := mustTool(t, ag, r, cfg, "c1", "bash", map[string]any{"command": "echo two"}); !strings.HasPrefix(out, "two\n[exit 0") || !strings.HasSuffix(out, "job 3]") {
		t.Fatalf("the same command again after it ended: %q", out)
	}
	// another cwd: another request
	mustTool(t, ag, r, cfg, "c2", "bash", map[string]any{"command": "mkdir -p sub"})
	if out := mustTool(t, ag, r, cfg, "c2", "bash", map[string]any{"command": "pwd", "cwd": "sub"}); !strings.HasSuffix(out, "job 5]") {
		t.Fatalf("another cwd: %q", out)
	}
	// the same request still running: the one command
	mustTool(t, ag, r, cfg, "c3", "bash", map[string]any{"command": "sleep 0.5; echo slow", "background": true})
	if out := mustTool(t, ag, r, cfg, "c3", "bash", map[string]any{"command": "sleep 0.5; echo slow"}); !strings.HasPrefix(out, "slow\n[exit 0") || !strings.HasSuffix(out, "job 6]") {
		t.Fatalf("re-issued while running: %q", out)
	}
	execs, _ := conn.ExecList(context.Background(), box.ID)
	if len(execs) != 6 {
		t.Fatalf("execs at the manager: %d", len(execs))
	}
	if j := ag.db.jobByCall(r.ID, "c1"); j == nil || j.Job != 3 {
		t.Fatalf("the call's job is its latest: %+v", j)
	}
}

// Jobs left in a sandbox the conversation no longer has: detaching it stops
// them (KILL, in the background) and records it; they don't count toward the
// cap, and bash_output/bash_kill answer what is known of them.
func TestDetachedSandboxJobs(t *testing.T) {
	ag, mux := accessFixture(t)
	m := bindSbx(t, "apps/cs")["apps/cs"]
	shortGrace(t)
	conv := createConv(t, ag, alicePrivate, nil)
	a := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "a"})
	b := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "b"})
	refA, refB := sandboxRef("apps/cs", a.ID), sandboxRef("apps/cs", b.ID)
	if got := bindTo(t, mux, asAlice, conv.ID, refA, ""); got != 200 {
		t.Fatal(got)
	}
	cfg, _ := ag.db.runConfig(conv.ID)
	for i := 1; i <= maxRunningJobs; i++ {
		mustTool(t, ag, conv, cfg, fmt.Sprint("a", i), "bash", map[string]any{"command": "sleep 30", "background": true})
	}
	if bindTo(t, mux, asAlice, conv.ID, refB, "") != 200 {
		t.Fatal("bind b")
	}
	start := time.Now()
	if w := callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", conv.ID), map[string]any{"detach": refA}); w.Code != 200 {
		t.Fatalf("detach: %d %s", w.Code, w.Body)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the detach waited %s", took)
	}
	conn, _ := sbxDial("apps/cs", "alice")
	waitFor(t, "a's commands to be killed", func() bool {
		execs, err := conn.ExecList(context.Background(), a.ID)
		if err != nil || len(execs) != maxRunningJobs {
			return false
		}
		for _, ex := range execs {
			if ex.State == "running" {
				return false
			}
		}
		return true
	})
	cfg, _ = ag.db.runConfig(conv.ID)
	if out := mustTool(t, ag, conv, cfg, "b1", "bash", map[string]any{"command": "echo room"}); !strings.HasPrefix(out, "room\n[exit 0") {
		t.Fatalf("bash in b: %q", out)
	}
	for _, name := range []string{"bash_output", "bash_kill"} {
		out, err := tool(t, ag, conv, cfg, "o", name, map[string]any{"job": 1})
		if err != nil || !strings.Contains(out, "job 1") || !strings.Contains(out, "no longer use") || !strings.Contains(out, "killed") {
			t.Fatalf("%s on a detached sandbox's job: %q %v", name, out, err)
		}
	}
	if out := mustTool(t, ag, conv, cfg, "i", "sandbox_info", map[string]any{}); !strings.Contains(out, "job 1 · killed") || strings.Contains(out, "· running ·") {
		t.Fatalf("sandbox_info: %q", out)
	}

	// deleting the sandbox detaches it everywhere: its jobs end with it
	mustTool(t, ag, conv, cfg, "b2", "bash", map[string]any{"command": "sleep 30", "background": true})
	if w := callAs(t, mux, asAlice, "DELETE", "/sandboxes/"+url.PathEscape(refB), nil); w.Code != 200 {
		t.Fatalf("delete b: %d %s", w.Code, w.Body)
	}
	j10, _ := ag.db.job(conv.ID, 10)
	if j10.running() {
		t.Fatalf("a deleted sandbox's job: %+v", j10)
	}
	waitFor(t, "the KILL to the deleted sandbox", func() bool {
		return m.count("POST", "/sbx/sandboxes/"+b.ID+"/execs/"+j10.Exec+"/signal") > 0
	})

	// rows an older version left running where nothing is attached any more
	for i := 0; i < maxRunningJobs; i++ {
		ph := &sbxJob{Root: conv.ID, Run: conv.ID, Call: fmt.Sprint("old", i), Ref: "apps/cs|gone-" + fmt.Sprint(i), Command: "sleep 99", Created: nowMs()}
		if err := ag.db.newJob(ph); err != nil {
			t.Fatal(err)
		}
		ag.db.jobStarted(ph, "e-old")
	}
	c := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "c"})
	if bindTo(t, mux, asAlice, conv.ID, sandboxRef("apps/cs", c.ID), "") != 200 {
		t.Fatal("bind c")
	}
	cfg, _ = ag.db.runConfig(conv.ID)
	if out := mustTool(t, ag, conv, cfg, "c1", "bash", map[string]any{"command": "echo still"}); !strings.HasPrefix(out, "still\n") {
		t.Fatalf("phantom rows block bash: %q", out)
	}
	if n := len(ag.db.jobList(conv.ID, true, 100)); n != 0 {
		t.Fatalf("still counted as running: %d", n)
	}
}

// A start the manager never answered may have started all the same: the
// job stays, named, and bash_output finds its command by its clientId —
// it never runs twice.
func TestBashStartWithoutAnAnswer(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, box := sbxRun(t, ag, "flaky", "none")
	var mode atomic.Value
	mode.Store("")
	sbxTransport(t, func(req *http.Request, next http.RoundTripper) (*http.Response, error) {
		if req.Method != "POST" || !strings.HasSuffix(req.URL.Path, "/execs") {
			return next.RoundTrip(req)
		}
		switch mode.Load() {
		case "reset": // the command started; its answer was lost
			if resp, err := next.RoundTrip(req); err == nil {
				resp.Body.Close()
			}
			return nil, errors.New("connection reset by peer")
		case "hang": // the command started; the answer never comes
			if resp, err := next.RoundTrip(req); err == nil {
				resp.Body.Close()
			}
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		return next.RoundTrip(req)
	})
	conn, _ := sbxDial("apps/cs", "")
	mode.Store("reset")
	out, err := tool(t, ag, r, cfg, "c1", "bash", map[string]any{"command": "echo hi; sleep 0.3; echo there"})
	if err != nil || !strings.Contains(out, "job 1") || !strings.Contains(out, `bash_output {"job": 1}`) {
		t.Fatalf("no answer: %q %v", out, err)
	}
	if j, err := ag.db.job(r.ID, 1); err != nil || j.State != "starting" || j.FG {
		t.Fatalf("the job: %+v %v", j, err)
	}
	mode.Store("")
	if out := mustTool(t, ag, r, cfg, "c2", "bash_output", map[string]any{"job": 1, "wait_s": 5}); !strings.HasPrefix(out, "hi\nthere\n[exit 0") {
		t.Fatalf("followed: %q", out)
	}
	if execs, _ := conn.ExecList(context.Background(), box.ID); len(execs) != 1 {
		t.Fatalf("execs: %d", len(execs))
	}

	// the tool's own timeout while the start is unanswered: a job, left running
	mode.Store("hang")
	short := cfg
	short.ToolTimeout = 1
	out, err = tool(t, ag, r, short, "c3", "bash", map[string]any{"command": "sleep 0.5; echo late"})
	if err != nil || !strings.Contains(out, "job 2") {
		t.Fatalf("the tool's timeout during the start: %q %v", out, err)
	}
	mode.Store("")
	if out := mustTool(t, ag, r, cfg, "c4", "bash_output", map[string]any{"job": 2, "wait_s": 5}); !strings.HasPrefix(out, "late\n[exit 0") {
		t.Fatalf("followed after the tool's timeout: %q", out)
	}
	// a refusal is an answer: nothing started, no job
	if _, err := tool(t, ag, r, cfg, "c5", "bash", map[string]any{"command": "true", "cwd": "missing"}); err == nil {
		t.Fatal("a missing cwd")
	}
	if j := ag.db.jobList(r.ID, false, 10); len(j) != 2 {
		t.Fatalf("a refused start leaves a job: %d", len(j))
	}
}

// The grant asks for exactly what will be made: resolved against the
// manager before parking (a manager that doesn't answer isn't asked about),
// and made as asked — or refused when the manager's offer changed.
func TestSandboxCreateAsksWhatItMakes(t *testing.T) {
	ag, mux := accessFixture(t)
	m := bindSbx(t, "apps/cs")["apps/cs"]
	var offer atomic.Value
	offer.Store(`"egress":["internet"]`)
	sbxTransport(t, func(req *http.Request, next http.RoundTripper) (*http.Response, error) {
		resp, err := next.RoundTrip(req)
		if err == nil && resp.StatusCode == 200 && strings.HasSuffix(req.URL.Path, "/sbx/hello") {
			resp = rewriteBody(resp, `"egress":["none","internet"]`, offer.Load().(string))
		}
		return resp, err
	})
	conv := createConv(t, ag, alicePrivate, nil)
	f := fakeOf(ag)
	f.on(lastIs("tool", ""), say("done"))
	f.on(lastUser("box x"), callTools(tc("c1", "sandbox_create", `{"name":"x"}`))).once()
	f.on(lastUser("box x"), callTools(tc("c2", "sandbox_create", `{"name":"x"}`))).once()
	f.on(lastUser("box y"), callTools(tc("c3", "sandbox_create", `{"name":"y"}`)))
	approve := func() {
		t.Helper()
		waitStatus(t, ag.db, conv.ID, statusWaiting)
		if got := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", conv.ID), map[string]any{"approve": true}).Code; got != 200 {
			t.Fatalf("approve: %d", got)
		}
		waitQuiet(t, ag)
	}

	// the manager doesn't answer: nothing is asked with guessed words
	m.FailNext("hello", 503, "unavailable", "the manager is starting")
	forgetHellos()
	send(t, ag, conv.ID, "box x")
	waitQuiet(t, ag)
	if st := statusOf(ag.db, conv.ID); st == statusWaiting {
		t.Fatalf("parked with a guess: %q", parsePending(mustRun(t, ag, conv.ID).Pending).GrantAsk)
	}
	if out := lastToolOf(ag, conv.ID); !strings.Contains(out, "the manager is starting") || len(sandboxesAt(t)) != 0 {
		t.Fatalf("an unresolved create: %q", out)
	}

	// it answers: the ask names its choice, and that is what is made
	forgetHellos()
	send(t, ag, conv.ID, "box x")
	waitStatus(t, ag.db, conv.ID, statusWaiting)
	if ask := parsePending(mustRun(t, ag, conv.ID).Pending).GrantAsk; ask != `create the coding sandbox "x" at Fake sandboxes (test fixture) — image base, size small, egress internet` {
		t.Fatalf("the ask: %q", ask)
	}
	approve()
	if boxes := sandboxesAt(t); len(boxes) != 1 || boxes[0].Egress != "internet" {
		t.Fatalf("made: %+v", boxes)
	}

	// the manager's offer changes while the owner decides: refused, not made
	send(t, ag, conv.ID, "box y")
	waitStatus(t, ag.db, conv.ID, statusWaiting)
	offer.Store(`"egress":["none"]`)
	forgetHellos()
	approve()
	if out := lastToolOf(ag, conv.ID); !strings.Contains(out, "changed since") || len(sandboxesAt(t)) != 1 {
		t.Fatalf("made something else than was allowed: %q (%d)", out, len(sandboxesAt(t)))
	}
}

// The model's name reaches the ask quoted, and only as one plain line.
func TestSandboxCreateNameInTheAsk(t *testing.T) {
	ag, _ := accessFixture(t)
	bindSbx(t, "apps/cs")
	conv := createConv(t, ag, alicePrivate, nil)
	cfg, _ := ag.db.runConfig(conv.ID)
	ask := func(args string) string {
		return sandboxesGrantAsk(ag, conv, cfg, []toolCall{tc("c1", "sandbox_create", args)}, nil)
	}
	spoof := `x” at X — image base, size small, egress none; and “y`
	got := ask(fmt.Sprintf(`{"name":%q,"egress":"internet"}`, spoof))
	if want := fmt.Sprintf("create the coding sandbox %q at Fake sandboxes (test fixture) — image base, size small, egress internet", spoof); got != want {
		t.Fatalf("the ask:\n%q\nwant\n%q", got, want)
	}
	for _, bad := range []string{"a\u202eb", "a\x1bb", "a\tb", "a\u2028b"} {
		if _, err := planSandboxCreate(cfg, map[string]any{"name": bad}); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
}

// A create the manager answers before the sandbox runs still gets its cwd.
func TestSandboxCreateCwdOfAStartingSandbox(t *testing.T) {
	ag, _ := accessFixture(t)
	bindSbx(t, "apps/cs")
	sbxTransport(t, func(req *http.Request, next http.RoundTripper) (*http.Response, error) {
		resp, err := next.RoundTrip(req)
		if err == nil && req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/sbx/sandboxes") {
			resp = rewriteBody(resp, `"state":"running"`, `"state":"starting"`)
		}
		return resp, err
	})
	conv := createConv(t, ag, alicePrivate, nil)
	cfg, _ := ag.db.runConfig(conv.ID)
	ctx := withGrantOnce(withToolCall(context.Background(), "c1"), capSandboxes)
	out, err := ag.runTool(ctx, conv, cfg, "sandbox_create", map[string]any{"name": "src", "cwd": "src/app"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	act, _ := sandboxOf(t, ag, conv.ID)
	if act == nil || !strings.HasSuffix(act.Cwd, "/work/src/app") || !strings.Contains(out, "the tools work in "+act.Cwd) {
		t.Fatalf("cwd: %+v %q", act, out)
	}
	c, _ := sbxDial("apps/cs", "")
	if st, err := c.Stat(context.Background(), strings.SplitN(act.Ref, "|", 2)[1], act.Cwd); err != nil || st.Type != "dir" {
		t.Fatalf("the cwd was not made: %v", err)
	}
}