package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// scm_pr's failing checks: each job's log excerpt is its tail, ≤ 2 KiB
// ("…" counted), the excerpts ≤ 8 KiB in all with the headers inside the
// frame's 8 KiB — so the last logged job keeps the end of its log, where
// the failure is; a status with no description has no dangling dash.
func TestCoordLogBounds(t *testing.T) {
	fx := newCoordFix(t)
	run := fx.coordinator(t, asAlice, fx.p.ID)
	k, _ := fx.addTask(t, fx.p, 1, "alice")
	prs, _ := json.Marshal([]TaskPR{{Repo: "acme/web", Number: 42, State: "open", HeadSHA: "abc1234"}})
	_ = fx.ag.db.setTask(k.ID, map[string]any{"prs": string(prs), "phase": phasePR})
	fx.scm.AddPull("acme/web", &scmPull{Number: 42, Title: "Fix login", State: "open",
		URL: "https://github.com/acme/web/pull/42", Head: scmRef{Ref: k.Branch, SHA: "abc1234"}, Base: scmRef{Ref: "main"}})
	var jobs []scmJob
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("j%d", i)
		jobs = append(jobs, scmJob{ID: id, Name: fmt.Sprintf("test-%d", i), Status: "completed", Conclusion: "failure",
			URL: "https://github.com/acme/web/actions/runs/7/job/" + id})
		var b strings.Builder
		for n := 0; b.Len() < 5000; n++ {
			fmt.Fprintf(&b, "%s line %d of the build output\n", id, n)
		}
		fx.scm.SetJobLog(id, b.String()+"--- FAIL: TestEnd"+id+"\n", false)
	}
	fx.scm.SetChecks("acme/web", "abc1234", &scmChecks{SHA: "abc1234", State: "failure", Checks: []scmCheck{},
		Statuses:     []scmStatus{{Context: "deploy/preview", State: "failure"}},
		WorkflowRuns: []scmWorkflowRun{{ID: "7", Name: "ci", Status: "completed", Conclusion: "failure", Jobs: jobs}}})
	out, err := fx.tool(t, run, "scm_pr", map[string]any{"task": 1})
	if err != nil {
		t.Fatal(err)
	}
	const open = "[untrusted — from github.com: the failing checks: their jobs, failing steps and the end of each log]\n"
	i := strings.Index(out, open)
	j := strings.Index(out[i+1:], "\n[end of untrusted text]")
	if i < 0 || j < 0 {
		t.Fatalf("no checks frame:\n%s", out)
	}
	frame := out[i+len(open) : i+1+j]
	if len(frame) > 8<<10 {
		t.Fatalf("the frame holds %d bytes, over 8 KiB", len(frame))
	}
	all := 0
	for _, sec := range strings.Split(frame, "== ")[1:] {
		head, excerpt, _ := strings.Cut(sec, "\n")
		excerpt = strings.TrimRight(excerpt, "\n")
		if len(excerpt) > 2<<10 {
			t.Errorf("%s: an excerpt of %d bytes, over 2 KiB", head, len(excerpt))
		}
		all += len(excerpt)
	}
	if all > 8<<10 {
		t.Errorf("the excerpts hold %d bytes, over 8 KiB", all)
	}
	for _, want := range []string{"--- FAIL: TestEndj1", "--- FAIL: TestEndj4", "== ci › test-5"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the frame says no %q:\n%s", want, frame)
		}
	}
	if !strings.HasSuffix(frame, "\n== deploy/preview: failure") {
		t.Errorf("the status line isn't the frame's last:\n%s", frame)
	}
	if strings.Contains(frame, "TestEndj5") || strings.Contains(frame, "failure — ") {
		t.Errorf("a fifth job's log, or a dangling dash:\n%s", frame)
	}
}
