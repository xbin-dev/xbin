package signins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// covers D245 (the sticky condition) — once someone else could change a
// tile's code, the tile asks for each person's trust even after it is
// "only theirs" again: the record, per tile identity, of who could — made
// when a second party is seen or the one leaves, never for a tile that was
// only ever one party's, never in a one-person workspace.
func TestPastRemembersWhoCouldChangeATile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signins", "tiles.json")
	now := time.UnixMilli(1_800_000_000_000)
	p := NewPast(path, func() time.Time { return now })
	noFile := func(when string) {
		t.Helper()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s: a record was written: %v", when, err)
		}
		if p.Recorded() != 0 {
			t.Fatalf("%s: %d tiles recorded", when, p.Recorded())
		}
	}
	others := func(ident, user string) []string { got, _ := p.Others(ident, user); return got }

	// a one-person workspace: the workspace's own tiles, nobody with write
	// but admins — nothing is ever recorded
	for i := 0; i < 3; i++ {
		p.Observe("i-ws", "apps/welcome", "", nil)
		p.Observe("i-ws2", "apps/notes", "", nil)
	}
	noFile("a one-person workspace")
	// a tile its maker owns and never shared: nothing either
	p.Observe("i-hers", "apps/hers", "user:alice", nil)
	p.Observe("i-hers", "apps/hers", "user:alice", []string{"alice"}) // (herself, as an editor entry)
	noFile("a tile only ever one person's")
	if got := others("i-hers", "alice"); got != nil {
		t.Fatalf("others on her own tile: %v", got)
	}
	// no identity: nothing
	p.Observe("", "apps/x", "user:bob", []string{"mallory"})
	noFile("a tile without an identity")

	// mallory gets write on alice's tile: recorded — and while she can, she
	// is no "past" party (the question names her as an editor)
	p.Observe("i-hers", "apps/hers", "user:alice", []string{"mallory"})
	if p.Recorded() != 1 || others("i-hers", "alice") != nil {
		t.Fatalf("while mallory can change it: recorded %d, past %v", p.Recorded(), others("i-hers", "alice"))
	}
	// she loses it again: the tile is "only alice's" now — and remembers
	then := now
	now = now.Add(48 * time.Hour)
	p.Observe("i-hers", "apps/hers", "user:alice", nil)
	got, until := p.Others("i-hers", "alice")
	if !reflect.DeepEqual(got, []string{"mallory"}) || until != now.UnixMilli() {
		t.Fatalf("after mallory lost write: %v until %d (then %d)", got, until, then.UnixMilli())
	}
	// …for mallory herself the other party is alice's ownership, which is current: nobody past
	if got := others("i-hers", "mallory"); got != nil {
		t.Fatalf("mallory's view of her own past: %v", got)
	}
	// it survives a restart
	p = NewPast(path, func() time.Time { return now })
	if got := others("i-hers", "alice"); !reflect.DeepEqual(got, []string{"mallory"}) {
		t.Fatalf("after a restart: %v", got)
	}
	// she comes back: current again, not past; and leaves: past, with the later date
	p.Observe("i-hers", "apps/hers", "user:alice", []string{"mallory"})
	if got := others("i-hers", "alice"); got != nil {
		t.Fatalf("mallory back: %v", got)
	}
	now = now.Add(time.Hour)
	p.Observe("i-hers", "apps/hers", "user:alice", nil)
	if _, until := p.Others("i-hers", "alice"); until != now.UnixMilli() {
		t.Fatalf("until after she left again: %d", until)
	}

	// an owner before: bob's tile handed to carol — bob could change it
	p.Observe("i-crm", "apps/crm", "user:bob", nil) // (in memory: one party)
	p.Observe("i-crm", "apps/crm", "user:carol", nil)
	if got := others("i-crm", "carol"); !reflect.DeepEqual(got, []string{"bob"}) {
		t.Fatalf("after the transfer, for carol: %v", got)
	}
	// an org's tile handed to a person; a person's tile handed to the workspace
	p.Observe("i-org", "apps/org", "org:acme", nil)
	p.Observe("i-org", "apps/org", "user:dave", nil)
	if got := others("i-org", "dave"); !reflect.DeepEqual(got, []string{"org:acme"}) {
		t.Fatalf("an org's tile that became dave's: %v", got)
	}
	p.Observe("i-gift", "apps/gift", "user:erin", nil)
	p.Observe("i-gift", "apps/gift", "", nil) // now the workspace's: an admin's own — but erin could change it
	if got := others("i-gift", "root"); !reflect.DeepEqual(got, []string{"erin"}) {
		t.Fatalf("a person's tile that became the workspace's, for an admin: %v", got)
	}
	// a workspace tile a non-admin could edit for a while: the admins' exemption is gone
	p.Observe("i-ws", "apps/welcome", "", []string{"frank"})
	p.Observe("i-ws", "apps/welcome", "", nil)
	if got := others("i-ws", "root"); !reflect.DeepEqual(got, []string{"frank"}) {
		t.Fatalf("a workspace tile a non-admin could edit: %v", got)
	}
	// another identity at the same path knows nothing of it
	if got := others("i-hers-remade", "alice"); got != nil {
		t.Fatalf("a tile made again: %v", got)
	}

	// the file: xbind's own, 0600, keyed by identity, no tile content
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the record's file: %v %v", fi, err)
	}
	b, _ := os.ReadFile(path)
	var f pastFile
	if err := json.Unmarshal(b, &f); err != nil || f.Tiles["i-hers"] == nil || f.Tiles["i-hers"].Tile != "apps/hers" {
		t.Fatalf("the record: %s %v", b, err)
	}
	// an unreadable record starts again rather than stopping anything
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	p = NewPast(path, nil)
	if p.Recorded() != 0 {
		t.Fatal("an unreadable record was read")
	}
}

// covers D245 — the sticky condition in the rule, and what an answer means
// in its basis: a tile that is only the person's now but that someone else
// could change before needs their trust; an answer given for less than a
// trust means now (before D245: the sign-in alone) is void and asked again;
// a card drawn with the old meaning is refused as stale.
func TestTrustCoversThePastAndWhatItMeans(t *testing.T) {
	w := newWorld(t)
	own := Source{Tile: "apps/hers", ID: "i-hers", Owner: "user:alice", OwnerKind: "you"}
	if own.NeedsTrust() {
		t.Fatal("her own tile, never shared, needs trust")
	}
	past := own
	past.Past, past.PastUntil = []string{"mallory"}, w.now.UnixMilli()
	if !past.NeedsTrust() {
		t.Fatal("a tile mallory could change before needs no trust")
	}
	if past.Basis() == own.Basis() {
		t.Fatal("who could change it before is not part of the basis")
	}
	if w.s.Trusts("alice", past) {
		t.Fatal("trusted before she answered")
	}
	// the card she answers names mallory; an answer for the card without her is stale
	if _, err := w.s.TrustTile("alice", past, "card", own.Basis()); !errors.Is(err, ErrTrustChanged) {
		t.Fatalf("an answer to the card as it was before the record: %v", err)
	}
	tr, err := w.s.TrustTile("alice", past, "card", past.Basis())
	if err != nil || tr.Means != TrustMeans || !reflect.DeepEqual(tr.Past, []string{"mallory"}) {
		t.Fatalf("her answer: %+v %v", tr, err)
	}
	if !w.s.Trusts("alice", past) {
		t.Fatal("her answer does not hold")
	}
	// mallory gains write again: someone new — void, as ever
	again := own
	again.Editors = []string{"mallory"}
	if w.s.Trusts("alice", again) {
		t.Fatal("mallory back with write, and the old answer covers it")
	}

	// a card drawn by an xbind that asked for less: its basis is another
	old := "tb_" + strings.TrimPrefix(past.Basis(), "tb2_")
	if _, err := w.s.TrustTile("alice", past, "card", old); !errors.Is(err, ErrTrustChanged) {
		t.Fatalf("an answer to a card of before D245: %v", err)
	}
	// an answer kept from before D245 (no meaning recorded): void, with the reason — asked again
	src := bobs("carol")
	if _, err := w.s.TrustTile("alice", src, "card", src.Basis()); err != nil {
		t.Fatal(err)
	}
	p := w.s.path("alice")
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"means": 2`) {
		t.Fatalf("her file does not record what the answer means: %s", b)
	}
	stale := strings.Replace(string(b), `"means": 2,`, "", -1)
	if strings.Contains(stale, `"means"`) {
		t.Fatalf("could not make a pre-D245 file: %s", stale)
	}
	if err := os.WriteFile(p, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	if w.s.Trusts("alice", src) {
		t.Fatal("an answer given before a trust meant the home too still holds")
	}
	for _, got := range w.s.Trusted("alice") {
		if got.Tile == src.Tile && (!got.Void() || got.VoidWhy != VoidMeans) {
			t.Fatalf("the old answer: %+v", got)
		}
	}
	// answered again, with the words as they are now: it holds
	if _, err := w.s.TrustTile("alice", src, "card", src.Basis()); err != nil || !w.s.Trusts("alice", src) {
		t.Fatalf("answered again: %v", err)
	}
}
