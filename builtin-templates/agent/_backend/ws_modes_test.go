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

// D251: start-defaults names the work mode, which doesn't ask — so a
// session its person put in an asking mode is known by xbind's own word on
// the session (the compact read's "asks"), never taken for one that acts
// without asking; and without that word a mode other than start-defaults'
// isn't known to ask.
func TestWsXbindsWordOnTheSessionAsks(t *testing.T) {
	d := wsStartDefaults{Tile: "apps/x", Harness: "claude", Harnesses: []wsHarness{{ID: "claude", Name: "Claude Code", Mode: "auto", Asks: false}}}
	wsDefaultsSeen.Store("h-said\x00apps/x", wsDefaultsAt{d, time.Now()})
	t.Cleanup(func() { wsDefaultsSeen.Delete("h-said\x00apps/x") })
	row := wsSessionWire{ID: "s1", Cwd: "apps/x", Kind: "agent", Provider: "claude", Mode: "default", Status: "running", Origin: "agent", Asks: true}
	if a := (wsHTTP{}).named(context.Background(), "h-said", row.agent()); !a.Asks || a.Mode != "default" {
		t.Fatalf("a session xbind says asks: %+v", a)
	}
	row.Asks, row.Mode = false, "auto"
	if a := (wsHTTP{}).named(context.Background(), "h-said", row.agent()); a.Asks {
		t.Fatalf("a session in the work mode: %+v", a)
	}
}
