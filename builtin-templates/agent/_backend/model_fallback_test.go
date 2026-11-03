package main

import (
	"context"
	"testing"
	"time"
)

// No pick, no tier and no preferred model: the first model a bound provider
// serves, not a hardcoded name its gateway may not carry; an explicit
// cfg.Model still wins over the catalog.
func TestModelForFallsBackToTheCatalog(t *testing.T) {
	now := time.Now()
	prefMu.Lock()
	prefCache["agent"] = prefEntry{model: "", at: now}
	prefMu.Unlock()
	catalogMu.Lock()
	catalogCache = &catalog{Models: []catalogModel{{ID: "served-1", Ref: "served-1"}}, at: now}
	catalogMu.Unlock()
	t.Cleanup(func() {
		prefMu.Lock()
		delete(prefCache, "agent")
		prefMu.Unlock()
		catalogMu.Lock()
		catalogCache = nil
		catalogMu.Unlock()
	})

	if got := modelFor(context.Background(), Config{}, "general"); got != "served-1" {
		t.Fatalf("fallback: got %q, want the catalog's first model", got)
	}
	if got := modelFor(context.Background(), Config{Model: "pinned"}, "general"); got != "pinned" {
		t.Fatalf("explicit model: got %q", got)
	}
}

// The fallback skips what no conversation can use — an OpenAI-style list
// sorts babbage-002 and the embedding models first — and the vlm tier takes
// a model that sees images when one is served.
func TestFallbackModelSkipsNonChat(t *testing.T) {
	ms := func(ids ...string) []catalogModel {
		out := make([]catalogModel, len(ids))
		for i, id := range ids {
			out[i] = catalogModel{ID: id, Ref: "p/" + id}
		}
		return out
	}
	openai := ms("babbage-002", "dall-e-3", "davinci-002", "gpt-3.5-turbo", "gpt-4o", "text-embedding-3-small", "tts-1", "whisper-1")
	for _, c := range []struct {
		models     []catalogModel
		tier, want string
	}{
		{openai, "general", "p/gpt-3.5-turbo"},
		{openai, "vlm", "p/gpt-4o"},
		{ms("text-embedding-3-small", "whisper-1"), "general", "p/text-embedding-3-small"}, // nothing better: the first
		{ms("mistral-small", "qwen3-coder"), "vlm", "p/mistral-small"},                     // nothing that sees: the first chat one
		{nil, "general", ""},
	} {
		if got := fallbackModel(c.models, c.tier); got != c.want {
			t.Errorf("%s over %v: got %q, want %q", c.tier, c.models, got, c.want)
		}
	}
}
