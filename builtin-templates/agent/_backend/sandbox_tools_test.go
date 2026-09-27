package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// sbxRun is a held coding conversation bound — by the tile itself — to a new
// sandbox at apps/cs (bindSbx first).
func sbxRun(t *testing.T, ag *Agent, name, egress string) (*Run, Config, *sbxSandbox) {
	t.Helper()
	box := mkSandbox(t, "apps/cs", "", sbxCreate{Name: name, Egress: egress})
	cfg := defaultConfig()
	cfg.Class = "coding"
	cfg.Features = map[string]bool{"streaming": false}
	b := sbxBindingOf(box)
	cfg.Sandbox, cfg.Attached = &b, []SandboxBinding{b}
	r, err := ag.startRunOpts(runOpts{Title: "t", Cfg: cfg, Hold: true})
	if err != nil {
		t.Fatal(err)
	}
	return r, cfg, box
}

func sbxBindingOf(box *sbxSandbox) SandboxBinding {
	return SandboxBinding{Ref: sandboxRef("apps/cs", box.ID), Cwd: box.Workdir, Name: box.Name,
		Manager: "Fake sandboxes", Image: box.Image.ID, Egress: box.Egress}
}

// tool runs one coding tool as the loop would, as call id.
func tool(t *testing.T, ag *Agent, r *Run, cfg Config, id, name string, args map[string]any) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(withToolCall(context.Background(), id), time.Duration(cfg.ToolTimeout)*time.Second)
	defer cancel()
	return ag.runTool(ctx, r, cfg, name, args)
}

func mustTool(t *testing.T, ag *Agent, r *Run, cfg Config, id, name string, args map[string]any) string {
	t.Helper()
	out, err := tool(t, ag, r, cfg, id, name, args)
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return out
}

func shortGrace(t *testing.T) {
	old := killGrace
	killGrace = 300 * time.Millisecond
	t.Cleanup(func() { killGrace = old })
}

func specLine(cfg Config, depth int) string {
	var names []string
	for _, s := range toolSpecs(cfg, depth, nil) {
		names = append(names, s.Function.Name)
	}
	return " " + strings.Join(names, " ") + " "
}

// The tools, the prompt section and the side-effect rule follow the class
// and the binding.
func TestSandboxToolsNeedClassAndBinding(t *testing.T) {
	b := SandboxBinding{Ref: "apps/cs|sb-1", Cwd: "/work/api", Name: "api-dev", Manager: "Coding sandboxes", Image: "base", Egress: "none"}
	other := SandboxBinding{Ref: "apps/cs|sb-2", Name: "db", Egress: "internet"}
	coding := Config{Class: "coding", Sandbox: &b, Attached: []SandboxBinding{b, other}}
	for _, c := range []struct {
		name string
		cfg  Config
		want bool
	}{
		{"bound coding", coding, true},
		{"a subagent", coding, true},
		{"unbound", Config{Class: "coding", Attached: []SandboxBinding{b}}, false},
		{"no sandbox toolset", Config{Class: "internal", Sandbox: &b, Attached: []SandboxBinding{b}}, false},
	} {
		depth := 0
		if c.name == "a subagent" {
			depth = 1
		}
		names := specLine(c.cfg, depth)
		for _, n := range []string{"bash", "bash_output", "bash_kill", "read", "write", "edit", "ls", "glob", "grep"} {
			if strings.Contains(names, " "+n+" ") != c.want {
				t.Errorf("%s: %s offered = %v", c.name, n, !c.want)
			}
		}
		if got := sandboxPrompt(c.cfg) != ""; got != c.want {
			t.Errorf("%s: # Sandbox in the prompt = %v", c.name, got)
		}
	}
	p := sandboxPrompt(coding)
	for _, want := range []string{"# Sandbox", `"api-dev"`, "Coding sandboxes", "image base", "no network at all", "/work/api", `"db" (apps/cs|sb-2; the public internet only)`} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, p)
		}
	}
	ag := newTestAgent(t, newTestDB(t))
	r, _ := ag.db.createRun("t", "", 0)
	run, _ := ag.db.getRun(r)
	if _, err := ag.runTool(context.Background(), run, Config{Class: "internal", Sandbox: &b}, "bash", map[string]any{"command": "true"}); err == nil ||
		!strings.Contains(err.Error(), "not available in this conversation's class") {
		t.Fatalf("a hallucinated call in a class without the toolset: %v", err)
	}
	// Approve mode parks bash only when the sandbox can reach out
	if sideEffect("bash", coding) {
		t.Fatal("bash in a sandbox with no egress is a side effect")
	}
	net := b
	net.Egress = "internet"
	netCfg := Config{Class: "coding", Sandbox: &net}
	for name, parks := range map[string]bool{"bash": true, "write": true, "edit": true, "bash_output": false, "bash_kill": false, "read": false, "ls": false, "glob": false, "grep": false} {
		if sideEffect(name, netCfg) != parks || sideEffect(name, coding) {
			t.Errorf("%s: parks with egress %v, without %v", name, sideEffect(name, netCfg), sideEffect(name, coding))
		}
	}
	if !sideEffect("xbin_call", Config{}) || sideEffect("file_write", Config{}) {
		t.Fatal("the other tools keep their rule")
	}
}

// Output keeps a short head and a long tail, valid UTF-8, no terminal noise.
func TestBashOutputShaping(t *testing.T) {
	s := newShaper()
	var all strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&all, "line %d é\n", i)
	}
	s.write([]byte(all.String()))
	out := s.String()
	if len(out) > bashMax || !utf8.ValidString(out) || !strings.HasPrefix(out, "line 0 é\n") ||
		!strings.HasSuffix(out, "line 4999 é\n") || !strings.Contains(out, "bytes elided") {
		t.Fatalf("shaped (%d bytes): %q … %q", len(out), out[:80], out[len(out)-80:])
	}
	if got := cleanOutput([]byte("\x1b[31mred\x1b[0m\nstep 10%\rstep 50%\rstep 100%\r\nok\r\n\x07bell\xff")); got != "red\nstep 100%\nok\nbell\uFFFD" {
		t.Fatalf("cleaned: %q", got)
	}
	gap := newShaper()
	gap.skip(100)
	gap.write([]byte("kept"))
	if got := gap.String(); !strings.HasPrefix(got, "… 100 earlier bytes are gone") || !strings.HasSuffix(got, "kept") {
		t.Fatalf("a ring that dropped the start: %q", got)
	}
	if fmtDur(14*time.Second) != "14s" || fmtDur(125*time.Second) != "2m05s" || fmtDur(3720*time.Second) != "1h02m" {
		t.Fatal("durations")
	}

	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, box := sbxRun(t, ag, "shape", "none")
	out = mustTool(t, ag, r, cfg, "c1", "bash", map[string]any{"command": "seq 1 100000; echo to-stderr >&2; exit 3"})
	if len(out) > bashMax+200 || !strings.HasPrefix(out, "1\n2\n3\n") || !strings.Contains(out, "bytes elided") ||
		!strings.Contains(out, "100000\nto-stderr\n[exit 3 · ") || !strings.HasSuffix(out, " · job 1]") {
		t.Fatalf("a long command (%d bytes): %q … %q", len(out), out[:40], out[len(out)-120:])
	}
	// it ran in the working directory, with the quiet environment
	out = mustTool(t, ag, r, cfg, "c2", "bash", map[string]any{"command": "pwd; echo $TERM $NO_COLOR $PAGER $GIT_TERMINAL_PROMPT"})
	if !strings.HasPrefix(out, box.Workdir+"\ndumb 1 cat 0\n[exit 0 · ") || !strings.Contains(out, "job 2]") {
		t.Fatalf("cwd and env: %q", out)
	}
	// the same call again once it ended (TestBashReusesOnlyTheSameRequest has
	// the rest): it runs again, as a job and an exec of its own
	conn, _ := sbxDial("apps/cs", "")
	before, _ := conn.ExecList(context.Background(), box.ID)
	if again := mustTool(t, ag, r, cfg, "c2", "bash", map[string]any{"command": "pwd; echo $TERM $NO_COLOR $PAGER $GIT_TERMINAL_PROMPT"}); !strings.Contains(again, "dumb 1 cat 0\n[exit 0") || !strings.HasSuffix(again, "job 3]") {
		t.Fatalf("re-issued: %q", again)
	}
	if after, _ := conn.ExecList(context.Background(), box.ID); len(after) != len(before)+1 {
		t.Fatalf("a re-issue after the end: %d → %d execs", len(before), len(after))
	}
	mustTool(t, ag, r, cfg, "c3", "bash", map[string]any{"command": "mkdir -p sub/dir"})
	if out := mustTool(t, ag, r, cfg, "c4", "bash", map[string]any{"command": "pwd", "cwd": "sub/dir"}); !strings.HasPrefix(out, filepath.Join(box.Workdir, "sub/dir")+"\n") {
		t.Fatalf("a relative cwd: %q", out)
	}
	if out := mustTool(t, ag, r, cfg, "c5", "bash", map[string]any{"command": "pwd", "cwd": "~"}); !strings.HasPrefix(out, box.Home+"\n") {
		t.Fatalf("~: %q", out)
	}
	if _, err := tool(t, ag, r, cfg, "c6", "bash", map[string]any{"command": "pwd", "cwd": "missing"}); err == nil || !strings.Contains(err.Error(), "work/missing") {
		t.Fatalf("a missing cwd: %v", err)
	}
	if j := ag.db.jobList(r.ID, false, 10); len(j) != 6 {
		t.Fatalf("a refused start leaves no job: %d", len(j))
	}
}

// At its timeout a command goes on as a job; bash_output follows it from
// where bash stopped, and says how it ended.
func TestBashTimeoutBecomesAJob(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, cfg, _ := sbxRun(t, ag, "slow", "none")
	out := mustTool(t, ag, r, cfg, "c1", "bash", map[string]any{"command": "echo early; sleep 1.5; echo late", "timeout_s": 1})
	if !strings.HasPrefix(out, "early\n[still running after 1s · job 1 — bash_output") {
		t.Fatalf("timed out: %q", out)
	}
	if j, _ := ag.db.job(r.ID, 1); j.FG || !j.running() || j.ReadOff != 6 {
		t.Fatalf("the job: %+v", j)
	}
	out = mustTool(t, ag, r, cfg, "c2", "bash_output", map[string]any{"job": 1, "wait_s": 10})
	if !strings.HasPrefix(out, "late\n[exit 0 · ") || !strings.HasSuffix(out, "· job 1]") {
		t.Fatalf("followed: %q", out)
	}
	if out := mustTool(t, ag, r, cfg, "c3", "bash_output", map[string]any{"job": 1}); !strings.HasPrefix(out, "(no new output)\n[exit 0") {
		t.Fatalf("nothing new: %q", out)
	}
	if out := mustTool(t, ag, r, cfg, "c4", "bash_output", map[string]any{"job": 1, "offset": 0}); !strings.HasPrefix(out, "early\nlate\n") {
		t.Fatalf("from the start: %q", out)
	}
	if _, err := tool(t, ag, r, cfg, "c5", "bash_output", map[string]any{"job": 9}); err == nil || !strings.Contains(err.Error(), "no job 9") {
		t.Fatalf("no such job: %v", err)
	}
	// background: a job at once; the per-tool timeout also turns bash into one
	out = mustTool(t, ag, r, cfg, "c6", "bash", map[string]any{"command": "sleep 0.3; echo bg", "background": true})
	if !strings.HasPrefix(out, "started job 2 in ") {
		t.Fatalf("background: %q", out)
	}
	short := cfg
	short.ToolTimeout = 4 // minus the 3 s margin: bash answers after about a second
	out = mustTool(t, ag, r, short, "c7", "bash", map[string]any{"command": "sleep 5"})
	if !strings.Contains(out, "still running after 1s · job 3") {
		t.Fatalf("the tool's own timeout: %q", out)
	}
	if out := mustTool(t, ag, r, cfg, "c8", "bash_output", map[string]any{"job": 2, "wait_s": 5}); !strings.HasPrefix(out, "bg\n[exit 0") {
		t.Fatalf("the background job: %q", out)
	}
	shortGrace(t)
	if out := mustTool(t, ag, r, cfg, "c9", "bash_kill", map[string]any{"job": 3}); !strings.HasPrefix(out, "job 3 stopped (killed by TERM)") {
		t.Fatalf("killed: %q", out)
	}
	if out := mustTool(t, ag, r, cfg, "c10", "bash_kill", map[string]any{"job": 3}); !strings.Contains(out, "isn't running") {
		t.Fatalf("killed twice: %q", out)
	}
}

// A conversation runs at most maxRunningJobs commands at once; the table is
// checked against the manager before a start is refused.
func TestBashJobCap(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	shortGrace(t)
	r, cfg, _ := sbxRun(t, ag, "busy", "none")
	for i := 1; i <= maxRunningJobs; i++ {
		cmd := "sleep 30"
		if i == 1 {
			cmd = "sleep 0.2" // ends by itself; the table doesn't know until it asks
		}
		mustTool(t, ag, r, cfg, fmt.Sprint("c", i), "bash", map[string]any{"command": cmd, "background": true})
	}
	time.Sleep(400 * time.Millisecond)
	out := mustTool(t, ag, r, cfg, "c9", "bash", map[string]any{"command": "true"})
	if !strings.Contains(out, "exit 0") || !strings.Contains(out, "job 9") {
		t.Fatalf("a finished job frees its place: %q", out)
	}
	mustTool(t, ag, r, cfg, "c10", "bash", map[string]any{"command": "sleep 30", "background": true})
	_, err := tool(t, ag, r, cfg, "c11", "bash", map[string]any{"command": "true"})
	if err == nil || !strings.Contains(err.Error(), "8 commands are already running") {
		t.Fatalf("over the cap: %v", err)
	}
	mustTool(t, ag, r, cfg, "c12", "bash_kill", map[string]any{"job": 10, "signal": "KILL"})
	if out := mustTool(t, ag, r, cfg, "c13", "bash", map[string]any{"command": "echo room"}); !strings.HasPrefix(out, "room\n") {
		t.Fatalf("after a kill: %q", out)
	}
}

func signals(m *sbxTestManager) []string {
	var out []string
	for _, c := range m.Calls() {
		if c.Method == "POST" && strings.HasSuffix(c.Path, "/signal") {
			out = append(out, c.Body)
		}
	}
	return out
}

// Interrupting the turn stops the command's process group: TERM, then KILL
// when it ignores TERM.
func TestBashInterruptSignalsTheGroup(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	useGlobalAgent(t, ag)
	m := bindSbx(t, "apps/cs")["apps/cs"]
	shortGrace(t)
	r, _, box := sbxRun(t, ag, "stubborn", "none")
	f := fakeOf(ag)
	f.on(forRun(r.ID, lastUser("go")), callTools(tc("b1", "bash", `{"command":"trap '' TERM; sleep 30; echo never"}`))).once()
	send(t, ag, r.ID, "go")
	waitFor(t, "the command", func() bool { return m.count("GET", "/sbx/sandboxes/"+box.ID+"/execs/e1/output") > 0 })
	if w := serve(handleInterrupt, "POST", "/runs/x/interrupt", r.ID, nil, ""); w.Code != 200 {
		t.Fatalf("interrupt: %d %s", w.Code, w.Body)
	}
	waitFor(t, "TERM then KILL to the group", func() bool {
		s := signals(m)
		return len(s) == 2 && strings.Contains(s[0], `"TERM"`) && strings.Contains(s[1], `"KILL"`) && strings.Contains(s[0], `"group":true`)
	})
	waitFor(t, "the job to be recorded as killed", func() bool {
		j, _ := ag.db.job(r.ID, 1)
		return j != nil && j.State == "killed"
	})
	waitStatus(t, ag.db, r.ID, statusIdle)
	if strings.Contains(fullText(ag.db, r.ID), "never") {
		t.Fatal("the command went on")
	}
}

// A backend handoff leaves the command running; the successor answers the
// call with the job it became, and bash_output resumes it from the start.
func TestBashHandoffResumesAsAJob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	a := fileAgent(t, path)
	m := bindSbx(t, "apps/cs")["apps/cs"]
	r, _, box := sbxRun(t, a, "long", "none")
	fa := fakeOf(a)
	fa.on(forRun(r.ID, lastUser("build")), callTools(tc("b1", "bash", `{"command":"echo started; sleep 1; echo finished"}`)))
	a.eng.Start()
	waitFor(t, "A to own", func() bool { a.eng.mu.Lock(); defer a.eng.mu.Unlock(); return a.eng.owned })
	send(t, a, r.ID, "build")
	waitFor(t, "A following the command", func() bool { return m.count("GET", "/sbx/sandboxes/"+box.ID+"/execs/e1/output") > 0 })

	b := fileAgent(t, path)
	fb := fakeOf(b)
	var lost atomic.Value
	fb.on(forRun(r.ID, lastIs("tool", "as job 1")), func(req LLMRequest) (LLMReply, error) {
		lost.Store(asString(req.Msgs[len(req.Msgs)-1].Content))
		return callTools(tc("o1", "bash_output", `{"job":1,"wait_s":10}`))(req)
	})
	fb.on(forRun(r.ID, lastIs("tool", "finished")), say("built"))
	b.eng.Start()
	a.eng.Shutdown(time.Second)
	waitFor(t, "B to finish the turn", func() bool { return strings.Contains(transcript(b.db, r.ID), "A:built") })
	if s, _ := lost.Load().(string); !strings.HasPrefix(s, "(no result: the backend restarted while this command ran. It went on in the sandbox as job 1") {
		t.Fatalf("the lost call's answer: %q", s)
	}
	if got := fullText(b.db, r.ID); !strings.Contains(got, "started\nfinished\n[exit 0 · ") {
		t.Fatalf("resumed from the start:\n%s", got)
	}
	if s := signals(m); len(s) != 0 {
		t.Fatalf("the handoff signalled the command: %v", s)
	}
	if j, _ := b.db.job(r.ID, 1); j.FG || j.State != "exited" {
		t.Fatalf("the job: %+v", j)
	}
	if got := b.db.lostResultText(r.ID, tc("x", "file_read", "{}")); got != toolLostToRestart {
		t.Fatalf("other tools keep the generic words: %q", got)
	}
	b.eng.Shutdown(time.Second)
}

// A rebind applies at the next turn: its commands, and its prompt.
func TestBashRebindAppliesNextTurn(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	bindSbx(t, "apps/cs")
	r, _, _ := sbxRun(t, ag, "first", "none")
	second := mkSandbox(t, "apps/cs", "", sbxCreate{Name: "second"})
	f := fakeOf(ag)
	var prompts []string
	f.on(forRun(r.ID, lastIs("user", "where")), func(req LLMRequest) (LLMReply, error) {
		prompts = append(prompts, asString(req.Msgs[0].Content))
		return callTools(tc(fmt.Sprint("w", len(prompts)), "bash", `{"command":"echo in $SANDBOX_NAME"}`))(req)
	})
	send(t, ag, r.ID, "where")
	waitFor(t, "the first turn", func() bool { return strings.Contains(transcript(ag.db, r.ID), "A:ok") })
	waitQuiet(t, ag) // over: a message sent before it ends joins it, with its config
	if err := storeBinding(ag.db, r.ID, func(c *Config) error { return attachSandbox(c, sbxBindingOf(second)) }); err != nil {
		t.Fatal(err)
	}
	send(t, ag, r.ID, "where now")
	waitFor(t, "the second turn", func() bool { return strings.Count(transcript(ag.db, r.ID), "A:ok") == 2 })
	got := fullText(ag.db, r.ID)
	if !strings.Contains(got, "in first\n[exit 0") || !strings.Contains(got, "in second\n[exit 0") || strings.Index(got, "in first") > strings.Index(got, "in second") {
		t.Fatalf("each turn's sandbox:\n%s", got)
	}
	if len(prompts) != 2 || !strings.Contains(prompts[0], `sandbox "first"`) || !strings.Contains(prompts[1], `sandbox "second"`) ||
		!strings.Contains(prompts[1], `Also attached: "first"`) {
		t.Fatalf("the prompts: %q", prompts)
	}
}
