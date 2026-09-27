//go:build linux && integration

package tilesbx

// Live commands: the exec suite (WP-17) on real tile sandboxes, driven
// through the routes as the manager, with the static probe for a program
// (a minimal lower has no shell). testLiveExecs takes the mode, so VM
// mode's tests (WP-16) run the same suite: testLiveExecs(t, le, ModeVM).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

const probeBin = "/opt/probe/probe"

func TestLiveExecs(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	bin := liveBinaries(t)
	t.Run("minimal", func(t *testing.T) {
		t.Setenv("XBIN_FUSE_OVERLAYFS", "none") // the kernel overlay
		testLiveExecs(t, newLiveEnv(t, bin, ""), ModeNamespace, false)
	})
	t.Run("rootfs", func(t *testing.T) {
		rootfs := os.Getenv("XBIN_TEST_ROOTFS")
		if rootfs == "" {
			rootfs, _ = filepath.Abs("../../.rootfs")
		}
		if _, err := os.Stat(filepath.Join(rootfs, "bin", "sh")); err != nil {
			t.Skip("no rootfs ($XBIN_TEST_ROOTFS or .rootfs)")
		}
		fuse := findFuseOverlayfs()
		if fuse == "" {
			fuse = "none"
		}
		t.Setenv("XBIN_FUSE_OVERLAYFS", fuse)
		testLiveExecs(t, newLiveEnv(t, bin, rootfs), ModeNamespace, true)
	})
}

// liveRun runs a command through the route (want 200).
func (le *liveEnv) liveRun(name string, body map[string]any) RunResult {
	le.t.Helper()
	w := le.do(mgr, "POST", "/sandboxes/"+name+"/run", body)
	le.want(w, http.StatusOK, "")
	return decodeAs[RunResult](le.t, w.Body.Bytes())
}

func (le *liveEnv) liveExec(name string, body map[string]any) Exec {
	le.t.Helper()
	w := le.do(mgr, "POST", "/sandboxes/"+name+"/execs", body)
	le.want(w, http.StatusCreated, "")
	return decodeAs[Exec](le.t, w.Body.Bytes())
}

func (le *liveEnv) liveExecGet(name, id string) Exec {
	le.t.Helper()
	w := le.do(mgr, "GET", "/sandboxes/"+name+"/execs/"+id, nil)
	le.want(w, http.StatusOK, "")
	return decodeAs[Exec](le.t, w.Body.Bytes())
}

func (le *liveEnv) liveEnded(name, id string) Exec {
	le.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if x := le.liveExecGet(name, id); x.State != ExecRunning {
			return x
		}
		if time.Now().After(deadline) {
			le.t.Fatalf("exec %s never ended", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (le *liveEnv) liveOutput(name, id, query string) OutputChunk {
	le.t.Helper()
	w := le.do(mgr, "GET", "/sandboxes/"+name+"/execs/"+id+"/output?"+query, nil)
	le.want(w, http.StatusOK, "")
	return decodeAs[OutputChunk](le.t, w.Body.Bytes())
}

// liveChild waits for a tree's "child <pid>" line.
func (le *liveEnv) liveChild(name, id string) int {
	le.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		c := le.liveOutput(name, id, "waitMs=500")
		if f := strings.Fields(c.Data); len(f) >= 2 && f[0] == "child" {
			pid, _ := strconv.Atoi(f[1])
			return pid
		}
		if time.Now().After(deadline) {
			le.t.Fatalf("no child line: %q", c.Data)
		}
	}
}

// liveGone waits until pid is gone inside the sandbox.
func (le *liveEnv) liveGone(name string, pid int) {
	le.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r := le.liveRun(name, map[string]any{"argv": []string{probeBin, "alive", strconv.Itoa(pid)}})
		if strings.TrimSpace(r.Stdout.Head) == "gone" {
			return
		}
		if time.Now().After(deadline) {
			le.t.Fatalf("process %d is still alive in %s", pid, name)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (le *liveEnv) server(p auth.Principal) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		le.mux.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	}))
	le.t.Cleanup(srv.Close)
	return srv
}

func testLiveExecs(t *testing.T, le *liveEnv, mode string, hasShell bool) {
	le.m.deps.Users = fakeUsers{"alice": true}
	le.create(map[string]any{"name": "ex-1", "mode": mode, "mounts": []any{probeMount}})

	t.Run("an exec starts its sandbox; its environment is xbind's", func(t *testing.T) {
		le.t = t
		r := le.liveRun("ex-1", map[string]any{"argv": []string{probeBin, "env"}, "env": map[string]string{"FOO": "bar"}})
		if r.ExitCode == nil || *r.ExitCode != 0 {
			t.Fatalf("env: %+v %+v", r, r.Stderr)
		}
		out := "\n" + r.Stdout.Head
		for _, want := range []string{"IN_SANDBOX=1", "SANDBOX_ID=ex-1", "SANDBOX_NAME=ex-1", "HOME=/root", "FOO=bar", "PATH=/"} {
			if !strings.Contains(out, "\n"+want) {
				t.Errorf("no %s in\n%s", want, out)
			}
		}
		if strings.Contains(out, "XBIN_") {
			t.Errorf("an xbin variable inside:\n%s", out)
		}
		if in, _ := le.m.infoOf(le.k, "ex-1"); in.State != StateRunning {
			t.Fatalf("the sandbox: %+v", in)
		}
		le.want(le.do(mgr, "POST", "/sandboxes/ex-1/run", map[string]any{"argv": []string{probeBin, "echo"}, "cwd": "/no/such"}), http.StatusBadRequest, RefInvalid)
		le.want(le.do(mgr, "POST", "/sandboxes/ex-1/run", map[string]any{"argv": []string{"/no/such/program"}}), http.StatusBadRequest, RefInvalid)
	})

	t.Run("20 concurrent execs", func(t *testing.T) {
		le.t = t
		var wg sync.WaitGroup
		errs := make(chan string, 20)
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				marker := fmt.Sprintf("marker-%d", i)
				var x Exec
				for try := 0; ; try++ { // past execsRunning (16) is 429: wait for a slot
					w := le.do(mgr, "POST", "/sandboxes/ex-1/execs", map[string]any{"argv": []string{probeBin, "echo", marker}})
					if w.Code == http.StatusTooManyRequests && try < 200 {
						time.Sleep(25 * time.Millisecond)
						continue
					}
					if w.Code != http.StatusCreated {
						errs <- fmt.Sprintf("exec %d: %d %s", i, w.Code, w.Body)
						return
					}
					_ = json.Unmarshal(w.Body.Bytes(), &x)
					break
				}
				for deadline := time.Now().Add(30 * time.Second); ; {
					c := le.liveOutputQuiet("ex-1", x.ID)
					if c.State != ExecRunning && c.End == c.Total {
						if c.Data != marker+"\n" || c.State != ExecExited || c.ExitCode == nil || *c.ExitCode != 0 {
							errs <- fmt.Sprintf("exec %d: %+v", i, c)
						}
						return
					}
					if time.Now().After(deadline) {
						errs <- fmt.Sprintf("exec %d never ended", i)
						return
					}
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			t.Error(e)
		}
	})

	t.Run("a run's timeout kills the group, grandchild included", func(t *testing.T) {
		le.t = t
		start := time.Now()
		r := le.liveRun("ex-1", map[string]any{"argv": []string{probeBin, "tree", "100s"}, "timeoutMs": 500})
		if !r.TimedOut || r.Signal != "TERM" || r.ExitCode != nil || time.Since(start) > 10*time.Second {
			t.Fatalf("a timeout: %+v", r)
		}
		f := strings.Fields(r.Stdout.Head)
		if len(f) < 2 {
			t.Fatalf("no child: %q", r.Stdout.Head)
		}
		pid, _ := strconv.Atoi(f[1])
		le.liveGone("ex-1", pid)
	})

	t.Run("a hang-up kills the group", func(t *testing.T) {
		le.t = t
		srv := le.server(mgr)
		ctx, cancel := context.WithCancel(context.Background())
		body := fmt.Sprintf(`{"argv":[%q,"tree","100s","/tmp/hangup.pid"]}`, probeBin)
		req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/sandboxes/ex-1/run", strings.NewReader(body))
		done := make(chan struct{})
		go func() {
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
			}
			close(done)
		}()
		var pid int
		for deadline := time.Now().Add(20 * time.Second); pid == 0; {
			r := le.liveRun("ex-1", map[string]any{"argv": []string{probeBin, "cat", "/tmp/hangup.pid"}})
			pid, _ = strconv.Atoi(strings.TrimSpace(r.Stdout.Head))
			if time.Now().After(deadline) {
				t.Fatal("the run never started")
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
		<-done
		le.liveGone("ex-1", pid)
	})

	t.Run(`{"signal":"INT"} reaches a grandchild`, func(t *testing.T) {
		le.t = t
		x := le.liveExec("ex-1", map[string]any{"argv": []string{probeBin, "tree", "100s"}})
		pid := le.liveChild("ex-1", x.ID)
		le.want(le.do(mgr, "POST", "/sandboxes/ex-1/execs/"+x.ID+"/signal", `{"signal":"INT"}`), http.StatusNoContent, "")
		if x := le.liveEnded("ex-1", x.ID); x.State != ExecKilled || x.Signal != "INT" {
			t.Fatalf("after INT: %+v", x)
		}
		le.liveGone("ex-1", pid)
	})

	t.Run("stdin", func(t *testing.T) {
		le.t = t
		x := le.liveExec("ex-1", map[string]any{"argv": []string{probeBin, "copy"}, "stdin": true})
		le.want(le.do(mgr, "POST", "/sandboxes/ex-1/execs/"+x.ID+"/stdin", "fed "), http.StatusNoContent, "")
		le.want(le.do(mgr, "POST", "/sandboxes/ex-1/execs/"+x.ID+"/stdin?eof=1", "in"), http.StatusNoContent, "")
		if x := le.liveEnded("ex-1", x.ID); x.State != ExecExited || *x.ExitCode != 0 {
			t.Fatalf("after eof: %+v", x)
		}
		if c := le.liveOutput("ex-1", x.ID, ""); c.Data != "fed in" {
			t.Fatalf("its output: %q", c.Data)
		}
		r := le.liveRun("ex-1", map[string]any{"argv": []string{probeBin, "copy"}, "stdin": "piped"})
		if r.Stdout.Head != "piped" {
			t.Fatalf("a run's stdin: %+v", r.Stdout)
		}
	})

	t.Run("a TTY on the /ws/term wire", func(t *testing.T) {
		le.t = t
		srv := le.server(mgr)
		x := le.liveExec("ex-1", map[string]any{"tty": true, "argv": []string{probeBin, "readexit"}, "forUser": "bob"})
		tc, code := dialTTY(t, srv, "/sandboxes/ex-1/execs/"+x.ID+"/tty?sessionId=S-1&sandboxId=box.1&forUser=bob")
		if code != http.StatusSwitchingProtocols {
			t.Fatalf("attach: %d", code)
		}
		if text, _, h := tc.next(10 * time.Second); !text || h["op"] != "session" || h["id"] != "S-1" || h["sandbox"] != "box.1" || h["echoAck"] != true {
			t.Fatalf("the session frame: %v", h)
		}
		tc.send("5") // echoed, then acked while it still runs
		for tc.hasCtl("ack") == nil {
			tc.next(10 * time.Second)
		}
		tc.send("\r")
		tc.until("got 5")
		tc.until("")
		if tc.exit["code"] != float64(5) || tc.hasCtl("ack")["n"] != float64(1) {
			t.Fatalf("exit %v, control %v", tc.exit, tc.ctl)
		}
		tc2, _ := dialTTY(t, srv, "/sandboxes/ex-1/execs/"+x.ID+"/tty")
		tc2.next(10 * time.Second)
		tc2.until("")
		if !strings.Contains(tc2.out.String(), "got 5") || tc2.exit["code"] != float64(5) {
			t.Fatalf("the replay %q, exit %v", tc2.out.String(), tc2.exit)
		}
		if _, code := dialTTY(t, srv, "/sandboxes/ex-1/execs/"+x.ID+"/tty?forUser=alice"); code != http.StatusForbidden {
			t.Fatalf("a noTerminal user's attach: %d", code)
		}
		if !hasShell {
			return
		}
		// the start route: the login shell on a PTY
		tc3, code := dialTTY(t, srv, "/sandboxes/ex-1/tty?rows=30&cols=90")
		if code != http.StatusSwitchingProtocols {
			t.Fatalf("a login shell: %d", code)
		}
		tc3.next(10 * time.Second)
		tc3.send("echo hi-$((1+2)); stty size\r")
		tc3.until("30 90")
		tc3.until("hi-3")
		tc3.send("exit 4\r")
		tc3.until("")
		if tc3.exit["code"] != float64(4) {
			t.Fatalf("the shell's exit: %v", tc3.exit)
		}
		q := url.Values{"cmd": {"echo from-cmd"}}
		tc4, _ := dialTTY(t, srv, "/sandboxes/ex-1/tty?"+q.Encode())
		tc4.next(10 * time.Second)
		tc4.until("")
		if !strings.Contains(tc4.out.String(), "from-cmd") || tc4.exit["code"] != float64(0) {
			t.Fatalf("a cmd on a tty: %q %v", tc4.out.String(), tc4.exit)
		}
	})

	t.Run("an exec running when its sandbox stops is killed; after a restart it is lost", func(t *testing.T) {
		le.t = t
		x := le.liveExec("ex-1", map[string]any{"argv": []string{probeBin, "tree", "100s"}})
		le.liveChild("ex-1", x.ID)
		le.stop("ex-1")
		got := le.liveExecGet("ex-1", x.ID)
		if got.State != ExecKilled || got.Signal != "KILL" || got.ExitCode != nil || got.Ended == nil {
			t.Fatalf("after the stop: %+v", got)
		}
		c := le.liveOutput("ex-1", x.ID, "waitMs=1000")
		if !strings.HasPrefix(c.Data, "child ") || c.End != c.Total || c.State != ExecKilled {
			t.Fatalf("its output after the stop: %+v", c)
		}
		// another xbind over the same workspace: every old id is lost
		m2 := New(Options{Root: le.m.root, Isolated: true, UIDRange: le.m.uidRange, Rootfs: le.m.rootfs, BxPath: le.m.bxPath,
			Deps: Deps{Caps: le.m.deps.Caps, Admin: le.m.deps.Admin, Mounts: le.m.deps.Mounts}})
		env2 := &testEnv{t: t, m: m2, mux: routes(m2)}
		env2.want(env2.do(mgr, "GET", "/sandboxes/ex-1/execs/"+x.ID, nil), http.StatusGone, RefLost)
		env2.want(env2.do(mgr, "GET", "/sandboxes/ex-1/execs/"+x.ID+"/output", nil), http.StatusGone, RefLost)
		m2.trash.wait()
	})
}

// liveOutputQuiet reads a chunk without failing the test (from goroutines).
func (le *liveEnv) liveOutputQuiet(name, id string) OutputChunk {
	w := le.do(mgr, "GET", "/sandboxes/"+name+"/execs/"+id+"/output?waitMs=1000", nil)
	var c OutputChunk
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	return c
}
