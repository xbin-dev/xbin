package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ciClone is a clone of a fresh local origin in the fixture's sandbox (at
// <workdir>/web), with branch old pushed an hour ago and branch feature
// just now — real pushes, so the remote-tracking refs' reflogs say "update
// by push" — and a third remote at a host no provider serves, pushed to
// now. Then origin's URL is made a github.com one with userinfo (the
// common https://user:token@ form). Answers feature's head.
func (fx *ciFix) ciClone(t *testing.T) (dir, feature string) {
	t.Helper()
	url, _ := bareOrigin(t)
	other, _ := bareOrigin(t)
	ciGit(t, fx.box.Workdir, nil, "clone", "-q", url, "web")
	dir = filepath.Join(fx.box.Workdir, "web")
	commit := func(name string, env []string) string {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ciGit(t, dir, env, "add", name)
		ciGit(t, dir, env, "commit", "-q", "-m", name)
		return ciGit(t, dir, nil, "rev-parse", "HEAD")
	}
	hourAgo := []string{fmt.Sprintf("GIT_COMMITTER_DATE=@%d +0000", time.Now().Add(-time.Hour).Unix())}
	ciGit(t, dir, nil, "checkout", "-q", "-b", "old")
	commit("old.txt", hourAgo)
	ciGit(t, dir, hourAgo, "push", "-q", "origin", "old")
	ciGit(t, dir, nil, "checkout", "-q", "-b", "feature")
	feature = commit("feature.txt", nil)
	ciGit(t, dir, nil, "push", "-q", "-u", "origin", "feature")
	ciGit(t, dir, nil, "remote", "add", "other", other)
	ciGit(t, dir, nil, "push", "-q", "other", "feature:elsewhere")
	ciGit(t, dir, nil, "remote", "set-url", "origin", "https://x-access-token:s3cr3t-t0ken@github.com/acme/web.git")
	ciGit(t, dir, nil, "remote", "set-url", "other", "https://gitlab.example/acme/web.git")
	return dir, feature
}

// sbxRuns counts the commands run in the fixture's sandbox.
func (fx *ciFix) sbxRuns() int {
	n := 0
	for _, c := range fx.m.Calls() {
		if c.Method == "POST" && strings.HasSuffix(c.Path, "/run") {
			n++
		}
	}
	return n
}

func (fx *ciFix) turnEnd(t *testing.T, run *Run) {
	t.Helper()
	if err := fx.ag.db.Tx(func(t *DB) error { runTurnEnd(t, run, turnAnswered, outcomeAnswered, ""); return nil }); err != nil {
		t.Fatal(err)
	}
}

// A run's turn end finds what it pushed since the turn began — from git's
// own record, in its own sandbox — at a host a bound provider serves: one
// watch (its pull request looked up, its subscription made, read), its
// remote URL's userinfo kept nowhere. An older push, another host's, a
// project's run and a hosted run make none.
func TestPushedBranchDetection(t *testing.T) {
	fx := newCIFix(t)
	_, head := fx.ciClone(t)
	fx.scm.AddPull("acme/web", &scmPull{Number: 7, State: "open", Head: scmRef{Ref: "feature", SHA: head}})
	fx.scm.SetChecks("acme/web", "feature", ciChecks(head))
	root := fx.conv(t, "alice", true)
	if _, err := fx.ag.db.q.Exec(`UPDATE runs SET turn_started=? WHERE id=?`, time.Now().Add(-time.Minute).Unix(), root); err != nil {
		t.Fatal(err)
	}
	run, _ := fx.ag.db.getRun(root)
	fx.turnEnd(t, run)
	ciWait(t, "the pushed branch's watch, read", func() bool {
		ws := fx.live(root)
		return len(ws) == 1 && ws[0].FetchedMs > 0 && ws[0].PR == 7
	})
	w := fx.live(root)[0]
	if w.Source != ciPushed || w.Repo != "acme/web" || w.Ref != "feature" || w.SHA != head || w.RunID != root || w.Host != "github.com" || w.State != ciFailure {
		t.Fatalf("the watch: %s", ciDump(fx.live(root)))
	}
	ciWait(t, "its subscription", func() bool { return len(fx.scm.Subscriptions()) == 1 })
	sub := fx.scm.Subscriptions()[0]
	if sub.Key != w.SubKey || strings.Join(sub.Branches, ",") != "feature" || strings.Join(sub.Kinds, ",") != "checks,pull,workflow,job,check,push" {
		t.Fatalf("the subscription: %+v", sub)
	}
	for _, tbl := range []string{"ci_watch", "steps", "messages", "settings"} {
		var n int
		_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM ` + tbl + ` WHERE CAST(` + map[string]string{"ci_watch": "snapshot || host || repo || error",
			"steps": "detail", "messages": "content", "settings": "value"}[tbl] + ` AS TEXT) LIKE '%s3cr3t%'`).Scan(&n)
		if n != 0 {
			t.Errorf("the remote's userinfo is in %s", tbl)
		}
	}
	// the turn ending again: nothing new pushed, the same one watch
	before := fx.sbxRuns()
	fx.turnEnd(t, run)
	ciWait(t, "the second look", func() bool { return fx.sbxRuns() > before })
	time.Sleep(50 * time.Millisecond)
	if ws := fx.ag.db.ciWatchesWhere(`WHERE root_run=?`, root); len(ws) != 1 {
		t.Fatalf("a second look: %s", ciDump(ws))
	}
	// a project's run and a hosted one: no look at all
	task := runAs(t, fx.ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: originProject, OriginID: 1}, false)
	fx.bind(t, task, "alice")
	tr, _ := fx.ag.db.getRun(task)
	before = fx.sbxRuns()
	fx.turnEnd(t, tr)
	fx.turnEnd(t, &Run{ID: teamIDBase + 1, Origin: "chat"})
	time.Sleep(100 * time.Millisecond)
	if fx.sbxRuns() != before || len(fx.live(task)) != 0 {
		t.Fatalf("a project's or a hosted run looked: %d runs", fx.sbxRuns()-before)
	}
	// unbound: nothing either
	t.Setenv("XBIN_IFACE_SCM", "")
	fx.turnEnd(t, run)
	time.Sleep(100 * time.Millisecond)
	if fx.sbxRuns() != before {
		t.Fatal("looked with no scm provider bound")
	}
}

// A coding agent child's push, after its parent's turn ended, is found at
// the child's own turn end, in the child's sandbox: the watch is the
// root's, run_id the child.
func TestPushedBranchDetectionChild(t *testing.T) {
	fx := newCIFix(t)
	_, head := fx.ciClone(t)
	root := fx.conv(t, "alice", false)
	cfg, _ := json.Marshal(Config{Engine: engineHarness, Harness: &HarnessConfig{Provider: "claude", Ref: sandboxRef("apps/cs", fx.box.ID), Cwd: fx.box.Workdir, By: "alice"}})
	kid, err := fx.ag.db.createRun("kid", string(cfg), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.ag.db.q.Exec(`UPDATE runs SET engine=?, turn_started=? WHERE id=?`, engineHarness, time.Now().Add(-time.Minute).Unix(), kid); err != nil {
		t.Fatal(err)
	}
	parent, _ := fx.ag.db.getRun(root)
	fx.turnEnd(t, parent) // the parent's turn ended first: no sandbox, nothing to look at
	child, _ := fx.ag.db.getRun(kid)
	fx.turnEnd(t, child)
	ciWait(t, "the child's push watched", func() bool { return len(fx.live(root)) == 1 })
	w := fx.live(root)[0]
	if w.RootRun != root || w.RunID != kid || w.SHA != head || w.Ref != "feature" {
		t.Fatalf("the watch: %s", ciDump(fx.live(root)))
	}
	if len(fx.live(kid)) != 0 {
		t.Fatal("a watch keyed by the child")
	}
	var v CIView
	ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/ci", kid), &v)
	if v.Root != root || len(v.Watches) != 1 || v.Watches[0].Run != kid {
		t.Fatalf("the child's CI is its root's: %+v", v)
	}
}

// At a bot home (an unpartitioned agent here) the bot reads only what
// someone authorised: a manual watch takes a manager or the scm bot rule;
// a pushed branch — even from a faked remote and reflog — only a repo a
// project of the owner's names, or the rule; in a conversation others
// share, only the project's.
func TestCIWatchBotRule(t *testing.T) {
	real := scmBotAllowed
	fx := newCIFix(t)
	scmBotAllowed = real
	root := fx.conv(t, "alice", true)
	body := map[string]any{"repo": "acme/web", "ref": "feature"}
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/ci/watch", root), body); w.Code != 403 {
		t.Fatalf("alice names a repo for the bot: %d %s", w.Code, w.Body)
	}
	// faked: a remote-tracking ref with an "update by push" reflog entry, at github.com
	dir := filepath.Join(fx.box.Workdir, "web")
	ciGit(t, fx.box.Workdir, nil, "init", "-q", "-b", "main", "web")
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	ciGit(t, dir, nil, "add", "a")
	ciGit(t, dir, nil, "commit", "-q", "-m", "a")
	head := ciGit(t, dir, nil, "rev-parse", "HEAD")
	ciGit(t, dir, nil, "remote", "add", "origin", "https://github.com/acme/web.git")
	ciGit(t, dir, nil, "update-ref", "-m", "update by push", "refs/remotes/origin/feature", head)
	detect := func(root int64, owner string) {
		ciDetect(context.Background(), fx.ag.db, root, root, owner, sandboxRef("apps/cs", fx.box.ID), fx.box.Workdir, owner, time.Now().Add(-time.Minute).Unix())
	}
	detect(root, "alice")
	if ws := fx.live(root); len(ws) != 0 {
		t.Fatalf("a faked push made the bot read an unnamed repo: %s", ciDump(ws))
	}
	// the rule names alice for acme/*: her own conversation watches; one others share doesn't
	if w := callAs(t, fx.mux, asMgr, "PUT", "/projects/scm/bot", map[string]any{"users": []string{"alice"}, "repos": []string{"acme/*"}}); w.Code != 200 {
		t.Fatalf("the rule: %d %s", w.Code, w.Body)
	}
	shared := runAs(t, fx.ag, runStamp{Owner: "alice", Visibility: visTeam, TeamRole: roleParticipant, Origin: "chat"}, false)
	fx.bind(t, shared, "alice")
	detect(shared, "alice")
	if ws := fx.live(shared); len(ws) != 0 {
		t.Fatalf("the rule let a shared conversation's push be read: %s", ciDump(ws))
	}
	detect(root, "alice")
	if ws := fx.live(root); len(ws) != 1 || ws[0].Source != ciPushed {
		t.Fatalf("the rule's own: %s", ciDump(ws))
	}
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/ci/watch", root), map[string]any{"repo": "acme/web", "ref": "main"}); w.Code != 201 {
		t.Fatalf("alice by the rule: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/ci/watch", root), map[string]any{"repo": "other/web", "ref": "main"}); w.Code != 403 {
		t.Fatalf("a repo outside the rule: %d %s", w.Code, w.Body)
	}
	// a project of alice's names acme/web: the shared conversation's push is read too
	p := &Project{Name: "Web", Slug: "web", Kind: projPersonal, Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, SCM: "apps/scm-github", State: projActive}
	if err := fx.ag.db.insertProject(p); err != nil {
		t.Fatal(err)
	}
	if err := fx.ag.db.insertRepo(&ProjectRepo{ProjectID: p.ID, Slug: "web", Repo: "acme/web", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	if w := callAs(t, fx.mux, asMgr, "PUT", "/projects/scm/bot", map[string]any{"users": []string{}, "repos": []string{}}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	detect(shared, "alice")
	if ws := fx.live(shared); len(ws) != 1 {
		t.Fatalf("the project's repo from a shared conversation: %s", ciDump(ws))
	}
	// bob takes no part in that project: his conversation's push isn't read
	bobs := fx.conv(t, "bob", true)
	detect(bobs, "bob")
	if ws := fx.live(bobs); len(ws) != 0 {
		t.Fatalf("bob's push of alice's project's repo: %s", ciDump(ws))
	}
}

// The command's lines are parsed in memory: userinfo gone, scp-like and
// ssh remotes understood, odd branches and URLs skipped.
func TestCIParsePushes(t *testing.T) {
	out := strings.Join([]string{
		"/w/web\tfeature\t" + ciSHA1 + "\thttps://u:p@github.com/acme/web.git",
		"/w/web\tfix/x\t" + ciSHA2 + "\tgit@github.com:acme/api.git",
		"/w/web\tfeat\t" + ciSHA2 + "\tssh://git@GitHub.com/acme/cli",
		"/w/web\tbad branch\t" + ciSHA1 + "\thttps://github.com/acme/web.git",
		"/w/web\tx\tnot-a-sha\thttps://github.com/acme/web.git",
		"/w/web\ty\t" + ciSHA1 + "\tfile:///tmp/origin.git",
		"/w/web\tz\t" + ciSHA1 + "\thttps://github.com/acme/web/extra",
		"garbage",
	}, "\n")
	got := ciParsePushes(out)
	b, _ := json.Marshal(got)
	want := `[{"Host":"github.com","Repo":"acme/web","Branch":"feature","SHA":"` + ciSHA1 + `"},{"Host":"github.com","Repo":"acme/api","Branch":"fix/x","SHA":"` + ciSHA2 +
		`"},{"Host":"github.com","Repo":"acme/cli","Branch":"feat","SHA":"` + ciSHA2 + `"}]`
	if string(b) != want {
		t.Fatalf("parsed:\n%s\nwant\n%s", b, want)
	}
}
