package delegation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	s := OpenStore(t.TempDir())
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	return s, &now
}

// The switches: the workspace's is off until set; a person's state that
// can't be read is the closed one; a nil store is closed.
func TestStateAndWorkspace(t *testing.T) {
	s, _ := testStore(t)
	if s.Workspace().Enabled {
		t.Fatal("the workspace switch starts on")
	}
	if w, err := s.SetWorkspace(true, "user:alice"); err != nil || !w.Enabled || w.By != "user:alice" || !s.Workspace().Enabled {
		t.Fatalf("on: %+v %v", w, err)
	}
	if st := s.State("bob"); st.Paused || st.AdminChanges {
		t.Fatalf("bob's first state: %+v", st)
	}
	st, err := s.SetState("bob", func(st *State) { st.Paused, st.AdminChanges = true, true })
	if err != nil || !st.Paused || !st.AdminChanges || st.Updated.IsZero() || s.State("alice").Paused {
		t.Fatalf("set: %+v %v", st, err)
	}
	// an unreadable file: paused, no admin changes — never open
	if err := os.WriteFile(s.userFile("carol", "state.json"), []byte("{"), 0o600); err == nil {
		t.Fatal("carol has no directory yet")
	}
	_ = os.MkdirAll(filepath.Dir(s.userFile("carol", "state.json")), 0o700)
	_ = os.WriteFile(s.userFile("carol", "state.json"), []byte("{"), 0o600)
	if st := s.State("carol"); !st.Paused || st.AdminChanges {
		t.Fatalf("an unreadable state: %+v", st)
	}
	_ = os.WriteFile(filepath.Join(s.dir, "workspace.json"), []byte("nope"), 0o600)
	if s.Workspace().Enabled {
		t.Fatal("an unreadable workspace switch reads on")
	}
	var none *Store
	if none.Workspace().Enabled || !none.State("bob").Paused {
		t.Fatal("a nil store is open")
	}
	if _, ok := none.Covers("bob", "apps/x", "POST", "/api/apps/x/a"); ok {
		t.Fatal("a nil store covers")
	}
	if _, err := none.Redeem("bob", "x", "POST", "/api/apps/x/a", "apps/x", "", nil); !errors.Is(err, ErrNoTicket) {
		t.Fatalf("a nil store redeems: %v", err)
	}
}

// A request becomes a one-shot ticket bound to exactly its call.
func TestRequestsAndTickets(t *testing.T) {
	s, now := testStore(t)
	body := []byte(`{"to":"x@example.com","text":"hi"}`)
	r, err := s.NewRequest(Request{User: "bob", Agent: "xbin/agent", Kind: KindCall, Method: "POST", Target: "/api/apps/mail/send?draft=1", Tile: "apps/mail",
		ContentType: "application/json", Summary: strings.Repeat("s", 600), State: Approved, Grant: GrantAlways}, body)
	if err != nil || r.State != Pending || r.Grant != "" || r.ID == "" || !r.BodyShown || r.Body != string(body) || r.BodySHA256 != BodyHash(body) || len(r.Summary) != 500 {
		t.Fatalf("new: %+v %v", r, err)
	}
	redeem := func(id, method, target string, b []byte) error {
		_, err := s.Redeem("bob", id, method, target, "apps/mail", "application/json", b)
		return err
	}
	// pending: no ticket yet
	if err := redeem(r.ID, "POST", r.Target, body); !errors.Is(err, ErrNoTicket) {
		t.Fatalf("a pending request redeemed: %v", err)
	}
	// only its person decides it
	if _, _, err := s.Decide("alice", r.ID, GrantOnce, "", "session"); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("alice decided bob's request: %v", err)
	}
	if _, _, err := s.Decide("bob", r.ID, "forever", "", "session"); !errors.Is(err, ErrBadGrant) {
		t.Fatalf("a bad grant: %v", err)
	}
	d, ap, err := s.Decide("bob", r.ID, GrantOnce, "", "session")
	if err != nil || ap != nil || d.State != Approved || d.DecidedVia != "session" || !d.Expires.Equal(now.Add(TicketTTL)) {
		t.Fatalf("approve: %+v %+v %v", d, ap, err)
	}
	if _, _, err := s.Decide("bob", r.ID, "", "", "session"); !errors.Is(err, ErrNotPending) {
		t.Fatalf("decided twice: %v", err)
	}
	// bound: another method, path, query or body is refused and spends nothing
	for name, e := range map[string]error{
		"another method": redeem(r.ID, "PUT", r.Target, body),
		"another path":   redeem(r.ID, "POST", "/api/apps/mail/delete?draft=1", body),
		"another query":  redeem(r.ID, "POST", "/api/apps/mail/send?draft=0", body),
		"no query":       redeem(r.ID, "POST", "/api/apps/mail/send", body),
		"another body":   redeem(r.ID, "POST", r.Target, []byte(`{"to":"evil@example.com","text":"hi"}`)),
		"no body":        redeem(r.ID, "POST", r.Target, nil),
		"another content type": func() error {
			_, err := s.Redeem("bob", r.ID, "POST", r.Target, "apps/mail", "application/x-www-form-urlencoded", body)
			return err
		}(),
		"another tile than it resolved to": func() error {
			_, err := s.Redeem("bob", r.ID, "POST", r.Target, "apps/mail/nested", "application/json", body)
			return err
		}(),
	} {
		if !errors.Is(e, ErrTicketBound) {
			t.Errorf("%s: %v, want ErrTicketBound", name, e)
		}
	}
	if _, err := s.Redeem("alice", r.ID, "POST", r.Target, "apps/mail", "application/json", body); !errors.Is(err, ErrNoTicket) {
		t.Fatalf("alice redeemed bob's ticket: %v", err)
	}
	used, err := s.Redeem("bob", r.ID, "POST", r.Target, "apps/mail", "application/json", body)
	if err != nil || used.State != Used || used.Body != "" {
		t.Fatalf("redeem: %+v %v", used, err)
	}
	if err := redeem(r.ID, "POST", r.Target, body); !errors.Is(err, ErrNoTicket) {
		t.Fatalf("a ticket redeemed twice: %v", err)
	}

	// a ticket runs out; so does a request nobody answers
	r2, _ := s.NewRequest(Request{User: "bob", Agent: "xbin/agent", Kind: KindCall, Method: "POST", Target: "/api/apps/mail/send", Tile: "apps/mail", ContentType: "application/json"}, body)
	r3, _ := s.NewRequest(Request{User: "bob", Agent: "xbin/agent", Kind: KindSession, Method: "POST", Target: "/api/xbin/term/sessions"}, []byte{0xff, 0xfe})
	if r3.BodyShown || r3.Body != "" || r3.BodySize != 2 {
		t.Fatalf("a body that isn't text is bound, not shown: %+v", r3)
	}
	if _, _, err := s.Decide("bob", r3.ID, GrantHour, "", "session"); !errors.Is(err, ErrStandingKind) {
		t.Fatalf("a standing approval of a session start: %v", err)
	}
	_, _, _ = s.Decide("bob", r2.ID, GrantOnce, "", "session")
	*now = now.Add(TicketTTL)
	if err := redeem(r2.ID, "POST", r2.Target, body); !errors.Is(err, ErrNoTicket) {
		t.Fatalf("an expired ticket: %v", err)
	}
	*now = now.Add(PendingTTL)
	if got, _ := s.Request("bob", r3.ID); got.State != Expired {
		t.Fatalf("an unanswered request: %+v", got)
	}
	if pend, _ := s.Requests("bob", true); len(pend) != 0 {
		t.Fatalf("pending: %+v", pend)
	}
	*now = now.Add(keepDecided + time.Second)
	if all, _ := s.Requests("bob", false); len(all) != 0 {
		t.Fatalf("old requests stay listed: %+v", all)
	}

	// a path past the limit isn't stored
	if _, err := s.NewRequest(Request{User: "bob", Kind: KindCall, Method: "POST", Target: "/api/apps/x/" + strings.Repeat("a", MaxTarget), Tile: "apps/x"}, nil); err == nil {
		t.Fatal("a path past the limit was taken")
	}
	// asking and withdrawing in a loop grows nothing: the list is bounded
	for i := 0; i < MaxRequests+50; i++ {
		r, err := s.NewRequest(Request{User: "carol", Kind: KindCall, Method: "POST", Target: "/api/apps/x/a", Tile: "apps/x"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Withdraw("carol", r.ID); err != nil {
			t.Fatal(err)
		}
	}
	if all, _ := s.Requests("carol", false); len(all) > MaxRequests {
		t.Fatalf("%d requests are kept for carol, more than %d", len(all), MaxRequests)
	}
	// the pending limit, withdrawing, the body limit
	var last Request
	for i := 0; i < MaxPending; i++ {
		if last, err = s.NewRequest(Request{User: "bob", Kind: KindCall, Method: "POST", Target: "/api/apps/x/a", Tile: "apps/x"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.NewRequest(Request{User: "bob", Kind: KindCall, Method: "POST", Target: "/api/apps/x/a", Tile: "apps/x"}, nil); !errors.Is(err, ErrTooMany) {
		t.Fatalf("past the limit: %v", err)
	}
	if w, err := s.Withdraw("bob", last.ID); err != nil || w.State != Withdrawn {
		t.Fatalf("withdraw: %+v %v", w, err)
	}
	if _, err := s.Withdraw("bob", last.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("withdrawn twice: %v", err)
	}
	if _, err := s.NewRequest(Request{User: "bob", Kind: KindCall, Method: "POST", Target: "/api/apps/x/a", Tile: "apps/x"}, make([]byte, MaxBody+1)); err == nil {
		t.Fatal("a body past the limit was taken")
	}
	// the file is the person's alone
	fi, err := os.Stat(s.userFile("bob", "requests.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("requests.json: %v %v", fi, err)
	}
}

// Standing approvals: by default one call — a method on a path of a tile —
// for an hour or until revoked; every write to the tile only when the
// person chose that wider scope.
func TestStandingApprovals(t *testing.T) {
	s, now := testStore(t)
	r, _ := s.NewRequest(Request{User: "bob", Kind: KindCall, Method: "POST", Target: "/api/apps/mail/send?draft=1", Tile: "apps/mail"}, nil)
	d, ap, err := s.Decide("bob", r.ID, GrantHour, "", "session")
	if err != nil || ap == nil || ap.Tile != "apps/mail" || ap.Scope != ScopeCall || ap.Method != "POST" || ap.Path != "/api/apps/mail/send" ||
		!ap.Until.Equal(now.Add(time.Hour)) || ap.Request != r.ID || d.State != Approved {
		t.Fatalf("an hour, this call: %+v %+v %v", d, ap, err)
	}
	if a, ok := s.Covers("bob", "apps/mail", "POST", "/api/apps/mail/send"); !ok || a.ID != ap.ID {
		t.Fatal("the approved call isn't covered")
	}
	for _, c := range [][4]string{
		{"bob", "apps/mail", "POST", "/api/apps/mail/delete-everything"}, // another path of the tile
		{"bob", "apps/mail", "DELETE", "/api/apps/mail/send"},            // another method
		{"alice", "apps/mail", "POST", "/api/apps/mail/send"},
		{"bob", "apps/other", "POST", "/api/apps/mail/send"},
		{"bob", "", "POST", "/api/apps/mail/send"},
	} {
		if _, ok := s.Covers(c[0], c[1], c[2], c[3]); ok {
			t.Errorf("%s is covered for %s %s on %q", c[0], c[2], c[3], c[1])
		}
	}
	*now = now.Add(time.Hour)
	if _, ok := s.Covers("bob", "apps/mail", "POST", "/api/apps/mail/send"); ok {
		t.Fatal("covered past the hour")
	}
	// the wider choice, made explicitly
	r2, _ := s.NewRequest(Request{User: "bob", Kind: KindCall, Method: "POST", Target: "/api/apps/mail/send", Tile: "apps/mail"}, nil)
	if _, _, err := s.Decide("bob", r2.ID, GrantAlways, "everything", "session"); !errors.Is(err, ErrBadScope) {
		t.Fatalf("a scope that isn't one: %v", err)
	}
	_, wide, err := s.Decide("bob", r2.ID, GrantAlways, ScopeTile, "session")
	if err != nil || wide.Scope != ScopeTile || wide.Method != "" || !wide.Until.IsZero() {
		t.Fatalf("always, the tile: %+v %v", wide, err)
	}
	if _, ok := s.Covers("bob", "apps/mail", "DELETE", "/api/apps/mail/anything"); !ok {
		t.Fatal("the tile-wide approval doesn't cover the tile")
	}
	if _, ok := s.Covers("bob", "apps/x", "POST", "/api/apps/x/a"); ok {
		t.Fatal("…and covers another tile")
	}
	again, _ := s.AddApproval(Approval{User: "bob", Tile: "apps/mail", Scope: ScopeTile}, GrantHour, "session") // replaces the same thing
	if list, _ := s.Approvals("bob"); len(list) != 1 || list[0].ID != again.ID {
		t.Fatalf("list: %+v", list)
	}
	for name, a := range map[string]Approval{
		"no scope": {User: "bob", Tile: "apps/mail"}, "a call without its path": {User: "bob", Tile: "apps/mail", Scope: ScopeCall, Method: "POST"},
		"no tile": {User: "bob", Scope: ScopeTile},
	} {
		if _, err := s.AddApproval(a, GrantAlways, "session"); err == nil {
			t.Errorf("%s was recorded", name)
		}
	}
	if _, err := s.AddApproval(Approval{User: "bob", Tile: "apps/mail", Scope: ScopeTile}, GrantOnce, "session"); !errors.Is(err, ErrBadGrant) {
		t.Fatalf("a once approval: %v", err)
	}
	if ok, _ := s.RevokeApproval("alice", again.ID); ok {
		t.Fatal("alice revoked bob's approval")
	}
	if ok, err := s.RevokeApproval("bob", again.ID); !ok || err != nil {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	if _, ok := s.Covers("bob", "apps/mail", "POST", "/api/apps/mail/send"); ok {
		t.Fatal("covered after the revoke")
	}
	// a tile that is gone takes its approvals with it
	_, _ = s.AddApproval(Approval{User: "bob", Tile: "apps/mail", Scope: ScopeTile}, GrantAlways, "session")
	_, _ = s.AddApproval(Approval{User: "bob", Tile: "apps/x", Scope: ScopeTile}, GrantAlways, "session")
	if n, err := s.DropApprovals("bob", func(a Approval) bool { return a.Tile == "apps/mail" }); n != 1 || err != nil {
		t.Fatalf("drop: %d %v", n, err)
	}
	if list, _ := s.Approvals("bob"); len(list) != 1 || list[0].Tile != "apps/x" {
		t.Fatalf("after the drop: %+v", list)
	}
	// an unreadable file, or a record that says no scope, covers nothing
	_ = os.WriteFile(s.userFile("bob", "approvals.json"), []byte(`{"schema":1,"approvals":[{"id":"x","user":"bob","tile":"apps/x","created":"2026-11-14T12:00:00Z"}]}`), 0o600)
	if _, ok := s.Covers("bob", "apps/x", "POST", "/api/apps/x/a"); ok {
		t.Fatal("a record without a scope covers")
	}
	_ = os.WriteFile(s.userFile("bob", "approvals.json"), []byte("{"), 0o600)
	if _, ok := s.Covers("bob", "apps/x", "POST", "/api/apps/x/a"); ok {
		t.Fatal("an unreadable approvals file covers")
	}
}

// One person's agent can't stall another's: each person's files are behind
// that person's lock, and a held one — a slow disk, a change set being
// claimed — leaves everyone else's calls running. And a hand edit is seen
// by the next call, without a read per call in between.
func TestPeopleDontWaitOnEachOther(t *testing.T) {
	s, _ := testStore(t)
	_, _ = s.AddApproval(Approval{User: "bob", Tile: "apps/x", Scope: ScopeTile}, GrantAlways, "session")
	cs, err := s.NewChangeSet(ChangeSet{User: "alice", Items: []ChangeItem{{ChangeOp: ChangeOp{Kind: "grant.add"}}}})
	if err != nil {
		t.Fatal(err)
	}
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() { // alice's lock, held
		_, _ = s.UpdateChangeSet("alice", cs.ID, func(*ChangeSet) error { close(held); <-release; return nil })
		close(done)
	}()
	<-held
	bobs := make(chan bool, 1)
	go func() {
		_, ok := s.Covers("bob", "apps/x", "POST", "/api/apps/x/a")
		_ = s.State("bob")
		_ = s.Audit.Append(Call{User: "bob", Method: "POST", Path: "/api/apps/x/a", Status: 200})
		_, err := s.NewRequest(Request{User: "bob", Kind: KindCall, Method: "POST", Target: "/api/apps/x/a", Tile: "apps/x"}, nil)
		bobs <- ok && err == nil
	}()
	select {
	case ok := <-bobs:
		if !ok {
			t.Fatal("bob's calls failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bob's calls waited on alice's lock")
	}
	close(release)
	<-done

	// a hand edit (a restore) is what the next call sees
	path := s.userFile("bob", "approvals.json")
	fi, _ := os.Stat(path)
	if err := os.WriteFile(path, []byte(`{"schema":1,"approvals":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(path, fi.ModTime().Add(time.Second), fi.ModTime().Add(time.Second))
	if _, ok := s.Covers("bob", "apps/x", "POST", "/api/apps/x/a"); ok {
		t.Fatal("a rewritten approvals file isn't seen")
	}
}

// BenchmarkCoversBesideABusyPerson measures the gate's hot read — is this
// write covered by a standing approval — for one person while another's
// agent floods the store with requests. Before per-person locks and the
// parsed-file cache every Covers re-read its file under the one lock the
// flood held.
func BenchmarkCoversBesideABusyPerson(b *testing.B) {
	s := OpenStore(b.TempDir())
	for i := 0; i < 50; i++ {
		_, _ = s.AddApproval(Approval{User: "bob", Tile: "apps/x", Scope: ScopeCall, Method: "POST", Path: "/api/apps/x/p" + strings.Repeat("a", i)}, GrantAlways, "session")
	}
	stop := make(chan struct{})
	go func() { // alice's agent: asks and withdraws as fast as the disk allows
		big := make([]byte, MaxBody)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if r, err := s.NewRequest(Request{User: "alice", Kind: KindCall, Method: "POST", Target: "/api/apps/x/a", Tile: "apps/x"}, big); err == nil {
				_, _ = s.Withdraw("alice", r.ID)
			}
		}
	}()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := s.Covers("bob", "apps/x", "POST", "/api/apps/x/p"); !ok {
			b.Fatal("not covered")
		}
	}
	b.StopTimer()
	close(stop)
}

// Change sets: stored per person, limited, expiring, updated in place.
func TestChangeSetStore(t *testing.T) {
	s, now := testStore(t)
	op := ChangeOp{Kind: "grant.add", Args: json.RawMessage(`{"from":"apps/a","target":"apps/b","role":"reader"}`)}
	if _, err := s.NewChangeSet(ChangeSet{User: "alice"}); !errors.Is(err, ErrEmptyChangeSet) {
		t.Fatalf("an empty set: %v", err)
	}
	if _, err := s.NewChangeSet(ChangeSet{User: "alice", Items: make([]ChangeItem, MaxItems+1)}); !errors.Is(err, ErrTooManyItems) {
		t.Fatalf("too many items: %v", err)
	}
	cs, err := s.NewChangeSet(ChangeSet{User: "alice", Agent: "xbin/agent", Title: "wire a to b", State: SetApplied,
		Items: []ChangeItem{{ChangeOp: op, Line: "Let apps/a call apps/b as reader", Result: ItemApplied}, {ChangeOp: op}}})
	if err != nil || cs.State != SetProposed || cs.Items[0].N != 1 || cs.Items[1].N != 2 || cs.Items[0].Result != "" || !cs.Expires.Equal(now.Add(SetTTL)) {
		t.Fatalf("new: %+v %v", cs, err)
	}
	if _, ok := s.ChangeSet("bob", cs.ID); ok {
		t.Fatal("bob reads alice's set")
	}
	for _, id := range []string{"", "../" + cs.ID, cs.ID + "/x", "a.b"} {
		if _, ok := s.ChangeSet("alice", id); ok {
			t.Errorf("id %q found a set", id)
		}
	}
	up, err := s.UpdateChangeSet("alice", cs.ID, func(c *ChangeSet) error { c.Items[0].Result = ItemApplied; c.State = SetPartial; return nil })
	if err != nil || up.State != SetPartial {
		t.Fatalf("update: %+v %v", up, err)
	}
	if _, err := s.UpdateChangeSet("alice", cs.ID, func(*ChangeSet) error { return ErrSetNotOpen }); !errors.Is(err, ErrSetNotOpen) {
		t.Fatalf("a refused update: %v", err)
	}
	if got, _ := s.ChangeSet("alice", cs.ID); got.State != SetPartial || got.Items[0].Result != ItemApplied {
		t.Fatalf("read back: %+v", got)
	}
	if _, err := s.UpdateChangeSet("bob", cs.ID, func(*ChangeSet) error { return nil }); !errors.Is(err, ErrNoChangeSet) {
		t.Fatalf("bob updates alice's set: %v", err)
	}
	// a file planted under another person's directory with this person's id is not theirs
	planted := cs
	planted.ID = "planted"
	b, _ := json.Marshal(planted)
	_ = os.MkdirAll(s.setsDir("bob"), 0o700)
	_ = os.WriteFile(filepath.Join(s.setsDir("bob"), "planted.json"), b, 0o600)
	if sets, _ := s.ChangeSets("bob"); len(sets) != 0 {
		t.Fatalf("a set naming alice is listed for bob: %+v", sets)
	}
	// a set xbind stopped while applying: partial, its unrun items skipped
	_, _ = s.UpdateChangeSet("alice", cs.ID, func(c *ChangeSet) error {
		c.State, c.Decided, c.Items[0].Result, c.Items[1].Result = SetApplying, *now, ItemApplied, ""
		return nil
	})
	if got, _ := s.ChangeSet("alice", cs.ID); got.State != SetApplying {
		t.Fatalf("applying: %s", got.State)
	}
	*now = now.Add(applyingStale + time.Second)
	if got, _ := s.ChangeSet("alice", cs.ID); got.State != SetPartial || got.Items[0].Result != ItemApplied || got.Items[1].Result != ItemSkipped {
		t.Fatalf("an interrupted set: %+v", got)
	}
	// the open limit and expiry
	for i := 0; i < MaxOpenSets; i++ {
		if _, err := s.NewChangeSet(ChangeSet{User: "alice", Items: []ChangeItem{{ChangeOp: op}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.NewChangeSet(ChangeSet{User: "alice", Items: []ChangeItem{{ChangeOp: op}}}); !errors.Is(err, ErrTooManySets) {
		t.Fatalf("past the open limit: %v", err)
	}
	*now = now.Add(SetTTL)
	sets, _ := s.ChangeSets("alice")
	for _, c := range sets {
		if c.ID != cs.ID && c.State != SetExpired {
			t.Errorf("an old proposal is %s", c.State)
		}
	}
	if _, err := s.NewChangeSet(ChangeSet{User: "alice", Items: []ChangeItem{{ChangeOp: op}}}); err != nil {
		t.Fatalf("after the old ones expired: %v", err)
	}
}

// Change sets are bounded and don't sit on the gate's lock (the second
// review's N4): a person's decided sets are capped in number and kept a
// month, a set is capped in size, a set's file is parsed once (not on
// every call), and reading or changing sets never holds the lock the
// gate's State() takes on every delegate call.
func TestChangeSetsAreBounded(t *testing.T) {
	s, now := testStore(t)
	op := ChangeOp{Kind: "grant.add", Args: json.RawMessage(`{"from":"apps/a","target":"apps/b","role":"reader"}`)}
	item := ChangeItem{ChangeOp: op, Line: "Let apps/a use apps/b as reader"}
	withdraw := func(cs *ChangeSet) error {
		cs.State, cs.Decided = SetWithdrawn, s.now().UTC()
		return nil
	}
	// an agent in a loop: propose, withdraw, again
	for i := 0; i < 3*MaxDecidedSets; i++ {
		cs, err := s.NewChangeSet(ChangeSet{User: "bob", Agent: "xbin/wsagent", Title: "t", Items: []ChangeItem{item}})
		if err != nil {
			t.Fatal(i, err)
		}
		if _, err := s.UpdateChangeSet("bob", cs.ID, withdraw); err != nil {
			t.Fatal(err)
		}
	}
	sets, err := s.ChangeSets("bob")
	if err != nil || len(sets) > MaxDecidedSets {
		t.Fatalf("%d decided sets are kept (at most %d): %v", len(sets), MaxDecidedSets, err)
	}
	if files, _ := filepath.Glob(filepath.Join(s.setsDir("bob"), "*.json")); len(files) > MaxDecidedSets {
		t.Fatalf("%d files on disk", len(files))
	}
	// the newest are the ones kept
	last, _ := s.NewChangeSet(ChangeSet{User: "bob", Title: "the last", Items: []ChangeItem{item}})
	if got, ok := s.ChangeSet("bob", last.ID); !ok || got.Title != "the last" {
		t.Fatal("the newest set is gone")
	}

	// a set's size
	big := item
	big.Args = json.RawMessage(`{"from":"` + strings.Repeat("a", MaxItemArgs) + `"}`)
	if _, err := s.NewChangeSet(ChangeSet{User: "bob", Items: []ChangeItem{big}}); !errors.Is(err, ErrSetTooLarge) {
		t.Fatalf("an operation with %d bytes of arguments: %v", len(big.Args), err)
	}
	fat := item
	fat.Before = json.RawMessage(`"` + strings.Repeat("b", MaxSetBytes) + `"`)
	if _, err := s.NewChangeSet(ChangeSet{User: "bob", Items: []ChangeItem{fat}}); !errors.Is(err, ErrSetTooLarge) {
		t.Fatalf("a set over %d bytes: %v", MaxSetBytes, err)
	}

	// decided sets go after KeepSets — a month, not three
	if KeepSets > 31*24*time.Hour {
		t.Fatalf("decided sets are kept %s", KeepSets)
	}
	if _, err := s.UpdateChangeSet("bob", last.ID, withdraw); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(KeepSets + time.Hour)
	if sets, _ := s.ChangeSets("bob"); len(sets) != 0 {
		t.Fatalf("%d sets after %s", len(sets), KeepSets)
	}

	// a file is parsed once: with its bytes swapped behind an unchanged
	// size and time, the set read is still the one that was parsed
	cs, err := s.NewChangeSet(ChangeSet{User: "bob", Title: "parsed once", Items: []ChangeItem{item}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.setsDir("bob"), cs.ID+".json")
	fi, _ := os.Stat(path)
	raw, _ := os.ReadFile(path)
	swapped := []byte(strings.Replace(string(raw), "parsed once", "parsed twic", 1))
	if err := os.WriteFile(path, swapped, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ChangeSet("bob", cs.ID); got.Title != "parsed once" {
		t.Fatalf("the file was read again: %q", got.Title)
	}

	// and the gate's lock is never held over them: State() answers while a
	// set is being changed
	done := make(chan State, 1)
	if _, err := s.UpdateChangeSet("bob", cs.ID, func(*ChangeSet) error {
		go func() { done <- s.State("bob") }()
		select {
		case <-done:
			return nil
		case <-time.After(2 * time.Second):
			return errors.New("State() waited on the change sets' lock")
		}
	}); err != nil {
		t.Fatal(err)
	}
}
