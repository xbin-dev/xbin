package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// covers D229 — bx org agents and bx agent connection send what the API
// takes (a rule by flag; "none" is the empty list; no org is the
// workspace's bounds), print what it answers, and exit 6 against an xbind
// without agent constraints.
func TestBxOrgAgents(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, strings.TrimSpace(r.Method+" "+r.URL.RequestURI()+" "+string(b)))
		switch {
		case r.URL.Path == "/api/xbin/orgs":
			_, _ = io.WriteString(w, `{"orgs":[{"id":"acme"},{"id":"finance","agents":{"workspaceAgent":"on","harnesses":["claude"],"models":"private"}}]}`)
		case r.URL.Path == "/api/xbin/agents/policy":
			_, _ = io.WriteString(w, `{"workspaceAgent":"on","harnesses":"any","models":["local"],"credentials":"connector","knownHarnesses":["claude"],"canSet":true}`)
		case r.URL.Path == "/api/xbin/agents/connection":
			_, _ = io.WriteString(w, `{"orgs":[{"id":"finance","name":"Finance","kind":"org","state":"blocked","reason":"Finance allows private models only","tiles":1,"connected":0},
				{"id":"workspace","name":"Not in an org","kind":"workspace","state":"connected","tiles":1,"connected":1}],
				"tiles":[{"tile":"apps/ledger","org":"finance","state":"blocked","reason":"Finance allows private models only"},{"tile":"apps/notes","org":"workspace","state":"connected"},
				{"tile":"apps/secret","org":"","state":"no-access"}],
				"counts":{"summary":"1 tile connected · 2 not connected"},
				"whatIf":{"connect":[{"tile":"apps/ledger"}],"disconnect":[],"counts":{"summary":"2 tiles · 1 org connected · 1 not connected"}}}`)
		default:
			_, _ = io.WriteString(w, `{"agents":{"workspaceAgent":"off","harnesses":[],"models":"private"},"bounds":{"workspaceAgent":"on","harnesses":"any","models":"any"}}`)
		}
	}))
	defer srv.Close()
	t.Setenv("XBIN_URL", srv.URL)
	t.Setenv("XBIN_TOKEN", "t")
	var out bytes.Buffer
	oOut := dcOut
	dcOut = &out
	defer func() { dcOut = oOut }()

	for _, args := range [][]string{
		{"finance"},
		{"acme"},
		{"finance", "--workspace-agent", "off", "--harnesses=none", "--models", "private"},
		{"finance", "--harnesses", "claude, codex", "--models", "local,hosted/small"},
		{},
		{"--models", "any", "--credentials", "own-first"},
	} {
		if err := cmdOrgAgents(args); err != nil {
			t.Fatalf("bx org agents %v: %v", args, err)
		}
	}
	if err := cmdAgentConnection([]string{"--model", "hosted/big", "--orgs", "finance,workspace", "--what-if=local/llama"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /api/xbin/orgs",
		"GET /api/xbin/orgs",
		`PUT /api/xbin/orgs/finance/agents {"harnesses":[],"models":"private","workspaceAgent":"off"}`,
		`PUT /api/xbin/orgs/finance/agents {"harnesses":["claude","codex"],"models":["local","hosted/small"]}`,
		"GET /api/xbin/agents/policy",
		`PUT /api/xbin/agents/policy {"credentials":"own-first","models":"any"}`,
		"GET /api/xbin/agents/connection?model=hosted%2Fbig&orgs=finance%2Cworkspace&whatIf=local%2Fllama",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requests:\n got %q\nwant %q", got, want)
	}
	for _, line := range []string{
		"  harnesses        claude\n  models           private\n",
		"acme: no rules of its own",
		"  harnesses        none\n",
		"within the workspace's bounds: workspace agent on · harnesses any · models any",
		"  models           local\n  credentials      connector\n",
		"1 tile connected · 2 not connected\n",
		"finance        blocked    0/1 tiles  — Finance allows private models only\n  apps/ledger                  blocked\n",
		"apps/secret                    no-access\n",
		"with local/llama: 2 tiles · 1 org connected · 1 not connected\n  + apps/ledger\n",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output lacks %q:\n%s", line, out.String())
		}
	}
	for _, args := range [][]string{{"a", "b"}, {"finance", "--workspace-agent", "maybe"}, {"finance", "--harnesses", "private"},
		{"finance", "--credentials", "connector"}, {"finance", "--models"}, {"finance", "--frob", "x"}} {
		var e *dcError
		if err := cmdOrgAgents(args); !errors.As(err, &e) || e.code != exitUsage {
			t.Errorf("bx org agents %v: %v, want a usage error", args, err)
		}
	}
	// An older xbind: its mux answers 404 with no JSON error → exit 6.
	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	t.Setenv("XBIN_URL", old.URL)
	var e *dcError
	if err := cmdOrgAgents([]string{}); !errors.As(err, &e) || e.code != 6 || e.msg != noAgentPolicyMsg {
		t.Errorf("bx org agents on an old xbind: %v", err)
	}
	if err := cmdAgentConnection(nil); !errors.As(err, &e) || e.code != 6 {
		t.Errorf("bx agent connection on an old xbind: %v", err)
	}
}
