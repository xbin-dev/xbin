package broker

import (
	"strings"
	"testing"
)

// xbind's sentence about a new tile is true (review finding 4): what the
// tile has from its first second is read from the template's manifest —
// a use of something in the scope it lands in is its own grant, so that
// line gives access by itself and carries a warning; endpoints for the
// outside are said; and the line is pinned to the manifest it was
// described from, so a template edited between the proposal and the
// approval makes it stale.
func TestPlanSaysWhatANewTileGets(t *testing.T) {
	w := changeSetWorld(t)
	planTemplates(t, w)
	_, bob := w.session("bob")
	w.write(map[string]string{
		"team/scope.json":           `{"resources":{"db":{"type":"kv"}}}`,
		"team/data/xbin.json":       `{"runtime":"go"}`,
		"team/data/backend/main.go": "package main\n",
		"tpl/reader/xbin.json": `{"runtime":"go","template":{"title":"Reader"},
			"uses":[{"target":"team/data","role":"reader"},{"target":"res:team/db","role":"writer"},{"target":"apps/x","role":"reader"}],
			"exposes":{"hook":{"kind":"http","paths":["/hook"]}},"provides":{"mcp":{"kind":"http","service":"mcp"}}}`,
		"tpl/reader/backend/main.go": "package main\n",
	})
	w.rescan()
	if c, ok := w.b.Reg.Component("team/data"); !ok || c.Scope != "team" {
		t.Fatalf("the fixture's scope: %+v", c)
	}
	cs := w.plan(t, "bob", `[
		{"kind":"tile.create","args":{"template":"tpl/reader","path":"team/r1"}},
		{"kind":"tile.create","args":{"template":"tpl/reader","path":"apps/r2"}},
		{"kind":"tile.create","args":{"template":"tool","path":"apps/t3"}}]`)
	in := strings.Join(cs.Items[0].Warnings, "\n")
	for _, want := range []string{"ACCESS: its code can use team/data as reader from the moment it exists", "ACCESS: its code can use res:team/db as writer", "EXPOSES: it declares hook for the outside"} {
		if !strings.Contains(in, want) {
			t.Errorf("a tile created inside the scope team lacks the warning %q:\n%s", want, in)
		}
	}
	eff := strings.Join(cs.Items[0].Effects, "\n")
	if !strings.Contains(eff, "its code asks for apps/x as reader") || !strings.Contains(eff, "it offers mcp to tiles bound to it") || strings.Contains(eff, "no access to another tile") {
		t.Errorf("its effects: %s", eff)
	}
	// the same template outside the scope: every use waits, nothing is granted by the creation
	out := strings.Join(cs.Items[1].Warnings, "\n")
	if strings.Contains(out, "ACCESS") || !strings.Contains(strings.Join(cs.Items[1].Effects, "\n"), "team/data as reader") {
		t.Errorf("the same template outside the scope: warnings %q effects %q", out, cs.Items[1].Effects)
	}
	if len(cs.Items[2].Warnings) != 0 || !strings.Contains(strings.Join(cs.Items[2].Effects, "\n"), "with no access to another tile") {
		t.Errorf("a template that asks for nothing: %+v", cs.Items[2])
	}

	// the template is edited between the proposal and the approval (a tile agent can): the line is stale
	w.write(map[string]string{"tpl/reader/xbin.json": `{"runtime":"go","template":{"title":"Reader"},"interfaces":{"net":{"kind":"net"}},
		"uses":[{"target":"team/data","role":"reader"},{"target":"res:team/db","role":"writer"},{"target":"apps/x","role":"reader"}],
		"exposes":{"hook":{"kind":"http","paths":["/hook"]}},"provides":{"mcp":{"kind":"http","service":"mcp"}}}`})
	w.rescan()
	done := w.approve(t, cs.ID, `{"items":[2,3]}`, bob)
	if results(done) != "rejected,stale,skipped" {
		t.Fatalf("a creation from a template that changed since it was described: %s (%q)", results(done), done.Items[1].Error)
	}
	if _, ok := w.b.Reg.Component("apps/r2"); ok {
		t.Fatal("the stale creation made the tile")
	}
}
