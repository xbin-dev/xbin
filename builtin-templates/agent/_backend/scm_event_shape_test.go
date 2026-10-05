package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// scmDocsEventV1 is /docs/scm.md §Delivery's event v1, as the docs print it
// (its elisions filled in). The scm-github tile's TestEventV1DocsShape
// holds the provider's side to the same literal.
const scmDocsEventV1 = `{
  "protocol": 1,
  "eventId": "scm:github.com:72d3162e-cc78-11e3-81ab-4c9367dc0958",
  "for": "user:alice",
  "forPid": "p_8d1f",
  "scm": {"provider": "apps/scm-github", "host": "github.com"},
  "kind": "checks",
  "action": "completed",
  "topic": "scm/github.com/acme/web/branch/xbin/k3x9/3-fix-login/checks.completed",
  "repo": "acme/web",
  "private": true,
  "ref": {"branch": "xbin/k3x9/3-fix-login", "sha": "9fceb02d0ae598e95dc970b74767f19372d61af8", "pr": 42},
  "actor": {"login": "github-actions[bot]", "association": "NONE", "bot": true, "self": false},
  "conclusion": "failure",
  "summary": "test failed on xbin/k3x9/3-fix-login",
  "url": "https://github.com/acme/web/pull/42/checks",
  "at": 1789990000000,
  "subs": ["task:3:7:web"],
  "data": {"checks": {"suite": "77", "headSha": "9fceb02d0ae598e95dc970b74767f19372d61af8",
           "runs": [{"id": "88001", "name": "test (ubuntu)", "conclusion": "failure", "url": "https://github.com/acme/web/actions/runs/7/job/88001"}]}}
}`

// The agent's intake reads every field of the documented event v1 under
// the documented names: nothing in the docs' literal is unknown to
// scmEvent, every field lands, and what it writes back has the same keys.
func TestSCMEventV1DocsShape(t *testing.T) {
	dec := json.NewDecoder(bytes.NewReader([]byte(scmDocsEventV1)))
	dec.DisallowUnknownFields()
	var ev scmEvent
	if err := dec.Decode(&ev); err != nil {
		t.Fatalf("the docs' event v1 isn't scmEvent's: %v", err)
	}
	sha := "9fceb02d0ae598e95dc970b74767f19372d61af8"
	want := scmEvent{Protocol: scmProtocol, EventID: "scm:github.com:72d3162e-cc78-11e3-81ab-4c9367dc0958", For: "user:alice", ForPid: "p_8d1f",
		SCM: scmEventSource{Provider: "apps/scm-github", Host: "github.com"}, Kind: scmKindChecks, Action: "completed",
		Topic: "scm/github.com/acme/web/branch/xbin/k3x9/3-fix-login/checks.completed", Repo: "acme/web", Private: true,
		Ref:   scmEventRef{Branch: "xbin/k3x9/3-fix-login", SHA: sha, PR: 42},
		Actor: scmActor{Login: "github-actions[bot]", Association: "NONE", Bot: true}, Conclusion: "failure",
		Summary: "test failed on xbin/k3x9/3-fix-login", URL: "https://github.com/acme/web/pull/42/checks", At: 1789990000000,
		Subs: []string{"task:3:7:web"}}
	got := ev
	got.Data = nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded:\n%+v\nwant:\n%+v", got, want)
	}
	var checks struct {
		Suite   string `json:"suite"`
		HeadSHA string `json:"headSha"`
		Runs    []struct {
			ID, Name, Conclusion, URL string
		} `json:"runs"`
	}
	if err := json.Unmarshal(ev.Data[scmKindChecks], &checks); err != nil || checks.Suite != "77" || checks.HeadSHA != sha ||
		len(checks.Runs) != 1 || checks.Runs[0].ID != "88001" {
		t.Fatalf("data.checks: %+v %v", checks, err)
	}
	keys := func(b []byte) []string {
		var m map[string]json.RawMessage
		_ = json.Unmarshal(b, &m)
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	back, _ := json.Marshal(&ev)
	if a, b := keys(back), keys([]byte(scmDocsEventV1)); !reflect.DeepEqual(a, b) {
		t.Fatalf("scmEvent's keys %v, the docs' %v", a, b)
	}
	// the kinds the docs' table lists are the intake's names
	for _, k := range []string{scmKindPull, scmKindChecks, scmKindComment, scmKindReview, scmKindPush, scmKindIssue, scmKindWorkflow, scmKindJob, scmKindCheck} {
		switch k {
		case "pull", "checks", "comment", "review", "push", "issue", "workflow", "job", "check":
		default:
			t.Errorf("kind %q isn't one of the docs' table", k)
		}
	}
	if !scmProgress("workflow") || !scmProgress("job") || !scmProgress("check") || scmProgress("checks") {
		t.Fatal("the progress kinds are workflow, job and check")
	}
}
