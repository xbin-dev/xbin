package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// docsEventV1 is docs/scm.md §Delivery's event v1, as the docs print it
// (its elisions filled in). The agent template's TestSCMEventV1DocsShape
// holds the consumer's side to the same literal.
const docsEventV1 = `{
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

// What the provider delivers is the documented event v1: the docs' literal
// is the event type's (no unknown field), and a check suite rendered for a
// person's subscription carries exactly the docs' keys — for, forPid and
// subs the consumer's own — with data.checks in the docs' words.
func TestEventV1DocsShape(t *testing.T) {
	dec := json.NewDecoder(bytes.NewReader([]byte(docsEventV1)))
	dec.DisallowUnknownFields()
	var doc event
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("the docs' event v1 isn't the event type's: %v", err)
	}
	e := one(t, "check_suite", "check_suite", kindChecks, "completed")
	g := &group{consumer: "apps/agent", forWhom: "user:alice", person: "alice", pid: "p_8d1f",
		subs: []*subscription{{ID: "s1", Repo: "acme/web", Branches: []string{fxBranch}, Key: "task:3:7:web", For: "user:alice", Consumer: "apps/agent", Person: "alice", PID: "p_8d1f"}}}
	raw := e.render(g)
	keysOf := func(b []byte) ([]string, map[string]json.RawMessage) {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out, m
	}
	got, m := keysOf(raw)
	want, dm := keysOf([]byte(docsEventV1))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delivered keys %v, the docs' %v", got, want)
	}
	for _, k := range []string{"scm", "ref", "actor"} {
		a, _ := keysOf(m[k])
		b, _ := keysOf(dm[k])
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s: delivered keys %v, the docs' %v", k, a, b)
		}
	}
	var out struct {
		For, ForPid string
		Subs        []string
		Data        map[string]map[string]json.RawMessage
	}
	_ = json.Unmarshal(raw, &out)
	if out.For != "user:alice" || out.ForPid != "p_8d1f" || !reflect.DeepEqual(out.Subs, []string{"task:3:7:web"}) {
		t.Fatalf("for %q forPid %q subs %v", out.For, out.ForPid, out.Subs)
	}
	var dd struct {
		Data map[string]map[string]json.RawMessage
	}
	_ = json.Unmarshal([]byte(docsEventV1), &dd)
	for k := range out.Data["checks"] {
		if _, ok := dd.Data["checks"][k]; !ok {
			t.Errorf("data.checks.%s isn't the docs'", k)
		}
	}
}
