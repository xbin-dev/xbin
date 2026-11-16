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

// What a person's agent is refused for says nothing of tiles its person
// can't see (review finding 9): a template they can't read is "no such
// template", like one that isn't there; a place taken by a tile they can't
// read is "taken", with no name — in a plan's line, in a single creation's
// dry run and in the request for one. What they can see is named as before.
func TestCreationRefusalsNameNothingHidden(t *testing.T) {
	w := changeSetWorld(t)
	planTemplates(t, w)
	w.write(map[string]string{
		"secret/tpl/xbin.json": `{"runtime":"go","template":{"title":"Hidden"}}`, "secret/tpl/backend/main.go": "package main\n",
		"secret/t/xbin.json": `{"runtime":"go"}`, "secret/t/backend/main.go": "package main\n",
	})
	w.rescan()
	cs := w.plan(t, "alice", `[
		{"kind":"tile.create","args":{"template":"secret/tpl","path":"apps/a1"}},
		{"kind":"tile.create","args":{"template":"secret/nope","path":"apps/a2"}},
		{"kind":"tile.create","args":{"template":"tool","path":"secret/t"}},
		{"kind":"tile.create","args":{"template":"tool","path":"secret/t/sub"}},
		{"kind":"tile.create","args":{"template":"tool","path":"secret"}},
		{"kind":"tile.create","args":{"template":"tool","path":"users/alice/mcp"}}]`)
	b := func(i int) string { return cs.Items[i].Blocked }
	if b(0) != "no such template: secret/tpl" || b(1) != "no such template: secret/nope" {
		t.Errorf("a template alice can't read, and one that isn't there:\n%q\n%q", b(0), b(1))
	}
	for i, path := range map[int]string{2: "secret/t", 3: "secret/t/sub", 4: "secret"} {
		if want := "can't create " + path + ": the place is taken — pick another path"; b(i) != want {
			t.Errorf("a place taken by a tile alice can't read: %q, want %q", b(i), want)
		}
	}
	if b(5) != "users/alice/mcp already exists" {
		t.Errorf("a place taken by a tile she can read is named: %q", b(5))
	}
	// the single creation's dry run and its request say the same
	for body, want := range map[string]string{
		`{"source":"secret/tpl","path":"apps/a1"}`: "no such template: secret/tpl",
		`{"source":"tool","path":"secret/t/sub"}`:  "can't create secret/t/sub: the place is taken — pick another path",
		`{"source":"tool","path":"secret/unused"}`: "",
	} {
		_, dry := w.do("POST", "/templates/new", body, both(agentOf("alice"), dryRun))
		if want == "" && strings.Contains(dry, "secret/t") || want != "" && !strings.Contains(dry, want) {
			t.Errorf("the dry run of %s: %s", body, dry)
		}
		if strings.Contains(dry, "existing component") || strings.Contains(dry, "no read access") {
			t.Errorf("the dry run of %s names what alice can't see: %s", body, dry)
		}
	}
}
