package broker

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/delegation"
)

// Taking a plan back ends what it asked of the admins, on every path
// (review finding 1): a decided plan whose line is still with the admins —
// the person rejects it, their agent withdraws it, they undo it — leaves no
// copy an admin could still apply; and an admin's copy of a plan that
// ended some other way (it expired, it is gone) applies nothing either.
func TestTakingAPlanBackEndsWhatItAskedOfTheAdmins(t *testing.T) {
	const plan = `[{"kind":"tile.create","args":{"template":"blank","path":"apps/mine"}},
		{"kind":"grant.add","args":{"from":"apps/helper","target":"apps/x","role":"reader"}}]`
	type world struct {
		*csWorld
		alice, bob func(*http.Request)
		cs, asked  delegation.ChangeSet
	}
	// alice's plan, her own line applied, the admin's line asked: decided, and still with the admins
	setup := func(t *testing.T) world {
		w := startingWorld(t)
		_, alice := w.session("alice")
		_, bob := w.session("bob")
		cs := w.plan(t, "alice", plan)
		if done := w.approve(t, cs.ID, `{"items":[1]}`, alice); results(done) != "applied," || done.State != delegation.SetApplied {
			t.Fatalf("alice's own line: %s %s", done.State, results(done))
		}
		if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/ask", "", alice); code != 200 {
			t.Fatalf("ask: %d %s", code, body)
		}
		var asked delegation.ChangeSet
		sets, _ := w.store.ChangeSets("bob")
		for _, o := range sets {
			if o.From == "alice" && o.AskOf == cs.ID {
				asked = o
			}
		}
		if asked.ID == "" || !asked.Open() {
			t.Fatalf("the admin's copy: %+v", asked)
		}
		return world{w, alice, bob, w.set(t, "alice", cs.ID), asked}
	}
	for name, take := range map[string]func(t *testing.T, w world){
		"the person rejects the plan": func(t *testing.T, w world) {
			if code, body := w.do("POST", "/delegate/changesets/"+w.cs.ID+"/reject", "", w.alice); code != 200 {
				t.Fatalf("reject: %d %s", code, body)
			}
		},
		"their agent withdraws it": func(t *testing.T, w world) {
			if code, body := w.do("DELETE", "/delegate/changesets/"+w.cs.ID, "", agentOf("alice")); code != 200 {
				t.Fatalf("withdraw: %d %s", code, body)
			}
		},
		"the person undoes it": func(t *testing.T, w world) {
			if code, body := w.do("POST", "/delegate/changesets/"+w.cs.ID+"/undo", "", w.alice); code != 200 && code != 409 {
				t.Fatalf("undo: %d %s", code, body)
			}
		},
		"the plan expired": func(t *testing.T, w world) {
			if _, err := w.store.UpdateChangeSet("alice", w.cs.ID, func(c *delegation.ChangeSet) error { c.State = delegation.SetExpired; return nil }); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := setup(t)
			take(t, w)
			code, body := w.do("POST", "/delegate/changesets/"+w.asked.ID+"/approve", `{"items":[1],"password":"password1"}`, w.bob)
			if code != 409 || slices.Contains(w.grants(), "apps/helper→apps/x:reader") {
				t.Fatalf("an admin applied a request of a plan that was taken back: %d %s (grants %v)", code, body, w.grants())
			}
			if got := w.set(t, "bob", w.asked.ID); got.State != delegation.SetWithdrawn || got.DecidedBy != "alice" {
				t.Errorf("the admin's copy: %s (decided by %q), want withdrawn by alice", got.State, got.DecidedBy)
			}
			if got := w.set(t, "alice", w.cs.ID).Items[1]; name != "the plan expired" && (got.Result != delegation.ItemRejected || !strings.Contains(got.Error, "before it was applied")) {
				t.Errorf("alice's asked line: %s (%q)", got.Result, got.Error)
			}
		})
	}
}

// …and a line an admin already approved that waits in the admin's copy
// ends with the asker's plan too: it must not apply later, as the admin,
// into a plan its person cancelled.
func TestTakingAPlanBackEndsAnAdminsWaitingCopy(t *testing.T) {
	w := startingWorld(t)
	w.write(map[string]string{"apps/x/xbin.json": `{"runtime":"go","interfaces":{"mcp":{"kind":"http","service":"mcp","multi":true}}}`})
	w.rescan()
	_, alice := w.session("alice")
	_, bob := w.session("bob")
	cs := w.plan(t, "alice", `[{"kind":"tile.create","args":{"template":"blank","path":"apps/dice"}},{"kind":"agent.start","args":{"tile":"apps/dice","brief":"b","harness":"claude"}},
		{"kind":"binding.set","args":{"component":"apps/x","slot":"mcp","provider":"apps/dice"}}]`)
	w.approve(t, cs.ID, `{"items":[1,2]}`, alice)
	if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/ask", "", alice); code != 200 {
		t.Fatalf("ask: %d %s", code, body)
	}
	var asked delegation.ChangeSet
	sets, _ := w.store.ChangeSets("bob")
	for _, o := range sets {
		if o.From == "alice" && o.AskOf == cs.ID {
			asked = o
		}
	}
	if done := w.approve(t, asked.ID, `{"items":[1],"password":"password1"}`, bob); results(done) != "waiting" {
		t.Fatalf("the admin's copy: %s", results(done))
	}
	if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/reject", "", alice); code != 200 {
		t.Fatalf("alice takes her plan back: %d %s", code, body)
	}
	w.dice(diceProvides)
	if got := w.set(t, "bob", asked.ID).Items[0]; got.Result != delegation.ItemCancelled || w.binding("apps/x", "mcp") != "" {
		t.Fatalf("the admin's waiting copy of a plan its person cancelled: %s (%q), bound %q", got.Result, got.Error, w.binding("apps/x", "mcp"))
	}
	if got := w.set(t, "alice", cs.ID).Items[2]; got.Result == delegation.ItemApplied || got.Result == delegation.ItemWaiting {
		t.Fatalf("alice's cancelled plan says %s", got.Result)
	}
}
