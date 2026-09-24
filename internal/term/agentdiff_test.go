package term

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

func gitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

func ev(typ string, d any) agent.Event {
	e := agent.New(typ, d)
	return e
}

// The snapshotter reports what each finished tool call and each turn
// changed, from its own private git dir: a shell write shows up with real
// line counts and a patch; excludes apply; the tile's repo — its index and
// its config (a hostile core.fsmonitor) — is never used.
func TestSnapperToolAndTurnDiffs(t *testing.T) {
	work := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		_ = os.MkdirAll(filepath.Dir(filepath.Join(work, p)), 0o755)
		if err := os.WriteFile(filepath.Join(work, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main\n\nfunc main() {}\n")
	write("old.txt", "a\nb\nc\nd\ne\nf\n")
	write("gone.txt", "bye\n")
	gitRepo(t, work)
	pwned := filepath.Join(t.TempDir(), "PWNED")
	cfg, _ := os.OpenFile(filepath.Join(work, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = cfg.WriteString("[core]\n\tfsmonitor = \"touch " + pwned + "; echo\"\n")
	cfg.Close()
	idx, _ := os.ReadFile(filepath.Join(work, ".git", "index"))

	got := make(chan agent.Event, 16)
	s := newSnapper(work, func(e agent.Event) { got <- e })
	if s == nil {
		t.Fatal("no snapper for a git tile")
	}
	defer s.close()
	next := func() map[string]any {
		t.Helper()
		select {
		case e := <-got:
			if e.Type != EvFilesChanged {
				t.Fatalf("event %s", e.Type)
			}
			var d map[string]any
			_ = json.Unmarshal(e.Data, &d)
			return d
		case <-time.After(10 * time.Second):
			t.Fatal("no files.changed")
		}
		return nil
	}

	s.turnStart(10 * time.Second)
	// a shell call rewrites main.go, adds a file, deletes one, renames one,
	// and touches node_modules (excluded)
	s.observe(ev(agent.EvToolCall, map[string]any{"id": "t1", "kind": "execute", "status": "in_progress"}))
	write("main.go", "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(1) }\n")
	write("made.txt", "hi\n")
	os.Remove(filepath.Join(work, "gone.txt"))
	os.Rename(filepath.Join(work, "old.txt"), filepath.Join(work, "new.txt"))
	write("node_modules/dep/index.js", "x\n")
	s.observe(ev(agent.EvToolUpdate, map[string]any{"id": "t1", "status": "completed"}))
	d := next()
	if d["toolCallId"] != "t1" {
		t.Fatalf("attribution: %v", d)
	}
	changes := func(d map[string]any) map[string]FileChange {
		var cs []FileChange
		b, _ := json.Marshal(d["changes"])
		_ = json.Unmarshal(b, &cs)
		out := map[string]FileChange{}
		for _, c := range cs {
			out[c.Path] = c
		}
		return out
	}
	cs := changes(d)
	want := map[string]FileChange{"made.txt": {Path: "made.txt", Status: "added", Add: 1}, "gone.txt": {Path: "gone.txt", Status: "deleted", Del: 1},
		"new.txt": {Path: "new.txt", OldPath: "old.txt", Status: "renamed"}, "main.go": {Path: "main.go", Status: "modified", Add: 3, Del: 1}}
	if len(cs) != len(want) {
		t.Fatalf("changes (node_modules excluded): %+v", cs)
	}
	for p, w := range want {
		if cs[p] != w {
			t.Fatalf("%s: got %+v want %+v", p, cs[p], w)
		}
	}
	patch := d["patch"].(map[string]any)
	if patch["format"] != "git_patch" || !strings.Contains(patch["text"].(string), "+func main() { fmt.Println(1) }") || patch["truncated"] != false {
		t.Fatalf("patch: %v", patch)
	}

	// an edit tool reports its own diff: snapshotted (the tree advances), not reported
	s.observe(ev(agent.EvToolCall, map[string]any{"id": "t2", "kind": "edit", "status": "pending"}))
	write("made.txt", "hi\nthere\n")
	s.observe(ev(agent.EvToolUpdate, map[string]any{"id": "t2", "status": "completed"}))
	// a no-op call reports nothing; the turn summary covers everything since its start
	s.observe(ev(agent.EvToolCall, map[string]any{"id": "t3", "kind": "execute", "status": "completed"}))
	s.observe(ev(agent.EvTurnEnd, map[string]any{"turn": 1, "stopReason": "end_turn"}))
	d = next()
	if d["turn"] != float64(1) || d["toolCallId"] != nil {
		t.Fatalf("the turn summary came next (no events for t2/t3): %v", d)
	}
	if c := changes(d)["made.txt"]; c.Status != "added" || c.Add != 2 {
		t.Fatalf("turn summary: %+v", changes(d))
	}

	// the user's repo was never touched or read
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("the tile's core.fsmonitor ran")
	}
	if idx2, _ := os.ReadFile(filepath.Join(work, ".git", "index")); string(idx2) != string(idx) {
		t.Fatal("the tile's index changed")
	}
	if st, _ := exec.Command("git", "-C", work, "-c", "core.fsmonitor=false", "log", "--oneline").Output(); strings.Count(string(st), "\n") != 1 {
		t.Fatalf("the tile's history changed: %s", st)
	}
}

// Not a git tile: no snapshotter, and every method is nil-safe.
func TestSnapperOffOutsideRepos(t *testing.T) {
	var s *snapper = newSnapper(t.TempDir(), func(agent.Event) { t.Fatal("emitted") })
	if s != nil {
		t.Fatal("a snapper for a non-repo")
	}
	s.turnStart(time.Second)
	s.observe(ev(agent.EvTurnEnd, map[string]any{"turn": 1}))
	s.close()
}

// End to end: a shell write inside an agent turn (the fake's `run:` is an
// execute tool call, as a real adapter's Bash is) lands as files.changed for
// that call, then as the turn's summary — and both persist in the log.
func TestAgentSessionFilesChanged(t *testing.T) {
	r := newAgentRig(t)
	gitRepo(t, filepath.Join(r.root, "apps", "x"))
	info, code, err := r.m.OpenAgent(auth.Principal{Owner: true}, "apps/x", "", "fake", "", "", "", nil)
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	if _, err := r.m.AgentPrompt(context.Background(), info.ID, "run: echo hi > made.txt"); err != nil {
		t.Fatal(err)
	}
	tool := r.until(t, func(e SessionEvent) bool { return e.Type == EvFilesChanged })
	d := edata(tool.Event)
	if d["toolCallId"] != "run1" || !strings.Contains(fmt.Sprint(d["changes"]), "made.txt") {
		t.Fatalf("the call's diff: %v", d)
	}
	turn := r.until(t, func(e SessionEvent) bool { return e.Type == EvFilesChanged })
	if d := edata(turn.Event); d["turn"] == nil || !strings.Contains(d["patch"].(map[string]any)["text"].(string), "+hi") {
		t.Fatalf("the turn's summary: %v", d)
	}
	evs, _, _, _ := r.m.AgentEvents(info.ID, 0)
	n := 0
	for _, e := range evs {
		if e.Type == EvFilesChanged {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("files.changed in the log: %d", n)
	}
	r.m.Kill(info.ID)
}
