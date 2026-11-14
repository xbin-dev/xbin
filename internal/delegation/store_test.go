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
	if _, ok := none.Covers("bob", "apps/x"); ok {
		t.Fatal("a nil store covers")
	}
	if _, err := none.Redeem("bob", "x", "POST", "/api/apps/x/a", nil); !errors.Is(err, ErrNoTicket) {
		t.Fatalf("a nil store redeems: %v", err)
	}
}

// A request becomes a one-shot ticket bound to exactly its call.
func TestRequestsAndTickets(t *testing.T) {
	s, now := testStore(t)
	body := []byte(`{"to":"x@example.com","text":"hi"}`)
	r, err := s.NewRequest(Request{User: "bob", Agent: "xbin/agent", Kind: KindCall, Method: "POST", Target: "/api/apps/mail/send?draft=1", Tile: "apps/mail",
		Summary: strings.Repeat("s", 600), State: Approved, Grant: GrantAlways}, body)
	if err != nil || r.State != Pending || r.Grant != "" || r.ID == "" || !r.BodyShown || r.Body != string(body) || r.BodySHA256 != BodyHash(body) || len(r.Summary) != 500 {
		t.Fatalf("new: %+v %v", r, err)
	}
	redeem := func(id, method, target string, b []byte) error {
		_, err := s.Redeem("bob", id, method, target, b)
		return err
	}
	// pending: no ticket yet
	if err := redeem(r.ID, "POST", r.Target, body); !errors.Is(err, ErrNoTicket) {
		t.Fatalf("a pending request redeemed: %v", err)
	}
	// only its person decides it
	if _, _, err := s.Decide("alice", r.ID, GrantOnce, "session"); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("alice decided bob's request: %v", err)
	}
	if _, _, err := s.Decide("bob", r.ID, "forever", "session"); !errors.Is(err, ErrBadGrant) {
		t.Fatalf("a bad grant: %v", err)
	}
	d, ap, err := s.Decide("bob", r.ID, GrantOnce, "session")
	if err != nil || ap != nil || d.State != Approved || d.DecidedVia != "session" || !d.Expires.Equal(now.Add(TicketTTL)) {
		t.Fatalf("approve: %+v %+v %v", d, ap, err)
	}
	if _, _, err := s.Decide("bob", r.ID, "", "session"); !errors.Is(err, ErrNotPending) {
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
	} {
		if !errors.Is(e, ErrTicketBound) {
			t.Errorf("%s: %v, want ErrTicketBound", name, e)
		}
	}
	if _, err := s.Redeem("alice", r.ID, "POST", r.Target, body); !errors.Is(err, ErrNoTicket) {
		t.Fatalf("alice redeemed bob's ticket: %v", err)
	}
	used, err := s.Redeem("bob", r.ID, "POST", r.Target, body)
	if err != nil || used.State != Used || used.Body != "" {
		t.Fatalf("redeem: %+v %v", used, err)
	}
	if err := redeem(r.ID, "POST", r.Target, body); !errors.Is(err, ErrNoTicket) {
		t.Fatalf("a ticket redeemed twice: %v", err)
	}

	// a ticket runs out; so does a request nobody answers
	r2, _ := s.NewRequest(Request{User: "bob", Agent: "xbin/agent", Kind: KindCall, Method: "POST", Target: "/api/apps/mail/send", Tile: "apps/mail"}, body)
	r3, _ := s.NewRequest(Request{User: "bob", Agent: "xbin/agent", Kind: KindSession, Method: "POST", Target: "/api/xbin/term/sessions"}, []byte{0xff, 0xfe})
	if r3.BodyShown || r3.Body != "" || r3.BodySize != 2 {
		t.Fatalf("a body that isn't text is bound, not shown: %+v", r3)
	}
	if _, _, err := s.Decide("bob", r3.ID, GrantHour, "session"); !errors.Is(err, ErrStandingKind) {
		t.Fatalf("a standing approval of a session start: %v", err)
	}
	_, _, _ = s.Decide("bob", r2.ID, GrantOnce, "session")
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

// Standing approvals: a tile's API for an hour or until revoked.
func TestStandingApprovals(t *testing.T) {
	s, now := testStore(t)
	r, _ := s.NewRequest(Request{User: "bob", Kind: KindCall, Method: "POST", Target: "/api/apps/mail/send", Tile: "apps/mail"}, nil)
	d, ap, err := s.Decide("bob", r.ID, GrantHour, "frame")
	if err != nil || ap == nil || ap.Tile != "apps/mail" || !ap.Until.Equal(now.Add(time.Hour)) || ap.Request != r.ID || d.State != Approved {
		t.Fatalf("an hour: %+v %+v %v", d, ap, err)
	}
	if a, ok := s.Covers("bob", "apps/mail"); !ok || a.ID != ap.ID {
		t.Fatal("not covered")
	}
	for _, c := range [][2]string{{"alice", "apps/mail"}, {"bob", "apps/other"}, {"bob", "apps"}, {"bob", ""}} {
		if _, ok := s.Covers(c[0], c[1]); ok {
			t.Errorf("%s is covered on %q", c[0], c[1])
		}
	}
	*now = now.Add(time.Hour)
	if _, ok := s.Covers("bob", "apps/mail"); ok {
		t.Fatal("covered past the hour")
	}
	always, err := s.AddApproval("bob", "apps/mail", GrantAlways, "session")
	if err != nil || !always.Until.IsZero() {
		t.Fatalf("always: %+v %v", always, err)
	}
	again, _ := s.AddApproval("bob", "apps/mail", GrantHour, "session") // replaces: one a tile
	if list, _ := s.Approvals("bob"); len(list) != 1 || list[0].ID != again.ID {
		t.Fatalf("list: %+v", list)
	}
	if _, err := s.AddApproval("bob", "apps/mail", GrantOnce, "session"); !errors.Is(err, ErrBadGrant) {
		t.Fatalf("a once approval: %v", err)
	}
	if ok, _ := s.RevokeApproval("alice", again.ID); ok {
		t.Fatal("alice revoked bob's approval")
	}
	if ok, err := s.RevokeApproval("bob", again.ID); !ok || err != nil {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	if _, ok := s.Covers("bob", "apps/mail"); ok {
		t.Fatal("covered after the revoke")
	}
	// an unreadable file covers nothing
	_, _ = s.AddApproval("bob", "apps/mail", GrantAlways, "session")
	_ = os.WriteFile(s.userFile("bob", "approvals.json"), []byte("{"), 0o600)
	if _, ok := s.Covers("bob", "apps/mail"); ok {
		t.Fatal("an unreadable approvals file covers")
	}
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
