package main

import (
	"sync/atomic"
	"testing"
)

// After the first answer a chat is named by the memory model — once, never
// over a name someone gave it, never with the feature off.
func TestAutoTitle(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	var titled atomic.Int32
	f.on(func(r LLMRequest) bool { return r.Purpose == "title" }, func(LLMRequest) (LLMReply, error) {
		titled.Add(1)
		return LLMReply{Msg: wireMsg{Role: "assistant", Content: `Title: "Quarterly budget review."`}}, nil
	})
	start := func(src string, cfg Config) int64 {
		r, err := ag.startRunOpts(runOpts{Title: "plan the q3 budget with the…", Cfg: cfg, Text: "plan the q3 budget with the team",
			Stamp: runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat", TitleSrc: src}})
		if err != nil {
			t.Fatal(err)
		}
		ag.eng.Poke(r.ID)
		return r.ID
	}
	id := start("clip", defaultConfig())
	waitFor(t, "the title", func() bool { r, _ := db.getRun(id); return r.TitleSrc == "auto" })
	if r, _ := db.getRun(id); r.Title != "Quarterly budget review" {
		t.Fatalf("title %q", r.Title)
	}
	named := start("user", defaultConfig())
	cfg := defaultConfig()
	cfg.Features = map[string]bool{"titles": false}
	off := start("clip", cfg)
	waitFor(t, "both answered", func() bool {
		a, _ := db.getRun(named)
		b, _ := db.getRun(off)
		return a.Status == statusIdle && b.Status == statusIdle
	})
	if titled.Load() != 1 {
		t.Fatalf("title calls: %d (a named chat and the feature off make none)", titled.Load())
	}
	if r, _ := db.getRun(off); r.TitleSrc != "clip" {
		t.Fatal("feature off: stays clipped")
	}
}

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		`"Budget review"`:              "Budget review",
		"Title: Plan the offsite.":     "Plan the offsite",
		"**Vendor prices**\nmore text": "Vendor prices",
		"":                             "",
		"one two three four five six seven eight nine ten eleven twelve thirteen": "",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// Background work takes a slot only when nobody waits and one stays free.
func TestBackgroundSlot(t *testing.T) {
	g := newLLMGate(4)
	var rels []func()
	for i := 0; i < 2; i++ {
		rel, _ := g.acquire(t.Context(), true)
		rels = append(rels, rel)
	}
	bg := g.tryBackground()
	if bg == nil {
		t.Fatal("2 of 4 busy: background may run")
	}
	if g.tryBackground() != nil {
		t.Fatal("3 of 4 busy: the last slot is a person's")
	}
	bg()
	for _, r := range rels {
		r()
	}
}
