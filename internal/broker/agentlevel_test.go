package broker

import (
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
)

// A tile's "agent" level is the tile's own file, and a tile agent the
// delegate prompted can write it (the second review's N3). So xbind keeps
// the level each tile last had approved: a manifest lowers it at once, and
// a manifest that states more — or drops the key after stating less —
// changes nothing until a person who manages the tile approves it, in
// their own session.
func TestAgentLevelIsRaisedOnlyByAPerson(t *testing.T) {
	w := partRouteWS(t)
	b := w.b
	const holder = "apps/q"
	t.Cleanup(auth.TestSealDelegate(holder))
	b.AgentPolicy = delegation.OpenPolicy{}
	alice := delegateOf(t, w, "alice", holder)
	manifest := func(agent string) {
		t.Helper()
		body := `{"runtime":"go"`
		if agent != "" {
			body += `,"agent":"` + agent + `"`
		}
		w.write(map[string]string{"apps/x/xbin.json": body + `}`})
		w.rescan()
	}
	may := func(write bool) error {
		_, _, _, err := b.DelegateTile(alice, "apps/x", write)
		return err
	}
	reads := func() bool { // read only: a GET passes, a write doesn't
		_, _, ro, err := b.DelegateTile(alice, "apps/x", false)
		return err == nil && ro && may(true) != nil
	}

	// never stated: full
	if may(true) != nil {
		t.Fatalf("a tile that never stated a level: %v", may(true))
	}
	// its first explicit value is taken as is
	manifest("off")
	if err := may(false); err == nil || !strings.Contains(err.Error(), "doesn't let a person's agent use it") {
		t.Fatalf("off: %v", err)
	}
	// what a tile agent can do to the file: raise it, or delete the key
	for _, raised := range []string{"full", "read", ""} {
		manifest(raised)
		if err := may(false); err == nil {
			t.Fatalf("the manifest went from off to %q and the agent is let in, with nobody asked", raised)
		}
	}
	manifest("full")
	agentLevelPart2(t, w, alice, manifest, may, reads)
}
