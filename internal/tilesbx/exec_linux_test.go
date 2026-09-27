//go:build linux

package tilesbx

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// The fake launcher's sessions are host processes (agentcore over the real
// factory), so these drive the whole exec path — the routes, the agent
// client, the rings, the teardown — with sh.

// startedEnv is a fake runtime with sandbox sb-1 created and running.
func startedEnv(t *testing.T, mut ...func(*Options)) *fakeEnv {
	t.Helper()
	fe := newFakeEnv(t, mut...)
	fe.create(ns("sb-1"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	return fe
}

// exec starts an exec through the route (want: 201).
func (fe *fakeEnv) exec(name string, body map[string]any) Exec {
	fe.t.Helper()
	w := fe.do(mgr, "POST", "/sandboxes/"+name+"/execs", body)
	fe.want(w, http.StatusCreated, "")
	return decodeAs[Exec](fe.t, w.Body.Bytes())
}

func decodeAs[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return v
}

// execGet is the exec now.
func (fe *fakeEnv) execGet(name, id string) Exec {
	fe.t.Helper()
	w := fe.do(mgr, "GET", "/sandboxes/"+name+"/execs/"+id, nil)
	fe.want(w, http.StatusOK, "")
	return decodeAs[Exec](fe.t, w.Body.Bytes())
}

// waitEnded waits for an exec's end.
func (fe *fakeEnv) waitEnded(name, id string) Exec {
	fe.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		x := fe.execGet(name, id)
		if x.State != ExecRunning {
			return x
		}
		if time.Now().After(deadline) {
			fe.t.Fatalf("exec %s never ended: %+v", id, x)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// output reads a chunk.
func (fe *fakeEnv) output(name, id, query string) OutputChunk {
	fe.t.Helper()
	w := fe.do(mgr, "GET", "/sandboxes/"+name+"/execs/"+id+"/output?"+query, nil)
	fe.want(w, http.StatusOK, "")
	return decodeAs[OutputChunk](fe.t, w.Body.Bytes())
}

// waitOutput reads the exec's output from 0 until it holds want.
func (fe *fakeEnv) waitOutput(name, id, want string) string {
	fe.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c := fe.output(name, id, "waitMs=200")
		if strings.Contains(c.Data, want) {
			return c.Data
		}
		if time.Now().After(deadline) {
			fe.t.Fatalf("exec %s's output %q never held %q", id, c.Data, want)
		}
	}
}

// procGone reports a process gone (or a zombie nobody reaped yet).
func procGone(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	i := bytes.LastIndexByte(b, ')')
	f := strings.Fields(string(b[i+1:]))
	return len(f) > 0 && f[0] == "Z"
}

func waitGone(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !procGone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d is still there", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var execIDFmt = regexp.MustCompile(`^[0-9a-f]{6}-[0-9]{1,12}$`)

// An exec runs in its sandbox — started for it when autoStart allows —
// with one combined output stream read by byte offset, the environment
// xbind builds, its end recorded; ids carry the boot prefix.
func TestExecs(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("sb-1")) // stopped: the exec starts it (autoStart)
	x := fe.exec("sb-1", map[string]any{"argv": []string{"sh", "-c", "echo out; echo err >&2; exit 3"}, "label": "build"})
	if !execIDFmt.MatchString(x.ID) || !strings.HasPrefix(x.ID, fe.m.boot+"-") || x.State != ExecRunning || x.Label != "build" || x.Cwd != "/" {
		t.Fatalf("the exec: %+v", x)
	}
	if fe.get("sb-1").State != StateRunning {
		t.Fatal("the exec didn't start its sandbox")
	}
	x = fe.waitEnded("sb-1", x.ID)
	if x.State != ExecExited || x.ExitCode == nil || *x.ExitCode != 3 || x.Signal != "" || x.Ended == nil || x.Total != 8 {
		t.Fatalf("ended: %+v", x)
	}
	if c := fe.output("sb-1", x.ID, ""); c.Data != "out\nerr\n" || c.Start != 0 || c.End != 8 || c.Total != 8 || c.State != ExecExited || *c.ExitCode != 3 {
		t.Fatalf("the output: %+v", c)
	}
	if c := fe.output("sb-1", x.ID, "since=4&encoding=base64"); c.Data != base64.StdEncoding.EncodeToString([]byte("err\n")) || c.Start != 4 || c.Encoding != "base64" {
		t.Fatalf("base64 from 4: %+v", c)
	}
	fe.want(fe.do(mgr, "GET", "/sandboxes/sb-1/execs/"+x.ID+"/output?since=9", nil), http.StatusBadRequest, RefInvalid)
	fe.want(fe.do(mgr, "GET", "/sandboxes/sb-1/execs/"+x.ID+"/output?encoding=hex", nil), http.StatusBadRequest, RefInvalid)

	// the environment: xbind's, defaults.env's, the command's
	env := fe.exec("sb-1", map[string]any{"argv": []string{"env"}, "env": map[string]string{"FOO": "bar", "IN_SANDBOX": "0"}})
	fe.waitEnded("sb-1", env.ID)
	out := fe.output("sb-1", env.ID, "").Data
	for _, want := range []string{"IN_SANDBOX=1", "SANDBOX_ID=sb-1", "SANDBOX_NAME=sb-1", "HOME=/root", "FOO=bar", "PATH="} {
		if !strings.Contains("\n"+out, "\n"+want) {
			t.Errorf("no %s in the environment:\n%s", want, out)
		}
	}
	if strings.Count(out, "\n") > 7 {
		t.Errorf("more than xbind's environment:\n%s", out)
	}

	// refusals: nothing is started, nothing is listed
	before := len(fe.execList("sb-1"))
	for _, c := range []struct {
		body    map[string]any
		status  int
		refusal string
	}{
		{map[string]any{"argv": []string{"true"}, "env": map[string]string{"XBIN_TOKEN": "x"}}, 400, RefInvalid},
		{map[string]any{"argv": []string{"true"}, "cmd": "true"}, 400, RefInvalid},
		{map[string]any{}, 400, RefInvalid},
		{map[string]any{"argv": []string{"true"}, "cwd": "/no/such/dir"}, 400, RefInvalid},
		{map[string]any{"argv": []string{"true"}, "cwd": "rel"}, 400, RefInvalid},
		{map[string]any{"argv": []string{"/no/such/program"}}, 400, RefInvalid},
		{map[string]any{"argv": []string{"true"}, "timeoutMs": -1}, 400, RefInvalid},
		{map[string]any{"argv": []string{strings.Repeat("x", argvEnvMax)}}, 413, RefTooLarge},
	} {
		fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs", c.body), c.status, c.refusal)
	}
	if n := len(fe.execList("sb-1")); n != before {
		t.Fatalf("refused execs were listed: %d → %d", before, n)
	}
	if n := fe.get("sb-1").ExecsRunning; n != 0 {
		t.Fatalf("execsRunning %d after the refusals", n)
	}

	// a cmd runs through the sandbox's shell
	// (a login shell: HOME is the host's /root here, whose profile it can't read)
	sh := fe.exec("sb-1", map[string]any{"cmd": "echo $((6*7))", "env": map[string]string{"HOME": t.TempDir()}})
	fe.waitEnded("sb-1", sh.ID)
	if c := fe.output("sb-1", sh.ID, ""); c.Data != "42\n" {
		t.Fatalf("a cmd: %q", c.Data)
	}

	// ids: another boot's is lost, this boot's unknown one not found
	other := "ffffff-1"
	if fe.m.boot == "ffffff" {
		other = "000000-1"
	}
	fe.want(fe.do(mgr, "GET", "/sandboxes/sb-1/execs/"+other, nil), http.StatusGone, RefLost)
	fe.want(fe.do(mgr, "GET", "/sandboxes/sb-1/execs/"+other+"/output", nil), http.StatusGone, RefLost)
	fe.want(fe.do(mgr, "GET", "/sandboxes/sb-1/execs/"+fe.m.boot+"-999999", nil), http.StatusNotFound, RefNotFound)
	fe.want(fe.do(mgr, "GET", "/sandboxes/sb-1/execs/nope", nil), http.StatusBadRequest, RefInvalid)

	// DELETE forgets it
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/sb-1/execs/"+x.ID, nil), http.StatusNoContent, "")
	fe.want(fe.do(mgr, "GET", "/sandboxes/sb-1/execs/"+x.ID, nil), http.StatusNotFound, RefNotFound)

	// an admin never reaches an exec
	fe.want(fe.do(admin, "GET", "/sandboxes/sb-1/execs?tile=apps/mgr", nil), http.StatusForbidden, RefNotAllowed)
}

func (fe *fakeEnv) execList(name string) []Exec {
	fe.t.Helper()
	w := fe.do(mgr, "GET", "/sandboxes/"+name+"/execs", nil)
	fe.want(w, http.StatusOK, "")
	return decodeAs[struct{ Execs []Exec }](fe.t, w.Body.Bytes()).Execs
}

// A clientId repeat answers the same exec (200); the same id for another
// request is 409 exists — also while the first is still starting.
func TestExecClientID(t *testing.T) {
	fe := startedEnv(t)
	body := map[string]any{"argv": []string{"sleep", "30"}, "clientId": "c-1"}
	var wg sync.WaitGroup
	codes := make([]int, 4)
	ids := make([]string, 4)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := fe.do(mgr, "POST", "/sandboxes/sb-1/execs", body)
			codes[i] = w.Code
			var x Exec
			_ = json.Unmarshal(w.Body.Bytes(), &x)
			ids[i] = x.ID
		}(i)
	}
	wg.Wait()
	created := 0
	for i, c := range codes {
		switch {
		case c == http.StatusCreated:
			created++
		case c != http.StatusOK:
			t.Fatalf("a repeat answered %d", c)
		}
		if ids[i] != ids[0] {
			t.Fatalf("repeats answered other execs: %v", ids)
		}
	}
	if created != 1 || len(fe.execList("sb-1")) != 1 {
		t.Fatalf("%d created, %d listed", created, len(fe.execList("sb-1")))
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs", map[string]any{"argv": []string{"sleep", "31"}, "clientId": "c-1"}), http.StatusConflict, RefExists)
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/sb-1/execs/"+ids[0], nil), http.StatusNoContent, "")
	// forgotten: the clientId is free again
	if x := fe.exec("sb-1", map[string]any{"argv": []string{"true"}, "clientId": "c-1"}); x.ID == ids[0] {
		t.Fatal("a forgotten exec answered its clientId")
	}
}

// A long-poll answers when output arrives, and — with nothing more to
// come — as soon as the exec ends.
func TestExecOutputLongPoll(t *testing.T) {
	fe := startedEnv(t)
	x := fe.exec("sb-1", map[string]any{"argv": []string{"sh", "-c", "sleep 0.3; printf abc; sleep 0.4"}})
	start := time.Now()
	c := fe.output("sb-1", x.ID, "since=0&waitMs=10000")
	if d := time.Since(start); c.Data != "abc" || c.End != 3 || c.State != ExecRunning || d > 5*time.Second {
		t.Fatalf("the first long-poll after %s: %+v", d, c)
	}
	start = time.Now()
	c = fe.output("sb-1", x.ID, "since=3&waitMs=10000")
	if d := time.Since(start); c.Data != "" || c.State != ExecExited || c.ExitCode == nil || *c.ExitCode != 0 || d > 5*time.Second {
		t.Fatalf("the end's long-poll after %s: %+v", d, c)
	}
	// no wait: at once
	start = time.Now()
	if c := fe.output("sb-1", x.ID, "since=3"); time.Since(start) > time.Second || c.Start != 3 || c.End != 3 {
		t.Fatalf("no wait: %+v", c)
	}
	// max bounds a read; a character is never split in text
	u := fe.exec("sb-1", map[string]any{"argv": []string{"printf", "a€b"}})
	fe.waitEnded("sb-1", u.ID)
	if c := fe.output("sb-1", u.ID, "max=2"); c.Data != "a" || c.End != 1 {
		t.Fatalf("a split character: %+v", c)
	}
	if c := fe.output("sb-1", u.ID, "since=1&max=3"); c.Data != "€" || c.End != 4 {
		t.Fatalf("the character whole: %+v", c)
	}
}

// stdin: a stdin: true exec takes the raw body, eof closes it; an exec
// without stdin refuses it, and one that ended is state.
func TestExecStdin(t *testing.T) {
	fe := startedEnv(t)
	x := fe.exec("sb-1", map[string]any{"argv": []string{"cat"}, "stdin": true})
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs/"+x.ID+"/stdin", "hello"), http.StatusNoContent, "")
	fe.waitOutput("sb-1", x.ID, "hello")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs/"+x.ID+"/stdin?eof=1", " world"), http.StatusNoContent, "")
	if x := fe.waitEnded("sb-1", x.ID); x.State != ExecExited || *x.ExitCode != 0 {
		t.Fatalf("after eof: %+v", x)
	}
	if c := fe.output("sb-1", x.ID, ""); c.Data != "hello world" {
		t.Fatalf("cat: %q", c.Data)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs/"+x.ID+"/stdin", "late"), http.StatusConflict, RefState)
	no := fe.exec("sb-1", map[string]any{"argv": []string{"sleep", "30"}})
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs/"+no.ID+"/stdin", "x"), http.StatusBadRequest, RefInvalid)
	// past stdinMax
	big := fe.exec("sb-1", map[string]any{"argv": []string{"sh", "-c", "cat >/dev/null"}, "stdin": true})
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs/"+big.ID+"/stdin", strings.Repeat("x", stdinMax+1)), http.StatusRequestEntityTooLarge, RefTooLarge)
}

// grandchild runs sh → sh → sleep and returns the exec and the sleep's pid:
// a foreground child, so a non-interactive shell leaves its SIGINT alone.
func grandchild(t *testing.T, fe *fakeEnv) (Exec, int) {
	t.Helper()
	x := fe.exec("sb-1", map[string]any{"argv": []string{"sh", "-c", `sh -c 'echo pid $$; exec sleep 100'; echo after`}})
	out := fe.waitOutput("sb-1", x.ID, "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(out, "pid ")))
	if err != nil {
		t.Fatalf("no pid in %q", out)
	}
	return x, pid
}

// A signal body without group is a group signal: a forwarded
// {"signal":"INT"} reaches the grandchild. group:false reaches the leader
// only.
func TestExecSignalGroupDefault(t *testing.T) {
	fe := startedEnv(t)
	x, pid := grandchild(t, fe)
	b := fe.box("sb-1")
	if n := b.act.inflight.Load(); n != 1 { // a running non-tty exec holds the idle stop off (§7)
		t.Fatalf("a running exec's hold: %d", n)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs/"+x.ID+"/signal", `{"signal":"INT"}`), http.StatusNoContent, "")
	waitGone(t, pid, 5*time.Second)
	fe.waitEnded("sb-1", x.ID)
	for deadline := time.Now().Add(5 * time.Second); b.act.inflight.Load() != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("an ended exec still holds the idle stop: %d", b.act.inflight.Load())
		}
	}

	y, pid2 := grandchild(t, fe)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs/"+y.ID+"/signal", `{"signal":"TERM","group":false}`), http.StatusNoContent, "")
	time.Sleep(300 * time.Millisecond)
	if procGone(pid2) {
		t.Fatal("group:false reached the grandchild")
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs/"+y.ID+"/signal", `{"signal":"KILL"}`), http.StatusNoContent, "")
	waitGone(t, pid2, 5*time.Second)
	if y := fe.waitEnded("sb-1", y.ID); y.State != ExecKilled {
		t.Fatalf("after KILL: %+v", y)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs/"+y.ID+"/signal", `{"signal":"KILL"}`), http.StatusConflict, RefState)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs/"+y.ID+"/signal", `{"signal":"USR1"}`), http.StatusBadRequest, RefInvalid)
}

// An exec's timeout sends TERM to its group: it ends killed.
func TestExecTimeout(t *testing.T) {
	fe := startedEnv(t)
	x, pid := func() (Exec, int) {
		x := fe.exec("sb-1", map[string]any{"argv": []string{"sh", "-c", `sleep 100 & echo pid $!; wait`}, "timeoutMs": 300})
		out := fe.waitOutput("sb-1", x.ID, "\n")
		pid, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(out, "pid ")))
		return x, pid
	}()
	x = fe.waitEnded("sb-1", x.ID)
	if x.State != ExecKilled || x.Signal != "TERM" || x.ExitCode != nil {
		t.Fatalf("timed out: %+v", x)
	}
	waitGone(t, pid, 5*time.Second)
}

// A stop — whatever ends the run — ends the execs running as killed
// (signal KILL, exitCode null); their records and output stay.
func TestExecKilledByStop(t *testing.T) {
	fe := startedEnv(t)
	x := fe.exec("sb-1", map[string]any{"argv": []string{"sh", "-c", "echo before; sleep 100"}})
	fe.waitOutput("sb-1", x.ID, "before")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/stop", nil), http.StatusOK, "")
	// the stop's answer already says so
	if x := fe.execGet("sb-1", x.ID); x.State != ExecKilled || x.Signal != "KILL" || x.ExitCode != nil || x.Ended == nil {
		t.Fatalf("after the stop: %+v", x)
	}
	c := fe.output("sb-1", x.ID, "waitMs=5000")
	if c.Data != "before\n" || c.State != ExecKilled || c.Signal != "KILL" || c.End != c.Total {
		t.Fatalf("its output after the stop: %+v", c)
	}
	if in := fe.get("sb-1"); in.ExecsRunning != 0 {
		t.Fatalf("execsRunning after the stop: %d", in.ExecsRunning)
	}
	// kept across the next run; the sandbox ended on its own ends them too
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	if len(fe.execList("sb-1")) != 1 {
		t.Fatal("the stop forgot the exec")
	}
	y := fe.exec("sb-1", map[string]any{"argv": []string{"sleep", "100"}})
	fe.l.last().Kill() // the agent dies
	fe.waitState("sb-1", StateStopped)
	if y := fe.waitEnded("sb-1", y.ID); y.State != ExecKilled || y.Signal != "KILL" {
		t.Fatalf("its sandbox died: %+v", y)
	}
	// a deleted sandbox's execs go, and their rings with them
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/sb-1", nil), http.StatusNoContent, "")
	fe.m.ringsMu.Lock()
	rb := fe.m.rings[fe.k.Tile]
	fe.m.ringsMu.Unlock()
	if rb.held() != 0 {
		t.Fatalf("the rings hold %d bytes after the delete", rb.held())
	}
}

// execsRunning caps a sandbox's commands: execs and runs together.
func TestExecsRunningCap(t *testing.T) {
	fe := startedEnv(t)
	var ids []string
	for i := 0; i < execsRunningMax; i++ {
		ids = append(ids, fe.exec("sb-1", map[string]any{"argv": []string{"sleep", "100"}}).ID)
	}
	if n := fe.get("sb-1").ExecsRunning; n != execsRunningMax {
		t.Fatalf("execsRunning %d", n)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs", map[string]any{"argv": []string{"true"}}), http.StatusTooManyRequests, RefLimit)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/run", map[string]any{"argv": []string{"true"}}), http.StatusTooManyRequests, RefLimit)
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/sb-1/execs/"+ids[0], nil), http.StatusNoContent, "")
	deadline := time.Now().Add(10 * time.Second)
	for fe.get("sb-1").ExecsRunning == execsRunningMax {
		if time.Now().After(deadline) {
			t.Fatal("the killed exec still counts")
		}
		time.Sleep(10 * time.Millisecond)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/run", map[string]any{"argv": []string{"true"}}), http.StatusOK, "")
}

// run: the contract's result — exit code, streams apart or merged, head and
// tail past maxOutput, stdin, a timeout that kills the group.
func TestRun(t *testing.T) {
	fe := startedEnv(t)
	run := func(body map[string]any) RunResult {
		t.Helper()
		w := fe.do(mgr, "POST", "/sandboxes/sb-1/run", body)
		fe.want(w, http.StatusOK, "")
		return decodeAs[RunResult](t, w.Body.Bytes())
	}
	r := run(map[string]any{"argv": []string{"sh", "-c", "printf out; printf err >&2; exit 2"}})
	if r.ExitCode == nil || *r.ExitCode != 2 || r.Signal != "" || r.TimedOut || r.Stdout.Head != "out" || r.Stderr.Head != "err" || r.Output != nil {
		t.Fatalf("a run: %+v %+v %+v", r, r.Stdout, r.Stderr)
	}
	home := map[string]string{"HOME": t.TempDir()} // a login shell: not the host's /root
	r = run(map[string]any{"cmd": "printf out; printf err >&2", "merge": true, "env": home})
	if r.Output == nil || r.Output.Head != "outerr" || r.Stdout != nil || *r.ExitCode != 0 {
		t.Fatalf("merged: %+v", r)
	}
	r = run(map[string]any{"cmd": "head -c 100 /dev/zero | tr '\\0' x", "maxOutput": 16, "env": home})
	if o := r.Stdout; o.Head != "xxxx" || o.Tail != strings.Repeat("x", 12) || o.Elided != 84 || o.Bytes != 100 {
		t.Fatalf("maxOutput 16: %+v", o)
	}
	r = run(map[string]any{"argv": []string{"cat"}, "stdin": "fed in"})
	if r.Stdout.Head != "fed in" {
		t.Fatalf("stdin: %+v", r.Stdout)
	}
	start := time.Now()
	r = run(map[string]any{"argv": []string{"sh", "-c", "sleep 100 & echo $!; sleep 100"}, "timeoutMs": 300})
	if !r.TimedOut || r.Signal != "TERM" || r.ExitCode != nil || time.Since(start) > 5*time.Second {
		t.Fatalf("a timeout: %+v", r)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(r.Stdout.Head))
	waitGone(t, pid, 5*time.Second)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/run", map[string]any{"argv": []string{"true"}, "cwd": "/no/such"}), http.StatusBadRequest, RefInvalid)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/run", map[string]any{"argv": []string{"/no/such/program"}}), http.StatusBadRequest, RefInvalid)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/run", map[string]any{"argv": []string{"cat"}, "stdin": strings.Repeat("x", stdinMax+1)}), http.StatusRequestEntityTooLarge, RefTooLarge)
	if n := len(fe.execList("sb-1")); n != 0 {
		t.Fatalf("runs were listed: %d", n)
	}
}

// A run is held against the idle stop while it runs, and a caller that
// hangs up has its group killed.
func TestRunHangUp(t *testing.T) {
	fe := startedEnv(t)
	srv := fe.server(mgr)
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(context.Background())
	body := fmt.Sprintf(`{"argv":["sh","-c","sleep 100 & echo $! > %s; sleep 100"]}`, pidFile)
	req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/sandboxes/sb-1/run", strings.NewReader(body))
	done := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()
	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for pid == 0 {
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		if time.Now().After(deadline) {
			t.Fatal("the run never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	b := fe.box("sb-1")
	if b.act.inflight.Load() != 1 || fe.get("sb-1").ExecsRunning != 1 {
		t.Fatalf("a run in flight: inflight %d, execsRunning %d", b.act.inflight.Load(), fe.get("sb-1").ExecsRunning)
	}
	cancel()
	<-done
	waitGone(t, pid, 5*time.Second)
	deadline = time.Now().Add(10 * time.Second)
	for b.act.inflight.Load() != 0 || fe.get("sb-1").ExecsRunning != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("after the hang-up: inflight %d, execsRunning %d", b.act.inflight.Load(), fe.get("sb-1").ExecsRunning)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// box is the sandbox's live state.
func (fe *fakeEnv) box(name string) *box {
	fe.m.mu.Lock()
	defer fe.m.mu.Unlock()
	return fe.m.live[fe.k][name]
}

// server serves the routes over HTTP as p (WebSockets need a real conn).
func (fe *fakeEnv) server(p auth.Principal) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fe.mux.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	}))
	fe.t.Cleanup(srv.Close)
	return srv
}

// A control line the agent never reads is bounded: a signal or an exec
// answers unavailable instead of hanging its request.
func TestControlSendsBounded(t *testing.T) {
	old := ctlSendWait
	ctlSendWait = 200 * time.Millisecond
	defer func() { ctlSendWait = old }()
	ours, theirs := net.Pipe() // unbuffered: nothing gets through while nobody reads
	defer theirs.Close()
	a := &agentClient{ctl: proto.NewConn(ours, nil), sessions: map[int]*agentSession{}, next: firstSession,
		ready: make(chan struct{}), gone: make(chan struct{}), logf: func(string, ...any) {}}
	defer a.Close()
	for name, send := range map[string]func() error{
		"signal": func() error { return a.Signal(2, 9, true) },
		"resize": func() error { return a.Resize(2, 24, 80) },
	} {
		done := make(chan error, 1)
		go func() { done <- send() }()
		select {
		case err := <-done:
			var e *Error
			if err == nil || !asError(err, &e) || e.Refusal != RefUnavailable {
				t.Fatalf("%s: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s blocked past its bound", name)
		}
	}
}

func asError(err error, e **Error) bool {
	x, ok := err.(*Error)
	*e = x
	return ok
}
