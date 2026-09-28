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
		case <-time.After(10 * time.Second):
			t.Fatal("no status")
		}
		return 0
	}
	shell := func(script string) (*os.Process, <-chan unix.WaitStatus, *bufio.Reader) {
		t.Helper()
		r, w, _ := os.Pipe()
		p, ch, err := sp.Start("/bin/sh", []string{"sh", "-c", script}, &os.ProcAttr{Files: []*os.File{nil, w, os.Stderr}})
		w.Close()
		if err != nil {
			t.Fatal(err)
		}
		return p, ch, bufio.NewReader(r)
	}
	if _, ch, _ := shell("exit 7"); status(ch).ExitStatus() != 7 {
		t.Fatal("exit status")
	}
	if _, ch, _ := shell("kill -9 $$"); status(ch).Signal() != unix.SIGKILL {
		t.Fatal("killed")
	}

	// an orphan registered while it runs
	_, ch, out := shell("sleep 0.3 & echo $!")
	line, _ := out.ReadString('\n')
	orphan, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("%q", line)
	}
	got := sp.Register(orphan)
	status(ch)
	if ws := status(got); !ws.Exited() || ws.ExitStatus() != 0 {
		t.Fatalf("the orphan: %v", ws)
	}

	// an orphan registered after it was reaped: remembered
	_, ch, out = shell("(exit 5) & echo $!")
	line, _ = out.ReadString('\n')
	orphan, _ = strconv.Atoi(strings.TrimSpace(line))
	status(ch)
	time.Sleep(300 * time.Millisecond) // reparented to us, and reaped unclaimed
	if ws := status(sp.Register(orphan)); ws.ExitStatus() != 5 {
		t.Fatalf("a late Register: %v", ws)
	}
}
