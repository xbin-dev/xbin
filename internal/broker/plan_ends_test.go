package broker

import (
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/delegation"
)

// A line is judged by BOTH of its ends (review finding 3): a binding lets
// the consumer's code — and a tile agent started in the consumer — reach
// the provider, and a grant reaches into its target. So a tile that turns
// agents away ("agent": "off") is no provider of a planned binding and no
// target of a planned grant, and a provider or a target outside the
// conversation's scope is refused like a consumer outside it.
func TestPlanLinesAreJudgedByBothEnds(t *testing.T) {
	w := changeSetWorld(t)
	planTemplates(t, w)
	_, bob := w.session("bob")
	if code, _ := w.do("PUT", "/delegate/state", `{"adminChanges":true}`, bob); code != 200 {
		t.Fatal("opt in")
	}
	// bob's own assistant, and a tile of his that keeps agents out
	done := w.approve(t, w.plan(t, "bob", `[{"kind":"tile.create","args":{"template":"assistant","path":"apps/asst"}}]`).ID, `{"items":[1]}`, bob)
	if done.State != delegation.SetApplied {
		t.Fatalf("the assistant: %s", results(done))
	}
	w.write(map[string]string{"apps/vault/xbin.json": `{"runtime":"go","agent":"off","provides":{"mcp":{"kind":"http","service":"mcp","role":"reader"}}}`, "apps/vault/backend/main.go": "package main\n"})
	w.rescan()
	if err := w.b.Users.SetOwner("apps/vault", "user:bob"); err != nil {
		t.Fatal(err)
	}
	cs := w.plan(t, "bob", `[
		{"kind":"binding.set","args":{"component":"apps/asst","slot":"mcp","provider":"apps/vault"}},
		{"kind":"grant.add","args":{"from":"apps/x","target":"apps/vault","role":"reader"}},
		{"kind":"tile.create","args":{"template":"assistant","path":"apps/asst2"}},
		{"kind":"binding.set","args":{"component":"apps/asst2","slot":"mcp","provider":"apps/vault"}}]`)
	for _, i := range []int{0, 1, 3} {
		if !strings.Contains(cs.Items[i].Blocked, `apps/vault doesn't let a person's agent use it (its agent level is "off")`) {
			t.Errorf("line %d (%s) reaches a tile that turns agents away: blocked %q", i+1, cs.Items[i].Line, cs.Items[i].Blocked)
		}
	}
	// the other end, outside the conversation's scope
	narrow := w.scoped(t, "bob", "apps/x", "apps/asst")
	cs = w.planAs(t, "bob", narrow, `[
		{"kind":"grant.add","args":{"from":"apps/x","target":"apps/pg","role":"writer"}},
		{"kind":"binding.set","args":{"component":"apps/asst","slot":"mcp","provider":"users/alice/mcp"}},
		{"kind":"grant.add","args":{"from":"apps/x","target":"apps/asst","role":"reader"}}]`)
	for i, want := range []bool{true, true, false} {
		if got := strings.Contains(cs.Items[i].Blocked, "hidden by you"); got != want {
			t.Errorf("line %d (%s): blocked %q", i+1, cs.Items[i].Line, cs.Items[i].Blocked)
		}
	}
}
