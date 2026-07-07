package main

import (
	"context"
	"encoding/json"
	"testing"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := openDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFeatureDefaults(t *testing.T) {
	// nil map ⇒ everything on.
	var c Config
	for _, k := range featureKeys {
		if !c.feature(k) {
			t.Fatalf("nil features: %q should default on", k)
		}
	}
	// explicit false disables; absent stays on.
	c.Features = map[string]bool{"streaming": false}
	if c.feature("streaming") {
		t.Fatal("streaming explicitly false should be off")
	}
	if !c.feature("recall") {
		t.Fatal("absent key should default on")
	}
}

func TestScheduleCRUD(t *testing.T) {
	d := newTestDB(t)
	id, err := d.createSchedule(&Schedule{Name: "nightly", Cron: "@every 1h", Goal: "tidy up", Watcher: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.getSchedule(id)
	if err != nil || got.Name != "nightly" || !got.Watcher || !got.Enabled {
		t.Fatalf("getSchedule: %+v err=%v", got, err)
	}
	got.Enabled = false
	got.Goal = "tidy up more"
	if err := d.updateSchedule(got); err != nil {
		t.Fatal(err)
	}
	list, _ := d.listSchedules()
	if len(list) != 1 || list[0].Enabled || list[0].Goal != "tidy up more" {
		t.Fatalf("update not persisted: %+v", list)
	}
	d.setScheduleRun(id, 42)
	if s, _ := d.getSchedule(id); s.RunID != 42 {
		t.Fatalf("setScheduleRun: %d", s.RunID)
	}
	if err := d.deleteSchedule(id); err != nil {
		t.Fatal(err)
	}
	if list, _ := d.listSchedules(); len(list) != 0 {
		t.Fatal("delete failed")
	}
}

func TestSkillsCRUD(t *testing.T) {
	d := newTestDB(t)
	if err := d.upsertSkill(&Skill{Name: "deploy", Description: "ship it", Content: "1. build\n2. push"}); err != nil {
		t.Fatal(err)
	}
	// upsert again = replace.
	_ = d.upsertSkill(&Skill{Name: "deploy", Description: "ship it v2", Content: "steps"})
	s, err := d.getSkill("deploy")
	if err != nil || s.Description != "ship it v2" {
		t.Fatalf("upsert replace: %+v err=%v", s, err)
	}
	if list, _ := d.listSkills(); len(list) != 1 {
		t.Fatalf("list: %d", len(list))
	}
	if err := d.deleteSkill("deploy"); err != nil {
		t.Fatal(err)
	}
	if list, _ := d.listSkills(); len(list) != 0 {
		t.Fatal("delete failed")
	}
}

func TestWatcherRollback(t *testing.T) {
	d := newTestDB(t)
	rid, _ := d.createRun("watch", "", 0)
	d.addMessage(&Message{RunID: rid, Role: "system", Content: "watcher"})
	mark := d.maxMessageSeq(rid) // before the round
	d.addMessage(&Message{RunID: rid, Role: "user", Content: "check now"})
	d.addMessage(&Message{RunID: rid, Role: "assistant", Content: "no change here"})
	if got, _ := d.messages(rid, false); len(got) != 3 {
		t.Fatalf("pre-rollback: %d msgs", len(got))
	}
	// No state_changed ⇒ discard the round.
	d.deleteMessagesAfter(rid, mark)
	got, _ := d.messages(rid, false)
	if len(got) != 1 || got[0].Role != "system" {
		t.Fatalf("rollback should leave only the system message, got %d", len(got))
	}
	// FTS rows for the deleted messages are gone too (no stale recall hits).
	if hits, _ := d.searchMessages(rid, ftsQuery("change"), 8); len(hits) != 0 {
		t.Fatalf("rolled-back message still in FTS: %d", len(hits))
	}
}

func TestVisionRouting(t *testing.T) {
	ctx := context.Background()
	// Explicit tiers ⇒ no network to llm-gw.
	cfg := Config{Models: ModelTiers{General: "text-only-x", VLM: "big-vision"}}
	imgMsg := wireMsg{Role: "user", Content: contentValue(`[{"type":"image_url","image_url":{"url":"data:x"}}]`)}
	txtMsg := wireMsg{Role: "user", Content: "hi"}

	if m := visionModelFor(ctx, cfg, []wireMsg{imgMsg}); m != "big-vision" {
		t.Fatalf("image on non-vision general should route to vlm, got %q", m)
	}
	if m := visionModelFor(ctx, cfg, []wireMsg{txtMsg}); m != "text-only-x" {
		t.Fatalf("text should stay general, got %q", m)
	}
	// A vision-capable general model keeps the image.
	cfg.Models.General = "gpt-4o"
	if m := visionModelFor(ctx, cfg, []wireMsg{imgMsg}); m != "gpt-4o" {
		t.Fatalf("vision-capable general should keep it, got %q", m)
	}
	// Feature off ⇒ never route to vlm.
	cfg.Models.General = "text-only-x"
	cfg.Features = map[string]bool{"vision": false}
	if m := visionModelFor(ctx, cfg, []wireMsg{imgMsg}); m != "text-only-x" {
		t.Fatalf("vision feature off should stay general, got %q", m)
	}
}

func TestContentValue(t *testing.T) {
	if _, ok := contentValue(`[{"type":"text"}]`).(json.RawMessage); !ok {
		t.Fatal("array content should become json.RawMessage (marshals as an array)")
	}
	if s, ok := contentValue("plain").(string); !ok || s != "plain" {
		t.Fatal("plain text should stay a string")
	}
}
