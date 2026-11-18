package broker

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/xbin-dev/xbin/internal/signins"
	"github.com/xbin-dev/xbin/internal/users"
)

// covers D245 × D246 — a person given write through an approved access.set
// line is someone who can change the tile's code like any other new
// editor: the apply announces that people's access changed (the event the
// trust answers are revalidated and the record of past editors is written
// on), the tile's owner is asked for her trust from then on, and when the
// person loses write again the record remembers them.
func TestAccessSetIsSeenByThePastRecord(t *testing.T) {
	w := accessWorld(t)
	w.b.SigninPast = signins.NewPast(filepath.Join(t.TempDir(), "signins", "tiles.json"), nil)
	announced := 0
	prev := w.b.OnPeopleChanged
	w.b.OnPeopleChanged = func() { // as boot wires it (boot/signins.go revalidate)
		if prev != nil {
			prev()
		}
		announced++
		w.b.ObserveTileParties()
	}
	_, alice := w.session("alice")
	before, ok := w.b.SigninSource("alice", "apps/x")
	if !ok || slices.Contains(before.Editors, "carol") || slices.Contains(before.Past, "carol") {
		t.Fatalf("before the line: %+v", before)
	}
	cs := w.plan(t, "alice", `[{"kind":"access.set","args":{"tile":"apps/x","user":"carol","level":"write"}}]`)
	if done := w.approve(t, cs.ID, `{"items":[1],"knows":["access.set"]}`, alice); results(done) != "applied" {
		t.Fatalf("alice applies it: %s", results(done))
	}
	if announced == 0 {
		t.Fatal("an applied access.set line did not announce that people's access changed: no trust is revalidated, nothing is recorded")
	}
	now, _ := w.b.SigninSource("alice", "apps/x")
	if !slices.Contains(now.Editors, "carol") || !now.NeedsTrust() || now.Basis() == before.Basis() {
		t.Fatalf("after carol was given write: editors %v, needs trust %v", now.Editors, now.NeedsTrust())
	}
	// she loses it again: "only alice's" now — and the record remembers
	if err := w.b.Users.SetUserTile("carol", "apps/x", users.LevelRead); err != nil {
		t.Fatal(err)
	}
	w.b.OnPeopleChanged()
	after, _ := w.b.SigninSource("alice", "apps/x")
	if slices.Contains(after.Editors, "carol") || !slices.Contains(after.Past, "carol") || !after.NeedsTrust() {
		t.Fatalf("after carol lost write: editors %v past %v", after.Editors, after.Past)
	}
}
