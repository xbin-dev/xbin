//go:build linux && integration

package isolated

// partitions_agent_signin_test.go — a person's saved sign-in for a coding
// agent (D179) in a real partition: the secret goes through xbind's vault of
// alice's partition (the agent's xbin.SetSecret/Secret from her partition
// reach her partition's own), into the fake coding agent's environment in
// her own sandbox — where it outranks the sandbox's $HOME sign-in (the fake
// counts CLAUDE_CODE_OAUTH_TOKEN as Claude Code does, and `whoami` says
// which sign-in a turn used) — and Forget stops the adapter that used it
// and takes it out of the vault. The global instance has none.
// A subtest of TestPartitionsAgentHarness (its env: alice's sandbox homed in
// her partition, the fake inside, its $HOME signed in by the fake's login).

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func phSavedSignin(t *testing.T, e *phEnv, box agSandbox) {
	const secret = "sk-ant-oat01-isolated-0123456789abcdef-WXYZ"
	var made struct {
		Signin struct {
			ID, Name, Env string
			IsDefault     bool
		}
	}
	r := e.agm(t, 201, "alice", "POST", "/prefs/harness-signins", map[string]any{"harness": "fake", "name": "Work", "secret": secret}, false)
	r.Decode(t, &made)
	if made.Signin.Name != "Work" || made.Signin.Env != "CLAUDE_CODE_OAUTH_TOKEN" || !made.Signin.IsDefault || strings.Contains(r.String(), secret) {
		t.Fatalf("the saved sign-in: %s", r)
	}
	if l := e.agm(t, 200, "alice", "GET", "/prefs/harness-signins", nil, false); strings.Contains(l.String(), secret) || !strings.Contains(l.String(), `"available":true`) {
		t.Fatalf("her list: %s", l)
	}
	// the global instance holds no one's credentials
	phRefused(t, "saved sign-ins at the global instance", e.agc(t, "alice", "GET", "/prefs/harness-signins", nil, true), 409,
		"shared space holds no one's credentials")

	// a new conversation of hers: the saved sign-in, out of her vault, wins over the sandbox's own
	var run struct{ ID int64 }
	e.agm(t, 200, "alice", "POST", "/ask", map[string]any{"text": "whoami", "harness": map[string]string{"provider": "fake"},
		"sandbox": map[string]string{"ref": box.Ref}}, false).Decode(t, &run)
	v := e.waitView(t, "alice", run.ID, "the answer with the saved sign-in", 90*time.Second, func(v hView) bool {
		return v.Run.Status != "running" && strings.Contains(v.lastAssistant(), "account:")
	})
	if got := v.lastAssistant(); !strings.Contains(got, "account: token …WXYZ") {
		t.Fatalf("the turn's sign-in: %q (want the saved one: %s)", got, v.brief())
	}
	var h struct {
		Harness struct {
			Signin struct {
				Pick  string
				Using *struct{ ID, Name string }
			}
		}
	}
	g := e.agm(t, 200, "alice", "GET", fmt.Sprintf("/runs/%d/harness", run.ID), nil, false)
	if err := json.Unmarshal(g.Body, &h); err != nil || h.Harness.Signin.Using == nil || h.Harness.Signin.Using.Name != "Work" ||
		h.Harness.Signin.Pick != "default" || strings.Contains(g.String(), secret) {
		t.Fatalf("its summary's sign-in: %s", g)
	}

	// Forget: the adapter that used it stops; the next message starts it with the sandbox's own
	f := e.agm(t, 200, "alice", "DELETE", "/prefs/harness-signins/"+made.Signin.ID, nil, false)
	if !strings.Contains(f.String(), `"stopped":1`) {
		t.Fatalf("Forget: %s", f)
	}
	e.waitView(t, "alice", run.ID, "the adapter stopped by Forget", 60*time.Second, func(v hView) bool {
		return v.summary().State == "failed" && strings.Contains(v.summary().Error, "was forgotten")
	})
	e.agm(t, 200, "alice", "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "whoami again"}, false)
	v = e.waitView(t, "alice", run.ID, "the answer with the sandbox's own sign-in", 90*time.Second, func(v hView) bool {
		return v.Run.Status != "running" && strings.Contains(v.lastAssistant(), "account: home")
	})
	if l := e.agm(t, 200, "alice", "GET", "/prefs/harness-signins", nil, false); strings.Contains(l.String(), made.Signin.ID) {
		t.Fatalf("forgotten, still listed: %s", l)
	}
	// leave the partition as the next subtests expect: one coding agent up (the first run's)
	e.agm(t, 200, "alice", "POST", fmt.Sprintf("/runs/%d/cancel", run.ID), map[string]any{}, false)
	e.waitView(t, "alice", run.ID, "its coding agent stopped", 60*time.Second, func(v hView) bool {
		return v.summary().State == "stopped" || v.summary().State == "failed"
	})
	_ = v
}
