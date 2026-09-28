package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
)

// logAt is a fixed clock for log entries: whole seconds, UTC.
var logAt = time.Date(2026, 9, 27, 10, 12, 3, 0, time.UTC)

// covers D119a D127e — the deploy log: one commit per finished attempt, failed and
// cancelled ones included, chained per deployment at refs/xbin/log/<name>,
// each entry's tree the attempted checkpoint's (the empty tree when none was
// made) and its parent the deployment's previous entry; the trailers record
// who, when, which feed and how, and read back field for field, newest
// first, filtered by deployment, id, before and limit; an error keeps its
// first line, 500 bytes at most. A tile without a store has no log, and
// appending to it creates none.
func TestDeployLog(t *testing.T) {
	needGit(t)
	s, _ := testStore(t)
	ctx := context.Background()
	src := tile(t, s, "apps/crm", map[string]string{"a.txt": "a\n"})
	c1 := capture(t, s, src, true)
	if err := os.WriteFile(filepath.Join(src.WorkTree, "a.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c2 := capture(t, s, src, false)

	long := strings.Repeat("é", 400) + "\nsecond line"
	entries := []LogEntry{
		{ID: 1, Deployment: "main", How: "pause", Checkpoint: c1.Hash, Feed: FeedWorkTree, By: "user:ana", Via: "session", Result: LogOK},
		{ID: 2, Deployment: "dev", How: "add", Checkpoint: c1.Hash, Feed: FeedWorkTree, FollowsWorkTree: true, By: "user:ana", Via: "session", Result: LogOK},
		{ID: 3, Deployment: "main", How: "deploy", Checkpoint: c2.Hash, Previous: c1.Hash, Feed: FeedWorkTree, By: "user:ben", Via: "terminal",
			Agent: true, Session: "s-42", Result: LogFailed, Error: "build: exit status 1\ncompiler output"},
		{ID: 4, Deployment: "main", How: "promote", From: "dev", Checkpoint: c2.Hash, Previous: c1.Hash, Feed: FeedWorkTree, By: "user:ana", Result: LogOK},
		{ID: 5, Deployment: "main", How: "reload-now", By: "user:ana", Result: LogFailed, Error: long}, // a refused capture: no checkpoint
		{ID: 6, Deployment: "dev", How: "deploy", Checkpoint: c2.Hash, Feed: FeedWorkTree, By: "user:ana", Result: LogCancelled},
	}
	for i := range entries {
		entries[i].RequestedAt = logAt.Add(time.Duration(i) * time.Minute)
		entries[i].FinishedAt = logAt.Add(time.Duration(i)*time.Minute + 30*time.Second)
		if err := s.AppendLog(ctx, "apps/crm", entries[i]); err != nil {
			t.Fatalf("append %d: %v", entries[i].ID, err)
		}
	}

	all, more, err := s.Log(ctx, "apps/crm", LogQuery{})
	if err != nil || more || len(all) != len(entries) {
		t.Fatalf("Log: %d entries, more %v, %v", len(all), more, err)
	}
	for i, got := range all {
		want := entries[len(entries)-1-i]
		if want.Result == LogFailed {
			want.Error = errorLine(want.Error)
		} else {
			want.Error = ""
		}
		if got != want {
			t.Errorf("entry %d reads back as\n%+v\nwant\n%+v", want.ID, got, want)
		}
	}
	if e := all[1]; len(e.Error) > MaxLogError || strings.Contains(e.Error, "second") || !strings.HasPrefix(e.Error, "éé") {
		t.Errorf("the error kept %d bytes: %q", len(e.Error), e.Error)
	}

	// the chains: parents, trees, messages
	chain := strings.Fields(storeGit(t, s, "apps/crm", "log", "--first-parent", "--format=%T", "refs/xbin/log/main"))
	if want := []string{emptyTree, c2.Hash, c2.Hash, c1.Hash}; strings.Join(chain, " ") != strings.Join(want, " ") {
		t.Errorf("main's chain trees: %v, want %v", chain, want)
	}
	if roots := strings.Fields(storeGit(t, s, "apps/crm", "rev-list", "--max-parents=0", "refs/xbin/log/main")); len(roots) != 1 {
		t.Errorf("main's chain has %d roots", len(roots))
	}
	msg := storeGit(t, s, "apps/crm", "log", "-1", "--format=%B", "refs/xbin/log/main~1")
	for _, want := range []string{"promote c:" + c2.Hash[:7] + " → main: ok\n", "Xbin-Deploy-Id: 4", "Xbin-From: dev", "Xbin-Checkpoint: " + c2.Hash,
		"Xbin-Previous: " + c1.Hash, "Xbin-Feed: work-tree", "Xbin-Follows-Work-Tree: false", "Xbin-By: user:ana", "Xbin-Agent: false",
		"Xbin-Requested-At: 2026-09-27T10:15:03Z", "Xbin-Result: ok"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the promote entry lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "Xbin-Error") || strings.Contains(msg, "Xbin-Session") {
		t.Errorf("an ok entry by a person names an error or a session:\n%s", msg)
	}
	if got := strings.TrimSpace(storeGit(t, s, "apps/crm", "log", "-1", "--format=%an <%ae> %at %cn %ct", "refs/xbin/log/dev")); got != fmt.Sprintf("xbin <xbin@localhost> %d xbin %d", entries[5].FinishedAt.Unix(), entries[5].FinishedAt.Unix()) {
		t.Errorf("the entry's author and dates: %q", got)
	}

	// queries
	for _, tc := range []struct {
		q    LogQuery
		ids  []int64
		more bool
	}{
		{LogQuery{Deployment: "main"}, []int64{5, 4, 3, 1}, false},
		{LogQuery{Deployment: "dev"}, []int64{6, 2}, false},
		{LogQuery{Limit: 2}, []int64{6, 5}, true},
		{LogQuery{Before: 5, Limit: 2}, []int64{4, 3}, true},
		{LogQuery{Deployment: "main", Before: 3}, []int64{1}, false},
		{LogQuery{ID: 3}, []int64{3}, false},
		{LogQuery{ID: 3, Deployment: "dev"}, nil, false},
		{LogQuery{ID: 99}, nil, false},
		{LogQuery{Deployment: "exp"}, nil, false},
		{LogQuery{Limit: 1000}, []int64{6, 5, 4, 3, 2, 1}, false},
	} {
		got, more, err := s.Log(ctx, "apps/crm", tc.q)
		var ids []int64
		for _, e := range got {
			ids = append(ids, e.ID)
		}
		if err != nil || fmt.Sprint(ids) != fmt.Sprint(tc.ids) || more != tc.more {
			t.Errorf("Log(%+v): %v more %v (%v), want %v more %v", tc.q, ids, more, err, tc.ids, tc.more)
		}
	}

	// refusals: nothing reaches git
	for _, bad := range []LogEntry{
		{Deployment: "main", How: "deploy", By: "u", Result: LogOK, FinishedAt: logAt},
		{ID: 7, Deployment: "Main", How: "deploy", By: "u", Result: LogOK, FinishedAt: logAt},
		{ID: 7, Deployment: "main", How: "merge", By: "u", Result: LogOK, FinishedAt: logAt},
		{ID: 7, Deployment: "main", How: "deploy", By: "u", Result: "running", FinishedAt: logAt},
		{ID: 7, Deployment: "main", How: "deploy", By: "u\nXbin-Result: ok", Result: LogFailed, FinishedAt: logAt},
		{ID: 7, Deployment: "main", How: "deploy", By: "u", Via: "a\x00b", Result: LogOK, FinishedAt: logAt},
		{ID: 7, Deployment: "main", How: "deploy", By: "u", Checkpoint: "c:" + c1.Hash[:7], Result: LogOK, FinishedAt: logAt},
		{ID: 7, Deployment: "main", How: "deploy", By: "u", Result: LogOK},
		{ID: 7, Deployment: "main", How: "promote", From: "../dev", By: "u", Result: LogOK, FinishedAt: logAt},
	} {
		if err := s.AppendLog(ctx, "apps/crm", bad); !errors.Is(err, ErrBadLogEntry) {
			t.Errorf("AppendLog(%+v): %v, want ErrBadLogEntry", bad, err)
		}
	}
	// a checkpoint the store doesn't hold
	if err := s.AppendLog(ctx, "apps/crm", LogEntry{ID: 7, Deployment: "main", How: "deploy", Checkpoint: strings.Repeat("ab", 20),
		By: "u", Result: LogOK, FinishedAt: logAt}); err == nil {
		t.Error("an entry for a tree the store doesn't hold was written")
	}
	if got, _, _ := s.Log(ctx, "apps/crm", LogQuery{}); len(got) != len(entries) {
		t.Errorf("refused appends changed the log: %d entries", len(got))
	}

	// no store: no log, and none is made
	if got, more, err := s.Log(ctx, "apps/none", LogQuery{}); err != nil || more || len(got) != 0 {
		t.Errorf("Log of a tile without a store: %v %v %v", got, more, err)
	}
	if err := s.AppendLog(ctx, "apps/none", entries[0]); !errors.Is(err, ErrNoStore) {
		t.Errorf("AppendLog on a tile without a store: %v", err)
	}
	if s.Exists("apps/none") {
		t.Error("the deploy log created a store")
	}
}

// covers T1 D119g — the deploy log's writes and reads are confined store runs
// (L17): every run goes through the Store's run hook as a store script, git
// only through the hardened prefix with the store as its repository and the
// pinned -c set first; appends bind the store read-write and reads read-only,
// nothing else, no network. An attempt's text — an error naming "--output",
// a session id, a via — travels on stdin, never on argv, and no marker a
// hostile string names is ever written.
func TestDeployLogConfined(t *testing.T) {
	needGit(t)
	s, rec := testStore(t)
	ctx := context.Background()
	src := tile(t, s, "apps/log", map[string]string{"a.txt": "a\n"})
	c := capture(t, s, src, true)
	gitlog := gitWrapper(t)
	before := rec.count()

	marker := filepath.Join(t.TempDir(), "PWNED")
	hostile := "--output=" + marker
	e := LogEntry{ID: 1, Deployment: "main", How: "deploy", Checkpoint: c.Hash, Feed: FeedWorkTree, By: "user:$(touch " + marker + ")",
		Via: hostile, Session: "-c core.fsmonitor=touch", RequestedAt: logAt, FinishedAt: logAt, Result: LogFailed, Error: "$(touch " + marker + ") " + hostile}
	if err := s.AppendLog(ctx, "apps/log", e); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Log(ctx, "apps/log", LogQuery{Deployment: "main"})
	if err != nil || len(got) != 1 || got[0].By != e.By || got[0].Via != hostile || got[0].Error != e.Error {
		t.Fatalf("the hostile entry reads back as %+v (%v)", got, err)
	}
	if _, _, err := s.Log(ctx, "apps/log", LogQuery{}); err != nil {
		t.Fatal(err)
	}

	store := s.Dir("apps/log")
	runs := rec.cmds[before:]
	if len(runs) != 3 {
		t.Fatalf("%d confined runs for one append and two reads", len(runs))
	}
	for i, r := range runs {
		if len(r.Argv) < 4 || r.Argv[0] != "sh" || r.Argv[1] != "-c" || !strings.HasPrefix(r.Argv[2], preamble) {
			t.Fatalf("run %d isn't a store script: %q", i, r.Argv)
		}
		if body := strings.TrimPrefix(r.Argv[2], preamble); bareGit.MatchString(body) {
			t.Errorf("run %d runs git without the hardened prefix:\n%s", i, body)
		}
		if r.Dir != store || len(r.Binds) != 0 || r.Net != confine.NetNone || r.ReadOnlyDir != (i > 0) {
			t.Errorf("run %d: Dir %s read-only %v binds %+v net %v", i, r.Dir, r.ReadOnlyDir, r.Binds, r.Net)
		}
		for _, a := range r.Argv[3:] {
			if strings.Contains(a, marker) || strings.Contains(a, "fsmonitor") {
				t.Errorf("run %d carries an attempt's text on argv: %q", i, r.Argv[3:])
			}
		}
	}
	if runs[0].Stdin == nil {
		t.Error("the append's message didn't travel on stdin")
	}

	want := make([]string, 0, 2*len(gitConfig))
	for _, kv := range gitConfig {
		want = append(want, "-c", kv)
	}
	calls := gitCalls(t, gitlog)
	if len(calls) < 5 {
		t.Fatalf("only %d git invocations logged", len(calls))
	}
	for _, call := range calls {
		all := strings.Join(call.args, "\x00")
		if strings.Join(call.args[:min(len(want), len(call.args))], "\x00") != strings.Join(want, "\x00") {
			t.Errorf("git ran without the pinned -c set first: %q", call.args)
		}
		if !strings.Contains(all, "\x00--git-dir="+store+"\x00") {
			t.Errorf("git ran without the store as its repository: %q", call.args)
		}
		if strings.Contains(all, marker) || strings.Contains(all, src.WorkTree) {
			t.Errorf("git's argv names an attempt's text or the work tree: %q", call.args)
		}
	}
	if exists(marker) {
		t.Fatal("an entry's text ran as a command")
	}
}
