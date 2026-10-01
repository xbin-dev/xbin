//go:build linux && integration

package agentcore

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// covers WP-3b — the agent dies with its root: under FuseWatch the root's
// fuse-overlayfs runs in the foreground as the init's child, never a daemon,
// and the agent is told its pid; SIGKILLing it from outside ends the agent with exit 3 within 1 s
// and empties the pid namespace. A fuse-overlayfs that fails to mount fails
// the start with its output; one that never mounts, after 10 s. Unwatched (a
// terminal's, a backend's) fuse-overlayfs still daemonizes. Sessions carry
// oom_score_adj 500 over fuse-overlayfs too.
func TestNamespaceAgentRoot(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	bin := t.TempDir()
	build(t, filepath.Join(bin, "bx"), "../../../cmd/bx")
	build(t, filepath.Join(bin, "probe"), "./testdata/nsprobe")

	t.Run("fails to mount", func(t *testing.T) {
		// a stand-in that says why and exits 1: the probe knows no op "-f"
		t.Setenv("XBIN_FUSE_OVERLAYFS", filepath.Join(bin, "probe"))
		sb := launchNamespaceAgent(t, bin, "")
		if code := exitCode(t, sb, 15*time.Second); code != 127 {
			t.Errorf("the init's exit: %d, want 127", code)
		}
		wantLog(t, sb, "sandbox-init: fuse-overlayfs mount: it exited before mounting the root (exit status 1): nsprobe: unknown op -f")
	})

	t.Run("never mounts", func(t *testing.T) {
		stub := filepath.Join(bin, "fuse-sleeps")
		if err := os.WriteFile(stub, []byte("#!/bin/sh\necho mounting any moment now\nexec sleep 60\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XBIN_FUSE_OVERLAYFS", stub)
		start := time.Now()
		sb := launchNamespaceAgent(t, bin, "")
		if code := exitCode(t, sb, 30*time.Second); code != 127 {
			t.Errorf("the init's exit: %d, want 127", code)
		}
		if d := time.Since(start); d < 10*time.Second || d > 15*time.Second {
			t.Errorf("gave up after %s, want 10 s", d.Round(time.Millisecond))
		}
		wantLog(t, sb, "sandbox-init: fuse-overlayfs mount: no FUSE mount after 10s: mounting any moment now")
	})

	fuse := findFuseOverlayfs()
	if fuse == "" {
		t.Skip("no fuse-overlayfs ($XBIN_FUSE_OVERLAYFS, bin/, PATH)")
	}
	t.Setenv("XBIN_FUSE_OVERLAYFS", fuse)

	t.Run("fuse-overlayfs refuses", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")
		sb := launchNamespaceAgent(t, bin, "", func(s *sandbox.Spec) { s.Lower = append([]string{missing}, s.Lower...) })
		if code := exitCode(t, sb, 15*time.Second); code != 127 {
			t.Errorf("the init's exit: %d, want 127", code)
		}
		wantLog(t, sb, "sandbox-init: fuse-overlayfs mount: it exited before mounting the root (exit status 1): ")
		if log := sb.log.String(); !strings.Contains(log, missing) {
			t.Errorf("the error doesn't quote fuse-overlayfs on the missing lower %s:\n%s", missing, log)
		}
	})

	t.Run("watched", func(t *testing.T) {
		sb := startNamespaceAgent(t, bin, "")
		fpid := fuseProcess(t, sb)
		inNS := nsPid(t, fpid)
		if st := procStat(t, fpid); st.ppid != sb.cmd.Process.Pid || st.sid != fpid {
			t.Errorf("fuse-overlayfs (pid %d): parent %d, session %d — want the agent's child (%d) in a session of its own", fpid, st.ppid, st.sid, sb.cmd.Process.Pid)
		}
		if argv := cmdline(t, fpid); !slices.Contains(argv, "-f") {
			t.Errorf("fuse-overlayfs doesn't run in the foreground: %q", argv)
		}
		if argv := cmdline(t, sb.cmd.Process.Pid); !slices.Contains(argv, "--fuse-pid") || argv[slices.Index(argv, "--fuse-pid")+1] != inNS {
			t.Errorf("the agent's argv %q, want --fuse-pid %s", argv, inNS)
		}
		sb.t = t
		testOOMScores(t, sb)

		s := sb.next
		sb.next++
		sb.exec(proto.Exec{Session: s, Argv: []string{"/probe", "sleep", "600"}, Merge: true, NoStdin: true})
		sb.wait(s, "started")
		pids := descendants(sb.cmd.Process.Pid)
		killed := time.Now()
		if err := unix.Kill(fpid, unix.SIGKILL); err != nil {
			t.Fatal(err)
		}
		if code := exitCode(t, sb, 10*time.Second); code != ExitRootGone {
			t.Errorf("the agent's exit: %d, want %d", code, ExitRootGone)
		}
		if d := time.Since(killed); d > time.Second {
			t.Errorf("the agent outlived its root by %s", d.Round(time.Millisecond))
		}
		for _, p := range append(pids, sb.cmd.Process.Pid) {
			for unix.Kill(p, 0) == nil {
				if time.Since(killed) > time.Second {
					t.Fatalf("pid %d of the sandbox outlived its root by 1 s", p)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		t.Logf("the pid namespace emptied %s after fuse-overlayfs died", time.Since(killed).Round(time.Millisecond))
		wantLog(t, sb, "sbx-agent: the root filesystem is gone: fuse-overlayfs (pid "+inNS+") was killed by SIGKILL")
	})

	t.Run("unwatched", func(t *testing.T) {
		sb := startNamespaceAgent(t, bin, "", func(s *sandbox.Spec) { s.FuseWatch = false })
		fpid := fuseProcess(t, sb)
		if st := procStat(t, fpid); st.sid != fpid {
			t.Errorf("fuse-overlayfs (pid %d) is in session %d: not the daemon it always was", fpid, st.sid)
		}
		if argv := cmdline(t, fpid); slices.Contains(argv, "-f") {
			t.Errorf("fuse-overlayfs runs in the foreground: %q", argv)
		}
		if argv := cmdline(t, sb.cmd.Process.Pid); slices.Contains(argv, "--fuse-pid") {
			t.Errorf("the agent watches an unwatched fuse-overlayfs: %q", argv)
		}
		sb.t = t
		if out := sb.probe("echo", "served"); out != "served\n" {
			t.Errorf("a session: %q", out)
		}
	})
}

// testOOMScores checks that a session's oom_score_adj is 500 — as root, and
// as a mapped non-root uid when there is one — while the agent keeps what
// it inherited (this process's).
func testOOMScores(t *testing.T, sb *nsSandbox) {
	t.Helper()
	b, err := os.ReadFile("/proc/self/oom_score_adj")
	if err != nil {
		t.Fatal(err)
	}
	own := strings.TrimSpace(string(b))
	if n, _ := strconv.Atoi(own); n >= 500 {
		t.Logf("this process's oom_score_adj is %s already: a session can't be given less (not checked)", own)
		return
	}
	if got := sb.probe("read", "/proc/self/oom_score_adj"); got != "500\n" {
		t.Errorf("a session's oom_score_adj: %q, want 500", got)
	}
	// The agent's, read from here with no session starting: it carries a
	// session's score while it clones one (spawnSession), so a session
	// reading /proc/1 would race the agent going back to its own.
	if got, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(sb.cmd.Process.Pid), "oom_score_adj")); string(got) != own+"\n" {
		t.Errorf("the agent's oom_score_adj: %q, want %s (inherited)", got, own)
	}
	uid := uint32(1000)
	pid := strconv.Itoa(sb.cmd.Process.Pid)
	if !idMapped(filepath.Join("/proc", pid, "uid_map"), uid) || !idMapped(filepath.Join("/proc", pid, "gid_map"), uid) {
		t.Log("uid 1000 isn't mapped (a single-id sandbox): no non-root session")
		return
	}
	out, m := sb.run(proto.Exec{Argv: []string{"/probe", "read", "/proc/self/oom_score_adj"}, UID: &uid, GID: &uid})
	if m.Op != "exited" || m.Code != 0 || out != "500\n" {
		t.Errorf("a uid 1000 session's oom_score_adj: %q %+v, want 500", out, m)
	}
}

// exitCode waits for the sandbox's init (the agent, once exec'd) to exit.
func exitCode(t *testing.T, sb *nsSandbox, within time.Duration) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- sb.cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if err != nil && !errors.As(err, &ee) {
			t.Fatalf("wait: %v", err)
		}
		return sb.cmd.ProcessState.ExitCode()
	case <-time.After(within):
		t.Fatalf("the sandbox still runs after %s:\n%s", within, sb.log)
	}
	return -1
}

func wantLog(t *testing.T, sb *nsSandbox, want string) {
	t.Helper()
	if log := sb.log.String(); !strings.Contains(log, want) {
		t.Errorf("the log has no %q:\n%s", want, log)
	}
}

// fuseProcess is the host pid of the sandbox's fuse-overlayfs.
func fuseProcess(t *testing.T, sb *nsSandbox) int {
	t.Helper()
	for _, p := range descendants(sb.cmd.Process.Pid) {
		if b, _ := os.ReadFile("/proc/" + strconv.Itoa(p) + "/comm"); strings.TrimSpace(string(b)) == "fuse-overlayfs" {
			return p
		}
	}
	t.Fatalf("no fuse-overlayfs under the sandbox's init (%d)", sb.cmd.Process.Pid)
	return 0
}

type stat struct{ ppid, sid int }

// procStat reads a process's parent and session from /proc/<pid>/stat.
func procStat(t *testing.T, pid int) stat {
	t.Helper()
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	i := bytes.LastIndexByte(b, ')')
	if err != nil || i < 0 {
		t.Fatalf("stat of %d: %v", pid, err)
	}
	f := strings.Fields(string(b[i+1:])) // state ppid pgrp session …
	ppid, _ := strconv.Atoi(f[1])
	sid, _ := strconv.Atoi(f[3])
	return stat{ppid, sid}
}

func cmdline(t *testing.T, pid int) []string {
	t.Helper()
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}

// nsPid is a host process's pid in its own (the sandbox's) pid namespace.
func nsPid(t *testing.T, pid int) string {
	t.Helper()
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "NSpid:"); ok {
			f := strings.Fields(v)
			return f[len(f)-1]
		}
	}
	t.Fatalf("no NSpid for %d", pid)
	return ""
}
