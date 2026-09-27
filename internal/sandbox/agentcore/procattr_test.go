//go:build linux

package agentcore

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// covers WP-3b — Options.SessionOOMScoreAdj: sessions from 2 start with it,
// session 1 (a backend's, a terminal's) and the agent keep what they had,
// and a session gone before the write logs nothing.
func TestSessionOOMScoreAdj(t *testing.T) {
	lowerOwnOOMScore()
	b, err := os.ReadFile("/proc/self/oom_score_adj")
	if err != nil {
		t.Skip(err)
	}
	own, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if own >= 500 {
		t.Skipf("this process's oom_score_adj is %d already: a session can't be given less", own)
	}
	var mu sync.Mutex
	var logged []string
	h := newHarness(t, func(o *Options) {
		o.SessionOOMScoreAdj = 500
		o.Logf = func(f string, a ...any) { mu.Lock(); logged = append(logged, fmt.Sprintf(f, a...)); mu.Unlock() }
	})
	score := func(h *harness, session int) string {
		t.Helper()
		ss := h.exec(proto.Exec{Session: session, Argv: sh("cat /proc/self/oom_score_adj"), Merge: true, NoStdin: true})
		out := readAll(t, ss["stdout"])
		if m := h.wait(session, "exited", "error"); m.Op != "exited" || m.Code != 0 {
			t.Fatalf("session %d: %+v %q", session, m, out)
		}
		return strings.TrimSpace(out)
	}
	if got := score(h, 1); got != strconv.Itoa(own) {
		t.Errorf("session 1's oom_score_adj %s, want the agent's %d", got, own)
	}
	for _, s := range []int{2, 3} {
		if got := score(h, s); got != "500" {
			t.Errorf("session %d's oom_score_adj %s, want 500", s, got)
		}
	}
	if b, _ := os.ReadFile("/proc/self/oom_score_adj"); strings.TrimSpace(string(b)) != strconv.Itoa(own) {
		t.Errorf("the agent's own oom_score_adj became %s", b)
	}
	// a session gone before the write, reaped or a zombie: nothing to log
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	h.core.adjustOOM(4, gone.Process.Pid)
	zombie := exec.Command("true")
	if err := zombie.Start(); err != nil {
		t.Fatal(err)
	}
	var info unix.Siginfo
	if err := unix.Waitid(unix.P_PID, zombie.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil); err != nil {
		t.Fatal(err)
	}
	h.core.adjustOOM(5, zombie.Process.Pid)
	_ = zombie.Wait()
	mu.Lock()
	if len(logged) > 0 {
		t.Errorf("logged: %q", logged)
	}
	mu.Unlock()

	// without the option, every session keeps the agent's
	h = newHarness(t, nil)
	if got := score(h, 2); got != strconv.Itoa(own) {
		t.Errorf("session 2 with no SessionOOMScoreAdj: %s, want %d", got, own)
	}
}

// lowerOwnOOMScore brings this process's oom_score_adj down to 0 when it is
// higher — before the agent under test inherits it — so the scores are
// checked on a CI runner that starts its jobs at 500 too. An unprivileged
// process may lower its own score down to the floor a privileged writer set;
// where that floor is higher, the write fails and the checks skip.
func lowerOwnOOMScore() {
	if b, err := os.ReadFile("/proc/self/oom_score_adj"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
			_ = os.WriteFile("/proc/self/oom_score_adj", []byte("0"), 0)
		}
	}
}
