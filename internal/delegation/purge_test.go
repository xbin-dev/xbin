package delegation

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A deleted account's delegation records are not the next account's under
// the same id: Purge takes them out of use — requests and tickets, standing
// approvals, handles, the pause and the opt-in, the audit log — and Claim
// catches a deletion nobody told the store about, by the account's
// creation stamp. The log is kept aside, not destroyed.
func TestPurgeAndClaim(t *testing.T) {
	dir := t.TempDir()
	s := OpenStore(dir)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	fill := func() Request {
		t.Helper()
		r, err := s.NewRequest(Request{User: "bob", Agent: "xbin/agent", Kind: KindCall, Method: "POST", Target: "/api/apps/mail/send", Tile: "apps/mail", ContentType: "application/json"}, []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Decide("bob", r.ID, GrantAlways, ScopeTile, "session"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.NewAgentSession("bob", "xbin/agent", nil, "local/a"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetState("bob", func(st *State) { st.Paused, st.AdminChanges = true, true }); err != nil {
			t.Fatal(err)
		}
		if _, err := s.NewChangeSet(ChangeSet{User: "bob", Items: []ChangeItem{{ChangeOp: ChangeOp{Kind: "grant.add"}}}}); err != nil {
			t.Fatal(err)
		}
		if err := s.Audit.Append(Call{User: "bob", Agent: "xbin/agent", Method: "POST", Path: "/api/apps/mail/send", Effect: true, Status: 200}); err != nil {
			t.Fatal(err)
		}
		return r
	}
	kept := func() (n int) {
		t.Helper()
		reqs, _ := s.Requests("bob", false)
		aps, _ := s.Approvals("bob")
		rows, _ := s.Audit.Read("bob", time.Time{}, 100)
		st := s.State("bob")
		sets, _ := s.ChangeSets("bob")
		n = len(reqs) + len(aps) + len(rows) + len(sets) + len(s.AgentSessions("bob", "xbin/agent"))
		if st.Paused || st.AdminChanges {
			n++
		}
		return n
	}
	r := fill()
	if kept() < 6 {
		t.Fatalf("the fixture kept %d things", kept())
	}

	// a claim by the same account changes nothing, twice
	for i := 0; i < 2; i++ {
		if purged, err := s.Claim("bob", 100); purged || err != nil {
			t.Fatalf("claim %d by the account that made it: %v %v", i, purged, err)
		}
	}
	if kept() < 6 {
		t.Fatal("a claim by the same account dropped something")
	}
	// the account is deleted: nothing of it is read under the id again
	if err := s.Purge("bob"); err != nil {
		t.Fatal(err)
	}
	if n := kept(); n != 0 {
		t.Fatalf("after the purge %d things are still read as bob's", n)
	}
	if _, ok := s.Request("bob", r.ID); ok {
		t.Fatal("the old request is still found by its id")
	}
	if _, ok := s.Covers("bob", "apps/mail", "POST", "/api/apps/mail/send"); ok {
		t.Fatal("the old standing approval still covers a call")
	}
	if got := s.People(); len(got) != 0 {
		t.Fatalf("people after the purge: %v", got)
	}
	// …kept aside for the log's retention, not destroyed
	ents, _ := os.ReadDir(filepath.Join(dir, deletedDir))
	if len(ents) != 1 {
		t.Fatalf("deleted/: %v", ents)
	}
	if _, err := os.Stat(filepath.Join(dir, deletedDir, ents[0].Name(), "audit-2026-11.jsonl")); err != nil {
		t.Fatalf("the log that was put away: %v", err)
	}
	if err := s.Purge("bob"); err != nil { // nothing kept: nothing to do
		t.Fatal(err)
	}

	// an account made later under the id starts clean, and what it makes is its own
	fill()
	if purged, err := s.Claim("bob", 200); purged || err != nil {
		t.Fatalf("the new account's first claim: %v %v", purged, err)
	}
	// a deletion the store never heard of: the id now belongs to an account
	// created at another time
	if purged, err := s.Claim("bob", 300); !purged || err != nil {
		t.Fatalf("a claim by another account under the id: %v %v", purged, err)
	}
	if n := kept(); n != 0 {
		t.Fatalf("the third account inherited %d things", n)
	}
	// what was put away is removed once the log's retention has passed
	now = now.AddDate(0, KeepMonths+1, 0)
	fill()
	if err := s.Purge("bob"); err != nil {
		t.Fatal(err)
	}
	if ents, _ = os.ReadDir(filepath.Join(dir, deletedDir)); len(ents) != 1 {
		t.Fatalf("deleted/ after the retention passed: %d entries, want the one just put away", len(ents))
	}
	if purged, err := s.Claim("nobody", 1); purged || err != nil {
		t.Fatalf("a claim with nothing kept: %v %v", purged, err)
	}
	if _, err := os.Stat(userDir(dir, "nobody")); err == nil {
		t.Fatal("a claim with nothing kept wrote something")
	}
}
