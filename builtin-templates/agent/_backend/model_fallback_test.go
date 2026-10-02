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
