//go:build linux

package agentcore

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// covers WP-3b — Options.SessionOOMScoreAdj: sessions from 2 start with it,
// from their first instruction on, and so does what they fork at once;
// session 1 (a backend's, a terminal's) and the agent keep what they had;
// a score the agent can't take is logged, and the session keeps the
// agent's.
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
	// score reads a session's oom_score_adj at once: in a child the session
	// forks first thing, then in the session's process. A session has its
	// score from the clone on (spawnSession), and so has that child.
	score := func(h *harness, session int) string {
		t.Helper()
		ss := h.exec(proto.Exec{Session: session, Argv: sh("cat /proc/self/oom_score_adj; cat /proc/$$/oom_score_adj"), Merge: true, NoStdin: true})
		out := readAll(t, ss["stdout"])
		if m := h.wait(session, "exited", "error"); m.Op != "exited" || m.Code != 0 {
			t.Fatalf("session %d: %+v %q", session, m, out)
		}
		return strings.Join(strings.Fields(out), " ")
	}
	agents := strconv.Itoa(own) + " " + strconv.Itoa(own)
	if got := score(h, 1); got != agents {
		t.Errorf("session 1's oom_score_adj (a child's, its own) %s, want the agent's %d", got, own)
	}
	for _, s := range []int{2, 3} {
		if got := score(h, s); got != "500 500" {
			t.Errorf("session %d's oom_score_adj (a child's, its own) %s, want 500", s, got)
		}
	}
	if b, _ := os.ReadFile("/proc/self/oom_score_adj"); strings.TrimSpace(string(b)) != strconv.Itoa(own) {
		t.Errorf("the agent's own oom_score_adj became %s", b)
	}
	mu.Lock()
	if len(logged) > 0 {
		t.Errorf("logged: %q", logged)
	}
	mu.Unlock()

	// a score the agent can't take (one below the floor a privileged writer
	// set; here, an own score it can't even read) is logged, and the session
	// starts with the agent's
	was := ownOOMScore
	t.Cleanup(func() { ownOOMScore = was })
	ownOOMScore = t.TempDir() // a directory
	if got := score(h, 4); got != agents {
		t.Errorf("session 4, the agent unable to take its score: %s, want the agent's %d", got, own)
	}
	mu.Lock()
	if len(logged) != 1 || !strings.HasPrefix(logged[0], "session 4: oom_score_adj 500: ") {
		t.Errorf("logged: %q, want session 4's oom_score_adj failure", logged)
	}
	mu.Unlock()
	ownOOMScore = was

	// without the option, every session keeps the agent's
	h = newHarness(t, nil)
	if got := score(h, 2); got != agents {
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

// The namespace agent clears its supplementary groups (xbind's user's,
// unmapped in the sandbox) unless its user namespace denies setgroups —
// WP-21's live run found `id -G` in a tile sandbox listing seven nogroups.
func TestDropGroups(t *testing.T) {
	var calls [][]int
	setgroups = func(gids []int) error { calls = append(calls, gids); return nil }
	t.Cleanup(func() { setgroups = syscall.Setgroups })
	dir := t.TempDir()
	for _, c := range []struct {
		file string // the setgroups file's contents ("" = none)
		drop bool
	}{{"allow\n", true}, {"deny\n", false}, {"", true}} {
		calls = nil
		p := filepath.Join(dir, "setgroups")
		_ = os.Remove(p)
		if c.file != "" {
			if err := os.WriteFile(p, []byte(c.file), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := dropGroups(p); err != nil {
			t.Fatal(err)
		}
		if dropped := len(calls) == 1 && len(calls[0]) == 0; dropped != c.drop || len(calls) > 1 {
			t.Errorf("setgroups %q: calls %v, want a drop: %v", c.file, calls, c.drop)
		}
	}
	setgroups = func([]int) error { return syscall.EPERM }
	if err := dropGroups(filepath.Join(dir, "none")); err == nil {
		t.Error("a failed drop must fail the agent's start")
	}
}
