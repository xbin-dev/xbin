package signins

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// covers D230 (the rule of 2026-11-17 and its review) — a person's "I
// trust this tile's source": kept with their sign-ins, for the tile as it
// stood and as they were shown it; VOID from the moment its basis is seen
// to differ, and void until they answer again; listed, revocable, gone
// with an account reset, and under the workspace's switch.

func bobs(editors ...string) Source {
	return Source{Tile: "apps/crm", ID: "i7:b1", Name: "CRM", Owner: "user:bob", OwnerKind: "person", OwnerID: "bob", NotOwn: true, Editors: editors}
}

func TestSourceNeedsTrust(t *testing.T) {
	for name, c := range map[string]struct {
		src  Source
		want bool
	}{
		"her own, nobody else can change it":            {Source{Owner: "user:alice", OwnerKind: "you"}, false},
		"condition 1: owned by someone else":            {Source{Owner: "user:bob", NotOwn: true}, true},
		"condition 1: an org's":                         {Source{Owner: "org:sales", NotOwn: true}, true},
		"condition 1: the workspace's, for a non-admin": {Source{Owner: "", NotOwn: true}, true},
		"the workspace's, for a workspace admin":        {Source{Owner: "", Admin: true}, false},
		"condition 2: her own, with a co-editor":        {Source{Owner: "user:alice", Editors: []string{"bob"}}, CoEditorsNeedTrust},
	} {
		if got := c.src.NeedsTrust(); got != c.want {
			t.Errorf("%s: needs trust %v, want %v", name, got, c.want)
		}
	}
	// the basis token: the tile, its path, its owner, whose it is, everyone who can change it
	base := bobs("carol").Basis()
	for name, other := range map[string]Source{
		"an editor more":   bobs("carol", "dave"),
		"an editor fewer":  bobs(),
		"another editor":   bobs("dave"),
		"another owner":    {Tile: "apps/crm", ID: "i7:b1", Owner: "user:erin", NotOwn: true, Editors: []string{"carol"}},
		"another path":     {Tile: "apps/crm2", ID: "i7:b1", Owner: "user:bob", NotOwn: true, Editors: []string{"carol"}},
		"another tile":     {Tile: "apps/crm", ID: "i9:b2", Owner: "user:bob", NotOwn: true, Editors: []string{"carol"}},
		"now someone's":    {Tile: "apps/crm", ID: "i7:b1", Owner: "user:bob", NotOwn: false, Editors: []string{"carol"}},
		"a split id trick": {Tile: "apps/crm", ID: "i7:b1", Owner: "user:bob", NotOwn: true, Editors: []string{"car", "ol"}},
	} {
		if other.Basis() == base {
			t.Errorf("%s has the same basis", name)
		}
	}
	if again := bobs("carol"); again.Basis() != base || !strings.HasPrefix(base, "tb_") {
		t.Errorf("the same source, another basis: %s %s", again.Basis(), base)
	}
	if named := bobs("carol"); true {
		named.Name, named.OwnerName, named.Names = "Renamed", "Bobby", map[string]string{"carol": "Bob"}
		if named.Basis() != base {
			t.Error("display names are part of the basis: they are their bearers' to change")
		}
	}
}

func TestTrust(t *testing.T) {
	w := newWorld(t)
	src := bobs("carol")
	if w.s.Trusts("alice", src) {
		t.Fatal("trusted before she said so")
	}
	// her answer is for what she was shown: another basis is refused, and nothing is kept
	for name, basis := range map[string]string{"none": "", "an older question's": bobs().Basis(), "junk": "tb_00"} {
		if _, err := w.s.TrustTile("alice", src, "card", basis); !errors.Is(err, ErrTrustChanged) {
			t.Fatalf("an answer with %s basis: %v", name, err)
		}
	}
	if len(w.s.Trusted("alice")) != 0 {
		t.Fatal("a refused answer was kept")
	}
	tr, err := w.s.TrustTile("alice", src, "card", src.Basis())
	if err != nil || tr.ID == "" || tr.Tile != "apps/crm" || tr.At != w.now.UnixMilli() || tr.Via != "card" {
		t.Fatalf("TrustTile: %+v %v", tr, err)
	}
	// kept with no sign-in saved at all, in her file, with no secret in it
	if b, err := os.ReadFile(w.s.path("alice")); err != nil || !strings.Contains(string(b), `"trust"`) {
		t.Fatalf("her file: %v", err)
	}
	if !w.s.Trusts("alice", src) || w.s.Trusts("bob", src) {
		t.Fatal("her answer is hers, and holds")
	}
	// a tile with no identity can't be trusted
	noID := Source{Tile: "apps/y", Owner: "user:bob", NotOwn: true}
	if _, err := w.s.TrustTile("alice", noID, "card", noID.Basis()); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("no identity: %v", err)
	}
	// someone who can no longer change it is dropped from what she trusted…
	if !w.s.Trusts("alice", bobs()) {
		t.Fatal("carol lost write: her answer no longer holds")
	}
	if got := w.s.Trusted("alice"); len(got) != 1 || len(got[0].Editors) != 0 || got[0].Void() {
		t.Fatalf("the record after carol left: %+v", got)
	}
	// …so their coming back is someone new: void, and void for good
	if w.s.Trusts("alice", bobs("carol")) {
		t.Fatal("carol came back, and the old answer covers her again")
	}
	got := w.s.Trusted("alice")
	if len(got) != 1 || !got[0].Void() || got[0].VoidWhy != VoidEditor || got[0].VoidAt != w.now.UnixMilli() {
		t.Fatalf("the record once someone new could change the tile: %+v", got)
	}
	if w.s.Trusts("alice", bobs()) {
		t.Fatal("the newcomer left, and the void answer holds again")
	}
	// answered again: one answer per tile, the newer, live
	w.now = w.now.Add(time.Hour)
	again := bobs("carol", "dave")
	if _, err := w.s.TrustTile("alice", again, "approval", again.Basis()); err != nil {
		t.Fatal(err)
	}
	if got := w.s.Trusted("alice"); len(got) != 1 || got[0].Void() || got[0].Via != "approval" || !w.s.Trusts("alice", again) {
		t.Fatalf("after she answered again: %+v", got)
	}
	// every other change of the basis voids too, each for good
	for name, c := range map[string]struct {
		now Source
		why string
	}{
		"another owner":               {Source{Tile: "apps/crm", ID: "i7:b1", Owner: "user:erin", NotOwn: true, Editors: []string{"carol", "dave"}}, VoidOwner},
		"made again at the same path": {Source{Tile: "apps/crm", ID: "i9:b2", Owner: "user:bob", NotOwn: true, Editors: []string{"carol", "dave"}}, VoidRemade},
		"moved to another path":       {Source{Tile: "apps/crm2", ID: "i7:b1", Owner: "user:bob", NotOwn: true, Editors: []string{"carol", "dave"}}, VoidMoved},
		"its identity can't be read":  {Source{Tile: "apps/crm", ID: "", Owner: "user:bob", NotOwn: true, Editors: []string{"carol", "dave"}}, VoidRemade},
	} {
		if _, err := w.s.TrustTile("alice", again, "card", again.Basis()); err != nil {
			t.Fatal(err)
		}
		if w.s.Trusts("alice", c.now) {
			t.Errorf("%s: still trusted", name)
		}
		got := w.s.Trusted("alice")
		if len(got) != 1 || got[0].VoidWhy != c.why {
			t.Errorf("%s: the record: %+v", name, got)
		}
		if w.s.Trusts("alice", again) {
			t.Errorf("%s, and back as it was (A→B→A): the void answer holds again", name)
		}
	}
	// revoked: only hers, once
	if _, err := w.s.TrustTile("alice", again, "card", again.Basis()); err != nil {
		t.Fatal(err)
	}
	other := Source{Tile: "apps/wiki", ID: "i8", Owner: "org:sales", OwnerKind: "org", NotOwn: true}
	if _, err := w.s.TrustTile("alice", other, "plan", other.Basis()); err != nil {
		t.Fatal(err)
	}
	sg := save(t, w, "alice", "claude", "", fakeOAT, false) // saving and removing a sign-in keeps her answers
	if _, err := w.s.Remove("alice", sg.ID); err != nil {
		t.Fatal(err)
	}
	list := w.s.Trusted("alice")
	if len(list) != 2 {
		t.Fatalf("removing her last sign-in dropped her trusted tiles: %+v", list)
	}
	crm := list[slices.IndexFunc(list, func(x Trust) bool { return x.Tile == "apps/crm" })]
	if _, err := w.s.Untrust("bob", crm.ID); !errors.Is(err, ErrNoTrust) {
		t.Fatalf("bob revoked hers: %v", err)
	}
	if _, err := w.s.Untrust("alice", crm.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Untrust("alice", crm.ID); !errors.Is(err, ErrNoTrust) {
		t.Fatalf("revoked twice: %v", err)
	}
	if w.s.Trusts("alice", again) || !w.s.Trusts("alice", other) {
		t.Fatal("after the revoke")
	}
	// an admin-made way into her account: her answers go with her sign-ins
	w.s.Clear("alice", "reset")
	if w.s.Trusts("alice", other) || len(w.s.Trusted("alice")) != 0 {
		t.Fatal("an account reset kept her trusted tiles")
	}
	// someone who is no person here can't trust
	if _, err := w.s.TrustTile("nobody", src, "card", src.Basis()); err == nil {
		t.Fatal("an unknown account trusted a tile")
	}
}

// A basis change voids the trust WHEN IT IS SEEN — at the event, with no
// session running and nobody asking — and it stays void: mallory gains
// write, plants a hook, loses write; alice's old trust must not hold again.
func TestTrustIsVoidedWhenTheChangeIsSeen(t *testing.T) {
	w := newWorld(t)
	editors := []string{"carol"}
	owner := "user:bob"
	gone := false
	source := func(user, tile string) (Source, bool) {
		if gone || tile != "apps/crm" {
			return Source{}, false
		}
		return Source{Tile: tile, ID: "i7:b1", Owner: owner, NotOwn: true, Editors: slices.Clone(editors)}, true
	}
	trust := func(user string) {
		t.Helper()
		src, _ := source(user, "apps/crm")
		if _, err := w.s.TrustTile(user, src, "card", src.Basis()); err != nil {
			t.Fatal(err)
		}
	}
	now := func(user string) bool { src, _ := source(user, "apps/crm"); return w.s.Trusts(user, src) }
	trust("alice")
	trust("bob")
	if n := w.s.Revalidate(source); n != 0 || !now("alice") {
		t.Fatalf("nothing changed, yet %d voided", n)
	}
	// mallory gains write: seen at the event
	editors = []string{"carol", "mallory"}
	if n := w.s.Revalidate(source); n != 2 {
		t.Fatalf("mallory can change the tile: %d trusts voided, want alice's and bob's", n)
	}
	// …and loses it again, with nobody having asked in between
	editors = []string{"carol"}
	if n := w.s.Revalidate(source); n != 0 {
		t.Fatalf("revalidating void answers voided %d more", n)
	}
	if now("alice") || now("bob") {
		t.Fatal("mallory came and went, and the old trust holds again")
	}
	if got := w.s.Trusted("alice"); len(got) != 1 || got[0].VoidWhy != VoidEditor {
		t.Fatalf("her list: %+v", got)
	}
	// the owner changes and changes back
	trust("alice")
	owner = "user:erin"
	if n := w.s.Revalidate(source); n != 1 {
		t.Fatalf("another owner: %d voided", n)
	}
	owner = "user:bob"
	if now("alice") {
		t.Fatal("the owner changed and changed back (A→B→A), and the old trust holds again")
	}
	// the tile goes (moved away, deleted) and something stands at its path again
	trust("alice")
	gone = true
	if n := w.s.Revalidate(source); n != 1 {
		t.Fatalf("the tile is gone: %d voided", n)
	}
	gone = false
	if now("alice") {
		t.Fatal("the tile went and came back, and the old trust holds again")
	}
	if got := w.s.Trusted("alice"); got[0].VoidWhy != VoidGone {
		t.Fatalf("her list: %+v", got)
	}
	// one person's recheck (their list) judges theirs alone
	trust("alice")
	trust("bob")
	editors = nil
	if n := w.s.Recheck("alice", source); n != 0 || len(w.s.Trusted("alice")[0].Editors) != 0 || len(w.s.Trusted("bob")[0].Editors) != 1 {
		t.Fatalf("carol left: alice's record shrinks at her recheck, bob's is not touched (%d voided)", n)
	}
}

// The switch on trusting: on by default, the admins', host-lockable like
// the one beside it; off, nothing can be trusted and what was isn't used.
// It is kept in a file of its own, which an xbind from before the switch
// never rewrites: a downgrade keeps an admin's ban.
func TestTrustPolicy(t *testing.T) {
	w := newWorld(t)
	src := bobs()
	if p := w.s.Policy(); !p.Enabled || !p.Trust.Allowed || p.Trust.Locked {
		t.Fatalf("the default: %+v", p)
	}
	if _, err := w.s.TrustTile("alice", src, "card", src.Basis()); err != nil {
		t.Fatal(err)
	}
	p, err := w.s.SetTrustPolicy(false)
	if err != nil || p.Trust.Allowed || !p.Enabled {
		t.Fatalf("off: %+v %v", p, err)
	}
	if w.s.Trusts("alice", src) {
		t.Fatal("trusting is off, and her earlier answer is still used")
	}
	if got := w.s.Trusted("alice"); len(got) != 1 || got[0].Void() {
		t.Fatalf("the switch voided her answer (it is kept, unused): %+v", got)
	}
	if _, err := w.s.TrustTile("alice", src, "card", src.Basis()); !errors.Is(err, ErrTrustOff) {
		t.Fatalf("trusting while off: %v", err)
	}
	// the two switches are kept apart
	if p, _ := w.s.SetPolicy(false); p.Enabled || p.Trust.Allowed {
		t.Fatalf("sign-ins off: %+v", p)
	}
	if p, _ := w.s.SetPolicy(true); !p.Enabled || p.Trust.Allowed {
		t.Fatalf("sign-ins on again changed the other switch: %+v", p)
	}
	// an xbind from before the switch (v0.3.80) sets the one it knows by
	// rewriting policy.json with that key alone: the ban stays
	if err := os.WriteFile(w.s.PolicyPath(), []byte("{\n  \"enabled\": true\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := w.s.Policy(); p.Trust.Allowed || !p.Enabled {
		t.Fatalf("after an older xbind rewrote policy.json: %+v — the ban on trusting was lost", p)
	}
	if b, _ := os.ReadFile(w.s.PolicyPath()); strings.Contains(string(b), "trust") {
		t.Fatalf("the ban is kept in the file older versions rewrite: %s", b)
	}
	if p, _ := w.s.SetTrustPolicy(true); !p.Trust.Allowed || !w.s.Trusts("alice", src) {
		t.Fatalf("on again: %+v", p)
	}
	// the host's file sets and locks it
	no := false
	w.hostTrust = &no
	if p := w.s.Policy(); p.Trust.Allowed || !p.Trust.Locked || p.Trust.Reason != HostLockReason || p.Locked {
		t.Fatalf("held by the host: %+v", p)
	}
	if _, err := w.s.SetTrustPolicy(true); status(err) != 409 {
		t.Fatalf("a write while the host holds it: %v", err)
	}
	if _, err := w.s.SetPolicy(true); err != nil {
		t.Fatalf("the other switch is still the admins': %v", err)
	}
	// the people's list knows the files that are no person's
	if got := w.s.People(); len(got) != 0 {
		t.Fatalf("People() read a policy file as a person: %+v", got)
	}
}
