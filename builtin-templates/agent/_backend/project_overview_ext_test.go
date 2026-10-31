package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The repo rows: the provider's stats, kept 5 minutes; a provider that
// answers 501 to them, or doesn't list the cap, gives the default branch's
// checks instead (stats false); a refusal is the row's err.
func TestOverviewSCMStats(t *testing.T) {
	t.Run("cached", func(t *testing.T) {
		fx := newCoordFix(t)
		s := withStats(t, fx.scm, true)
		s.stats["acme/web"] = &scmRepoStats{Repo: "acme/web", OpenPulls: intp(2), MergedPulls: &scmMergedPulls{Days: 7, Count: 1}}
		for i := 0; i < 2; i++ {
			if _, ov := overviewOf(t, fx.mux, asAlice, fx.p.ID, ""); !ov.SCM.Stats || len(ov.SCM.Repos) != 1 || *ov.SCM.Repos[0].OpenPulls != 2 {
				t.Fatalf("read %d: %+v", i, ov.SCM)
			}
		}
		if n := s.count(); n != 1 {
			t.Errorf("two reads asked the provider %d times (want 1: kept 5 minutes)", n)
		}
		// a field the host can't tell stays absent
		_, ov := overviewOf(t, fx.mux, asAlice, fx.p.ID, "")
		if r := ov.SCM.Repos[0]; r.OpenIssues != nil || r.Checks != nil || r.URL != "" || r.DefaultBranch != "main" || *r.Merged7d != 1 {
			t.Errorf("a partial answer: %+v", r)
		}
		forgetOverviewRepos()
		overviewOf(t, fx.mux, asAlice, fx.p.ID, "")
		if n := s.count(); n != 2 {
			t.Errorf("after the cache: %d reads", n)
		}
	})
	t.Run("501", func(t *testing.T) {
		fx := newCoordFix(t)
		s := withStats(t, fx.scm, true)
		s.unsupported = true
		fx.scm.SetChecks("acme/web", "main", &scmChecks{SHA: "abc", State: "failure", Checks: []scmCheck{},
			WorkflowRuns: []scmWorkflowRun{{ID: "1", Name: "ci", Conclusion: "success", URL: "https://github.com/acme/web/actions/runs/1"},
				{ID: "2", Name: "test", Conclusion: "failure", URL: "https://github.com/acme/web/actions/runs/2"}}})
		_, ov := overviewOf(t, fx.mux, asAlice, fx.p.ID, "")
		if ov.SCM.Stats || len(ov.SCM.Repos) != 1 {
			t.Fatalf("a provider answering 501: %+v", ov.SCM)
		}
		r := ov.SCM.Repos[0]
		if r.Checks == nil || r.Checks.State != "failure" || r.Checks.URL != "https://github.com/acme/web/actions/runs/2" ||
			r.OpenPulls != nil || r.DefaultBranch != "main" || r.Err != "" {
			t.Errorf("the fallback row: %+v checks %+v", r, r.Checks)
		}
		overviewOf(t, fx.mux, asAlice, fx.p.ID, "")
		if n, checks := s.count(), len(fx.scm.Requests("GET /checks")); n != 1 || checks != 1 {
			t.Errorf("the fallback kept: %d stats reads, %d checks reads (want 1, 1)", n, checks)
		}
	})
	t.Run("no cap", func(t *testing.T) {
		fx := newCoordFix(t)
		s := withStats(t, fx.scm, false)
		_, ov := overviewOf(t, fx.mux, asAlice, fx.p.ID, "")
		if ov.SCM.Stats || ov.SCM.Provider == nil || len(ov.SCM.Repos) != 1 || ov.SCM.Repos[0].Checks == nil || ov.SCM.Repos[0].Checks.State != "none" {
			t.Fatalf("a provider without the cap: %+v", ov.SCM)
		}
		if n := s.count(); n != 0 {
			t.Errorf("asked a provider without the cap for stats %d times", n)
		}
	})
	t.Run("refused", func(t *testing.T) {
		fx := newCoordFix(t)
		withStats(t, fx.scm, true) // no stats for acme/web: 404
		_, ov := overviewOf(t, fx.mux, asAlice, fx.p.ID, "")
		if r := ov.SCM.Repos[0]; !strings.Contains(r.Err, "no such repo") || r.Repo != "acme/web" || !ov.SCM.Stats {
			t.Errorf("a refused read: %+v (stats %v)", r, ov.SCM.Stats)
		}
		// the provider unreachable: no provider, the rows as the project knows them
		forgetOverviewRepos()
		forgetSCMHellos()
		fx.scm.FailNext("GET /hello", &scmError{})
		_, ov = overviewOf(t, fx.mux, asAlice, fx.p.ID, "")
		if ov.SCM.Provider != nil || ov.SCM.Stats || len(ov.SCM.Repos) != 1 || ov.SCM.Repos[0].Err == "" || ov.SCM.Repos[0].DefaultBranch != "main" {
			t.Errorf("an unreachable provider: %+v", ov.SCM)
		}
	})
}

// The sandbox: the manager's view of it, with `usage` (D215) passed through
// as the manager sent it — absent when it sends none — and the commands
// the agent runs in it now.
func TestOverviewSandbox(t *testing.T) {
	fx := newCoordFix(t)
	const usage = `{"at":1793445000000,"cpuPct":63.5,"memBytes":2791728742,"memMaxBytes":4429185024,"pids":12,"series":[{"t":1793444940000,"cpu":41.5,"mem":2600000000}]}`
	var inject atomic.Bool
	wrap := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rest, ok := strings.CutPrefix(r.URL.Path, "/sbx/sandboxes/")
			if !inject.Load() || r.Method != "GET" || !ok || strings.Contains(rest, "/") {
				h.ServeHTTP(w, r)
				return
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			var m map[string]any
			if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &m) != nil {
				w.WriteHeader(rec.Code)
				_, _ = w.Write(rec.Body.Bytes())
				return
			}
			m["usage"], m["diskBytes"] = json.RawMessage(usage), 3328599654
			fakeJSON(w, 200, m)
		})
	}
	bindSbxWith(t, wrap, "apps/cs")
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "sb-web"})
	ref := sandboxRef("apps/cs", box.ID)
	ovExec(t, fx.ag.db, `UPDATE projects SET sandbox_ref=? WHERE id=?`, ref, fx.p.ID)
	ovExec(t, fx.ag.db, `INSERT INTO sandbox_jobs (root_id, job, run_id, ref, command, state, created_ms) VALUES (1, 1, 1, ?, 'make test', 'running', 1),
		(1, 2, 1, ?, 'ls', 'exited', 1)`, ref, ref)
	inject.Store(true)
	_, ov := overviewOf(t, fx.mux, asAlice, fx.p.ID, "")
	sb := ov.Sandbox
	if sb == nil || sb.Ref != ref || sb.Name != "sb-web" || sb.State != box.State || sb.Commands != 1 || sb.Err != "" {
		t.Fatalf("the sandbox: %+v", sb)
	}
	if sb.DiskBytes == nil || *sb.DiskBytes != 3328599654 || string(sb.Usage) != usage {
		t.Errorf("passed through: diskBytes %v usage %s", sb.DiskBytes, sb.Usage)
	}
	// a viewer sees it, as in the status (read as the project's owner)
	ovExec(t, fx.ag.db, `INSERT INTO project_members (project_id, user, role) VALUES (?, 'dave', ?)`, fx.p.ID, roleViewer)
	projACL.flush(fx.p.ID)
	if _, ov := overviewOf(t, fx.mux, asDave, fx.p.ID, ""); ov.Sandbox == nil || ov.Sandbox.Name != "sb-web" || string(ov.Sandbox.Usage) != usage {
		t.Errorf("a viewer's sandbox: %+v", ov.Sandbox)
	}
	// a manager that sends no usage: none
	inject.Store(false)
	w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/projects/%d/overview", fx.p.ID), nil)
	if strings.Contains(w.Body.String(), `"usage"`) || strings.Contains(w.Body.String(), `"diskBytes"`) {
		t.Errorf("usage without a manager's: %s", w.Body)
	}
	// a sandbox the manager no longer has: its state unknown, why in err
	ovExec(t, fx.ag.db, `UPDATE projects SET sandbox_ref=? WHERE id=?`, sandboxRef("apps/cs", "gone"), fx.p.ID)
	if _, ov := overviewOf(t, fx.mux, asAlice, fx.p.ID, ""); ov.Sandbox == nil || ov.Sandbox.State != "unknown" || ov.Sandbox.Err == "" {
		t.Errorf("a gone sandbox: %+v", ov.Sandbox)
	}
}
