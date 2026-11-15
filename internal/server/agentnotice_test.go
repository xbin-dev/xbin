package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// covers D232 — what xbin's chrome for the workspace agent reads beyond the
// switches. GET /delegate/state answers agent — the component whose page
// the dock frames — only while boot has named one (AgentHolder), pausedAt
// while the person's agent is paused, and workspaceAt once the workspace's
// switch was ever set (off after that is "turned off", not "never set
// up"). The notice that names /xbin/agent (AgentPageNotice) is a person's
// while workspace agents are on and there is an agent to show, until they
// dismiss it (AgentPageSeen) — which changes nothing else of their state.
func TestAgentChromeState(t *testing.T) {
	w := delegateWorld(t)
	state := func(user string) map[string]any {
		rec := w.do(w.full, "GET", "/api/xbin/delegate/state", "", w.cookie(user))
		var out map[string]any
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("state: %d %s", rec.Code, rec.Body)
		}
		return out
	}
	st := state("bob")
	if _, has := st["agent"]; has || st["workspace"] != true || st["workspaceAt"] == nil || st["pausedAt"] != nil {
		t.Fatalf("no holder named: %v", st)
	}
	if _, ok := w.s.AgentPageNotice("bob"); ok {
		t.Fatal("a notice without an agent to show")
	}
	// …and only for a person who was given it: nobody has an agent until
	// then (agentwho.go), and nobody without one is told of its page
	w.s.AgentHolder = func() string { return dHolder }
	if st = state("bob"); st["agent"] != nil || st["access"] != false {
		t.Fatalf("a person who wasn't given the agent: %v", st)
	}
	if _, ok := w.s.AgentPageNotice("bob"); ok {
		t.Fatal("a notice for a person without an agent")
	}
	if err := w.s.Auth.Users.SetUserTile("bob", dHolder, "read"); err != nil {
		t.Fatal(err)
	}
	if st = state("bob"); st["agent"] != dHolder || st["access"] != true || st["who"] != nil {
		t.Fatalf("agent: %v", st)
	}
	msg, ok := w.s.AgentPageNotice("bob")
	if !ok || !strings.Contains(msg, AgentPagePath) {
		t.Fatalf("the notice: %v %q", ok, msg)
	}
	if _, ok := w.s.AgentPageNotice(""); ok {
		t.Fatal("a notice for nobody")
	}
	// paused: pausedAt, kept across another change, gone on resume
	if rec := w.do(w.full, "PUT", "/api/xbin/delegate/state", `{"paused":true}`, w.cookie("bob")); rec.Code != 200 {
		t.Fatalf("pause: %d %s", rec.Code, rec.Body)
	}
	at, _ := state("bob")["pausedAt"].(string)
	if _, err := time.Parse(time.RFC3339, at); err != nil {
		t.Fatalf("pausedAt %q: %v", at, err)
	}
	// dismissing leaves the pause, and the person's other switches, as they are
	if err := w.s.AgentPageSeen("bob"); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.s.AgentPageNotice("bob"); ok {
		t.Fatal("the notice after its dismissal")
	}
	// alice is an admin: the role gives her no agent, so no notice of its
	// page — until an entry of her own does
	if _, ok := w.s.AgentPageNotice("alice"); ok {
		t.Fatal("an admin who wasn't given an agent is told of its page")
	}
	if err := w.s.Auth.Users.SetUserTile("alice", dHolder, "read"); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.s.AgentPageNotice("alice"); !ok {
		t.Fatal("bob's dismissal ended alice's notice")
	}
	if st = state("bob"); st["paused"] != true || st["pausedAt"] != at {
		t.Fatalf("after the dismissal: %v", st)
	}
	if rec := w.do(w.full, "PUT", "/api/xbin/delegate/state", `{"paused":false}`, w.cookie("bob")); rec.Code != 200 {
		t.Fatalf("resume: %d %s", rec.Code, rec.Body)
	}
	if st = state("bob"); st["paused"] != false || st["pausedAt"] != nil {
		t.Fatalf("resumed: %v", st)
	}
	if _, ok := w.s.AgentPageNotice("bob"); ok {
		t.Fatal("the notice came back with a resume")
	}
	// workspace agents off: no notice for anyone; the state says it was set
	if _, err := w.store.SetWorkspace(false, "test"); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.s.AgentPageNotice("alice"); ok {
		t.Fatal("a notice while workspace agents are off")
	}
	if st = state("alice"); st["workspace"] != false || st["workspaceAt"] == nil {
		t.Fatalf("turned off: %v", st)
	}
}
