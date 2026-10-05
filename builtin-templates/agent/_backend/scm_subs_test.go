package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// eSubs is the fake provider's subscriptions that are E's (a task's, a
// project's issues) — not a CI watch's (key ci:<watch id>, its own tests:
// TestCI*), which a task's refs make beside them (API.md §CI).
func eSubs(fx *evFx) []scmSubscription {
	return slices.DeleteFunc(fx.scm.Subscriptions(), func(s scmSubscription) bool { return strings.HasPrefix(s.Key, "ci:") })
}

// ePosts counts E's POST /subscriptions at the fake provider (a CI
// watch's, posted from its own goroutine, left out).
func ePosts(fx *evFx) int {
	n := 0
	for _, r := range fx.scm.Requests("POST /subscriptions") {
		var b struct{ Key string }
		if json.Unmarshal([]byte(r.Body), &b) == nil && !strings.HasPrefix(b.Key, "ci:") {
			n++
		}
	}
	return n
}

// subsByKey is E's subscriptions at the fake provider by key.
func subsByKey(fx *evFx) map[string]scmSubscription {
	out := map[string]scmSubscription{}
	for _, s := range eSubs(fx) {
		out[s.Key] = s
	}
	return out
}

// A task's subscription is posted once its branch is on the remote (and
// replaced when a PR opens), posted again at 25 days while the task is
// open, deleted when it is cleaned up; a project's issue subscription
// follows policy.autoLabel; a provider without events keeps none and is
// asked again after 6 h.
func TestSubscriptionLifecycle(t *testing.T) {
	fx := newEvFx(t, modeLegacy, "")
	fx.addTask(1, evBranch, "") // not pushed yet
	fx.pass()
	if subs := eSubs(fx); len(subs) != 0 {
		t.Fatalf("a branch not on the remote is subscribed: %+v", subs)
	}
	k := fx.task(1)
	_ = fx.ag.db.putCheckout(ProjectCheckout{TaskID: k.ID, Repo: "web", Path: "/w", Mode: coWorktree, State: "ready", RemoteSHA: evSHA})
	fx.refs(k)
	fx.pass()
	key := fmt.Sprintf("task:%d:1:web", fx.p.ID)
	s, ok := subsByKey(fx)[key]
	if !ok || s.Repo != "acme/web" || !slices.Equal(s.Branches, []string{evBranch}) || len(s.PRs) != 0 ||
		!slices.Equal(s.Kinds, scmTaskKinds) || !slices.Contains(s.Kinds, scmKindWorkflow) {
		t.Fatalf("the task's subscription: %+v (all %+v)", s, eSubs(fx))
	}
	posts := ePosts(fx)
	fx.pass()
	if n := ePosts(fx); n != posts {
		t.Fatalf("posted again without a change: %d → %d", posts, n)
	}
	// a PR opens: the same key, now with the PR
	b, _ := json.Marshal([]TaskPR{{Repo: "acme/web", Number: 42, State: "open", HeadSHA: evSHA, Checks: "none"}})
	_ = fx.ag.db.setTask(k.ID, map[string]any{"prs": string(b), "phase": phasePR})
	fx.refs(k)
	fx.pass()
	subs := eSubs(fx)
	if len(subs) != 1 || subs[0].Key != key || !slices.Equal(subs[0].PRs, []int{42}) || subs[0].ID != s.ID {
		t.Fatalf("after the PR opened: %+v", subs)
	}
	// 25 days on: posted again
	posts = ePosts(fx)
	fx.advance(25*24*time.Hour + time.Minute)
	fx.pass()
	if n := ePosts(fx); n != posts+1 {
		t.Fatalf("not posted again at 25 days: %d → %d", posts, n)
	}
	// cleaned up: deleted there and here
	_ = fx.ag.db.Tx(func(t *DB) error {
		cur, _ := t.taskByID(k.ID)
		cur.WS, cur.Phase = wsCleaned, phaseMerged
		_ = t.setTask(cur.ID, map[string]any{"ws": wsCleaned, "phase": phaseMerged})
		onTaskChange(t, fx.p, cur, "ws")
		return nil
	})
	fx.pass()
	var left int
	_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM scm_subs`).Scan(&left)
	var refs int
	_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM project_refs WHERE project_id=?`, fx.p.ID).Scan(&refs)
	if len(eSubs(fx)) != 0 || left != 0 || refs != 0 || len(fx.scm.Requests("DELETE /subscriptions/"+s.ID)) != 1 {
		t.Fatalf("after the cleanup: provider %+v, %d rows, %d refs", eSubs(fx), left, refs)
	}

	t.Run("a task no longer open isn't posted again", func(t *testing.T) {
		fx := newEvFx(t, modeLegacy, "")
		fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
		fx.pass()
		_ = fx.ag.db.setTask(fx.task(1).ID, map[string]any{"phase": phaseClosed})
		fx.advance(25*24*time.Hour + time.Minute)
		fx.pass()
		if subs := eSubs(fx); len(subs) != 0 {
			t.Fatalf("a closed task's subscription kept: %+v", subs)
		}
	})
	t.Run("issues follow autoLabel", func(t *testing.T) {
		fx := newEvFx(t, modeLegacy, `{"autoLabel":"xbin"}`)
		fx.pass()
		key := fmt.Sprintf("issues:%d:web", fx.p.ID)
		s, ok := subsByKey(fx)[key]
		if !ok || !s.Issues || !slices.Equal(s.Kinds, []string{scmKindIssue}) || s.Repo != "acme/web" {
			t.Fatalf("the issue subscription: %+v", eSubs(fx))
		}
		_, _ = fx.ag.db.q.Exec(`UPDATE projects SET policy='{}' WHERE id=?`, fx.p.ID)
		fx.pass()
		fx.pass() // marked, then deleted
		if subs := eSubs(fx); len(subs) != 0 {
			t.Fatalf("with autoLabel unset: %+v", subs)
		}
	})
	t.Run("team definitions subscribe to nothing", func(t *testing.T) {
		fx := newEvFx(t, modeLegacy, `{"autoLabel":"xbin"}`)
		_, _ = fx.ag.db.q.Exec(`UPDATE projects SET kind=? WHERE id=?`, projTeam, fx.p.ID)
		fx.p.Kind = projTeam
		fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
		fx.pass()
		if subs := eSubs(fx); len(subs) != 0 {
			t.Fatalf("a team definition subscribed: %+v", subs)
		}
	})
	t.Run("a provider without events", func(t *testing.T) {
		fx := newEvFx(t, modeLegacy, "")
		fx.scm.mu.Lock()
		fx.scm.Caps = slices.DeleteFunc(slices.Clone(fx.scm.Caps), func(c string) bool { return c == scmCapEvents })
		fx.scm.mu.Unlock()
		fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
		fx.pass()
		var state, errText string
		var next int64
		_ = fx.ag.db.q.QueryRow(`SELECT state, next_ms, error FROM scm_subs`).Scan(&state, &next, &errText)
		if state != subPost || next != fx.now()+(6*time.Hour).Milliseconds() || !strings.Contains(errText, "unsupported") {
			t.Fatalf("the row: %s, next in %s, %q", state, time.Duration(next-fx.now())*time.Millisecond, errText)
		}
		// and its reads still run (polling stands in)
		fx.advance(2 * time.Minute)
		fx.pass()
		if r := fx.pollRow(1, scmPollChecks); r.Step != 1 {
			t.Fatalf("no read without events: %+v", r)
		}
	})
	t.Run("a task's conversation deleted", func(t *testing.T) {
		fx := newEvFx(t, modeLegacy, "")
		k := fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
		fx.pass()
		if err := fx.ag.db.Tx(func(t *DB) error { return projectRunDeleted(t, k.RunID) }); err != nil {
			t.Fatal(err)
		}
		fx.pass()
		var polls int
		_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM scm_poll`).Scan(&polls)
		if len(eSubs(fx)) != 0 || polls != 0 {
			t.Fatalf("after the delete: %+v, %d poll rows", eSubs(fx), polls)
		}
	})
}
