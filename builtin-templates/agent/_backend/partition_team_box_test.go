package main

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// TestTeamSandboxInPartition: a person's partition sees the team's
// sandboxes — homed at the agent's own global identity, which the manager
// shows it `shared` — by the person rules, never to change or delete them
// there, and says of each that it isn't homed there (so W6-U's pick greys it
// with the backend's words); another consumer's share still needs one for
// this tile, and unpartitioned nothing changes.
func TestTeamSandboxInPartition(t *testing.T) {
	ag, h, _, _, _, global := harnessPartitionG(t)
	_ = ag
	global.Store(true)
	// hers, made in a shared conversation (at the global instance): still not
	// hers to change or delete from her partition
	team := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "team box", Visibility: "team"})
	priv := mkSandbox(t, "apps/cs", "bob", sbxCreate{Name: "bob's own"})
	global.Store(false)
	invalidateSandboxCatalog()
	var list struct{ Sandboxes []map[string]any }
	serveJSON(t, h, as("GET", "/sandboxes?fresh=1", "", alicesFrame("read")), 200, &list)
	seen := map[string]map[string]any{}
	for _, s := range list.Sandboxes {
		seen[s["id"].(string)] = s
	}
	s := seen[team.ID]
	if s == nil || s["shared"] != true || s["homed"] != false || s["canUse"] != true || s["mine"] != true || s["canManage"] != false || s["canEdit"] != false ||
		!strings.Contains(fmt.Sprint(s["why"]), "isn't a sandbox of your own space") {
		t.Fatalf("the team's sandbox in her partition: %v", s)
	}
	if seen[priv.ID] != nil {
		t.Fatalf("bob's private sandbox at the global identity, in her partition: %v", seen[priv.ID])
	}
	ref := url.PathEscape(sandboxRef("apps/cs", team.ID))
	serveJSON(t, h, as("GET", "/sandboxes/"+ref, "", alicesFrame("read")), 200, nil)

	box := func(via, pid string, shared bool) *sbxSandbox {
		b := &sbxSandbox{Name: "b", Shared: shared, Visibility: visTeam}
		b.Owner.Via, b.Owner.PartitionID, b.Owner.User = via, pid, "bob"
		return b
	}
	alice := who{kind: whoUser, user: "alice"}
	if a := sandboxAccess(alice, box("apps/other", "", true)); a.seen() {
		t.Fatalf("another consumer's sandbox without a share for this tile: %+v", a)
	}
	if a := sandboxAccess(alice, box("apps/agent", alicePID, true)); a.seen() {
		t.Fatalf("a sandbox of her partition's id marked shared (not the global identity's): %+v", a)
	}
	setMode(t, modeLegacy, "")
	if a := sandboxAccess(alice, box("apps/agent", "", true)); a.seen() {
		t.Fatalf("unpartitioned, a shared sandbox without a share for this tile: %+v", a)
	}
}
