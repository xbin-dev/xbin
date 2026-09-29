package main

// D134: jobs that don't kill themselves or lose their output, a sleeping
// run that wakes when its job ends, waits that say when they were cut.

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The command reaches its shell through the environment, so a pkill -f in
// it can't match the job's own shell; its exec is labelled with the job.
func TestPkillDoesNotKillItsOwnJob(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	m := bindSbx(t, "apps/cs")["apps/cs"]
	r, cfg, box := sbxRun(t, ag, "self", "none")
	out := mustTool(t, ag, r, cfg, "c1", "bash", map[string]any{
		"command": "echo before; pkill -f marker-q7z; echo after marker-q7z", "force": true})
	if !strings.Contains(out, "before\nafter marker-q7z\n[exit 0 · ") {
		t.Fatalf("the job killed itself: %q", out)
	}
	conn, _ := sbxDial("apps/cs", "")
	execs, _ := conn.ExecList(context.Background(), box.ID)
	if len(execs) != 1 || execs[0].Cmd != jobShellCmd || !strings.HasPrefix(execs[0].Label, "agent · job 1 · echo before; pkill") {
		t.Fatalf("the exec: %+v", execs[0])
	}
	for _, c := range m.Calls() {
		if c.Method == "POST" && strings.HasSuffix(c.Path, "/execs") && (!strings.Contains(c.Body, `"AGENT_JOB_CMD":"echo before`) || !strings.Contains(c.Body, `"PYTHONUNBUFFERED":"1"`)) {
			t.Fatalf("the start: %s", c.Body)
		}
	}
}

// A command that kills by name isn't run without force: the answer names
// bash_kill and lists the jobs.
func TestKillByNameGuard(t *testing.T) {
	for cmd, want := range map[string]bool{
		"pkill -f server.py": true, "pkill --full 'node x'": true, "pkill -9 -f x": true, "sleep 1; pkill -fx y": true,
		"killall node": true, "sudo killall -9 python3": true,
		"pkill node": false, "pkill -u dev": false, "kill 123": false, "echo pkill-f": false, "grep -f pkill.txt": false,
		"skillall x": false, "echo ok | pkill -x x": false,
	} {
		if got := killsByName(cmd); got != want {
			t.Errorf("killsByName(%q) = %v", cmd, got)
		}
	}
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, _ := sbxRun(t, ag, "guard", "none")
	mustTool(t, ag, r, cfg, "c1", "bash", map[string]any{"command": "sleep 30 # the server", "background": true})
	out := mustTool(t, ag, r, cfg, "c2", "bash", map[string]any{"command": "pkill -f 'the server'"})
	if !strings.HasPrefix(out, "not run: stop jobs with bash_kill") || !strings.Contains(out, "force:true") ||
		!strings.Contains(out, "job 1 · running · ") || !strings.Contains(out, "sleep 30 # the server") {
		t.Fatalf("refused: %q", out)
	}
	if jobs := ag.db.jobList(r.ID, false, 10); len(jobs) != 1 {
		t.Fatalf("a refused command became a job: %d", len(jobs))
	}
	shortGrace(t)
	mustTool(t, ag, r, cfg, "c3", "bash_kill", map[string]any{"job": 1})
}

// bash_kill says how the job ended and shows what it wrote last; the signal
// is kept, and jobs and bash_output say it later.
func TestBashKillReportsTheTail(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	shortGrace(t)
	r, cfg, _ := sbxRun(t, ag, "tail", "none")
	mustTool(t, ag, r, cfg, "c1", "bash", map[string]any{"command": "for i in $(seq 1 2000); do echo line $i; done; echo last words; sleep 30", "background": true})
	waitFor(t, "its output", func() bool {
		out := mustTool(t, ag, r, cfg, "p", "bash_output", map[string]any{"job": 1, "offset": 0})
		return strings.Contains(out, "last words")
	})
	j, _ := ag.db.job(r.ID, 1)
	ag.db.jobRead(j, 0) // as if nothing was read
	out := mustTool(t, ag, r, cfg, "c2", "bash_kill", map[string]any{"job": 1})
	if !strings.HasPrefix(out, "… its last 4096 bytes (bash_output {\"job\": 1, \"offset\": 0} reads what came before) …\n") ||
		!strings.Contains(out, "line 2000\nlast words\n[job 1 stopped · killed by TERM]") || len(out) > 4400 {
		t.Fatalf("killed: %q", out)
	}
	if j, _ := ag.db.job(r.ID, 1); j.State != "killed" || j.Signal != "TERM" || j.Exit != nil {
		t.Fatalf("the job: %+v", j)
	}
	if out := mustTool(t, ag, r, cfg, "c3", "bash_output", map[string]any{"job": 1}); !strings.HasPrefix(out, "(no new output)\n[killed by TERM") {
		t.Fatalf("read after: %q", out)
	}
	if out := mustTool(t, ag, r, cfg, "c4", "jobs", nil); !strings.Contains(out, "job 1 · killed by TERM · ran ") || !strings.Contains(out, "for i in $(seq 1 2000)") {
		t.Fatalf("jobs: %q", out)
	}
	if out := mustTool(t, ag, r, cfg, "c5", "bash_kill", map[string]any{"job": 1}); out != "job 1 isn't running (killed by TERM)" {
		t.Fatalf("killed twice: %q", out)
	}
	// read to the end already: nothing new
	mustTool(t, ag, r, cfg, "c6", "bash", map[string]any{"command": "echo only; sleep 30", "background": true})
	waitFor(t, "its output", func() bool {
		return strings.HasPrefix(mustTool(t, ag, r, cfg, "p2", "bash_output", map[string]any{"job": 2}), "only\n")
	})
	if out := mustTool(t, ag, r, cfg, "c7", "bash_kill", map[string]any{"job": 2}); out != "(no new output since you last read it)\n[job 2 stopped · killed by TERM]" {
		t.Fatalf("nothing new: %q", out)
	}
}

// jobs lists the running and the ended, newest first.
func TestJobsTool(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	shortGrace(t)
	r, cfg, _ := sbxRun(t, ag, "jobs", "none")
	if out := mustTool(t, ag, r, cfg, "c0", "jobs", nil); !strings.HasPrefix(out, "no jobs yet") {
		t.Fatalf("none: %q", out)
	}
	mustTool(t, ag, r, cfg, "c1", "bash", map[string]any{"command": "exit 4"})
	mustTool(t, ag, r, cfg, "c2", "bash", map[string]any{"command": "sleep 0.2", "background": true})
	mustTool(t, ag, r, cfg, "c3", "bash", map[string]any{"command": "sleep 30", "background": true})
	time.Sleep(400 * time.Millisecond)
	out := mustTool(t, ag, r, cfg, "c4", "jobs", nil)
	lines := strings.Split(out, "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "job 3 · running · ") || !strings.HasPrefix(lines[1], "job 2 · exit 0 · ran ") ||
		!strings.HasPrefix(lines[2], "job 1 · exit 4 · ran ") || !strings.Contains(lines[0], "sleep 30 (in /") {
		t.Fatalf("jobs (the ended one asked of the sandbox):\n%s", out)
	}
	mustTool(t, ag, r, cfg, "c5", "bash_kill", map[string]any{"job": 3})
}

// A wait cut short by the tool call's time limit says so; the default wait
// doesn't.
func TestClippedWaitsAreStated(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	shortGrace(t)
	r, cfg, _ := sbxRun(t, ag, "clip", "none")
	short := cfg
	short.ToolTimeout = 4 // minus the 3 s margin: about a second
	out := mustTool(t, ag, r, short, "c1", "bash", map[string]any{"command": "sleep 30", "timeout_s": 60})
	if !strings.Contains(out, "still running after 1s (timeout_s 60 was cut to 1s: a tool call's time limit) · job 1") {
		t.Fatalf("bash: %q", out)
	}
	out = mustTool(t, ag, r, short, "c2", "bash_output", map[string]any{"job": 1, "wait_s": 900})
	if !strings.Contains(out, "so far (wait_s 900 was cut to 1s: a tool call's time limit) · job 1") {
		t.Fatalf("bash_output: %q", out)
	}
	if out := mustTool(t, ag, r, short, "c3", "bash", map[string]any{"command": "sleep 30"}); strings.Contains(out, "was cut") {
		t.Fatalf("the default wait: %q", out)
	}
	if _, cut := waitUntil(context.Background(), "wait_s", 900, jobWaitMax); cut != " (wait_s 900 was cut to its most, 600)" {
		t.Fatalf("the cap: %q", cut)
	}
	if _, cut := waitUntil(context.Background(), "wait_s", 5, jobWaitMax); cut != "" {
		t.Fatalf("a wait that fits: %q", cut)
	}
	for _, n := range []any{1, 2} {
		mustTool(t, ag, r, cfg, "k", "bash_kill", map[string]any{"job": n})
	}
}

// An interrupted bash keeps what the command wrote, and names the job.
func TestInterruptedBashKeepsItsOutput(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	useGlobalAgent(t, ag)
	m := bindSbx(t, "apps/cs")["apps/cs"]
	shortGrace(t)
	r, _, box := sbxRun(t, ag, "partial", "none")
	f := fakeOf(ag)
	f.on(forRun(r.ID, lastUser("go")), callTools(tc("b1", "bash", `{"command":"echo partial-out; sleep 30"}`))).once()
	send(t, ag, r.ID, "go")
	waitFor(t, "the output read", func() bool { return m.count("GET", "/sbx/sandboxes/"+box.ID+"/execs/e1/output") > 1 })
	if w := serve(handleInterrupt, "POST", "/runs/x/interrupt", r.ID, nil, ""); w.Code != 200 {
		t.Fatalf("interrupt: %d %s", w.Code, w.Body)
	}
	waitStatus(t, ag.db, r.ID, statusIdle)
	got := fullText(ag.db, r.ID)
	if !strings.Contains(got, "partial-out\n[interrupted by the owner · job 1 got TERM") {
		t.Fatalf("the call's answer:\n%s", got)
	}
	waitFor(t, "the job to be recorded as killed", func() bool {
		j, _ := ag.db.job(r.ID, 1)
		return j != nil && j.State == "killed" && j.Signal == "TERM"
	})
}

// A run yielding while its job runs wakes when the job ends — not at its
// timer; until_job waits for one job, and one that ended returns at once.
func TestYieldWakesWhenItsJobEnds(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	useGlobalAgent(t, ag)
	bindSbx(t, "apps/cs")
	shortGrace(t)
	r, _, _ := sbxRun(t, ag, "yield", "none")
	f := fakeOf(ag)
	f.on(forRun(r.ID, lastUser("build")), callTools(tc("b1", "bash", `{"command":"sleep 0.5; echo built","background":true}`))).once()
	f.on(forRun(r.ID, lastIs("tool", "started job 1")), callTools(tc("y1", "yield", `{"seconds":3600}`))).once()
	f.on(forRun(r.ID, lastIs("tool", "waking early when job 1 ends")), say("job done"))
	start := time.Now()
	send(t, ag, r.ID, "build")
	waitFor(t, "the wake", func() bool { return strings.Contains(transcript(ag.db, r.ID), "A:job done") })
	if time.Since(start) > 4*time.Second {
		t.Fatalf("woke after %s", time.Since(start))
	}
	if j, _ := ag.db.job(r.ID, 1); j.State != "exited" {
		t.Fatalf("the job: %+v", j)
	}

	// until_job: another job ending doesn't wake it
	f.on(forRun(r.ID, lastUser("two")), callTools(tc("b2", "bash", `{"command":"sleep 0.2","background":true}`))).once()
	f.on(forRun(r.ID, lastIs("tool", "started job 2")), callTools(tc("b3", "bash", `{"command":"sleep 1","background":true}`))).once()
	f.on(forRun(r.ID, lastIs("tool", "started job 3")), callTools(tc("y2", "yield", `{"until_job":3}`))).once()
	f.on(forRun(r.ID, lastIs("tool", "sleeping until job 3 ends, 3600s at most")), func(req LLMRequest) (LLMReply, error) {
		if j, _ := ag.db.job(r.ID, 3); j == nil || j.running() {
			return say("woke early")(req)
		}
		return say("job 3 done")(req)
	})
	send(t, ag, r.ID, "two")
	waitFor(t, "the until_job wake", func() bool {
		tr := transcript(ag.db, r.ID)
		return strings.Contains(tr, "A:job 3 done") || strings.Contains(tr, "A:woke early")
	})
	if tr := transcript(ag.db, r.ID); !strings.Contains(tr, "A:job 3 done") {
		t.Fatalf("until_job:\n%s", fullText(ag.db, r.ID))
	}

	// a job that has already ended: no sleep
	f.on(forRun(r.ID, lastUser("again")), callTools(tc("y3", "yield", `{"until_job":1}`))).once()
	f.on(forRun(r.ID, lastIs("tool", "didn't sleep: job 1 has already ended — exit 0")), say("at once"))
	send(t, ag, r.ID, "again")
	waitFor(t, "no sleep", func() bool { return strings.Contains(transcript(ag.db, r.ID), "A:at once") })
	ag.eng.mu.Lock()
	watching := len(ag.eng.jobWatch)
	ag.eng.mu.Unlock()
	if watching != 0 {
		t.Fatalf("%d watchers left", watching)
	}
}

// The prompt names the image's tools and the two places files live; the
// yield tool takes until_job only where there are jobs.
func TestSandboxPromptAndYieldSpec(t *testing.T) {
	cfg := defaultConfig()
	cfg.Class = "coding"
	b := SandboxBinding{Ref: "apps/cs|sb-1", Name: "box", Egress: "none", Tools: []string{"git", "node"}}
	cfg.Sandbox, cfg.Attached = &b, []SandboxBinding{b}
	p := sandboxPrompt(cfg)
	for _, want := range []string{"Its image has git, node. ", "Files live in two places", "never pkill -f or killall", "yield {until_job}"} {
		if !strings.Contains(p, want) {
			t.Errorf("no %q in\n%s", want, p)
		}
	}
	props := func(c Config) map[string]any {
		return yieldSpec(c).Function.Parameters["properties"].(map[string]any)
	}
	if _, ok := props(cfg)["until_job"]; !ok {
		t.Error("no until_job with a sandbox")
	}
	if _, ok := props(defaultConfig())["until_job"]; ok {
		t.Error("until_job without a sandbox")
	}
	h := &sbxHello{Images: []sbxImage{{ID: "base", Tools: []string{"git", " go ", "", "a,b", "rg"}}}}
	if got := strings.Join(h.imageTools("base"), " "); got != "git go rg" {
		t.Errorf("imageTools: %q", got)
	}
	if h.imageTools("other") != nil || (*sbxHello)(nil).imageTools("base") != nil {
		t.Error("an unknown image")
	}
}
