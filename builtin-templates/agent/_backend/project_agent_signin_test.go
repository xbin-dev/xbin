package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp/acptest"
)

// signinTaskFix is a project whose sandbox has the fake coding agent
// signed out (--require-login), and a task of it answered by that agent.
func signinTaskFix(t *testing.T) (*projFix, ProjectView, TaskView, int64) {
	t.Helper()
	fx := newProjFix(t)
	fx.m.Caps = []string{"exec", "files", "tar"}
	argv := acptest.Command("--require-login")
	fx.m.Harnesses = []fsbHarness{{ID: "fake", Title: "Fake agent (tests)", Argv: argv, Login: argv[0] + " acptest login"}}
	forgetHarnessProbes()
	t.Cleanup(func() { settleHarnesses(fx.ag.eng) })
	p := fx.newProject(t, asAlice, nil)
	tv, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "fix the header", "agent": map[string]any{"provider": "fake"}})
	fx.waitWS(t, p.ID, tv.N, wsReady)
	return fx, p, tv, runID
}

// A task whose coding agent waits for a sign-in is agent-signin — in Needs
// you, waiting for a sign-in — not a question: on its view, in the
// project's counts and overview, and in the coordinator's words, which
// name the agent and the sandbox to sign in in.
func TestAgentSigninState(t *testing.T) {
	fx, p, tv, runID := signinTaskFix(t)
	d := fx.ag.db
	pk := parkOf(t, fx.ag, runID, "login")
	// no saved sign-in in an unpartitioned agent: the card offers this task's sign-in alone, saying why
	var l hLogin
	if json.Unmarshal(pk.Harness.Login, &l) != nil || l.Save == nil || l.Save.OK || l.Save.Why != signinsLegacy {
		t.Fatalf("the park's word on saving a sign-in: %s", pk.Harness.Login)
	}
	var tw TaskView
	if w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/runs/%d/task", runID), nil); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tw) != nil {
		t.Fatalf("GET task: %d %s", w.Code, w.Body)
	}
	if tw.State != taskAgentSignin || tw.Column != colNeedsYou || tw.WaitingFor != "signin" || tw.RunStatus != statusWaiting {
		t.Fatalf("the task parked on its sign-in: state %s column %s waiting %s status %s", tw.State, tw.Column, tw.WaitingFor, tw.RunStatus)
	}
	var pv struct {
		Project ProjectView `json:"project"`
	}
	if w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/projects/%d", p.ID), nil); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &pv) != nil ||
		pv.Project.Counts[colNeedsYou] != 1 || pv.Project.Counts[colWorking] != 0 {
		t.Fatalf("the project's counts: %d %s", w.Code, w.Body)
	}
	pp, _ := d.getProject(p.ID)
	if b := loadBoard(d, pp, who{kind: whoUser, user: "alice"}, time.Now()); b.state[tv.N] != taskAgentSignin || b.col[tv.N] != colNeedsYou {
		t.Fatalf("the overview's board: %s / %s", b.state[tv.N], b.col[tv.N])
	}
	evs := d.projectEvents(p.ID, 0, 100)
	said := false
	for _, ev := range evs {
		said = said || ev.N == tv.N && ev.Kind == pevTaskState && strings.Contains(string(ev.Body), "waits for the owner to sign in to Fake agent (tests)")
	}
	if !said {
		t.Fatalf("no feed event names the sign-in: %+v", evs)
	}
	line := coordTaskLine(tw)
	if !strings.Contains(line, "waiting for the owner to sign in to Fake agent (tests) in the project's sandbox") || strings.Contains(line, "waiting for a person") {
		t.Fatalf("the coordinator's line: %s", line)
	}
	tw.Fork, tw.Harness = true, "claude"
	if s := coordStateOf(tw); s != "waiting for the owner to sign in to Claude Code in the task's own sandbox" {
		t.Fatalf("a big task's words: %s", s)
	}
	// a question is still a person's question
	if got := taskRunStatus(statusWaiting, `{"kind":"question","harness":{"message":"which one?"}}`); got != statusWaiting {
		t.Fatalf("a question's status: %s", got)
	}
	if got := taskRunStatus(statusWaiting, `{"kind":"approval","toolCalls":[{"id":"x","name":"login"}]}`); got != statusWaiting {
		t.Fatalf("an approval naming login: %s", got)
	}
	if _, st, wf := taskDerived(&ProjectTask{WS: wsReady, Phase: phaseOpen}, taskRunStatus(statusWaiting, `{"kind":"login"}`), nil, false); st != taskAgentSignin || wf != "signin" {
		t.Fatalf("taskDerived of a login park: %s %s", st, wf)
	}
	// the team board keeps the state a member sends
	if row, err := teamSanitize(BoardRow{State: taskAgentSignin, Run: partitionIDBase + 1}, "github.com"); err != nil || row.State != taskAgentSignin {
		t.Fatalf("the team board's row: %+v %v", row, err)
	}
}
