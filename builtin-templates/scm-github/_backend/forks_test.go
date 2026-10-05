package main

import (
	"strings"
	"testing"
	"time"
)

// An outsider's fork with a branch named like a task's: its pull request's
// CI — workflow run, jobs, check suite, check runs — never reaches the
// task's subscription by branch. This repo's own branch does, proven by a
// pull request, the run's head repo, or the branch's head.
func TestForkCIBranches(t *testing.T) {
	ee := newEvEnv(t)
	ee.subscribe(agentC, map[string]any{"repo": "acme/web", "branches": []string{fxBranch}, "kinds": []string{"workflow", "job", "check", "checks"}})
	fork := map[string]any{"id": 4242, "full_name": "octocat/web", "owner": map[string]any{"login": "octocat"}}
	own := map[string]any{"id": 1296269, "full_name": "acme/web", "owner": map[string]any{"login": "acme"}}
	ee.gh.mu.Lock()
	ee.gh.ci.runByID["7002"] = map[string]any{"id": 7002, "head_repository": fork}
	ee.gh.ci.runByID["7004"] = map[string]any{"id": 7004, "head_repository": own}
	ee.gh.mu.Unlock()

	// The fork's: its branch doesn't exist here; GitHub lists no pull request.
	ok(t, ee.hook("workflow_run", fixtureWith(t, "workflow_run", map[string]any{"workflow_run.head_repository": fork, "workflow_run.pull_requests": []any{}})), 202)
	ok(t, ee.hook("workflow_job", fixture(t, "workflow_job_completed")), 202) // run 7001: its workflow_run said the fork
	ok(t, ee.hook("workflow_job", fixtureWith(t, "workflow_job_completed", map[string]any{"workflow_job.run_id": 7002})), 202)
	ok(t, ee.hook("check_suite", fixtureWith(t, "check_suite", map[string]any{"check_suite.pull_requests": []any{}})), 202)
	ok(t, ee.hook("check_run", fixtureWith(t, "check_run", map[string]any{"check_run.pull_requests": []any{}})), 202)
	ee.clock.advance(6 * time.Second)
	ee.deliver()
	if got := ee.agent.take(); len(got) != 0 {
		t.Fatalf("a fork's CI reached the branch's subscription: %v", got)
	}
	if n7001, n7002 := ee.gh.count("GET /repos/acme/web/actions/runs/7001"), ee.gh.count("GET /repos/acme/web/actions/runs/7002"); n7001 != 0 || n7002 != 1 {
		t.Fatalf("run lookups: 7001 %d (its workflow_run said), 7002 %d", n7001, n7002)
	}

	// This repo's own branch, no pull request yet (a minute on: "no such
	// branch" is remembered that long).
	ee.clock.advance(time.Minute)
	ee.gh.mu.Lock()
	ee.gh.ci.branches["acme/web|"+fxBranch] = fxSHA
	ee.gh.mu.Unlock()
	ok(t, ee.hook("check_suite", fixtureWith(t, "check_suite", map[string]any{"check_suite.pull_requests": []any{}, "check_suite.id": 78})), 202)
	ok(t, ee.hook("check_run", fixtureWith(t, "check_run", map[string]any{"check_run.pull_requests": []any{}})), 202)
	ok(t, ee.hook("workflow_run", fixtureWith(t, "workflow_run", map[string]any{"workflow_run.id": 7003, "workflow_run.pull_requests": []any{}})), 202)
	// The branch moves on: run 7003's job is still the branch's (its run
	// said so); 7004's GitHub's run says.
	ee.gh.mu.Lock()
	ee.gh.ci.branches["acme/web|"+fxBranch] = strings.Repeat("d", 40)
	ee.gh.mu.Unlock()
	ok(t, ee.hook("workflow_job", fixtureWith(t, "workflow_job_completed", map[string]any{"workflow_job.run_id": 7003})), 202)
	ok(t, ee.hook("workflow_job", fixtureWith(t, "workflow_job_completed", map[string]any{"workflow_job.run_id": 7004})), 202)
	// GitHub not answering is no proof.
	ee.gh.fail("GET /repos/acme/web/actions/runs/7005", 1, 502, nil, `{}`)
	ee.gh.fail("GET /repos/acme/web/branches/", 1, 502, nil, `{}`)
	ok(t, ee.hook("workflow_job", fixtureWith(t, "workflow_job_completed", map[string]any{"workflow_job.run_id": 7005, "workflow_job.head_sha": strings.Repeat("c", 40)})), 202)
	ee.clock.advance(6 * time.Second)
	ee.deliver()
	got := ee.agent.take()
	kinds := map[string]int{}
	for _, ev := range got {
		kinds[ev["kind"].(string)]++
		if get(ev, "ref.branch") != fxBranch || !strings.Contains(ev["topic"].(string), "/branch/"+fxBranch+"/") {
			t.Fatalf("%v", ev)
		}
	}
	if len(got) != 5 || kinds["checks"] != 1 || kinds["check"] != 1 || kinds["workflow"] != 1 || kinds["job"] != 2 {
		t.Fatalf("%d events: %v", len(got), kinds)
	}
}
