package main

import "testing"

// The grant registry: every capability names its words and its check, and
// grantCaps (what the approve and revoke routes accept) is derived from it.
func TestGrantRegistry(t *testing.T) {
	if len(grantCaps) != len(grantDefs) {
		t.Fatalf("grantCaps %v vs %d defs", grantCaps, len(grantDefs))
	}
	for _, g := range grantDefs {
		if !grantCaps[g.Cap] || g.Ask == "" || g.Chip == "" || g.Denied == "" || g.Forbid == "" || g.Needed == nil {
			t.Fatalf("incomplete grant %q: %+v", g.Cap, g)
		}
		if grantOf(g.Cap).Ask != g.Ask {
			t.Fatalf("grantOf(%q)", g.Cap)
		}
	}
	if p := parsePending(`{"kind":"approval","grant":"threads"}`); p.GrantAsk != grantOf(capThreads).Ask {
		t.Fatalf("the pending ask carries the registry's words: %+v", p)
	}
	if grantOf("nope").askText() != "use “nope”" {
		t.Fatal("an unknown capability still reads")
	}
}
