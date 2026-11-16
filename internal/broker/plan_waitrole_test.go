package broker

import (
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/delegation"
)

const diceAs = `{"runtime":"go","provides":{"mcp":{"kind":"http","service":"mcp","role":"%s"}}}`

// The review said "as reader", before the tile declared anything. That is
// the most the binding it made ever grants: the tile's coding agent
// declaring writer or admin afterwards widens nothing through it, a role
// outside the conventional order grants nothing — and binding the slot
// again by hand, to what the tile declares then, is a new decision.
func TestWaitedBindingKeepsItsApprovedRole(t *testing.T) {
	w, bob, cs := waitWorld(t, "bob")
	w.dice(diceProvides)
	if got := w.set(t, "bob", cs.ID).Items[3]; got.Result != delegation.ItemApplied {
		t.Fatalf("the line: %s %s", got.Result, got.Error)
	}
	role := func() string { r, _ := w.b.httpBindingRole("apps/assistant", "apps/dice"); return r }
	if role() != "reader" || w.b.Reg.Workspace().BindingCaps["apps/assistant"]["mcp"]["apps/dice"] != "reader" {
		t.Fatalf("the binding grants %q, on record %v", role(), w.b.Reg.Workspace().BindingCaps)
	}
	for declared, want := range map[string]string{"writer": "reader", "admin": "reader", "reader": "reader", "boss": ""} {
		w.dice(strings.Replace(diceAs, "%s", declared, 1))
		if got := role(); got != want {
			t.Errorf("the tile now declares %q: the binding grants %q, want %q", declared, got, want)
		}
	}
	// (the provider's own expose.implies is the provider's word: it lifts nothing)
	w.dice(`{"runtime":"go","expose":{"implies":{"reader":["boss"]}},"provides":{"mcp":{"kind":"http","service":"mcp","role":"boss"}}}`)
	if got := role(); got != "" {
		t.Errorf("a role the tile says reader implies: the binding grants %q", got)
	}
	// bound again by hand, to what the tile declares now: a new decision, and nothing is on record for it
	w.dice(strings.Replace(diceAs, "%s", "writer", 1))
	if code, body := w.do("POST", "/bindings", `{"component":"apps/assistant","slot":"mcp","provider":"apps/dice"}`, bob); code != 200 {
		t.Fatalf("binding by hand: %d %s", code, body)
	}
	if caps := w.b.Reg.Workspace().BindingCaps; role() != "writer" || len(caps) != 0 {
		t.Fatalf("after a bind by hand: grants %q, on record %v", role(), caps)
	}
}

// Between xbind judging a waiting line and writing its binding a scan can
// land: the tile's manifest is read once more under the lock the binding
// is written under, so a tile that changed in that window is not bound —
// the line waits on, held, as if the change had come a moment earlier.
func TestWaitedBindReadsTheTileUnderItsLock(t *testing.T) {
	w, bob, cs := waitWorld(t, "bob")
	n := 0
	bindWaitHook = func() { // the scan that lands in the window: the tile now declares admin
		n++
		w.write(map[string]string{"apps/dice/xbin.json": strings.Replace(diceAs, "%s", "admin", 1)})
		w.rescan()
	}
	t.Cleanup(func() { bindWaitHook = nil })
	w.dice(diceProvides)
	got := w.set(t, "bob", cs.ID).Items[3]
	if n != 1 || got.Result != delegation.ItemWaiting || !strings.Contains(got.Wait.Held, `declares mcp for the role "admin"`) || w.binding("apps/assistant", "mcp") != "" {
		t.Fatalf("a manifest changed between the judgement and the bind (hook ran %d×): %s (%s), held %q, bound %q", n, got.Result, got.Error, got.Wait.Held, w.binding("apps/assistant", "mcp"))
	}
	bindWaitHook = nil
	w.dice(diceProvides)
	if got := w.set(t, "bob", cs.ID).Items[3]; got.Result != delegation.ItemApplied || w.binding("apps/assistant", "mcp") != "apps/dice" {
		t.Fatalf("once it declares what was approved again: %s (%s)", got.Result, got.Error)
	}
	// the role on record lives as long as its binding
	if code, body := w.do("DELETE", "/bindings", `{"component":"apps/assistant","slot":"mcp"}`, bob); code != 200 || w.b.Reg.Workspace().BindingCaps != nil {
		t.Fatalf("unbound: %d %s, on record %v", code, body, w.b.Reg.Workspace().BindingCaps)
	}
}
