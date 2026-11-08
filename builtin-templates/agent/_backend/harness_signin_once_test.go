package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// A coding agent's login park says whether a sign-in the person saves would
// go into its sandbox (hLogin.Save) — the sign-in card's first offer on a
// big task (D221): in alice's own private sandbox it would; in one others
// may use, not (a project task's shared sandbox said as such); without a
// person's partition, never.
func TestLoginSaveVerdict(t *testing.T) {
	_ = memVault(t)
	ag, _, box, _, _, _ := harnessPartitionG(t, "--require-login")
	_ = ag
	own := &Run{ID: partitionIDBase + 1, Owner: "alice", Engine: engineHarness}
	ref := sandboxRef("apps/cs", box.ID)
	if why := loginSaveWhy(own, ref, box); why != "" {
		t.Fatalf("alice's own private sandbox: %q", why)
	}
	shared := *box
	shared.Members = []string{"bob"}
	if why := loginSaveWhy(own, ref, &shared); why != credNotOwnBox {
		t.Fatalf("a shared sandbox: %q", why)
	}
	task := *own
	task.Origin = originProject
	if why := loginSaveWhy(&task, ref, &shared); why != credProjectShared {
		t.Fatalf("a project task's shared sandbox: %q", why)
	}
	setMode(t, modeLegacy, "")
	if why := loginSaveWhy(own, ref, box); why != signinsLegacy {
		t.Fatalf("an unpartitioned agent: %q", why)
	}
}

// The park itself carries the verdict as the gate judged it at the start.
func TestLoginParkCarriesSave(t *testing.T) {
	_ = memVault(t)
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login")
	run := askIn(t, h, box, "whoami")
	p := parkOf(t, ag, run.ID, "login")
	var l hLogin
	if p.Harness == nil || json.Unmarshal(p.Harness.Login, &l) != nil || l.Save == nil || !l.Save.OK || l.Save.Why != "" {
		t.Fatalf("the park's login: %s", p.Harness.Login)
	}
}

// Signing in once for every big task: the guided sign-in with Remember and
// default makes the minted sign-in the coding agent's default even when
// another (refused) one was — so every later start that takes the default
// (every big task's fork) uses it — and the run starts again with it.
func TestGuidedRememberAsDefault(t *testing.T) {
	testClaude(t)
	_ = memVault(t)
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login")
	old, err := ag.db.saveSignin("alice", "fake", "Old", acp.Key{Env: "CLAUDE_CODE_OAUTH_TOKEN", Kind: "setup-token"}, testToken("0ld"), time.Now())
	if err != nil || !old.IsDefault {
		t.Fatalf("the first saved sign-in: %+v %v", old, err)
	}
	old.RefusedAt = nowMs()
	if err := ag.db.putSignin(old); err != nil {
		t.Fatal(err)
	}
	run := askIn(t, h, box, "whoami")
	parkOf(t, ag, run.ID, "login")
	path := fmt.Sprintf("/runs/%d/harness/authenticate", run.ID)
	if code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided", "remember": true}); code != 202 {
		t.Fatalf("the mint's start: %d %s", code, body)
	}
	code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided", "code": "good-N3w4#test-state", "default": true})
	var done struct{ Saved hSignin }
	if code != 200 || json.Unmarshal([]byte(body), &done) != nil || !done.Saved.IsDefault || done.Saved.ID == old.ID {
		t.Fatalf("the minted sign-in: %d %s", code, body)
	}
	if o := ag.db.signin(old.ID); o == nil || o.IsDefault {
		t.Fatalf("the refused one is still the default: %+v", o)
	}
	hAnswered(t, ag, run.ID, "account: token …N3w4")
	if cfg, _ := ag.db.runConfig(run.ID); cfg.Harness.Signin != pickDefault {
		t.Fatalf("the conversation pinned a sign-in rather than the default: %q", cfg.Harness.Signin)
	}
}

// Once a saved default exists, a coding agent in a sandbox of the person's
// own — a big task's fork is one — starts signed in with it: credPick picks
// it, no sign-in park, and the fork's note says nothing.
func TestSavedDefaultStartsSignedIn(t *testing.T) {
	_ = memVault(t)
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login")
	s, err := ag.db.saveSignin("alice", "fake", "Personal", acp.Key{Env: "CLAUDE_CODE_OAUTH_TOKEN", Kind: "setup-token"}, testToken("Pers"), time.Now())
	if err != nil || !s.IsDefault {
		t.Fatalf("the saved default: %+v %v", s, err)
	}
	run := askIn(t, h, box, "whoami")
	hAnswered(t, ag, run.ID, "account: token …Pers")
	r, _ := ag.db.getRun(run.ID)
	if strings.Contains(r.Pending, `"login"`) {
		t.Fatalf("it parked on its sign-in: %s", r.Pending)
	}
	cfg, _ := ag.db.runConfig(run.ID)
	ref := sandboxRef("apps/cs", box.ID)
	if c, why := ag.db.credPick(r, cfg, ref, box); c == nil || c.ID != s.ID || why != "" {
		t.Fatalf("credPick: %+v %q", c, why)
	}
	if hs, _ := ag.db.harnessSession(run.ID); hs.Cred != s.ID {
		t.Fatalf("the adapter's sign-in: %q", hs.Cred)
	}
	if note := forkSigninNote(ag.db, &ProjectTask{RunID: run.ID}, ref, box); note != "" {
		t.Fatalf("a fork with a saved default goes in: %q", note)
	}
}

// The project's status hints, to its person, to save a sign-in while a
// coding agent works in the project's sandbox on that sandbox's own login
// and they have none saved — and not once they have, nor to anyone else.
func TestSigninOnceHint(t *testing.T) {
	_ = memVault(t)
	ag, _, box, _, _, _ := harnessPartitionG(t)
	p := insertTestProject(t, ag.db, "alice")
	p.SandboxRef = sandboxRef("apps/cs", box.ID)
	alice := who{kind: whoUser, user: "alice"}
	if hs := signinOnceHints(ag.db, p, alice); len(hs) != 0 {
		t.Fatalf("no coding agent there yet: %+v", hs)
	}
	if err := ag.db.putHarnessSession(&harnessSession{RunID: partitionIDBase + 7, RootID: partitionIDBase + 7, Ref: p.SandboxRef, Provider: "fake",
		State: hsLive, StartedMs: nowMs()}); err != nil {
		t.Fatal(err)
	}
	hs := signinOnceHints(ag.db, p, alice)
	if len(hs) != 1 || hs[0].Kind != "agent-signin" || !strings.Contains(hs[0].Text, "save a sign-in so big tasks start signed in") ||
		!strings.Contains(hs[0].Text, "Fake agent (tests)") {
		t.Fatalf("the hint: %+v", hs)
	}
	if hs := signinOnceHints(ag.db, p, who{kind: whoUser, user: "bob"}); len(hs) != 0 {
		t.Fatalf("someone else's view: %+v", hs)
	}
	if _, err := ag.db.saveSignin("alice", "fake", "Personal", acp.Key{Env: "CLAUDE_CODE_OAUTH_TOKEN", Kind: "setup-token"}, testToken("Pers"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if hs := signinOnceHints(ag.db, p, alice); len(hs) != 0 {
		t.Fatalf("with a saved sign-in: %+v", hs)
	}
}
