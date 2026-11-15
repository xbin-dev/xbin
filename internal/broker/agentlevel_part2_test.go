package broker

import (
	"os"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
)

// agentLevelPart2: the raise waits, who may approve it, and what the store
// holds afterwards (TestAgentLevelIsRaisedOnlyByAPerson, whose manifest
// says "full" over a stored "off" when this starts).
func agentLevelPart2(t *testing.T, w *partWS, alice auth.Principal, manifest func(string), may func(bool) error, reads func() bool) {
	b := w.b
	bob, user := personP(t, w, "bob"), personP(t, w, "alice") // an admin; a user who manages nothing
	put := func(p auth.Principal, body string) (int, string) {
		rec := call(t, b.apiAgentLevelsPut, p, "PUT", "/agent-levels", body, nil)
		return rec.Code, rec.Body.String()
	}
	alerts := func(p auth.Principal) string {
		var kinds []string
		for _, a := range b.agentLevelAlerts(p) {
			kinds = append(kinds, a.Kind+":"+a.Tile)
		}
		return strings.Join(kinds, " ")
	}
	// it waits, and the people who manage the tile are told
	rec := call(t, b.apiAgentLevelsGet, bob, "GET", "/agent-levels", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `{"tile":"apps/x","level":"off","requested":"full","stated":"full","by":"manifest"`) {
		t.Fatalf("GET /agent-levels: %d %s", rec.Code, rec.Body.String())
	}
	if got := alerts(bob); got != "agent-level:apps/x" {
		t.Errorf("bob's alerts: %q", got)
	}
	if got := alerts(user) + alerts(alice); got != "" {
		t.Errorf("alerts for a user who manages nothing, and for an agent: %q", got)
	}
	// who can't approve: a user who doesn't manage the tile, an agent (an
	// admin's too), the tile's own frame, backend and terminal, view-as
	bobsAgent := bob
	bobsAgent.Via, bobsAgent.Agent = "delegate", alice.Agent
	viewAs := bob
	viewAs.Impersonator = "owner"
	for name, p := range map[string]auth.Principal{
		"a user": user, "alice's agent": alice, "an admin's agent": bobsAgent, "view-as": viewAs,
		"the tile's frame, driven by an admin": {Component: "apps/x", Via: "frame", UserID: "bob", User: bob.User, Access: bob.Access, Gen: "s.1"},
		"the tile's backend":                   {Component: "apps/x", Via: "instance"},
		"the tile's terminal, an admin's":      {Component: "apps/x", Via: "terminal", UserID: "bob", User: bob.User, Access: bob.Access},
	} {
		if code, body := put(p, `{"tile":"apps/x","level":"full"}`); code != 403 {
			t.Errorf("%s approved the raise: %d %s", name, code, body)
		}
		if rec := call(t, b.apiAgentLevelsGet, p, "GET", "/agent-levels", "", nil); rec.Code == 200 && strings.Contains(rec.Body.String(), "apps/x") {
			t.Errorf("%s reads the levels: %s", name, rec.Body.String())
		}
	}
	if may(false) == nil {
		t.Fatal("a refused approval raised the level")
	}
	// what is approved is what the manifest asks for now
	if code, _ := put(bob, `{"tile":"apps/x","level":"read"}`); code != 409 {
		t.Errorf("approving a level the manifest doesn't ask for: %d", code)
	}
	if code, body := put(bob, `{"tile":"apps/x","level":"full"}`); code != 200 || !strings.Contains(body, `"by":"user:bob"`) {
		t.Fatalf("bob approves: %d %s", code, body)
	}
	if err := may(true); err != nil || alerts(bob) != "" {
		t.Fatalf("after the approval: %v, alerts %q", err, alerts(bob))
	}
	// lowering is the manifest's, at once; the way back up is a person's again
	manifest("read")
	if !reads() {
		t.Fatal("a manifest lowered to read isn't read at once")
	}
	for _, raised := range []string{"full", ""} {
		manifest(raised)
		if !reads() || alerts(bob) != "agent-level:apps/x" {
			t.Fatalf("from read to %q: reads %v, alerts %q", raised, reads(), alerts(bob))
		}
	}
	// the record is xbind's file: it holds across a restart, and outlives the tile
	b.agentLv = agentLevels{}
	if !reads() {
		t.Fatal("the level after a reload")
	}
	if err := os.RemoveAll(w.root + "/apps/x"); err != nil {
		t.Fatal(err)
	}
	w.rescan()
	manifest("full") // made again at the path
	if !reads() {
		t.Fatal("a tile deleted and made again starts at full")
	}

	// the workspace's consoles: this release's level, whatever their copy says
	for _, body := range []string{`{}`, `{"agent":"full"}`} {
		w.write(map[string]string{"tiles/admin/xbin.json": body, "tiles/admin/index.html": "<p>admin</p>"})
		w.rescan()
		if _, _, _, err := b.DelegateTile(bobsAgent, "tiles/admin", false); err == nil || !strings.Contains(err.Error(), `"off"`) {
			t.Errorf("the admin console with manifest %s: %v", body, err)
		}
	}
	if code, _ := put(bob, `{"tile":"tiles/admin","level":"full"}`); code != 409 {
		t.Errorf("approving a console's level: %d", code)
	}
	// a record that can't be read is never read as "nothing was approved"
	if err := os.WriteFile(b.agentLevelsPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.agentLv = agentLevels{}
	if err := may(false); err == nil {
		t.Fatal("with an unreadable record, a tile that states a level is let in")
	}
}
