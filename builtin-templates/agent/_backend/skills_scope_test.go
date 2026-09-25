package main

import (
	"strings"
	"testing"
)

// A skill the agent writes belongs to its conversation's owner and its tool
// mode; a run sees the shared skills and its owner's, in its own mode, and
// can't overwrite a shared skill or someone else's. Skills from before (no
// owner, no lane) stay shared everywhere.
func TestSkillScopes(t *testing.T) {
	ag, _ := accessFixture(t)
	if err := ag.db.upsertSkill(&Skill{Name: "legacy", Description: "old", Content: "steps"}); err != nil {
		t.Fatal(err)
	}
	run := func(owner, lane string) (*Run, Config) {
		cfg := defaultConfig()
		cfg.Toolset = lane
		r, err := ag.startRunOpts(runOpts{Title: "t", Cfg: cfg, Hold: true,
			Stamp: runStamp{Owner: owner, Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}})
		if err != nil {
			t.Fatal(err)
		}
		return r, cfg
	}
	tool := func(r *Run, cfg Config, name string, args map[string]any) (string, error) {
		return ag.runSkillTool(r, cfg, name, args)
	}
	aliceP, cfgP := run("alice", "private")
	aliceW, cfgW := run("alice", "web")
	bob, cfgB := run("bob", "private")

	if _, err := tool(aliceW, cfgW, "skill_manage", map[string]any{"action": "save", "name": "scrape", "description": "d", "content": "c"}); err != nil {
		t.Fatal(err)
	}
	s, _ := ag.db.getSkill("scrape")
	if s.Owner != "alice" || s.Lane != "web" {
		t.Fatalf("stamped: %+v", s)
	}
	list := func(r *Run, cfg Config) string { out, _ := tool(r, cfg, "skills_list", nil); return out }
	if l := list(aliceW, cfgW); !strings.Contains(l, "scrape") || !strings.Contains(l, "legacy") {
		t.Fatalf("alice's web run: %s", l)
	}
	if l := list(aliceP, cfgP); strings.Contains(l, "scrape") || !strings.Contains(l, "legacy") {
		t.Fatalf("a web-mode skill never reaches a run with internal reach: %s", l)
	}
	if l := list(bob, cfgB); strings.Contains(l, "scrape") {
		t.Fatalf("bob sees alice's skill: %s", l)
	}
	if out, _ := tool(bob, cfgB, "skill_view", map[string]any{"name": "scrape"}); out != "(no such skill)" {
		t.Fatalf("bob views it: %q", out)
	}
	if _, err := tool(bob, cfgB, "skill_manage", map[string]any{"action": "save", "name": "legacy", "content": "poison"}); err == nil {
		t.Fatal("a personal run overwrote a shared skill")
	}
	if _, err := tool(bob, cfgB, "skill_manage", map[string]any{"action": "remove", "name": "scrape"}); err == nil {
		t.Fatal("bob removed alice's skill")
	}
	// the context lists the scope's skills only
	msgs, err := ag.assembleContext(t.Context(), aliceP, cfgP)
	if err != nil {
		t.Fatal(err)
	}
	if sys := msgs[0].Content.(string); strings.Contains(sys, "scrape") || !strings.Contains(sys, "legacy") {
		t.Fatalf("the injected list: %s", sys)
	}
}
