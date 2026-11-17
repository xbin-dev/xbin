package signins

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// covers D230 (the rule of 2026-11-17) — a person's "I trust this tile's
// source": kept with their sign-ins, for the tile as it stood, listed,
// revocable, gone with an account reset, and under the workspace's switch.

func bobs(editors ...string) Source {
	return Source{Tile: "apps/crm", ID: "i7:b1", Name: "CRM", Owner: "user:bob", OwnerKind: "person", OwnerName: "bob", NotOwn: true, Editors: editors}
}

func TestSourceNeedsTrust(t *testing.T) {
	for name, c := range map[string]struct {
		src  Source
		want bool
	}{
		"her own, nobody else can change it":     {Source{Owner: "user:alice", OwnerKind: "you"}, false},
		"condition 1: owned by someone else":     {Source{Owner: "user:bob", NotOwn: true}, true},
		"condition 1: an org's":                  {Source{Owner: "org:sales", NotOwn: true}, true},
		"condition 1: the workspace's":           {Source{Owner: "", NotOwn: true}, true},
		"condition 2: her own, with a co-editor": {Source{Owner: "user:alice", Editors: []string{"bob"}}, CoEditorsNeedTrust},
	} {
		if got := c.src.NeedsTrust(); got != c.want {
			t.Errorf("%s: needs trust %v, want %v", name, got, c.want)
		}
	}
}

func TestTrust(t *testing.T) {
	w := newWorld(t)
	src := bobs("carol")
	if w.s.Trusts("alice", src) {
		t.Fatal("trusted before she said so")
	}
	tr, err := w.s.TrustTile("alice", src, "card")
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
	// the basis: the same tile, the same owner, nobody new
	for name, c := range map[string]struct {
		src  Source
		want bool
	}{
		"as it stood":                     {bobs("carol"), true},
		"an editor fewer":                 {bobs(), true},
		"someone new can change it":       {bobs("carol", "dave"), !CoEditorsNeedTrust},
		"another owner":                   {Source{Tile: "apps/crm", ID: "i7:b1", Owner: "user:erin", NotOwn: true, Editors: []string{"carol"}}, false},
		"made again at the same path":     {Source{Tile: "apps/crm", ID: "i9:b2", Owner: "user:bob", NotOwn: true, Editors: []string{"carol"}}, false},
		"the same tile at another path":   {Source{Tile: "apps/crm2", ID: "i7:b1", Owner: "user:bob", NotOwn: true, Editors: []string{"carol"}}, true},
		"a tile whose identity is unread": {Source{Tile: "apps/crm", ID: "", Owner: "user:bob", NotOwn: true}, false},
	} {
		if got := w.s.Trusts("alice", c.src); got != c.want {
			t.Errorf("%s: trusted %v, want %v", name, got, c.want)
		}
	}
	// a tile with no identity can't be trusted
	if _, err := w.s.TrustTile("alice", Source{Tile: "apps/y", Owner: "user:bob", NotOwn: true}, "card"); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("no identity: %v", err)
	}
	// trusted again (someone new): one answer per tile, the newer
	w.now = w.now.Add(time.Hour)
	if _, err := w.s.TrustTile("alice", bobs("carol", "dave"), "approval"); err != nil {
		t.Fatal(err)
	}
	other := Source{Tile: "apps/wiki", ID: "i8", Owner: "org:sales", OwnerKind: "org", NotOwn: true}
	if _, err := w.s.TrustTile("alice", other, "plan"); err != nil {
		t.Fatal(err)
	}
	list := w.s.Trusted("alice")
	if len(list) != 2 || list[0].Tile != "apps/wiki" && list[1].Tile != "apps/wiki" || !w.s.Trusts("alice", bobs("carol", "dave")) {
		t.Fatalf("the list: %+v", list)
	}
	// saving and removing a sign-in keeps her answers
	sg := save(t, w, "alice", "claude", "", fakeOAT, false)
	if _, err := w.s.Remove("alice", sg.ID); err != nil {
		t.Fatal(err)
	}
	if len(w.s.Trusted("alice")) != 2 {
		t.Fatal("removing her last sign-in dropped her trusted tiles")
	}
	// revoked: only that one
	var crm Trust
	for _, x := range w.s.Trusted("alice") {
		if x.Tile == "apps/crm" {
			crm = x
		}
	}
	if _, err := w.s.Untrust("alice", crm.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Untrust("alice", crm.ID); !errors.Is(err, ErrNoTrust) {
		t.Fatalf("revoked twice: %v", err)
	}
	if _, err := w.s.Untrust("bob", w.s.Trusted("alice")[0].ID); !errors.Is(err, ErrNoTrust) {
		t.Fatalf("bob revoked hers: %v", err)
	}
	if w.s.Trusts("alice", bobs("carol", "dave")) || !w.s.Trusts("alice", other) {
		t.Fatal("after the revoke")
	}
	// an admin-made way into her account: her answers go with her sign-ins
	w.s.Clear("alice", "reset")
	if w.s.Trusts("alice", other) || len(w.s.Trusted("alice")) != 0 {
		t.Fatal("an account reset kept her trusted tiles")
	}
	// someone who is no person here can't trust
	if _, err := w.s.TrustTile("nobody", src, "card"); err == nil {
		t.Fatal("an unknown account trusted a tile")
	}
}

// The switch on trusting: on by default, the admins', host-lockable like
// the one beside it; off, nothing can be trusted and what was isn't used.
func TestTrustPolicy(t *testing.T) {
	w := newWorld(t)
	src := bobs()
	if p := w.s.Policy(); !p.Enabled || !p.Trust.Allowed || p.Trust.Locked {
		t.Fatalf("the default: %+v", p)
	}
	if _, err := w.s.TrustTile("alice", src, "card"); err != nil {
		t.Fatal(err)
	}
	p, err := w.s.SetTrustPolicy(false)
	if err != nil || p.Trust.Allowed || !p.Enabled {
		t.Fatalf("off: %+v %v", p, err)
	}
	if w.s.Trusts("alice", src) {
		t.Fatal("trusting is off, and her earlier answer is still used")
	}
	if _, err := w.s.TrustTile("alice", src, "card"); !errors.Is(err, ErrTrustOff) {
		t.Fatalf("trusting while off: %v", err)
	}
	// the two switches are kept apart
	if p, _ := w.s.SetPolicy(false); p.Enabled || p.Trust.Allowed {
		t.Fatalf("sign-ins off: %+v", p)
	}
	if p, _ := w.s.SetPolicy(true); !p.Enabled || p.Trust.Allowed {
		t.Fatalf("sign-ins on again changed the other switch: %+v", p)
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
}
