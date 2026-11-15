package main

import (
	"context"
	"testing"
	"time"
)

// Only the mode xbind names as asking for a harness (its ask or plan mode)
// is "asks before acting": a session in another mode, or in a mode nobody
// said, isn't known to ask.
func TestWsOnlyTheAskingModeAsks(t *testing.T) {
	d := wsStartDefaults{Tile: "apps/x", Harness: "claude", Harnesses: []wsHarness{{ID: "claude", Name: "Claude Code", Mode: "default", Asks: true}}}
	wsDefaultsSeen.Store("h-modes\x00apps/x", wsDefaultsAt{d, time.Now()})
	t.Cleanup(func() { wsDefaultsSeen.Delete("h-modes\x00apps/x") })
	for mode, want := range map[string]bool{"default": true, "": false, "bypassPermissions": false, "acceptEdits": false} {
		if a := (wsHTTP{}).named(context.Background(), "h-modes", wsAgent{Tile: "apps/x", Provider: "claude", Mode: mode}); a.Asks != want || a.Name != "Claude Code" {
			t.Errorf("a session in mode %q: asks %v (%+v), want %v", mode, a.Asks, a, want)
		}
	}
}
