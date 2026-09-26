package term

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

// The full patch of a reported tool call, of an edit call (snapshotted, not
// reported), of a turn; narrowed to one file; cut at a line past the cap;
// nothing for an unknown key or once the snapshotter is closed.
func TestSnapperFullDiff(t *testing.T) {
	work := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "one\n")
	write("b.txt", "two\n")
	gitRepo(t, work)
	got := make(chan agent.Event, 16)
	s := newSnapper(work, func(e agent.Event) { got <- e })
	if s == nil {
		t.Fatal("no snapper for a git tile")
	}
	waitEv := func() {
		t.Helper()
		select {
		case <-got:
		case <-time.After(10 * time.Second):
			t.Fatal("no files.changed")
		}
	}
	ctx := context.Background()
	s.turnStart(10 * time.Second)
	s.observe(ev(agent.EvToolCall, map[string]any{"id": "t1", "kind": "execute", "status": "in_progress"}))
	write("a.txt", "one\nmore\n")
	write("b.txt", "two\nmore\n")
	s.observe(ev(agent.EvToolUpdate, map[string]any{"id": "t1", "status": "completed"}))
	waitEv()
	s.observe(ev(agent.EvToolCall, map[string]any{"id": "t2", "kind": "edit", "status": "pending"}))
	write("a.txt", "one\nmore\nedited\n")
	s.observe(ev(agent.EvToolUpdate, map[string]any{"id": "t2", "status": "completed"}))
	s.observe(ev(agent.EvTurnEnd, map[string]any{"turn": 1}))
	waitEv() // the turn's

	p, trunc, err := s.fullDiff(ctx, "tool:t1", "")
	if err != nil || trunc || !strings.Contains(string(p), "diff --git a/a.txt b/a.txt") || !strings.Contains(string(p), "diff --git a/b.txt b/b.txt") || strings.Contains(string(p), "+edited") {
		t.Fatalf("t1: %v %v\n%s", err, trunc, p)
	}
	if p, _, err := s.fullDiff(ctx, "tool:t2", ""); err != nil || !strings.Contains(string(p), "+edited") || strings.Contains(string(p), "b.txt") {
		t.Fatalf("t2 (an edit, not reported, still kept): %v\n%s", err, p)
	}
	if p, _, err := s.fullDiff(ctx, "turn:1", "b.txt"); err != nil || !strings.Contains(string(p), "b/b.txt") || strings.Contains(string(p), "a.txt") {
		t.Fatalf("turn 1 narrowed to b.txt: %v\n%s", err, p)
	}
	if p, _, err := s.fullDiff(ctx, "turn:1", ":(exclude)b.txt"); err != nil || len(p) != 0 {
		t.Fatalf("pathspec magic must be literal: %v\n%s", err, p)
	}
	old := fullPatchCap
	fullPatchCap = 40
	p, trunc, err = s.fullDiff(ctx, "turn:1", "")
	fullPatchCap = old
	if err != nil || !trunc || len(p) > 40 || len(p) == 0 || p[len(p)-1] != '\n' {
		t.Fatalf("capped: %v %v %q", err, trunc, p)
	}
	if _, _, err := s.fullDiff(ctx, "tool:nope", ""); !errors.Is(err, ErrNoDiff) {
		t.Fatalf("unknown: %v", err)
	}
	// bounded: one full diff at a time per session, fullDiffSlots across the
	// daemon — a caller past either waits (here until its deadline)
	blocked := func(what string) {
		t.Helper()
		short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		if _, _, err := s.fullDiff(short, "turn:1", ""); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s: %v", what, err)
		}
	}
	s.full <- struct{}{} // a diff of this session running
	blocked("a second diff of the session")
	<-s.full
	for range cap(fullDiffSlots) { // the daemon's slots taken by other sessions
		fullDiffSlots <- struct{}{}
	}
	blocked("past the daemon's slots")
	for range cap(fullDiffSlots) {
		<-fullDiffSlots
	}
	if _, _, err := s.fullDiff(ctx, "turn:1", ""); err != nil {
		t.Fatalf("after the others: %v", err)
	}
	s.close()
	if _, _, err := s.fullDiff(ctx, "tool:t1", ""); !errors.Is(err, ErrNoDiff) {
		t.Fatalf("after close: %v", err)
	}
	var none *snapper
	if _, _, err := none.fullDiff(ctx, "tool:t1", ""); !errors.Is(err, ErrNoDiff) {
		t.Fatalf("no snapper: %v", err)
	}
}

func TestCleanDiffPath(t *testing.T) {
	for in, ok := range map[string]bool{"": true, "a.txt": true, "dir/a b.txt": true, "/etc/passwd": false, "../x": false, "a/../b": false, "./a": false, "..": false, "a\x00b": false} {
		if _, got := cleanDiffPath(in); got != ok {
			t.Errorf("%q: %v", in, got)
		}
	}
}

// End to end: a turn whose patch passes the event caps — the event says
// truncated, AgentDiff serves all of it (per call and per turn, and one
// file of it); an unknown call is ErrNoDiff.
func TestAgentSessionFullDiff(t *testing.T) {
	r := newAgentRig(t)
	gitRepo(t, filepath.Join(r.root, "apps", "x"))
	info, code, err := r.m.OpenAgent(auth.Principal{Owner: true}, "apps/x", "", "fake", "", "", "", nil)
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	defer func() {
		r.m.Kill(info.ID)
		waitClose(t, r.change, "close:"+info.ID)
	}()
	r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	if _, err := r.m.AgentPrompt(context.Background(), info.ID, "run: seq 1 60000 > big.txt; echo x > small.txt"); err != nil {
		t.Fatal(err)
	}
	tool := edata(r.until(t, func(e SessionEvent) bool { return e.Type == EvFilesChanged }).Event)
	turn := edata(r.until(t, func(e SessionEvent) bool { return e.Type == EvFilesChanged }).Event)
	if tool["patch"].(map[string]any)["truncated"] != true || turn["patch"].(map[string]any)["truncated"] != true {
		t.Fatalf("the events' patches should be capped: %v / %v", tool["patch"].(map[string]any)["truncated"], turn["patch"].(map[string]any)["truncated"])
	}
	ctx := context.Background()
	for _, q := range []struct {
		tool string
		turn int64
	}{{"run1", 0}, {"", int64(turn["turn"].(float64))}} {
		p, trunc, err := r.m.AgentDiff(ctx, info.ID, q.tool, q.turn, "")
		if err != nil || trunc || !strings.Contains(string(p), "+60000\n") || !strings.Contains(string(p), "b/small.txt") {
			t.Fatalf("%+v: %v %v (%d bytes)", q, err, trunc, len(p))
		}
	}
	p, _, err := r.m.AgentDiff(ctx, info.ID, "run1", 0, "small.txt")
	if err != nil || strings.Contains(string(p), "big.txt") || !strings.Contains(string(p), "+x") {
		t.Fatalf("one file: %v\n%s", err, p)
	}
	if _, _, err := r.m.AgentDiff(ctx, info.ID, "nope", 0, ""); !errors.Is(err, ErrNoDiff) {
		t.Fatalf("unknown call: %v", err)
	}
	if _, _, err := r.m.AgentDiff(ctx, info.ID, "run1", 0, "../x"); err == nil || errors.Is(err, ErrNoDiff) {
		t.Fatalf("a path outside the tile: %v", err)
	}
	if _, _, err := r.m.AgentDiff(ctx, "nope", "run1", 0, ""); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unknown session: %v", fmt.Sprint(err))
	}
}
