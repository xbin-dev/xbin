//go:build linux

package agentcore

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestPID1Spawner runs the PID 1 reaper in a child process of its own (its
// wait4(-1) would take this test binary's other children), made a child
// subreaper so that orphans come to it as they do to a sandbox's PID 1.
func TestPID1Spawner(t *testing.T) {
	if os.Getenv("AGENTCORE_PID1_CHILD") == "1" {
		pid1Child(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPID1Spawner$", "-test.v")
	cmd.Env = append(os.Environ(), "AGENTCORE_PID1_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS") {
		t.Fatalf("the reaper child: %v\n%s", err, out)
	}
}

func pid1Child(t *testing.T) {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Skipf("no child subreaper here: %v", err)
	}
	sp := PID1Spawner()
	status := func(ch <-chan unix.WaitStatus) unix.WaitStatus {
		t.Helper()
		select {
		case ws := <-ch:
			return ws
		case <-time.After(hangGuard):
			t.Fatal("no status")
		}
		return 0
	}
	shell := func(script string, extra ...*os.File) (*os.Process, <-chan unix.WaitStatus, *bufio.Reader) {
		t.Helper()
		r, w, _ := os.Pipe()
		files := append([]*os.File{nil, w, os.Stderr}, extra...)
		p, ch, err := sp.Start("/bin/sh", []string{"sh", "-c", script}, &os.ProcAttr{Files: files})
		w.Close()
		if err != nil {
			t.Fatal(err)
		}
		return p, ch, bufio.NewReader(r)
	}
	// orphan starts a background subshell that exits with code once release
	// is called, and returns after the shell that started it has exited:
	// the orphan outlives its parent, so it comes to us. (A shell reaps a
	// background child that exits before the shell does; a child given
	// only a head start, like `sleep 0.3 &`, loses that race under load.)
	// whileParentRuns, if set, gets the orphan's pid before its parent
	// exits.
	orphan := func(code int, whileParentRuns func(pid int)) (pid int, release func()) {
		t.Helper()
		gate, open, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_, ch, out := shell("(read x <&3; exit "+strconv.Itoa(code)+") & echo $!", gate)
		gate.Close()
		line, _ := out.ReadString('\n')
		pid, err = strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			t.Fatalf("%q", line)
		}
		if whileParentRuns != nil {
			whileParentRuns(pid)
		}
		if ws := status(ch); !ws.Exited() || ws.ExitStatus() != 0 {
			t.Fatalf("the orphan's parent: %v", ws)
		}
		return pid, func() { open.Close() } // the orphan's read sees EOF
	}
	// reapedUnclaimed waits until the reaper has collected pid with nobody
	// registered for it.
	reapedUnclaimed := func(pid int) {
		t.Helper()
		p := sp.(*pid1)
		waitUntil(t, "the orphan reaped", func() bool {
			p.mu.Lock()
			defer p.mu.Unlock()
			_, ok := p.unclaimed[pid]
			return ok
		})
	}

	if _, ch, _ := shell("exit 7"); status(ch).ExitStatus() != 7 {
		t.Fatal("exit status")
	}
	if _, ch, _ := shell("kill -9 $$"); status(ch).Signal() != unix.SIGKILL {
		t.Fatal("killed")
	}

	// an orphan registered while it runs (and while its parent still does)
	var got <-chan unix.WaitStatus
	_, release := orphan(3, func(pid int) { got = sp.Register(pid) })
	release()
	if ws := status(got); !ws.Exited() || ws.ExitStatus() != 3 {
		t.Fatalf("the orphan: %v", ws)
	}

	// an orphan registered after it was reaped: remembered
	pid, release := orphan(5, nil)
	release()
	reapedUnclaimed(pid)
	if ws := status(sp.Register(pid)); !ws.Exited() || ws.ExitStatus() != 5 {
		t.Fatalf("a late Register: %v", ws)
	}
}
